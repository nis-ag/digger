package controllers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/diggerhq/digger/backend/models"
	"github.com/diggerhq/digger/backend/utils"
	"github.com/diggerhq/digger/libs/ci"
	"github.com/diggerhq/digger/libs/ci/github"
	"github.com/diggerhq/digger/libs/comment_utils/reporting"
	"github.com/diggerhq/digger/libs/digger_config"
	orchestrator_scheduler "github.com/diggerhq/digger/libs/scheduler"
	"github.com/diggerhq/digger/libs/spec"
	"github.com/google/uuid"
	"github.com/samber/lo"
)

func IsDriftStatusJob(job *models.DiggerJob) (bool, error) {
	if job == nil || job.Batch == nil {
		return false, nil
	}

	if job.Batch.BatchType != orchestrator_scheduler.DiggerCommandPlan || job.Batch.PrNumber != 0 {
		return false, nil
	}

	var vcsSpec spec.VcsSpec
	err := json.Unmarshal(job.SerializedVcsSpec, &vcsSpec)
	if err != nil {
		return false, err
	}

	return strings.EqualFold(vcsSpec.VcsType, "noop"), nil
}

func ProjectDriftStateMachineApply(project models.Project, tfplan string, resourcesCreated uint, resourcesUpdated uint, resourcesDeleted uint) error {
	isEmptyPlan := resourcesCreated == 0 && resourcesUpdated == 0 && resourcesDeleted == 0
	wasEmptyPlan := project.DriftToCreate == 0 && project.DriftToUpdate == 0 && project.DriftToDelete == 0
	if isEmptyPlan {
		project.DriftStatus = models.DriftStatusNoDrift
	}
	if !isEmptyPlan && wasEmptyPlan {
		project.DriftStatus = models.DriftStatusNewDrift
	}
	if !isEmptyPlan && !wasEmptyPlan {
		if project.DriftTerraformPlan != tfplan {
			if project.DriftStatus == models.DriftStatusAcknowledgeDrift {
				project.DriftStatus = models.DriftStatusNewDrift
			}
		}
	}

	project.DriftTerraformPlan = tfplan
	project.DriftToCreate = resourcesCreated
	project.DriftToUpdate = resourcesUpdated
	project.DriftToDelete = resourcesDeleted
	project.LatestDriftCheck = time.Now()
	result := models.DB.GormDB.Save(&project)
	if result.Error != nil {
		return result.Error
	}

	return nil
}

func GenerateChecksSummaryForBatch(batch *models.DiggerBatch) (string, error) {
	summaryEndpoint := os.Getenv("DIGGER_AI_SUMMARY_ENDPOINT")
	if summaryEndpoint == "" {
		slog.Error("DIGGER_AI_SUMMARY_ENDPOINT not set")
		return "", fmt.Errorf("could not generate AI summary, ai summary endpoint missing")
	}
	apiToken := os.Getenv("DIGGER_AI_SUMMARY_API_TOKEN")

	jobs, err := models.DB.GetDiggerJobsForBatch(batch.ID)
	if err != nil {
		slog.Error("Could not get jobs for batch",
			"batchId", batch.ID,
			"error", err,
		)

		return "", fmt.Errorf("could not get jobs for batch: %v", err)
	}

	terraformOutputs := ""
	for _, job := range jobs {
		var jobSpec orchestrator_scheduler.JobJson
		err := json.Unmarshal(job.SerializedJobSpec, &jobSpec)
		if err != nil {
			slog.Error("Could not unmarshal job spec",
				"jobId", job.DiggerJobID,
				"error", err,
			)

			return "", fmt.Errorf("could not summarize plans due to unmarshalling error: %v", err)
		}

		projectName := jobSpec.ProjectName
		slog.Debug("Adding Terraform output for project",
			"projectName", projectName,
			"jobId", job.DiggerJobID,
			"outputLength", len(job.TerraformOutput),
		)

		terraformOutputs += fmt.Sprintf("<PLAN_START>terraform output for %v: %v <PLAN_END>\n\n", projectName, job.TerraformOutput)
	}

	aiSummary, err := utils.GetAiSummaryFromTerraformPlans(terraformOutputs, summaryEndpoint, apiToken)
	if err != nil {
		slog.Error("Could not generate AI summary from Terraform outputs",
			"batchId", batch.ID,
			"error", err,
		)

		return "", fmt.Errorf("could not summarize terraform outputs: %v", err)
	}

	summary := ""
	if aiSummary != "FOUR_OH_FOUR" {
		summary = fmt.Sprintf(":sparkles: **AI summary (experimental):** %v", aiSummary)
	}

	return summary, nil
}

