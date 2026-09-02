package controllers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/diggerhq/digger/backend/models"
	"github.com/diggerhq/digger/backend/utils"
	"github.com/diggerhq/digger/libs/ci"
	github_ci "github.com/diggerhq/digger/libs/ci/github"
	"github.com/diggerhq/digger/libs/comment_utils/reporting"
	"github.com/diggerhq/digger/libs/digger_config"
	orchestrator_scheduler "github.com/diggerhq/digger/libs/scheduler"
	"github.com/google/go-github/v61/github"
	"github.com/google/uuid"
	"github.com/migueleliasweb/go-github-mock/src/mock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAutomergeWhenBatchIsSuccessfulStatus(t *testing.T) {
	teardownSuite, _ := setupSuite(t)
	defer teardownSuite(t)
	isMergeCalled := false
	mockedHTTPClient := mock.NewMockedHTTPClient(
		mock.WithRequestMatch(
			mock.GetReposIssuesByOwnerByRepoByIssueNumber,
			github.Issue{
				Number:           github.Int(2),
				PullRequestLinks: &github.PullRequestLinks{},
			}),
		mock.WithRequestMatch(
			mock.GetReposPullsByOwnerByRepoByPullNumber,
			github.PullRequest{
				Number: github.Int(2),
				Head:   &github.PullRequestBranch{Ref: github.String("main")},
			}),
		mock.WithRequestMatch(
			mock.GetReposByOwnerByRepo,
			github.Repository{
				Name:          github.String("testRepo"),
				DefaultBranch: github.String("main"),
			}),
		mock.WithRequestMatch(
			mock.GetReposGitRefByOwnerByRepoByRef,
			github.Reference{Object: &github.GitObject{SHA: github.String("test")}, Ref: github.String("test_ref")},
		),
		mock.WithRequestMatch(
			mock.PostReposGitRefsByOwnerByRepo,
			github.Reference{Object: &github.GitObject{SHA: github.String("test")}},
		),
		mock.WithRequestMatch(
			mock.GetReposContentsByOwnerByRepoByPath,
			github.RepositoryContent{},
		),
		mock.WithRequestMatchHandler(
			mock.PutReposPullsMergeByOwnerByRepoByPullNumber,
			http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				isMergeCalled = true
			}),
		),
	)
	gh := &utils.DiggerGithubClientMockProvider{}
	gh.MockedHTTPClient = mockedHTTPClient

	batch := models.DiggerBatch{
		VCS:        "github",
		ID:         uuid.UUID{},
		PrNumber:   2,
		Status:     orchestrator_scheduler.BatchJobSucceeded,
		BranchName: "main",
		DiggerConfig: "" +
			"projects:\n" +
			"  - name: dev\n" +
			"    dir: dev\n" +
			"auto_merge: false",
		GithubInstallationId:     int64(41584295),
		RepoFullName:             "diggerhq/github-job-scheduler",
		RepoOwner:                "diggerhq",
		RepoName:                 "github-job-scheduler",
		BatchType:                orchestrator_scheduler.DiggerCommandApply,
		CoverAllImpactedProjects: true,
	}
	err := AutomergePRforBatchIfEnabled(gh, &batch)
	assert.NoError(t, err)
	assert.False(t, isMergeCalled)

	batch.DiggerConfig = "" +
		"projects:\n" +
		"  - name: dev\n" +
		"    dir: dev\n" +
		"auto_merge: true"
	batch.BatchType = orchestrator_scheduler.DiggerCommandPlan
	err = AutomergePRforBatchIfEnabled(gh, &batch)
	assert.NoError(t, err)
	assert.False(t, isMergeCalled)

	batch.DiggerConfig = "" +
		"projects:\n" +
		"  - name: dev\n" +
		"    dir: dev\n" +
		"auto_merge: true"
	batch.BatchType = orchestrator_scheduler.DiggerCommandApply
	batch.CoverAllImpactedProjects = false
	err = AutomergePRforBatchIfEnabled(gh, &batch)
	assert.NoError(t, err)
	assert.False(t, isMergeCalled)

	batch.DiggerConfig = "" +
		"projects:\n" +
		"  - name: dev\n" +
		"    dir: dev\n" +
		"auto_merge: true"
	batch.BatchType = orchestrator_scheduler.DiggerCommandApply
	batch.CoverAllImpactedProjects = true
	err = AutomergePRforBatchIfEnabled(gh, &batch)
	assert.NoError(t, err)
	assert.True(t, isMergeCalled)
}

func TestCharacterLimit(t *testing.T) {
	tests := []struct {
		name           string
		inputLength    int
		expectTruncate bool
	}{
		{
			name:           "under limit - no truncation",
			inputLength:    1000,
			expectTruncate: false,
		},
		{
			name:           "at limit - no truncation",
			inputLength:    65535,
			expectTruncate: false,
		},
		{
			name:           "over limit - truncation applied",
			inputLength:    70000,
			expectTruncate: true,
		},
	}

	const maxCheckRunTextLength = 65535
	cutOffMsg := "\n[Character limit exceeded, output truncated]"

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := strings.Repeat("a", tt.inputLength)

			result := input
			if utf8.RuneCountInString(result) > maxCheckRunTextLength {
				runes := []rune(result)
				truncateAt := maxCheckRunTextLength - utf8.RuneCountInString(cutOffMsg)
				result = string(runes[:truncateAt]) + cutOffMsg
			}

			if tt.expectTruncate {
				assert.Equal(t, maxCheckRunTextLength, utf8.RuneCountInString(result),
					"truncated output should be exactly 65535 characters")
				assert.True(t, strings.HasSuffix(result, cutOffMsg),
					"truncated output should end with cutoff message")
			} else {
				assert.Equal(t, tt.inputLength, utf8.RuneCountInString(result),
					"non-truncated output should maintain original length")
				assert.False(t, strings.HasSuffix(result, cutOffMsg),
					"non-truncated output should not have cutoff message")
			}
		})
	}
}

