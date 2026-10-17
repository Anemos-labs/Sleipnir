package providertest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// PostThroughRefusingProxy sends one request to an https endpoint through tr, with its proxy replaced by one that answers every
// CONNECT with status, and returns the transport's error (nil when the request went through, which a test should fail on).
// The rest of tr is the adapter's own, its OnProxyConnectResponse hook included.
func PostThroughRefusingProxy(t *testing.T, tr *http.Transport, status int) error {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
	t.Cleanup(srv.Close)
	proxy, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	tr.Proxy = http.ProxyURL(proxy)
	defer tr.CloseIdleConnections()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://api.example.test/v1/chat/completions", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := (&http.Client{Transport: tr}).Do(req)
	if err == nil {
		res.Body.Close()
	}
	return err
}
