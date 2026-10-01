package main

// End-to-end tests run the command in a child process: the test binary re-executes itself
// with e2eChildEnv set (see TestMain), which makes it run main() on the arguments it was
// given. So what these tests exercise is what a person runs: flag parsing, the process
// context and its signals, the exit status, and, with ptytest, a real terminal.
//
// Every test gets a private world: a home directory (the user's configuration, with a
// provider pointing at an in-process mock), a state directory, a temporary directory and a
// project, and a child environment that holds nothing else (no key, no proxy, none of the
// caller's SLEIPNIR_* settings). The model is scripted: a goal is a line that ends in a
// name the test registered, and a turn can be held until the test lets it go, which is how a
// test makes "the turn is running" something it can wait for instead of guess at.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/provider/mock"
	"github.com/reee344/sleipnir/internal/ptytest"
)

// e2eChildEnv tells a re-executed test binary to be the sleipnir command.
const e2eChildEnv = "SLEIPNIR_E2E_CHILD"

// runAsSleipnir is what the test binary does in a child process: main(), on the arguments
// after the program name.
func runAsSleipnir() {
	os.Unsetenv(e2eChildEnv)
	os.Args = append([]string{"sleipnir"}, os.Args[1:]...)
	main()
}

// e2eGuard is the most a test waits for anything that has to happen: a hang guard, not a
// timing (three race-instrumented suites at once are slow).
const e2eGuard = ptytest.Guard

var selfExe = sync.OnceValues(os.Executable)

// world is a private user of the command.
type world struct {
	t       *testing.T
	home    string   // $HOME: the user's configuration lives here
	state   string   // $SLEIPNIR_HOME: sessions, catalogue cache
	tmp     string   // $TMPDIR
	project string   // the working directory of every command; a .sleipnir directory marks it as a project root
	extra   []string // more of the child's environment, as NAME=value (a locale, NO_COLOR)
}

// newWorld makes a world. With a provider URL the user's configuration has a provider named
// mock at that URL and makes mock/mock-1 the default model; with "" there is no configuration.
func newWorld(t *testing.T, providerURL string) *world {
	t.Helper()
	root := t.TempDir()
	w := &world{t: t, home: filepath.Join(root, "home"), state: filepath.Join(root, "state"), tmp: filepath.Join(root, "tmp"), project: filepath.Join(root, "project")}
	for _, d := range []string{w.home, w.state, w.tmp, filepath.Join(w.project, ".sleipnir")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if providerURL != "" {
		w.writeUserConfig(map[string]any{
			"providers": map[string]any{"mock": map[string]any{"base_url": providerURL}},
			"models":    map[string]any{"default": "mock/mock-1"},
		})
	}
	return w
}

func (w *world) writeUserConfig(cfg map[string]any) {
	w.t.Helper()
	b, err := json.Marshal(cfg)
	if err != nil {
		w.t.Fatal(err)
	}
	dir := filepath.Join(w.home, ".sleipnir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), b, 0o600); err != nil {
		w.t.Fatal(err)
	}
}

// env is the whole environment of a child. PATH is the caller's, for git, sh and stty.
// GORACE: an instrumented binary sleeps a second at exit unless told not to.
func (w *world) env() []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + w.home,
		"SLEIPNIR_HOME=" + w.state,
		"TMPDIR=" + w.tmp,
		"TERM=xterm",
		"GORACE=atexit_sleep_ms=0",
		e2eChildEnv + "=1",
	}
	// A test binary built for coverage that runs as the command says on its stderr that it has nowhere to write its counters, which is
	// what these tests read (the nightly coverage run failed every one of them that looks at stderr). Told where, it says nothing.
	if testing.CoverMode() != "" {
		env = append(env, "GOCOVERDIR="+filepath.Join(w.tmp, "covdata"))
		os.MkdirAll(filepath.Join(w.tmp, "covdata"), 0o755)
	}
	return append(env, w.extra...)
}

// cmd is `sleipnir args...` in the project, in a private environment.
func (w *world) cmd(args ...string) *exec.Cmd {
	return w.cmdContext(context.Background(), args...)
}

func (w *world) cmdContext(ctx context.Context, args ...string) *exec.Cmd {
	w.t.Helper()
	exe, err := selfExe()
	if err != nil {
		w.t.Fatalf("cannot find the test binary: %v", err)
	}
	c := exec.CommandContext(ctx, exe, args...)
	c.Dir = w.project
	c.Env = w.env()
	return c
}

