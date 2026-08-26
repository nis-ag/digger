package reporting

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/diggerhq/digger/libs/scheduler"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func succeededPlan(name, output string) AccumulatedPlan {
	return AccumulatedPlan{ProjectName: name, DisplayName: name, Status: scheduler.DiggerJobSucceeded, Output: output}
}

func TestChunkProjects(t *testing.T) {
	tests := []struct {
		name     string
		projects []string
		size     int
		want     [][]string
	}{
		{
			name:     "a full batch splits into equal groups with a short tail",
			projects: []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"},
			size:     4,
			want:     [][]string{{"a", "b", "c", "d"}, {"e", "f", "g", "h"}, {"i", "j"}},
		},
		{
			name:     "an exact multiple leaves no empty trailing group",
			projects: []string{"a", "b", "c", "d"},
			size:     4,
			want:     [][]string{{"a", "b", "c", "d"}},
		},
		{
			name:     "fewer projects than the limit gives one short group",
			projects: []string{"a", "b", "c"},
			size:     8,
			want:     [][]string{{"a", "b", "c"}},
		},
		{
			name:     "a limit of one gives one group per project",
			projects: []string{"a", "b", "c"},
			size:     1,
			want:     [][]string{{"a"}, {"b"}, {"c"}},
		},
		{
			name:     "no projects gives no groups",
			projects: []string{},
			size:     8,
			want:     nil,
		},
		{
			name:     "input order is preserved rather than sorted",
			projects: []string{"c", "a", "b"},
			size:     2,
			want:     [][]string{{"c", "a"}, {"b"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ChunkProjects(tt.projects, tt.size)
			if tt.want == nil {
				assert.Empty(t, got)
				return
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestChunkProjectsCoversEveryProjectExactlyOnce(t *testing.T) {
	projects := make([]string, 26)
	for i := range projects {
		projects[i] = fmt.Sprintf("project-%02d", i)
	}

	groups := ChunkProjects(projects, 8)
	require.Len(t, groups, 4)

	var flattened []string
	for _, group := range groups {
		assert.LessOrEqual(t, len(group), 8)
		flattened = append(flattened, group...)
	}
	assert.Equal(t, projects, flattened)
}

func TestPlanGroupHeader(t *testing.T) {
	tests := []struct {
		offset, count, total int
		want                 string
	}{
		{0, 8, 26, "## Digger plan output (plans 1-8 of 26)"},
		{8, 8, 26, "## Digger plan output (plans 9-16 of 26)"},
		{24, 2, 26, "## Digger plan output (plans 25-26 of 26)"},
		{0, 3, 3, "## Digger plan output (plans 1-3 of 3)"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			assert.Equal(t, tt.want, PlanGroupHeader(tt.offset, tt.count, tt.total))
		})
	}
}

func TestRenderAccumulatedPlansEmitsOneBlockPerSucceededPlan(t *testing.T) {
	plans := []AccumulatedPlan{
		succeededPlan("alpha", "alpha plan output"),
		succeededPlan("beta", "beta plan output"),
		succeededPlan("gamma", "gamma plan output"),
	}

	body := RenderAccumulatedPlans(PlanGroupHeader(0, 3, 3), plans)

	_, blocks := splitTerraformBlocks(body)
	require.Len(t, blocks, 3)
	assert.Equal(t, "alpha plan output", blocks[0])
	assert.Equal(t, "beta plan output", blocks[1])
	assert.Equal(t, "gamma plan output", blocks[2])

	for _, name := range []string{"alpha", "beta", "gamma"} {
		assert.Contains(t, body, "Plan for "+name)
	}
	assert.Less(t, strings.Index(body, "Plan for alpha"), strings.Index(body, "Plan for beta"))
	assert.Less(t, strings.Index(body, "Plan for beta"), strings.Index(body, "Plan for gamma"))
}

func TestRenderAccumulatedPlansMarksFailedAndPendingPlans(t *testing.T) {
	plans := []AccumulatedPlan{
		succeededPlan("alpha", "alpha plan output"),
		{ProjectName: "beta", DisplayName: "beta", Status: scheduler.DiggerJobFailed},
		{ProjectName: "gamma", DisplayName: "gamma", Status: scheduler.DiggerJobCreated},
		{ProjectName: "delta", DisplayName: "delta", Status: scheduler.DiggerJobSucceeded, Output: ""},
		succeededPlan("epsilon", "epsilon plan output"),
	}

	body := RenderAccumulatedPlans(PlanGroupHeader(0, 5, 5), plans)

	assert.Contains(t, body, ":x: **beta** - plan failed, see the job logs")
	assert.Contains(t, body, ":hourglass_flowing_sand: **gamma** - pending")
	assert.Contains(t, body, ":white_check_mark: **delta** - plan complete, no output captured")

	// A plan without output must not swallow the blocks around it.
	_, blocks := splitTerraformBlocks(body)
	require.Len(t, blocks, 2)
	assert.Equal(t, "alpha plan output", blocks[0])
	assert.Equal(t, "epsilon plan output", blocks[1])
}

func TestRenderAccumulatedPlansRendersAnAllPendingPlaceholder(t *testing.T) {
	plans := []AccumulatedPlan{
		{ProjectName: "alpha", DisplayName: "alpha", Status: scheduler.DiggerJobCreated},
		{ProjectName: "beta", DisplayName: "beta", Status: scheduler.DiggerJobCreated},
	}

	body := RenderAccumulatedPlans(PlanGroupHeader(0, 2, 2), plans)

	assert.Contains(t, body, ":hourglass_flowing_sand: **alpha** - pending")
	assert.Contains(t, body, ":hourglass_flowing_sand: **beta** - pending")
	_, blocks := splitTerraformBlocks(body)
	assert.Empty(t, blocks)
}

func TestRenderAccumulatedPlansUsesTheProjectAlias(t *testing.T) {
	plans := []AccumulatedPlan{
		{
			ProjectName: "customers_bkw_prod",
			DisplayName: "bkw-prod",
			Status:      scheduler.DiggerJobSucceeded,
			Output:      "plan output",
		},
	}

	body := RenderAccumulatedPlans(PlanGroupHeader(0, 1, 1), plans)

	assert.Contains(t, body, "Plan for bkw-prod")
	assert.NotContains(t, body, "customers_bkw_prod")
}

func TestRenderAccumulatedPlansSatisfiesCommentInvariants(t *testing.T) {
	tests := []struct {
		name  string
		plans []AccumulatedPlan
	}{
		{
			name: "mixed statuses",
			plans: []AccumulatedPlan{
				succeededPlan("alpha", fixturePlanBlock(t)),
				{ProjectName: "beta", DisplayName: "beta", Status: scheduler.DiggerJobFailed},
				{ProjectName: "gamma", DisplayName: "gamma", Status: scheduler.DiggerJobCreated},
				succeededPlan("delta", fixturePlanBlock(t)),
			},
		},
		{
			name:  "eight oversized plans, so every block is elided",
			plans: eightOversizedPlans(t),
		},
		{
			name: "a plan carrying its own fence",
			plans: []AccumulatedPlan{
				succeededPlan("alpha", "before\n```\nnot a real fence\n```\nafter"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			header := PlanGroupHeader(0, len(tt.plans), len(tt.plans))
			body := RenderAccumulatedPlans(header, tt.plans)

			// Guard the invariants below, which an empty body would satisfy vacuously.
			require.Contains(t, body, header)
			for _, plan := range tt.plans {
				require.Contains(t, body, plan.DisplayName)
			}

			assertCommentInvariants(t, body, false)
		})
	}
}

func eightOversizedPlans(t testing.TB) []AccumulatedPlan {
	t.Helper()
	plans := make([]AccumulatedPlan, 8)
	for i := range plans {
		plans[i] = succeededPlan(fmt.Sprintf("project-%d", i), planContentOfSize(t, GithubCommentMaxLength))
	}
	return plans
}

func TestRenderAccumulatedPlansTrimsEightOversizedPlans(t *testing.T) {
	plans := eightOversizedPlans(t)
	header := PlanGroupHeader(0, 8, 26)

	body := RenderAccumulatedPlans(header, plans)

	assert.LessOrEqual(t, utf8.RuneCountInString(body), GithubCommentMaxLength,
		"a rendered group must fit the comment limit, otherwise GitHub rejects the edit")
	assert.NotContains(t, body, commentTruncationMarker,
		"trimming the plan blocks must be enough, the tail cut would eat the status lines")

	// Every header and status line the untrimmed render produced must survive verbatim.
	assertProtectedTextSurvives(t, body, []string{renderAccumulatedPlans(header, plans)})

	_, blocks := splitTerraformBlocks(body)
	require.Len(t, blocks, 8)
	for i, block := range blocks {
		assertBlockIsHeadPlusTail(t, block, plans[i].Output)
	}
}

// max_plans_per_comment has no upper bound, and past roughly sixty plans a comment cannot hold even
// the floor for each of them. How the blocks shrink from there is unspecified, but the body must
// still fit and must still name every plan it covers.
func TestRenderAccumulatedPlansSurvivesMorePlansThanTheFloorFits(t *testing.T) {
	plans := make([]AccumulatedPlan, 100)
	for i := range plans {
		plans[i] = succeededPlan(fmt.Sprintf("project-%02d", i), planContentOfSize(t, GithubCommentMaxLength))
	}
	header := PlanGroupHeader(0, len(plans), len(plans))

	body := RenderAccumulatedPlans(header, plans)

	assert.LessOrEqual(t, utf8.RuneCountInString(body), GithubCommentMaxLength,
		"a group this large must still fit, otherwise GitHub rejects every edit of it")
	assert.NotContains(t, body, commentTruncationMarker,
		"shrinking the blocks must be enough, the tail cut would eat the plans at the end")

	require.Contains(t, body, header)
	for _, plan := range plans {
		assert.Contains(t, body, "Plan for "+plan.DisplayName)
	}
	assertCommentInvariants(t, body, false)
}
