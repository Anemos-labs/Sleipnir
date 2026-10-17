package session_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// localServer is a server of this machine that answers chat like the mock under /v1, lists qwen3:8b (ids only) at /v1/models
// unless answers gives the list, and answers each "METHOD /path" of answers with its body; any other path is 404. It counts the
// requests that are not chat.
func localServer(t *testing.T, answers map[string]string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	chat := mock.New(mock.Config{Engine: mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64}},
		func(*mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	var asked atomic.Int32
	mux := http.NewServeMux()
	if _, ok := answers["GET /v1/models"]; !ok {
		answers["GET /v1/models"] = `{"object":"list","data":[{"id":"qwen3:8b","object":"model"}]}`
	}
	for route, body := range answers {
		mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) {
			asked.Add(1)
			io.Copy(io.Discard, r.Body)
			io.WriteString(w, body)
		})
	}
	mux.HandleFunc("/v1/chat/completions", chat.Handler().ServeHTTP)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &asked
}

// localSession starts a session on local/<model> at srv, a keyless provider of this machine with options.
func localSession(t *testing.T, srv *httptest.Server, model string, options map[string]any, offline bool) (*session.Session, session.Options) {
	t.Helper()
	t.Setenv("SLEIPNIR_HOME", "")
	repo := newRepo(t)
	cfg := config.Defaults()
	cfg.Providers = map[string]config.Provider{"local": {Dialect: config.DialectOpenAIChat, BaseURL: srv.URL + "/v1", Options: options}}
	o := session.Options{
		Cwd: repo, Root: repo, Home: t.TempDir(), Dir: t.TempDir(), Config: cfg, Model: "local/" + model,
		Mode: perm.ModeBypass, NoWeb: true, TrustProject: true, Offline: offline,
	}
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, o
}

// A model on a server of this machine takes the window the server serves it with, where the server says: vLLM's catalogue
// (max_model_len), llama.cpp's /props (the slot's n_ctx, not the n_ctx_train its catalogue lists), the instance LM Studio has
// loaded, and Ollama's num_ctx (at most the model's own context_length) or the context_length its running model was loaded with.
// A model's maximum is never taken for what is served, and a server that says nothing leaves the cautious 8192.
func TestAModelOnALocalServerTakesTheWindowTheServerServes(t *testing.T) {
	for _, c := range []struct {
		name    string
		model   string
		answers map[string]string
		window  int
		source  string
	}{
		{"vLLM", "Qwen/Qwen3-8B", map[string]string{
			"GET /v1/models": `{"object":"list","data":[{"id":"Qwen/Qwen3-8B","object":"model","owned_by":"vllm","max_model_len":32768}]}`,
		}, 32768, "catalogue"},
		{"llama.cpp", "qwen3:8b", map[string]string{
			"GET /v1/models": `{"object":"list","data":[{"id":"qwen3:8b","object":"model","meta":{"n_ctx_train":131072}}]}`,
			"GET /props":     `{"default_generation_settings":{"id":0,"id_task":-1,"n_ctx":16384},"total_slots":1}`,
		}, 16384, "server"},
		{"LM Studio, loaded", "qwen3:8b", map[string]string{
			"GET /api/v1/models": `{"models":[{"type":"llm","key":"qwen3:8b","max_context_length":131072,"loaded_instances":[{"id":"qwen3:8b","config":{"context_length":24576}}]}]}`,
		}, 24576, "server"},
		{"LM Studio, not loaded", "qwen3:8b", map[string]string{
			"GET /api/v1/models": `{"models":[{"type":"llm","key":"qwen3:8b","max_context_length":131072,"loaded_instances":[]}]}`,
		}, 8192, "fallback"},
		{"Ollama, num_ctx", "qwen3:8b", map[string]string{
			"POST /api/show": `{"parameters":"num_ctx                        32768\nstop                           \"<|im_end|>\"","model_info":{"general.architecture":"qwen3","qwen3.context_length":40960}}`,
		}, 32768, "server"},
		{"Ollama, num_ctx past the model's own", "qwen3:8b", map[string]string{
			"POST /api/show": `{"parameters":"num_ctx 65536","model_info":{"general.architecture":"qwen3","qwen3.context_length":40960}}`,
		}, 40960, "server"},
		{"Ollama, loaded", "qwen3:8b", map[string]string{
			"POST /api/show": `{"parameters":"stop \"<|im_end|>\"","model_info":{"general.architecture":"qwen3","qwen3.context_length":40960}}`,
			"GET /api/ps":    `{"models":[{"name":"qwen3:8b","model":"qwen3:8b","context_length":4096}]}`,
		}, 4096, "server"},
		{"Ollama, neither", "qwen3:8b", map[string]string{
			"POST /api/show": `{"parameters":"","model_info":{"general.architecture":"qwen3","qwen3.context_length":40960}}`,
			"GET /api/ps":    `{"models":[]}`,
		}, 8192, "fallback"},
		{"a window past any model's", "qwen3:8b", map[string]string{
			"GET /props": `{"default_generation_settings":{"n_ctx":2000000000}}`,
		}, 8192, "fallback"},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv, _ := localServer(t, c.answers)
			s, o := localSession(t, srv, c.model, nil, false)
			if s.Model.ContextTokens != c.window || s.Model.WindowGuessed || s.Model.Price.InputPerM != 0 || s.Model.Price.OutputPerM != 0 {
				t.Fatalf("window %d (guessed %v), $%v/$%v per M; want %d, known, and free", s.Model.ContextTokens, s.Model.WindowGuessed,
					s.Model.Price.InputPerM, s.Model.Price.OutputPerM, c.window)
			}
			if _, err := s.Run(context.Background(), "say when there is nothing to do"); err != nil {
				t.Fatal(err)
			}
			s.Close()
			if r := recordedModels(t, o.Dir)[c.model]; r.Context != c.window || r.ContextSource != c.source || r.Source != "fallback" {
				t.Errorf("session.start records %+v; want window %d from %q, prices from the fallback", r, c.window, c.source)
			}
		})
	}
}

