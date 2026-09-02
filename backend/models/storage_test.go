package models

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/diggerhq/digger/libs/scheduler"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupSuite(tb testing.TB) (func(tb testing.TB), *Database, *Organisation) {
	// database file name
	dbName := "database_storage_test.db"

	// remove old database
	e := os.Remove(dbName)
	if e != nil {
		if !strings.Contains(e.Error(), "no such file or directory") {
			panic(e)
		}
	}

	// open and create a new database
	gdb, err := gorm.Open(sqlite.Open(dbName), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		panic(err)
	}

	// migrate tables
	err = gdb.AutoMigrate(&Policy{}, &Organisation{}, &Repo{}, &Project{}, &Token{},
		&User{}, &ProjectRun{}, &GithubAppInstallation{}, &VCSConnection{}, &GithubAppInstallationLink{},
		&GithubDiggerJobLink{}, &DiggerJob{}, &DiggerJobParentLink{}, &DiggerLock{},
		&DiggerBatch{}, &DiggerPlanCommentGroup{})
	if err != nil {
		panic(err)
	}

	database := &Database{GormDB: gdb}
	DB = database

	// create an org
	orgTenantId := "11111111-1111-1111-1111-111111111111"
	externalSource := "test"
	orgName := "testOrg"
	org, err := database.CreateOrganisation(orgName, externalSource, orgTenantId, nil)
	if err != nil {
		panic(err)
	}

	DB = database
	// Return a function to teardown the test
	return func(tb testing.TB) {
		err = os.Remove(dbName)
		if err != nil {
			panic(err)
		}
	}, database, org
}

func TestCreateGithubInstallationLink(t *testing.T) {
	teardownSuite, _, org := setupSuite(t)
	defer teardownSuite(t)

	installationId := int64(1)

	link, err := DB.CreateGithubInstallationLink(org, installationId)
	assert.NoError(t, err)
	assert.NotNil(t, link)

	link2, err := DB.CreateGithubInstallationLink(org, installationId)
	assert.NoError(t, err)
	assert.NotNil(t, link2)
	assert.Equal(t, link.ID, link2.ID)
}

func TestGithubRepoAdded(t *testing.T) {
	teardownSuite, _, _ := setupSuite(t)
	defer teardownSuite(t)

	installationId := int64(1)
	appId := int64(1)
	accountId := int64(1)
	login := "test"
	repoFullName := "test/test"

	i, err := DB.GithubRepoAdded(installationId, appId, login, accountId, repoFullName)
	assert.NoError(t, err)
	assert.NotNil(t, i)

	i2, err := DB.GithubRepoAdded(installationId, appId, login, accountId, repoFullName)
	assert.NoError(t, err)
	assert.NotNil(t, i)
	assert.Equal(t, i.ID, i2.ID)
	assert.Equal(t, GithubAppInstallActive, i.Status)
}

func TestGithubRepoRemoved(t *testing.T) {
	teardownSuite, _, _ := setupSuite(t)
	defer teardownSuite(t)

	installationId := int64(1)
	appId := int64(1)
	accountId := int64(1)
	login := "test"
	repoFullName := "test/test"

	i, err := DB.GithubRepoAdded(installationId, appId, login, accountId, repoFullName)
	assert.NoError(t, err)
	assert.NotNil(t, i)

	i, err = DB.GithubRepoRemoved(installationId, appId, repoFullName)
	assert.NoError(t, err)
	assert.NotNil(t, i)
	assert.Equal(t, GithubAppInstallDeleted, i.Status)

	i2, err := DB.GithubRepoAdded(installationId, appId, login, accountId, repoFullName)
	assert.NoError(t, err)
	assert.NotNil(t, i)
	assert.Equal(t, i.ID, i2.ID)
	assert.Equal(t, GithubAppInstallDeleted, i.Status)
}

