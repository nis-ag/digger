package reporting

import (
	"strings"
	"unicode/utf8"
)

const terraformFenceLanguage = "terraform"

// fencedBody is a comment body split into the parts that must survive verbatim and the
// ```terraform block contents that may shrink. len(protected) == len(blocks)+1, and the body is
// protected[0] + "\n" + blocks[0] + "\n" + protected[1] + … — so joining them round-trips it.
type fencedBody struct {
	protected []string
	blocks    []string
}

func (f fencedBody) join() string {
	pieces := make([]string, 0, len(f.protected)+len(f.blocks))
	for i, segment := range f.protected {
		pieces = append(pieces, segment)
		if i < len(f.blocks) {
			pieces = append(pieces, f.blocks[i])
		}
	}
	return strings.Join(pieces, "\n")
}

// fenceDelimiter parses a line as a code fence delimiter: backticks is the length of its leading
// backtick run, language the info string after it. Fewer than three backticks delimit nothing, and
// a run of four is a bare fence rather than a fence tagged with a backtick.
func fenceDelimiter(line string) (language string, backticks int, ok bool) {
	trimmed := strings.TrimSpace(line)
	backticks = len(trimmed) - len(strings.TrimLeft(trimmed, "`"))
	if backticks < 3 {
		return "", 0, false
	}
	return strings.TrimSpace(trimmed[backticks:]), backticks, true
}

// terraformFence returns the delimiter to wrap content in: a backtick run longer than any run
// content itself carries, so nothing inside can close the block early. Real plan output renders a
// multi-line string attribute as an indented heredoc, so a resource holding markdown puts a bare
// ``` in the plan; a fence that short would end there, spill the rest of the comment out of the
// block as untrimmable prose, and leave GitHub rendering from the wrong side of a code fence.
//
// Runs are measured anywhere in the content, not just where a line starts one: elideBlock cuts on a
// rune boundary, so a run that sits mid-line in the pristine plan can end up starting the line that
// begins the surviving tail.
func terraformFence(content string) string {
	longest, run := 2, 0 // a run shorter than a fence cannot close one
	for _, r := range content {
		if r != '`' {
			run = 0
			continue
		}
		if run++; run > longest {
			longest = run
		}
	}
	return strings.Repeat("`", longest+1)
}

// splitFencedBody reports ok == false when the body ends inside a fence: the parse cannot be
// trusted and join() would not round-trip it.
func splitFencedBody(body string) (fencedBody, bool) {
	var out fencedBody
	var current, block []string
	inFence := false
	openLanguage := ""
	openBackticks := 0

	for _, line := range strings.Split(body, "\n") {
		language, backticks, isFence := fenceDelimiter(line)
		switch {
		case !inFence && isFence:
			inFence, openLanguage, openBackticks = true, language, backticks
			current = append(current, line)
			if language == terraformFenceLanguage {
				out.protected = append(out.protected, strings.Join(current, "\n"))
				current, block = nil, nil
			}
		case inFence && isFence && language == "" && backticks >= openBackticks:
			// A fence closes only on a bare run at least as long as the one that opened it. A
			// shorter run, or one carrying a language tag, is content.
			inFence = false
			if openLanguage == terraformFenceLanguage {
				out.blocks = append(out.blocks, strings.Join(block, "\n"))
				block = nil
			}
			current = append(current, line)
		case inFence && openLanguage == terraformFenceLanguage:
			block = append(block, line)
		default:
			current = append(current, line)
		}
	}

	if inFence {
		return fencedBody{}, false
	}
	out.protected = append(out.protected, strings.Join(current, "\n"))
	return out, true
}

// trimTerraformBlocks fits body into budget by shrinking only ```terraform block content, leaving
// every other rune of the body untouched. Allocation is greedy and front-first: every block is
// first reserved the floor it needs to stay readable, then the whole remainder goes to the
// earliest block until it holds its complete plan, then to the next, and so on. Blocks that get no
// share sit at the floor.
//
// Reports ok == false when the body carries no terraform block, when its fences do not parse, or
// when shrinking every block still does not fit — the caller then falls back to a tail cut.
func trimTerraformBlocks(body string, budget int) (string, bool) {
	split, ok := splitFencedBody(body)
	if !ok || len(split.blocks) == 0 {
		return "", false
	}

	sizes := make([]int, len(split.blocks))
	free := budget - 2*len(split.blocks) // the "\n" on either side of every block
	for i, block := range split.blocks {
		sizes[i] = utf8.RuneCountInString(block)
	}
	for _, segment := range split.protected {
		free -= utf8.RuneCountInString(segment)
	}

	floor := minTerraformHeadRunes + utf8.RuneCountInString(terraformElisionMarker) + minTerraformTailRunes

	allocations := make([]int, len(sizes))
	remaining := free
	for i, size := range sizes {
		allocations[i] = min(size, floor)
		remaining -= allocations[i]
	}
	if remaining < 0 {
		// Not even the floor fits for every block. Split what there is evenly; each block ends up
		// below the floor, or empty when a marker alone does not fit.
		share := max(free, 0) / len(sizes)
		for i, size := range sizes {
			allocations[i] = min(size, share)
		}
	} else {
		for i, size := range sizes {
			give := min(size-allocations[i], remaining)
			allocations[i] += give
			remaining -= give
		}
	}

	for i, block := range split.blocks {
		split.blocks[i] = elideBlock(block, allocations[i])
	}

	trimmed := split.join()
	if utf8.RuneCountInString(trimmed) > budget {
		return "", false
	}
	return trimmed, true
}

// elideBlock shrinks one ```terraform block to at most target runes, keeping a prefix and a
// suffix of the plan joined by one terraformElisionMarker. A block that already carries a marker
// is re-split at it, so a re-trim never nests a second marker and the surviving head and tail
// stay a prefix and a suffix of the pristine plan.
func elideBlock(content string, target int) string {
	runes := []rune(content)
	markerRunes := utf8.RuneCountInString(terraformElisionMarker)

	if len(runes) <= target {
		return content
	}
	if target < markerRunes {
		return ""
	}
	keep := target - markerRunes

	head, tail := runes, runes
	if i := strings.Index(content, terraformElisionMarker); i >= 0 {
		head = []rune(content[:i])
		tail = []rune(content[i+len(terraformElisionMarker):])
	}

	tailKeep := min(minTerraformTailRunes, keep/2, len(tail))
	headKeep := min(keep-tailKeep, len(head))
	return string(head[:headKeep]) + terraformElisionMarker + string(tail[len(tail)-tailKeep:])
}