func TestReporterTypeForConfig(t *testing.T) {
	const accumulate = "comment_render_mode: accumulate_plans\nprojects:\n- name: dev\n  dir: .\n"
	const commentsOff = "reporting:\n  comments_enabled: false\nprojects:\n- name: dev\n  dir: .\n"

	tests := []struct {
		name      string
		diggerCfg string
		command   orchestrator_scheduler.DiggerCommand
		want      string
	}{
		{
			name:      "the default posts plan comments from the runner",
			diggerCfg: "projects:\n- name: dev\n  dir: .\n",
			command:   orchestrator_scheduler.DiggerCommandPlan,
			want:      "lazy",
		},
		{
			name:      "comments disabled silences the runner",
			diggerCfg: commentsOff,
			command:   orchestrator_scheduler.DiggerCommandPlan,
			want:      "noop",
		},
		{
			name:      "comments disabled silences an apply too",
			diggerCfg: commentsOff,
			command:   orchestrator_scheduler.DiggerCommandApply,
			want:      "noop",
		},
		{
			name:      "accumulate_plans silences the runner on plan, the backend renders instead",
			diggerCfg: accumulate,
			command:   orchestrator_scheduler.DiggerCommandPlan,
			want:      "noop",
		},
		{
			name:      "accumulate_plans leaves an apply to the runner, nothing accumulates that",
			diggerCfg: accumulate,
			command:   orchestrator_scheduler.DiggerCommandApply,
			want:      "lazy",
		},
		{
			name:      "both at once is still noop",
			diggerCfg: "comment_render_mode: accumulate_plans\nreporting:\n  comments_enabled: false\nprojects:\n- name: dev\n  dir: .\n",
			command:   orchestrator_scheduler.DiggerCommandPlan,
			want:      "noop",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config, _, _, err := digger_config.LoadDiggerConfigFromString(tt.diggerCfg, "./")
			require.NoError(t, err)
			assert.Equal(t, tt.want, reporterTypeForConfig(config, tt.command))
		})
	}
}

func loadTestConfig(t *testing.T, diggerCfg string) *digger_config.DiggerConfig {
	t.Helper()
	config, _, _, err := digger_config.LoadDiggerConfigFromString(diggerCfg, "./")
	require.NoError(t, err)
	return config
}

func accumulatePlansConfig(t *testing.T, maxPerComment int) *digger_config.DiggerConfig {
	t.Helper()
	return loadTestConfig(t, fmt.Sprintf(
		"comment_render_mode: accumulate_plans\nreporting:\n  max_plans_per_comment: %v\nprojects:\n- name: dev\n  dir: .\n",
		maxPerComment))
}

const accumulatePlansYaml = "comment_render_mode: accumulate_plans\nprojects:\n- name: dev\n  dir: .\n"

func createTestBatchOfType(t *testing.T, prNumber int, batchType orchestrator_scheduler.DiggerCommand, diggerConfig string) *models.DiggerBatch {
	t.Helper()
	batch, err := models.DB.CreateDiggerBatch(models.DiggerVCSGithub, 1, "diggerhq", "digger", "diggerhq/digger",
		prNumber, diggerConfig, "main", batchType, nil, 0, "", true, false, nil, "sha", nil, nil)
	require.NoError(t, err)
	return batch
}

func createTestBatch(t *testing.T, prNumber int) *models.DiggerBatch {
	t.Helper()
	return createTestBatchOfType(t, prNumber, orchestrator_scheduler.DiggerCommandPlan, "")
}

// A batch's PR service is resolved through the installation row matching its repo.
func registerTestInstallation(t *testing.T) {
	t.Helper()
	_, err := models.DB.CreateGithubAppInstallation(1, 1, "diggerhq", 1, "diggerhq/digger")
	require.NoError(t, err)
}

// githubProviderRecordingEdits serves the comment edit endpoint and records every body it is sent,
// keyed by comment id, so the tests can assert on what reached GitHub.
func githubProviderRecordingEdits(edits map[string]string) *utils.DiggerGithubClientMockProvider {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload github.IssueComment
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &payload)
		edits[path.Base(r.URL.Path)] = payload.GetBody()
		_, _ = w.Write([]byte(`{"id":1}`))
	})

	provider := &utils.DiggerGithubClientMockProvider{}
	provider.MockedHTTPClient = mock.NewMockedHTTPClient(
		mock.WithRequestMatchHandler(mock.PatchReposIssuesCommentsByOwnerByRepoByCommentId, handler),
	)
	return provider
}

func createTestJob(t *testing.T, batch *models.DiggerBatch, projectName string, alias string, status orchestrator_scheduler.DiggerJobStatus, output string) *models.DiggerJob {
	t.Helper()
	jobSpec, err := json.Marshal(orchestrator_scheduler.JobJson{ProjectName: projectName, ProjectAlias: alias})
	require.NoError(t, err)

	job, err := models.DB.CreateDiggerJob(batch.ID, jobSpec, "workflow.yml", nil, nil, "noop", projectName)
	require.NoError(t, err)

	job.Status = status
	job.TerraformOutput = output
	require.NoError(t, models.DB.UpdateDiggerJob(job))
	return job
}

func testProjectNames(count int) []string {
	names := make([]string, count)
	for i := range names {
		names[i] = fmt.Sprintf("project-%02d", i)
	}
	return names
}

func bodies(t *testing.T, svc github_ci.MockCiService, prNumber int) []string {
	t.Helper()
	comments, err := svc.GetComments(prNumber)
	require.NoError(t, err)

	out := make([]string, 0, len(comments))
	for _, comment := range comments {
		require.NotNil(t, comment.Body)
		out = append(out, *comment.Body)
	}
	return out
}

// renderGroupOf renders the group holding projectName from freshly loaded batch state, the way the job
// completion path does. Loading inside the helper is what lets a test create a job between two renders
// and have the second one see it.
func renderGroupOf(t *testing.T, svc ci.PullRequestService, batch *models.DiggerBatch, projectName string) error {
	t.Helper()

	groups, err := models.DB.GetPlanCommentGroupsForBatch(batch.ID)
	if err != nil {
		return err
	}
	jobs, err := models.DB.GetDiggerJobsForBatch(batch.ID)
	if err != nil {
		return err
	}
	groupIndex, err := planGroupIndexForProject(groups, projectName)
	if err != nil {
		return err
	}

	return RenderPlanCommentGroup(svc, batch, groups, jobs, groupIndex, false)
}

func TestCreatePlanCommentGroupsPostsOnePlaceholderPerGroup(t *testing.T) {
	teardownSuite, _ := setupSuite(t)
	defer teardownSuite(t)

	batch := createTestBatch(t, 7)
	svc := github_ci.NewMockCiService()
	projects := testProjectNames(26)

	require.NoError(t, CreatePlanCommentGroupsForBatch(svc, batch, accumulatePlansConfig(t, 8), projects))

	groups, err := models.DB.GetPlanCommentGroupsForBatch(batch.ID)
	require.NoError(t, err)
	require.Len(t, groups, 4, "26 projects at 8 per comment is four comments")

	var sizes []int
	var covered []string
	for _, group := range groups {
		var groupProjects []string
		require.NoError(t, json.Unmarshal(group.Projects, &groupProjects))
		sizes = append(sizes, len(groupProjects))
		covered = append(covered, groupProjects...)
	}
	assert.Equal(t, []int{8, 8, 8, 2}, sizes)
	assert.ElementsMatch(t, projects, covered, "every project belongs to exactly one group")

	published := bodies(t, svc, 7)
	require.Len(t, published, 4, "one placeholder comment per group, posted before any job runs")
	wantHeaders := []string{
		"## Digger plan output (plans 1-8 of 26)",
		"## Digger plan output (plans 9-16 of 26)",
		"## Digger plan output (plans 17-24 of 26)",
		"## Digger plan output (plans 25-26 of 26)",
	}
	for i, body := range published {
		assert.Contains(t, body, wantHeaders[i],
			"every group's header must name the slice of the batch that group covers")
		assert.Equal(t, sizes[i], strings.Count(body, ":clock11:"),
			"a placeholder lists every project in its group as pending")
	}
}