// The provider's options.context_window is the window whatever the server says, and then nobody is asked; an offline session asks
// nobody either. A server is asked about a model once in a session, however many of its agents use it.
func TestALocalServerIsAskedOnlyWhenItsAnswerCounts(t *testing.T) {
	ollama := map[string]string{
		"POST /api/show": `{"parameters":"num_ctx 32768","model_info":{"general.architecture":"qwen3","qwen3.context_length":40960}}`,
	}
	t.Run("configured", func(t *testing.T) {
		srv, asked := localServer(t, ollama)
		s, _ := localSession(t, srv, "qwen3:8b", map[string]any{"context_window": float64(65536)}, false)
		if s.Model.ContextTokens != 65536 {
			t.Fatalf("window %d, want the configured 65536", s.Model.ContextTokens)
		}
		if n := asked.Load(); n != 1 {
			t.Errorf("%d requests besides chat; want only the catalogue's", n)
		}
	})
	t.Run("offline", func(t *testing.T) {
		srv, asked := localServer(t, ollama)
		s, _ := localSession(t, srv, "qwen3:8b", nil, true)
		if s.Model.ContextTokens != 8192 {
			t.Fatalf("window %d, want the cautious 8192", s.Model.ContextTokens)
		}
		if n := asked.Load(); n != 0 {
			t.Errorf("an offline session sent %d requests besides chat", n)
		}
	})
	t.Run("once", func(t *testing.T) {
		srv, asked := localServer(t, ollama)
		t.Setenv("SLEIPNIR_HOME", "")
		repo := newRepo(t)
		cfg := config.Defaults()
		cfg.Providers = map[string]config.Provider{"local": {Dialect: config.DialectOpenAIChat, BaseURL: srv.URL + "/v1"}}
		s, err := session.New(context.Background(), session.Options{
			Cwd: repo, Root: repo, Home: t.TempDir(), Dir: t.TempDir(), Config: cfg, Model: "local/qwen3:8b",
			Mode: perm.ModeBypass, NoWeb: true, TrustProject: true, Swarm: true, Workers: 2,
			RoleModels: map[string]string{"reviewer": "local/qwen3:8b", "scout": "local/qwen3:8b"},
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		if s.Model.ContextTokens != 32768 {
			t.Fatalf("window %d, want Ollama's num_ctx 32768", s.Model.ContextTokens)
		}
		// The catalogue once (it is kept for the session after), /props and /api/v1/models (404: not llama.cpp, not LM Studio) and
		// /api/show once each.
		if n := asked.Load(); n != 4 {
			t.Errorf("%d requests besides chat for three agents on one model; want 4", n)
		}
	})
}

// A catalogue that gives a window and no prices (vLLM's, SGLang's) on an endpoint that takes a key gives the model its window
// only: prices it did not publish are not zero, and the fallback's estimate stands.
func TestACatalogueWithoutPricesGivesTheWindowOnly(t *testing.T) {
	srv, _ := localServer(t, map[string]string{
		"GET /v1/models": `{"object":"list","data":[{"id":"Qwen/Qwen3-8B","object":"model","owned_by":"vllm","max_model_len":32768}]}`,
	})
	t.Setenv("SLEIPNIR_HOME", "")
	t.Setenv("VLLM_TEST_KEY", "k")
	repo := newRepo(t)
	cfg := config.Defaults()
	cfg.Providers = map[string]config.Provider{"vllm": {Dialect: config.DialectOpenAIChat, BaseURL: srv.URL + "/v1", APIKeyEnv: "VLLM_TEST_KEY"}}
	s, err := session.New(context.Background(), session.Options{
		Cwd: repo, Root: repo, Home: t.TempDir(), Dir: t.TempDir(), Config: cfg, Model: "vllm/Qwen/Qwen3-8B",
		Mode: perm.ModeBypass, NoWeb: true, TrustProject: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if s.Model.ContextTokens != 32768 || s.Model.WindowGuessed || s.Model.Price.InputPerM != 3 || s.Model.Price.OutputPerM != 15 {
		t.Errorf("window %d (guessed %v), $%v/$%v per M; want vLLM's 32768 and the fallback's $3/$15", s.Model.ContextTokens,
			s.Model.WindowGuessed, s.Model.Price.InputPerM, s.Model.Price.OutputPerM)
	}
}
