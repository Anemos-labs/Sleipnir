package swarm

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/gitx"
	"github.com/anemos-labs/sleipnir/internal/workspace"
)

// The team, its board and its mail exist for one run. What the run leaves behind
// (code, comments, documentation, commit messages) is read by people who never saw
// it, for whom "fe-1" or "T12" means nothing. These helpers find such references
// so the harness can keep them out of the project.

// agentIDShape is the shape of a team member's id ("fe-1", "be-12"): a role
// prefix and a number. Ids of another shape (the manager's) are ordinary words
// that a project may well use, so they are not looked for.
var agentIDShape = regexp.MustCompile(`^[a-z][a-z0-9]*-\d+$`)

// boardRef matches a mention of a board task in prose: "task T12", "[T3]", "(T4)"
// or a bare id ("T12"). It is used only on text the harness writes from a worker's
// report, never on code, where T1 is as likely a type parameter.
var boardRef = `\[T\d+\]|\(T\d+\)|\bT\d+\b`

// codeBoardRef matches the board references that are unambiguous in code too:
// "task T12" and "[T12]".
var codeBoardRef = `(?i:\btasks?\s+T\d+\b)|\[T\d+\]`

// runRefs matches mentions of the team members ids (those of agentIDShape) or,
// with board, of board tasks. Nil when there is nothing to look for.
func runRefs(ids []string, board string) *regexp.Regexp {
	var alts []string
	for _, id := range ids {
		if agentIDShape.MatchString(id) {
			alts = append(alts, regexp.QuoteMeta(id))
		}
	}
	sort.Strings(alts)
	var parts []string
	if len(alts) > 0 {
		// Not inside a longer token: "fe-1" is not in "fe-10" or "safe-1x".
		parts = append(parts, `(?:^|[^\w-])(?:`+strings.Join(alts, "|")+`)(?:$|[^\w-])`)
	}
	if board != "" {
		parts = append(parts, board)
	}
	if len(parts) == 0 {
		return nil
	}
	return regexp.MustCompile(strings.Join(parts, "|"))
}

// scrubRunRefs drops from a worker's report the sentences that mention a team
// member or a board task, so what remains reads as a description of the change.
// Line structure is kept; a line left empty is removed.
func scrubRunRefs(text string, ids []string) string {
	re := runRefs(ids, boardRef)
	if re == nil || !re.MatchString(text) {
		return text
	}
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if !re.MatchString(line) {
			out = append(out, line)
			continue
		}
		var kept []string
		for _, s := range sentences(line) {
			if !re.MatchString(s) {
				kept = append(kept, strings.TrimSpace(s))
			}
		}
		if l := strings.Join(kept, " "); strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// sentences splits a line after each ". ", "; ", "! " or "? ".
func sentences(line string) []string {
	var out []string
	start := 0
	for i := 0; i+1 < len(line); i++ {
		switch line[i] {
		case '.', ';', '!', '?':
			if line[i+1] == ' ' {
				out = append(out, line[start:i+1])
				start = i + 2
			}
		}
	}
	return append(out, line[start:])
}

// mentionsRun reports whether text names a team member or a board task.
func mentionsRun(text string, ids []string) bool {
	re := runRefs(ids, boardRef)
	return re != nil && re.MatchString(text)
}

// runRefLeak is one added line of a change that names a team member or a board task.
type runRefLeak struct {
	Path string
	Line int
	Text string
}

// String formats the leak as path:line: text.
func (l runRefLeak) String() string {
	return fmt.Sprintf("%s:%d: %s", l.Path, l.Line, l.Text)
}

// findRunRefs lists, up to max, the lines a patch adds that name a team member or
// a board task. Lockfiles are skipped: no person writes them.
func findRunRefs(patch string, ids []string, max int) []runRefLeak {
	re := runRefs(ids, codeBoardRef)
	if re == nil {
		return nil
	}
	var out []runRefLeak
	path, line := "", 0
	for _, l := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(l, "+++ "):
			path = strings.TrimPrefix(strings.TrimPrefix(l, "+++ "), "b/")
			if path == "/dev/null" || workspace.IsLockfile(path) {
				path = ""
			}
		case strings.HasPrefix(l, "@@"):
			line = hunkStart(l)
		case strings.HasPrefix(l, "+"):
			if path != "" && re.MatchString(l[1:]) {
				out = append(out, runRefLeak{Path: path, Line: line, Text: cleanText(l[1:], 120)})
				if len(out) == max {
					return out
				}
			}
			line++
		case strings.HasPrefix(l, " "):
			line++
		}
	}
	return out
}

// hunkStart is the first new-file line of a hunk header "@@ -a,b +c,d @@".
func hunkStart(header string) int {
	_, after, ok := strings.Cut(header, " +")
	if !ok {
		return 0
	}
	n, _, _ := strings.Cut(after, " ")
	n, _, _ = strings.Cut(n, ",")
	v, _ := strconv.Atoi(n)
	return v
}

// runRefNotice checks the change m's tree holds for t for lines that name team
// members or board tasks and returns what to tell the worker, once per task
// revision: the next done after a notice goes through, so a reference the worker
// judges legitimate never holds the task. "" when there is nothing to say.
func (s *Swarm) runRefNotice(ctx context.Context, m *member, t Task) string {
	s.mu.Lock()
	warned, ok := s.runRefWarned[t.ID]
	s.mu.Unlock()
	if ok && warned == t.Rev {
		return ""
	}
	d, err := m.tree.DiffWith(ctx, gitx.DiffOptions{})
	if err != nil || d == nil {
		return ""
	}
	leaks := findRunRefs(d.Patch, s.everyMember(), 8)
	if len(leaks) == 0 {
		return ""
	}
	s.mu.Lock()
	if s.runRefWarned == nil {
		s.runRefWarned = map[string]uint64{}
	}
	s.runRefWarned[t.ID] = t.Rev
	s.mu.Unlock()
	var b strings.Builder
	b.WriteString("Not done yet: your change names team members or board tasks, which mean nothing to whoever reads the project after this run. " +
		"Say what the code does or why instead (or drop the line), then call done again:\n")
	for _, l := range leaks {
		b.WriteString("- " + l.String() + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// everyMember lists, sorted, the id of every team member the run registered,
// including those that have since retired: a successor may still name them.
func (s *Swarm) everyMember() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.teamIDs))
	for id := range s.teamIDs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
