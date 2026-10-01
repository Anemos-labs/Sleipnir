package harness_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/provider/openaichat"
	"github.com/anemos-labs/sleipnir/internal/rl"
	"github.com/anemos-labs/sleipnir/internal/rl/env"
	"github.com/anemos-labs/sleipnir/internal/rl/harness"
)

func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/demo\n\ngo 1.24\n")
	write("demo.go", "package demo\n\n// Add adds.\nfunc Add(a, b int) int { return a - b }\n")
	write("demo_test.go", "package demo\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(1, 2) != 3 {\n\t\tt.Fatal(\"Add is broken\")\n\t}\n}\n")
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "init"}} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v %s", err, out)
		}
	}
	return dir
}

func call(id, name string, args any) mock.ToolCall {
	b, _ := json.Marshal(args)
	return mock.ToolCall{ID: id, Name: name, Args: string(b)}
}

func turns(c *mock.Call) int {
	n := 0
	for _, m := range c.Messages {
		if m.Role == "assistant" {
			n++
		}
	}
	return n
}

// rig is a policy endpoint (the cache-faithful mock) plus the recorded traffic.
type rig struct {
	url  string
	mu   sync.Mutex
	auth []string
	body [][]byte
	last []mock.Msg
}

func startPolicy(t *testing.T, r mock.Responder) *rig {
	t.Helper()
	agent.RetryBase = time.Millisecond
	rg := &rig{}
	srv := mock.New(mock.Config{Engine: mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64}}, func(c *mock.Call) mock.Reply {
		rg.mu.Lock()
		rg.last = c.Messages
		rg.mu.Unlock()
		return r(c)
	})
	h := srv.Handler()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(b))
		rg.mu.Lock()
		rg.auth = append(rg.auth, req.Header.Get("Authorization"))
		rg.body = append(rg.body, b)
		rg.mu.Unlock()
		h.ServeHTTP(w, req)
	}))
	t.Cleanup(ts.Close)
	rg.url = ts.URL
	return rg
}

func (r *rig) bodies() [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]byte(nil), r.body...)
}

// spec builds a RunSpec the way env.Runner would: a private HOME, an explicit
// environment, a run directory.
func spec(t *testing.T, repo, prompt string) env.RunSpec {
	t.Helper()
	home := t.TempDir()
	return env.RunSpec{
		Task:      rl.Task{ID: "demo/fix add", Kind: rl.TaskFix, Prompt: prompt, Verifier: rl.Verifier{Cmd: "go test ./..."}},
		Workspace: repo, RunDir: filepath.Join(t.TempDir(), "run"),
		Policy:  env.PolicySpec{Model: "mock-1"},
		Capture: true, Sample: 3, Group: "g1", Attempt: 1, Seed: 7,
		// GOCACHE is shared across the tests of this package only to keep them fast;
		// the runner's own environment builder keeps caches private.
		Env: []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "TMPDIR=" + home, "GOFLAGS=-mod=mod", "GOTOOLCHAIN=local", "GOCACHE=" + goCache(t)},
	}
}

var (
	goCacheOnce sync.Once
	goCacheDir  string
)

func goCache(t *testing.T) string {
	goCacheOnce.Do(func() {
		out, err := exec.Command("go", "env", "GOCACHE").Output()
		if err == nil {
			goCacheDir = strings.TrimSpace(string(out))
		}
	})
	if goCacheDir == "" {
		return filepath.Join(t.TempDir(), "gocache")
	}
	return goCacheDir
}

// injected serves the policy from the mock server through the normal adapter.
func injected(url string) func(env.RunSpec) (provider.Provider, cost.Model, error) {
	return func(env.RunSpec) (provider.Provider, cost.Model, error) {
		prof := openaichat.DefaultProfile("mock", url)
		prof.CaptureTokens = true
		c := openaichat.New(openaichat.Config{Name: "mock", BaseURL: url, Profile: &prof, Options: openaichat.Options{SessionHeader: true, CacheKeyBody: true}})
		return c, cost.Model{ID: "mock-1", ContextTokens: 1_000_000, MaxOutput: 4096, Cache: cost.OpenAICacheModel(),
			Price: cost.Price{InputPerM: 4, OutputPerM: 20, CacheReadPerM: 1, CacheWrite5mPerM: 4, CacheWrite1hPerM: 4}}, nil
	}
}

