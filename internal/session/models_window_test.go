package session_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/chatgptauth"
	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// gatewayCatalogue lists two models the built-in table knows: gpt-6-luna with a 400k window (and the gateway's own prices, which
// the table's stay in front of), and gpt-6.1-sol with prices and no window.
const gatewayCatalogue = `{"data":[
	{"id":"openai/gpt-6-luna","context_length":400000,"architecture":{"modality":"text->text"},"pricing":{"prompt":"0.000001","completion":"0.000002"}},
	{"id":"openai/gpt-6.1-sol","architecture":{"modality":"text->text"},"pricing":{"prompt":"0.000002","completion":"0.00001"}}]}`

// pinnedCatalogue reports windows for a model the table knows and for one it does not; the provider that serves it sets
// options.context_window, which is the window of both whatever the catalogue says.
const pinnedCatalogue = `{"data":[
	{"id":"openai/gpt-5.6-luna","context_length":400000,"architecture":{"modality":"text->text"},"pricing":{"prompt":"0.000001","completion":"0.000002"}},
	{"id":"acme/small","context_length":32768,"architecture":{"modality":"text->text"},"pricing":{"prompt":"0.000001","completion":"0.000004"}}]}`

// chatWithCatalogue is an endpoint that answers chat like the mock and publishes catalogue at /models ("" answers 404 there), and
// counts the requests for the catalogue.
func chatWithCatalogue(t *testing.T, catalogue string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	chat := mock.New(mock.Config{Engine: mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64}},
		func(*mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	var hits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/models", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if catalogue == "" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, catalogue)
	})
	mux.Handle("/", chat.Handler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &hits
}