func GenerateChecksSummaryForJob(job *models.DiggerJob) (string, error) {
	batch := job.Batch
	summaryEndpoint := os.Getenv("DIGGER_AI_SUMMARY_ENDPOINT")
	if summaryEndpoint == "" {
		slog.Info("AI summary endpoint not configured, skipping", "batch", batch.ID, "jobId", job.ID, "DiggerJobId", job.DiggerJobID)
		return "", nil
	}
	apiToken := os.Getenv("DIGGER_AI_SUMMARY_API_TOKEN")

	if job.TerraformOutput == "" {
		slog.Warn("Terraform output not set yet, ignoring this call")
		return "", nil
	}
	terraformOutput := fmt.Sprintf("<PLAN_START>Terraform output for: %v<PLAN_END>\n\n", job.TerraformOutput)
	aiSummary, err := utils.GetAiSummaryFromTerraformPlans(terraformOutput, summaryEndpoint, apiToken)
	if err != nil {
		slog.Error("Could not generate AI summary from Terraform outputs",
			"batchId", batch.ID,
			"error", err,
		)

		return "", fmt.Errorf("could not summarize terraform outputs: %v", err)
	}

	summary := ""

	if job.WorkflowRunUrl != nil {
		summary += fmt.Sprintf(":link: <a href='%v'>CI job</a>\n\n", *job.WorkflowRunUrl)
	}

	if aiSummary != "FOUR_OH_FOUR" {
		summary += fmt.Sprintf(":sparkles: **AI summary (experimental):** %v", aiSummary)
	}

	return summary, nil
}