func readEvents(t *testing.T, dir string) []events.Event {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var out []events.Event
	for _, ln := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var e events.Event
		if err := json.Unmarshal([]byte(ln), &e); err != nil {
			t.Fatalf("bad event line %q: %v", ln, err)
		}
		out = append(out, e)
	}
	return out
}

func TestRunFixesTheTaskAndLeavesAClosedLog(t *testing.T) {
	repo := newRepo(t)
	pol := startPolicy(t, func(c *mock.Call) mock.Reply {
		switch turns(c) {
		case 0:
			return mock.Reply{Text: "read", ToolCalls: []mock.ToolCall{call("c1", "read", map[string]any{"path": "demo.go"})}}
		case 1:
			return mock.Reply{Text: "fix", ToolCalls: []mock.ToolCall{call("c2", "edit", map[string]any{
				"path": "demo.go", "old_string": "a - b", "new_string": "a + b"})}}
		case 2:
			return mock.Reply{Text: "test", ToolCalls: []mock.ToolCall{call("c3", "bash", map[string]any{"command": "go test ./..."})}}
		}
		return mock.Reply{Text: "fixed Add; go test passes"}
	})
	h := &harness.Harness{NewProvider: injected(pol.url)}
	sp := spec(t, repo, "Add returns the wrong result. Fix it.")

	res, err := h.Run(context.Background(), sp)
	if err != nil {
		t.Fatal(err)
	}
	if res.Claimed != "done" || !strings.Contains(res.FinalMessage, "fixed Add") || res.Err != nil {
		t.Fatalf("result: %+v", res)
	}
	got, _ := os.ReadFile(filepath.Join(repo, "demo.go"))
	if !strings.Contains(string(got), "a + b") {
		t.Fatalf("the edit did not land in the workspace:\n%s", got)
	}

	// The tests ran for real, under the private environment, and passed.
	var ranTests bool
	for _, e := range readEvents(t, sp.RunDir) {
		if e.Type == events.TypeTurnAppend && strings.Contains(string(e.Data), "ok  \\texample.com/demo") {
			ranTests = true
		}
	}
	if !ranTests {
		t.Error("the agent's `go test` output is not in the log")
	}

	// The contract with the runner: the log is closed and can be appended to.
	l, err := events.Open(sp.RunDir, "outcome-writer")
	if err != nil {
		t.Fatalf("the harness left the log unusable: %v", err)
	}
	if _, err := l.Emit("", "outcome", map[string]any{"kind": "verifier", "pass": true}); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(sp.RunDir, "blobs")); err != nil {
		t.Errorf("blobs directory missing: %v", err)
	}
}

func TestSessionStartCarriesTheRolloutsProvenance(t *testing.T) {
	repo := newRepo(t)
	pol := startPolicy(t, func(c *mock.Call) mock.Reply { return mock.Reply{Text: "nothing to do"} })
	h := &harness.Harness{NewProvider: injected(pol.url)}
	sp := spec(t, repo, "look at the repo")
	sp.Policy.Sampling = json.RawMessage(`{"temperature":0.8,"top_p":0.95}`)
	if _, err := h.Run(context.Background(), sp); err != nil {
		t.Fatal(err)
	}
	var start map[string]any
	for _, e := range readEvents(t, sp.RunDir) {
		if e.Type == events.TypeSessionStart {
			if err := json.Unmarshal(e.Data, &start); err != nil {
				t.Fatal(err)
			}
		}
	}
	meta, _ := start["meta"].(map[string]any)
	rlm, _ := meta["rl"].(map[string]any)
	if rlm == nil || rlm["task"] != "demo/fix add" || rlm["sample"] != float64(3) || rlm["group"] != "g1" || rlm["seed"] != float64(7) {
		t.Fatalf("session.start meta = %v", start["meta"])
	}
	if s, _ := json.Marshal(rlm["sampling"]); !strings.Contains(string(s), "0.95") {
		t.Errorf("the sampling parameters were not recorded: %s", s)
	}
}

