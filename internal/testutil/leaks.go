// Package testutil holds what several test suites need and no product package should
// carry. It imports the standard library and nothing else.
//
// # Goroutine leaks
//
// A goroutine that is still running when the tests that started it are over is a leak: it
// holds a connection, a file, a child process or a lock, and on a long-lived session it is a
// slow drain. The suites of the packages that start goroutines end with a check:
//
//	func TestMain(m *testing.M) { os.Exit(testutil.CheckLeaks(m)) }
//
// and a single test that must clean up after itself says so first:
//
//	func TestSomething(t *testing.T) {
//		testutil.VerifyNone(t)
//		...
//	}
//
// Only goroutines of this module are looked at: one whose stack holds a function of
// github.com/anemos-labs/sleipnir, or one that such a function started (the runtime names the
// creator in the dump). The goroutines of the test runner are not leaks, and neither is the
// caller. A goroutine that was told to stop gets [LeakWait] to do it, so the check does not
// fail on one that is merely on its way out. (The functions of a package main are named
// main.f in a dump, like the test binary's own entry point, so the goroutines of such a
// package are not recognised.)
package testutil

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Module is the import path of this module. A goroutine is ours when a function whose name
// starts with it is on its stack or started it.
const Module = "github.com/anemos-labs/sleipnir"

// LeakWait is how long a goroutine that is on its way out is given: the check polls until
// nothing of the module is left, and reports what is still there after this long.
const LeakWait = 10 * time.Second

// leakWait and stderr are what the checks use; the tests of this package replace them.
var (
	leakWait           = LeakWait
	stderr   io.Writer = os.Stderr
)

// A Goroutine is one entry of a goroutine dump.
type Goroutine struct {
	ID      int      // the number in the dump; numbers are not reused within a process
	State   string   // what it waits for: "chan receive", "select", "IO wait", ...
	Frames  []string // function names, the top of the stack first
	Creator string   // the function that started it; empty for the main goroutine
	Dump    string   // the entry as the runtime printed it
}

// Snapshot lists the goroutines of the process. The first is the caller's.
func Snapshot() []Goroutine {
	buf := make([]byte, 1<<16)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return parseDump(string(buf[:n]))
		}
		buf = make([]byte, 2*len(buf))
	}
}

// parseDump reads what runtime.Stack prints with all set: entries separated by an empty
// line, each a header ("goroutine 7 [chan receive, 2 minutes]:"), then a function line and
// an indented file line for every frame, then "created by f in goroutine 1" and its file.
func parseDump(dump string) []Goroutine {
	var out []Goroutine
	for _, entry := range strings.Split(strings.TrimSpace(dump), "\n\n") {
		lines := strings.Split(entry, "\n")
		rest, ok := strings.CutPrefix(lines[0], "goroutine ")
		if !ok {
			continue
		}
		id, after, _ := strings.Cut(rest, " ")
		n, err := strconv.Atoi(id)
		if err != nil {
			continue
		}
		g := Goroutine{ID: n, Dump: entry}
		if i := strings.IndexByte(after, '['); i >= 0 {
			if j := strings.IndexByte(after[i:], ']'); j > 0 {
				g.State = after[i+1 : i+j]
			}
		}
		for _, line := range lines[1:] {
			switch {
			case strings.HasPrefix(line, "\t"), strings.HasPrefix(line, "..."):
				// a file and line, or "...additional frames elided..."
			case strings.HasPrefix(line, "created by "):
				name := strings.TrimPrefix(line, "created by ")
				if i := strings.Index(name, " in goroutine "); i >= 0 {
					name = name[:i]
				}
				g.Creator = name
			default:
				g.Frames = append(g.Frames, funcName(line))
			}
		}
		out = append(out, g)
	}
	return out
}

// funcName cuts the arguments off a frame line: "pkg.(*T).M(0xc000012345, {0x1, 0x2})" is
// "pkg.(*T).M", and "main.main()" is "main.main".
func funcName(line string) string {
	if !strings.HasSuffix(line, ")") {
		return line
	}
	depth := 0
	for i := len(line) - 1; i >= 0; i-- {
		switch line[i] {
		case ')':
			depth++
		case '(':
			if depth--; depth == 0 {
				return line[:i]
			}
		}
	}
	return line
}