func UpdateCheckRunForBatch(gh utils.GithubClientProvider, batch *models.DiggerBatch, aiSummaryEnabled bool) error {
	slog.Info("Updating PR status for batch",
		"batchId", batch.ID,
		"prNumber", batch.PrNumber,
		"batchStatus", batch.Status,
		"batchType", batch.BatchType,
	)

	if batch.CheckRunId == nil {
		slog.Warn("Check Run update skipped - CheckRunId is nil",
			"batchId", batch.ID,
			"prNumber", batch.PrNumber,
			"batchStatus", batch.Status,
			"reason", "checkRunId_nil")
		return fmt.Errorf("error checking run id, found nil batch")
	}

	if batch.VCS != models.DiggerVCSGithub {
		slog.Warn("Check Run update skipped - non-GitHub VCS",
			"batchId", batch.ID,
			"vcs", batch.VCS,
			"prNumber", batch.PrNumber,
			"reason", "non_github_vcs")
		return fmt.Errorf("We only support github VCS for modern checks at the moment")
	}
	prService, err := utils.GetPrServiceFromBatch(batch, gh)
	if err != nil {
		slog.Warn("Check Run update failed - could not get PR service",
			"batchId", batch.ID,
			"prNumber", batch.PrNumber,
			"error", err,
			"errorType", fmt.Sprintf("%T", err),
			"reason", "pr_service_unavailable")
		return fmt.Errorf("error getting github service: %v", err)
	}

	ghPrService := prService.(*github.GithubService)
	diggerYmlString := batch.DiggerConfig
	diggerConfigYml, err := digger_config.LoadDiggerConfigYamlFromString(diggerYmlString)
	if err != nil {
		slog.Warn("Check Run update failed - could not load Digger config",
			"batchId", batch.ID,
			"prNumber", batch.PrNumber,
			"error", err,
			"errorType", fmt.Sprintf("%T", err),
			"reason", "config_load_failed")
		return fmt.Errorf("error loading digger config from batch: %v", err)
	}

	config, _, err := digger_config.ConvertDiggerYamlToConfig(diggerConfigYml)
	if err != nil {
		slog.Warn("Check Run update failed - could not convert Digger YAML to config",
			"batchId", batch.ID,
			"prNumber", batch.PrNumber,
			"error", err,
			"errorType", fmt.Sprintf("%T", err),
			"reason", "config_conversion_failed")
		return fmt.Errorf("error converting Digger YAML to config: %v", err)
	}

	disableDiggerApplyStatusCheck := config.DisableDiggerApplyStatusCheck

	isPlanBatch := batch.BatchType == orchestrator_scheduler.DiggerCommandPlan

	serializedBatch, err := batch.MapToJsonStruct()
	if err != nil {
		slog.Warn("Check Run update failed - could not serialize batch",
			"batchId", batch.ID,
			"prNumber", batch.PrNumber,
			"error", err,
			"errorType", fmt.Sprintf("%T", err),
			"reason", "batch_serialization_failed")
		return fmt.Errorf("error mapping batch to json struct: %v", err)
	}
	slog.Debug("Updating PR status for batch",
		"batchId", batch.ID, "prNumber", batch.PrNumber, "batchStatus", batch.Status, "batchType", batch.BatchType,
		"newStatus", serializedBatch.ToCheckRunStatus())

	jobs, err := models.DB.GetDiggerJobsForBatch(batch.ID)
	if err != nil {
		slog.Warn("Check Run update failed - could not get jobs for batch",
			"batchId", batch.ID,
			"prNumber", batch.PrNumber,
			"error", err,
			"errorType", fmt.Sprintf("%T", err),
			"reason", "jobs_fetch_failed")
		return fmt.Errorf("error getting jobs for batch: %v", err)
	}
	message, err := utils.GenerateRealtimeCommentMessage(jobs, batch.BatchType)
	if err != nil {
		slog.Warn("Check Run update failed - could not generate comment message",
			"batchId", batch.ID,
			"prNumber", batch.PrNumber,
			"error", err,
			"errorType", fmt.Sprintf("%T", err),
			"reason", "message_generation_failed")
		return fmt.Errorf("error generating realtime comment message: %v", err)
	}

	var summary = ""
	if aiSummaryEnabled && (batch.Status == orchestrator_scheduler.BatchJobSucceeded || batch.Status == orchestrator_scheduler.BatchJobFailed) {
		summary, err = GenerateChecksSummaryForBatch(batch)
		if err != nil {
			slog.Warn("Error generating checks summary for batch", "batchId", batch.ID, "error", err)
		}
	}

	if isPlanBatch {
		status := serializedBatch.ToCheckRunStatus()
		conclusion := serializedBatch.ToCheckRunConclusion()
		title := "Plans Summary"
		opts := github.GithubCheckRunUpdateOptions{
			&status,
			conclusion,
			&title,
			&summary,
			&message,
			utils.GetActionsForBatch(batch),
		}
		_, err = ghPrService.UpdateCheckRun(*batch.CheckRunId, opts)
		if err != nil {
			slog.Warn("GitHub Check Run API call failed for batch plan",
				"operation", "UpdateCheckRun",
				"batchId", batch.ID,
				"checkRunId", *batch.CheckRunId,
				"prNumber", batch.PrNumber,
				"batchStatus", batch.Status,
				"batchType", "plan",
				"error", err,
				"errorType", fmt.Sprintf("%T", err),
				"reason", "github_api_failed")
		} else {
			slog.Debug("Successfully updated Check Run for batch plan",
				"batchId", batch.ID,
				"checkRunId", *batch.CheckRunId,
				"status", status)
		}

		// Check if plan batch succeeded with zero changes and auto-succeed the apply check
		if batch.Status == orchestrator_scheduler.BatchJobSucceeded {
			allJobsHaveZeroChanges := true
			for _, job := range jobs {
				if job.DiggerJobSummary.ResourcesCreated > 0 ||
					job.DiggerJobSummary.ResourcesUpdated > 0 ||
					job.DiggerJobSummary.ResourcesDeleted > 0 {
					allJobsHaveZeroChanges = false
					break
				}
			}

			if allJobsHaveZeroChanges {
				slog.Info("Plan batch completed with zero changes - auto-succeeding apply check",
					"batchId", batch.ID,
					"prNumber", batch.PrNumber,
				)

				// Find and update the digger/apply check to success
				completedStatus := "completed"
				successConclusion := "success"
				applyTitle := "No changes to apply"
				applySummary := "All plan jobs completed with zero changes. The apply check has been automatically set to succeeded."
				applyMessage := "All terraform plans show no changes:\n\n" + message

				applyOpts := github.GithubCheckRunUpdateOptions{
					Status:     &completedStatus,
					Conclusion: &successConclusion,
					Title:      &applyTitle,
					Summary:    &applySummary,
					Text:       &applyMessage,
					Actions:    nil, // No actions needed since there's nothing to apply
				}

				// Get the digger/apply check run for this commit and update it
				// We need to find it by name since we don't store its ID
				checkRuns, err := ghPrService.GetCheckRunsForCommit(batch.CommitSha)
				if err != nil {
					slog.Warn("Failed to get check runs for commit to update apply check",
						"batchId", batch.ID,
						"commitSha", batch.CommitSha,
						"error", err,
					)
				} else {
					for _, checkRun := range checkRuns {
						if checkRun.GetName() == "digger/apply" {
							_, err = ghPrService.UpdateCheckRun(fmt.Sprintf("%d", checkRun.GetID()), applyOpts)
							if err != nil {
								slog.Warn("Failed to auto-succeed apply check for zero-change plan",
									"batchId", batch.ID,
									"checkRunId", checkRun.GetID(),
									"error", err,
								)
							} else {
								slog.Info("Successfully auto-succeeded apply check for zero-change plan",
									"batchId", batch.ID,
									"checkRunId", checkRun.GetID(),
								)
							}
							break
						}
					}
				}
			}
		}
	} else {
		if disableDiggerApplyStatusCheck == false {
			status := serializedBatch.ToCheckRunStatus()
			conclusion := serializedBatch.ToCheckRunConclusion()
			title := "Apply Summary"
			opts := github.GithubCheckRunUpdateOptions{
				&status,
				conclusion,
				&title,
				&summary,
				&message,
				utils.GetActionsForBatch(batch),
			}
			_, err = ghPrService.UpdateCheckRun(*batch.CheckRunId, opts)
			if err != nil {
				slog.Warn("GitHub Check Run API call failed for batch apply",
					"operation", "UpdateCheckRun",
					"batchId", batch.ID,
					"checkRunId", *batch.CheckRunId,
					"prNumber", batch.PrNumber,
					"batchStatus", batch.Status,
					"batchType", "apply",
					"error", err,
					"errorType", fmt.Sprintf("%T", err),
					"reason", "github_api_failed")
			} else {
				slog.Debug("Successfully updated Check Run for batch apply",
					"batchId", batch.ID,
					"checkRunId", *batch.CheckRunId,
					"status", status)
			}
		}
	}
	return nil
}

