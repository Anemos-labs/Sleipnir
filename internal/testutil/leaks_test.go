package testutil

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// The suite of this package ends with the check it provides. The binary also runs as its own
// fixture: started with helperEnv set (and no tests), it ends like a suite that leaks, or like one
// that does not.
const helperEnv = "TESTUTIL_FIXTURE"

func TestMain(m *testing.M) {
	switch os.Getenv(helperEnv) {
	case "leak":
		leakWait = 100 * time.Millisecond
		started := make(chan struct{})
		go leakForever(started)
		<-started // a goroutine that has not run yet has no frames to show
	case "clean":
		leakWait = 100 * time.Millisecond
	}
	os.Exit(CheckLeaks(m))
}

func leakForever(started chan<- struct{}) {
	close(started)
	select {}
}

// parked is a goroutine of this module that waits until it is released; park starts one, waits
// until it is blocked there (before that the runtime has no frames to show for it, and its state
// is not the one a test looks for), and returns what stops it: stop returns when the goroutine
// is gone from the dump, so that the next test does not meet the end of this one's.
func parked(started chan<- int, release <-chan struct{}) {
	started <- Snapshot()[0].ID
	<-release
}

func park() (stop func()) {
	started, release := make(chan int), make(chan struct{})
	var once sync.Once
	go parked(started, release)
	id := <-started
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if g, ok := find(id); ok && strings.HasPrefix(g.State, "chan receive") {
			break
		}
	}
	return func() {
		once.Do(func() { close(release) })
		deadline := time.Now().Add(time.Minute)
		for _, ok := find(id); ok && time.Now().Before(deadline); _, ok = find(id) {
			time.Sleep(time.Millisecond)
		}
	}
}

// find looks a goroutine up by its number in a fresh snapshot.
func find(id int) (Goroutine, bool) {
	for _, g := range Snapshot() {
		if g.ID == id {
			return g, true
		}
	}
	return Goroutine{}, false
}

// quiet waits until no goroutine of the module is left from the tests before this one (one
// that has just been stopped, or a timer's callback, is on its way out for a moment): the tests
// that count what is running start from nothing.
func quiet(t *testing.T) {
	t.Helper()
	if bad := settle(nil, nil, time.Minute); len(bad) != 0 {
		t.Fatalf("goroutines of the module that nothing stops: %s", names(bad))
	}
}

// inParked reports the goroutines started by park.
func inParked(g Goroutine) bool {
	for _, f := range g.Frames {
		if strings.HasSuffix(f, "testutil.parked") {
			return true
		}
	}
	return false
}

// quickly shortens the wait for the test that looks at a leak that stays.
func quickly(t *testing.T) {
	t.Helper()
	old := leakWait
	leakWait = 20 * time.Millisecond
	t.Cleanup(func() { leakWait = old })
}

func names(gs []Goroutine) string {
	var b strings.Builder
	for _, g := range gs {
		b.WriteString(strings.Join(g.Frames, " < ") + " | ")
	}
	return b.String()
}

const realDump = `goroutine 1 [running]:
main.main()
	/src/cmd/x/main.go:10 +0x1d

goroutine 7 [chan receive, 2 minutes]:
github.com/anemos-labs/sleipnir/internal/foo.(*Pool).loop(0xc000123456, {0x55a7c0, 0xc0000b2000})
	/src/internal/foo/pool.go:42 +0x65
created by github.com/anemos-labs/sleipnir/internal/foo.New in goroutine 1
	/src/internal/foo/pool.go:20 +0x99

goroutine 9 gp=0xc000004380 m=nil [GC sweep wait]:
runtime.gopark(0x1?, 0x0?, 0x0?, 0x0?, 0x0?)
	/usr/local/go/src/runtime/proc.go:435 +0xce
runtime.goparkunlock(...)
	/usr/local/go/src/runtime/proc.go:441
created by runtime.gcenable in goroutine 1
	/usr/local/go/src/runtime/mgc.go:204 +0x66

goroutine 12 [select (no cases)]:
os/exec.(*Cmd).Wait(0xc0001)
	/usr/local/go/src/os/exec/exec.go:900 +0x1
...additional frames elided...
created by github.com/anemos-labs/sleipnir/internal/tools.run in goroutine 7
	/src/internal/tools/run.go:77 +0x2
`