func TestSoftDeleteRepoAndProjects(t *testing.T) {
	teardownSuite, db, org := setupSuite(t)
	defer teardownSuite(t)

	installationId := int64(1)
	appId := int64(1)
	repoFullName := "test/test"

	repo, err := db.CreateRepo("test-test", repoFullName, "test", "test", "", org, "", installationId, appId, "main", "")
	assert.NoError(t, err)
	assert.NotNil(t, repo)

	project := Project{
		Name:           "proj",
		OrganisationID: org.ID,
		Organisation:   org,
		RepoFullName:   repoFullName,
		Status:         ProjectActive,
	}
	err = db.GormDB.Create(&project).Error
	assert.NoError(t, err)

	err = db.SoftDeleteRepoAndProjects(org.ID, repoFullName)
	assert.NoError(t, err)

	// Verify repo is soft-deleted
	var repoRecord Repo
	err = db.GormDB.Unscoped().Where("id = ?", repo.ID).First(&repoRecord).Error
	assert.NoError(t, err)
	assert.True(t, repoRecord.DeletedAt.Valid)

	// Verify project is soft-deleted
	var projectRecord Project
	err = db.GormDB.Unscoped().Where("id = ?", project.ID).First(&projectRecord).Error
	assert.NoError(t, err)
	assert.True(t, projectRecord.DeletedAt.Valid)
}

func TestSoftDeleteReposAndProjectsByInstallation(t *testing.T) {
	teardownSuite, db, org := setupSuite(t)
	defer teardownSuite(t)

	appId := int64(1)
	installA := int64(1)
	installB := int64(2)

	repoA, err := db.CreateRepo("org-repo-a", "org/repo-a", "org", "repo-a", "", org, "", installA, appId, "main", "")
	assert.NoError(t, err)
	repoB, err := db.CreateRepo("org-repo-b", "org/repo-b", "org", "repo-b", "", org, "", installB, appId, "main", "")
	assert.NoError(t, err)

	projectA := Project{
		Name:           "proj-a",
		OrganisationID: org.ID,
		Organisation:   org,
		RepoFullName:   repoA.RepoFullName,
		Status:         ProjectActive,
	}
	projectB := Project{
		Name:           "proj-b",
		OrganisationID: org.ID,
		Organisation:   org,
		RepoFullName:   repoB.RepoFullName,
		Status:         ProjectActive,
	}
	assert.NoError(t, db.GormDB.Create(&projectA).Error)
	assert.NoError(t, db.GormDB.Create(&projectB).Error)

	// Soft-delete only repos for installA
	err = db.SoftDeleteReposAndProjectsByInstallation(org.ID, installA)
	assert.NoError(t, err)

	// Verify repoA is soft-deleted, repoB is not
	var repoARecord, repoBRecord Repo
	assert.NoError(t, db.GormDB.Unscoped().Where("id = ?", repoA.ID).First(&repoARecord).Error)
	assert.NoError(t, db.GormDB.Unscoped().Where("id = ?", repoB.ID).First(&repoBRecord).Error)
	assert.True(t, repoARecord.DeletedAt.Valid)
	assert.False(t, repoBRecord.DeletedAt.Valid)

	// Verify projectA is soft-deleted, projectB is not
	var projectARecord, projectBRecord Project
	assert.NoError(t, db.GormDB.Unscoped().Where("id = ?", projectA.ID).First(&projectARecord).Error)
	assert.NoError(t, db.GormDB.Unscoped().Where("id = ?", projectB.ID).First(&projectBRecord).Error)
	assert.True(t, projectARecord.DeletedAt.Valid)
	assert.False(t, projectBRecord.DeletedAt.Valid)
}

func TestGetDiggerJobsForBatchPreloadsSummary(t *testing.T) {
	teardownSuite, _, _ := setupSuite(t)
	defer teardownSuite(t)

	prNumber := 123
	repoName := "test"
	repoOwner := "test"
	repoFullName := "test/test"
	diggerconfig := ""
	branchName := "main"
	batchType := scheduler.DiggerCommandPlan
	commentId := int64(123)
	jobSpec := "abc"

	resourcesCreated := uint(1)
	resourcesUpdated := uint(2)
	resourcesDeleted := uint(3)

	batch, err := DB.CreateDiggerBatch(DiggerVCSGithub, 123, repoOwner, repoName, repoFullName, prNumber, diggerconfig, branchName, batchType, &commentId, 0, "", false, true, nil, "", nil, nil)
	assert.NoError(t, err)

	job, err := DB.CreateDiggerJob(batch.ID, []byte(jobSpec), "workflow_file.yml", nil, nil, "lazy", "")
	assert.NoError(t, err)

	job, err = DB.UpdateDiggerJobSummary(job.DiggerJobID, resourcesCreated, resourcesUpdated, resourcesDeleted)
	assert.NoError(t, err)

	jobssss, err := DB.GetDiggerJobsForBatch(batch.ID)
	assert.Equal(t, jobssss[0].DiggerJobSummary.ResourcesCreated, resourcesCreated)
	assert.Equal(t, jobssss[0].DiggerJobSummary.ResourcesUpdated, resourcesUpdated)
	assert.Equal(t, jobssss[0].DiggerJobSummary.ResourcesDeleted, resourcesDeleted)
}

