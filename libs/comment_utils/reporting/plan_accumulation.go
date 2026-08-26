package reporting

import (
	"fmt"
	"strings"

	"github.com/diggerhq/digger/libs/scheduler"
)

// AccumulatedPlan is one project's plan as the backend knows it: the project name it is keyed by,
// the alias to show a reviewer, the job's current status and whatever output the runner sent back.
type AccumulatedPlan struct {
	ProjectName string
	DisplayName string
	Status      scheduler.DiggerJobStatus
	Output      string
}

// ChunkProjects splits project names into groups of at most size, preserving order.
func ChunkProjects(projects []string, size int) [][]string {
	var groups [][]string
	for start := 0; start < len(projects); start += size {
		groups = append(groups, projects[start:min(start+size, len(projects))])
	}
	return groups
}

// PlanGroupHeader labels a group by its position in the batch: offset is the index of the group's
// first project, count its project count, total the batch's project count.
func PlanGroupHeader(offset, count, total int) string {
	return fmt.Sprintf("## Digger plan output (plans %v-%v of %v)", offset+1, offset+count, total)
}

func renderAccumulatedPlan(plan AccumulatedPlan) string {
	switch {
	case plan.Status == scheduler.DiggerJobSucceeded && plan.Output != "":
		return GetTerraformOutputAsCollapsibleComment("Plan for "+plan.DisplayName, false)(plan.Output)
	case plan.Status == scheduler.DiggerJobSucceeded:
		return ":white_check_mark: **" + plan.DisplayName + "** - plan complete, no output captured"
	case plan.Status == scheduler.DiggerJobFailed:
		return ":x: **" + plan.DisplayName + "** - plan failed, see the job logs"
	default:
		return ":hourglass_flowing_sand: **" + plan.DisplayName + "** - pending"
	}
}

func renderAccumulatedPlans(header string, plans []AccumulatedPlan) string {
	sections := make([]string, 0, len(plans)+1)
	sections = append(sections, header+"\n")
	for _, plan := range plans {
		sections = append(sections, renderAccumulatedPlan(plan))
	}
	return strings.Join(sections, "\n")
}

// RenderAccumulatedPlans builds a whole comment body from database state. It is a full replacement,
// never an append, so two concurrent renders cannot lose a plan. The backend renders the body
// itself, so there is no wrapper to reserve overhead for.
func RenderAccumulatedPlans(header string, plans []AccumulatedPlan) string {
	return TrimToCommentLimit(renderAccumulatedPlans(header, plans), 0)
}
