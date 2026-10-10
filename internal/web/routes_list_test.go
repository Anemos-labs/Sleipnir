package web

import (
	"net/http"
	"slices"
	"testing"
)

// Routes lists the built-in patterns and every pattern added with Handle, sorted, and does not alias the server's own list.
func TestRoutesListsWhatWasRegistered(t *testing.T) {
	rg := newRig(t, nil)
	got := rg.srv.Routes()
	if !slices.IsSorted(got) {
		t.Errorf("Routes is not sorted: %v", got)
	}
	for _, want := range []string{"GET /healthz", "GET /api/ping", "POST /api/confirm", "POST /api/auth/logout", "POST /api/auth/rotate", "GET /api/", "GET /",
		"GET /api/echo/{id}", "POST /api/echo/{id}"} {
		if !slices.Contains(got, want) {
			t.Errorf("Routes lacks %q: %v", want, got)
		}
	}
	rg.srv.HandleFunc("GET /api/late", func(w http.ResponseWriter, r *http.Request) {}, RouteOpts{})
	if after := rg.srv.Routes(); len(after) != len(got)+1 || !slices.Contains(after, "GET /api/late") {
		t.Errorf("a route added later is not listed: %v", after)
	}
	got[0] = "changed"
	if rg.srv.Routes()[0] == "changed" {
		t.Error("Routes returns the server's own slice")
	}
}