func TestDiggerLockFunctionalities(t *testing.T) {
	teardownSuite, _, _ := setupSuite(t)
	defer teardownSuite(t)

	DB.CreateDiggerLock("org/repo1#dev", 1, 1)
	DB.CreateDiggerLock("org/repo1#staging", 1, 1)
	DB.CreateDiggerLock("org/repo1#prod", 1, 1)

	DB.CreateDiggerLock("org/repo2#dev", 1, 1)
	DB.CreateDiggerLock("org/repo2#prod", 1, 1)

	existingLocks, err := DB.GetLocksForOrg(1)
	assert.NoError(t, err)
	assert.Equal(t, 5, len(existingLocks))

	DB.DeleteAllLocksAcquiredByPR(1, "org/repo1", 1)

	existingLocksAfterDeletion, err := DB.GetLocksForOrg(1)
	assert.NoError(t, err)
	assert.Equal(t, 2, len(existingLocksAfterDeletion))
	assert.Equal(t, "org/repo2#dev", existingLocksAfterDeletion[0].Resource)
	assert.Equal(t, "org/repo2#prod", existingLocksAfterDeletion[1].Resource)
}

func createTestBatch(t *testing.T) *DiggerBatch {
	t.Helper()
	batch, err := DB.CreateDiggerBatch(DiggerVCSGithub, 1, "diggerhq", "digger", "diggerhq/digger", 42,
		"", "main", scheduler.DiggerCommandPlan, nil, 0, "", true, false, nil, "abc123", nil, nil)
	require.NoError(t, err)
	return batch
}

func TestCreatePlanCommentGroups(t *testing.T) {
	teardownSuite, _, _ := setupSuite(t)
	defer teardownSuite(t)

	batch := createTestBatch(t)
	groups := [][]string{
		{"alpha", "beta"},
		{"gamma", "delta"},
		{"epsilon", "zeta"},
		{"eta"},
	}
	for i, projects := range groups {
		_, err := DB.CreatePlanCommentGroup(batch.ID, i, fmt.Sprintf("comment-%v", i), projects)
		require.NoError(t, err)
	}

	stored, err := DB.GetPlanCommentGroupsForBatch(batch.ID)
	require.NoError(t, err)
	require.Len(t, stored, 4)

	for i, group := range stored {
		assert.Equal(t, i, group.GroupIndex, "groups must come back in group_index order")
		assert.Equal(t, fmt.Sprintf("comment-%v", i), group.CommentId)

		var projects []string
		require.NoError(t, json.Unmarshal(group.Projects, &projects))
		assert.Equal(t, groups[i], projects)
	}
}

func TestCreatePlanCommentGroupsIsIdempotent(t *testing.T) {
	teardownSuite, _, _ := setupSuite(t)
	defer teardownSuite(t)

	batch := createTestBatch(t)
	for range 2 {
		for i, projects := range [][]string{{"alpha"}, {"beta"}} {
			_, err := DB.CreatePlanCommentGroup(batch.ID, i, fmt.Sprintf("comment-%v", i), projects)
			require.NoError(t, err)
		}
	}

	stored, err := DB.GetPlanCommentGroupsForBatch(batch.ID)
	require.NoError(t, err)
	assert.Len(t, stored, 2, "a retried webhook must not double-post comment groups")
}

func TestPlanCommentGroupsOfDifferentBatchesAreIndependent(t *testing.T) {
	teardownSuite, _, _ := setupSuite(t)
	defer teardownSuite(t)

	first, second := createTestBatch(t), createTestBatch(t)
	_, err := DB.CreatePlanCommentGroup(first.ID, 0, "comment-first", []string{"alpha"})
	require.NoError(t, err)
	_, err = DB.CreatePlanCommentGroup(second.ID, 0, "comment-second", []string{"alpha"})
	require.NoError(t, err)

	firstGroups, err := DB.GetPlanCommentGroupsForBatch(first.ID)
	require.NoError(t, err)
	require.Len(t, firstGroups, 1)
	assert.Equal(t, "comment-first", firstGroups[0].CommentId)

	secondGroups, err := DB.GetPlanCommentGroupsForBatch(second.ID)
	require.NoError(t, err)
	require.Len(t, secondGroups, 1)
	assert.Equal(t, "comment-second", secondGroups[0].CommentId)
}

