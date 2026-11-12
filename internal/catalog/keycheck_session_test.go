package catalog

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/config"
)

// A key check against an endpoint that routes by session (OpenCode Go answers 400 without x-opencode-session)
// carries a routing key, so a good key is accepted instead of never being checked.
func TestCheckKeySendsARoutingKey(t *testing.T) {
	isolate(t)
	var keys []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s := r.Header.Get("x-opencode-session")
		keys = append(keys, s)
		w.Header().Set("Content-Type", "application/json")
		if s == "" {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"type":"error","error":{"type":"MissingSessionID","message":"Request is missing x-opencode-session"}}`)
			return
		}
		io.WriteString(w, `{"id":"r","model":"m","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"pong"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer ts.Close()
	t.Setenv("SESSGW_API_KEY", "test-key")
	cfg := &config.Config{Providers: map[string]config.Provider{"sessgw": {
		Dialect: config.DialectOpenAIChat, BaseURL: ts.URL, APIKeyEnv: "SESSGW_API_KEY",
		Options: map[string]any{"session_header_name": "x-opencode-session"},
	}}}
	kc := CheckKey(context.Background(), cfg, "sessgw/m")
	if !kc.Checked || kc.Refused {
		t.Fatalf("check = %+v, want checked and accepted", kc)
	}
	if len(keys) != 1 || keys[0] == "" {
		t.Fatalf("x-opencode-session sent: %q", keys)
	}
	if CheckPrompt("m").CacheKey == CheckPrompt("m").CacheKey {
		t.Error("two key checks share a routing key")
	}
}
