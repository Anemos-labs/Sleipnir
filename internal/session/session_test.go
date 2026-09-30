package session_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/agent"
	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/cost"
	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/perm"
	"github.com/reee344/sleipnir/internal/provider/mock"
	"github.com/reee344/sleipnir/internal/provider/openaichat"
	"github.com/reee344/sleipnir/internal/session"
	"github.com/reee344/sleipnir/internal/swarm"
)

// newRepo makes a small Go project under git.
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
	write("main.go", "package main\n\nfunc main() { Serve() }\n")
	write("server/server.go", "package server\n\n// Serve starts the demo server.\nfunc Serve() {}\n\ntype Config struct{ Addr string }\n\nfunc NewConfig() Config { return Config{} }\n")
	write("server/server_test.go", "package server\n\nimport \"testing\"\n\nfunc TestServe(t *testing.T) { Serve() }\n")
	write("Makefile", "test:\n\tgo test ./...\n\nlint:\n\tgo vet ./...\n")
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "init"}} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v %s", err, out)
		}
	}
	return dir
}

func startMock(t *testing.T, r mock.Responder) (*openaichat.Client, cost.Model) {
	t.Helper()
	agent.RetryBase = time.Millisecond
	srv := mock.New(mock.Config{Engine: mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64}}, r)
	ts := srv.Start()
	t.Cleanup(ts.Close)
	prof := openaichat.DefaultProfile("mock", ts.URL)
	prof.CaptureTokens = true
	client := openaichat.New(openaichat.Config{Name: "mock", BaseURL: ts.URL, Profile: &prof, Options: openaichat.Options{SessionHeader: true, CacheKeyBody: true}})
	model := cost.Model{ID: "mock-1", ContextTokens: 1_000_000, MaxOutput: 4096, Cache: cost.OpenAICacheModel(),
		Price: cost.Price{InputPerM: 4, OutputPerM: 20, CacheReadPerM: 1, CacheWrite5mPerM: 4, CacheWrite1hPerM: 4}}
	return client, model
}

