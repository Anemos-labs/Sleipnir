package session_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/goal"
	"github.com/anemos-labs/sleipnir/internal/lessons"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// A goal that is met teaches the lesson memory, and the next session's goal on a like task starts with that lesson; a session
// started with --no-memory neither reads nor writes it. The embeddings come from the embedder role's provider, the distillation
// from the session's model.
func TestAMetGoalTeachesTheNextSessionsGoal(t *testing.T) {
	repo := newRepo(t)
	home := t.TempDir()
	t.Setenv("SLEIPNIR_HOME", "")
	var mu sync.Mutex
	embedded := 0
	emb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Input []string }
		if r.URL.Path != "/embeddings" || json.NewDecoder(r.Body).Decode(&req) != nil {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		mu.Lock()
		embedded += len(req.Input)
		mu.Unlock()
		var out struct {
			Data []map[string]any `json:"data"`
		}
		for i := range req.Input {
			out.Data = append(out.Data, map[string]any{"index": i, "embedding": []float32{1, 0, 0}})
		}
		json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(emb.Close)
	var firsts []string // the first message of each goal
	srv := mock.New(mock.Config{Engine: mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64}}, func(c *mock.Call) mock.Reply {
		switch {
		case strings.Contains(c.System, "You extract reusable lessons"):
			return mock.Reply{Text: `{"lessons":[{"kind":"strategy","text":"Run the build before the tests to fail fast."}]}`}
		case strings.Contains(c.System, "You judge whether a coding agent"):
			return mock.Reply{Text: `{"verdict":"done","reason":"the build passed","left":[]}`}
		}
		return mock.Reply{Text: "it builds"}
	})
	ts := srv.Start()
	t.Cleanup(ts.Close)
	cfg := config.Defaults()
	cfg.Providers = map[string]config.Provider{"gw": {BaseURL: ts.URL}, "vec": {BaseURL: emb.URL}}
	cfg.Models.Roles = map[string]string{session.EmbedderRole: "vec/acme/embed"}
	start := func(noMemory bool) (*session.Session, string) {
		t.Helper()
		s, err := session.New(context.Background(), session.Options{
			Cwd: repo, Root: repo, Home: home, Dir: t.TempDir(), Config: cfg, Offline: true,
			Model: "gw/acme/main", Mode: perm.ModeBypass, NoWeb: true, TrustProject: true, NoMemory: noMemory,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		g := goal.New("make sure the project builds")
		s.SaveGoal(g)
		first := s.GoalStart(context.Background(), g)
		firsts = append(firsts, first)
		if _, err := s.Run(context.Background(), first); err != nil {
			t.Fatal(err)
		}
		note, _ := s.GoalTurn(context.Background(), g, nil)
		return s, note
	}
	s, note := start(false)
	if !s.LessonsOn() || note != "goal met: the build passed" || strings.Contains(firsts[0], lessons.RenderHeader) {
		t.Fatalf("the first session: lessons on %v, note %q, first message %q", s.LessonsOn(), note, firsts[0])
	}
	path := filepath.Join(home, ".sleipnir", "lessons.json")
	if b, err := os.ReadFile(path); err != nil || !strings.Contains(string(b), "fail fast") {
		t.Fatalf("the store after a met goal: %s %v", b, err)
	}
	start(false)
	if !strings.Contains(firsts[1], lessons.RenderHeader) || !strings.Contains(firsts[1], "- [strategy] Run the build before the tests to fail fast.") {
		t.Errorf("the next session's goal starts without the lesson: %q", firsts[1])
	}
	mu.Lock()
	before := embedded
	mu.Unlock()
	off, _ := start(true)
	mu.Lock()
	after := embedded
	mu.Unlock()
	if off.LessonsOn() || strings.Contains(firsts[2], lessons.RenderHeader) || after != before {
		t.Errorf("a session without memory: lessons on %v, %d embeddings, first message %q", off.LessonsOn(), after-before, firsts[2])
	}
}
