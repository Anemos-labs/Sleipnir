package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// DefaultAddr is the address `sleipnir web` listens on when none is given.
const DefaultAddr = "127.0.0.1:6969"

const (
	defaultMaxInFlight = 128
	defaultSessionTTL  = 24 * time.Hour
	defaultConfirmTTL  = time.Minute

	// shutdownGrace is how long Serve waits for requests to finish after its context ends.
	shutdownGrace = 3 * time.Second
)

// Config configures a Server. The zero value is usable: it listens on DefaultAddr, serves the
// embedded UI and has no routes beyond the built-in ones.
type Config struct {
	// Addr is the listen address, host:port. It also decides which Host headers are accepted. It
	// must be a loopback address unless AllowNonLoopback is set. Port 0 picks a free port; the
	// port that was bound is what the Host and Origin checks use. Default DefaultAddr.
	Addr string
	// AllowNonLoopback permits an Addr that is not loopback. The token is always required; plain
	// HTTP over a network exposes it to anything on the path, and the page can run commands, so
	// the supported way to reach the server from another machine is an SSH tunnel. The command
	// does not set this.
	AllowNonLoopback bool
	// UI is the file system served for the page and its assets; it must hold index.html. Default:
	// the UI embedded in the binary.
	UI fs.FS
	// Routes, if set, is called once by New, after the built-in routes are registered, so that the
	// code that knows about sessions can add its routes (Server.Handle) and use Server.Hub.
	Routes func(*Server)
	// Logf receives operational messages: panics, rotations, routes' own lines. Credentials are
	// masked in them. Nil discards them.
	Logf func(format string, args ...any)
	// Hub configures the event hub (Server.Hub).
	Hub HubConfig
	// SessionTTL is how long a cookie session lasts, counted from the token exchange. Default 24h.
	SessionTTL time.Duration
	// ConfirmTTL is how long a confirmation id can be spent. Default one minute.
	ConfirmTTL time.Duration
	// MaxInFlight bounds the requests being handled at once, event streams excluded; the next one
	// is answered 503. Default 128.
	MaxInFlight int
	// Now is the clock for session expiry, confirmations and throttling. Default time.Now. It
	// exists so that tests do not sleep.
	Now func() time.Time
}

// Server is the HTTP server of `sleipnir web`: the envelope, the built-in routes, the event hub
// and the embedded UI. Create one with New, add routes with Handle, then Listen and Serve.
type Server struct {
	cfg    Config
	auth   *authority
	hub    *Hub
	assets *assetSet
	mux    *http.ServeMux
	hosts  map[string]bool // accepted host names; nil accepts any (remote access)
	port   atomic.Int32    // the port that was bound; 0 until known
	sem    chan struct{}
	served atomic.Bool

	// timeouts, overridden by tests
	readHeaderTimeout time.Duration
	readTimeout       time.Duration
	idleTimeout       time.Duration
}

// New creates a server. It validates the address, generates the run token, loads the UI and
// registers the built-in routes, then lets cfg.Routes add the rest. It does not listen.
func New(cfg Config) (*Server, error) {
	if cfg.Addr == "" {
		cfg.Addr = DefaultAddr
	}
	if err := checkListenAddr(cfg.Addr, cfg.AllowNonLoopback); err != nil {
		return nil, err
	}
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = defaultSessionTTL
	}
	if cfg.ConfirmTTL <= 0 {
		cfg.ConfirmTTL = defaultConfirmTTL
	}
	if cfg.MaxInFlight <= 0 {
		cfg.MaxInFlight = defaultMaxInFlight
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	auth, err := newAuthority(cfg.Now, cfg.SessionTTL, cfg.ConfirmTTL)
	if err != nil {
		return nil, fmt.Errorf("web: no source of randomness for the token: %w", err)
	}
	ui := cfg.UI
	if ui == nil {
		ui = embeddedUI()
	}
	assets, err := loadAssets(ui)
	if err != nil {
		return nil, err
	}
	s := &Server{
		cfg: cfg, auth: auth, assets: assets, hub: NewHub(cfg.Hub),
		mux: http.NewServeMux(), sem: make(chan struct{}, cfg.MaxInFlight),
		readHeaderTimeout: 10 * time.Second, readTimeout: 30 * time.Second, idleTimeout: 2 * time.Minute,
	}
	host, port, _ := net.SplitHostPort(cfg.Addr)
	if n, err := strconv.Atoi(port); err == nil {
		s.port.Store(int32(n))
	}
	if !cfg.AllowNonLoopback {
		s.hosts = map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true}
		s.hosts[strings.ToLower(strings.Trim(host, "[]"))] = true
	}
	s.registerBuiltins()
	if cfg.Routes != nil {
		cfg.Routes(s)
	}
	return s, nil
}

// Handler returns the server as an http.Handler.
func (s *Server) Handler() http.Handler { return s }

