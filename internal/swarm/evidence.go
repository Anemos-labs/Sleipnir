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

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// Evidence is what the harness itself observed an agent do. Status shown on the
// board and the case for "done" are derived from it, not from what the model
// says about its own progress: self-reports are optimistic and stale.
//
// What it observes is kept per task: a worker's edits and commands count for the task they were made for, so a task is
// judged on its own work, not on the files its worker edited for another one (Begin, route).
type Evidence struct {
	mu    sync.Mutex
	reads int
	calls int
	// cur is the record of task, the task Begin last pointed the evidence at ("" before any: what the agent did before it
	// had a task, which its first task adopts). tasks holds the record of every task seen, the oldest dropped past
	// maxEvidenceTasks.
	task  string
	cur   *evRecord
	tasks map[string]*evRecord
	order []string
	// route, when set, names the task an edited path belongs to, "" for the current one (Swarm.editTask): an edit of a file
	// in the scope of another task its agent is doing counts for that task.
	route func(path string) string
	// held, when set, reports whether a task is still its agent's (doing, blocked or in review): the record of such a task
	// is never dropped, so work on it cannot read as "no files edited".
	held func(task string) bool
}

// evRecord is what was observed for one task.
type evRecord struct {
	edited   map[string]int
	commands []CmdRecord
}

// maxEvidenceTasks bounds how many tasks' records one agent's evidence keeps.
const maxEvidenceTasks = 16

// newRecord returns an empty record.
func newRecord() *evRecord { return &evRecord{edited: map[string]int{}} }

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
func NewEvidence() *Evidence { return &Evidence{cur: newRecord(), tasks: map[string]*evRecord{}} }

// Begin points the evidence at task, the task its agent is now working on: what it does from now on counts for that task
// (route excepted). The first task adopts what the agent did before it had one; a task seen before gets its record back, so
// a worker that comes back to its task (requeued, sent back, restarted on it) keeps the edits it made on it; a new task starts
// empty, so a reused worker's evidence for it does not list the files of its last one. The raw counters (Counts' reads and
// calls) go on counting.
func (e *Evidence) Begin(task string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if task == "" || task == e.task {
		return
	}
	if e.task == "" {
		e.tasks[task] = e.cur
		e.order = append(e.order, task)
	} else {
		e.cur = e.recordLocked(task)
	}
	e.task = task
}

// recordLocked returns task's record, made when it has none. Past maxEvidenceTasks the oldest records of tasks the agent no
// longer holds are dropped; the current task's, task's and those of tasks it still holds (held) are kept, past the bound if
// need be: it is then bounded by what the agent holds. e.mu is held.
func (e *Evidence) recordLocked(task string) *evRecord {
	if r, ok := e.tasks[task]; ok {
		return r
	}
	r := newRecord()
	e.tasks[task] = r
	e.order = append(e.order, task)
	for i := 0; len(e.tasks) > maxEvidenceTasks && i < len(e.order); {
		if old := e.order[i]; old != e.task && old != task && (e.held == nil || !e.held(old)) {
			delete(e.tasks, old)
			e.order = append(e.order[:i], e.order[i+1:]...)
			continue
		}
		i++
	}
	return r
}

// bind installs what the evidence asks its swarm: the task an edited path belongs to (route) and whether a task is still its
// agent's (held).
func (e *Evidence) bind(route func(path string) string, held func(task string) bool) {
	e.mu.Lock()
	e.route, e.held = route, held
	e.mu.Unlock()
}

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

// editedBy lists the paths a successful edit call changed, nil for any other call.
func editedBy(call core.Block, res *tools.Result) []string {
	if res == nil || res.IsError || res.Unchanged { // a write that left every file as it was edited nothing
		return nil
	}
	switch call.ToolName {
	case "edit", "write":
		if p := jsonString(call.Input, "path", "file_path"); p != "" {
			return []string{p}
		}
	case "apply_patch":
		var out []string
		for _, l := range strings.Split(jsonString(call.Input, "patch"), "\n") {
			for _, pre := range []string{"*** Update File: ", "*** Add File: ", "*** Delete File: "} {
				if strings.HasPrefix(l, pre) {
					out = append(out, strings.TrimSpace(strings.TrimPrefix(l, pre)))
				}
			}
		}
		return out
	}
	return nil
}

