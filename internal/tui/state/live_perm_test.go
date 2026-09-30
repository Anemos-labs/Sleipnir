package state

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/reee344/sleipnir/internal/cost"
	"github.com/reee344/sleipnir/internal/perm"
	"github.com/reee344/sleipnir/internal/provider/mock"
	"github.com/reee344/sleipnir/internal/provider/openaichat"
	"github.com/reee344/sleipnir/internal/session"
)

// The permission and cancellation sessions are one agent through the real harness, in the default permission mode, against the mock
// endpoint: what the mode does not allow on its own the engine asks about, and the log holds the perm.ask and perm.decide that the
// real audit writes (internal/session/permaudit.go), and, when the person interrupts, the agent.cancel that the real agent writes
// (internal/agent/agent.go). Four of them, each recorded once per test process:
//
//	prompted   a person answers: a write they allow for the session, a command they refuse, a key that policy refuses without asking,
//	           a read outside the project they allow once, and a second write that the first answer makes a question no more
//	no one     the same run with no one to ask: every question is refused
//	asking     the run is cancelled while a question waits for its answer
//	model      the run is cancelled while a request is out
//
// Nothing of it is timed: the scripts tell the test when the session has reached the moment to interrupt it, and the test waits for
// exactly that.

// soloEnv is the project, the private home and a file outside the project of one of these sessions.
type soloEnv struct {
	root, ws, home, outside string
}

func newSoloEnv(root string) (*soloEnv, error) {
	env := &soloEnv{root: root, ws: filepath.Join(root, "work"), home: filepath.Join(root, "home"), outside: filepath.Join(root, "outside")}
	for _, d := range []string{filepath.Join(env.ws, "notes"), filepath.Join(env.home, ".ssh"), env.outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	for path, body := range map[string]string{
		filepath.Join(env.ws, "README.md"):        "# Notes\n",
		filepath.Join(env.home, ".ssh", "id_rsa"): "not a key\n",
		filepath.Join(env.outside, "secret.txt"):  "outside the project\n",
	} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			return nil, err
		}
	}
	return env, nil
}

// soloScript is the model of a solo session: one tool call to a turn, the next step of the script each time it is asked, and then a
// final answer. block, when set, is the turn at which it does not answer until released, after saying it was reached.
type soloScript struct {
	steps []mock.ToolCall

	blockAt int           // the turn to hold (0: none; turn 1 is the second request)
	reached chan struct{} // closed when the held turn is asked for
	release chan struct{} // the held turn answers once this is closed
	once    sync.Once
}

func (p *soloScript) respond(c *mock.Call) mock.Reply {
	k := assistantTurns(c)
	if p.blockAt > 0 && k == p.blockAt {
		p.once.Do(func() { close(p.reached) })
		<-p.release
		return mock.Reply{Text: "too late"}
	}
	if k < len(p.steps) {
		return mock.Reply{Text: fmt.Sprintf("step %d", k), ToolCalls: []mock.ToolCall{p.steps[k]}}
	}
	return mock.Reply{Text: "done"}
}

// openSolo starts a session of one agent against a mock endpoint that plays script. The function it returns stops the endpoint.
func openSolo(ctx context.Context, env *soloEnv, respond mock.Responder, prompter perm.Prompter) (s *session.Session, sdir string, stop func(), err error) {
	srv := mock.New(mock.Config{Engine: mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64}}, respond)
	ts := srv.Start()
	prof := openaichat.DefaultProfile("solo", ts.URL)
	client := openaichat.New(openaichat.Config{Name: "solo", BaseURL: ts.URL, Profile: &prof,
		Options: openaichat.Options{SessionHeader: true, CacheKeyBody: true}})
	model := cost.Model{ID: "solo-model", ContextTokens: 1_000_000, MaxOutput: 8192, Cache: cost.OpenAICacheModel(),
		Price: cost.Price{InputPerM: 2, OutputPerM: 8, CacheReadPerM: 0.5, CacheWrite5mPerM: 2, CacheWrite1hPerM: 2}}
	sdir = filepath.Join(env.root, "session")
	s, err = session.New(ctx, session.Options{
		Cwd: env.ws, Root: env.ws, Home: env.home, Dir: sdir,
		Provider: client, ModelInfo: &model, Model: model.ID,
		Mode: perm.ModeDefault, Prompter: prompter, NoWeb: true, TrustProject: true, Offline: true, NoMCP: true,
	})
	if err != nil {
		ts.Close()
		return nil, "", nil, err
	}
	return s, sdir, ts.Close, nil
}

// permSteps is what the model of the prompted and no-one sessions does, one call to a turn. The last call is the first again, under
// a name the harness has to repair (functions.write is write).
func permSteps(env *soloEnv) []mock.ToolCall {
	return []mock.ToolCall{
		call("s0", "write", map[string]any{"path": "notes/a.txt", "content": "hello\n"}),
		call("s1", "bash", map[string]any{"command": "rm -rf build"}),
		call("s2", "read", map[string]any{"path": filepath.Join(env.home, ".ssh", "id_rsa")}),
		call("s3", "read", map[string]any{"path": filepath.Join(env.outside, "secret.txt")}),
		call("s4", "functions.write", map[string]any{"path": "notes/a.txt", "content": "hello again\n"}),
	}
}