// Hub returns the event hub of the server. Routes publish to it; the stream route is
// srv.Hub().ServeSSE registered with Handle.
func (s *Server) Hub() *Hub { return s.hub }

// registerBuiltins adds the routes every server has.
func (s *Server) registerBuiltins() {
	s.Handle("GET /healthz", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
	}), RouteOpts{public: true})
	s.Handle("GET /api/ping", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
	}), RouteOpts{})
	s.Handle("POST /api/confirm", http.HandlerFunc(s.handleConfirm), RouteOpts{MaxBody: 1 << 10})
	s.Handle("POST /api/auth/logout", http.HandlerFunc(s.handleLogout), RouteOpts{NoBody: true})
	s.Handle("POST /api/auth/rotate", http.HandlerFunc(s.handleRotate), RouteOpts{NoBody: true})
	s.Handle("GET /api/", http.HandlerFunc(s.miss), RouteOpts{})
	s.Handle("GET /", http.HandlerFunc(s.handleAssets), RouteOpts{})
}

// ---- listening and serving ------------------------------------------------------------------

// isLoopbackHost reports whether host names only this machine. Anything else, including an empty
// host (which listens everywhere), is remote.
func isLoopbackHost(host string) bool {
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if i := strings.IndexByte(host, '%'); i >= 0 {
		host = host[:i]
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// checkListenAddr validates host:port and refuses a non-loopback host unless remote access was
// asked for.
func checkListenAddr(addr string, allowRemote bool) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("web: bad address %q: %w", addr, err)
	}
	if n, err := strconv.ParseUint(port, 10, 16); err != nil {
		return fmt.Errorf("web: bad port in address %q", addr)
	} else if n > 65535 {
		return fmt.Errorf("web: bad port in address %q", addr)
	}
	if !isLoopbackHost(host) && !allowRemote {
		return fmt.Errorf("web: refusing to listen on %q: it is not a loopback address, and the page can start agents and run commands with your rights; "+
			"to use it from another machine, forward the port with SSH (ssh -L 6969:127.0.0.1:6969 host)", addr)
	}
	return nil
}

// isAddrInUse reports whether err is a bind failure because the address is taken.
func isAddrInUse(err error) bool {
	if errors.Is(err, syscall.EADDRINUSE) {
		return true
	}
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "address already in use") || strings.Contains(m, "only one usage of each socket address")
}

// Listen opens the listening socket for addr, enforcing the loopback rule twice: on the requested
// address and on the address that was actually bound (a name that resolves somewhere unexpected
// is caught here). When the address is taken the error says so and names --addr 127.0.0.1:0,
// which picks a free port.
func Listen(addr string, allowNonLoopback bool) (net.Listener, error) {
	if err := checkListenAddr(addr, allowNonLoopback); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		if isAddrInUse(err) {
			return nil, fmt.Errorf("web: %s is already in use (another sleipnir web, or another program): "+
				"stop it, or choose another port; --addr 127.0.0.1:0 picks a free one and prints the URL", addr)
		}
		return nil, fmt.Errorf("web: cannot listen on %s: %w", addr, err)
	}
	if ta, ok := ln.Addr().(*net.TCPAddr); ok && !allowNonLoopback && !ta.IP.IsLoopback() {
		_ = ln.Close()
		return nil, fmt.Errorf("web: %q is bound to the non-loopback address %s; refusing to serve", addr, ta)
	}
	return ln, nil
}

// Serve serves on ln until ctx is cancelled, then shuts down: event streams end, requests in
// flight get a few seconds, and the rest are cut off. It returns nil after a clean shutdown. It
// refuses a listener that is not loopback unless remote access was allowed (the third check of
// the rule), and can be called once.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	ta, ok := ln.Addr().(*net.TCPAddr)
	if ok && !s.cfg.AllowNonLoopback && !ta.IP.IsLoopback() {
		return fmt.Errorf("web: refusing to serve on non-loopback address %s", ta)
	}
	if !s.served.CompareAndSwap(false, true) {
		return errors.New("web: Serve was already called")
	}
	if ok {
		s.port.Store(int32(ta.Port))
	}
	baseCtx, stop := context.WithCancel(context.WithoutCancel(ctx))
	defer stop()
	hs := &http.Server{
		Handler:           s,
		ReadHeaderTimeout: s.readHeaderTimeout,
		ReadTimeout:       s.readTimeout,
		IdleTimeout:       s.idleTimeout,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          log.New(io.Discard, "", 0),
		BaseContext:       func(net.Listener) context.Context { return baseCtx },
		// no WriteTimeout: it would cut event streams. Responses get a deadline each (route.go, envelope.go).
	}
	hs.RegisterOnShutdown(s.hub.Close)
	errc := make(chan error, 1)
	go func() { errc <- hs.Serve(ln) }()
	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		s.hub.Close()
		if err := hs.Shutdown(shutdown); err != nil {
			_ = hs.Close()
		}
		<-errc
		return nil
	case err := <-errc:
		s.hub.Close()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
