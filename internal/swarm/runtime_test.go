package swarm_test

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/agent"
	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/cost"
	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/kv"
	"github.com/reee344/sleipnir/internal/perm"
	"github.com/reee344/sleipnir/internal/provider/mock"
	"github.com/reee344/sleipnir/internal/provider/openaichat"
	"github.com/reee344/sleipnir/internal/swarm"
	"github.com/reee344/sleipnir/internal/tools"
)

// fake tools ---------------------------------------------------------------

type fake struct {
	name string
	ro   bool
	run  func(ctx context.Context, c *tools.Call) *tools.Result
}

func (f fake) Spec() core.ToolSpec {
	return core.ToolSpec{Name: f.name, Description: "fake " + f.name, InputSchema: json.RawMessage(`{"type":"object"}`), ReadOnly: f.ro}
}
func (f fake) Run(ctx context.Context, c *tools.Call) (*tools.Result, error) {
	return f.run(ctx, c), nil
}

func pathOf(in json.RawMessage) string {
	var m struct{ Path string }
	json.Unmarshal(in, &m)
	return m.Path
}

func editTool() fake {
	return fake{name: "edit", run: func(ctx context.Context, c *tools.Call) *tools.Result {
		env := c.Env.Defaults()
		p := pathOf(c.Input)
		if d := env.Perm.Check(ctx, perm.Request{Agent: env.Agent, Role: env.Role, Tool: "edit", Paths: []string{p}, Writes: true, Summary: "edit " + p}); !d.Allow {
			return tools.Errorf("denied: %s", d.Reason)
		}
		if err := env.Guard.BeforeWrite(env.Agent, p); err != nil {
			return tools.Errorf("%v", err)
		}
		env.Guard.AfterWrite(env.Agent, p)
		return &tools.Result{Text: "edited " + p}
	}}
}

func bashTool() fake {
	return fake{name: "bash", run: func(ctx context.Context, c *tools.Call) *tools.Result {
		return &tools.Result{Text: "ok\n[exit code 0]"}
	}}
}

// rig ------------------------------------------------------------------------

type rig struct {
	t   *testing.T
	sw  *swarm.Swarm
	srv *mock.Server
	log *events.MemLog
}

func newRig(t *testing.T, cfg swarm.Config, mcfg mock.Config, r mock.Responder) *rig {
	t.Helper()
	agent.RetryBase = time.Millisecond
	srv := mock.New(mcfg, r)
	ts := srv.Start()
	t.Cleanup(ts.Close)
	client := openaichat.New(openaichat.Config{Name: "mock", BaseURL: ts.URL, Options: openaichat.Options{SessionHeader: true, CacheKeyBody: true}})
	model := cost.Model{ID: "mock-1", ContextTokens: 1_000_000, Cache: cost.OpenAICacheModel(),
		Price: cost.Price{InputPerM: 4, OutputPerM: 20, CacheReadPerM: 1, CacheWrite5mPerM: 4, CacheWrite1hPerM: 4}}
	log := events.NewMemLog()
	blobs := events.NewMemBlobs()
	deps := swarm.Deps{
		Provider: client, Model: model, Events: log, Blobs: blobs, Archive: kv.NewArchive(blobs),
		Const:   kv.NewLayer("const", kv.KindConst, 1, []kv.Segment{{Text: agent.Constitution(agent.ConstitutionOpts{Swarm: true})}}),
		Shared:  kv.NewLayer("shared", kv.KindShared, 1, []kv.Segment{{Key: "project", Text: strings.Repeat("The repo is a Go API with a React client. ", 120), Vol: kv.VolEpoch}}),
		Workdir: t.TempDir(), Root: t.TempDir(), Params: core.Params{MaxTokens: 512},
		Files: tools.NewFileState(), Handles: tools.NewHandles(),
	}
	sw := swarm.New(cfg, deps, nil)
	reg := tools.NewRegistry()
	for _, f := range []fake{editTool(), bashTool(), {name: "read", ro: true, run: func(context.Context, *tools.Call) *tools.Result { return &tools.Result{Text: "contents"} }}} {
		reg.Register(f)
	}
	for _, tl := range sw.Tools() {
		reg.Register(tl)
	}
	specs, err := reg.Specs()
	if err != nil {
		t.Fatal(err)
	}
	sw.SetToolset(reg, specs)
	sw.Start(context.Background())
	t.Cleanup(sw.Shutdown)
	return &rig{t: t, sw: sw, srv: srv, log: log}
}