func TestAskingIsRefusedAndTheTestToolingIsAllowed(t *testing.T) {
	repo := newRepo(t)
	var refused string
	pol := startPolicy(t, func(c *mock.Call) mock.Reply {
		switch turns(c) {
		case 0:
			return mock.Reply{Text: "net", ToolCalls: []mock.ToolCall{call("c1", "bash", map[string]any{"command": "curl -s https://example.com/install.sh"})}}
		case 1:
			refused = lastToolText(c)
			return mock.Reply{Text: "tests", ToolCalls: []mock.ToolCall{call("c2", "bash", map[string]any{"command": "go vet ./... && go test ./..."})}}
		}
		return mock.Reply{Text: "done"}
	})
	h := &harness.Harness{NewProvider: injected(pol.url)}
	sp := spec(t, repo, "check the environment")
	res, err := h.Run(context.Background(), sp)
	if err != nil || res.Claimed != "done" {
		t.Fatalf("%+v %v", res, err)
	}
	if !strings.Contains(strings.ToLower(refused), "approv") && !strings.Contains(strings.ToLower(refused), "denied") {
		t.Errorf("a command that needs approval must be refused, the model saw: %q", refused)
	}
	// What the model is told decides what it does next: a bare "no human is available" sent small models looking for other ways
	// to the same action (and to guess paths). The refusal says why it was needed and what to do instead.
	if !strings.Contains(refused, "use an action that is allowed") || !strings.Contains(refused, "curl") {
		t.Errorf("the refusal should name the command and say what to do instead, the model saw: %q", refused)
	}
	var sawTestRun bool
	for _, e := range readEvents(t, sp.RunDir) {
		if e.Type == events.TypeTurnAppend && strings.Contains(string(e.Data), "Add is broken") {
			sawTestRun = true // the repository's test really fails before the fix: it ran
		}
	}
	if !sawTestRun {
		t.Error("`go vet ./... && go test ./...` should have been allowed and run")
	}
}

func lastToolText(c *mock.Call) string {
	for i := len(c.Messages) - 1; i >= 0; i-- {
		if c.Messages[i].Role == "tool" {
			return c.Messages[i].Content
		}
	}
	return ""
}

func TestTheAgentsShellSeesOnlyThePreparedEnvironment(t *testing.T) {
	t.Setenv("RL_HOST_CANARY_VALUE", "must-not-reach-the-agent")
	t.Setenv("RL_POLICY_KEY_FOR_TEST", "canary-key-not-real")
	repo := newRepo(t)
	var seen string
	pol := startPolicy(t, func(c *mock.Call) mock.Reply {
		if turns(c) == 0 {
			return mock.Reply{Text: "env", ToolCalls: []mock.ToolCall{call("c1", "bash", map[string]any{"command": `echo "HOME=$HOME"; echo "CANARY=${RL_HOST_CANARY_VALUE:-unset}"; echo "KEY=${RL_POLICY_KEY_FOR_TEST:-unset}"`})}}
		}
		seen = lastToolText(c)
		return mock.Reply{Text: "done"}
	})
	h := &harness.Harness{NewProvider: injected(pol.url)}
	sp := spec(t, repo, "print the environment")
	if _, err := h.Run(context.Background(), sp); err != nil {
		t.Fatal(err)
	}
	home := ""
	for _, kv := range sp.Env {
		if v, ok := strings.CutPrefix(kv, "HOME="); ok {
			home = v
		}
	}
	if !strings.Contains(seen, "HOME="+home) || !strings.Contains(seen, "CANARY=unset") || !strings.Contains(seen, "KEY=unset") {
		t.Fatalf("the agent's shell environment is not the prepared one:\n%s", seen)
	}
}

