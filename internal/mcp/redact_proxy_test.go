package mcp

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// A templated URL loses the same parts as any other: userinfo, path, query and fragment, while a templated host stays readable.
func TestRedactURLWithTemplates(t *testing.T) {
	for raw, want := range map[string]string{
		"https://${HOST}/v1/sk-live-secret?key=abc":                           "https://${HOST}/...",
		"https://user:pass@${HOST}:8443/x":                                    "https://${HOST}:8443/...",
		"https://${SUB}.example.com/${TOKEN}":                                 "https://${SUB}.example.com/...",
		"https://api.example.com/token-abc":                                   "https://api.example.com/...",
		"https://${H1}${H2}${H3}${H4}${H5}${H6}${H7}${H8}${H9}${H10}${H11}/p": "https://${H1}${H2}${H3}${H4}${H5}${H6}${H7}${H8}${H9}${H10}${H11}/...",
		"not a url ${X} sk-secret":                                            "(invalid url)",
	} {
		if got := redactURL(raw); got != want {
			t.Errorf("redactURL(%q) = %q, want %q", raw, got, want)
		}
		if strings.Contains(redactURL(raw), "secret") {
			t.Errorf("%q kept a secret", raw)
		}
	}
}

// A request the proxy selector sends direct is dialed with the caller's Dial, which owns the address policy.
func TestProxySelectorKeepsTheCallersDialForDirectRequests(t *testing.T) {
	refused := errors.New("refused by policy")
	called := false
	c := newHTTPClient(NetOptions{
		AllowPrivate: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			called = true
			return nil, refused
		},
		Proxy: func(*http.Request) (*url.URL, error) { return nil, nil },
	})
	_, err := c.Get("http://127.0.0.1:9/x")
	if !called || !errors.Is(err, refused) {
		t.Fatalf("called %v, err %v", called, err)
	}
}