func TestPlanCommentGroupsAreNotCreated(t *testing.T) {
	tests := []struct {
		name      string
		batchType orchestrator_scheduler.DiggerCommand
		batchYaml string
		config    string
		reason    string
	}{
		{
			name:      "in basic render mode",
			batchType: orchestrator_scheduler.DiggerCommandPlan,
			batchYaml: accumulatePlansYaml,
			config:    "projects:\n- name: dev\n  dir: .\n",
			reason:    "existing users must see no change in behaviour",
		},
		{
			name:      "for apply batches",
			batchType: orchestrator_scheduler.DiggerCommandApply,
			batchYaml: accumulatePlansYaml,
			config:    accumulatePlansYaml,
			reason:    "an apply has no plans to accumulate, and its output does not belong under a plan header",
		},
		{
			// reporting.comments_enabled switches plan and apply comments off. Under accumulate_plans the
			// backend is the one posting them, so the setting has to be honoured here as well - otherwise
			// it only silences the runner.
			name:      "when comments are disabled",
			batchType: orchestrator_scheduler.DiggerCommandPlan,
			batchYaml: accumulatePlansYaml,
			config:    "comment_render_mode: accumulate_plans\nreporting:\n  comments_enabled: false\nprojects:\n- name: dev\n  dir: .\n",
			reason:    "comments_enabled: false must silence the backend, not just the runner",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			teardownSuite, _ := setupSuite(t)
			defer teardownSuite(t)

			batch := createTestBatchOfType(t, 7, tt.batchType, tt.batchYaml)
			svc := github_ci.NewMockCiService()

			require.NoError(t, CreatePlanCommentGroupsForBatch(svc, batch, loadTestConfig(t, tt.config), testProjectNames(26)))

			groups, err := models.DB.GetPlanCommentGroupsForBatch(batch.ID)
			require.NoError(t, err)
			assert.Empty(t, groups, tt.reason)
			assert.Empty(t, svc.CommentsPerPr[7], tt.reason)
		})
	}
}

// GitHub redelivers a webhook whose handler it did not hear back from. Publishing before persisting
// leaves the PR with a second set of group comments that nothing ever edits or deletes.
func TestCreatePlanCommentGroupsDoesNotPostTwiceForOneBatch(t *testing.T) {
	teardownSuite, _ := setupSuite(t)
	defer teardownSuite(t)

	batch := createTestBatch(t, 7)
	svc := github_ci.NewMockCiService()
	projects := testProjectNames(26)
	config := accumulatePlansConfig(t, 8)

	require.NoError(t, CreatePlanCommentGroupsForBatch(svc, batch, config, projects))
	firstPass := bodies(t, svc, 7)
	require.Len(t, firstPass, 4)

	require.NoError(t, CreatePlanCommentGroupsForBatch(svc, batch, config, projects))

	groups, err := models.DB.GetPlanCommentGroupsForBatch(batch.ID)
	require.NoError(t, err)
	assert.Len(t, groups, 4)
	assert.Equal(t, firstPass, bodies(t, svc, 7), "a second pass must reuse the comments it already posted")
}

// A pass that failed part way through leaves some groups posted. Finishing the set must number the
// groups it adds by their real position in the batch, not by their position among the ones it posts.
func TestCreatePlanCommentGroupsFinishesAPartlyPostedSet(t *testing.T) {
	teardownSuite, _ := setupSuite(t)
	defer teardownSuite(t)

	batch := createTestBatch(t, 7)
	svc := github_ci.NewMockCiService()
	projects := testProjectNames(26)

	_, err := models.DB.CreatePlanCommentGroup(batch.ID, 0, "101", projects[:8])
	require.NoError(t, err)

	require.NoError(t, CreatePlanCommentGroupsForBatch(svc, batch, accumulatePlansConfig(t, 8), projects))

	groups, err := models.DB.GetPlanCommentGroupsForBatch(batch.ID)
	require.NoError(t, err)
	require.Len(t, groups, 4)

	published := bodies(t, svc, 7)
	require.Len(t, published, 3, "the group that was already posted must not be posted again")
	assert.Contains(t, published[0], "## Digger plan output (plans 9-16 of 26)")
	assert.Contains(t, published[1], "## Digger plan output (plans 17-24 of 26)")
	assert.Contains(t, published[2], "## Digger plan output (plans 25-26 of 26)")
}

func TestCreatePlanCommentGroupsSortsProjectsForDeterministicGroups(t *testing.T) {
	teardownSuite, _ := setupSuite(t)
	defer teardownSuite(t)

	batch := createTestBatch(t, 7)
	svc := github_ci.NewMockCiService()

	// Job maps have no stable iteration order, so grouping has to impose one.
	require.NoError(t, CreatePlanCommentGroupsForBatch(svc, batch, accumulatePlansConfig(t, 2),
		[]string{"delta", "alpha", "charlie", "bravo", "echo"}))

	groups, err := models.DB.GetPlanCommentGroupsForBatch(batch.ID)
	require.NoError(t, err)
	require.Len(t, groups, 3)

	var grouped [][]string
	for _, group := range groups {
		var groupProjects []string
		require.NoError(t, json.Unmarshal(group.Projects, &groupProjects))
		grouped = append(grouped, groupProjects)
	}
	assert.Equal(t, [][]string{{"alpha", "bravo"}, {"charlie", "delta"}, {"echo"}}, grouped)
}

func planGroupsOfSizes(t *testing.T, sizes ...int) []models.DiggerPlanCommentGroup {
	t.Helper()
	groups := make([]models.DiggerPlanCommentGroup, 0, len(sizes))
	next := 0
	for groupIndex, size := range sizes {
		names := make([]string, size)
		for i := range names {
			names[i] = fmt.Sprintf("project-%02d", next+i)
		}
		serialized, err := json.Marshal(names)
		require.NoError(t, err)
		groups = append(groups, models.DiggerPlanCommentGroup{GroupIndex: groupIndex, Projects: serialized})
		next += size
	}
	return groups
}