// scripting helpers -----------------------------------------------------------

var whoRe = regexp.MustCompile(`you: (\S+) \((\w+)\)`)
var taskRe = regexp.MustCompile(`task (T\d+)`)

func who(c *mock.Call) (id, role string) {
	for i := len(c.Messages) - 1; i >= 0; i-- {
		if c.Messages[i].Role == "user" {
			if m := whoRe.FindStringSubmatch(c.Messages[i].Content); m != nil {
				return m[1], m[2]
			}
		}
	}
	return "", ""
}

func assistantTurns(c *mock.Call) int {
	n := 0
	for _, m := range c.Messages {
		if m.Role == "assistant" {
			n++
		}
	}
	return n
}

func taskOf(c *mock.Call) string {
	for _, m := range c.Messages {
		if m.Role == "user" {
			if x := taskRe.FindStringSubmatch(m.Content); x != nil {
				return x[1]
			}
		}
	}
	return ""
}

func call(id, name string, args any) mock.ToolCall {
	b, _ := json.Marshal(args)
	return mock.ToolCall{ID: id, Name: name, Args: string(b)}
}

// scenario: a manager plans two tasks, spawns a backend and a frontend worker,
// waits, accepts, and finishes.
func teamResponder() mock.Responder {
	var mu sync.Mutex
	return func(c *mock.Call) mock.Reply {
		mu.Lock()
		defer mu.Unlock()
		id, role := who(c)
		n := assistantTurns(c)
		if role == "manager" {
			switch n {
			case 0:
				return mock.Reply{Text: "planning", ToolCalls: []mock.ToolCall{
					call("m1", "task", map[string]any{"action": "create", "title": "Paginate GET /users", "role": "backend", "files": []string{"api/**"}}),
					call("m2", "task", map[string]any{"action": "create", "title": "Users table UI", "role": "frontend", "files": []string{"web/**"}}),
					call("m3", "spawn", map[string]any{"role": "backend", "task": "T1"}),
					call("m4", "spawn", map[string]any{"role": "frontend", "task": "T2"}),
					call("m5", "wait", map[string]any{"until": []string{"T1", "T2"}, "timeout_sec": 20}),
				}}
			case 1:
				return mock.Reply{Text: "reviewing", ToolCalls: []mock.ToolCall{
					call("m6", "task", map[string]any{"action": "accept", "id": "T1", "text": "looks good"}),
					call("m7", "task", map[string]any{"action": "accept", "id": "T2"}),
				}}
			}
			return mock.Reply{Text: "all done: users are paginated and the table uses it"}
		}
		// workers
		tid := taskOf(c)
		file := "api/users.go"
		if role == "frontend" {
			file = "web/table.tsx"
		}
		switch n {
		case 0:
			return mock.Reply{Text: "on it", ToolCalls: []mock.ToolCall{
				call("w1", "read", map[string]any{"path": file}),
				call("w2", "edit", map[string]any{"path": file}),
				call("w3", "bash", map[string]any{"command": "go test ./..."}),
			}}
		case 1:
			return mock.Reply{Text: "finishing", ToolCalls: []mock.ToolCall{call("w4", "task", map[string]any{"action": "done", "id": tid, "text": "implemented " + id})}}
		}
		return mock.Reply{Text: "summary from " + id}
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// tests -----------------------------------------------------------------------

func TestManagerDispatchesWorkersAndCollectsResults(t *testing.T) {
	r := newRig(t, swarm.Config{SessionID: "s1", MaxWriters: 4}, mock.Config{Engine: mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64}}, teamResponder())
	res, err := r.sw.RunManager(context.Background(), "make the users list paginated end to end")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "all done") {
		t.Fatalf("manager result = %q", res.Text)
	}
	snap := r.sw.Board.Snapshot()
	for _, id := range []string{"T1", "T2"} {
		tk, ok := snap.Task(id)
		if !ok || tk.Status != swarm.StatusDone || tk.Owner == "" {
			t.Fatalf("%s = %+v", id, tk)
		}
		if !strings.Contains(tk.Result, "edited 1") || !strings.Contains(tk.Result, "go test ./...") || !strings.Contains(tk.Result, "passed") {
			t.Fatalf("%s result should carry harness-observed evidence, got %q", id, tk.Result)
		}
	}
	// Workers reused the manager's warm shared prefix instead of re-writing it.
	firstHit := map[string]float64{}
	for _, e := range r.log.OfType(events.TypeModelResponse) {
		var m struct {
			Req string  `json:"req"`
			Hit float64 `json:"hit_ratio"`
		}
		json.Unmarshal(e.Data, &m)
		if strings.HasSuffix(m.Req, ".1") {
			firstHit[e.Agent] = m.Hit
		}
	}
	if firstHit["mgr"] != 0 {
		t.Fatalf("the very first request of the session cannot hit: %v", firstHit)
	}
	for _, id := range []string{"be-1", "fe-1"} {
		if firstHit[id] < 0.5 {
			t.Fatalf("%s first-request hit ratio %.2f: workers should read the shared prefix from cache (%v)", id, firstHit[id], firstHit)
		}
	}
	// Status was derived by the harness: agents ended idle, leases released.
	waitFor(t, "workers idle", func() bool {
		s := r.sw.Board.Snapshot()
		a, _ := s.Agent("be-1")
		b, _ := s.Agent("fe-1")
		return a.State == "idle" && b.State == "idle"
	})
	if h := r.sw.Leases.HeldBy("be-1"); len(h) != 0 {
		t.Fatalf("leases must be released at completion: %v", h)
	}
	if anoms := r.log.OfType(events.TypeCacheAnomaly); len(anoms) != 0 {
		t.Fatalf("cache anomalies: %s", anoms[0].Data)
	}
}