// more modern check runs on github have their own page
func UpdateCheckRunForJob(gh utils.GithubClientProvider, job *models.DiggerJob, aiSummaryEnabled bool) error {
	batch := job.Batch
	slog.Info("Updating PR Check run for job",
		"jobId", job.DiggerJobID,
		"prNumber", batch.PrNumber,
		"jobStatus", job.Status,
		"batchType", batch.BatchType,
	)

	if batch.VCS != models.DiggerVCSGithub {
		slog.Warn("Check Run update skipped for job - non-GitHub VCS",
			"jobId", job.DiggerJobID,
			"batchId", batch.ID,
			"vcs", batch.VCS,
			"prNumber", batch.PrNumber,
			"reason", "non_github_vcs")
		return fmt.Errorf("Error updating PR status for job only github is supported")
	}

	if job.CheckRunId == nil {
		slog.Warn("Check Run update skipped for job - CheckRunId is nil",
			"jobId", job.DiggerJobID,
			"batchId", batch.ID,
			"prNumber", batch.PrNumber,
			"jobStatus", job.Status,
			"reason", "checkRunId_nil")
		return fmt.Errorf("Error updating PR status, could not find checkRunId in job")
	}

	prService, err := utils.GetPrServiceFromBatch(batch, gh)
	ghService := prService.(*github.GithubService)

	if err != nil {
		slog.Warn("Check Run update failed for job - could not get PR service",
			"jobId", job.DiggerJobID,
			"batchId", batch.ID,
			"prNumber", batch.PrNumber,
			"error", err,
			"errorType", fmt.Sprintf("%T", err),
			"reason", "pr_service_unavailable")
		return fmt.Errorf("error getting github service: %v", err)
	}

	var jobSpec orchestrator_scheduler.JobJson
	err = json.Unmarshal([]byte(job.SerializedJobSpec), &jobSpec)
	if err != nil {
		slog.Warn("Check Run update failed for job - could not unmarshal job spec",
			"jobId", job.DiggerJobID,
			"batchId", batch.ID,
			"error", err,
			"errorType", fmt.Sprintf("%T", err),
			"reason", "job_spec_unmarshal_failed")
		return fmt.Errorf("could not unmarshal json string: %v", err)
	}

	isPlan := jobSpec.IsPlan()
	status, err := models.GetCheckRunStatusForJob(job)
	if err != nil {
		slog.Warn("Failed to get check run status for job",
			"jobId", job.DiggerJobID,
			"jobStatus", job.Status,
			"error", err)
		return fmt.Errorf("could not get status check for job: %v", err)
	}

	conclusion, err := models.GetCheckRunConclusionForJob(job)
	if err != nil {
		slog.Warn("Failed to get check run conclusion for job",
			"jobId", job.DiggerJobID,
			"jobStatus", job.Status,
			"error", err)
		return fmt.Errorf("could not get conclusion for job: %v", err)
	}

	// Validate status and conclusion before sending to GitHub
	validStatuses := map[string]bool{"queued": true, "in_progress": true, "completed": true}
	validConclusions := map[string]bool{"": true, "success": true, "failure": true, "neutral": true, "cancelled": true, "timed_out": true, "action_required": true, "skipped": true}

	if !validStatuses[status] {
		slog.Warn("Invalid Check Run status detected",
			"jobId", job.DiggerJobID,
			"jobStatus", job.Status,
			"checkRunStatus", status,
			"validStatuses", []string{"queued", "in_progress", "completed"})
	}

	if !validConclusions[conclusion] {
		slog.Warn("Invalid Check Run conclusion detected",
			"jobId", job.DiggerJobID,
			"jobStatus", job.Status,
			"checkRunConclusion", conclusion,
			"validConclusions", []string{"", "success", "failure", "neutral", "cancelled", "timed_out", "action_required", "skipped"})
	}

	slog.Debug("Preparing to update Check Run for job",
		"jobId", job.DiggerJobID,
		"jobStatus", job.Status,
		"checkRunStatus", status,
		"checkRunConclusion", conclusion,
		"isPlan", isPlan)

	// Only pass conclusion to GitHub API when job is completed (non-empty conclusion).
	// GitHub rejects empty string as conclusion - it must be omitted for in-progress jobs.
	var conclusionPtr *string
	if conclusion != "" {
		conclusionPtr = &conclusion
	}

	// Character limit check - GitHub check run text field has a 65535 character limit
	const maxCheckRunTextLength = 65535
	cutOffMsg := "\n[Character limit exceeded, output truncated]"
	if utf8.RuneCountInString(job.TerraformOutput) > maxCheckRunTextLength {
		runes := []rune(job.TerraformOutput)
		truncateAt := maxCheckRunTextLength - utf8.RuneCountInString(cutOffMsg)
		job.TerraformOutput = string(runes[:truncateAt]) + cutOffMsg
	}

	text := "" +
		"```terraform\n" +
		job.TerraformOutput +
		"```\n"

	var summary = ""
	if aiSummaryEnabled && (job.Status == orchestrator_scheduler.DiggerJobSucceeded || job.Status == orchestrator_scheduler.DiggerJobFailed) {
		summary, err = GenerateChecksSummaryForJob(job)
		if err != nil {
			slog.Warn("Error generating checks summary for job", "jobId", job.DiggerJobID, "batchId", batch.ID, "error", err)
		}
	}

	slog.Debug("Updating PR status for job", "jobId", job.DiggerJobID, "status", status, "conclusion", conclusion)
	if isPlan {
		title := fmt.Sprintf("%v to create %v to update %v to delete", job.DiggerJobSummary.ResourcesCreated, job.DiggerJobSummary.ResourcesUpdated, job.DiggerJobSummary.ResourcesDeleted)
		opts := github.GithubCheckRunUpdateOptions{
			Status:     &status,
			Conclusion: conclusionPtr,
			Title:      &title,
			Summary:    &summary,
			Text:       &text,
			Actions:    utils.GetActionsForJob(job),
		}
		_, err = ghService.UpdateCheckRun(*job.CheckRunId, opts)
		if err != nil {
			slog.Warn("GitHub Check Run API call failed for job plan",
				"operation", "UpdateCheckRun",
				"jobId", job.DiggerJobID,
				"checkRunId", *job.CheckRunId,
				"batchId", batch.ID,
				"prNumber", batch.PrNumber,
				"jobStatus", job.Status,
				"jobType", "plan",
				"error", err,
				"errorType", fmt.Sprintf("%T", err),
				"reason", "github_api_failed")
		} else {
			slog.Debug("Successfully updated Check Run for job plan",
				"jobId", job.DiggerJobID,
				"checkRunId", *job.CheckRunId,
				"status", status)
		}
	} else {
		title := fmt.Sprintf("%v created %v updated %v deleted", job.DiggerJobSummary.ResourcesCreated, job.DiggerJobSummary.ResourcesUpdated, job.DiggerJobSummary.ResourcesDeleted)
		opts := github.GithubCheckRunUpdateOptions{
			Status:     &status,
			Conclusion: conclusionPtr,
			Title:      &title,
			Summary:    &summary,
			Text:       &text,
			Actions:    utils.GetActionsForJob(job),
		}
		_, err = ghService.UpdateCheckRun(*job.CheckRunId, opts)
		if err != nil {
			slog.Warn("GitHub Check Run API call failed for job apply",
				"operation", "UpdateCheckRun",
				"jobId", job.DiggerJobID,
				"checkRunId", *job.CheckRunId,
				"batchId", batch.ID,
				"prNumber", batch.PrNumber,
				"jobStatus", job.Status,
				"jobType", "apply",
				"error", err,
				"errorType", fmt.Sprintf("%T", err),
				"reason", "github_api_failed")
		} else {
			slog.Debug("Successfully updated Check Run for job apply",
				"jobId", job.DiggerJobID,
				"checkRunId", *job.CheckRunId,
				"status", status)
		}
	}
	return nil
}