func TestPlanGroupSlice(t *testing.T) {
	groups := planGroupsOfSizes(t, 8, 8, 8, 2)

	for groupIndex, wantOffset := range map[int]int{0: 0, 1: 8, 2: 16, 3: 24} {
		target, projectNames, offset, total, err := planGroupSlice(groups, groupIndex)
		require.NoError(t, err)
		require.NotNil(t, target)
		assert.Equal(t, groupIndex, target.GroupIndex, "the group it returns must be the one asked for")
		assert.Equal(t, wantOffset, offset, "group %v starts after every earlier group's projects", groupIndex)
		assert.Equal(t, 26, total, "the total counts the whole batch, not the group")
		assert.Equal(t, fmt.Sprintf("project-%02d", wantOffset), projectNames[0],
			"the project list must be the group's own, decoded once")
	}
}

func TestPlanGroupSliceRejectsAForeignGroup(t *testing.T) {
	_, _, _, _, err := planGroupSlice(planGroupsOfSizes(t, 8, 8), 4)
	require.Error(t, err, "a group index the batch does not hold must not render as offset zero")
	assert.Contains(t, err.Error(), "not part of its batch")
}

func TestPlanGroupIndexForProject(t *testing.T) {
	groups := planGroupsOfSizes(t, 8, 8, 8, 2)

	for _, tt := range []struct {
		project string
		want    int
	}{
		{"project-00", 0},
		{"project-07", 0},
		{"project-08", 1},
		{"project-16", 2},
		{"project-25", 3},
	} {
		t.Run(tt.project, func(t *testing.T) {
			groupIndex, err := planGroupIndexForProject(groups, tt.project)
			require.NoError(t, err)
			assert.Equal(t, tt.want, groupIndex)
		})
	}
}

func TestPlanGroupIndexForProjectRejectsAnUnknownProject(t *testing.T) {
	_, err := planGroupIndexForProject(planGroupsOfSizes(t, 8, 8), "project-99")
	require.Error(t, err, "an unknown project must not silently resolve to the first group")
	assert.Contains(t, err.Error(), "project-99")
}

// The render claim counts the group's own terminal jobs. Counting the whole batch instead would let
// one group's progress admit a stale render of another, dropping a plan that already landed.
func TestStaleRenderIsNotAdmittedByAnotherGroupsProgress(t *testing.T) {
	teardownSuite, _ := setupSuite(t)
	defer teardownSuite(t)

	batch := createTestBatch(t, 7)
	svc := github_ci.NewMockCiService()
	require.NoError(t, CreatePlanCommentGroupsForBatch(svc, batch, accumulatePlansConfig(t, 2), testProjectNames(4)))

	// The second group finishes while the first is still running.
	stillRunning := []*models.DiggerJob{
		createTestJob(t, batch, "project-00", "", orchestrator_scheduler.DiggerJobStarted, ""),
		createTestJob(t, batch, "project-01", "", orchestrator_scheduler.DiggerJobStarted, ""),
	}
	late := createTestJob(t, batch, "project-02", "", orchestrator_scheduler.DiggerJobSucceeded, "the plan for project-02")
	createTestJob(t, batch, "project-03", "", orchestrator_scheduler.DiggerJobSucceeded, "the plan for project-03")

	require.NoError(t, renderGroupOf(t, svc, batch, "project-02"))
	require.Equal(t, 2, strings.Count(bodies(t, svc, 7)[1], "```terraform"))

	// Now the first group finishes too, so the batch holds four terminal jobs where the second
	// group holds two.
	for _, job := range stillRunning {
		job.Status = orchestrator_scheduler.DiggerJobSucceeded
		job.TerraformOutput = "the plan for " + job.ProjectName
		require.NoError(t, models.DB.UpdateDiggerJob(job))
	}

	// A render of the second group that read its jobs before the last of them finished.
	late.Status = orchestrator_scheduler.DiggerJobStarted
	require.NoError(t, models.DB.UpdateDiggerJob(late))

	require.NoError(t, renderGroupOf(t, svc, batch, "project-02"))

	assert.Equal(t, 2, strings.Count(bodies(t, svc, 7)[1], "```terraform"),
		"the claim must count this group's terminal jobs, not the batch's")
}

func TestJobCompletionEditsOnlyItsOwnGroupComment(t *testing.T) {
	teardownSuite, _ := setupSuite(t)
	defer teardownSuite(t)

	batch := createTestBatch(t, 7)
	svc := github_ci.NewMockCiService()
	projects := testProjectNames(26)
	require.NoError(t, CreatePlanCommentGroupsForBatch(svc, batch, accumulatePlansConfig(t, 8), projects))

	// project-09 sits in the second group.
	createTestJob(t, batch, "project-09", "", orchestrator_scheduler.DiggerJobSucceeded, "the plan for project-09")

	require.NoError(t, renderGroupOf(t, svc, batch, "project-09"))

	published := bodies(t, svc, 7)
	require.Len(t, published, 4, "rendering must edit, never post another comment")
	assert.Contains(t, published[1], "the plan for project-09")
	assert.Contains(t, published[1], "## Digger plan output (plans 9-16 of 26)",
		"a re-render must keep naming this group's own slice of the batch")
	for i, body := range published {
		if i == 1 {
			continue
		}
		assert.NotContains(t, body, "the plan for project-09",
			"a job must not touch the comments of other groups")
	}
}

func TestJobCompletionsInDifferentGroupsEditDifferentComments(t *testing.T) {
	teardownSuite, _ := setupSuite(t)
	defer teardownSuite(t)

	batch := createTestBatch(t, 7)
	svc := github_ci.NewMockCiService()
	require.NoError(t, CreatePlanCommentGroupsForBatch(svc, batch, accumulatePlansConfig(t, 8), testProjectNames(26)))

	for _, name := range []string{"project-00", "project-20"} {
		createTestJob(t, batch, name, "", orchestrator_scheduler.DiggerJobSucceeded, "the plan for "+name)
		require.NoError(t, renderGroupOf(t, svc, batch, name))
	}

	published := bodies(t, svc, 7)
	require.Len(t, published, 4)
	assert.Contains(t, published[0], "the plan for project-00")
	assert.Contains(t, published[2], "the plan for project-20")
	assert.NotContains(t, published[0], "the plan for project-20")
	assert.NotContains(t, published[2], "the plan for project-00")
}