func TestARunWithoutAPreparedEnvironmentDoesNotInheritTheHosts(t *testing.T) {
	t.Setenv("RL_HOST_CANARY_VALUE", "must-not-reach-the-agent")
	repo := newRepo(t)
	var seen string
	pol := startPolicy(t, func(c *mock.Call) mock.Reply {
		if turns(c) == 0 {
			return mock.Reply{Text: "env", ToolCalls: []mock.ToolCall{call("c1", "bash", map[string]any{"command": `echo "CANARY=${RL_HOST_CANARY_VALUE:-unset}"`})}}
		}
		seen = lastToolText(c)
		return mock.Reply{Text: "done"}
	})
	h := &harness.Harness{NewProvider: injected(pol.url)}
	sp := spec(t, repo, "print the environment")
	sp.Env = nil
	if _, err := h.Run(context.Background(), sp); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(seen, "CANARY=unset") {
		t.Fatalf("a rollout without an environment inherited the host's: %q", seen)
	}
}

func TestRequestBudgetEndsTheRunAsABudgetOutcome(t *testing.T) {
	repo := newRepo(t)
	pol := startPolicy(t, func(c *mock.Call) mock.Reply {
		return mock.Reply{Text: "again", ToolCalls: []mock.ToolCall{call("c"+string(rune('a'+turns(c))), "bash", map[string]any{"command": "echo hi"})}}
	})
	h := &harness.Harness{NewProvider: injected(pol.url)}
	sp := spec(t, repo, "loop forever")
	sp.Budget.Requests = 3
	res, err := h.Run(context.Background(), sp)
	if err != nil {
		t.Fatalf("running out of budget is the policy's doing, not an infrastructure error: %v", err)
	}
	if res.Claimed != "budget" || res.Err == nil {
		t.Fatalf("result: %+v", res)
	}
	if n := len(pol.bodies()); n != 3 {
		t.Errorf("the endpoint saw %d requests, the budget was 3", n)
	}
}