// Observe folds one finished tool call into the record: an edit into the record of the task it belongs to (route), anything
// else into the current task's.
func (e *Evidence) Observe(call core.Block, res *tools.Result, now time.Time) {
	paths := editedBy(call, res)
	e.mu.Lock()
	route := e.route
	e.mu.Unlock()
	owners := make([]string, len(paths))
	if route != nil {
		for i, p := range paths {
			owners[i] = route(p) // outside e.mu: it reads the board
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	for i, p := range paths {
		r := e.cur
		if t := owners[i]; t != "" && t != e.task {
			r = e.recordLocked(t)
		}
		r.noteEdit(p)
	}
	switch call.ToolName {
	case "read", "grep", "glob", "ls":
		e.reads++
	case "bash":
		cmd := jsonString(call.Input, "command")
		isTest, masked := classifyCommand(cmd)
		rec := CmdRecord{Cmd: cleanText(firstLine(cmd, 100), 100), At: now, Test: isTest, Masked: masked}
		rec.Exit, rec.Known = exitOf(res)
		e.cur.commands = append(e.cur.commands, rec)
		if len(e.cur.commands) > 40 {
			e.cur.commands = e.cur.commands[len(e.cur.commands)-40:]
		}
	}
}

// noteEdit records an edited path (bounded: an agent editing thousands of files
// keeps a bounded record).
func (r *evRecord) noteEdit(p string) {
	if _, ok := r.edited[p]; !ok && len(r.edited) >= 512 {
		return
	}
	r.edited[p]++
}

// files lists the record's edited paths, sorted.
func (r *evRecord) files() []string {
	out := make([]string, 0, len(r.edited))
	for p := range r.edited {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// lastTest returns the record's most recent test-like command.
func (r *evRecord) lastTest() (CmdRecord, bool) {
	for i := len(r.commands) - 1; i >= 0; i-- {
		if r.commands[i].Test {
			return r.commands[i], true
		}
	}
	return CmdRecord{}, false
}

// Edited lists the paths edited for the current task, sorted.
func (e *Evidence) Edited() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cur.files()
}

// LastTest returns the most recent test-like command of the current task.
func (e *Evidence) LastTest() (CmdRecord, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cur.lastTest()
}

// Summary renders the evidence of the current task as one line for the board and the manager.
func (e *Evidence) Summary() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cur.summary()
}

// SummaryFor renders the evidence of task as one line: what was observed for it, which is nothing when the agent was never
// pointed at it and has had another task (a record before any task belongs to the first one).
func (e *Evidence) SummaryFor(task string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, ok := e.tasks[task]
	switch {
	case ok:
	case task == e.task || e.task == "":
		r = e.cur
	default:
		r = newRecord()
	}
	return r.summary()
}

// evidenceNoEdits is the clause of a summary (Evidence.Summary) that says the harness saw no file edited.
const evidenceNoEdits = "no files edited"

// evidenceShowsNoEdits reports whether evidence the harness recorded on the board says that it saw no file edited: one of its
// "; "-separated clauses is the one Summary writes for that. Evidence is harness-written text (a worker cannot set it), so the
// clause is the harness's own record, not a claim.
func evidenceShowsNoEdits(evidence string) bool {
	for _, clause := range strings.Split(evidence, "; ") {
		if clause == evidenceNoEdits {
			return true
		}
	}
	return false
}

// summary renders a record as one line.
func (r *evRecord) summary() string {
	files := r.files()
	var parts []string
	switch n := len(files); {
	case n == 0:
		parts = append(parts, evidenceNoEdits)
	case n <= 4:
		parts = append(parts, fmt.Sprintf("edited %d (%s)", n, cleanText(strings.Join(base(files), ", "), 90)))
	default:
		parts = append(parts, fmt.Sprintf("edited %d (%s, …)", n, cleanText(strings.Join(base(files[:4]), ", "), 90)))
	}
	if t, ok := r.lastTest(); ok {
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

// Counts reports raw counters: the edits of the current task, and every read and call.
func (e *Evidence) Counts() (edits, reads, calls int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, n := range e.cur.edited {
		edits += n
	}
	return edits, e.reads, e.calls
}

// base copies path basenames using either slash convention, preserving input order.
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

// jsonString returns the first nonempty string at the requested object keys, or an empty string
// for invalid JSON or no match.
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

// firstLine trims surrounding whitespace and limits the first line to max runes, including an
// ellipsis when truncated. max must be positive.
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
	case "task", "mail", "note", "spawn", "approve":
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
		case c == '&':
			// "&" backgrounds a command; in "2>&1" and "&>file" it is part of a redirection.
			if (i > 0 && (rs[i-1] == '>' || rs[i-1] == '<')) || (i+1 < len(rs) && rs[i+1] == '>') {
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
	masked = cs[last].op == "|" || cs[last].op == "||" || cs[last].op == "&"
	for _, c := range cs[last+1:] {
		if len(c.words) > 0 {
			masked = true
		}
	}
	return true, masked
}