func TestGroupCommentAccumulatesEveryPlanInItsGroup(t *testing.T) {
	teardownSuite, _ := setupSuite(t)
	defer teardownSuite(t)

	batch := createTestBatch(t, 7)
	svc := github_ci.NewMockCiService()
	require.NoError(t, CreatePlanCommentGroupsForBatch(svc, batch, accumulatePlansConfig(t, 8), testProjectNames(8)))

	for i, name := range []string{"project-00", "project-01", "project-02"} {
		createTestJob(t, batch, name, "", orchestrator_scheduler.DiggerJobSucceeded, "the plan for "+name)
		require.NoError(t, renderGroupOf(t, svc, batch, "project-00"))

		body := bodies(t, svc, 7)[0]
		assert.Equal(t, i+1, strings.Count(body, "```terraform"),
			"each render must carry every plan completed so far, not just the newest")
	}

	body := bodies(t, svc, 7)[0]
	for _, name := range []string{"project-00", "project-01", "project-02"} {
		assert.Contains(t, body, "the plan for "+name)
	}
	assert.Equal(t, 5, strings.Count(body, ":clock11:"), "the other five are still pending")
}

func TestStaleRenderDoesNotOverwriteAFresherOne(t *testing.T) {
	teardownSuite, _ := setupSuite(t)
	defer teardownSuite(t)

	batch := createTestBatch(t, 7)
	svc := github_ci.NewMockCiService()
	require.NoError(t, CreatePlanCommentGroupsForBatch(svc, batch, accumulatePlansConfig(t, 8), testProjectNames(8)))

	var jobs []*models.DiggerJob
	for _, name := range []string{"project-00", "project-01", "project-02"} {
		jobs = append(jobs, createTestJob(t, batch, name, "", orchestrator_scheduler.DiggerJobSucceeded, "the plan for "+name))
	}
	require.NoError(t, renderGroupOf(t, svc, batch, "project-00"))
	require.Equal(t, 3, strings.Count(bodies(t, svc, 7)[0], "```terraform"))

	// Simulate a render that started before the third job finished and only now gets around to
	// publishing: it sees two terminal jobs where three have already been published.
	jobs[2].Status = orchestrator_scheduler.DiggerJobStarted
	require.NoError(t, models.DB.UpdateDiggerJob(jobs[2]))

	require.NoError(t, renderGroupOf(t, svc, batch, "project-00"))

	body := bodies(t, svc, 7)[0]
	assert.Equal(t, 3, strings.Count(body, "```terraform"),
		"a stale render must not drop a plan that already reached the comment")
	assert.Contains(t, body, "the plan for project-02")
}

// editHookService lets a test reject chosen edits the way GitHub rejects a transient failure or a
// comment that is gone. failEdit is consulted first and its error, if any, is returned instead.
type editHookService struct {
	github_ci.MockCiService
	failEdit func(id string) error
}

func (s *editHookService) EditComment(prNumber int, id string, body string) error {
	if s.failEdit != nil {
		if err := s.failEdit(id); err != nil {
			return err
		}
	}
	return s.MockCiService.EditComment(prNumber, id, body)
}

// failOnce rejects the first edit it sees, whichever comment that is.
func failOnce(err error) func(string) error {
	failed := false
	return func(string) error {
		if failed {
			return nil
		}
		failed = true
		return err
	}
}

// An edit that never reached GitHub rendered nothing, so the next render of the same state must
// still be allowed to try again.
func TestRenderPlanCommentGroupRetriesAfterAFailedEdit(t *testing.T) {
	teardownSuite, _ := setupSuite(t)
	defer teardownSuite(t)

	batch := createTestBatch(t, 7)
	svc := &editHookService{
		MockCiService: github_ci.NewMockCiService(),
		failEdit:      failOnce(fmt.Errorf("502 Bad Gateway")),
	}
	require.NoError(t, CreatePlanCommentGroupsForBatch(svc, batch, accumulatePlansConfig(t, 8), testProjectNames(2)))

	createTestJob(t, batch, "project-00", "", orchestrator_scheduler.DiggerJobSucceeded, "the plan for project-00")

	require.Error(t, renderGroupOf(t, svc, batch, "project-00"), "a failed edit must be reported")

	require.NoError(t, renderGroupOf(t, svc, batch, "project-00"))

	assert.Contains(t, bodies(t, svc.MockCiService, 7)[0], "the plan for project-00",
		"the retry of a render that never landed must not be refused as redundant")
}

// pausingEditService holds the first edit inside the service until the test releases it, so that two
// renders of one group overlap the way two job completions do.
type pausingEditService struct {
	github_ci.MockCiService
	edits      atomic.Int32
	firstEdit  chan struct{}
	release    chan struct{}
	secondEdit chan struct{}
	seenSecond sync.Once
}

func (s *pausingEditService) EditComment(prNumber int, id string, body string) error {
	if s.edits.Add(1) == 1 {
		close(s.firstEdit)
		<-s.release
		return s.MockCiService.EditComment(prNumber, id, body)
	}

	err := s.MockCiService.EditComment(prNumber, id, body)
	s.seenSecond.Do(func() { close(s.secondEdit) })
	return err
}

// Two jobs of one group finishing at once put two renders on the same comment. The database counter
// alone cannot order them: it is written before the edit reaches GitHub, so the render that read
// fewer finished jobs can still land last and drop a plan that was already published.
func TestConcurrentRendersDoNotLeaveAStaleComment(t *testing.T) {
	teardownSuite, _ := setupSuite(t)
	defer teardownSuite(t)

	batch := createTestBatch(t, 7)
	svc := &pausingEditService{
		MockCiService: github_ci.NewMockCiService(),
		firstEdit:     make(chan struct{}),
		release:       make(chan struct{}),
		secondEdit:    make(chan struct{}),
	}
	require.NoError(t, CreatePlanCommentGroupsForBatch(svc, batch, accumulatePlansConfig(t, 8), testProjectNames(2)))

	createTestJob(t, batch, "project-00", "", orchestrator_scheduler.DiggerJobSucceeded, "the plan for project-00")
	var renders sync.WaitGroup
	renders.Add(1)
	go func() {
		defer renders.Done()
		assert.NoError(t, renderGroupOf(t, svc, batch, "project-00"))
	}()
	<-svc.firstEdit

	// The second job finishes while the first render is still in flight.
	createTestJob(t, batch, "project-01", "", orchestrator_scheduler.DiggerJobSucceeded, "the plan for project-01")
	renders.Add(1)
	go func() {
		defer renders.Done()
		assert.NoError(t, renderGroupOf(t, svc, batch, "project-00"))
	}()

	// Either the second render edits while the first is paused, or it is waiting for the first to
	// finish. Both are legitimate; only the comment the pair leaves behind matters.
	select {
	case <-svc.secondEdit:
	case <-time.After(500 * time.Millisecond):
	}
	close(svc.release)
	renders.Wait()

	body := bodies(t, svc.MockCiService, 7)[0]
	assert.Equal(t, 2, strings.Count(body, "```terraform"),
		"the render that read fewer finished jobs must not land on top of a fresher one")
	assert.Contains(t, body, "the plan for project-01")
}