// ours reports goroutines that run code of this module or were started by it.
func (g Goroutine) ours() bool {
	if strings.HasPrefix(g.Creator, Module+"/") {
		return true
	}
	for _, f := range g.Frames {
		if strings.HasPrefix(f, Module+"/") {
			return true
		}
	}
	return false
}

// runner reports goroutines that belong to the test runner and to the program's entry
// point: the test that is running, a parent test waiting for its subtests, the main
// goroutine inside TestMain.
func (g Goroutine) runner() bool {
	for _, f := range g.Frames {
		if f == "main.main" || strings.HasPrefix(f, "testing.") {
			return true
		}
	}
	return false
}

// matches reports goroutines with one of the given strings in their dump.
func (g Goroutine) matches(ignore []string) bool {
	for _, s := range ignore {
		if s != "" && strings.Contains(g.Dump, s) {
			return true
		}
	}
	return false
}

// leaked lists the goroutines of the module that are running now: not the caller, not the
// test runner, none in skip (goroutine numbers that were there before), and none whose dump
// holds one of the ignore strings.
func leaked(skip map[int]bool, ignore []string) []Goroutine {
	var out []Goroutine
	for i, g := range Snapshot() {
		if i == 0 || skip[g.ID] || !g.ours() || g.runner() || g.matches(ignore) {
			continue
		}
		out = append(out, g)
	}
	return out
}

// settle polls until no goroutine is leaked or wait is over, and returns what is left.
func settle(skip map[int]bool, ignore []string, wait time.Duration) []Goroutine {
	deadline := time.Now().Add(wait)
	for delay := time.Millisecond; ; delay = min(2*delay, 100*time.Millisecond) {
		bad := leaked(skip, ignore)
		if len(bad) == 0 || !time.Now().Before(deadline) {
			return bad
		}
		time.Sleep(delay)
	}
}

// describe is what is printed for a leak: a line, then the stack of every goroutine.
func describe(bad []Goroutine, wait time.Duration) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d goroutine(s) of %s still running %v after the tests ended (each is shown where it is blocked, and what started it):\n", len(bad), Module, wait)
	for _, g := range bad {
		b.WriteString("\n")
		b.WriteString(g.Dump)
		b.WriteString("\n")
	}
	return b.String()
}

// CheckLeaks runs the tests of a package and then checks that none of the module's
// goroutines is left. Use it as the body of TestMain:
//
//	func TestMain(m *testing.M) { os.Exit(testutil.CheckLeaks(m)) }
//
// It returns the exit code: the tests' if they failed (a failed test leaves goroutines
// behind, and the failure is what to read), 1 if goroutines are left after [LeakWait] (their
// stacks are printed to standard error), else 0. A goroutine that is meant to outlive the
// tests is named by a string that is in its stack, such as the name of its function.
func CheckLeaks(m *testing.M, ignore ...string) int {
	return checkLeaks(m.Run, ignore)
}

func checkLeaks(run func() int, ignore []string) int {
	if code := run(); code != 0 {
		return code
	}
	if bad := settle(nil, ignore, leakWait); len(bad) > 0 {
		fmt.Fprint(stderr, "FAIL: goroutine leak: "+describe(bad, leakWait))
		return 1
	}
	return 0
}

// TB is what VerifyNone needs of a test: a *testing.T or a *testing.B has all of it, and a
// test of VerifyNone itself can stand in with less.
type TB interface {
	Helper()
	Cleanup(func())
	Errorf(format string, args ...any)
}

// VerifyNone makes the test fail if a goroutine of the module that started during it is
// still running when it ends and its cleanups have run, after [LeakWait]. Call it first in
// the test, so that its check runs last. Goroutines that were already running are not
// counted; a test that runs in parallel with others sees theirs, so it is for tests that run
// on their own.
func VerifyNone(t TB, ignore ...string) {
	t.Helper()
	before := map[int]bool{}
	for _, g := range Snapshot() {
		before[g.ID] = true
	}
	t.Cleanup(func() {
		if bad := settle(before, ignore, leakWait); len(bad) > 0 {
			t.Errorf("goroutine leak: %s", describe(bad, leakWait))
		}
	})
}
