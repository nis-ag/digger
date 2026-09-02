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
	return AccumulatedPlan{DisplayName: name, Status: scheduler.DiggerJobSucceeded, Output: output}
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
		{DisplayName: "beta", Status: scheduler.DiggerJobFailed},
		{DisplayName: "gamma", Status: scheduler.DiggerJobCreated},
		{DisplayName: "delta", Status: scheduler.DiggerJobSucceeded, Output: ""},
		succeededPlan("epsilon", "epsilon plan output"),
	}

	body := RenderAccumulatedPlans(PlanGroupHeader(0, 5, 5), plans)

	assert.Contains(t, body, ":x: **beta** - plan failed, see the job logs")
	assert.Contains(t, body, ":clock11: **gamma** - pending")
	assert.Contains(t, body, ":white_check_mark: **delta** - plan complete, no output captured")

	// A plan without output must not swallow the blocks around it.
	_, blocks := splitTerraformBlocks(body)
	require.Len(t, blocks, 2)
	assert.Equal(t, "alpha plan output", blocks[0])
	assert.Equal(t, "epsilon plan output", blocks[1])
}

// "</details>" opens a CommonMark HTML block that closes only at a blank line, so a status line one
// newline after a finished plan is swallowed into it and shown verbatim - no emoji, literal
// asterisks. A group where one plan has landed and the rest have not is the normal in-progress state,
// so this is what a reviewer sees for most of a run.
func TestRenderAccumulatedPlansSeparatesSectionsWithABlankLine(t *testing.T) {
	body := RenderAccumulatedPlans(PlanGroupHeader(0, 4, 4), []AccumulatedPlan{
		succeededPlan("alpha", "alpha plan output"),
		{DisplayName: "beta", Status: scheduler.DiggerJobCreated},
		{DisplayName: "gamma", Status: scheduler.DiggerJobFailed},
		{DisplayName: "delta", Status: scheduler.DiggerJobSucceeded},
	})

	require.Contains(t, body, "</details>", "the fixture must actually produce a collapsible plan")

	// Every section must be separated from the previous one by a blank line, so no markdown line can
	// land inside the HTML block a "</details>" opens.
	assert.NotContains(t, body, "</details>\n:",
		"a status line directly after </details> is absorbed into the raw HTML block")
	assert.Contains(t, body, "</details>\n\n:clock11: **beta** - pending",
		"the pending line must start its own markdown block")
	assert.Contains(t, body, "\n\n:x: **gamma** - plan failed, see the job logs")
	assert.Contains(t, body, "\n\n:white_check_mark: **delta** - plan complete, no output captured")
	assert.Contains(t, body, PlanGroupHeader(0, 4, 4)+"\n\n<details",
		"the header must not run into the first plan either")
}

func TestRenderAccumulatedPlansRendersAnAllPendingPlaceholder(t *testing.T) {
	plans := []AccumulatedPlan{
		{DisplayName: "alpha", Status: scheduler.DiggerJobCreated},
		{DisplayName: "beta", Status: scheduler.DiggerJobCreated},
	}

	body := RenderAccumulatedPlans(PlanGroupHeader(0, 2, 2), plans)

	assert.Contains(t, body, ":clock11: **alpha** - pending")
	assert.Contains(t, body, ":clock11: **beta** - pending")
	_, blocks := splitTerraformBlocks(body)
	assert.Empty(t, blocks)
}

func TestRenderAccumulatedPlansUsesTheProjectAlias(t *testing.T) {
	plans := []AccumulatedPlan{
		{
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
				{DisplayName: "beta", Status: scheduler.DiggerJobFailed},
				{DisplayName: "gamma", Status: scheduler.DiggerJobCreated},
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
