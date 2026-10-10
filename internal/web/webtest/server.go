package webtest

import (
	"context"
	"io/fs"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anemos-labs/sleipnir/internal/web"
)

// Options configure a fake server.
type Options struct {
	// Addr is the listen address (default 127.0.0.1:0: a free port).
	Addr string
	// UI is the file system served for the page; the default is the UI embedded in the binary. A directory (os.DirFS) lets the page's
	// files be edited between runs of the server.
	UI fs.FS
	// Host is the fake host (default NewShopHost()).
	Host *Host
	// Speed plays the canned continuation at this multiple of real time once the server runs: 1 is real time, 0 plays nothing (drive it
	// with POST /api/_fake/step and /api/_fake/play).
	Speed float64
	// Logf receives the server's operational log.
	Logf func(format string, args ...any)
}

// Server is the fake server: the real envelope, hub and UI of internal/web with the fake host and the fake routes.
type Server struct {
	// Web is the underlying server; its Token and URL are the real ones.
	Web *web.Server
	// Host is the fake host the routes use.
	Host *Host

	opts Options
	addr atomic.Value // string
}

// NewServer builds the fake server. It does not listen.
func NewServer(o Options) (*Server, error) {
	if o.Addr == "" {
		o.Addr = "127.0.0.1:0"
	}
	if o.Host == nil {
		o.Host = NewShopHost()
	}
	s := &Server{Host: o.Host, opts: o}
	s.addr.Store(o.Addr)
	w, err := web.New(web.Config{
		Addr: o.Addr, UI: o.UI, Logf: o.Logf,
		// the hub of `sleipnir web` (CONTRACT.md 2.9, delta 3): one topic carries every tab
		Hub: web.HubConfig{ReplayEvents: 20000, ReplayBytes: 32 << 20, Buffer: 2048, BufferBytes: 8 << 20, MaxEventBytes: 1 << 20},
		Routes: func(srv *web.Server) {
			RegisterRoutes(srv, o.Host, RouteOptions{Addr: func() string { return s.addr.Load().(string) }})
		},
	})
	if err != nil {
		return nil, err
	}
	s.Web = w
	return s, nil
}

// Serve serves on ln until ctx ends. The address the hello reports is the listener's. If Options.Speed is positive the canned
// continuation is played while it runs.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	s.addr.Store(ln.Addr().String())
	var wg sync.WaitGroup
	pctx, stop := context.WithCancel(ctx)
	defer func() { stop(); wg.Wait() }()
	if s.opts.Speed > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Host.Play(pctx, TabID, s.opts.Speed)
		}()
	}
	return s.Web.Serve(ctx, ln)
}

// Play publishes the continuation of the tab's canned session at speed times real time, starting after the first event's delay,
// until it is over or ctx ends.
func (h *Host) Play(ctx context.Context, tabID string, speed float64) {
	t := h.FakeTab(tabID)
	if t == nil || speed <= 0 {
		return
	}
	last := Now
	for {
		next, ok := t.NextAt()
		if !ok {
			return
		}
		d := time.Duration((next - last) / speed * float64(time.Second))
		timer := time.NewTimer(d)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if !t.Step() {
			return
		}
		last = next
	}
}
