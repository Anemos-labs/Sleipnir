package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"example.com/svc/middleware"
	"example.com/svc/ratelimit"
)

var ok = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
})

func status(h http.Handler, remoteAddr string) int {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = remoteAddr
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestGlobalLimit(t *testing.T) {
	h := middleware.Global(ratelimit.New(0.001, 2), ok) // nothing refills during the test
	for i, want := range []int{204, 204, 429, 429} {
		if got := status(h, "10.0.0.1:5000"); got != want {
			t.Errorf("request %d: status %d, want %d", i+1, got, want)
		}
	}
}

func TestPerClientLimit(t *testing.T) {
	h := middleware.PerClient(ratelimit.NewKeyed(0.001, 2), ok)
	// Two requests are allowed per client, whatever the client's port.
	for i, tc := range []struct {
		addr string
		want int
	}{
		{"10.0.0.1:1111", 204},
		{"10.0.0.1:2222", 204},
		{"10.0.0.1:3333", 429},
		{"10.0.0.2:1111", 204},
	} {
		if got := status(h, tc.addr); got != tc.want {
			t.Errorf("request %d from %s: status %d, want %d", i+1, tc.addr, got, tc.want)
		}
	}
}
