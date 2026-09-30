package swarm

import (
	"encoding/json"
	"fmt"
	"path"
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
	Cmd string
	// Exit is the command's exit status when Known. A command that timed out, was
	// cancelled or failed without reporting a status is recorded as -1.
	Exit  int
	Known bool
	At    time.Time
	// Test marks a test, vet or check command (the program being run is a test
	// runner, not a command that merely mentions one).
	Test bool
	// Masked marks a test whose exit status does not reach the shell's: it is
	// piped into something, or followed by another command.
	Masked bool
}

// Passed reports whether the record is a test the harness saw succeed.
func (c CmdRecord) Passed() bool { return c.Known && !c.Masked && c.Exit == 0 }

// exitRe finds the "[exit code N]" mark the bash tool appends. The last one wins:
// the tool writes it after the output, so a command that prints the same text
// cannot override it.
var exitRe = regexp.MustCompile(`\[exit code (-?\d+)\]`)

// NewEvidence returns an empty record.
func NewEvidence() *Evidence { return &Evidence{edited: map[string]int{}} }

// exitOf recovers a command's exit status. The structured status in Result.Meta
// is authoritative (it survives output truncation); the mark in the text is the
// fallback, and when neither is there the status is unknown, never zero.
func exitOf(res *tools.Result) (code int, known bool) {
	if res == nil {
		return -1, true
	}
	if b, _ := res.Meta["timed_out"].(bool); b {
		return -1, true
	}
	if v, ok := res.Meta["exit_code"]; ok {
		switch x := v.(type) {
		case int:
			return x, true
		case int32:
			return int(x), true
		case int64:
			return int(x), true
		case uint8:
			return int(x), true
		case float64:
			return int(x), true
		case json.Number:
			if n, err := x.Int64(); err == nil {
				return int(n), true
			}
		}
	}
	if ms := exitRe.FindAllStringSubmatch(res.Text, -1); len(ms) > 0 {
		n, _ := strconv.Atoi(ms[len(ms)-1][1])
		return n, true
	}
	if res.IsError {
		return -1, true
	}
	return 0, false
}

// Observe folds one finished tool call into the record.
func (e *Evidence) Observe(call core.Block, res *tools.Result, now time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	failed := res == nil || res.IsError
	switch call.ToolName {
	case "edit", "write":
		if !failed {
			if p := jsonString(call.Input, "path", "file_path"); p != "" {
				e.noteEdit(p)
			}
		}
	case "apply_patch":
		if !failed {
			for _, l := range strings.Split(jsonString(call.Input, "patch"), "\n") {
				for _, pre := range []string{"*** Update File: ", "*** Add File: ", "*** Delete File: "} {
					if strings.HasPrefix(l, pre) {
						e.noteEdit(strings.TrimSpace(strings.TrimPrefix(l, pre)))
					}
				}
			}
		}
	case "read", "grep", "glob", "ls":
		e.reads++
	case "bash":
		cmd := jsonString(call.Input, "command")
		isTest, masked := classifyCommand(cmd)
		rec := CmdRecord{Cmd: cleanText(firstLine(cmd, 100), 100), At: now, Test: isTest, Masked: masked}
		rec.Exit, rec.Known = exitOf(res)
		e.commands = append(e.commands, rec)
		if len(e.commands) > 40 {
			e.commands = e.commands[len(e.commands)-40:]
		}
	}
}

