package reporting

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	maxFuzzPlans = 8
	// Capped: prose that alone exceeds the limit is an unsatisfiable crasher and would become
	// permanent corpus noise. TestProtectedProseAloneExceedsLimit covers that regime instead.
	maxFuzzProseRunes = 2000
	// Block sizes reach twice the comment limit; beyond that they are equivalent and only cost
	// time.
	fuzzPlanSizeScale = 2
)

type fuzzPlan struct {
	planRunes  int
	proseRunes int
}

// Reads a plan count from byte 0 and then, per plan, a two-byte terraform block size and a
// one-byte prose size. Missing bytes read as zero, so any input length decodes.
func decodeFuzzPlans(data []byte) []fuzzPlan {
	at := func(i int) int {
		if i < len(data) {
			return int(data[i])
		}
		return 0
	}

	plans := make([]fuzzPlan, 0, maxFuzzPlans)
	for i := range 1 + at(0)%maxFuzzPlans {
		plans = append(plans, fuzzPlan{
			planRunes:  1 + (at(1+3*i)<<8|at(2+3*i))*fuzzPlanSizeScale,
			proseRunes: at(3+3*i) * maxFuzzProseRunes / 255,
		})
	}
	return plans
}

func fuzzSeed(plans, planRunes, proseRunes int) []byte {
	size := (planRunes - 1) / fuzzPlanSizeScale
	data := []byte{byte(plans - 1)}
	for range plans {
		data = append(data, byte(size>>8), byte(size), byte(proseRunes*255/maxFuzzProseRunes))
	}
	return data
}

func FuzzCommentTrimming(f *testing.F) {
	f.Add(fuzzSeed(1, utf8.RuneCountInString(fixturePlanBlock(f)), 0))
	f.Add(fuzzSeed(1, oversizedPlanRunes, 0))
	f.Add(fuzzSeed(maxFuzzPlans, 20000, 0))
	f.Add(fuzzSeed(maxFuzzPlans, 20000, maxFuzzProseRunes))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		plans := decodeFuzzPlans(data)

		for _, collapsible := range []bool{true, false} {
			svc := newMockCiService()
			strategy := CommentPerRunStrategy{Title: accumulatingTitle, TimeOfRun: fixedTimeOfRun}

			var formatted []string
			for i, plan := range plans {
				project := fmt.Sprintf("shared-services-%d", i+1)
				reports := planReports(t, project, planContentOfSize(t, plan.planRunes), collapsible)
				// Multi-byte padding: a trimmer that byte-slices instead of rune-slicing shows
				// up here.
				reports[1].body += "\n" + strings.Repeat("─", plan.proseRunes)
				formatted = append(formatted, replay(t, svc, strategy, reports, collapsible)...)
			}

			body := onlyBody(t, svc, 1)
			assertCommentInvariants(t, body, collapsible)
			assertProtectedTextSurvives(t, body, formatted)

			_, blocks := splitTerraformBlocks(body)
			assert.Len(t, blocks, len(plans), "every plan keeps its terraform block")
		}
	})
}

// Protected prose that alone exceeds the limit: keeping all of it and fitting the limit are
// irreconcilable, so only the limit and utf8 validity can be asserted here.
func TestProtectedProseAloneExceedsLimit(t *testing.T) {
	svc := newMockCiService()
	strategy := CommentPerRunStrategy{Title: accumulatingTitle, TimeOfRun: fixedTimeOfRun}

	reports := planReports(t, "shared-services", planContentOfSize(t, 5000), true)
	reports[3].body += strings.Repeat("─", GithubCommentMaxLength)
	require.Greater(t, utf8.RuneCountInString(reports[3].formatted()), GithubCommentMaxLength,
		"the Instructions report alone must not fit")

	replay(t, svc, strategy, reports, true)
	body := onlyBody(t, svc, 1)

	assert.LessOrEqual(t, utf8.RuneCountInString(body), GithubCommentMaxLength)
	assert.True(t, utf8.ValidString(body))
}
