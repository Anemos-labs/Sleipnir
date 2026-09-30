package swarm

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/tools"
)

// Evidence is what the harness itself observed an agent do. Status shown on the
// board and the case for "done" are derived from it, not from what the model
// says about its own progress: self-reports are optimistic and stale.
type Evidence struct {
	mu       sync.Mutex
	edited   map[string]int
	reads    int
	calls    int
	commands []CmdRecord
}

// CmdRecord is one shell command and its exit status.
type CmdRecord struct {
	Cmd  string
	Exit int
	At   time.Time
	Test bool
}

var (
	exitRe = regexp.MustCompile(`\[exit code (-?\d+)\]\s*$`)
	testRe = regexp.MustCompile(`(?i)\b(go test|go vet|npm (run )?test|npx (jest|vitest)|pnpm test|yarn test|pytest|python -m pytest|cargo test|make (test|check)|mvn test|gradle test|dotnet test|ctest|rspec|phpunit)\b`)
)

// NewEvidence returns an empty record.
func NewEvidence() *Evidence { return &Evidence{edited: map[string]int{}} }

// Observe folds one finished tool call into the record.
func (e *Evidence) Observe(call core.Block, res *tools.Result, now time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	switch call.ToolName {
	case "edit", "write":
		if !res.IsError {
			if p := jsonString(call.Input, "path", "file_path"); p != "" {
				e.edited[p]++
			}
		}
	case "apply_patch":
		if !res.IsError {
			for _, l := range strings.Split(jsonString(call.Input, "patch"), "\n") {
				for _, pre := range []string{"*** Update File: ", "*** Add File: ", "*** Delete File: "} {
					if strings.HasPrefix(l, pre) {
						e.edited[strings.TrimSpace(strings.TrimPrefix(l, pre))]++
					}
				}
			}
		}
	case "read", "grep", "glob", "ls":
		e.reads++
	case "bash":
		cmd := jsonString(call.Input, "command")
		rec := CmdRecord{Cmd: firstLine(cmd, 100), Exit: 0, At: now, Test: testRe.MatchString(cmd)}
		if m := exitRe.FindStringSubmatch(res.Text); m != nil {
			rec.Exit, _ = strconv.Atoi(m[1])
		} else if res.IsError {
			rec.Exit = -1
		}
		e.commands = append(e.commands, rec)
		if len(e.commands) > 40 {
			e.commands = e.commands[len(e.commands)-40:]
		}
	}
}

// Edited lists edited paths, sorted.
func (e *Evidence) Edited() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, 0, len(e.edited))
	for p := range e.edited {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// LastTest returns the most recent test-like command.
func (e *Evidence) LastTest() (CmdRecord, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := len(e.commands) - 1; i >= 0; i-- {
		if e.commands[i].Test {
			return e.commands[i], true
		}
	}
	return CmdRecord{}, false
}

// Summary renders the evidence as one line for the board and the manager.
func (e *Evidence) Summary() string {
	files := e.Edited()
	var parts []string
	switch n := len(files); {
	case n == 0:
		parts = append(parts, "no files edited")
	case n <= 4:
		parts = append(parts, fmt.Sprintf("edited %d (%s)", n, strings.Join(base(files), ", ")))
	default:
		parts = append(parts, fmt.Sprintf("edited %d (%s, …)", n, strings.Join(base(files[:4]), ", ")))
	}
	if t, ok := e.LastTest(); ok {
		verdict := "passed"
		if t.Exit != 0 {
			verdict = fmt.Sprintf("FAILED (exit %d)", t.Exit)
		}
		parts = append(parts, fmt.Sprintf("last test %q %s", t.Cmd, verdict))
	} else {
		parts = append(parts, "no tests run")
	}
	return strings.Join(parts, "; ")
}

// Counts reports raw counters.
func (e *Evidence) Counts() (edits, reads, calls int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, n := range e.edited {
		edits += n
	}
	return edits, e.reads, e.calls
}

func base(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		if j := strings.LastIndexAny(p, `/\`); j >= 0 {
			p = p[j+1:]
		}
		out[i] = p
	}
	return out
}

func jsonString(in json.RawMessage, keys ...string) string {
	var m map[string]any
	if json.Unmarshal(in, &m) != nil {
		return ""
	}
	for _, k := range keys {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func firstLine(s string, max int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	r := []rune(s)
	if len(r) > max {
		return string(r[:max-1]) + "…"
	}
	return s
}

// activity describes what an agent is doing right now, from its tool calls.
func activity(call core.Block) string {
	switch call.ToolName {
	case "read":
		return "reading " + shortPath(jsonString(call.Input, "path", "file_path"))
	case "edit", "write":
		return "editing " + shortPath(jsonString(call.Input, "path", "file_path"))
	case "apply_patch":
		return "applying a patch"
	case "bash":
		return "running `" + firstLine(jsonString(call.Input, "command"), 40) + "`"
	case "grep", "glob":
		return "searching"
	case "recall":
		return "recalling earlier context"
	case "wait":
		return "waiting for the team"
	case "web_fetch", "web_search":
		return "browsing"
	}
	return call.ToolName
}

func shortPath(p string) string {
	if p == "" {
		return "a file"
	}
	if j := strings.LastIndexAny(p, `/\`); j >= 0 {
		return p[j+1:]
	}
	return p
}
