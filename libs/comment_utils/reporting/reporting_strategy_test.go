package reporting

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/diggerhq/digger/libs/ci"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The comment title embeds TimeOfRun, so a ticking clock breaks accumulation into one comment.
var fixedTimeOfRun = time.Date(2026, 8, 24, 10, 54, 3, 0, time.UTC)

func identityFormatter(report string) string {
	return report
}

func newMockCiService() MockCiService {
	return MockCiService{CommentsPerPr: map[int][]*ci.Comment{}}
}

func onlyBody(t *testing.T, svc MockCiService, prNumber int) string {
	comments, err := svc.GetComments(prNumber)
	assert.NoError(t, err)
	assert.Len(t, comments, 1)
	return *comments[0].Body
}

func TestTrimToCommentLimitLeavesShortBodyAlone(t *testing.T) {
	body := "a short plan output"
	assert.Equal(t, body, TrimToCommentLimit(body, 0))
	assert.Equal(t, body, TrimToCommentLimit(body, 1000))
}

func TestTrimToCommentLimitRespectsOverhead(t *testing.T) {
	body := strings.Repeat("a", GithubCommentMaxLength*2)

	trimmed := TrimToCommentLimit(body, 0)
	assert.Equal(t, GithubCommentMaxLength, utf8.RuneCountInString(trimmed))
	assert.True(t, strings.HasSuffix(trimmed, commentTruncationMarker))

	trimmedWithOverhead := TrimToCommentLimit(body, 500)
	assert.Equal(t, GithubCommentMaxLength-500, utf8.RuneCountInString(trimmedWithOverhead))
}

// Trimming must not cut a multi-byte rune in half, which byte slicing would do.
func TestTrimToCommentLimitKeepsValidUtf8(t *testing.T) {
	body := strings.Repeat("→", GithubCommentMaxLength+1000)

	trimmed := TrimToCommentLimit(body, 0)

	assert.True(t, utf8.ValidString(trimmed))
	assert.Equal(t, GithubCommentMaxLength, utf8.RuneCountInString(trimmed))
}

func TestCommentPerRunStrategyTrimsOversizedReport(t *testing.T) {
	svc := newMockCiService()
	strategy := CommentPerRunStrategy{Title: "Digger run report", TimeOfRun: fixedTimeOfRun}

	_, _, err := strategy.Report(svc, 1, strings.Repeat("a", GithubCommentMaxLength*2), identityFormatter, true)
	assert.NoError(t, err)

	body := onlyBody(t, svc, 1)
	assert.Equal(t, GithubCommentMaxLength, utf8.RuneCountInString(body))
	assert.Contains(t, body, "Digger run report")
	// The wrapper must still get to close its tag, otherwise the next report in this
	// run corrupts the comment when it strips the first and last lines.
	assert.True(t, strings.HasSuffix(body, "</details>"))
	assert.Contains(t, body, commentTruncationMarker)
}

func TestCommentPerRunStrategyStaysUnderLimitWhenAccumulating(t *testing.T) {
	svc := newMockCiService()
	strategy := CommentPerRunStrategy{Title: "Digger run report", TimeOfRun: fixedTimeOfRun}

	for i := 0; i < 3; i++ {
		_, _, err := strategy.Report(svc, 1, strings.Repeat("a", GithubCommentMaxLength), identityFormatter, true)
		assert.NoError(t, err)
	}

	// All three reports land in the same comment for the run.
	body := onlyBody(t, svc, 1)
	assert.Equal(t, GithubCommentMaxLength, utf8.RuneCountInString(body))
	assert.Contains(t, body, "Digger run report")
	assert.True(t, strings.HasSuffix(body, "</details>"))
}

func TestLatestRunCommentStrategyTrimsOversizedReport(t *testing.T) {
	svc := newMockCiService()
	strategy := LatestRunCommentStrategy{TimeOfRun: fixedTimeOfRun}

	_, _, err := strategy.Report(svc, 1, strings.Repeat("a", GithubCommentMaxLength*2), identityFormatter, true)
	assert.NoError(t, err)

	body := onlyBody(t, svc, 1)
	assert.Equal(t, GithubCommentMaxLength, utf8.RuneCountInString(body))
	assert.Contains(t, body, "Digger latest run report")
}

