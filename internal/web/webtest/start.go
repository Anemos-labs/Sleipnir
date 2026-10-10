package webtest

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"
)

// Running is a fake server serving on a loopback port.
type Running struct {
	*Server
	// Addr is the address it listens on, host:port.
	Addr string
	// URL is the first line `sleipnir web` would print: the address with the run token.
	URL string
	// Base is the address without the token, "http://" + Addr.
	Base string

	cancel context.CancelFunc
	done   chan error
}

// Start builds a fake server and serves it on a free loopback port (or o.Addr) until Close.
func Start(o Options) (*Running, error) {
	s, err := NewServer(o)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", s.opts.Addr)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &Running{Server: s, Addr: ln.Addr().String(), Base: "http://" + ln.Addr().String(), cancel: cancel, done: make(chan error, 1)}
	r.URL = s.Web.URL(ln.Addr())
	go func() { r.done <- s.Serve(ctx, ln) }()
	return r, nil
}

// Close stops the server and waits for it.
func (r *Running) Close() error {
	r.cancel()
	return <-r.done
}

// StartT is Start for a test: the server stops when the test ends.
func StartT(t testing.TB, o Options) *Running {
	t.Helper()
	r, err := Start(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Errorf("fake server: %v", err)
		}
	})
	return r
}

// Client returns an HTTP client that is signed in to the server the way a script is: every request carries the run token as a bearer
// token and, for the methods that need them, the custom header and the JSON content type are left to the caller.
func (r *Running) Client() *http.Client {
	return &http.Client{Timeout: 30 * time.Second, Transport: &bearer{token: r.Web.Token(), next: http.DefaultTransport}}
}

// bearer adds the Authorization header.
type bearer struct {
	token string
	next  http.RoundTripper
}

// RoundTrip sends the request with the token.
func (b *bearer) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+b.token)
	return b.next.RoundTrip(req)
}