// A model the built-in table knows keeps the table's prices and takes the context window its provider reports: the window drives
// compaction, and the table's is a cautious guess. A catalogue that reports no window for it, or none at all, leaves the table's; a
// provider's options.context_window is the window whatever its catalogue says; a catalogue that cannot be read is asked once per
// session, however many models it serves; and session.start says where each window came from.
func TestEachModelTakesTheContextWindowItsProviderReports(t *testing.T) {
	t.Setenv("SLEIPNIR_HOME", "")
	repo := newRepo(t)
	gw, gwHits := chatWithCatalogue(t, gatewayCatalogue)
	bare, bareHits := chatWithCatalogue(t, "")
	pinned, _ := chatWithCatalogue(t, pinnedCatalogue)
	cfg := config.Defaults()
	cfg.Providers = map[string]config.Provider{
		"gw":     {BaseURL: gw.URL},
		"bare":   {BaseURL: bare.URL},
		"pinned": {BaseURL: pinned.URL, Options: map[string]any{"context_window": 150000}},
	}
	o := session.Options{
		Cwd: repo, Root: repo, Home: t.TempDir(), Dir: t.TempDir(), Config: cfg,
		Model: "gw/openai/gpt-6-luna", Mode: perm.ModeBypass, NoWeb: true, TrustProject: true,
		Swarm: true, Workers: 2, RoleModels: map[string]string{
			"reviewer":  "gw/openai/gpt-6.1-sol",
			"scout":     "bare/openai/gpt-6-astra",
			"architect": "bare/openai/gpt-6-sol",
			"tester":    "pinned/openai/gpt-5.6-luna",
			"docs":      "pinned/acme/small",
		},
	}
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if s.Model.ContextTokens != 400000 || s.Model.Price.InputPerM != 0.10 {
		t.Errorf("the main model: window %d, input $%v/M; want the catalogue's 400000 and the table's $0.10", s.Model.ContextTokens, s.Model.Price.InputPerM)
	}
	if _, err := s.Run(context.Background(), "say when there is nothing to do"); err != nil {
		t.Fatal(err)
	}
	s.Close()

	got := recordedModels(t, o.Dir)
	want := map[string]startRecord{
		"openai/gpt-6-luna":   {Source: "table", Context: 400000, ContextSource: "catalogue", InputPerM: 0.10, OutputPerM: 0.50},
		"openai/gpt-6.1-sol":  {Source: "table", Context: 200000, ContextSource: "table", InputPerM: 2, OutputPerM: 10},
		"openai/gpt-6-astra":  {Source: "table", Context: 200000, ContextSource: "table", InputPerM: 10, OutputPerM: 50},
		"openai/gpt-6-sol":    {Source: "table", Context: 200000, ContextSource: "table", InputPerM: 2, OutputPerM: 10},
		"openai/gpt-5.6-luna": {Source: "table", Context: 150000, ContextSource: "configured", InputPerM: 0.20, OutputPerM: 1.20},
		"acme/small":          {Source: "catalogue", Context: 150000, ContextSource: "configured", InputPerM: 1, OutputPerM: 4},
	}
	for id, w := range want {
		r, ok := got[id]
		if !ok || r.Source != w.Source || r.Context != w.Context || r.ContextSource != w.ContextSource || r.InputPerM != w.InputPerM || r.OutputPerM != w.OutputPerM {
			t.Errorf("%s: recorded %+v, want %+v", id, r, w)
		}
	}
	if n := bareHits.Load(); n != 1 {
		t.Errorf("the catalogue that is not there was asked %d times for two models; want once", n)
	}
	if n := gwHits.Load(); n != 1 {
		t.Errorf("the gateway's catalogue was asked %d times; want once, then read from the copy kept", n)
	}

	// Offline, nobody is asked: the table's window stands, and the record says so.
	gwHits.Store(0)
	o.Home, o.Dir, o.Offline = t.TempDir(), t.TempDir(), true
	s, err = session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if s.Model.ContextTokens != 200000 {
		t.Errorf("offline, the main model's window is %d; want the table's 200000", s.Model.ContextTokens)
	}
	if _, err := s.Run(context.Background(), "say when there is nothing to do"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if r := recordedModels(t, o.Dir)["openai/gpt-6-luna"]; r.Context != 200000 || r.ContextSource != "table" {
		t.Errorf("offline record: %+v", r)
	}
	if n := gwHits.Load(); n != 0 {
		t.Errorf("an offline session asked the catalogue %d times", n)
	}
}

// planServer is a ChatGPT plan's endpoint: the plan's model list at /v1/models (gpt-6-luna with a 272k window), which it answers only
// with the sign-in's token, and a Responses route that answers every request with "done". It counts the requests for the list.
func planServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var listed atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		listed.Add(1)
		if r.Header.Get("Authorization") != "Bearer at-live" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		io.WriteString(w, `{"models":[{"slug":"gpt-6-luna","display_name":"GPT-6 Luna","visibility":"list","context_window":272000},{"slug":"gpt-6-sol","visibility":"list"}]}`)
	})
	mux.HandleFunc("/v1/responses", func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: response.completed\ndata: "+`{"type":"response.completed","response":{"id":"r1","model":"gpt-6-luna","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":100,"output_tokens":2}}}`+"\n\n")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &listed
}

// loopbackOnly lets requests reach this machine only, and counts the ones that would have gone elsewhere (OpenAI's real issuer, for
// a renewal of the sign-in), so a test can tell that none was tried.
func loopbackOnly(t *testing.T) *atomic.Int32 {
	t.Helper()
	var foreign atomic.Int32
	old := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if ip := net.ParseIP(r.URL.Hostname()); ip == nil || !ip.IsLoopback() {
			foreign.Add(1)
			return nil, fmt.Errorf("the test reaches no other machine (%s)", r.URL.Host)
		}
		return old.RoundTrip(r)
	})
	t.Cleanup(func() { http.DefaultTransport = old })
	return &foreign
}

// planSession starts a session on chatgpt/gpt-6-luna at the plan endpoint srv, with home as its state directory.
func planSession(t *testing.T, srv *httptest.Server, home string, offline bool) (*session.Session, session.Options) {
	t.Helper()
	repo := newRepo(t)
	o := session.Options{
		Home: home, Dir: t.TempDir(), Config: planConfig(srv.URL + "/v1"), Model: "chatgpt/gpt-6-luna", Cwd: repo, Root: repo,
		Mode: perm.ModeBypass, NoWeb: true, TrustProject: true, Offline: offline,
	}
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	return s, o
}

