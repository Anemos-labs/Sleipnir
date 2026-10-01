package openaichat

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/provider"
)

func doWith(t *testing.T, cfg Config) (*provider.Response, error) {
	t.Helper()
	return New(cfg).Do(context.Background(), &provider.Request{Prompt: secRevPrompt("hello"), NoStream: true}, nil)
}

// S45, transport half: an API key is only ever sent over https, or over http to a
// loopback address.
func TestAPIKeyIsNeverSentOverPlainHTTPToAnotherHost(t *testing.T) {
	for _, url := range []string{
		"http://collector.attacker.example/v1",
		"http://api.openai.com/v1",
		"http://10.1.2.3:8000/v1",
		"http://192.168.0.7/v1",
		"http://localhost.attacker.example/v1",
		"http://127.0.0.1.attacker.example/v1",
		"http://user:pw@collector.attacker.example/v1",
	} {
		t.Run(url, func(t *testing.T) {
			_, err := doWith(t, Config{Name: "x", BaseURL: url, APIKey: "sk-secret-canary", HTTPClient: refuseAll(t)})
			pe := wantProviderError(t, err, provider.ErrBadRequest, false)
			if !pe.NoRetry {
				t.Error("a refusal must not be retried")
			}
			if !strings.Contains(pe.Message, "plain http") || !strings.Contains(pe.Message, "allow_insecure_http") {
				t.Errorf("the refusal must say what to do: %q", pe.Message)
			}
			if strings.Contains(pe.Message, "sk-secret-canary") || strings.Contains(pe.Message, "pw") {
				t.Errorf("the refusal leaks a credential: %q", pe.Message)
			}
		})
	}
}

func TestAPIKeyMayGoToLoopbackAndToHTTPS(t *testing.T) {
	// Loopback over http: a real local server.
	var gotAuth string
	s := newScripted(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, okBody)
	})
	if _, err := doWith(t, Config{Name: "x", BaseURL: s.URL, APIKey: "sk-local"}); err != nil || gotAuth != "Bearer sk-local" {
		t.Fatalf("loopback http must work with a key: %v (%q)", err, gotAuth)
	}
	// https anywhere (the transport is a stub, so no network is used).
	stub := &stubTransport{status: 200, body: okBody}
	for _, url := range []string{"https://api.openai.com/v1", "https://gateway.example:8443/api/v1", "http://localhost:8000/v1", "http://[::1]:9/v1", "http://127.9.9.9/v1"} {
		stub.seen = nil
		if _, err := doWith(t, Config{Name: "x", BaseURL: url, APIKey: "sk-k", HTTPClient: &http.Client{Transport: stub}}); err != nil {
			t.Errorf("%s: %v", url, err)
			continue
		}
		if len(stub.seen) != 1 || stub.seen[0].Header.Get("Authorization") != "Bearer sk-k" {
			t.Errorf("%s: request not sent with the key: %+v", url, stub.seen)
		}
	}
}

func TestPlainHTTPWithoutAKeyIsAllowed(t *testing.T) {
	stub := &stubTransport{status: 200, body: okBody}
	resp, err := doWith(t, Config{Name: "x", BaseURL: "http://gpu-box.lan:8000/v1", HTTPClient: &http.Client{Transport: stub}})
	if err != nil || resp.Turn.PlainText() != "ok" || len(stub.seen) != 1 {
		t.Fatalf("a keyless local server over http is fine: %v", err)
	}
	if stub.seen[0].Header.Get("Authorization") != "" {
		t.Fatal("an Authorization header was invented")
	}
}

func TestAllowInsecureHTTPIsADeliberateException(t *testing.T) {
	stub := &stubTransport{status: 200, body: okBody}
	resp, err := doWith(t, Config{
		Name: "lan", BaseURL: "http://gpu-box.lan:8000/v1", APIKey: "sk-lan", AllowInsecureHTTP: true,
		HTTPClient: &http.Client{Transport: stub},
	})
	if err != nil || resp.Turn.PlainText() != "ok" {
		t.Fatalf("AllowInsecureHTTP: %v", err)
	}
	if len(stub.seen) != 1 || stub.seen[0].Header.Get("Authorization") != "Bearer sk-lan" {
		t.Fatalf("seen %+v", stub.seen)
	}
}

func TestAnUnusableBaseURLWithAKeyIsRefused(t *testing.T) {
	for _, url := range []string{"", "not a url", "ftp://example.com/v1", "//example.com/v1", "example.com/v1", "https://"} {
		_, err := doWith(t, Config{Name: "x", BaseURL: url, APIKey: "sk-k", HTTPClient: refuseAll(t)})
		pe := wantProviderError(t, err, provider.ErrBadRequest, false)
		if pe.Message == "" {
			t.Errorf("%q: empty message", url)
		}
	}
}

func TestClientStringRedactsTheBaseURLAndNeverShowsTheKey(t *testing.T) {
	c := New(Config{Name: "gw", BaseURL: "https://user:hunter2@gateway.example/v1?token=abc", APIKey: "sk-secret-canary"})
	s := c.String()
	for _, leak := range []string{"hunter2", "user", "token=abc", "sk-secret-canary"} {
		if strings.Contains(s, leak) {
			t.Errorf("String() leaks %q: %s", leak, s)
		}
	}
	if s != "openaichat(gw https://gateway.example/v1)" {
		t.Errorf("String() = %q", s)
	}
}
