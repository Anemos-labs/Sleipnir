package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"example.com/svc/middleware"
	"example.com/svc/ratelimit"
)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

func newClock() *fakeClock {
	return &fakeClock{now: time.Date(2001, 9, 9, 1, 46, 40, 0, time.UTC)}
}

var noContent = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
})

func code(h http.Handler, remoteAddr string) int {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = remoteAddr
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestPerClientWithFakeClock(t *testing.T) {
	clk := newClock()
	h := middleware.PerClient(ratelimit.NewKeyedWithClock(1, 2, clk), noContent)
	steps := []struct {
		advance time.Duration
		addr    string
		want    int
	}{
		{0, "10.0.0.1:1", 204},
		{0, "10.0.0.1:2", 204},
		{0, "10.0.0.1:3", 429},
		{0, "10.0.0.2:1", 204},
		{time.Second, "10.0.0.1:4", 204}, // one token a second: client 1 may send one more
		{0, "10.0.0.1:5", 429},
		{0, "10.0.0.2:2", 204}, // client 2 had 1 left and gained 1: two more requests
		{0, "10.0.0.2:3", 204},
		{0, "10.0.0.2:4", 429},
	}
	for i, s := range steps {
		clk.Advance(s.advance)
		if got := code(h, s.addr); got != s.want {
			t.Errorf("step %d (%s): status %d, want %d", i+1, s.addr, got, s.want)
		}
	}
}

func TestGlobalWithFakeClock(t *testing.T) {
	clk := newClock()
	h := middleware.Global(ratelimit.NewWithClock(2, 1, clk), noContent) // 2 tokens per second, burst 1
	steps := []struct {
		advance time.Duration
		want    int
	}{
		{0, 204},
		{0, 429},
		{250 * time.Millisecond, 429}, // half a token
		{250 * time.Millisecond, 204}, // one token
		{0, 429},
	}
	for i, s := range steps {
		clk.Advance(s.advance)
		if got := code(h, "10.0.0.1:1"); got != s.want {
			t.Errorf("step %d: status %d, want %d", i+1, got, s.want)
		}
	}
}

func TestRetryAfterStillOne(t *testing.T) {
	h := middleware.Global(ratelimit.NewWithClock(1, 1, newClock()), noContent)
	code(h, "10.0.0.1:1")
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "1" {
		t.Fatalf("got %d with Retry-After %q, want 429 with \"1\"", rec.Code, rec.Header().Get("Retry-After"))
	}
}