// reporterTypeForConfig decides whether the runner posts its own comments. accumulate_plans silences
// it for a plan because the backend renders every plan into the group comments instead; any other
// command has nothing accumulating it and keeps reporting for itself.
func reporterTypeForConfig(config *digger_config.DiggerConfig, command orchestrator_scheduler.DiggerCommand) string {
	if !config.Reporting.CommentsEnabled {
		return "noop"
	}
	if config.CommentRenderMode == digger_config.CommentRenderModeAccumulatePlans &&
		command == orchestrator_scheduler.DiggerCommandPlan {
		return "noop"
	}
	return "lazy"
}

// CreatePlanCommentGroupsForBatch posts one placeholder comment per group before any job runs, so
// no runner ever has to create a comment and there is no creation race. It is a no-op unless the
// batch is a plan whose config asks for accumulate_plans and allows comments at all.
func CreatePlanCommentGroupsForBatch(prService ci.PullRequestService, batch *models.DiggerBatch, config *digger_config.DiggerConfig, projectNames []string) error {
	if !config.Reporting.CommentsEnabled ||
		config.CommentRenderMode != digger_config.CommentRenderModeAccumulatePlans ||
		batch.BatchType != orchestrator_scheduler.DiggerCommandPlan {
		return nil
	}

	// lo.Chunk panics on a non-positive size, so refuse it here rather than take the handler down.
	// ValidateDiggerConfig rejects it too, but not every path that builds a DiggerConfig runs the
	// validator.
	maxPerComment := config.Reporting.MaxPlansPerComment
	if maxPerComment < 1 {
		return fmt.Errorf("reporting.max_plans_per_comment must be at least 1, got %v", maxPerComment)
	}

	// A second pass over the same batch must reuse the comments the first pass posted, otherwise the
	// PR keeps a second set that nothing edits or deletes. Note this cannot dedupe a redelivered
	// webhook: CreateDiggerBatch mints a fresh batch id per delivery, so a redelivery arrives here
	// with an empty group set and does post a second set of comments.
	existing, err := models.DB.GetPlanCommentGroupsForBatch(batch.ID)
	if err != nil {
		return fmt.Errorf("could not read the existing plan comment groups: %v", err)
	}
	alreadyPosted := make(map[int]bool, len(existing))
	for _, group := range existing {
		alreadyPosted[group.GroupIndex] = true
	}

	// Sorting is what makes group membership deterministic: the caller's project names come from a
	// map, whose iteration order is not.
	sorted := slices.Clone(projectNames)
	slices.Sort(sorted)

	for groupIndex, group := range lo.Chunk(sorted, maxPerComment) {
		if alreadyPosted[groupIndex] {
			continue
		}

		plans := make([]reporting.AccumulatedPlan, 0, len(group))
		for _, projectName := range group {
			plans = append(plans, reporting.AccumulatedPlan{
				DisplayName: projectName,
				Status:      orchestrator_scheduler.DiggerJobCreated,
			})
		}

		// Chunks are uniform, so the group's first project sits at groupIndex * maxPerComment.
		header := reporting.PlanGroupHeader(groupIndex*maxPerComment, len(group), len(sorted))
		comment, err := prService.PublishComment(batch.PrNumber, reporting.RenderAccumulatedPlans(header, plans))
		if err != nil {
			return fmt.Errorf("could not publish plan comment group %v: %v", groupIndex, err)
		}
		// Not every PullRequestService returns the comment it created, and this one's id is the only
		// handle later renders have.
		if comment == nil {
			return fmt.Errorf("plan comment group %v was published without a comment id", groupIndex)
		}

		if _, err := models.DB.CreatePlanCommentGroup(batch.ID, groupIndex, comment.Id, group); err != nil {
			return fmt.Errorf("could not persist plan comment group %v: %v", groupIndex, err)
		}
	}

	slog.Info("created plan comment groups for batch",
		"batchId", batch.ID,
		"projectCount", len(sorted),
		"maxPlansPerComment", maxPerComment)
	return nil
}

