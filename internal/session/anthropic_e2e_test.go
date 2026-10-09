package session_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/swarm"
)

// startAnthropic serves the explicit-cache Anthropic-dialect mock and returns a
// session option set that talks to it through the real adapter, built the way
// the CLI builds it (config, dialect, BuildProvider).
func startAnthropic(t *testing.T, r mock.Responder) (*mock.AnthropicServer, session.Options) {
	t.Helper()
	agent.RetryBase = time.Millisecond
	srv := mock.NewAnthropic(mock.AnthropicConfig{}, r)
	ts := srv.Start()
	t.Cleanup(ts.Close)
	t.Setenv("SLEIPNIR_TEST_CLAUDE_KEY", "test-key-not-a-secret")
	cfg := &config.Config{Providers: map[string]config.Provider{
		"claude": {Dialect: config.DialectAnthropic, BaseURL: ts.URL, APIKeyEnv: "SLEIPNIR_TEST_CLAUDE_KEY"},
	}}
	p, m, err := session.BuildProvider(cfg, session.ModelRef{Provider: "claude", Model: "claude-opus-5-5"}, session.ProviderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return srv, session.Options{
		Home: t.TempDir(), Dir: t.TempDir(), Provider: p, ModelInfo: &m, Model: m.ID,
		Mode: perm.ModeBypass, NoWeb: true, TrustProject: true, Offline: true,
	}
}

func TestSingleAgentOverTheMessagesAPI(t *testing.T) {
	repo := newRepo(t)
	srv, o := startAnthropic(t, func(c *mock.Call) mock.Reply {
		switch assistantTurns(c) {
		case 0:
			return mock.Reply{Text: "reading", ToolCalls: []mock.ToolCall{call("c1", "read", map[string]any{"path": "main.go"})}}
		case 1:
			return mock.Reply{Text: "writing", ToolCalls: []mock.ToolCall{call("c2", "write", map[string]any{"path": "hello.txt", "content": "hi\n"})}}
		case 2:
			return mock.Reply{Text: "checking", ToolCalls: []mock.ToolCall{call("c3", "bash", map[string]any{"command": "cat hello.txt"})}}
		case 3:
			return mock.Reply{Text: "listing", ToolCalls: []mock.ToolCall{call("c4", "bash", map[string]any{"command": "ls"})}}
		}
		return mock.Reply{Text: "done: created hello.txt"}
	})
	o.Cwd, o.Root = repo, repo
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.Run(context.Background(), "create hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "created hello.txt") {
		t.Fatalf("result %q", res.Text)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(repo, "hello.txt")); err != nil || string(b) != "hi\n" {
		t.Fatalf("the write tool must have worked: %v %q", err, b)
	}

	stats := srv.AnthropicStats()
	if len(stats) != 5 {
		t.Fatalf("requests: %d", len(stats))
	}
	for i, st := range stats {
		if st.Err != "" {
			t.Fatalf("request %d failed: %s", i+1, st.Err)
		}
		if st.Markers == 0 {
			t.Errorf("request %d carried no cache_control marker: the explicit-cache route needs breakpoints", i+1)
		}
	}
	// The first request writes the prefix; every later one reads it (tools and system at least)
	// and, from the third on, the thread too.
	if stats[0].Write5m+stats[0].Write1h == 0 {
		t.Errorf("the first request should write the prefix: %+v", stats[0])
	}
	for i := 1; i < len(stats); i++ {
		st := stats[i]
		if st.ReadTiers.Tools+st.ReadTiers.System == 0 {
			t.Errorf("request %d did not read the tools/system prefix: %+v", i+1, st)
		}
		if i >= 2 && st.ReadTiers.Messages == 0 {
			t.Errorf("request %d did not read the earlier thread: %+v", i+1, st.ReadTiers)
		}
		if ratio := float64(st.Read) / float64(st.Read+st.Write5m+st.Write1h+st.Uncached); i >= 2 && ratio < 0.8 {
			t.Errorf("request %d hit ratio %.2f", i+1, ratio)
		}
	}
	anomalies := 0
	for _, e := range readEvents(t, res.Dir) {
		if e.Type == events.TypeCacheAnomaly {
			anomalies++
		}
	}
	if anomalies != 0 {
		t.Errorf("%d cache anomalies on a plain single-agent run", anomalies)
	}
}

func TestSwarmOverTheMessagesAPISharesItsPrefix(t *testing.T) {
	const workers = 6
	repo := newRepo(t)
	whoRe := regexp.MustCompile(`you: (\S+) \((\w+)\)`)
	taskRe := regexp.MustCompile(`task (T\d+)`)
	var mu sync.Mutex
	srv, o := startAnthropic(t, func(c *mock.Call) mock.Reply {
		mu.Lock()
		defer mu.Unlock()
		id, role, tid := "", "", ""
		for i := len(c.Messages) - 1; i >= 0; i-- {
			if c.Messages[i].Role == "assistant" { // the hot view arrives as a (turn-scoped) system message here
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
				var calls []mock.ToolCall
				var ids []string
				for i := 1; i <= workers; i++ {
					calls = append(calls, call(fmt.Sprintf("t%d", i), "task", map[string]any{"action": "create", "title": fmt.Sprintf("Survey area %d", i), "role": "scout"}))
					ids = append(ids, fmt.Sprintf("T%d", i))
				}
				for i := 1; i <= workers; i++ {
					calls = append(calls, call(fmt.Sprintf("s%d", i), "spawn", map[string]any{"role": "scout", "task": fmt.Sprintf("T%d", i)}))
				}
				calls = append(calls, call("w", "wait", map[string]any{"until": ids, "timeout_sec": 60}))
				return mock.Reply{Text: "dispatching", ToolCalls: calls}
			case 1:
				var calls []mock.ToolCall
				for i := 1; i <= workers; i++ {
					calls = append(calls, call(fmt.Sprintf("a%d", i), "task", map[string]any{"action": "accept", "id": fmt.Sprintf("T%d", i)}))
				}
				return mock.Reply{Text: "accepting", ToolCalls: calls}
			}
			return mock.Reply{Text: "all areas surveyed"}
		}
		switch n {
		case 0:
			return mock.Reply{Text: "looking", ToolCalls: []mock.ToolCall{call("r"+id, "read", map[string]any{"path": "main.go"})}}
		case 1:
			return mock.Reply{Text: "reporting", ToolCalls: []mock.ToolCall{call("d"+id, "task", map[string]any{"action": "done", "id": tid, "text": "surveyed by " + id})}}
		}
		return mock.Reply{Text: "done " + id}
	})
	o.Cwd, o.Root = repo, repo
	o.Swarm, o.Workers = true, workers+1
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	res, err := s.Run(ctx, "survey the repository in parts")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "all areas surveyed") {
		t.Fatalf("manager result %q", res.Text)
	}
	done := 0
	for _, tk := range s.Swarm.Board.Snapshot().Tasks {
		if tk.Status == swarm.StatusDone {
			done++
		}
	}
	if done != workers {
		t.Fatalf("%d of %d tasks accepted", done, workers)
	}

	stats := srv.AnthropicStats()
	var errs, readers int
	var total, read int
	for _, st := range stats {
		if st.Err != "" {
			errs++
		}
		total += st.Read + st.Write5m + st.Write1h + st.Uncached
		read += st.Read
	}
	if errs != 0 {
		t.Fatalf("%d of %d requests failed", errs, len(stats))
	}
	// Workers' first requests should read what the manager (the primer) wrote: at least tools and system.
	firsts := 0
	for _, e := range readEvents(t, s.Dir) {
		if e.Type != events.TypeModelResponse || e.Agent == "mgr" {
			continue
		}
		var m struct {
			Req   string `json:"req"`
			Usage struct {
				CacheRead int `json:"cache_read_tokens"`
			} `json:"usage"`
		}
		_ = json.Unmarshal(e.Data, &m)
		if strings.HasSuffix(m.Req, ".1") {
			firsts++
			if m.Usage.CacheRead > 0 {
				readers++
			}
		}
	}
	if firsts != workers {
		t.Fatalf("saw %d worker first requests, want %d", firsts, workers)
	}
	if readers < workers-1 {
		t.Errorf("only %d of %d workers' first requests read the shared prefix on the explicit-cache route", readers, workers)
	}
	t.Logf("%d requests; overall hit ratio %.2f; %d/%d worker first requests read the shared prefix", len(stats), float64(read)/float64(total), readers, firsts)
}