// The batch-terminal pass is the only forced render, so the groups after a transient failure need it
// just as much as the ones before it. Giving up on the first error left them showing whatever an
// earlier refused render happened to leave.
func TestBatchCompletionRendersLaterGroupsAfterOneFails(t *testing.T) {
	teardownSuite, _ := setupSuite(t)
	defer teardownSuite(t)

	batch := createTestBatch(t, 7)
	// Assigned once the groups exist, so publishing them succeeds and only the later edit is rejected
	// the way GitHub answers a comment that is gone.
	var rejectId string
	svc := &editHookService{
		MockCiService: github_ci.NewMockCiService(),
		failEdit: func(id string) error {
			if id == rejectId {
				return fmt.Errorf("404 Not Found")
			}
			return nil
		},
	}
	projects := testProjectNames(26)
	require.NoError(t, CreatePlanCommentGroupsForBatch(svc, batch, accumulatePlansConfig(t, 8), projects))

	for _, name := range projects {
		createTestJob(t, batch, name, "", orchestrator_scheduler.DiggerJobSucceeded, "the plan for "+name)
	}

	groups, err := models.DB.GetPlanCommentGroupsForBatch(batch.ID)
	require.NoError(t, err)
	require.Len(t, groups, 4)
	rejectId = groups[0].CommentId

	err = RenderAllPlanCommentGroups(svc, batch)
	require.Error(t, err, "the group that could not be rendered must still be reported")

	published := bodies(t, svc.MockCiService, 7)
	require.Len(t, published, 4)
	assert.NotContains(t, published[0], "the plan for project-00", "this group's edit was rejected")
	for _, name := range projects[8:] {
		assert.Contains(t, strings.Join(published[1:], "\n"), "the plan for "+name,
			"a group after the failing one must still be rendered")
	}
}

// A project with several impacted parents gets one job row per parent edge, and the rows come back
// unordered. Picking whichever came last could hide a plan that had already finished.
func TestRenderPrefersTheFinishedRowOfADuplicatedProject(t *testing.T) {
	teardownSuite, _ := setupSuite(t)
	defer teardownSuite(t)

	batch := createTestBatch(t, 7)
	svc := github_ci.NewMockCiService()
	require.NoError(t, CreatePlanCommentGroupsForBatch(svc, batch, accumulatePlansConfig(t, 8),
		[]string{"alpha", "beta"}))

	createTestJob(t, batch, "alpha", "", orchestrator_scheduler.DiggerJobSucceeded, "the plan for alpha")
	// The second row of the same project, created later and still pending.
	createTestJob(t, batch, "alpha", "", orchestrator_scheduler.DiggerJobCreated, "")

	require.NoError(t, renderGroupOf(t, svc, batch, "alpha"))

	assert.Contains(t, bodies(t, svc, 7)[0], "the plan for alpha",
		"a finished job row must win over a sibling row that is still pending")
}

// The scheduler only starts a job once every parent has succeeded, so the children of a failed plan
// stay in DiggerJobCreated for good. Requiring every job to have finished meant the authoritative
// final render never ran for those batches.
func TestBatchIsFinishedWhenAFailedPlanStrandsItsChildren(t *testing.T) {
	teardownSuite, _ := setupSuite(t)
	defer teardownSuite(t)

	batch := createTestBatch(t, 7)
	createTestJob(t, batch, "parent", "", orchestrator_scheduler.DiggerJobFailed, "")
	stranded := createTestJob(t, batch, "child", "", orchestrator_scheduler.DiggerJobCreated, "")

	done, err := allBatchJobsTerminal(batch.ID)
	require.NoError(t, err)
	assert.True(t, done, "a job that can never be scheduled must not hold the batch open forever")

	// A job that is still genuinely running must, though.
	stranded.Status = orchestrator_scheduler.DiggerJobStarted
	require.NoError(t, models.DB.UpdateDiggerJob(stranded))

	done, err = allBatchJobsTerminal(batch.ID)
	require.NoError(t, err)
	assert.False(t, done, "a running job means the batch is not finished")
}

// max_plans_per_comment reaches lo.Chunk from config, where a non-positive value panics. The handler
// has to reject it before that, since not every path that builds a DiggerConfig runs the validator.
func TestCreatePlanCommentGroupsRejectsANonPositiveMaxPerComment(t *testing.T) {
	teardownSuite, _ := setupSuite(t)
	defer teardownSuite(t)

	batch := createTestBatch(t, 7)
	svc := github_ci.NewMockCiService()
	config := accumulatePlansConfig(t, 8)
	config.Reporting.MaxPlansPerComment = 0

	err := CreatePlanCommentGroupsForBatch(svc, batch, config, testProjectNames(4))
	require.Error(t, err, "an unusable group size must fail the request, not take the handler down")
	assert.Contains(t, err.Error(), "max_plans_per_comment")
	assert.Empty(t, svc.CommentsPerPr[7])
}

// nilCommentService publishes without returning the comment, the way AzureReposService does.
type nilCommentService struct {
	github_ci.MockCiService
}

func (s *nilCommentService) PublishComment(prNumber int, comment string) (*ci.Comment, error) {
	return nil, nil
}

// The returned comment id is the only handle later renders have, so a service that does not give one
// has to be an error rather than a nil dereference in the webhook handler.
func TestCreatePlanCommentGroupsFailsWhenNoCommentIdComesBack(t *testing.T) {
	teardownSuite, _ := setupSuite(t)
	defer teardownSuite(t)

	batch := createTestBatch(t, 7)
	svc := &nilCommentService{MockCiService: github_ci.NewMockCiService()}

	err := CreatePlanCommentGroupsForBatch(svc, batch, accumulatePlansConfig(t, 8), testProjectNames(4))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "without a comment id")
}

func TestBatchCompletionRerendersEveryGroup(t *testing.T) {
	teardownSuite, _ := setupSuite(t)
	defer teardownSuite(t)

	batch := createTestBatch(t, 7)
	svc := github_ci.NewMockCiService()
	projects := testProjectNames(26)
	require.NoError(t, CreatePlanCommentGroupsForBatch(svc, batch, accumulatePlansConfig(t, 8), projects))

	for _, name := range projects {
		createTestJob(t, batch, name, "", orchestrator_scheduler.DiggerJobSucceeded, "the plan for "+name)
	}

	// One intermediate render already claimed the first group at its full count.
	require.NoError(t, renderGroupOf(t, svc, batch, "project-00"))

	require.NoError(t, RenderAllPlanCommentGroups(svc, batch))

	published := bodies(t, svc, 7)
	require.Len(t, published, 4)
	for _, name := range projects {
		assert.Contains(t, strings.Join(published, "\n"), "the plan for "+name,
			"the render at batch completion is authoritative and must cover every plan")
	}

	// A comment that drifted after its group was claimed must still be restored.
	groups, err := models.DB.GetPlanCommentGroupsForBatch(batch.ID)
	require.NoError(t, err)
	require.NoError(t, svc.EditComment(7, groups[0].CommentId, "clobbered"))
	require.NoError(t, RenderAllPlanCommentGroups(svc, batch))
	assert.Contains(t, bodies(t, svc, 7)[0], "the plan for project-00",
		"the terminal render must not be blocked by its own earlier claim")
}

