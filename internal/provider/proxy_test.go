package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// proxyAnswering is an HTTPS proxy that answers every CONNECT with status.
func proxyAnswering(t *testing.T, status int) *url.URL {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			t.Errorf("the proxy was sent %s, not CONNECT", r.Method)
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// postThrough sends one request to an https endpoint through proxy, the way the adapters' transports do, and classifies the failure.
func postThrough(t *testing.T, proxy *url.URL) *Error {
	t.Helper()
	tr := &http.Transport{Proxy: http.ProxyURL(proxy), OnProxyConnectResponse: CheckProxyConnect}
	defer tr.CloseIdleConnections()
	ctx := context.Background()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.example.test/api/v1/chat/completions", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := (&http.Client{Transport: tr}).Do(req)
	if err == nil {
		res.Body.Close()
		t.Fatal("the request went through a proxy that refuses every CONNECT")
	}
	return TransportError(ctx, err)
}

// A proxy that refuses the host is the proxy's rules saying no: repeating the request cannot change that, so it is not
// retried, and the words name the proxy, the host and the status instead of Go's bare "Forbidden".
func TestAProxyThatRefusesTheHostIsNotRetried(t *testing.T) {
	e := postThrough(t, proxyAnswering(t, http.StatusForbidden))
	if e.Retryable() {
		t.Fatalf("a CONNECT refused with 403 is retried: %v", e)
	}
	if e.Kind != ErrNetwork {
		t.Errorf("kind %v, want network", e.Kind)
	}
	for _, want := range []string{"refused a connection to api.example.test:443", "403 Forbidden", "the proxy does not allow api.example.test "} {
		if !strings.Contains(e.Message, want) {
			t.Errorf("message %q lacks %q", e.Message, want)
		}
	}
}

// A proxy that wants credentials is an auth failure, and not retried either.
func TestAProxyThatWantsCredentialsIsAnAuthFailure(t *testing.T) {
	e := postThrough(t, proxyAnswering(t, http.StatusProxyAuthRequired))
	if e.Retryable() || e.Kind != ErrAuth {
		t.Fatalf("407 from the proxy: kind %v, retryable %v; want auth, not retried", e.Kind, e.Retryable())
	}
}

// A proxy whose upstream did not answer this time (502, 503, 504) is a network blip like any other, and is retried.
func TestAProxyGatewayFailureIsRetried(t *testing.T) {
	for _, status := range []int{http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout} {
		if e := postThrough(t, proxyAnswering(t, status)); !e.Retryable() || e.Kind != ErrNetwork {
			t.Errorf("%d from the proxy: kind %v, retryable %v; want network, retried", status, e.Kind, e.Retryable())
		}
	}
}

// Reach says an endpoint that answers anything, even 404, is reachable, and an endpoint behind a proxy that refuses it is not,
// with the words the first request would have had.
func TestReachTellsAnAnswerFromARefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }))
	defer srv.Close()
	if e := Reach(context.Background(), srv.Client(), srv.URL+"/api/v1"); e != nil {
		t.Fatalf("an endpoint that answered 404 is unreachable: %v", e)
	}
	tr := &http.Transport{Proxy: http.ProxyURL(proxyAnswering(t, http.StatusForbidden)), OnProxyConnectResponse: CheckProxyConnect}
	defer tr.CloseIdleConnections()
	e := Reach(context.Background(), &http.Client{Transport: tr}, "https://api.example.test/api/v1")
	if e == nil || e.Retryable() || !strings.Contains(e.Message, "refused a connection to api.example.test:443") {
		t.Fatalf("an endpoint behind a refusing proxy: %v", e)
	}
	if e := Reach(context.Background(), nil, "mock://local"); e != nil {
		t.Fatalf("a base URL that is not http is checked: %v", e)
	}
}