func TestParseDump(t *testing.T) {
	got := parseDump(realDump)
	want := []struct {
		id      int
		state   string
		frames  []string
		creator string
	}{
		{1, "running", []string{"main.main"}, ""},
		{7, "chan receive, 2 minutes", []string{"github.com/anemos-labs/sleipnir/internal/foo.(*Pool).loop"}, "github.com/anemos-labs/sleipnir/internal/foo.New"},
		{9, "GC sweep wait", []string{"runtime.gopark", "runtime.goparkunlock"}, "runtime.gcenable"},
		{12, "select (no cases)", []string{"os/exec.(*Cmd).Wait"}, "github.com/anemos-labs/sleipnir/internal/tools.run"},
	}
	if len(got) != len(want) {
		t.Fatalf("%d goroutines, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		g := got[i]
		if g.ID != w.id || g.State != w.state || !reflect.DeepEqual(g.Frames, w.frames) || g.Creator != w.creator {
			t.Errorf("goroutine %d = {%d %q %q %q}, want {%d %q %q %q}", i, g.ID, g.State, g.Frames, g.Creator, w.id, w.state, w.frames, w.creator)
		}
		if !strings.HasPrefix(g.Dump, "goroutine "+strings.Fields(g.Dump)[1]) || strings.Contains(g.Dump, "\n\n") {
			t.Errorf("goroutine %d: dump is not one entry: %q", g.ID, g.Dump)
		}
	}
	for _, bad := range []string{"", "\n\n", "not a dump", "goroutine x [running]:\nmain.main()", "goroutine"} {
		if gs := parseDump(bad); len(gs) != 0 {
			t.Errorf("parseDump(%q) = %+v, want nothing", bad, gs)
		}
	}
}

func TestFuncName(t *testing.T) {
	for line, want := range map[string]string{
		"main.main()": "main.main",
		"testing.tRunner(0xc000102000, 0x5a7b40)":                             "testing.tRunner",
		"pkg.(*T).M(0xc000012345, {0x1, 0x2})":                                "pkg.(*T).M",
		"pkg.F[...](...)":                                                     "pkg.F[...]",
		"pkg.F.func1({0xc000, 0x5}, 0x1, {0xc0, (0x2)})":                      "pkg.F.func1",
		"github.com/anemos-labs/sleipnir/internal/foo.(*Pool).loop(0x1, 0x2)": "github.com/anemos-labs/sleipnir/internal/foo.(*Pool).loop",
		"runtime.goexit({})":                                                  "runtime.goexit",
		"noargs":                                                              "noargs",
		"":                                                                    "",
		"odd)":                                                                "odd)",
	} {
		if got := funcName(line); got != want {
			t.Errorf("funcName(%q) = %q, want %q", line, got, want)
		}
	}
}

// What is a leak: a goroutine of this module that is not the test runner's, not the caller's
// and not named by the ignore list.
func TestWhatCountsAsOurs(t *testing.T) {
	const (
		mine = Module + "/internal/foo.(*Pool).loop"
		test = Module + "/internal/foo.TestPool"
	)
	for _, tc := range []struct {
		name         string
		g            Goroutine
		ours, runner bool
	}{
		{"a goroutine of the module", Goroutine{Frames: []string{mine}}, true, false},
		{"one in the standard library that the module started", Goroutine{Frames: []string{"os/exec.(*Cmd).Wait"}, Creator: Module + "/internal/tools.run"}, true, false},
		{"a callback of the module deep in the standard library", Goroutine{Frames: []string{"net/http.HandlerFunc.ServeHTTP", mine, "net/http.(*conn).serve"}}, true, false},
		{"one in the standard library that it started itself", Goroutine{Frames: []string{"net/http.(*persistConn).readLoop"}, Creator: "net/http.(*Transport).dialConn"}, false, false},
		{"the runtime", Goroutine{Frames: []string{"runtime.gopark", "runtime.bgsweep"}, Creator: "runtime.gcenable"}, false, false},
		{"another module with our name as a prefix", Goroutine{Frames: []string{Module + "x/pkg.F"}}, false, false},
		{"the test that is running", Goroutine{Frames: []string{test, "testing.tRunner"}, Creator: "testing.(*T).Run"}, true, true},
		{"a test waiting for its subtests", Goroutine{Frames: []string{"testing.(*T).Run", test, "testing.tRunner"}}, true, true},
		{"the main goroutine inside TestMain", Goroutine{Frames: []string{"testing.(*M).Run", Module + "/internal/foo.TestMain", "main.main"}}, true, true},
		{"the main goroutine", Goroutine{Frames: []string{"main.main"}}, false, true},
		{"a closure in a main package", Goroutine{Frames: []string{"main.main.func1"}, Creator: Module + "/cmd/x.run"}, true, false},
		{"a package that only starts with testing", Goroutine{Frames: []string{"testing/iotest.Reader", mine}}, true, false},
	} {
		if got := tc.g.ours(); got != tc.ours {
			t.Errorf("%s: ours = %v, want %v", tc.name, got, tc.ours)
		}
		if got := tc.g.runner(); got != tc.runner {
			t.Errorf("%s: runner = %v, want %v", tc.name, got, tc.runner)
		}
	}
	g := Goroutine{Dump: "goroutine 3 [select]:\npkg.(*Watcher).poll()\n"}
	if !g.matches([]string{"Watcher"}) || !g.matches([]string{"nope", "poll"}) || g.matches([]string{"nope"}) || g.matches([]string{""}) || g.matches(nil) {
		t.Errorf("matches is wrong for %q", g.Dump)
	}
}

// The caller is first in the snapshot, and a snapshot is taken whatever the number of
// goroutines: the buffer grows until the dump fits.
func TestSnapshot(t *testing.T) {
	stops := make([]func(), 0, 200)
	for range cap(stops) {
		stops = append(stops, park())
	}
	t.Cleanup(func() {
		for _, stop := range stops {
			stop()
		}
	})
	gs := Snapshot()
	if len(gs) < 200 {
		t.Fatalf("%d goroutines in the snapshot, want at least 200", len(gs))
	}
	if caller := gs[0]; !caller.runner() || !strings.HasSuffix(names(gs[:1]), "TestSnapshot < testing.tRunner | ") {
		t.Errorf("the first goroutine is %s, want the caller's", names(gs[:1]))
	}
	seen := map[int]bool{}
	for _, g := range gs {
		if seen[g.ID] {
			t.Errorf("goroutine %d twice", g.ID)
		}
		seen[g.ID] = true
		if g.State == "" {
			t.Errorf("goroutine %d has no state: %q", g.ID, g.Dump)
		}
	}
	parkedHere := 0
	for _, g := range gs {
		if inParked(g) {
			parkedHere++
		}
	}
	if parkedHere != 200 {
		t.Errorf("found %d of the 200 parked goroutines", parkedHere)
	}
}

func TestLeakedFindsWhatIsLeftAndOnlyThat(t *testing.T) {
	quiet(t)
	stop := park()
	t.Cleanup(stop)
	bad := leaked(nil, nil)
	if len(bad) != 1 || !inParked(bad[0]) || !strings.HasSuffix(bad[0].Creator, "testutil.park") {
		t.Fatalf("leaked = %s, want the parked goroutine, started by park", names(bad))
	}

	if got := leaked(map[int]bool{bad[0].ID: true}, nil); len(got) != 0 {
		t.Errorf("a goroutine that was there before was reported: %s", names(got))
	}
	for _, ignore := range [][]string{{"testutil.parked"}, {"nothing", "parked("}} {
		if got := leaked(nil, ignore); len(got) != 0 {
			t.Errorf("ignore %q: %s reported", ignore, names(got))
		}
	}
	if got := leaked(nil, []string{"nothing of the kind"}); len(got) != 1 {
		t.Errorf("an ignore string that matches nothing hid the goroutine")
	}

	stop()
	if got := settle(nil, nil, 10*time.Second); len(got) != 0 {
		t.Errorf("a goroutine that is gone is still reported: %s", names(got))
	}
}

// A goroutine that is on its way out is waited for, and one that stays is reported with the
// stack that shows why.
func TestSettleWaitsForAGoroutineOnItsWayOut(t *testing.T) {
	stop := park()
	time.AfterFunc(30*time.Millisecond, stop)
	if bad := settle(nil, nil, 2*time.Minute); len(bad) != 0 {
		t.Fatalf("after the wait: %s", names(bad))
	}
}

func TestSettleReportsAGoroutineThatStays(t *testing.T) {
	quiet(t)
	stop := park()
	t.Cleanup(stop)
	bad := settle(nil, nil, 20*time.Millisecond)
	if len(bad) != 1 {
		t.Fatalf("settle = %s, want the parked goroutine", names(bad))
	}
	text := describe(bad, 20*time.Millisecond)
	for _, want := range []string{"1 goroutine(s) of " + Module, "20ms", "testutil.parked", "chan receive", "created by " + Module + "/internal/testutil.park in goroutine"} {
		if !strings.Contains(text, want) {
			t.Errorf("the report lacks %q:\n%s", want, text)
		}
	}
}

// VerifyNone counts what started after it, when the test and its cleanups are over.
type fakeT struct {
	cleanups []func()
	errs     []string
}

func (f *fakeT) Helper()          {}
func (f *fakeT) Cleanup(c func()) { f.cleanups = append(f.cleanups, c) }
func (f *fakeT) Errorf(format string, args ...any) {
	var b bytes.Buffer
	b.WriteString(strings.TrimSpace(format))
	b.WriteString(" | ")
	for _, a := range args {
		if s, ok := a.(string); ok {
			b.WriteString(s)
		}
	}
	f.errs = append(f.errs, b.String())
}

// run executes the cleanups last in, first out, as the testing package does.
func (f *fakeT) run() {
	for i := len(f.cleanups) - 1; i >= 0; i-- {
		f.cleanups[i]()
	}
}

func TestVerifyNone(t *testing.T) {
	quickly(t)
	before := park() // running before the check starts: not counted
	t.Cleanup(before)

	t.Run("clean", func(t *testing.T) {
		f := &fakeT{}
		VerifyNone(f)
		stop := park()
		stop()
		f.run()
		if len(f.errs) != 0 {
			t.Errorf("a test that cleaned up failed: %v", f.errs)
		}
	})
	t.Run("a goroutine on its way out", func(t *testing.T) {
		old := leakWait
		leakWait = time.Minute
		defer func() { leakWait = old }()
		f := &fakeT{}
		VerifyNone(f)
		stop := park()
		f.Cleanup(func() { time.AfterFunc(30*time.Millisecond, stop) })
		f.run()
		if len(f.errs) != 0 {
			t.Errorf("a goroutine that stopped within the wait was reported: %v", f.errs)
		}
	})
	t.Run("a leak", func(t *testing.T) {
		f := &fakeT{}
		VerifyNone(f)
		stop := park()
		t.Cleanup(stop)
		f.run()
		if len(f.errs) != 1 || !strings.Contains(f.errs[0], "goroutine leak") || !strings.Contains(f.errs[0], "testutil.parked") {
			t.Errorf("errors = %v, want one about the parked goroutine", f.errs)
		}
	})
	t.Run("a leak that the test is told to expect", func(t *testing.T) {
		f := &fakeT{}
		VerifyNone(f, "testutil.parked")
		stop := park()
		t.Cleanup(stop)
		f.run()
		if len(f.errs) != 0 {
			t.Errorf("an ignored goroutine was reported: %v", f.errs)
		}
	})
}

// The real test type can be used: a test that leaves nothing passes.
func TestVerifyNoneOnARealTest(t *testing.T) {
	VerifyNone(t)
	stop := park()
	t.Cleanup(stop)
}

// CheckLeaks with a real *testing.M: a binary whose suite leaves a goroutine behind exits with 1
// and shows where it sits and what started it, one that does not exits with 0.
func TestCheckLeaksEndsTheBinary(t *testing.T) {
	run := func(mode string) (int, string) {
		cmd := exec.Command(os.Args[0], "-test.run=^$")
		cmd.Env = append(os.Environ(), helperEnv+"="+mode, "GORACE=atexit_sleep_ms=0")
		out, err := cmd.CombinedOutput()
		var exit *exec.ExitError
		switch {
		case err == nil:
			return 0, string(out)
		case errors.As(err, &exit):
			return exit.ExitCode(), string(out)
		}
		t.Fatalf("running the fixture (%s): %v", mode, err)
		return -1, ""
	}
	if code, out := run("clean"); code != 0 || !strings.Contains(out, "PASS") || strings.Contains(out, "leak") {
		t.Errorf("a suite that leaves nothing: exit %d, output %q", code, out)
	}
	code, out := run("leak")
	for _, want := range []string{"PASS", "FAIL: goroutine leak: 1 goroutine(s) of " + Module, "testutil.leakForever", "created by " + Module + "/internal/testutil.TestMain"} {
		if !strings.Contains(out, want) {
			t.Errorf("the output of a suite that leaks lacks %q:\n%s", want, out)
		}
	}
	if code != 1 {
		t.Errorf("a suite that leaks exits with %d, want 1", code)
	}
}

func TestCheckLeaks(t *testing.T) {
	quiet(t)
	quickly(t)
	var out bytes.Buffer
	old := stderr
	stderr = &out
	t.Cleanup(func() { stderr = old })

	if code := checkLeaks(func() int { return 0 }, nil); code != 0 || out.Len() != 0 {
		t.Errorf("clean suite: code %d, output %q", code, out.String())
	}

	stop := park()
	t.Cleanup(stop)
	if code := checkLeaks(func() int { return 3 }, nil); code != 3 || out.Len() != 0 {
		t.Errorf("a failed suite keeps its code and says nothing more: code %d, output %q", code, out.String())
	}
	if code := checkLeaks(func() int { return 0 }, []string{"testutil.parked"}); code != 0 || out.Len() != 0 {
		t.Errorf("an ignored goroutine failed the suite: code %d, output %q", code, out.String())
	}
	if code := checkLeaks(func() int { return 0 }, nil); code != 1 || !strings.HasPrefix(out.String(), "FAIL: goroutine leak: 1 goroutine(s)") || !strings.Contains(out.String(), "testutil.parked") {
		t.Errorf("a leaking suite: code %d, output %q", code, out.String())
	}
}
