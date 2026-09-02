package controllers

import (
	"net/http"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/diggerhq/digger/backend/models"
	"github.com/diggerhq/digger/backend/utils"
	github_ci "github.com/diggerhq/digger/libs/ci/github"
	"github.com/diggerhq/digger/libs/comment_utils/reporting"
	orchestrator_scheduler "github.com/diggerhq/digger/libs/scheduler"
	"github.com/migueleliasweb/go-github-mock/src/mock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type blockingAccumulatedPlanEditService struct {
	github_ci.MockCiService
	editStarted chan struct{}
	resumeEdit  chan struct{}
}

func (s *blockingAccumulatedPlanEditService) EditComment(prNumber int, id string, body string) error {
	close(s.editStarted)
	<-s.resumeEdit
	return s.MockCiService.EditComment(prNumber, id, body)
}

func TestStaleReplicaCannotOverwriteAnAccumulatedPlanComment(t *testing.T) {
	teardownSuite, _ := setupSuite(t)
	defer teardownSuite(t)

	batch := createTestBatch(t, 7)
	svc := &blockingAccumulatedPlanEditService{
		MockCiService: github_ci.NewMockCiService(),
		editStarted:   make(chan struct{}),
		resumeEdit:    make(chan struct{}),
	}
	require.NoError(t, CreatePlanCommentGroupsForBatch(svc, batch, accumulatePlansConfig(t, 2), testProjectNames(2)))

	createTestJob(t, batch, "project-00", "", orchestrator_scheduler.DiggerJobSucceeded, "the plan for project-00")
	groups, err := models.DB.GetPlanCommentGroupsForBatch(batch.ID)
	require.NoError(t, err)
	jobs, err := models.DB.GetDiggerJobsForBatch(batch.ID)
	require.NoError(t, err)
	require.Len(t, groups, 1)

	staleRenderDone := make(chan error, 1)
	go func() {
		staleRenderDone <- RenderPlanCommentGroup(svc, batch, groups, jobs, 0, false)
	}()

	select {
	case <-svc.editStarted:
	case <-time.After(time.Second):
		t.Fatal("the stale renderer did not reach its GitHub edit")
	}

	// This simulates a second backend replica: it cannot share the first process's in-memory lock.
	createTestJob(t, batch, "project-01", "", orchestrator_scheduler.DiggerJobSucceeded, "the plan for project-01")
	claimed, err := models.DB.ClaimPlanCommentGroupRender(groups[0].ID, 2, false)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, svc.MockCiService.EditComment(batch.PrNumber, groups[0].CommentId,
		reporting.RenderAccumulatedPlans(reporting.PlanGroupHeader(0, 2, 2), []reporting.AccumulatedPlan{
			{DisplayName: "project-00", Status: orchestrator_scheduler.DiggerJobSucceeded, Output: "the plan for project-00"},
			{DisplayName: "project-01", Status: orchestrator_scheduler.DiggerJobSucceeded, Output: "the plan for project-01"},
		})))

	close(svc.resumeEdit)
	require.NoError(t, <-staleRenderDone)

	body := bodies(t, svc.MockCiService, batch.PrNumber)[0]
	assert.Contains(t, body, "the plan for project-01",
		"a stale render must not replace the two-plan body a newer replica already published")
}

func TestDeleteOlderCommentsDoesNotDeleteNewerPlanCommentGroups(t *testing.T) {
	teardownSuite, _ := setupSuite(t)
	defer teardownSuite(t)
	registerTestInstallation(t)

	config := "delete_prior_comments: true\nprojects:\n- name: dev\n  dir: .\n"
	older := createTestBatchOfType(t, 7, orchestrator_scheduler.DiggerCommandPlan, config)
	older.Status = orchestrator_scheduler.BatchJobSucceeded
	older.CoverAllImpactedProjects = true
	require.NoError(t, models.DB.UpdateDiggerBatch(older))

	newer := createTestBatchOfType(t, 7, orchestrator_scheduler.DiggerCommandPlan, config)
	_, err := models.DB.CreatePlanCommentGroup(newer.ID, 0, "202", []string{"project-00"})
	require.NoError(t, err)

	var deletedCommentIDs []string
	provider := &utils.DiggerGithubClientMockProvider{}
	provider.MockedHTTPClient = mock.NewMockedHTTPClient(
		mock.WithRequestMatchHandler(mock.DeleteReposIssuesCommentsByOwnerByRepoByCommentId,
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				deletedCommentIDs = append(deletedCommentIDs, path.Base(r.URL.Path))
				w.WriteHeader(http.StatusNoContent)
			})),
	)

	require.NoError(t, DeleteOlderPRCommentsIfEnabled(provider, older))
	assert.NotContains(t, strings.Join(deletedCommentIDs, ","), "202",
		"a late status update from an older batch must not remove a newer batch's accumulated plans")
}

func TestAccumulatedPlanShowsFailureWhenOneDuplicateJobFails(t *testing.T) {
	teardownSuite, _ := setupSuite(t)
	defer teardownSuite(t)

	batch := createTestBatch(t, 7)
	svc := github_ci.NewMockCiService()
	require.NoError(t, CreatePlanCommentGroupsForBatch(svc, batch, accumulatePlansConfig(t, 2), []string{"alpha"}))

	createTestJob(t, batch, "alpha", "", orchestrator_scheduler.DiggerJobFailed, "")
	createTestJob(t, batch, "alpha", "", orchestrator_scheduler.DiggerJobSucceeded, "the plan for alpha")

	require.NoError(t, renderGroupOf(t, svc, batch, "alpha"))
	body := bodies(t, svc, batch.PrNumber)[0]
	assert.Contains(t, body, ":x: **alpha** - plan failed, see the job logs",
		"a successful sibling job must not hide a failed execution of the same grouped project")
}

func TestStartedAccumulatedPlanUpdatesItsPlaceholder(t *testing.T) {
	teardownSuite, _ := setupSuite(t)
	defer teardownSuite(t)

	batch := createTestBatch(t, 7)
	svc := github_ci.NewMockCiService()
	require.NoError(t, CreatePlanCommentGroupsForBatch(svc, batch, accumulatePlansConfig(t, 2), []string{"alpha"}))

	createTestJob(t, batch, "alpha", "", orchestrator_scheduler.DiggerJobStarted, "")
	require.NoError(t, renderGroupOf(t, svc, batch, "alpha"))

	body := bodies(t, svc, batch.PrNumber)[0]
	assert.Contains(t, body, ":arrows_counterclockwise: **alpha** - planning",
		"a started job must not leave its accumulated plan placeholder pending")
}