// planCommentGroupLocks serialises the renders of one group. The counter in the group's row cannot
// do it alone: it is only written once the edit has landed, so two overlapping renders would both
// pass its guard and could then reach the VCS in either order. Striped rather than one lock per
// group, so the table cannot grow with every group the process ever renders; sharing a lock only
// costs two unrelated groups a serialised edit. A second backend replica falls back to the counter.
var planCommentGroupLocks [64]sync.Mutex

func lockPlanCommentGroup(groupId uint) func() {
	lock := &planCommentGroupLocks[groupId%uint(len(planCommentGroupLocks))]
	lock.Lock()
	return lock.Unlock
}

// RenderPlanCommentGroup rebuilds one group's comment from state the caller has already loaded, so
// rendering every group of a batch reads the batch's groups and jobs once rather than once per group.
// force skips the check against the last claimed render and is used by the batch-terminal render,
// which is authoritative.
func RenderPlanCommentGroup(prService ci.PullRequestService, batch *models.DiggerBatch, groups []models.DiggerPlanCommentGroup, jobs []models.DiggerJob, groupIndex int, force bool) error {
	target, projectNames, offset, total, err := planGroupSlice(groups, groupIndex)
	if err != nil {
		return err
	}

	unlock := lockPlanCommentGroup(target.ID)
	defer unlock()

	jobsByProject := make(map[string]models.DiggerJob, len(jobs))
	for _, job := range jobs {
		// A project with several impacted parents gets one job row per parent edge, and the batch's
		// jobs come back unordered, so pick deterministically and prefer a row that has finished:
		// otherwise a completed plan can be hidden by a sibling row that is still pending, which also
		// walks terminalCount backwards and lets the claim refuse the next legitimate render.
		if existing, ok := jobsByProject[job.ProjectName]; ok && !planJobSupersedes(job, existing) {
			continue
		}
		jobsByProject[job.ProjectName] = job
	}

	plans := make([]reporting.AccumulatedPlan, 0, len(projectNames))
	terminalCount := 0
	for _, projectName := range projectNames {
		job, hasJob := jobsByProject[projectName]
		if !hasJob {
			plans = append(plans, reporting.AccumulatedPlan{
				DisplayName: projectName,
				Status:      orchestrator_scheduler.DiggerJobCreated,
			})
			continue
		}

		if planJobIsTerminal(job) {
			terminalCount++
		}
		plans = append(plans, reporting.AccumulatedPlan{
			DisplayName: planDisplayName(job),
			Status:      job.Status,
			Output:      job.TerraformOutput,
		})
	}

	// Claim before editing, not after. A guard written only once the edit has landed cannot refuse
	// anything, so it could not order two renders of one group driven by different backend replicas -
	// which the in-process lock above cannot do either.
	previousCount := target.RenderedJobCount
	claimed, err := models.DB.ClaimPlanCommentGroupRender(target.ID, terminalCount, force)
	if err != nil {
		return fmt.Errorf("could not claim the render of plan comment group %v: %v", groupIndex, err)
	}
	if !claimed {
		slog.Info("skipping plan comment group render, a render covering at least as many jobs was already claimed",
			"batchId", batch.ID,
			"groupIndex", groupIndex,
			"terminalCount", terminalCount)
		return nil
	}

	body := reporting.RenderAccumulatedPlans(reporting.PlanGroupHeader(offset, len(projectNames), total), plans)
	if err := prService.EditComment(batch.PrNumber, target.CommentId, body); err != nil {
		// The claim describes a body that never reached the VCS. Hand it back, so a retry of the same
		// state is not refused as redundant.
		if _, releaseErr := models.DB.ClaimPlanCommentGroupRender(target.ID, previousCount, true); releaseErr != nil {
			slog.Warn("Could not release the claim of a plan comment group whose edit failed",
				"batchId", batch.ID, "groupIndex", groupIndex, "error", releaseErr)
		}
		return fmt.Errorf("could not edit plan comment group %v: %v", groupIndex, err)
	}
	return nil
}

