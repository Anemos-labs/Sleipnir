package session

import (
	"fmt"
	"sort"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/memory"
)

// The allowance is selected once at session creation. Model switches and worker
// roles must not independently change the shared prompt bytes.
func instructionTokenBudget(configured, contextWindow int) int {
	if configured > 0 {
		return configured
	}
	budget := 16384
	if contextWindow > 0 {
		budget = min(budget, max(1, contextWindow/4))
	}
	return budget
}

// fitInstructions keeps higher-precedence sources intact first, then uses any
// remaining room for the beginning of the next source, ending at a line boundary.
// Files that already fit keep exactly their original rendered bytes.
func fitInstructions(srcs []memory.Source, budget int, est core.Estimator) (string, []string) {
	full := memory.Render(srcs)
	if est.Tokens(full) <= budget {
		return full, nil
	}
	paths := make([]string, len(srcs))
	for i, src := range srcs {
		paths[i] = src.Path
	}
	// Keep the note inside the budget too. The notice shown to the user includes
	// every affected path; the prompt includes as many as the allowance permits.
	note := instructionCutNote(paths)
	if est.Tokens(note) > budget {
		return "", paths
	}
	kept := ""
	for i := len(srcs) - 1; i >= 0; i-- {
		candidate := memory.Render(srcs[i:])
		if est.Tokens(candidate+instructionCutNote(paths[:i])) <= budget {
			kept = candidate
			continue
		}
		note = instructionCutNote(paths[:i+1])
		// All later sources fit whole. Search line endings, never bytes, so a
		// UTF-8 character or a single instruction line cannot be cut in half.
		lines := strings.SplitAfter(srcs[i].Text, "\n")
		ends := []int{0}
		for _, line := range lines {
			ends = append(ends, ends[len(ends)-1]+len(line))
		}
		partial := func(n int) string {
			if n == 0 {
				return kept + note
			}
			src := srcs[i]
			src.Text = src.Text[:ends[n]]
			return memory.Render([]memory.Source{src}) + "\n" + kept + note
		}
		n := sort.Search(len(ends), func(n int) bool { return est.Tokens(partial(n)) > budget }) - 1
		if n < 0 {
			// Adding another path can enlarge the note. Preserve the previous
			// fitted suffix rather than let metadata push it over the allowance.
			return kept + "\n[instruction files truncated; read the original files for complete rules]\n", paths[:i+1]
		}
		return partial(n), paths[:i+1]
	}
	return kept, nil
}

func instructionCutNote(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	var names []string
	for _, path := range paths[:min(3, len(paths))] {
		// Limit metadata for long filenames without breaking UTF-8.
		runes := []rune(path)
		if len(runes) > 80 {
			path = string(runes[:77]) + "..."
		}
		names = append(names, fmt.Sprintf("%q", path))
	}
	if len(paths) > 3 {
		names = append(names, fmt.Sprintf("and %d more", len(paths)-3))
	}
	return "\n[instruction files truncated or omitted: " + strings.Join(names, ", ") + "; read the original files for complete rules]\n"
}