// A ChatGPT plan's model takes the context window of the plan's own model list (gpt-6-luna: 272k there, 200k in the table). The list is
// kept under the state directory, without the token, and the next session reads it from there; an offline session asks nobody.
func TestAPlanModelTakesTheWindowOfThePlansList(t *testing.T) {
	t.Setenv("SLEIPNIR_HOME", "")
	signedIn(t)
	foreign := loopbackOnly(t)
	srv, listed := planServer(t)
	home := t.TempDir()

	s, o := planSession(t, srv, home, false)
	if !s.Model.Plan || s.Model.ContextTokens != 272000 {
		t.Fatalf("the plan's model: plan %v, window %d; want the list's 272000", s.Model.Plan, s.Model.ContextTokens)
	}
	if _, err := s.Run(context.Background(), "say done"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if r := recordedModels(t, o.Dir)["gpt-6-luna"]; !r.Plan || r.Source != "table" || r.Context != 272000 || r.ContextSource != "catalogue" || r.InputPerM != 0.10 {
		t.Errorf("session.start records %+v", r)
	}
	if n := listed.Load(); n != 1 {
		t.Errorf("the list was asked %d times", n)
	}
	kept, _ := filepath.Glob(filepath.Join(home, ".sleipnir", "cache", "chatgpt-models-*.json"))
	if len(kept) != 1 {
		t.Fatalf("kept lists: %v", kept)
	}
	b, _ := os.ReadFile(kept[0])
	if !strings.Contains(string(b), `"context_window":272000`) || strings.Contains(string(b), "at-live") || strings.Contains(string(b), "rt-1") {
		t.Errorf("the kept list: %s", b)
	}

	s, _ = planSession(t, srv, home, false)
	s.Close()
	if s.Model.ContextTokens != 272000 || listed.Load() != 1 {
		t.Errorf("the second session: window %d after %d requests for the list; want 272000 from the copy kept", s.Model.ContextTokens, listed.Load())
	}

	s, _ = planSession(t, srv, t.TempDir(), true)
	s.Close()
	if s.Model.ContextTokens != 200000 || listed.Load() != 1 {
		t.Errorf("offline: window %d after %d requests for the list; want the table's 200000 and no request", s.Model.ContextTokens, listed.Load())
	}
	if n := foreign.Load(); n != 0 {
		t.Errorf("%d requests left this machine", n)
	}
}

// A session does not renew the sign-in to ask for the list: a renewal cut off by the start's deadline could lose the rotated refresh
// token, and the session's first request renews it anyway. With a token that is due, the list is not asked: an older copy of it is
// used when there is one, else the table's window.
func TestAPlanListIsNotAskedWithATokenThatIsDue(t *testing.T) {
	t.Setenv("SLEIPNIR_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	conn := map[string]any{"version": 1, "host_id": "urn:uuid:x", "client_id": "client_1", "subject": "u", "email": "me@example.com",
		"access_token": "at-live", "expires_at_ms": time.Now().Add(time.Minute).UnixMilli(), "refresh_token": "rt-1", "id_token": "x.y.z"}
	b, _ := json.Marshal(conn)
	if err := os.MkdirAll(filepath.Join(home, ".sleipnir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(chatgptauth.Path(home), b, 0o600); err != nil {
		t.Fatal(err)
	}
	foreign := loopbackOnly(t)
	srv, listed := planServer(t)

	state := t.TempDir()
	s, _ := planSession(t, srv, state, false)
	s.Close()
	if s.Model.ContextTokens != 200000 || listed.Load() != 0 || foreign.Load() != 0 {
		t.Errorf("window %d, %d requests for the list, %d for a renewal; want the table's 200000 and none", s.Model.ContextTokens, listed.Load(), foreign.Load())
	}

	// A copy kept seven hours ago is past its time, and still better than the table while the list cannot be asked.
	st, err := chatgptauth.Open(chatgptauth.Options{Path: chatgptauth.Path(home), Models: srv.URL + "/v1/models"})
	if err != nil {
		t.Fatal(err)
	}
	path := st.ListPath(filepath.Join(state, ".sleipnir", "cache"))
	if err := chatgptauth.SaveList(path, []chatgptauth.Model{{Slug: "gpt-6-luna", ContextWindow: 272000}}); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-7 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	s, _ = planSession(t, srv, state, false)
	s.Close()
	if s.Model.ContextTokens != 272000 || listed.Load() != 0 || foreign.Load() != 0 {
		t.Errorf("window %d, %d requests for the list, %d for a renewal; want the older copy's 272000 and no request", s.Model.ContextTokens, listed.Load(), foreign.Load())
	}
}
