package catalog

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/chatgptauth"
	"github.com/anemos-labs/sleipnir/internal/config"
)

// The ChatGPT plan's model list is kept where sessions read the windows of the plan's models: a listing that asks the plan (`sleipnir
// models`, the browser's Models page) keeps the answer, and an offline listing shows the copy kept, whatever its age, instead of nothing.
func TestThePlansListIsKeptForSessionsAndShownOffline(t *testing.T) {
	home := isolate(t)
	var listed atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		listed.Add(1)
		if r.Header.Get("Authorization") != "Bearer at-live" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		io.WriteString(w, `{"models":[{"slug":"gpt-6-luna","display_name":"GPT-6 Luna","visibility":"list","context_window":272000}]}`)
	}))
	defer srv.Close()
	localSources = func(context.Context, *config.Config) []Source { return nil }
	usableSources = func(*config.Config) []Source { return []Source{{Name: "chatgpt", Plan: true}} }
	planListURL = srv.URL + "/models"
	t.Cleanup(func() {
		localSources = LocalSources
		usableSources = func(cfg *config.Config) []Source { return UsableSources(cfg, true) }
		planListURL = ""
	})
	conn := map[string]any{"version": 1, "host_id": "urn:uuid:x", "client_id": "client_1", "subject": "u", "email": "me@example.com",
		"access_token": "at-live", "expires_at_ms": time.Now().Add(time.Hour).UnixMilli(), "refresh_token": "rt-1"}
	b, _ := json.Marshal(conn)
	if err := os.MkdirAll(filepath.Dir(chatgptauth.Path(home)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(chatgptauth.Path(home), b, 0o600); err != nil {
		t.Fatal(err)
	}
	luna := func(rows []Row) (Row, bool) {
		for _, r := range rows {
			if r.Ref == "chatgpt/gpt-6-luna" {
				return r, true
			}
		}
		return Row{}, false
	}

	cfg := &config.Config{}
	res := FetchResult(context.Background(), Options{Home: home, Config: cfg})
	if r, ok := luna(res.Rows); !ok || r.Context != 272000 || !r.Plan || listed.Load() != 1 {
		t.Fatalf("asked: %+v %v (%d requests, errors %v)", r, ok, listed.Load(), res.Errors)
	}
	st, err := chatgptauth.Open(chatgptauth.Options{Path: chatgptauth.Path(home), Models: planListURL})
	if err != nil {
		t.Fatal(err)
	}
	kept, _, err := chatgptauth.LoadList(st.ListPath(filepath.Join(home, ".sleipnir", "cache")))
	if err != nil || len(kept) != 1 || kept[0].ContextWindow != 272000 {
		t.Fatalf("the list kept for sessions: %+v %v", kept, err)
	}

	srv.Close()
	later := time.Now().Add(48 * time.Hour)
	off := FetchResult(context.Background(), Options{Home: home, Config: cfg, Offline: true, Now: func() time.Time { return later }})
	if r, ok := luna(off.Rows); !ok || r.Context != 272000 || len(off.Errors) != 0 || listed.Load() != 1 {
		t.Errorf("offline: %+v %v (%d requests, errors %v)", r, ok, listed.Load(), off.Errors)
	}
	if off.AsOf.After(time.Now()) {
		t.Errorf("an offline listing is as of when the list was kept, not %v", off.AsOf)
	}
}
