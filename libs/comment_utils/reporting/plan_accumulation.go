package reporting

import (
	"fmt"
	"strings"

	"github.com/diggerhq/digger/libs/scheduler"
)

// AccumulatedPlan is one project's plan as the backend knows it: the name to show a reviewer, the
// job's current status and whatever output the runner sent back.
type AccumulatedPlan struct {
	DisplayName string
	Status      scheduler.DiggerJobStatus
	Output      string
}

// PlanGroupHeader labels a group by its position in the batch: offset is the index of the group's
// first project, count its project count, total the batch's project count.
func PlanGroupHeader(offset, count, total int) string {
	return fmt.Sprintf("## Digger plan output (plans %v-%v of %v)", offset+1, offset+count, total)
}

func renderAccumulatedPlan(plan AccumulatedPlan) string {
	if plan.Status == scheduler.DiggerJobSucceeded && plan.Output != "" {
		return GetTerraformOutputAsCollapsibleComment("Plan for "+plan.DisplayName, false)(plan.Output)
	}
	// ToEmoji keeps this comment's markers the same as the ones the check run summary uses for the
	// same job status, so one PR does not show a project as pending in two different alphabets.
	return plan.Status.ToEmoji() + " **" + plan.DisplayName + "** - " + planStatusNote(plan.Status)
}

func planStatusNote(status scheduler.DiggerJobStatus) string {
	switch status {
	case scheduler.DiggerJobSucceeded:
		return "plan complete, no output captured"
	case scheduler.DiggerJobFailed:
		return "plan failed, see the job logs"
	case scheduler.DiggerJobStarted, scheduler.DiggerJobTriggered:
		return "planning"
	default:
		return "pending"
	}
}

func renderAccumulatedPlans(header string, plans []AccumulatedPlan) string {
	sections := make([]string, 0, len(plans)+1)
	sections = append(sections, header)
	for _, plan := range plans {
		sections = append(sections, renderAccumulatedPlan(plan))
	}
	// Blank lines between sections, not single newlines. A succeeded plan ends in "</details>", which
	// opens a CommonMark HTML block that closes only at a blank line, so a status line one newline
	// later is swallowed into it and shown verbatim instead of rendered. This is why
	// GetTerraformOutputAsCollapsibleComment already puts a blank line after "</summary>".
	return strings.Join(sections, "\n\n")
}

// RenderAccumulatedPlans builds a whole comment body from database state. It is a full replacement,
// never an append, so two concurrent renders cannot lose a plan. The backend renders the body
// itself, so there is no wrapper to reserve overhead for.
func RenderAccumulatedPlans(header string, plans []AccumulatedPlan) string {
	return TrimToCommentLimit(renderAccumulatedPlans(header, plans), 0)
}