func TestGroupCommentSurvivesOversizedPlans(t *testing.T) {
	teardownSuite, _ := setupSuite(t)
	defer teardownSuite(t)

	batch := createTestBatch(t, 7)
	svc := github_ci.NewMockCiService()
	projects := testProjectNames(8)
	require.NoError(t, CreatePlanCommentGroupsForBatch(svc, batch, accumulatePlansConfig(t, 8), projects))

	for _, name := range projects {
		createTestJob(t, batch, name, "", orchestrator_scheduler.DiggerJobSucceeded,
			strings.Repeat("x", reporting.GithubCommentMaxLength))
	}

	// The mock rejects an oversized body the way GitHub's 422 does, so an untrimmed render errors.
	require.NoError(t, renderGroupOf(t, svc, batch, "project-00"))

	body := bodies(t, svc, 7)[0]
	assert.LessOrEqual(t, utf8.RuneCountInString(body), reporting.GithubCommentMaxLength)
	assert.Equal(t, 8, strings.Count(body, "```terraform"), "every plan must still be represented")
}

func TestRenderPlanCommentGroupUsesTheProjectAlias(t *testing.T) {
	teardownSuite, _ := setupSuite(t)
	defer teardownSuite(t)

	batch := createTestBatch(t, 7)
	svc := github_ci.NewMockCiService()
	require.NoError(t, CreatePlanCommentGroupsForBatch(svc, batch, accumulatePlansConfig(t, 8),
		[]string{"customers_bkw_prod"}))

	createTestJob(t, batch, "customers_bkw_prod", "bkw-prod", orchestrator_scheduler.DiggerJobSucceeded, "the plan")

	require.NoError(t, renderGroupOf(t, svc, batch, "customers_bkw_prod"))

	assert.Contains(t, bodies(t, svc, 7)[0], "Plan for bkw-prod")
}

func TestRenderPlanCommentGroupMarksFailedJobs(t *testing.T) {
	teardownSuite, _ := setupSuite(t)
	defer teardownSuite(t)

	batch := createTestBatch(t, 7)
	svc := github_ci.NewMockCiService()
	require.NoError(t, CreatePlanCommentGroupsForBatch(svc, batch, accumulatePlansConfig(t, 8),
		[]string{"alpha", "beta"}))

	createTestJob(t, batch, "alpha", "", orchestrator_scheduler.DiggerJobSucceeded, "the plan for alpha")
	createTestJob(t, batch, "beta", "", orchestrator_scheduler.DiggerJobFailed, "")

	require.NoError(t, renderGroupOf(t, svc, batch, "alpha"))

	body := bodies(t, svc, 7)[0]
	assert.Contains(t, body, "the plan for alpha")
	assert.Contains(t, body, ":x: **beta** - plan failed, see the job logs")
}

func TestCommentRenderModeForBatch(t *testing.T) {
	tests := []struct {
		name         string
		diggerConfig string
		want         string
	}{
		{
			name:         "accumulate_plans is read from the config the batch carries",
			diggerConfig: accumulatePlansYaml,
			want:         digger_config.CommentRenderModeAccumulatePlans,
		},
		{
			name:         "a config that does not set the mode means the default",
			diggerConfig: "projects:\n- name: dev\n  dir: .\n",
			want:         digger_config.CommentRenderModeBasic,
		},
		{
			name:         "an empty config means the default",
			diggerConfig: "",
			want:         digger_config.CommentRenderModeBasic,
		},
		{
			name:         "a config that does not parse means the default",
			diggerConfig: "projects: [unclosed",
			want:         digger_config.CommentRenderModeBasic,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, commentRenderModeForBatch(&models.DiggerBatch{DiggerConfig: tt.diggerConfig}))
		})
	}
}

func TestRenderPlanCommentGroupsForJobEditsTheGroupComment(t *testing.T) {
	teardownSuite, _ := setupSuite(t)
	defer teardownSuite(t)
	registerTestInstallation(t)

	batch := createTestBatchOfType(t, 7, orchestrator_scheduler.DiggerCommandPlan, accumulatePlansYaml)
	_, err := models.DB.CreatePlanCommentGroup(batch.ID, 0, "101", []string{"alpha", "beta"})
	require.NoError(t, err)

	job := createTestJob(t, batch, "alpha", "", orchestrator_scheduler.DiggerJobSucceeded, "the plan for alpha")
	createTestJob(t, batch, "beta", "", orchestrator_scheduler.DiggerJobStarted, "")

	edits := map[string]string{}
	renderPlanCommentGroupsForJob(githubProviderRecordingEdits(edits), batch, job)

	require.Contains(t, edits, "101", "a finished job must rewrite the group comment it belongs to")
	assert.Contains(t, edits["101"], "the plan for alpha")
	assert.Contains(t, edits["101"], "## Digger plan output (plans 1-2 of 2)")
	assert.Contains(t, edits["101"], ":arrows_counterclockwise: **beta** - planning")
}

// Both cases plant a group row the batch would never have, so the guard being tested is the one that
// looks at the batch itself rather than the mere existence of groups.
func TestRenderPlanCommentGroupsForJobIsSilent(t *testing.T) {
	tests := []struct {
		name      string
		batchType orchestrator_scheduler.DiggerCommand
		batchYaml string
		reason    string
	}{
		{
			name:      "in basic render mode",
			batchType: orchestrator_scheduler.DiggerCommandPlan,
			batchYaml: "projects:\n- name: dev\n  dir: .\n",
			reason:    "the render mode of the batch's own config decides, and it is not accumulate_plans",
		},
		{
			name:      "for apply batches",
			batchType: orchestrator_scheduler.DiggerCommandApply,
			batchYaml: accumulatePlansYaml,
			reason:    "accumulate_plans accumulates plans, an apply keeps the runner's own comments",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			teardownSuite, _ := setupSuite(t)
			defer teardownSuite(t)
			registerTestInstallation(t)

			batch := createTestBatchOfType(t, 7, tt.batchType, tt.batchYaml)
			_, err := models.DB.CreatePlanCommentGroup(batch.ID, 0, "101", []string{"alpha"})
			require.NoError(t, err)
			job := createTestJob(t, batch, "alpha", "", orchestrator_scheduler.DiggerJobSucceeded, "the output for alpha")

			edits := map[string]string{}
			renderPlanCommentGroupsForJob(githubProviderRecordingEdits(edits), batch, job)

			assert.Empty(t, edits, tt.reason)
		})
	}
}