func call(id, name string, args any) mock.ToolCall {
	b, _ := json.Marshal(args)
	return mock.ToolCall{ID: id, Name: name, Args: string(b)}
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

func opts(t *testing.T, repo string, client *openaichat.Client, model cost.Model) session.Options {
	return session.Options{
		Cwd: repo, Root: repo, Home: t.TempDir(), Dir: t.TempDir(),
		Provider: client, ModelInfo: &model, Model: "mock-1",
		Mode: perm.ModeBypass, NoWeb: true, TrustProject: true, CaptureTokens: true,
	}
}

func TestSingleAgentEditsARepoThroughRealTools(t *testing.T) {
	repo := newRepo(t)
	client, model := startMock(t, func(c *mock.Call) mock.Reply {
		switch assistantTurns(c) {
		case 0:
			return mock.Reply{Text: "looking", ToolCalls: []mock.ToolCall{
				call("c1", "read", map[string]any{"path": "main.go"}),
				call("c2", "write", map[string]any{"path": "hello.txt", "content": "hello from sleipnir\n"}),
			}}
		case 1:
			return mock.Reply{Text: "checking", ToolCalls: []mock.ToolCall{call("c3", "bash", map[string]any{"command": "cat hello.txt"})}}
		}
		return mock.Reply{Text: "done: created hello.txt"}
	})
	s, err := session.New(context.Background(), opts(t, repo, client, model))
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.Run(context.Background(), "create hello.txt saying hello")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "created hello.txt") || res.Steps != 3 {
		t.Fatalf("result: %+v", res)
	}
	got, err := os.ReadFile(filepath.Join(repo, "hello.txt"))
	if err != nil || string(got) != "hello from sleipnir\n" {
		t.Fatalf("the write tool must have created the file: %v %q", err, got)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// The recorded run is a training-grade recording: manifests expand to the
	// exact prompt and the shared pin carries the project map.
	evs := readEvents(t, res.Dir)
	blobs, err := events.NewDirBlobs(filepath.Join(res.Dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	var starts, ends, requests int
	msgs := map[string][]core.Hash{}
	for _, e := range evs {
		switch e.Type {
		case events.TypeSessionStart:
			starts++
		case events.TypeSessionEnd:
			ends++
		case events.TypeModelRequest:
			var q struct {
				Req      string        `json:"req"`
				Kind     string        `json:"kind"`
				Manifest core.Manifest `json:"manifest"`
			}
			if err := json.Unmarshal(e.Data, &q); err != nil {
				t.Fatal(err)
			}
			p, hs, err := q.Manifest.Expand(msgs[q.Manifest.Base], blobs.Get)
			if err != nil {
				t.Fatalf("request %s does not expand: %v", q.Req, err)
			}
			msgs[q.Req] = hs
			if requests == 0 {
				pin := p.Messages[0].Blocks[0].Text
				if !strings.Contains(pin, "server/") || !strings.Contains(pin, "NewConfig") {
					t.Fatalf("the shared pin must carry the project map, got:\n%s", pin)
				}
				if len(p.Tools) < 5 {
					t.Fatalf("expected the frozen tool list, got %d tools", len(p.Tools))
				}
			}
			requests++
		}
	}
	if starts != 1 || ends != 1 || requests != 3 {
		t.Fatalf("session.start=%d session.end=%d requests=%d", starts, ends, requests)
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

func TestProjectInstructionsNeedTrust(t *testing.T) {
	repo := newRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("Run `make test` before finishing. Never touch vendor/.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	client, model := startMock(t, func(c *mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	for _, trust := range []bool{false, true} {
		o := opts(t, repo, client, model)
		o.TrustProject = trust
		s, err := session.New(context.Background(), o)
		if err != nil {
			t.Fatal(err)
		}
		has := strings.Contains(s.Shared.Text(), "Never touch vendor")
		if has != trust {
			t.Fatalf("trust=%v: project instructions in shared layer = %v", trust, has)
		}
		s.Close()
	}
}

func TestReconIsDenseDeterministicAndBudgeted(t *testing.T) {
	repo := newRepo(t)
	build := func(budget int) *session.Recon {
		r, err := session.BuildRecon(context.Background(), session.ReconOptions{Root: repo, BudgetTokens: budget})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	a, b := build(2000), build(2000)
	if len(a.Segments) != len(b.Segments) {
		t.Fatal("segments differ between runs")
	}
	for i := range a.Segments {
		if a.Segments[i] != b.Segments[i] {
			t.Fatalf("recon must be byte-identical for an unchanged tree:\n%s\n---\n%s", a.Segments[i].Text, b.Segments[i].Text)
		}
	}
	all := ""
	for _, s := range a.Segments {
		all += s.Text
	}
	for _, want := range []string{"example.com/demo", "server/", "Serve", "NewConfig", "make test", "go test ./..."} {
		if !strings.Contains(all, want) {
			t.Errorf("survey should mention %q:\n%s", want, all)
		}
	}
	if strings.Contains(all, "TestServe") {
		t.Errorf("test files are noise in a code map:\n%s", all)
	}
	if a.Tokens > 2000+100 {
		t.Errorf("survey used %d tokens for a 2000-token budget", a.Tokens)
	}
	if tiny := build(150); tiny.Tokens > 250 {
		t.Errorf("a tiny budget must still be respected, got %d", tiny.Tokens)
	}
	if !contains(a.Commands, "make test") || !contains(a.Commands, "go test ./...") {
		t.Errorf("commands: %v", a.Commands)
	}
}

func contains(s []string, x string) bool {
	for _, v := range s {
		if v == x {
			return true
		}
	}
	return false
}

// TestSwarmSessionDispatchesWorkersWithRealTools drives manager + worker through
// the assembled session: real fs/bash tools, the swarm's board, leases and the
// shared prefix.
func TestSwarmSessionDispatchesWorkersWithRealTools(t *testing.T) {
	repo := newRepo(t)
	whoRe := regexp.MustCompile(`you: (\S+) \((\w+)\)`)
	taskRe := regexp.MustCompile(`task (T\d+)`)
	var mu sync.Mutex
	client, model := startMock(t, func(c *mock.Call) mock.Reply {
		mu.Lock()
		defer mu.Unlock()
		id, role, tid := "", "", ""
		for i := len(c.Messages) - 1; i >= 0; i-- {
			if c.Messages[i].Role != "user" {
				continue
			}
			if m := whoRe.FindStringSubmatch(c.Messages[i].Content); m != nil && id == "" {
				id, role = m[1], m[2]
			}
			if m := taskRe.FindStringSubmatch(c.Messages[i].Content); m != nil && tid == "" {
				tid = m[1]
			}
		}
		n := assistantTurns(c)
		if role == "manager" {
			switch n {
			case 0:
				return mock.Reply{Text: "plan", ToolCalls: []mock.ToolCall{
					call("m1", "task", map[string]any{"action": "create", "title": "Add notes.txt", "role": "backend", "files": []string{"notes.txt"}}),
					call("m2", "spawn", map[string]any{"role": "backend", "task": "T1"}),
					call("m3", "wait", map[string]any{"until": []string{"T1"}, "timeout_sec": 20}),
				}}
			case 1:
				return mock.Reply{Text: "accepting", ToolCalls: []mock.ToolCall{call("m4", "task", map[string]any{"action": "accept", "id": "T1"})}}
			}
			return mock.Reply{Text: "all done: notes.txt added"}
		}
		switch n {
		case 0:
			return mock.Reply{Text: "on it", ToolCalls: []mock.ToolCall{call("w1", "write", map[string]any{"path": "notes.txt", "content": "written by " + id + "\n"})}}
		case 1:
			return mock.Reply{Text: "finishing", ToolCalls: []mock.ToolCall{call("w2", "task", map[string]any{"action": "done", "id": tid, "text": "added notes.txt"})}}
		}
		return mock.Reply{Text: "summary from " + id}
	})
	o := opts(t, repo, client, model)
	o.Swarm = true
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	res, err := s.Run(context.Background(), "add a notes.txt file")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "notes.txt added") {
		t.Fatalf("manager result %q", res.Text)
	}
	b, err := os.ReadFile(filepath.Join(repo, "notes.txt"))
	if err != nil || !strings.HasPrefix(string(b), "written by be-1") {
		t.Fatalf("worker must have written through the real tool: %v %q", err, b)
	}
	snap := s.Swarm.Board.Snapshot()
	if tk, ok := snap.Task("T1"); !ok || tk.Status != swarm.StatusDone {
		t.Fatalf("T1 = %+v", tk)
	}
	// Both agents appear in the log with lineage, and the swarm's tool list is
	// identical for every agent (one tools blob).
	spawns := map[string]string{}
	tools := map[core.Hash]bool{}
	for _, e := range readEvents(t, s.Dir) {
		switch e.Type {
		case events.TypeAgentSpawn:
			var sp struct{ ID, Role string }
			json.Unmarshal(e.Data, &sp)
			spawns[sp.ID] = sp.Role
		case events.TypeModelRequest:
			var q struct {
				Manifest core.Manifest `json:"manifest"`
			}
			json.Unmarshal(e.Data, &q)
			tools[q.Manifest.Tools] = true
		}
	}
	if spawns["mgr"] != "manager" || spawns["be-1"] != "backend" {
		t.Fatalf("spawn events: %v", spawns)
	}
	if len(tools) != 1 {
		t.Fatalf("every agent must send byte-identical tools, saw %d distinct tool lists", len(tools))
	}
}

func TestResolveModelAndProviders(t *testing.T) {
	t.Setenv("HEIMDALL_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	if _, err := session.ResolveModel(nil, "some/model"); err == nil {
		t.Fatal("no provider configured must be an error, not a guess")
	}
	t.Setenv("HEIMDALL_API_KEY", "k")
	got, err := session.ResolveModel(nil, "deepseek/deepseek-v4.1-flash")
	if err != nil || got.Provider != "heimdall" || got.Model != "deepseek/deepseek-v4.1-flash" {
		t.Fatalf("bare marketplace id goes to the default provider: %+v %v", got, err)
	}
	got, err = session.ResolveModel(nil, "openrouter/x/y")
	if err != nil || got.Provider != "openrouter" || got.Model != "x/y" {
		t.Fatalf("explicit provider prefix: %+v %v", got, err)
	}
	if _, _, err := session.BuildProvider(nil, session.ModelRef{Provider: "openai", Model: "gpt"}, session.ProviderOptions{}); err == nil {
		t.Fatal("a provider whose key is unset must fail with a message naming the variable")
	}
	p, m, err := session.BuildProvider(nil, session.ModelRef{Provider: "heimdall", Model: "deepseek/deepseek-v4.1-flash"}, session.ProviderOptions{CaptureTokens: true})
	if err != nil || !p.Profile().CaptureTokens || m.ContextTokens == 0 {
		t.Fatalf("heimdall provider: %v %+v %+v", err, p.Profile(), m)
	}
}
