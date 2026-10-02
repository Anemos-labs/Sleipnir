package session_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/checkpoint"
	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/provider/openaichat"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/swarm"
	"github.com/anemos-labs/sleipnir/internal/tools"
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

func TestUserInstructionsAndTheirImportsSurviveAnUntrustedProject(t *testing.T) {
	repo := newRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("Project rule: PROJECT-MARKER.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	client, model := startMock(t, func(c *mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	o := opts(t, repo, client, model)
	o.TrustProject = false
	udir := filepath.Join(o.Home, ".sleipnir")
	if err := os.MkdirAll(udir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(udir, "SLEIPNIR.md"), []byte("Personal rule: USER-MARKER.\n@prefs.md\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(udir, "prefs.md"), []byte("Imported preference: IMPORT-MARKER.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	text := s.Shared.Text()
	for marker, want := range map[string]bool{"USER-MARKER": true, "IMPORT-MARKER": true, "PROJECT-MARKER": false} {
		if got := strings.Contains(text, marker); got != want {
			t.Errorf("%s in the shared layer = %v, want %v (untrusted project)", marker, got, want)
		}
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

func TestBuiltInHostsAndLocalServers(t *testing.T) {
	for _, k := range []string{"HEIMDALL_API_KEY", "OPENROUTER_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "TOGETHER_API_KEY"} {
		t.Setenv(k, "")
	}
	// Nothing set: the error says what to start with, and it is Heimdall.
	if _, err := session.ResolveModel(nil, "some/model"); err == nil || !strings.Contains(err.Error(), "sleipnir login` (Heimdall is the recommended provider)") {
		t.Errorf("the no-provider error recommends Heimdall first: %v", err)
	}
	if _, err := session.ResolveModel(nil, ""); err == nil || !strings.Contains(err.Error(), "Recommended start: run `sleipnir login`") {
		t.Errorf("the no-model error recommends Heimdall first: %v", err)
	}
	// A local server needs no key and is never the default for a bare id.
	got, err := session.ResolveModel(nil, "ollama/qwen3:8b")
	if err != nil || got.Provider != "ollama" || got.Model != "qwen3:8b" {
		t.Fatalf("ollama: %+v %v", got, err)
	}
	if _, _, err := session.BuildProvider(nil, got, session.ProviderOptions{}); err != nil {
		t.Fatalf("a local server must build without a key: %v", err)
	}
	if _, _, err := session.BuildProvider(nil, session.ModelRef{Provider: "together", Model: "m"}, session.ProviderOptions{}); err == nil ||
		!strings.Contains(err.Error(), "TOGETHER_API_KEY") {
		t.Fatalf("a hosted provider names its key variable: %v", err)
	}
	// "anthropic/claude-x" is a marketplace id when only the marketplace has a key.
	t.Setenv("OPENROUTER_API_KEY", "k")
	got, err = session.ResolveModel(nil, "anthropic/claude-x")
	if err != nil || got.Provider != "openrouter" || got.Model != "anthropic/claude-x" {
		t.Fatalf("marketplace id that starts with a built-in provider name: %+v %v", got, err)
	}
	t.Setenv("ANTHROPIC_API_KEY", "k")
	got, _ = session.ResolveModel(nil, "anthropic/claude-x")
	if got.Provider != "anthropic" || got.Model != "claude-x" {
		t.Fatalf("with its key set the provider prefix is the provider: %+v", got)
	}
}

func TestEnrichModelUsesTheEndpointsCatalogueAndCachesIt(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"acme/coder-32k","context_length":32768,"architecture":{"modality":"text->text"},
			"pricing":{"prompt":"0.000001","completion":"0.000004","input_cache_read":"0.00000025"},
			"top_provider":{"context_length":32768,"max_completion_tokens":4096},"supported_parameters":["tools"]}]}`)
	}))
	defer srv.Close()
	cache := t.TempDir()
	unknown := cost.Fallback("acme/coder-32k")
	got := session.EnrichModel(context.Background(), cache, srv.URL, unknown)
	if got.ContextTokens != 32768 {
		t.Fatalf("the catalogue's context window must replace the 200k fallback, got %d", got.ContextTokens)
	}
	if got.Price.InputPerM != 1 || got.Price.CacheReadPerM != 0.25 {
		t.Fatalf("prices from the catalogue: %+v", got.Price)
	}
	before := hits
	if again := session.EnrichModel(context.Background(), cache, srv.URL, unknown); again.ContextTokens != 32768 || hits != before {
		t.Fatalf("second lookup must come from the disk cache (hits %d -> %d)", before, hits)
	}
	// Known vendor models keep the built-in table; unreachable catalogues fall back silently.
	known, _ := cost.Defaults().Lookup("claude-opus-5-5")
	if session.EnrichModel(context.Background(), cache, srv.URL, known).ContextTokens != known.ContextTokens {
		t.Fatal("a model the table knows must not be overwritten")
	}
	if session.EnrichModel(context.Background(), t.TempDir(), "http://127.0.0.1:1", unknown).ContextTokens != unknown.ContextTokens {
		t.Fatal("an unreachable catalogue must leave the fallback in place")
	}
}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

func TestReconIgnoresTestdataAndListsOnlyRealBinaries(t *testing.T) {
	repo := newRepo(t)
	for name, body := range map[string]string{
		"cmd/app/main.go":                       "package main\n\nfunc main() {}\n",
		"cmd/app/testdata/deep/fixture.go":      "package deep\n",
		"pkg/testdata/data.go":                  "package testdata\n",
		"cmd/app/internal/helper/helper_one.go": "package helper\n",
	} {
		p := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r, err := session.BuildRecon(context.Background(), session.ReconOptions{Root: repo, BudgetTokens: 3000, NoGit: true})
	if err != nil {
		t.Fatal(err)
	}
	all := ""
	for _, s := range r.Segments {
		all += s.Text
	}
	if strings.Contains(all, "testdata") {
		t.Errorf("testdata is fixture noise, not project structure:\n%s", all)
	}
	if !strings.Contains(all, "binaries: cmd/app") || strings.Contains(all, "cmd/app/") && strings.Contains(all, "binaries: cmd/app cmd/app/") {
		t.Errorf("only cmd/<name> directories are binaries:\n%s", all)
	}
}

// TestReadOnlyRoleIsEnforcedByTheEngine: a reviewer cannot write files or run
// mutating commands (including ones hidden behind a permitted prefix), but can
// still run the project's checks.
func TestReadOnlyRoleIsEnforcedByTheEngine(t *testing.T) {
	repo := newRepo(t)
	whoRe := regexp.MustCompile(`you: (\S+) \((\w+)\)`)
	taskRe := regexp.MustCompile(`task (T\d+)`)
	var mu sync.Mutex
	client, model := startMock(t, func(c *mock.Call) mock.Reply {
		mu.Lock()
		defer mu.Unlock()
		role, tid := "", ""
		for i := len(c.Messages) - 1; i >= 0; i-- {
			if c.Messages[i].Role != "user" {
				continue
			}
			if m := whoRe.FindStringSubmatch(c.Messages[i].Content); m != nil && role == "" {
				role = m[2]
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
					call("m1", "task", map[string]any{"action": "create", "title": "Review the repo", "role": "reviewer"}),
					call("m2", "spawn", map[string]any{"role": "reviewer", "task": "T1"}),
					call("m3", "wait", map[string]any{"until": []string{"T1"}, "timeout_sec": 20}),
				}}
			case 1:
				return mock.Reply{Text: "accepting", ToolCalls: []mock.ToolCall{call("m4", "task", map[string]any{"action": "accept", "id": "T1"})}}
			}
			return mock.Reply{Text: "reviewed"}
		}
		switch n {
		case 0:
			return mock.Reply{Text: "trying", ToolCalls: []mock.ToolCall{
				call("r1", "write", map[string]any{"path": "pwned.txt", "content": "x"}),
				call("r2", "bash", map[string]any{"command": "touch pwned2.txt"}),
				call("r3", "bash", map[string]any{"command": "go vet ./server && touch pwned3.txt"}),
				call("r4", "bash", map[string]any{"command": "go vet ./server"}),
			}}
		case 1:
			return mock.Reply{Text: "done", ToolCalls: []mock.ToolCall{call("r5", "task", map[string]any{"action": "done", "id": tid, "text": "reviewed"})}}
		}
		return mock.Reply{Text: "summary"}
	})
	o := opts(t, repo, client, model)
	o.Swarm = true
	o.Mode = perm.ModeAcceptEdits // the session may edit; the reviewer role may not
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Run(context.Background(), "review the repository"); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"pwned.txt", "pwned2.txt", "pwned3.txt"} {
		if _, err := os.Stat(filepath.Join(repo, f)); err == nil {
			t.Errorf("read-only role created %s", f)
		}
	}
	// Results the reviewer saw, from the recorded transcript.
	var denied, vetRan int
	for _, e := range readEvents(t, s.Dir) {
		if e.Type != events.TypeToolResult || e.Agent != "rv-1" {
			continue
		}
		var r struct {
			ID    string `json:"id"`
			Error bool   `json:"error"`
		}
		json.Unmarshal(e.Data, &r)
		switch r.ID {
		case "r1", "r2", "r3":
			if r.Error {
				denied++
			}
		case "r4":
			if !r.Error {
				vetRan++
			}
		}
	}
	if denied != 3 || vetRan != 1 {
		t.Fatalf("expected 3 denied writes/mutations and 1 permitted vet, got denied=%d vet=%d", denied, vetRan)
	}
}

// TestPermissionModesThroughTheAssembledSession runs the same four actions under
// each mode: a file write in the project, a shell command that touches a file in
// the project, a network command, and a read of a credential file.
func TestPermissionModesThroughTheAssembledSession(t *testing.T) {
	type outcome struct{ write, touch, curl, secret bool } // true = allowed to run
	cases := []struct {
		name    string
		mode    perm.Mode
		answer  bool // what the prompter says when asked
		want    outcome
		prompts int
	}{
		{"default asks and the user allows", perm.ModeDefault, true, outcome{true, true, true, false}, 3},
		{"default asks and the user denies", perm.ModeDefault, false, outcome{false, false, false, false}, 3},
		{"accept-edits takes edits in the project without asking, asks for the network", perm.ModeAcceptEdits, false, outcome{true, true, false, false}, 1},
		{"plan is read-only", perm.ModePlan, true, outcome{false, false, false, false}, 0},
		{"bypass allows everything except hard denies", perm.ModeBypass, false, outcome{true, true, true, false}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newRepo(t)
			home := t.TempDir()
			if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(home, ".ssh", "id_rsa"), []byte("not a real key"), 0o600); err != nil {
				t.Fatal(err)
			}
			client, model := startMock(t, func(c *mock.Call) mock.Reply {
				if assistantTurns(c) == 0 {
					return mock.Reply{Text: "acting", ToolCalls: []mock.ToolCall{
						call("w", "write", map[string]any{"path": "out.txt", "content": "hi\n"}),
						call("t", "bash", map[string]any{"command": "touch touched.txt"}),
						call("c", "bash", map[string]any{"command": "curl -s --max-time 1 http://127.0.0.1:9/x"}),
						call("s", "read", map[string]any{"path": filepath.Join(home, ".ssh", "id_rsa")}),
					}}
				}
				return mock.Reply{Text: "done"}
			})
			var mu sync.Mutex
			prompts := 0
			o := opts(t, repo, client, model)
			o.Home = home
			o.Mode = tc.mode
			o.Prompter = func(ctx context.Context, r perm.Request) perm.Decision {
				mu.Lock()
				prompts++
				mu.Unlock()
				return perm.Decision{Allow: tc.answer, Reason: "scripted"}
			}
			s, err := session.New(context.Background(), o)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if _, err := s.Run(context.Background(), "do the things"); err != nil {
				t.Fatal(err)
			}
			// A tool "ran" unless the transcript shows the engine refusing it.
			denied := map[string]bool{}
			results := map[string]string{} // what each tool said, for the failure message
			for _, e := range readEvents(t, s.Dir) {
				if e.Type != events.TypeTurnAppend {
					continue
				}
				var turn core.Turn
				json.Unmarshal(e.Data, &turn)
				for _, b := range turn.Blocks {
					if b.Kind == core.BlockToolResult {
						results[b.ToolID] = b.PlainText()
						if strings.Contains(b.PlainText(), "permission denied") {
							denied[b.ToolID] = true
						}
					}
				}
			}
			got := outcome{write: !denied["w"], touch: !denied["t"], curl: !denied["c"], secret: !denied["s"]}
			if got != tc.want {
				t.Errorf("outcome = %+v, want %+v\nwhat each tool said: %q\nprompts: %d", got, tc.want, results, prompts)
			}
			if _, err := os.Stat(filepath.Join(repo, "out.txt")); (err == nil) != tc.want.write {
				t.Errorf("out.txt exists=%v, want %v", err == nil, tc.want.write)
			}
			mu.Lock()
			defer mu.Unlock()
			if prompts != tc.prompts {
				t.Errorf("prompter asked %d times, want %d", prompts, tc.prompts)
			}
		})
	}
}

// TestRewindRestoresWhatTheAgentChanged: every write passes the checkpoint store
// first, so a rewind puts modified files back and removes files the agent created.
func TestRewindRestoresWhatTheAgentChanged(t *testing.T) {
	repo := newRepo(t)
	orig, err := os.ReadFile(filepath.Join(repo, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	client, model := startMock(t, func(c *mock.Call) mock.Reply {
		if assistantTurns(c) == 0 {
			return mock.Reply{Text: "editing", ToolCalls: []mock.ToolCall{
				call("r", "read", map[string]any{"path": "main.go"}),
				call("e", "write", map[string]any{"path": "main.go", "content": "package main\n\n// changed by the agent\nfunc main() {}\n"}),
				call("n", "write", map[string]any{"path": "created.txt", "content": "new\n"}),
			}}
		}
		return mock.Reply{Text: "done"}
	})
	s, err := session.New(context.Background(), opts(t, repo, client, model))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Run(context.Background(), "change main.go and add created.txt"); err != nil {
		t.Fatal(err)
	}
	changed, _ := os.ReadFile(filepath.Join(repo, "main.go"))
	if string(changed) == string(orig) {
		t.Fatal("the agent's write did not land")
	}
	list := s.Ckpt.List()
	if len(list) != 1 || len(list[0].Files) < 2 {
		t.Fatalf("expected one checkpoint covering both files, got %+v", list)
	}
	rep, err := s.Ckpt.Restore(list[0].ID, checkpoint.RestoreOpts{})
	if err != nil || !rep.OK() {
		t.Fatalf("restore: %v %s", err, rep.Summary())
	}
	back, _ := os.ReadFile(filepath.Join(repo, "main.go"))
	if string(back) != string(orig) {
		t.Fatalf("main.go not restored:\n%s", back)
	}
	if _, err := os.Stat(filepath.Join(repo, "created.txt")); err == nil {
		t.Fatal("a file the agent created must be removed by the rewind")
	}
}

// A person who wrote instructions for their agent must be told when they are not
// used: because the project is not trusted, or because they do not fit.
func TestInstructionFilesThatAreSkippedOrCutAreReported(t *testing.T) {
	repo := newRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("Run `make test` before finishing.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	client, model := startMock(t, func(c *mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })

	o := opts(t, repo, client, model)
	o.TrustProject = false
	sink := &noticeSink{}
	o.Sink = sink
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if got := sink.all(); !strings.Contains(got, "not loaded because the project is not trusted") || !strings.Contains(got, "AGENTS.md") || !strings.Contains(got, "--trust-project") {
		t.Errorf("no notice about the skipped instruction file: %q", got)
	}

	// Trusted: nothing to report while they fit.
	o = opts(t, repo, client, model)
	sink = &noticeSink{}
	o.Sink = sink
	s, err = session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if got := sink.all(); strings.Contains(got, "instruction files") {
		t.Errorf("a notice for instructions that were loaded whole: %q", got)
	}

	// Trusted but too long: the cut is announced, and the note in the prompt says what happened.
	var long strings.Builder
	for i := 0; i < 3000; i++ {
		fmt.Fprintf(&long, "Rule %d: keep functions short and name things after what they do.\n", i)
	}
	if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte(long.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	o = opts(t, repo, client, model)
	sink = &noticeSink{}
	o.Sink = sink
	s, err = session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := sink.all(); !strings.Contains(got, "instruction files come to") || !strings.Contains(got, "3000") {
		t.Errorf("no notice about the cut: %q", got)
	}
	if txt := s.Shared.Text(); !strings.Contains(txt, "instruction files truncated") || strings.Contains(txt, "survey truncated") {
		t.Errorf("the shared layer does not say its instructions were cut (or says it about a survey)")
	}
}

// A turn that is only tool calls, opened by a model with a newline or two, must not leave blank lines
// on the answer stream (thirty of them preceded the final answer of a small swarm). Blank lines that
// begin a message with words in it are dropped too, its indentation and everything after it are not.
func TestTextSinkDropsBlankLinesThatOnlyOpenAMessage(t *testing.T) {
	var out, log strings.Builder
	sink := session.NewTextSink(&out, &log, "main", false)
	resp := func() { sink.Response("main", nil, 0) }

	sink.Text("main", "\n")
	sink.Text("main", "  \n\n")
	sink.Text("worker", "\n\nnot the answer\n")
	resp() // a message that said nothing but white space
	if out.String() != "" {
		t.Fatalf("white space alone reached the answer: %q", out.String())
	}

	sink.Text("main", "\n")
	sink.Text("main", "\n  indented")
	sink.Text("main", " and more\n\nafter a blank line\n")
	resp()
	if got, want := out.String(), "  indented and more\n\nafter a blank line\n"; got != want {
		t.Fatalf("answer = %q, want %q", got, want)
	}

	// A held-back message does not leak into the next one, and each message starts afresh.
	sink.Text("main", "\n\n")
	resp()
	sink.Text("main", "\nsecond\n")
	resp()
	if got, want := out.String(), "  indented and more\n\nafter a blank line\nsecond\n"; got != want {
		t.Fatalf("answer = %q, want %q", got, want)
	}
}

// Text that reaches the terminal is data: an escape sequence in a model's answer
// (say, an OSC 52 clipboard write a web page talked it into) must not arrive as one.
func TestTextSinkStripsTerminalEscapes(t *testing.T) {
	var out, log strings.Builder
	sink := session.NewTextSink(&out, &log, "main", true)
	sink.Text("main", "hello \x1b]52;c;ZXZpbA==\x07world\x1b[2J\x1b[31m red\x1b[0m\r\nline two\n")
	sink.Notice("main", "warn", "tool said \x1b]0;pwned\x07 done")
	call := core.ToolUse("c1", "bash", json.RawMessage(`{"command":"echo \u001b[41m hi"}`))
	sink.ToolStart("main", call)
	sink.ToolEnd("main", call, &tools.Result{Text: "boom \x1b[?1049h", IsError: true}, time.Millisecond)
	for name, got := range map[string]string{"out": out.String(), "log": log.String()} {
		if strings.ContainsAny(got, "\x1b\x07\r") {
			t.Errorf("%s contains a control character: %q", name, got)
		}
	}
	if !strings.Contains(out.String(), "hello ") || !strings.Contains(out.String(), "world") || !strings.Contains(out.String(), "line two") {
		t.Errorf("ordinary text must survive: %q", out.String())
	}
}

// TestScratchFilesGoToThePrivateTmpdir: a command's TMPDIR is a directory of the
// session, inside the workspace for the permission engine, so `mktemp` and `go build -o
// $TMPDIR/x` need no question in accept-edits mode (a quarter of the refusals of the
// first benchmark and most of those of the second were scratch files in /tmp).
func TestScratchFilesGoToThePrivateTmpdir(t *testing.T) {
	repo := newRepo(t)
	client, model := startMock(t, func(c *mock.Call) mock.Reply {
		if assistantTurns(c) == 0 {
			return mock.Reply{ToolCalls: []mock.ToolCall{
				call("m", "bash", map[string]any{"command": `echo scratch > "$TMPDIR/x.txt" && cat "$TMPDIR/x.txt"`}),
			}}
		}
		return mock.Reply{Text: "done"}
	})
	o := opts(t, repo, client, model)
	o.Home = t.TempDir()
	o.Mode = perm.ModeAcceptEdits
	o.Prompter = func(ctx context.Context, r perm.Request) perm.Decision {
		t.Errorf("asked: %+v", r)
		return perm.Decision{}
	}
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(s.Dir, "tmp", "x.txt"))
	if err != nil || string(b) != "scratch\n" {
		t.Fatalf("scratch file: %q, %v", b, err)
	}
}

// /model moves the single agent to another model and keeps the conversation: the new endpoint's first request holds the
// first goal, the old endpoint hears nothing more, and a swarm is refused.
func TestSwitchModelKeepsTheThreadOnTheNewEndpoint(t *testing.T) {
	var mu sync.Mutex
	var seenA, seenB []string
	last := func(c *mock.Call) string { return c.LastUser() }
	clientA, modelA := startMock(t, func(c *mock.Call) mock.Reply {
		mu.Lock()
		seenA = append(seenA, last(c))
		mu.Unlock()
		return mock.Reply{Text: "answer from A"}
	})
	var bHadFirstGoal bool
	srvB := mock.New(mock.Config{}, func(c *mock.Call) mock.Reply {
		mu.Lock()
		seenB = append(seenB, last(c))
		for _, m := range c.Messages {
			if strings.Contains(fmt.Sprint(m), "first goal") {
				bHadFirstGoal = true
			}
		}
		mu.Unlock()
		return mock.Reply{Text: "answer from B"}
	})
	tsB := srvB.Start()
	t.Cleanup(tsB.Close)

	repo := newRepo(t)
	o := opts(t, repo, clientA, modelA)
	cfg := config.Defaults()
	cfg.Providers = map[string]config.Provider{"other": {Dialect: config.DialectOpenAIChat, BaseURL: tsB.URL + "/v1"}}
	o.Config = cfg
	o.Offline = true
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Run(context.Background(), "first goal"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TOGETHER_API_KEY", "")
	if _, err := s.SwitchModel(context.Background(), "together/x"); err == nil || s.Model.ID != "mock-1" {
		t.Fatalf("a failed switch says why and leaves the model alone: %v (model %s)", err, s.Model.ID)
	}
	ref, err := s.SwitchModel(context.Background(), "other/m2")
	if err != nil || ref != "other/m2" || s.Model.ID != "m2" {
		t.Fatalf("switch: %q %v (model %s)", ref, err, s.Model.ID)
	}
	res, err := s.Run(context.Background(), "second goal")
	if err != nil || !strings.Contains(res.Text, "answer from B") {
		t.Fatalf("the next turn runs on the new model: %+v %v", res, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seenA) != 1 || len(seenB) != 1 || !bHadFirstGoal {
		t.Errorf("A saw %v, B saw %v, B had the first goal: %v", seenA, seenB, bHadFirstGoal)
	}
	var switched bool
	for _, e := range readEvents(t, s.Dir) {
		if e.Type == events.TypeModelSwitch {
			switched = true
		}
	}
	if !switched {
		t.Error("the switch is recorded in the log")
	}
}

// A note the model saved in one session is in the shared layer of the next, in a project that has nothing of its own, and the line
// that says the notes may be wrong survives the loader (which removes HTML comments).
func TestSavedMemoryNotesReachTheNextSessionsSharedLayer(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".sleipnir"), 0o700); err != nil {
		t.Fatal(err)
	}
	notes := "Notes the agent saved with its memory tool. They may be wrong or out of date; the user's own instructions win.\n- Prefers table-driven tests.\n"
	if err := os.WriteFile(filepath.Join(home, ".sleipnir", "MEMORY.md"), []byte(notes), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := newRepo(t)
	client, model := startMock(t, func(c *mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	o := opts(t, repo, client, model)
	o.Home = home
	o.TrustProject = false // only the user's own files count in an untrusted project: the memory file is the user's
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got := s.Shared.Text()
	if !strings.Contains(got, "Prefers table-driven tests.") || !strings.Contains(got, "may be wrong or out of date") {
		t.Errorf("the shared layer lacks the saved notes:\n%s", got)
	}
}

// The plan a model sets reaches its next request in the hot tail, and a plan with open steps sends a finished answer back once.
func TestThePlanToolsListIsShownBackAndHoldsTheRunOpen(t *testing.T) {
	repo := newRepo(t)
	var sawPlan, sawNudge bool
	client, model := startMock(t, func(c *mock.Call) mock.Reply {
		last := c.LastUser()
		sawPlan = sawPlan || strings.Contains(fmt.Sprint(c.Messages), "Your plan (the plan tool changes it)")
		sawNudge = sawNudge || strings.Contains(last, "Your plan still has")
		switch assistantTurns(c) {
		case 0:
			return mock.Reply{Text: "planning", ToolCalls: []mock.ToolCall{call("p1", "plan", map[string]any{"items": []map[string]string{
				{"step": "read the code", "status": "done"}, {"step": "run the tests", "status": "pending"}}})}}
		case 1:
			return mock.Reply{Text: "that is all"} // a step is open: sent back
		}
		return mock.Reply{Text: "finished after the note"}
	})
	s, err := session.New(context.Background(), opts(t, repo, client, model))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	res, err := s.Run(context.Background(), "do three things")
	if err != nil || !strings.Contains(res.Text, "finished after the note") {
		t.Fatalf("%+v %v", res, err)
	}
	if !sawPlan || !sawNudge {
		t.Errorf("the plan was shown back: %v; the open step was noted: %v", sawPlan, sawNudge)
	}
}
