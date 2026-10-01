package main

// `sleipnir run` as a process: what it does with its input, with a directory that is not there, with Ctrl-C, and with a
// team that did not finish.

import (
	"bytes"
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/provider/mock"
)

// capturingModel is an endpoint that answers "ok" and keeps the last user message it was sent, and the system prompt.
type capturingModel struct {
	ts     *httptest.Server
	mu     sync.Mutex
	last   string
	system string
}

func startCapturingModel(t *testing.T) *capturingModel {
	t.Helper()
	m := &capturingModel{}
	srv := mock.New(mock.Config{Engine: mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64}}, func(c *mock.Call) mock.Reply {
		m.mu.Lock()
		m.last, m.system = c.LastUser(), c.System
		for _, msg := range c.Messages {
			m.system += "\n" + msg.Content
		}
		m.mu.Unlock()
		return mock.Reply{Text: "ok"}
	})
	m.ts = srv.Start()
	t.Cleanup(m.ts.Close)
	return m
}

func (m *capturingModel) url() string { return m.ts.URL + "/v1" }
func (m *capturingModel) got() string { m.mu.Lock(); defer m.mu.Unlock(); return m.last }

// prompt is everything the last request told the model: the system prompt and every message (the shared layer with the project's
// instructions is one of them).
func (m *capturingModel) prompt() string { m.mu.Lock(); defer m.mu.Unlock(); return m.system }

// A prompt and something piped in are both the goal: `git diff | sleipnir run "review this"` used to read only the words and drop
// the diff without a word.
func TestRunTakesThePromptAndWhatIsPipedIn(t *testing.T) {
	m := startCapturingModel(t)
	w := newWorld(t, m.url())

	r := w.run("diff --git a/x b/x\n+added line\n", "run", "review this change")
	assertRun(t, r, 0, nil, nil)
	got := m.got()
	if !strings.Contains(got, "+added line") || !strings.Contains(got, "review this change") {
		t.Fatalf("the model was sent %q: the prompt and the input are both wanted", got)
	}
	if strings.Index(got, "+added line") > strings.Index(got, "review this change") {
		t.Errorf("the instruction comes after the material it is about:\n%s", got)
	}

	// the words alone, and the input alone, are as they were
	r = w.run("", "run", "just the words")
	assertRun(t, r, 0, nil, nil)
	if got := m.got(); !strings.HasSuffix(got, "just the words") || strings.Contains(got, "<stdin>") {
		t.Errorf("a prompt with nothing piped in was changed: %q", got)
	}
	r = w.run("only the input\n", "run")
	assertRun(t, r, 0, nil, nil)
	if got := m.got(); !strings.HasSuffix(got, "only the input") || strings.Contains(got, "<stdin>") {
		t.Errorf("input alone is the prompt, as it was: %q", got)
	}
	// "-" says where the input goes: it is the prompt
	r = w.run("from the pipe\n", "run", "-")
	assertRun(t, r, 0, nil, nil)
	if got := m.got(); !strings.HasSuffix(got, "from the pipe") {
		t.Errorf("run - : %q", got)
	}
}

// A working directory that does not exist is said so, at once, instead of failing in whatever first tries to use it.
func TestRunWithAWorkingDirectoryThatIsNotThere(t *testing.T) {
	m := startCapturingModel(t)
	w := newWorld(t, m.url())
	r := w.run("", "run", "--cwd", "/definitely/not/here", "hello")
	assertRun(t, r, 1, nil, []string{"sleipnir: ", "working directory /definitely/not/here", "does not exist"})
	if m.got() != "" {
		t.Errorf("a request was made although there was nowhere to work: %q", m.got())
	}
	file := w.project + "/AGENTS.md"
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	r = w.run("", "run", "--cwd", file, "hello")
	assertRun(t, r, 1, nil, []string{"working directory " + file, "not a directory"})
}

// Ctrl-C ends a command that does one thing, and says so: "context canceled" is what the library says, not what a person asked. The
// status is the shell's for a process that was interrupted (128 + the signal), so that a script can tell it from a failure.
func TestRunInterruptedSaysInterruptedAndExitsWithTheSignalsStatus(t *testing.T) {
	for _, tc := range []struct {
		sig  syscall.Signal
		code int
	}{{syscall.SIGINT, 130}, {syscall.SIGTERM, 143}} {
		t.Run(tc.sig.String(), func(t *testing.T) {
			m := startModel(t)
			g := m.on("@held", say("never").held())
			w := newWorld(t, m.url())
			ctx, cancel := context.WithTimeout(context.Background(), e2eGuard)
			defer cancel()
			c := w.cmdContext(ctx, "run", "@held")
			var out, errb bytes.Buffer
			c.Stdout, c.Stderr = &out, &errb
			if err := c.Start(); err != nil {
				t.Fatal(err)
			}
			g.wait(t) // the model has the request: the turn is running
			if err := c.Process.Signal(tc.sig); err != nil {
				t.Fatal(err)
			}
			err := c.Wait()
			g.release()
			code := 0
			if e, ok := err.(*exec.ExitError); ok {
				code = e.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if code != tc.code {
				t.Errorf("exit status %d, want %d\nstderr:\n%s", code, tc.code, errb.String())
			}
			if !strings.Contains(errb.String(), "sleipnir: interrupted") || strings.Contains(errb.String(), "context canceled") {
				t.Errorf("stderr should say the run was interrupted:\n%s", errb.String())
			}
			noCrash(t, out.String()+errb.String())
		})
	}
}
