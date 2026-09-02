package reporting

import (
	"strings"
	"testing"

	"github.com/diggerhq/digger/libs/scheduler"
	"github.com/stretchr/testify/assert"
)

func TestRenderAccumulatedPlansKeepsAliasesFromChangingMarkup(t *testing.T) {
	body := RenderAccumulatedPlans(PlanGroupHeader(0, 1, 1), []AccumulatedPlan{
		{
			DisplayName: "alpha</details>",
			Status:      scheduler.DiggerJobSucceeded,
			Output:      "plan output",
		},
	})

	assert.Equal(t, strings.Count(body, "<details"), strings.Count(body, "</details>"),
		"a project alias must be text, not markup that closes a generated details block")
}