// aPerson answers the questions of the prompted session as a person who has been asked: a write is allowed for the session, a
// command that removes things is refused, anything else is allowed once. The reasons are the terminal prompter's own words.
func aPerson(_ context.Context, r perm.Request) perm.Decision {
	switch {
	case strings.Contains(r.Command, "rm -rf"):
		return perm.Decision{Reason: "denied by user"}
	case strings.EqualFold(r.Tool, "write"):
		return perm.Decision{Allow: true, Reason: "allowed by user for the session", Remember: perm.ScopeSession}
	}
	return perm.Decision{Allow: true, Reason: "allowed by user"}
}

var (
	promptedRecording recording
	noOneRecording    recording
	askingRecording   recording
	modelRecording    recording
)

func promptedLog(t testing.TB) []byte {
	t.Helper()
	return promptedRecording.get(t, "prompted", func() ([]byte, error) { return recordPermissions(aPerson) })
}

func noOneLog(t testing.TB) []byte {
	t.Helper()
	return noOneRecording.get(t, "no one to ask", func() ([]byte, error) { return recordPermissions(nil) })
}

func askingLog(t testing.TB) []byte {
	t.Helper()
	return askingRecording.get(t, "cancelled while asking", recordCancelWhileAsking)
}

func modelCancelLog(t testing.TB) []byte {
	t.Helper()
	return modelRecording.get(t, "cancelled in a request", recordCancelInRequest)
}

// recordPermissions runs the script to its end with the given prompter (nil: no one to ask).
func recordPermissions(prompter perm.Prompter) ([]byte, error) {
	root, cleanup, err := tempRoot("state-perm-")
	if err != nil {
		return nil, err
	}
	defer cleanup()
	env, err := newSoloEnv(root)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), recordingGuard)
	defer cancel()
	sc := &soloScript{steps: permSteps(env)}
	s, sdir, stop, err := openSolo(ctx, env, sc.respond, prompter)
	if err != nil {
		return nil, err
	}
	defer stop()
	defer s.Close()
	if _, err := s.Run(ctx, "write the notes, tidy up and read the secret"); err != nil {
		return nil, fmt.Errorf("the permission session: %w", err)
	}
	if err := s.Close(); err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(sdir, "events.jsonl"))
}

// recordCancelWhileAsking cancels the run while the person has not answered: the prompter says the question was put, and is then
// held until the run's context ends, as a prompt at a terminal is when a person presses Ctrl-C.
func recordCancelWhileAsking() ([]byte, error) {
	root, cleanup, err := tempRoot("state-ask-")
	if err != nil {
		return nil, err
	}
	defer cleanup()
	env, err := newSoloEnv(root)
	if err != nil {
		return nil, err
	}
	guard, stopGuard := context.WithTimeout(context.Background(), recordingGuard)
	defer stopGuard()
	ctx, cancel := context.WithCancel(guard)
	defer cancel()
	asked := make(chan struct{})
	var once sync.Once
	prompter := func(pctx context.Context, _ perm.Request) perm.Decision {
		once.Do(func() { close(asked) })
		<-pctx.Done()
		return perm.Decision{}
	}
	sc := &soloScript{steps: []mock.ToolCall{call("s0", "bash", map[string]any{"command": "touch x"})}}
	s, sdir, stop, err := openSolo(guard, env, sc.respond, prompter)
	if err != nil {
		return nil, err
	}
	defer stop()
	defer s.Close()
	ran := make(chan error, 1)
	go func() { _, err := s.Run(ctx, "touch a file"); ran <- err }()
	select {
	case <-asked:
	case err := <-ran:
		return nil, fmt.Errorf("the run ended before the question was put: %v", err)
	case <-guard.Done():
		return nil, guard.Err()
	}
	cancel()
	if err := <-ran; err == nil {
		return nil, fmt.Errorf("a cancelled run returned no error")
	}
	if err := s.Close(); err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(sdir, "events.jsonl"))
}

// recordCancelInRequest cancels the run while its second request is out: the endpoint says the request reached it, and answers only
// when the test lets it (after the run has returned).
func recordCancelInRequest() ([]byte, error) {
	root, cleanup, err := tempRoot("state-model-")
	if err != nil {
		return nil, err
	}
	defer cleanup()
	env, err := newSoloEnv(root)
	if err != nil {
		return nil, err
	}
	guard, stopGuard := context.WithTimeout(context.Background(), recordingGuard)
	defer stopGuard()
	ctx, cancel := context.WithCancel(guard)
	defer cancel()
	sc := &soloScript{steps: []mock.ToolCall{call("s0", "read", map[string]any{"path": "README.md"})},
		blockAt: 1, reached: make(chan struct{}), release: make(chan struct{})}
	s, sdir, stop, err := openSolo(guard, env, sc.respond, nil)
	if err != nil {
		return nil, err
	}
	defer stop()
	defer s.Close()
	defer close(sc.release) // the endpoint's handler is let go before the endpoint is closed (defers run last to first)
	ran := make(chan error, 1)
	go func() { _, err := s.Run(ctx, "read the readme"); ran <- err }()
	select {
	case <-sc.reached:
	case err := <-ran:
		return nil, fmt.Errorf("the run ended before the request was out: %v", err)
	case <-guard.Done():
		return nil, guard.Err()
	}
	cancel()
	if err := <-ran; err == nil {
		return nil, fmt.Errorf("a cancelled run returned no error")
	}
	if err := s.Close(); err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(sdir, "events.jsonl"))
}