func TestStepBudgetEndsTheRunAsABudgetOutcome(t *testing.T) {
	repo := newRepo(t)
	pol := startPolicy(t, func(c *mock.Call) mock.Reply {
		return mock.Reply{Text: "again", ToolCalls: []mock.ToolCall{call("c"+string(rune('a'+turns(c))), "bash", map[string]any{"command": "echo hi"})}}
	})
	h := &harness.Harness{NewProvider: injected(pol.url)}
	sp := spec(t, repo, "loop forever")
	sp.Budget.Steps = 4
	res, err := h.Run(context.Background(), sp)
	if err != nil || res.Claimed != "budget" {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestAnUnreachablePolicyIsAnInfrastructureError(t *testing.T) {
	repo := newRepo(t)
	agent.RetryBase = time.Millisecond
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", 502) }))
	defer dead.Close()
	h := &harness.Harness{}
	sp := spec(t, repo, "anything")
	sp.Policy = env.PolicySpec{Model: "m", BaseURL: dead.URL}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := h.Run(ctx, sp)
	if !env.IsInfra(err) {
		t.Fatalf("a dead endpoint must not become a (zero-reward) episode: %v", err)
	}
}

func TestMissingPolicyKeyIsAnInfrastructureError(t *testing.T) {
	repo := newRepo(t)
	t.Setenv("RL_TEST_NO_SUCH_KEY", "")
	h := &harness.Harness{}
	sp := spec(t, repo, "anything")
	sp.Policy = env.PolicySpec{Model: "m", BaseURL: "http://127.0.0.1:9/v1", APIKeyEnv: "RL_TEST_NO_SUCH_KEY"}
	_, err := h.Run(context.Background(), sp)
	if !env.IsInfra(err) || !strings.Contains(err.Error(), "RL_TEST_NO_SUCH_KEY") {
		t.Fatalf("got %v", err)
	}
}

func TestPolicyEndpointGetsTheKeySamplingAndSeedFromTheSpec(t *testing.T) {
	repo := newRepo(t)
	pol := startPolicy(t, func(c *mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	t.Setenv("RL_TEST_POLICY_KEY", "policy-key-for-test")
	h := &harness.Harness{}
	sp := spec(t, repo, "hello")
	sp.Policy = env.PolicySpec{
		Model: "mock-1", BaseURL: pol.url, APIKeyEnv: "RL_TEST_POLICY_KEY",
		Sampling: json.RawMessage(`{"temperature":0.7,"top_p":0.9,"top_k":40,"max_tokens":777}`),
	}
	if _, err := h.Run(context.Background(), sp); err != nil {
		t.Fatal(err)
	}
	bodies := pol.bodies()
	if len(bodies) == 0 {
		t.Fatal("no request reached the policy")
	}
	pol.mu.Lock()
	auth := pol.auth[0]
	pol.mu.Unlock()
	if auth != "Bearer policy-key-for-test" {
		t.Errorf("Authorization = %q", auth)
	}
	var m map[string]any
	if err := json.Unmarshal(bodies[0], &m); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]float64{"temperature": 0.7, "top_p": 0.9, "top_k": 40, "seed": 7} {
		if got, _ := m[k].(float64); got != want {
			t.Errorf("request %s = %v, want %v (body %s)", k, m[k], want, bodies[0])
		}
	}
	if got, _ := m["max_tokens"].(float64); got != 777 {
		t.Errorf("max_tokens = %v", m["max_tokens"])
	}
	if m["return_token_ids"] != true {
		t.Errorf("capture was asked for but the request does not ask for token ids: %s", bodies[0])
	}
}

// Endpoints refuse sampling seeds they cannot hold: a gateway written in TypeScript above
// 2^53-1, and the upstream behind it above 2^31-1 (the first rollouts against a real marketplace
// all failed that way). Whatever the run's seeds are, the request carries one every endpoint takes.
func TestTheSamplingSeedSentToTheEndpointIsOneEveryEndpointAccepts(t *testing.T) {
	for _, seed := range []int64{1 << 62, math.MaxInt64, 1<<53 + 1, 1<<53 - 1, 1<<31 + 5, 1<<31 - 1, 12345} {
		repo := newRepo(t)
		pol := startPolicy(t, func(c *mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
		h := &harness.Harness{}
		sp := spec(t, repo, "hello")
		sp.Policy = env.PolicySpec{Model: "mock-1", BaseURL: pol.url}
		sp.Seed = seed
		if _, err := h.Run(context.Background(), sp); err != nil {
			t.Fatal(err)
		}
		bodies := pol.bodies()
		if len(bodies) == 0 {
			t.Fatal("no request reached the policy")
		}
		var m struct {
			Seed json.Number `json:"seed"`
		}
		dec := json.NewDecoder(bytes.NewReader(bodies[0]))
		dec.UseNumber()
		if err := dec.Decode(&m); err != nil {
			t.Fatal(err)
		}
		got, err := m.Seed.Int64()
		if err != nil {
			t.Fatalf("seed %d: the request's seed %q is not an integer: %v", seed, m.Seed, err)
		}
		if got < 0 || got > env.MaxWireSeed {
			t.Errorf("seed %d went out as %d, beyond %d", seed, got, int64(env.MaxWireSeed))
		}
		if seed <= env.MaxWireSeed && got != seed {
			t.Errorf("a seed the wire can hold was changed: %d -> %d", seed, got)
		}
	}
}

func TestCancellationIsPassedThroughForTheRunnerToInterpret(t *testing.T) {
	repo := newRepo(t)
	started := make(chan struct{}, 1)
	pol := startPolicy(t, func(c *mock.Call) mock.Reply {
		select {
		case started <- struct{}{}:
		default:
		}
		time.Sleep(200 * time.Millisecond)
		return mock.Reply{Text: "again", ToolCalls: []mock.ToolCall{call("c"+string(rune('a'+turns(c))), "bash", map[string]any{"command": "echo hi"})}}
	})
	h := &harness.Harness{NewProvider: injected(pol.url)}
	sp := spec(t, repo, "loop")
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-started; cancel() }()
	done := make(chan error, 1)
	go func() { _, err := h.Run(ctx, sp); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want the context error so the runner can tell cancellation from failure, got %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not stop promptly when its context ended")
	}
	// The log must be closed even then.
	l, err := events.Open(sp.RunDir, "w")
	if err != nil {
		t.Fatal(err)
	}
	l.Close()
}

// A run that the wall clock cuts off has claimed nothing. The runner turns the deadline it set into a budget outcome only when
// the harness left the claim empty; "done" there made 51 of the 349 episodes of the first benchmark run (all the ones that ran
// out the clock) false "done" claims, which the FALSEDONE metric counted and the reward's honest-done term punished.
func TestARunThatTheContextEndsClaimsNothing(t *testing.T) {
	repo := newRepo(t)
	started := make(chan struct{}, 1)
	pol := startPolicy(t, func(c *mock.Call) mock.Reply {
		select {
		case started <- struct{}{}:
		default:
		}
		time.Sleep(100 * time.Millisecond)
		return mock.Reply{Text: "again", ToolCalls: []mock.ToolCall{call("c"+string(rune('a'+turns(c))), "bash", map[string]any{"command": "echo hi"})}}
	})
	h := &harness.Harness{NewProvider: injected(pol.url)}
	sp := spec(t, repo, "loop")
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-started; cancel() }()
	res, err := h.Run(ctx, sp)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want the context error, got %v", err)
	}
	if res.Claimed != "" {
		t.Errorf("a run that was cut off claimed %q: the runner reads an empty claim as the budget ending it, and any other as the agent's word", res.Claimed)
	}
}

// A provider that gave up waiting is the endpoint's fault, not the policy's, even though its error wraps context.DeadlineExceeded:
// the run's own context is alive, so Run returns the error (the runner repeats a rollout when it does, and scores nothing) and
// not a normal ending, which was scored as the agent's "done".
func TestAProviderTimeoutWhileTheRunIsAliveIsAnInfrastructureError(t *testing.T) {
	repo := newRepo(t)
	agent.RetryBase = time.Millisecond
	h := &harness.Harness{NewProvider: func(env.RunSpec) (provider.Provider, cost.Model, error) {
		m := cost.Model{ID: "mock-1", ContextTokens: 1_000_000, MaxOutput: 4096, Cache: cost.OpenAICacheModel(),
			Price: cost.Price{InputPerM: 4, OutputPerM: 20, CacheReadPerM: 1, CacheWrite5mPerM: 4, CacheWrite1hPerM: 4}}
		return timingOut{}, m, nil
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute) // a hang guard, never the thing that ends the run
	defer cancel()
	res, err := h.Run(ctx, spec(t, repo, "anything"))
	if err == nil {
		t.Fatalf("a provider that timed out ended the run as a normal result, %+v: it is a fault of the endpoint", res)
	}
	if res.Claimed == "done" {
		t.Errorf("claimed %q although nothing was done", res.Claimed)
	}
}

// timingOut is a provider whose every request runs out of time, as an endpoint that never answers does.
type timingOut struct{}

func (timingOut) Profile() provider.Profile { return provider.Profile{Name: "timing-out"} }

func (timingOut) Do(context.Context, *provider.Request, func(provider.Event)) (*provider.Response, error) {
	return nil, &provider.Error{Kind: provider.ErrTimeout, Message: "no answer in time", Err: context.DeadlineExceeded}
}

func TestSwarmTaskRunsAManagerAndRecordsAgents(t *testing.T) {
	repo := newRepo(t)
	pol := startPolicy(t, func(c *mock.Call) mock.Reply {
		return mock.Reply{Text: "the team has nothing to do"}
	})
	h := &harness.Harness{NewProvider: injected(pol.url)}
	sp := spec(t, repo, "coordinate the fix")
	sp.Task.Kind = rl.TaskSwarm
	sp.Task.Team = rl.Team{Mode: "swarm", Agents: 3, Roles: []string{"backend", "tester"}}
	sp.Swarm, sp.Agents = true, 3
	res, err := h.Run(context.Background(), sp)
	if err != nil || res.Claimed != "done" {
		t.Fatalf("%+v %v", res, err)
	}
	var mgr bool
	for _, e := range readEvents(t, sp.RunDir) {
		if e.Agent == "mgr" {
			mgr = true
		}
	}
	if !mgr {
		t.Error("no manager events in the log")
	}
}

func TestUnknownTeamRoleIsRejectedAsATaskFault(t *testing.T) {
	repo := newRepo(t)
	pol := startPolicy(t, func(c *mock.Call) mock.Reply { return mock.Reply{Text: "x"} })
	h := &harness.Harness{NewProvider: injected(pol.url)}
	sp := spec(t, repo, "coordinate")
	sp.Task.Team = rl.Team{Mode: "swarm", Roles: []string{"astronaut"}}
	sp.Swarm = true
	_, err := h.Run(context.Background(), sp)
	if !env.IsInfra(err) || !strings.Contains(err.Error(), "astronaut") {
		t.Fatalf("a task naming an unknown role is a broken task, not a zero-reward episode: %v", err)
	}
}

func TestTaskWithoutAPromptIsRejected(t *testing.T) {
	h := &harness.Harness{NewProvider: injected("http://127.0.0.1:9")}
	sp := spec(t, newRepo(t), "  ")
	if _, err := h.Run(context.Background(), sp); !env.IsInfra(err) {
		t.Fatalf("got %v", err)
	}
}

func TestVisibleVerifyNeverExposesHiddenVerifiers(t *testing.T) {
	// Exercised through a swarm run: with hidden files the gate command must be empty,
	// which shows as workers being able to finish without a verifier command in the
	// event stream. The unit check here is on the exported behaviour of Run only.
	repo := newRepo(t)
	pol := startPolicy(t, func(c *mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	h := &harness.Harness{NewProvider: injected(pol.url)}
	sp := spec(t, repo, "coordinate")
	sp.Swarm = true
	sp.Task.Verifier = rl.Verifier{Cmd: "go test ./hidden/...", Hidden: map[string]string{"hidden/x_test.go": "text:package hidden"}}
	if _, err := h.Run(context.Background(), sp); err != nil {
		t.Fatal(err)
	}
	for _, e := range readEvents(t, sp.RunDir) {
		if strings.Contains(string(e.Data), "go test ./hidden") {
			t.Fatalf("the hidden verifier's command reached the agent-side log: %s", e.Type)
		}
	}
}

// A policy key does not travel over plain http to a host that is not this machine unless the
// operator said so: the refusal comes before any request, and names the flag that allows it.
func TestPolicyKeyStaysOffPlainHTTPToAnotherHostUnlessTheOperatorAllowsIt(t *testing.T) {
	repo := newRepo(t)
	t.Setenv("RL_TEST_LAN_KEY", "lan-key-for-test")
	lan := env.PolicySpec{Model: "m", BaseURL: "http://192.0.2.1:8000/v1", APIKeyEnv: "RL_TEST_LAN_KEY"} // TEST-NET-1: nothing answers

	_, err := (&harness.Harness{}).Run(context.Background(), specWithPolicy(t, repo, lan))
	if err == nil || !strings.Contains(err.Error(), "plain http") || !strings.Contains(err.Error(), "--allow-insecure-http") {
		t.Fatalf("a key over plain http to another host must be refused with the flag that allows it: %v", err)
	}

	// Allowed by the operator: no refusal (the attempt itself goes nowhere and is cut short).
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err = (&harness.Harness{PolicyAllowInsecureHTTP: true}).Run(ctx, specWithPolicy(t, repo, lan))
	if err != nil && strings.Contains(err.Error(), "plain http") {
		t.Fatalf("the operator allowed it: %v", err)
	}

	// The same server without a key is not carrying a credential anywhere.
	nokey := env.PolicySpec{Model: "m", BaseURL: "http://192.0.2.1:8000/v1"}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel2()
	if _, err := (&harness.Harness{}).Run(ctx2, specWithPolicy(t, repo, nokey)); err != nil && strings.Contains(err.Error(), "plain http") {
		t.Fatalf("no key, nothing to protect: %v", err)
	}
}

func specWithPolicy(t *testing.T, repo string, pol env.PolicySpec) env.RunSpec {
	t.Helper()
	sp := spec(t, repo, "anything")
	sp.Policy = pol
	return sp
}
