//go:build unix

package hooks

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func httpRunner(t *testing.T, url string, mod func(hookSpec)) *Runner {
	t.Helper()
	h := hookSpec{"type": "http", "url": url}
	if mod != nil {
		mod(h)
	}
	r := newRunner(t, settings(t, PreToolUse, group{hooks: []hookSpec{h}}))
	r.AllowHTTP = true
	return r
}

func TestHTTPHookPostsThePayloadAndReadsTheDecision(t *testing.T) {
	var got struct {
		method, contentType, agent, custom string
		body                               map[string]any
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.method, got.contentType, got.agent, got.custom = r.Method, r.Header.Get("Content-Type"), r.Header.Get("User-Agent"), r.Header.Get("X-Team")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got.body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"hookSpecificOutput":{"permissionDecision":"deny","permissionDecisionReason":"remote policy"}}`))
	}))
	defer srv.Close()
	r := httpRunner(t, srv.URL+"/hook", func(h hookSpec) { h["headers"] = map[string]string{"X-Team": "platform"} })
	res := run(t, r, Event{Name: PreToolUse, Tool: "bash", Input: json.RawMessage(`{"command": "ls"}`), Agent: "be-1"})
	if !res.Blocked || res.Decision != Deny || res.Reason != "remote policy" {
		t.Errorf("result = %+v (%s)", res, errorText(res))
	}
	if got.method != "POST" || got.contentType != "application/json" || got.agent != "sleipnir-hooks" || got.custom != "platform" {
		t.Errorf("request: %+v", got)
	}
	if got.body["tool_name"] != "Bash" || got.body["hook_event_name"] != "PreToolUse" || got.body["agent"] != "be-1" {
		t.Errorf("payload = %v", got.body)
	}
}

func TestHTTPHookFailuresDoNotBlock(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		errs    string
	}{
		{"server error", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "boom", 500) }, "HTTP status 500: boom"},
		{"client error", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "nope", http.StatusForbidden) }, "HTTP status 403"},
		{"a 3xx is a failure, not a redirect", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://example.invalid/", http.StatusFound)
		}, "HTTP status 302"},
		{"invalid json", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"broken`)) }, "starts like JSON"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			r := httpRunner(t, srv.URL, nil)
			res := run(t, r, Event{Name: PreToolUse, Tool: "bash"})
			if res.Blocked || !strings.Contains(errorText(res), tc.errs) {
				t.Errorf("blocked=%v errors=%q", res.Blocked, errorText(res))
			}
			r.FailClosed = true
			if res := run(t, r, Event{Name: PreToolUse, Tool: "bash"}); !res.Blocked {
				t.Error("a failing http hook must block when fail-closed")
			}
		})
	}
}

func TestHTTPHookDoesNotFollowRedirects(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { elsewhere.Add(1) }))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	r := httpRunner(t, srv.URL, nil)
	run(t, r, Event{Name: PreToolUse, Tool: "bash", Input: json.RawMessage(`{"command": "echo private"}`)})
	if elsewhere.Load() != 0 {
		t.Fatal("the payload was replayed to another host through a redirect")
	}
}

func TestHTTPHookTimeoutAndResponseCap(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body) // reading the body lets the server notice the client leaving
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	}))
	defer slow.Close()
	r := httpRunner(t, slow.URL, func(h hookSpec) { h["timeout"] = 0.3 })
	start := time.Now()
	res := run(t, r, Event{Name: PreToolUse})
	if elapsed := time.Since(start); elapsed > 5*time.Second || !strings.Contains(errorText(res), "timed out after 300ms") {
		t.Errorf("elapsed %v errors %q", elapsed, errorText(res))
	}

	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"hookSpecificOutput":{"additionalContext":"` + strings.Repeat("a", 2<<20) + `"}}`))
	}))
	defer big.Close()
	r = httpRunner(t, big.URL, nil)
	res = run(t, r, Event{Name: PreToolUse})
	if len(res.AdditionalContext) != 0 || !strings.Contains(errorText(res), "starts like JSON") {
		t.Errorf("an oversized response was accepted: %d bytes of context, errors %q", len(res.AdditionalContext), errorText(res))
	}
}

func TestHTTPHookRefusesPlainHTTPToARemoteHost(t *testing.T) {
	for _, url := range []string{"http://example.com/hook", "http://10.1.2.3/hook", "http://[2001:db8::1]/hook"} {
		r := httpRunner(t, url, nil)
		res := run(t, r, Event{Name: PreToolUse})
		if !strings.Contains(errorText(res), "plain http is only allowed to a loopback address") {
			t.Errorf("%s: errors %q", url, errorText(res))
		}
	}
	// Loopback is fine (and is what the tests use).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	if res := run(t, httpRunner(t, srv.URL, nil), Event{Name: PreToolUse}); len(res.Errors) != 0 {
		t.Errorf("loopback http: %q", errorText(res))
	}
}

func TestHTTPHookMessagesDoNotLeakTheURLsSecrets(t *testing.T) {
	r := httpRunner(t, "https://127.0.0.1:1/hook?token=canary-in-query#frag", nil)
	res := run(t, r, Event{Name: PreToolUse})
	text := errorText(res) + " " + r.Set.Hooks(PreToolUse)[0].String() + " " + res.Runs[0].Hook
	if strings.Contains(text, "canary-in-query") || strings.Contains(text, "frag") || errorText(res) == "" {
		t.Errorf("messages = %q", text)
	}
	if !strings.Contains(text, "https://127.0.0.1:1/hook") {
		t.Errorf("the destination should still be shown: %q", text)
	}
}

func TestHTTPClientPanicIsContained(t *testing.T) {
	r := httpRunner(t, "http://127.0.0.1:1/x", nil)
	r.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { panic("boom") })}
	res, err := r.Run(t.Context(), Event{Name: PreToolUse})
	if err != nil || !strings.Contains(errorText(res), "internal error: boom") {
		t.Errorf("err %v errors %q", err, errorText(res))
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