func TestClaimRenderAdvancesMonotonically(t *testing.T) {
	teardownSuite, _, _ := setupSuite(t)
	defer teardownSuite(t)

	batch := createTestBatch(t)
	group, err := DB.CreatePlanCommentGroup(batch.ID, 0, "comment-0", []string{"alpha"})
	require.NoError(t, err)
	require.NotNil(t, group)

	renderedJobCount := func() int {
		reloaded, err := DB.GetPlanCommentGroupsForBatch(batch.ID)
		require.NoError(t, err)
		require.Len(t, reloaded, 1)
		return reloaded[0].RenderedJobCount
	}

	claimed, err := DB.ClaimPlanCommentGroupRender(group.ID, 5, false)
	require.NoError(t, err)
	assert.True(t, claimed)
	assert.Equal(t, 5, renderedJobCount())

	// The claim is what orders two replicas: the one that read fewer terminal jobs has to be told no,
	// because after it there is no second guard between it and the VCS.
	claimed, err = DB.ClaimPlanCommentGroupRender(group.ID, 4, false)
	require.NoError(t, err)
	assert.False(t, claimed, "a render that read fewer terminal jobs must be refused")
	assert.Equal(t, 5, renderedJobCount(), "and must not walk the counter back")

	claimed, err = DB.ClaimPlanCommentGroupRender(group.ID, 6, false)
	require.NoError(t, err)
	assert.True(t, claimed, "a fresher render must be admitted")
	assert.Equal(t, 6, renderedJobCount())

	// force is the batch-terminal render, which is authoritative, and is also how a failed edit hands
	// its claim back.
	claimed, err = DB.ClaimPlanCommentGroupRender(group.ID, 2, true)
	require.NoError(t, err)
	assert.True(t, claimed, "force must not be refused by the guard")
	assert.Equal(t, 2, renderedJobCount(), "force must be able to lower the counter again")
}

// A claim against a row that is gone must report that it did not get the claim, otherwise the caller
// edits the comment believing the guard is live when it is not recording anything.
func TestClaimRenderOfAMissingGroupIsNotGranted(t *testing.T) {
	teardownSuite, _, _ := setupSuite(t)
	defer teardownSuite(t)

	claimed, err := DB.ClaimPlanCommentGroupRender(4242, 1, true)
	require.NoError(t, err)
	assert.False(t, claimed)
}

// GORM omits zero-valued fields from a struct condition, so keying this lookup on a struct made group
// index 0 match whatever row of the batch had the lowest id.
func TestCreatePlanCommentGroupHonoursGroupIndexZero(t *testing.T) {
	teardownSuite, _, _ := setupSuite(t)
	defer teardownSuite(t)

	batch := createTestBatch(t)
	_, err := DB.CreatePlanCommentGroup(batch.ID, 1, "comment-one", []string{"beta"})
	require.NoError(t, err)

	zero, err := DB.CreatePlanCommentGroup(batch.ID, 0, "comment-zero", []string{"alpha"})
	require.NoError(t, err)
	assert.Equal(t, 0, zero.GroupIndex, "group 0 must not resolve to another group of the batch")
	assert.Equal(t, "comment-zero", zero.CommentId)

	stored, err := DB.GetPlanCommentGroupsForBatch(batch.ID)
	require.NoError(t, err)
	require.Len(t, stored, 2, "group 0 must be persisted even though 1 already existed")
	assert.Equal(t, "comment-zero", stored[0].CommentId)
	assert.Equal(t, "comment-one", stored[1].CommentId)
}

func TestDeletePlanCommentGroup(t *testing.T) {
	teardownSuite, _, _ := setupSuite(t)
	defer teardownSuite(t)

	batch := createTestBatch(t)
	group, err := DB.CreatePlanCommentGroup(batch.ID, 0, "comment-0", []string{"alpha"})
	require.NoError(t, err)

	require.NoError(t, DB.DeletePlanCommentGroup(group.ID))

	stored, err := DB.GetPlanCommentGroupsForBatch(batch.ID)
	require.NoError(t, err)
	assert.Empty(t, stored, "a group whose comment is gone must not be found again")
}
