package reporting

import (
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/diggerhq/digger/libs/ci"
)

const GithubCommentMaxLength = 65536

const commentTruncationMarker = "\n\n[output truncated - see the linked full plan or the job logs]"

const minTerraformHeadRunes = 500
const minTerraformTailRunes = 500
const terraformElisionMarker = "\n\n[... plan output trimmed, see the linked full plan ...]\n\n"

func TrimToCommentLimit(body string, overhead int) string {
	budget := GithubCommentMaxLength - overhead
	length := utf8.RuneCountInString(body)
	if length <= budget {
		return body
	}

	slog.Warn("comment body too long, trimming",
		"originalLength", length,
		"maxLength", GithubCommentMaxLength,
		"overhead", overhead)

	if trimmed, ok := trimTerraformBlocks(body, budget); ok {
		return trimmed
	}
	return truncateTail(body, budget)
}

func truncateTail(body string, budget int) string {
	keep := budget - utf8.RuneCountInString(commentTruncationMarker)
	if keep < 0 {
		keep = 0
	}
	return string([]rune(body)[:keep]) + commentTruncationMarker
}

type CiReporter struct {
	CiService         ci.PullRequestService
	PrNumber          int
	IsSupportMarkdown bool
	ReportStrategy    ReportStrategy
}

func (ciReporter CiReporter) Report(report string, reportFormatting func(report string) string) (string, string, error) {
	commentId, commentUrl, err := ciReporter.ReportStrategy.Report(ciReporter.CiService, ciReporter.PrNumber, report, reportFormatting, ciReporter.SupportsMarkdown())
	return commentId, commentUrl, err
}

func (ciReporter CiReporter) Flush() (string, string, error) {
	return "", "", nil
}

func (ciReporter CiReporter) Suppress() error {
	return nil
}

func (ciReporter CiReporter) SupportsMarkdown() bool {
	return ciReporter.IsSupportMarkdown
}

type CiReporterLazy struct {
	CiReporter   CiReporter
	isSuppressed bool
	reports      []string
	formatters   []func(report string) string
}

func NewCiReporterLazy(ciReporter CiReporter) *CiReporterLazy {
	return &CiReporterLazy{
		CiReporter:   ciReporter,
		isSuppressed: false,
		reports:      []string{},
		formatters:   []func(report string) string{},
	}
}

func (lazyReporter *CiReporterLazy) Report(report string, reportFormatting func(report string) string) (string, string, error) {
	lazyReporter.reports = append(lazyReporter.reports, report)
	lazyReporter.formatters = append(lazyReporter.formatters, reportFormatting)
	return "", "", nil
}

func (lazyReporter *CiReporterLazy) Flush() (string, string, error) {
	if lazyReporter.isSuppressed {
		slog.Info("reporter is suppressed, ignoring messages")
		return "", "", nil
	}
	var commentId, commentUrl string
	for i := range lazyReporter.formatters {
		var err error
		commentId, commentUrl, err = lazyReporter.CiReporter.ReportStrategy.Report(lazyReporter.CiReporter.CiService, lazyReporter.CiReporter.PrNumber, lazyReporter.reports[i], lazyReporter.formatters[i], lazyReporter.SupportsMarkdown())
		if err != nil {
			slog.Error("failed to report strategy", "error", err)
			return "", "", err
		}
	}
	// clear the buffers
	lazyReporter.formatters = []func(comment string) string{}
	lazyReporter.reports = []string{}
	return commentId, commentUrl, nil
}

func (lazyReporter *CiReporterLazy) Suppress() error {
	lazyReporter.isSuppressed = true
	return nil
}

func (lazyReporter *CiReporterLazy) SupportsMarkdown() bool {
	return lazyReporter.CiReporter.IsSupportMarkdown
}

type StdOutReporter struct{}

func (reporter StdOutReporter) Report(report string, reportFormatting func(report string) string) (string, string, error) {
	slog.Info("report", "content", report)
	return "", "", nil
}

func (reporter StdOutReporter) Flush() (string, string, error) {
	return "", "", nil
}

func (reporter StdOutReporter) SupportsMarkdown() bool {
	return false
}

func (reporter StdOutReporter) Suppress() error {
	return nil
}

type ReportStrategy interface {
	Report(ciService ci.PullRequestService, PrNumber int, report string, reportFormatter func(report string) string, supportsCollapsibleComment bool) (commentId string, commentUrl string, error error)
}