func TestMultipleCommentsStrategyTrimsOversizedReport(t *testing.T) {
	svc := newMockCiService()
	strategy := MultipleCommentsStrategy{}

	_, _, err := strategy.Report(svc, 1, strings.Repeat("a", GithubCommentMaxLength*2), identityFormatter, true)
	assert.NoError(t, err)

	body := onlyBody(t, svc, 1)
	assert.Equal(t, GithubCommentMaxLength, utf8.RuneCountInString(body))
	assert.True(t, strings.HasSuffix(body, commentTruncationMarker))
}

// Terraform output routinely contains % sequences. The formatters used to
// interpolate the body through a Sprintf format string, which turned them into
// %!d(MISSING) and inflated the body past the limit after it had been trimmed.
func TestFormattersDoNotInterpretPercentSequences(t *testing.T) {
	plan := "tags = { name = \"100%\" }\ntemplate = \"%{ if true }x%{ endif }\"\n%d %s %v"

	formatters := map[string]func(string) string{
		"collapsible":           AsCollapsibleComment("Plan output", false),
		"comment":               AsComment("Plan output"),
		"terraform collapsible": GetTerraformOutputAsCollapsibleComment("Plan output", true),
		"terraform comment":     GetTerraformOutputAsComment("Plan output"),
	}
	for name, formatter := range formatters {
		out := formatter(plan)
		assert.Contains(t, out, plan, name)
		assert.NotContains(t, out, "MISSING", name)
	}
}

func TestCommentPerRunStrategyTrimsReportWithPercentSequences(t *testing.T) {
	svc := newMockCiService()
	strategy := CommentPerRunStrategy{Title: "Digger run report", TimeOfRun: fixedTimeOfRun}

	_, _, err := strategy.Report(svc, 1, strings.Repeat("%d", GithubCommentMaxLength), identityFormatter, true)
	assert.NoError(t, err)

	body := onlyBody(t, svc, 1)
	assert.Equal(t, GithubCommentMaxLength, utf8.RuneCountInString(body))
	assert.NotContains(t, body, "MISSING")
}

// AsComment only prepends a title, so unwrapping must not strip a trailing line — here the
// closing fence.
func TestNonCollapsibleAccumulationKeepsLastLine(t *testing.T) {
	svc := newMockCiService()
	strategy := CommentPerRunStrategy{Title: "Digger run report", TimeOfRun: fixedTimeOfRun}
	formatter := GetTerraformOutputAsComment("Plan output")

	for _, plan := range []string{"first plan output", "second plan output"} {
		_, _, err := strategy.Report(svc, 1, plan, formatter, false)
		assert.NoError(t, err)
	}

	body := onlyBody(t, svc, 1)
	assert.Contains(t, body, "first plan output\n```")
	assert.Contains(t, body, "second plan output\n```")
	assert.Equal(t, 4, strings.Count(body, "```"))
}

// A comment carrying the report title can be hand-edited down to the title alone, leaving no
// closing line to strip. Unwrapping it must not slice out of range.
func TestAccumulationIntoASingleLineComment(t *testing.T) {
	reportTitle := "Digger run report " + fixedTimeOfRun.Format("2006-01-02 15:04:05 (MST)")

	for _, collapsible := range []bool{true, false} {
		t.Run(wrapperName(collapsible), func(t *testing.T) {
			svc := newMockCiService()
			_, err := svc.PublishComment(1, reportTitle)
			require.NoError(t, err)

			strategy := CommentPerRunStrategy{Title: "Digger run report", TimeOfRun: fixedTimeOfRun}
			_, _, err = strategy.Report(svc, 1, "plan output", identityFormatter, collapsible)
			require.NoError(t, err)

			body := onlyBody(t, svc, 1)
			assert.Contains(t, body, reportTitle)
			assert.Contains(t, body, "plan output")
		})
	}
}

// Non-collapsible reporters (bitbucket) have a different wrapper and so a
// different overhead; the result must still fit.
func TestCommentPerRunStrategyTrimsWithoutMarkdownSupport(t *testing.T) {
	svc := newMockCiService()
	strategy := CommentPerRunStrategy{Title: "Digger run report", TimeOfRun: fixedTimeOfRun}

	_, _, err := strategy.Report(svc, 1, strings.Repeat("a", GithubCommentMaxLength*2), identityFormatter, false)
	assert.NoError(t, err)

	body := onlyBody(t, svc, 1)
	assert.Equal(t, GithubCommentMaxLength, utf8.RuneCountInString(body))
	assert.NotContains(t, body, "<details")
}