// RenderAllPlanCommentGroups re-renders every group of a batch. It does not stop at the first group
// it cannot render: this is the authoritative pass, and the groups after a transient failure need it
// just as much as the ones before it.
func RenderAllPlanCommentGroups(prService ci.PullRequestService, batch *models.DiggerBatch) error {
	groups, err := models.DB.GetPlanCommentGroupsForBatch(batch.ID)
	if err != nil {
		return fmt.Errorf("could not get plan comment groups: %v", err)
	}
	jobs, err := models.DB.GetDiggerJobsForBatch(batch.ID)
	if err != nil {
		return fmt.Errorf("could not get jobs for batch: %v", err)
	}

	var errs []error
	for _, group := range groups {
		if err := RenderPlanCommentGroup(prService, batch, groups, jobs, group.GroupIndex, true); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// planGroupIndexForProject finds the group holding projectName among the groups the caller has already
// loaded. A batch has a handful of groups, so decoding their project lists beats a join table.
func planGroupIndexForProject(groups []models.DiggerPlanCommentGroup, projectName string) (int, error) {
	for _, group := range groups {
		var projects []string
		if err := json.Unmarshal(group.Projects, &projects); err != nil {
			return 0, fmt.Errorf("could not deserialize project names of group %v: %v", group.GroupIndex, err)
		}
		if slices.Contains(projects, projectName) {
			return group.GroupIndex, nil
		}
	}
	return 0, fmt.Errorf("no plan comment group holds project %v", projectName)
}

// planGroupSlice locates a group within its batch in one pass, returning its row and project list
// alongside the offset and total its header must name, so no group's project list is decoded twice.
func planGroupSlice(groups []models.DiggerPlanCommentGroup, groupIndex int) (target *models.DiggerPlanCommentGroup, projectNames []string, offset int, total int, err error) {
	for i := range groups {
		var names []string
		if err := json.Unmarshal(groups[i].Projects, &names); err != nil {
			return nil, nil, 0, 0, fmt.Errorf("could not deserialize project names of group %v: %v", groups[i].GroupIndex, err)
		}
		if groups[i].GroupIndex < groupIndex {
			offset += len(names)
		}
		if groups[i].GroupIndex == groupIndex {
			target, projectNames = &groups[i], names
		}
		total += len(names)
	}
	if target == nil {
		return nil, nil, 0, 0, fmt.Errorf("group %v is not part of its batch", groupIndex)
	}
	return target, projectNames, offset, total, nil
}

func planJobIsTerminal(job models.DiggerJob) bool {
	return job.Status == orchestrator_scheduler.DiggerJobSucceeded || job.Status == orchestrator_scheduler.DiggerJobFailed
}

// planJobSupersedes orders two job rows of the same project: a finished row beats an unfinished one,
// and the newer row breaks the tie.
func planJobSupersedes(candidate models.DiggerJob, current models.DiggerJob) bool {
	if planJobIsTerminal(candidate) != planJobIsTerminal(current) {
		return planJobIsTerminal(candidate)
	}
	return candidate.ID > current.ID
}

// planDisplayName prefers the alias a reviewer recognises, falling back to the project name when the
// job spec cannot be read. GetProjectAlias already falls back to the spec's project name.
func planDisplayName(job models.DiggerJob) string {
	serialized, err := job.MapToJsonStruct()
	if err != nil {
		return job.ProjectName
	}
	return orchestrator_scheduler.GetProjectAlias(serialized)
}

// allBatchJobsTerminal reports whether the batch can still make progress, which is what decides
// whether the authoritative final render may run. batch.Status cannot answer it: UpdateBatchStatus
// only ever marks a batch succeeded, so a batch holding one failed plan stays non-terminal forever.
// "Every job has finished" cannot answer it either: services.DiggerJobCompleted schedules a child
// only once every parent has succeeded, so the children of a failed plan sit in DiggerJobCreated for
// good and a strict test would never let the final render run for them.
func allBatchJobsTerminal(batchId uuid.UUID) (bool, error) {
	jobs, err := models.DB.GetDiggerJobsForBatch(batchId)
	if err != nil {
		return false, fmt.Errorf("could not get jobs for batch: %v", err)
	}
	return batchJobsFinished(jobs), nil
}

func batchJobsFinished(jobs []models.DiggerJob) bool {
	anyFailed, anyRunning, anyUnscheduled := false, false, false
	for _, job := range jobs {
		switch job.Status {
		case orchestrator_scheduler.DiggerJobSucceeded:
		case orchestrator_scheduler.DiggerJobFailed:
			anyFailed = true
		case orchestrator_scheduler.DiggerJobTriggered, orchestrator_scheduler.DiggerJobStarted, orchestrator_scheduler.DiggerJobQueuedForRun:
			anyRunning = true
		default:
			anyUnscheduled = true
		}
	}
	if anyRunning {
		return false
	}
	return !anyUnscheduled || anyFailed
}

// commentRenderModeForBatch reads the render mode out of the config the batch was created with, so a
// config change mid-flight cannot leave a batch half rendered one way and half the other.
func commentRenderModeForBatch(batch *models.DiggerBatch) string {
	configYml, err := digger_config.LoadDiggerConfigYamlFromString(batch.DiggerConfig)
	if err != nil {
		slog.Warn("Could not load the digger config of a batch, assuming the default render mode",
			"batchId", batch.ID, "error", err)
		return digger_config.CommentRenderModeBasic
	}
	if configYml.CommentRenderMode == nil {
		return digger_config.CommentRenderModeBasic
	}
	return *configYml.CommentRenderMode
}

// renderPlanCommentGroupsForJob refreshes the group comment a finished job belongs to, then
// re-renders every group once the whole batch has finished. The batch-terminal pass is the backstop:
// intermediate renders can be refused by the monotonic claim, so only a forced pass guarantees a
// complete final body.
func renderPlanCommentGroupsForJob(gh utils.GithubClientProvider, batch *models.DiggerBatch, job *models.DiggerJob) {
	if batch.BatchType != orchestrator_scheduler.DiggerCommandPlan ||
		commentRenderModeForBatch(batch) != digger_config.CommentRenderModeAccumulatePlans {
		return
	}

	// Look the groups up before building a PR service: getting one mints a GitHub App installation
	// token. A batch can legitimately have no groups - comments_enabled off, a VCS this feature is not
	// wired into, or a batch created before it shipped - and none of those should pay for a token or
	// log a warning on every job status update.
	groups, err := models.DB.GetPlanCommentGroupsForBatch(batch.ID)
	if err != nil {
		slog.Warn("Could not get plan comment groups", "batchId", batch.ID, "error", err)
		return
	}
	if len(groups) == 0 {
		return
	}

	prService, err := utils.GetPrServiceFromBatch(batch, gh)
	if err != nil {
		slog.Warn("Could not get PR service to render plan comment groups", "batchId", batch.ID, "error", err)
		return
	}

	// Read the jobs after the PR service, not before: minting the installation token takes a round trip,
	// and every job that finishes during it would otherwise be missing from this render.
	jobs, err := models.DB.GetDiggerJobsForBatch(batch.ID)
	if err != nil {
		slog.Warn("Could not get jobs for batch", "batchId", batch.ID, "error", err)
		return
	}

	groupIndex, err := planGroupIndexForProject(groups, job.ProjectName)
	if err != nil {
		slog.Warn("Could not find plan comment group for project",
			"batchId", batch.ID, "projectName", job.ProjectName, "error", err)
	} else if err := RenderPlanCommentGroup(prService, batch, groups, jobs, groupIndex, false); err != nil {
		slog.Warn("Could not render plan comment group",
			"batchId", batch.ID, "groupIndex", groupIndex, "error", err)
	}

	// Deliberately a fresh read rather than the snapshot above: the render just spent a round trip on
	// the VCS, and a sibling job that finished during it must count here. Whichever handler sees the
	// last job is the only one that runs the authoritative pass, so a stale snapshot can skip it
	// entirely.
	batchFinished, err := allBatchJobsTerminal(batch.ID)
	if err != nil {
		slog.Warn("Could not tell whether the batch has finished", "batchId", batch.ID, "error", err)
		return
	}
	if !batchFinished {
		return
	}

	if err := RenderAllPlanCommentGroups(prService, batch); err != nil {
		slog.Warn("Could not re-render plan comment groups at batch completion",
			"batchId", batch.ID, "error", err)
	}
}

// deletePlanCommentGroupsForBatch removes the group comments a previous plan batch rendered, so
// delete_prior_comments keeps the PR from growing by one comment per group per run. It reports
// whether every comment went, which is what the caller warns about.
func deletePlanCommentGroupsForBatch(prService ci.PullRequestService, batch *models.DiggerBatch) bool {
	groups, err := models.DB.GetPlanCommentGroupsForBatch(batch.ID)
	if err != nil {
		slog.Warn("Could not get plan comment groups for batch", "batchId", batch.ID, "error", err)
		return false
	}

	allDeleted := true
	for _, group := range groups {
		slog.Debug("Deleting plan comment group", "batchId", batch.ID, "commentID", group.CommentId)
		if err := prService.DeleteComment(group.CommentId); err != nil {
			slog.Warn("Could not delete plan comment group",
				"batchId", batch.ID, "commentID", group.CommentId, "error", err)
			allDeleted = false
			continue
		}
		// Drop the row along with the comment. Every later plan batch of this PR walks the same prior
		// batches, so a row left behind means re-deleting a comment that is already gone, a 404, and a
		// "some of the previous comments failed to delete" warning on every run from here on.
		if err := models.DB.DeletePlanCommentGroup(group.ID); err != nil {
			slog.Warn("Could not delete plan comment group row",
				"batchId", batch.ID, "groupId", group.ID, "error", err)
		}
	}
	return allDeleted
}