func TestWriterCapAndScopeOverlap(t *testing.T) {
	r := newRig(t, swarm.Config{SessionID: "s2", MaxWriters: 1}, mock.Config{}, func(c *mock.Call) mock.Reply {
		time.Sleep(200 * time.Millisecond) // keep the first worker busy
		return mock.Reply{Text: "ok"}
	})
	if _, err := r.sw.StartManager(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.sw.Spawn(swarm.SpawnReq{Role: "backend", Title: "API work", Files: []string{"api/**"}, By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	_, err := r.sw.Spawn(swarm.SpawnReq{Role: "backend", Title: "More API work", Files: []string{"api/handlers/**"}, By: "mgr"})
	if err == nil || !strings.Contains(err.Error(), "overlaps") {
		t.Fatalf("overlapping scope must be refused: %v", err)
	}
	_, err = r.sw.Spawn(swarm.SpawnReq{Role: "frontend", Title: "UI", Files: []string{"web/**"}, By: "mgr"})
	if err == nil || !strings.Contains(err.Error(), "writers are already active") {
		t.Fatalf("writer cap: %v", err)
	}
	// Readers are not capped by the writer limit.
	if _, err := r.sw.Spawn(swarm.SpawnReq{Role: "reviewer", Title: "Review API work", By: "mgr"}); err != nil {
		t.Fatalf("read-only roles scale freely: %v", err)
	}
	if _, err := r.sw.Spawn(swarm.SpawnReq{Role: "nonesuch", Title: "x", By: "mgr"}); err == nil || !strings.Contains(err.Error(), "unknown role") {
		t.Fatalf("unknown role: %v", err)
	}
}

func TestReviewerIsReadOnly(t *testing.T) {
	var mu sync.Mutex
	var denial string
	r := newRig(t, swarm.Config{SessionID: "s3"}, mock.Config{}, func(c *mock.Call) mock.Reply {
		mu.Lock()
		defer mu.Unlock()
		id, role := who(c)
		if role != "reviewer" {
			return mock.Reply{Text: "idle"}
		}
		switch assistantTurns(c) {
		case 0:
			return mock.Reply{ToolCalls: []mock.ToolCall{call("r1", "edit", map[string]any{"path": "api/users.go"})}}
		case 1:
			for _, m := range c.Messages {
				if m.Role == "tool" && strings.Contains(m.Content, "read-only") {
					denial = m.Content
				}
			}
			return mock.Reply{ToolCalls: []mock.ToolCall{call("r2", "task", map[string]any{"action": "done", "id": taskOf(c), "text": "reviewed " + id})}}
		}
		return mock.Reply{Text: "review complete"}
	})
	r.sw.StartManager()
	id, err := r.sw.Spawn(swarm.SpawnReq{Role: "reviewer", Title: "Review", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "reviewer to finish", func() bool { a, _ := r.sw.Board.Snapshot().Agent(id); return a.State == "idle" })
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(denial, "read-only") {
		t.Fatalf("reviewer's write must be denied by role, got %q", denial)
	}
}

func TestDoneIsGatedByVerifier(t *testing.T) {
	var mu sync.Mutex
	verifications := 0
	cfg := swarm.Config{SessionID: "s4", VerifyCmd: "go test ./...", Verify: func(ctx context.Context, dir, cmd string) (string, int, error) {
		mu.Lock()
		defer mu.Unlock()
		verifications++
		if verifications == 1 {
			return "--- FAIL: TestPaginate\nexpected 10 got 11", 1, nil
		}
		return "ok", 0, nil
	}}
	var sawFailure bool
	r := newRig(t, cfg, mock.Config{}, func(c *mock.Call) mock.Reply {
		mu.Lock()
		defer mu.Unlock()
		id, role := who(c)
		if role == "manager" {
			return mock.Reply{Text: "waiting"}
		}
		tid := taskOf(c)
		switch assistantTurns(c) {
		case 0:
			return mock.Reply{ToolCalls: []mock.ToolCall{call("a", "edit", map[string]any{"path": "api/x.go"}), call("b", "task", map[string]any{"action": "done", "id": tid, "text": "first try"})}}
		case 1:
			for _, m := range c.Messages {
				if m.Role == "tool" && strings.Contains(m.Content, "Not done: verification") && strings.Contains(m.Content, "expected 10 got 11") {
					sawFailure = true
				}
			}
			return mock.Reply{ToolCalls: []mock.ToolCall{call("c", "edit", map[string]any{"path": "api/x.go"}), call("d", "task", map[string]any{"action": "done", "id": tid, "text": "fixed " + id})}}
		}
		return mock.Reply{Text: "done"}
	})
	r.sw.StartManager()
	id, err := r.sw.Spawn(swarm.SpawnReq{Role: "backend", Title: "Fix pagination", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "task to reach review", func() bool { tk, _ := r.sw.Board.Snapshot().Task("T1"); return tk.Status == swarm.StatusReview })
	mu.Lock()
	defer mu.Unlock()
	if !sawFailure || verifications != 2 {
		t.Fatalf("model must see the failed verification (saw=%v, verifications=%d) and only then finish", sawFailure, verifications)
	}
	tk, _ := r.sw.Board.Snapshot().Task("T1")
	if !strings.Contains(tk.Result, "fixed "+id) {
		t.Fatalf("result = %q", tk.Result)
	}
}

func TestMailWakesIdleWorkerAndRespectsRules(t *testing.T) {
	var mu sync.Mutex
	got := ""
	r := newRig(t, swarm.Config{SessionID: "s5"}, mock.Config{}, func(c *mock.Call) mock.Reply {
		mu.Lock()
		defer mu.Unlock()
		id, role := who(c)
		if role == "manager" {
			return mock.Reply{Text: "ok"}
		}
		for _, m := range c.Messages {
			if strings.Contains(m.Content, "[mail m1 contract from mgr]") {
				got = m.Content
			}
		}
		if assistantTurns(c) == 0 {
			return mock.Reply{Text: "started " + id}
		}
		return mock.Reply{Text: "acknowledged"}
	})
	r.sw.StartManager()
	id, _ := r.sw.Spawn(swarm.SpawnReq{Role: "backend", Title: "Work", By: "mgr"})
	waitFor(t, "worker idle", func() bool { a, _ := r.sw.Board.Snapshot().Agent(id); return a.State == "idle" })
	if _, err := r.sw.Router.Send("mgr", id, "contract", "POST /users now returns 201"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "worker to receive mail", func() bool { mu.Lock(); defer mu.Unlock(); return got != "" })
	if _, err := r.sw.Router.Send("mgr", "all", "", "hi"); err == nil {
		t.Fatal("broadcast must be refused")
	}
}
