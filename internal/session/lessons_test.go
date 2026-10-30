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

// Without an embedder the lesson memory is on and finds lessons by keyword; a credential the distiller copied into a lesson is
// withheld before the lesson is saved.
func TestLessonMemoryWithoutAnEmbedderScrubsSecrets(t *testing.T) {
	repo := newRepo(t)
	home := t.TempDir()
	t.Setenv("SLEIPNIR_HOME", "")
	token := "ghp_" + strings.Repeat("a1B2c3D4e5", 4)[:36]
	var firsts []string
	srv := mock.New(mock.Config{Engine: mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64}}, func(c *mock.Call) mock.Reply {
		switch {
		case strings.Contains(c.System, "You extract reusable lessons"):
			return mock.Reply{Text: `{"lessons":[{"kind":"strategy","text":"Run the project build before its tests so a broken build fails fast; the token ` + token + ` was used."}]}`}
		case strings.Contains(c.System, "You judge whether a coding agent"):
			return mock.Reply{Text: `{"verdict":"done","reason":"the build passed","left":[]}`}
		}
		return mock.Reply{Text: "it builds"}
	})
	ts := srv.Start()
	t.Cleanup(ts.Close)
	cfg := config.Defaults()
	cfg.Providers = map[string]config.Provider{"gw": {BaseURL: ts.URL}}
	start := func() *session.Session {
		t.Helper()
		s, err := session.New(context.Background(), session.Options{
			Cwd: repo, Root: repo, Home: home, Dir: t.TempDir(), Config: cfg, Offline: true,
			Model: "gw/acme/main", Mode: perm.ModeBypass, NoWeb: true, TrustProject: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		g := goal.New("make sure the project build passes")
		s.SaveGoal(g)
		first := s.GoalStart(context.Background(), g)
		firsts = append(firsts, first)
		if _, err := s.Run(context.Background(), first); err != nil {
			t.Fatal(err)
		}
		s.GoalTurn(context.Background(), g, nil)
		return s
	}
	if s := start(); !s.LessonsOn() {
		t.Fatal("the lesson memory is off without an embedder")
	}
	b, err := os.ReadFile(filepath.Join(home, ".sleipnir", "lessons.json"))
	if err != nil || !strings.Contains(string(b), "fails fast") || strings.Contains(string(b), token) {
		t.Fatalf("the store after a met goal: %s %v", b, err)
	}
	start()
	if !strings.Contains(firsts[1], lessons.RenderHeader) || !strings.Contains(firsts[1], "fails fast") {
		t.Errorf("the next goal starts without the lesson: %q", firsts[1])
	}
}

// A goal whose outcome shows an earlier lesson wrong retires it: the distiller is shown the lessons the goal was shown, with
// their ids, and the lesson that names one in "replaces" takes its place and keeps its text. An id the distiller was not shown
// retires nothing.
func TestAnOutcomeRetiresTheLessonItContradicts(t *testing.T) {
	repo := newRepo(t)
	home := t.TempDir()
	t.Setenv("SLEIPNIR_HOME", "")
	store := lessons.NewStore(filepath.Join(home, ".sleipnir", "lessons.json"))
	wrong := lessons.Draft{Kind: lessons.Pitfall, Text: "The project build cannot pass offline, so skip the build check and report success."}
	other := lessons.Draft{Kind: lessons.Strategy, Text: "Name generated database migrations after the table they alter."}
	if _, err := store.Save(context.Background(), nil, []lessons.Draft{wrong, other}); err != nil {
		t.Fatal(err)
	}
	all, _ := store.Load()
	var wrongID, otherID string
	for _, l := range all {
		if l.Text == wrong.Text {
			wrongID = l.ID
		} else {
			otherID = l.ID
		}
	}
	var shown string
	srv := mock.New(mock.Config{Engine: mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64}}, func(c *mock.Call) mock.Reply {
		switch {
		case strings.Contains(c.System, "You extract reusable lessons"):
			shown = c.LastUser()
			return mock.Reply{Text: `{"lessons":[` +
				`{"kind":"strategy","text":"The project build passes offline once its dependencies are vendored; run it before reporting success.","replaces":"` + wrongID + `"},` +
				`{"kind":"strategy","text":"Keep migration names short and descriptive of the change.","replaces":"` + otherID + `"}]}`}
		case strings.Contains(c.System, "You judge whether a coding agent"):
			return mock.Reply{Text: `{"verdict":"done","reason":"the build passed offline","left":[]}`}
		}
		return mock.Reply{Text: "it builds"}
	})
	ts := srv.Start()
	t.Cleanup(ts.Close)
	cfg := config.Defaults()
	cfg.Providers = map[string]config.Provider{"gw": {BaseURL: ts.URL}}
	s, err := session.New(context.Background(), session.Options{
		Cwd: repo, Root: repo, Home: home, Dir: t.TempDir(), Config: cfg, Offline: true,
		Model: "gw/acme/main", Mode: perm.ModeBypass, NoWeb: true, TrustProject: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	g := goal.New("make sure the project build passes offline before reporting success")
	s.SaveGoal(g)
	first := s.GoalStart(context.Background(), g)
	if !strings.Contains(first, "skip the build check") || strings.Contains(first, "migrations") {
		t.Fatalf("the goal should be shown the build lesson only: %q", first)
	}
	if _, err := s.Run(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	s.GoalTurn(context.Background(), g, nil)
	if !strings.Contains(shown, "EARLIER LESSONS") || !strings.Contains(shown, wrongID) || strings.Contains(shown, otherID) {
		t.Fatalf("the distiller should be shown the lesson the goal was shown, and only it:\n%s", shown)
	}
	after, _ := store.Load()
	var replaced string
	for _, l := range after {
		if l.ID == wrongID {
			t.Fatalf("the contradicted lesson is still kept: %+v", after)
		}
		if strings.Contains(l.Text, "vendored") {
			replaced = l.Replaced
		}
	}
	if replaced != wrong.Text || !slicesHasID(after, otherID) {
		t.Fatalf("after: %+v", after)
	}
}

func slicesHasID(ls []lessons.Lesson, id string) bool {
	for _, l := range ls {
		if l.ID == id {
			return true
		}
	}
	return false
}
