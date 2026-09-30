package session_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/reee344/sleipnir/internal/config"
	"github.com/reee344/sleipnir/internal/perm"
	"github.com/reee344/sleipnir/internal/provider/mock"
	"github.com/reee344/sleipnir/internal/session"
)

// startRecord is the "models" table of a run's session.start event.
type startRecord struct {
	Source           string  `json:"source"`
	Context          int     `json:"context"`
	InputPerM        float64 `json:"input_per_m"`
	OutputPerM       float64 `json:"output_per_m"`
	CacheReadPerM    float64 `json:"cache_read_per_m"`
	CacheWrite5mPerM float64 `json:"cache_write_5m_per_m"`
}

func recordedModels(t *testing.T, dir string) map[string]startRecord {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var e struct {
			Type string `json:"type"`
			Data struct {
				Models map[string]startRecord `json:"models"`
			} `json:"data"`
		}
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.Type == "session.start" {
			return e.Data.Models
		}
	}
	t.Fatal("no session.start in the log")
	return nil
}

// mainCatalogue publishes the session's own model: $2 in, $8 out and $0.5 cached, a 64k window.
const mainCatalogue = `{"data":[{"id":"acme/main","context_length":65536,"architecture":{"modality":"text->text"},
	"pricing":{"prompt":"0.000002","completion":"0.000008","input_cache_read":"0.0000005"},
	"top_provider":{"context_length":65536,"max_completion_tokens":8192},"supported_parameters":["tools"]}]}`

// smallCatalogue publishes one model priced $1 in, $4 out and $0.25 cached per million tokens
// with a 32k window: not a model the built-in table knows.
const smallCatalogue = `{"data":[{"id":"acme/small","context_length":32768,"architecture":{"modality":"text->text"},
	"pricing":{"prompt":"0.000001","completion":"0.000004","input_cache_read":"0.00000025"},
	"top_provider":{"context_length":32768,"max_completion_tokens":4096},"supported_parameters":["tools"]}]}`

// session.start says what each model the run may call was priced with, and where that came
// from: the endpoint's own catalogue for a marketplace model (the run is billed by it), the
// caller's word for a model it described itself. The inspector prices the log with these numbers.
func TestSessionStartRecordsThePricesEveryModelWasDescribedWith(t *testing.T) {
	repo := newRepo(t)
	// The main model's endpoint answers chat like the mock and publishes a catalogue of its own.
	chat := mock.New(mock.Config{Engine: mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64}},
		func(*mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	mux := http.NewServeMux()
	mux.HandleFunc("/models", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, mainCatalogue) })
	mux.Handle("/", chat.Handler())
	main := httptest.NewServer(mux)
	t.Cleanup(main.Close)
	small, _ := catalogueServer(t, smallCatalogue)

	cfg := config.Defaults()
	cfg.Providers = map[string]config.Provider{"gw": {BaseURL: main.URL}, "other": {BaseURL: small.URL}}
	o := session.Options{
		Cwd: repo, Root: repo, Home: t.TempDir(), Dir: t.TempDir(), Config: cfg,
		Model: "gw/acme/main", Mode: perm.ModeBypass, NoWeb: true, TrustProject: true,
		Swarm: true, MaxAgents: 3, RoleModels: map[string]string{"reviewer": "other/acme/small"},
	}
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Run(context.Background(), "say when there is nothing to do"); err != nil {
		t.Fatal(err)
	}
	s.Close()

	got := recordedModels(t, o.Dir)
	if r := got["acme/main"]; r.Source != "catalogue" || r.InputPerM != 2 || r.OutputPerM != 8 || r.CacheReadPerM != 0.5 || r.Context != 65536 {
		t.Errorf("main model: %+v", r)
	}
	// A role's model is refined from its own endpoint the same way: it used to keep the generic
	// 200k window and $3/$15 whatever the marketplace said.
	if r := got["acme/small"]; r.Source != "catalogue" || r.InputPerM != 1 || r.OutputPerM != 4 || r.CacheReadPerM != 0.25 || r.Context != 32768 {
		t.Errorf("role model: %+v", r)
	}
	if len(got) != 2 {
		t.Errorf("recorded %d models, want 2: %+v", len(got), got)
	}
}

// An offline session asks nobody: a model the table does not know keeps the fallback, and the
// record says so, so a reader of the log knows its dollar figures are estimates.
func TestOfflineSessionRecordsTheFallbackAsAFallback(t *testing.T) {
	repo := newRepo(t)
	client, model := startMock(t, func(*mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	model.ID = "acme/unheard-of"
	o := opts(t, repo, client, model)
	o.Model = "acme/unheard-of"
	o.ModelInfo = nil // the session describes it itself: not in the table, so the fallback
	o.Offline = true
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Run(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	r := recordedModels(t, o.Dir)["acme/unheard-of"]
	if r.Source != "fallback" || r.InputPerM != 3 || r.OutputPerM != 15 {
		t.Errorf("record: %+v", r)
	}

	// A model the caller described is recorded as given.
	client2, given := startMock(t, func(*mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	o2 := opts(t, repo, client2, given)
	s2, err := session.New(context.Background(), o2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Run(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	s2.Close()
	if r := recordedModels(t, o2.Dir)["mock-1"]; r.Source != "given" || r.InputPerM != 4 || r.Context != 1_000_000 {
		t.Errorf("given model: %+v", r)
	}
}