// noteEdit records an edited path (bounded: an agent editing thousands of files
// keeps a bounded record).
func (e *Evidence) noteEdit(p string) {
	if _, ok := e.edited[p]; !ok && len(e.edited) >= 512 {
		return
	}
	e.edited[p]++
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
		parts = append(parts, fmt.Sprintf("edited %d (%s)", n, cleanText(strings.Join(base(files), ", "), 90)))
	default:
		parts = append(parts, fmt.Sprintf("edited %d (%s, …)", n, cleanText(strings.Join(base(files[:4]), ", "), 90)))
	}
	if t, ok := e.LastTest(); ok {
		var verdict string
		switch {
		case !t.Known:
			verdict = "ran (exit status unknown)"
		case t.Masked:
			verdict = "ran (exit status masked by the command line)"
		case t.Exit != 0:
			verdict = fmt.Sprintf("FAILED (exit %d)", t.Exit)
		default:
			verdict = "passed"
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

// activity describes what an agent is doing right now, from the KIND of tool it
// is using. It never includes the call's arguments: file names and commands are
// chosen by the model (or by whoever wrote the files) and this line is shown to
// every teammate as harness-derived status.
func activity(call core.Block) string {
	switch call.ToolName {
	case "read", "ls":
		return "reading files"
	case "edit", "write", "apply_patch":
		return "editing files"
	case "bash":
		return "running a command"
	case "grep", "glob":
		return "searching"
	case "recall":
		return "recalling earlier context"
	case "wait":
		return "waiting for the team"
	case "web_fetch", "web_search":
		return "browsing"
	case "task", "mail", "note", "spawn":
		return "coordinating"
	}
	return "using " + safeToken(call.ToolName, 24)
}

// ---- shell-aware test detection ------------------------------------------------

// simpleCmd is one command of a shell command line and the operator after it.
type simpleCmd struct {
	words []string
	op    string // "", "&&", "||", ";", "|", "&"
}

// splitShell splits a command line into simple commands. It understands quotes,
// escapes and the control operators well enough to tell which program a command
// line runs; it does not expand anything.
func splitShell(line string) []simpleCmd {
	var out []simpleCmd
	var cur []string
	var word strings.Builder
	inWord := false
	flushWord := func() {
		if inWord {
			cur = append(cur, word.String())
			word.Reset()
			inWord = false
		}
	}
	endCmd := func(op string) {
		flushWord()
		if len(cur) > 0 || op != "" {
			out = append(out, simpleCmd{words: cur, op: op})
		}
		cur = nil
	}
	rs := []rune(line)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case c == '\'':
			inWord = true
			for i++; i < len(rs) && rs[i] != '\''; i++ {
				word.WriteRune(rs[i])
			}
		case c == '"':
			inWord = true
			for i++; i < len(rs) && rs[i] != '"'; i++ {
				if rs[i] == '\\' && i+1 < len(rs) {
					i++
				}
				word.WriteRune(rs[i])
			}
		case c == '\\' && i+1 < len(rs):
			inWord = true
			i++
			word.WriteRune(rs[i])
		case c == '`' || (c == '$' && i+1 < len(rs) && rs[i+1] == '('):
			// Command substitution: opaque, part of the word.
			inWord = true
			word.WriteRune(c)
			depth := 0
			for i++; i < len(rs); i++ {
				word.WriteRune(rs[i])
				if c == '`' && rs[i] == '`' {
					break
				}
				if c == '$' {
					if rs[i] == '(' {
						depth++
					} else if rs[i] == ')' {
						depth--
						if depth == 0 {
							break
						}
					}
				}
			}
		case c == '#' && !inWord:
			for i < len(rs) && rs[i] != '\n' {
				i++
			}
			i--
		case c == ' ' || c == '\t' || c == '\r':
			flushWord()
		case c == '\n' || c == ';':
			endCmd(";")
		case c == '&' && i+1 < len(rs) && rs[i+1] == '&':
			endCmd("&&")
			i++
		case c == '|' && i+1 < len(rs) && rs[i+1] == '|':
			endCmd("||")
			i++
		case c == '|':
			endCmd("|")
		case c == '&' && !(inWord && word.Len() > 0 && strings.HasSuffix(word.String(), ">")):
			// "&" backgrounds a command ("2>&1" keeps its ampersand).
			if i > 0 && (rs[i-1] == '>' || rs[i-1] == '<') {
				inWord = true
				word.WriteRune(c)
			} else {
				endCmd("&")
			}
		default:
			inWord = true
			word.WriteRune(c)
		}
	}
	endCmd("")
	return out
}

var assignRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// stripWrappers drops leading environment assignments and wrapper programs.
func stripWrappers(w []string) []string {
	for len(w) > 0 {
		switch {
		case assignRe.MatchString(w[0]):
			w = w[1:]
		case w[0] == "env" || w[0] == "time" || w[0] == "command" || w[0] == "exec" || w[0] == "nohup" || w[0] == "nice" || w[0] == "stdbuf":
			w = w[1:]
			for len(w) > 0 && (strings.HasPrefix(w[0], "-") || assignRe.MatchString(w[0])) {
				w = w[1:]
			}
		case w[0] == "timeout" && len(w) > 2:
			w = w[2:]
		default:
			return w
		}
	}
	return w
}

// isTestWords reports whether a command runs a test, vet or check.
func isTestWords(w []string) bool {
	w = stripWrappers(w)
	if len(w) == 0 {
		return false
	}
	prog := path.Base(w[0])
	rest := w[1:]
	// The first word after the program that is not a flag.
	sub := ""
	for _, x := range rest {
		if !strings.HasPrefix(x, "-") {
			sub = x
			break
		}
	}
	switch prog {
	case "go":
		return sub == "test" || sub == "vet"
	case "npm", "pnpm", "yarn", "bun":
		if sub == "test" || sub == "t" {
			return true
		}
		if sub == "run" || sub == "run-script" {
			for i, x := range rest {
				if x == sub && i+1 < len(rest) {
					return strings.HasPrefix(rest[i+1], "test")
				}
			}
		}
		return false
	case "npx":
		return sub == "jest" || sub == "vitest" || sub == "mocha"
	case "pytest", "py.test", "jest", "vitest", "ctest", "rspec", "phpunit", "tox", "nox":
		return true
	case "python", "python3":
		for i, x := range rest {
			if x == "-m" && i+1 < len(rest) {
				return rest[i+1] == "pytest" || rest[i+1] == "unittest"
			}
		}
		return false
	case "cargo":
		return sub == "test" || sub == "nextest" || sub == "clippy" || sub == "check"
	case "make", "gmake":
		return sub == "test" || sub == "check" || sub == "tests" || strings.HasPrefix(sub, "test-")
	case "mvn", "mvnw":
		return sub == "test" || sub == "verify"
	case "gradle", "gradlew":
		return sub == "test" || sub == "check"
	case "dotnet":
		return sub == "test"
	case "bash", "sh", "zsh":
		for i, x := range rest {
			if x == "-c" && i+1 < len(rest) {
				t, _ := classifyCommand(rest[i+1])
				return t
			}
		}
	}
	return false
}

// classifyCommand reports whether a shell command line runs a test (the program
// being run is a test runner: `echo "go test ./... passed"` is not one) and
// whether the line's exit status can still be the test's: a test that is piped
// into another command, or followed by one, has its status masked.
func classifyCommand(cmd string) (isTest, masked bool) {
	cs := splitShell(cmd)
	last := -1
	for i, c := range cs {
		if isTestWords(c.words) {
			last = i
		}
	}
	if last < 0 {
		return false, false
	}
	for i := last; i < len(cs)-1; i++ {
		if len(cs[i+1].words) > 0 || cs[i].op == "|" || cs[i].op == "||" {
			return true, true
		}
	}
	return true, cs[last].op == "|" || cs[last].op == "||"
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
