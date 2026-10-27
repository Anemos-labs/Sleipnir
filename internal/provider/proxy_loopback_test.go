package provider

import (
	"net/http"
	"net/url"
	"testing"
)

// A loopback host, however it is spelled where IsLoopbackHost accepts it, is never sent to the environment's proxy; other
// hosts still are.
func TestLoopbackHostsBypassTheProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://proxy.example:3128")
	t.Setenv("HTTPS_PROXY", "http://proxy.example:3128")
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")
	for _, raw := range []string{"http://localhost./v1", "http://LOCALHOST:8080/v1", "http://127.0.0.1/v1", "http://[::1]:1/v1"} {
		u, _ := url.Parse(raw)
		p, err := ProxyFromEnvironment(&http.Request{URL: u})
		if err != nil || p != nil {
			t.Errorf("%s goes through %v (%v)", raw, p, err)
		}
	}
}