func TestRenderPlanCommentGroupsForJobRerendersEveryGroupWhenTheBatchFinishes(t *testing.T) {
	teardownSuite, _ := setupSuite(t)
	defer teardownSuite(t)
	registerTestInstallation(t)

	batch := createTestBatchOfType(t, 7, orchestrator_scheduler.DiggerCommandPlan, accumulatePlansYaml)
	_, err := models.DB.CreatePlanCommentGroup(batch.ID, 0, "101", []string{"alpha", "beta"})
	require.NoError(t, err)
	_, err = models.DB.CreatePlanCommentGroup(batch.ID, 1, "102", []string{"gamma", "delta"})
	require.NoError(t, err)

	job := createTestJob(t, batch, "alpha", "", orchestrator_scheduler.DiggerJobSucceeded, "the plan for alpha")
	createTestJob(t, batch, "beta", "", orchestrator_scheduler.DiggerJobSucceeded, "the plan for beta")
	createTestJob(t, batch, "gamma", "", orchestrator_scheduler.DiggerJobSucceeded, "the plan for gamma")
	unfinished := createTestJob(t, batch, "delta", "", orchestrator_scheduler.DiggerJobStarted, "")

	edits := map[string]string{}
	renderPlanCommentGroupsForJob(githubProviderRecordingEdits(edits), batch, job)
	assert.NotContains(t, edits, "102", "while the batch runs, a job only touches its own group")

	unfinished.Status = orchestrator_scheduler.DiggerJobSucceeded
	unfinished.TerraformOutput = "the plan for delta"
	require.NoError(t, models.DB.UpdateDiggerJob(unfinished))

	edits = map[string]string{}
	renderPlanCommentGroupsForJob(githubProviderRecordingEdits(edits), batch, unfinished)

	require.Contains(t, edits, "102", "the last job's own group")
	require.Contains(t, edits, "101", "the batch-terminal pass is authoritative and must cover every group")
	assert.Contains(t, edits["101"], "the plan for alpha")
	assert.Contains(t, edits["102"], "the plan for delta")
}

// The backend never marks a batch failed, only succeeded, so a batch with one failed plan never
// reaches a terminal status. Completion has to be derived from the jobs instead.
func TestAllBatchJobsTerminal(t *testing.T) {
	teardownSuite, _ := setupSuite(t)
	defer teardownSuite(t)

	batch := createTestBatch(t, 7)
	createTestJob(t, batch, "alpha", "", orchestrator_scheduler.DiggerJobSucceeded, "the plan for alpha")
	createTestJob(t, batch, "beta", "", orchestrator_scheduler.DiggerJobSucceeded, "the plan for beta")
	running := createTestJob(t, batch, "gamma", "", orchestrator_scheduler.DiggerJobStarted, "")

	done, err := allBatchJobsTerminal(batch.ID)
	require.NoError(t, err)
	assert.False(t, done, "a running job means the batch is not finished")

	running.Status = orchestrator_scheduler.DiggerJobFailed
	require.NoError(t, models.DB.UpdateDiggerJob(running))

	done, err = allBatchJobsTerminal(batch.ID)
	require.NoError(t, err)
	assert.True(t, done, "a failed job is finished, and the batch status will never say so")

	// The claim this helper exists for. Asserting against the in-memory batch would prove nothing: it
	// was never written to. Drive the real updater and re-read the row.
	require.NoError(t, models.DB.UpdateBatchStatus(batch))
	reloaded, err := models.DB.GetDiggerBatch(&batch.ID)
	require.NoError(t, err)
	assert.NotEqual(t, orchestrator_scheduler.BatchJobSucceeded, reloaded.Status,
		"one job failed, so the batch must not be marked succeeded")
	assert.NotEqual(t, orchestrator_scheduler.BatchJobFailed, reloaded.Status,
		"UpdateBatchStatus never marks a batch failed, which is exactly why completion has to be derived from the jobs")
}

func TestDeletePlanCommentGroupsForBatch(t *testing.T) {
	teardownSuite, _ := setupSuite(t)
	defer teardownSuite(t)

	svc := github_ci.NewMockCiService()
	older := createTestBatch(t, 7)
	require.NoError(t, CreatePlanCommentGroupsForBatch(svc, older, accumulatePlansConfig(t, 2),
		[]string{"alpha", "beta", "gamma"}))

	// A comment that does not belong to the older batch must survive.
	unrelated, err := svc.PublishComment(7, "some other comment")
	require.NoError(t, err)

	require.Len(t, bodies(t, svc, 7), 3, "two group comments plus the unrelated one")

	assert.True(t, deletePlanCommentGroupsForBatch(svc, older))

	comments, err := svc.GetComments(7)
	require.NoError(t, err)
	require.Len(t, comments, 1, "every group comment of the older batch must be gone")
	assert.Equal(t, unrelated.Id, comments[0].Id)
}

func TestDeletePlanCommentGroupsForBatchWithoutGroups(t *testing.T) {
	teardownSuite, _ := setupSuite(t)
	defer teardownSuite(t)

	svc := github_ci.NewMockCiService()
	batch := createTestBatch(t, 7)
	unrelated, err := svc.PublishComment(7, "some other comment")
	require.NoError(t, err)

	assert.True(t, deletePlanCommentGroupsForBatch(svc, batch))

	comments, err := svc.GetComments(7)
	require.NoError(t, err)
	require.Len(t, comments, 1, "a batch rendered in basic mode has no group comments to delete")
	assert.Equal(t, unrelated.Id, comments[0].Id)
}

// DeleteOlderPRCommentsIfEnabled warns about prior comments it could not remove, which it can only do
// if every delete it drives reports what happened.
func TestDeletePlanCommentGroupsReportsAFailedDeletion(t *testing.T) {
	teardownSuite, _ := setupSuite(t)
	defer teardownSuite(t)
	registerTestInstallation(t)

	batch := createTestBatch(t, 7)
	_, err := models.DB.CreatePlanCommentGroup(batch.ID, 0, "101", []string{"alpha"})
	require.NoError(t, err)

	provider := &utils.DiggerGithubClientMockProvider{}
	provider.MockedHTTPClient = mock.NewMockedHTTPClient(
		// GitHub answers a comment that is already gone with a 404.
		mock.WithRequestMatchHandler(mock.DeleteReposIssuesCommentsByOwnerByRepoByCommentId,
			http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			})),
	)
	prService, err := utils.GetPrServiceFromBatch(batch, provider)
	require.NoError(t, err)

	assert.False(t, deletePlanCommentGroupsForBatch(prService, batch))
}