type CommentPerRunStrategy struct {
	Title     string
	TimeOfRun time.Time
}

func (strategy CommentPerRunStrategy) Report(ciService ci.PullRequestService, PrNumber int, report string, reportFormatter func(report string) string, supportsCollapsibleComment bool) (string, string, error) {
	comments, err := ciService.GetComments(PrNumber)
	if err != nil {
		slog.Error("error getting comments", "error", err, "prNumber", PrNumber)
		return "", "", fmt.Errorf("error getting comments: %v", err)
	}

	var reportTitle string
	if strategy.Title != "" {
		reportTitle = strategy.Title + " " + strategy.TimeOfRun.Format("2006-01-02 15:04:05 (MST)")
	} else {
		reportTitle = "Digger run report at " + strategy.TimeOfRun.Format("2006-01-02 15:04:05 (MST)")
	}
	commentId, commentUrl, err := upsertComment(ciService, PrNumber, report, reportFormatter, comments, reportTitle, supportsCollapsibleComment)
	return commentId, commentUrl, err
}

func upsertComment(ciService ci.PullRequestService, PrNumber int, report string, reportFormatter func(report string) string, comments []ci.Comment, reportTitle string, supportsCollapsible bool) (string, string, error) {
	report = reportFormatter(report)
	commentIdForThisRun := ""
	var commentBody string
	var commentUrl string
	for _, comment := range comments {
		if strings.Contains(*comment.Body, reportTitle) {
			commentIdForThisRun = comment.Id
			commentBody = *comment.Body
			commentUrl = comment.Url
			break
		}
	}

	wrapFn := AsComment(reportTitle)
	if supportsCollapsible {
		wrapFn = AsCollapsibleComment(reportTitle, false)
	}
	overhead := utf8.RuneCountInString(wrapFn(""))

	if commentIdForThisRun == "" {
		comment, err := ciService.PublishComment(PrNumber, wrapFn(TrimToCommentLimit(report, overhead)))
		if err != nil {
			slog.Error("error publishing comment", "error", err, "prNumber", PrNumber)
			return "", "", fmt.Errorf("error publishing comment: %v", err)
		}
		return fmt.Sprintf("%v", comment.Id), comment.Url, nil
	}

	// Strip the wrapper added last time: AsCollapsibleComment wraps both sides, AsComment
	// only prepends a title line. A body edited by hand may be down to the title alone, so
	// there is not always a closing line to drop.
	lines := strings.Split(commentBody, "\n")
	if supportsCollapsible && len(lines) > 1 {
		lines = lines[1 : len(lines)-1]
	} else {
		lines = lines[1:]
	}
	commentBody = strings.Join(lines, "\n")

	commentBody = commentBody + "\n\n" + report + "\n"

	completeComment := wrapFn(TrimToCommentLimit(commentBody, overhead))

	err := ciService.EditComment(PrNumber, commentIdForThisRun, completeComment)

	if err != nil {
		slog.Error("error editing comment", "error", err, "commentId", commentIdForThisRun, "prNumber", PrNumber)
		return "", "", fmt.Errorf("error editing comment: %v", err)
	}
	return fmt.Sprintf("%v", commentIdForThisRun), commentUrl, nil
}

type LatestRunCommentStrategy struct {
	TimeOfRun time.Time
}

func (strategy LatestRunCommentStrategy) Report(ciService ci.PullRequestService, PrNumber int, report string, reportFormatter func(report string) string, supportsCollapsibleComment bool) (string, string, error) {
	comments, err := ciService.GetComments(PrNumber)
	if err != nil {
		slog.Error("error getting comments", "error", err, "prNumber", PrNumber)
		return "", "", fmt.Errorf("error getting comments: %v", err)
	}

	reportTitle := "Digger latest run report"
	commentId, commentUrl, err := upsertComment(ciService, PrNumber, report, reportFormatter, comments, reportTitle, supportsCollapsibleComment)
	return commentId, commentUrl, err
}

type MultipleCommentsStrategy struct{}

func (strategy MultipleCommentsStrategy) Report(ciService ci.PullRequestService, PrNumber int, report string, reportFormatter func(report string) string, supportsCollapsibleComment bool) (string, string, error) {
	comment, err := ciService.PublishComment(PrNumber, TrimToCommentLimit(reportFormatter(report), 0))
	if err != nil {
		slog.Error("error publishing comment", "error", err, "prNumber", PrNumber)
		return "", "", err
	}
	return comment.Id, comment.Url, nil
}