// result is what a command did.
type result struct {
	stdout, stderr string
	code           int // the exit status; -1 when a signal ended it
}

// run runs `sleipnir args...` with stdin as its standard input (nothing: /dev/null, which is
// not a terminal) and waits for it.
func (w *world) run(stdin string, args ...string) result {
	w.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), e2eGuard)
	defer cancel()
	c := w.cmdContext(ctx, args...)
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	if stdin != "" {
		c.Stdin = strings.NewReader(stdin)
	}
	err := c.Run()
	r := result{stdout: out.String(), stderr: errb.String()}
	var ee *exec.ExitError
	switch {
	case ctx.Err() != nil:
		w.t.Fatalf("sleipnir %v did not finish in %v.\nstdout:\n%s\nstderr:\n%s", args, e2eGuard, r.stdout, r.stderr)
	case errors.As(err, &ee):
		r.code = ee.ExitCode()
	case err != nil:
		w.t.Fatalf("sleipnir %v: %v", args, err)
	}
	noCrash(w.t, r.stdout+r.stderr)
	return r
}

// noCrash fails the test when output says that the child crashed or that the race
// detector fired in it.
func noCrash(t *testing.T, output string) {
	t.Helper()
	for _, bad := range []string{"WARNING: DATA RACE", "panic: ", "fatal error: "} {
		if strings.Contains(output, bad) {
			t.Errorf("the child printed %q:\n%s", bad, output)
		}
	}
}

// sessionEnd is the reason the newest recorded session gives for its end, and whether it
// recorded one (a session records an end only when a turn ran).
func (w *world) sessionEnd() (reason string, ok bool) {
	w.t.Helper()
	dirs, _ := filepath.Glob(filepath.Join(w.state, "sessions", "*"))
	if len(dirs) == 0 {
		w.t.Fatalf("no session was recorded under %s", w.state)
	}
	sort.Strings(dirs)
	b, err := os.ReadFile(filepath.Join(dirs[len(dirs)-1], "events.jsonl"))
	if err != nil {
		w.t.Fatal(err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		var e events.Event
		if json.Unmarshal([]byte(line), &e) != nil || e.Type != events.TypeSessionEnd {
			continue
		}
		var d struct{ Reason string }
		if err := json.Unmarshal(e.Data, &d); err != nil {
			w.t.Fatal(err)
		}
		return d.Reason, true
	}
	return "", false
}

// ---- the scripted model ----

// turn is what the model does with a goal.
type turn struct {
	text string         // the reply; ignored when the turn opens with a tool call
	call *mock.ToolCall // a tool call the turn opens with: the model then reports what the tool said
	gate *gate          // the model answers only when the test lets it
}

func say(text string) turn { return turn{text: text} }

// writes opens a turn with a call of the write tool, the one that asks for approval in the
// default permission mode.
func writes(path string) turn {
	args, _ := json.Marshal(map[string]string{"path": path, "content": "from the model\n"})
	return turn{call: &mock.ToolCall{ID: "call-1", Name: "write", Args: string(args)}}
}

// runs opens a turn with a call of the bash tool (in the default mode a command that writes
// asks for approval; one that only reads does not).
func runs(command string) turn {
	args, _ := json.Marshal(map[string]string{"command": command})
	return turn{call: &mock.ToolCall{ID: "call-1", Name: "bash", Args: string(args)}}
}

// held makes the turn wait for the test: the model has the request (gate.wait returns) but
// does not answer until gate.release.
func (t turn) held() turn {
	t.gate = &gate{arrived: make(chan struct{}), open: make(chan struct{})}
	return t
}

// gate is a turn that is held.
type gate struct {
	arrived, open        chan struct{}
	arriveOnce, openOnce sync.Once
}

// wait returns when the model has the request of the held turn: the command is in that turn.
func (g *gate) wait(t *testing.T) {
	t.Helper()
	select {
	case <-g.arrived:
	case <-time.After(e2eGuard):
		t.Fatalf("the model did not get the request within %v", e2eGuard)
	}
}

// release lets the model answer.
func (g *gate) release() { g.openOnce.Do(func() { close(g.open) }) }

// scriptedModel is an OpenAI-style endpoint (the mock provider) whose answers the test
// scripts by goal: the goal of a request is the registered name its last user message ends with.
type scriptedModel struct {
	t  *testing.T
	ts *httptest.Server

	mu         sync.Mutex
	turns      map[string]turn
	goals      []string // the goal of each request that started a turn, in the order they arrived
	results    []string // what the tools said, as the model got it
	unscripted []string // goals that nothing scripted
	stray      []string // goals that came with text in front of them: input that was not the goal's own
	gates      []*gate
}

func startModel(t *testing.T) *scriptedModel {
	t.Helper()
	m := &scriptedModel{t: t, turns: map[string]turn{}}
	srv := mock.New(mock.Config{Engine: mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64}}, m.respond)
	m.ts = srv.Start()
	// Cleanups run last in, first out: what a request is held on is released before the
	// server waits for its requests to end.
	t.Cleanup(m.ts.Close)
	t.Cleanup(func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		for _, g := range m.gates {
			g.release()
		}
		if len(m.unscripted) > 0 {
			t.Errorf("the model was sent goals that no test scripted: %q", m.unscripted)
		}
		if len(m.stray) > 0 {
			t.Errorf("the model was sent goals with text in front that is neither the prompt pins nor nothing: %q", m.stray)
		}
	})
	return m
}

// url is the provider's base URL.
func (m *scriptedModel) url() string { return m.ts.URL + "/v1" }

// on scripts what the model does with a goal, a line that ends in name. It returns the turn's
// gate if the turn is held.
func (m *scriptedModel) on(name string, tr turn) *gate {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.turns[name] = tr
	if tr.gate != nil {
		m.gates = append(m.gates, tr.gate)
	}
	return tr.gate
}

// seen returns the goals the model got, in order.
func (m *scriptedModel) seen() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.goals...)
}

// toolResults returns what the tools said to the model, in order.
func (m *scriptedModel) toolResults() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.results...)
}

func (m *scriptedModel) respond(c *mock.Call) mock.Reply {
	last := c.Messages[len(c.Messages)-1]
	if last.Role == "tool" {
		// The second request of a turn that opened with a tool call.
		m.mu.Lock()
		m.results = append(m.results, last.Content)
		m.mu.Unlock()
		return mock.Reply{Text: "the tool said: " + oneLineCLI(last.Content, 200)}
	}
	text := strings.TrimSpace(c.LastUser())
	m.mu.Lock()
	// The goal is the last registered name in the message (the longest, where one ends in
	// another: a name is a prefix of a longer one at the same place).
	var goal string
	var tr turn
	at := -1
	for name, t := range m.turns {
		i := strings.LastIndex(text, name)
		if i >= 0 && (i > at || (i == at && len(name) > len(goal))) {
			goal, tr, at = name, t, i
		}
	}
	if goal == "" {
		m.unscripted = append(m.unscripted, oneLineCLI(text, 80)+" … "+text[max(0, len(text)-120):])
		m.mu.Unlock()
		return mock.Reply{Text: "unscripted goal"}
	}
	m.goals = append(m.goals, goal)
	// The first message of a session carries the shared pins in front of the goal; later ones
	// are the goal alone; a goal whose turn was cancelled (nothing answered it) is still in the
	// message that the next goal is added to, a blank line between; and a swarm's manager is given the board after
	// the goal. Anything else around it is input that ended up in the goal by mistake (a
	// half-typed line that was not discarded, for one).
	front := text[:at]
	for name := range m.turns {
		front = strings.ReplaceAll(front, name, "")
	}
	// the goal that follows an unanswered one is set off from it by a blank line (sleipnir-kv/3)
	front = strings.TrimRight(front, "\n")
	after := text[at+len(goal):]
	if (front != "" && !strings.HasSuffix(front, "</role-context>")) || (after != "" && !strings.HasPrefix(after, "<live board=")) {
		m.stray = append(m.stray, "…"+front[max(0, len(front)-60):]+"⟦"+goal+"⟧"+oneLineCLI(after, 60))
	}
	m.mu.Unlock()
	if g := tr.gate; g != nil {
		g.arriveOnce.Do(func() { close(g.arrived) })
		<-g.open
	}
	if tr.call != nil {
		return mock.Reply{ToolCalls: []mock.ToolCall{*tr.call}}
	}
	return mock.Reply{Text: tr.text}
}
