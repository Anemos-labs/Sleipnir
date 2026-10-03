package inspect

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
)

//go:embed web
var webFS embed.FS

// contentSecurityPolicy is deliberately tight: the UI is external files served
// from this binary, so no inline script or style is ever needed, nothing is loaded
// from elsewhere, and the page cannot be framed, post forms or open connections
// to any other origin.
const contentSecurityPolicy = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; " +
	"connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

const cookieName = "sleipnir_inspect"

// Config configures a Server.
type Config struct {
	// Root is a session directory (holding events.jsonl and blobs/) or a directory
	// of sessions, such as `sleipnir rl rollout` output.
	Root string
	// Addr is the listen address. It also decides which Host headers are
	// accepted: a loopback address only answers to loopback names, which blocks
	// DNS-rebinding pages from reading the dashboard.
	Addr string
	// Token, when set, is required on every request (except /healthz). It is
	// mandatory for a non-loopback Addr.
	Token string
	// Interval is how often live logs are polled. Default 1s.
	Interval time.Duration
	// LoadWait is how long a request waits for a session that is still loading
	// before it is told to retry (202 with progress). Default 1.5s.
	LoadWait time.Duration
	// Options apply to every session loaded.
	Options Options
	// Logf receives operational messages (never request contents or the token).
	Logf func(format string, args ...any)
}

// Server serves the inspector API and UI. It is read-only: GET and HEAD only.
type Server struct {
	cfg       Config
	reg       *registry
	assets    map[string]*asset
	hasToken  bool
	tokenSum  [sha256.Size]byte
	cookieVal []byte
	hosts     map[string]bool // accepted Host names; nil accepts any (token-protected)
	mux       *http.ServeMux
	logf      func(string, ...any)
}

type asset struct {
	data  []byte
	ctype string
	etag  string
}

// NewServer scans cfg.Root and prepares the handler. It does not listen.
func NewServer(cfg Config) (*Server, error) {
	if cfg.Root == "" {
		return nil, errors.New("inspect: a session directory is required")
	}
	if cfg.Interval <= 0 {
		cfg.Interval = time.Second
	}
	if cfg.LoadWait <= 0 {
		cfg.LoadWait = 1500 * time.Millisecond
	}
	cfg.Options.fill()
	logf := cfg.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if cfg.Addr != "" {
		if err := checkListenAddr(cfg.Addr, cfg.Token); err != nil {
			return nil, err
		}
	}
	reg, err := newRegistry(cfg.Root, cfg.Options, logf)
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, reg: reg, assets: map[string]*asset{}, logf: logf}
	if cfg.Token != "" {
		s.hasToken = true
		s.tokenSum = sha256.Sum256([]byte(cfg.Token))
		cv := sha256.Sum256([]byte("sleipnir-inspect-cookie\x00" + cfg.Token))
		s.cookieVal = []byte(hex.EncodeToString(cv[:]))
	}
	s.hosts = allowedHosts(cfg.Addr, s.hasToken)
	if err := s.loadAssets(); err != nil {
		return nil, err
	}
	s.mux = http.NewServeMux()
	s.routes()
	return s, nil
}

func (s *Server) loadAssets() error {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		return err
	}
	return fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(sub, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		ct := "application/octet-stream"
		switch path.Ext(p) {
		case ".html":
			ct = "text/html; charset=utf-8"
		case ".js":
			ct = "text/javascript; charset=utf-8"
		case ".css":
			ct = "text/css; charset=utf-8"
		case ".svg":
			ct = "image/svg+xml"
		}
		s.assets["/"+p] = &asset{data: data, ctype: ct, etag: `"` + hex.EncodeToString(sum[:8]) + `"`}
		return nil
	})
}

func (s *Server) routes() {
	m := s.mux
	m.HandleFunc("/healthz", s.handleHealth)
	m.HandleFunc("/api/sessions", s.handleSessions)
	m.HandleFunc("/api/summary", s.withSession(func(w http.ResponseWriter, r *http.Request, sess *Session) {
		s.writeJSON(w, http.StatusOK, sess.Summary())
	}))
	m.HandleFunc("/api/agents", s.withSession(func(w http.ResponseWriter, r *http.Request, sess *Session) {
		s.writeJSON(w, http.StatusOK, sess.Agents())
	}))
	m.HandleFunc("/api/agent/{id}", s.withSession(s.handleAgent))
	m.HandleFunc("/api/requests", s.withSession(s.handleRequests))
	m.HandleFunc("/api/compactions", s.withSession(func(w http.ResponseWriter, r *http.Request, sess *Session) {
		s.writeJSON(w, http.StatusOK, sess.Compactions())
	}))
	m.HandleFunc("/api/anomalies", s.withSession(func(w http.ResponseWriter, r *http.Request, sess *Session) {
		s.writeJSON(w, http.StatusOK, sess.Anomalies())
	}))
	m.HandleFunc("/api/swarm", s.withSession(func(w http.ResponseWriter, r *http.Request, sess *Session) {
		s.writeJSON(w, http.StatusOK, sess.Swarm())
	}))
	m.HandleFunc("/api/layers", s.withSession(s.handleLayers))
	m.HandleFunc("/api/events", s.withSession(s.handleEvents))
	m.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { s.fail(w, http.StatusNotFound, "no such endpoint") })
	m.HandleFunc("/", s.handleStatic)
}

// ServeHTTP applies the security envelope, then routes.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Security-Policy", contentSecurityPolicy)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")

	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		h.Set("Allow", "GET, HEAD")
		s.fail(w, http.StatusMethodNotAllowed, "the inspector is read-only: GET only")
		return
	}
	if !s.hostAllowed(r.Host) {
		s.fail(w, http.StatusForbidden, "unexpected Host header")
		return
	}
	if r.URL.Path != "/healthz" && s.hasToken && !s.authorized(w, r) {
		return
	}
	s.mux.ServeHTTP(w, r)
}

// Handler returns the server as an http.Handler.
func (s *Server) Handler() http.Handler { return s }

// ---- access control -------------------------------------------------------------------

// isLoopbackHost reports whether host names the local machine only. Anything
// else (including an empty host, which listens everywhere) needs a token.
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

// checkListenAddr validates host:port syntax and requires a token before serving inspection data
// on a non-loopback address.
func checkListenAddr(addr, token string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("inspect: bad address %q: %w", addr, err)
	}
	if !isLoopbackHost(host) && token == "" {
		return fmt.Errorf("inspect: refusing to listen on non-loopback address %q without --token: the dashboard shows prompts, notes and tool output", addr)
	}
	return nil
}

// Listen opens the listening socket, enforcing the loopback rule twice: on the
// requested address and on the address actually bound.
func Listen(addr, token string) (net.Listener, error) {
	if err := checkListenAddr(addr, token); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	if ta, ok := ln.Addr().(*net.TCPAddr); ok && token == "" && !ta.IP.IsLoopback() {
		ln.Close()
		return nil, fmt.Errorf("inspect: bound to non-loopback address %s without a token", ta)
	}
	return ln, nil
}

// allowedHosts returns the Host names a loopback-bound server answers to. A
// page on another origin that rebinds its DNS name to 127.0.0.1 sends its own
// name in Host, so refusing unknown names stops it reading the API. With a token
// on a public address every request must prove the token anyway.
func allowedHosts(addr string, hasToken bool) map[string]bool {
	host := ""
	if addr != "" {
		host, _, _ = net.SplitHostPort(addr)
	}
	if !isLoopbackHost(host) && addr != "" {
		if hasToken {
			return nil
		}
	}
	m := map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true}
	if h := strings.ToLower(strings.Trim(host, "[]")); h != "" {
		m[h] = true
	}
	return m
}

// hostAllowed normalizes the request hostname and checks the host allowlist, allowing all hosts
// only when no list is configured.
func (s *Server) hostAllowed(hostport string) bool {
	if s.hosts == nil {
		return true
	}
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.ToLower(strings.Trim(host, "[]"))
	return s.hosts[host]
}

// tokenEqual compares a presented token with the configured one in constant
// time: both sides are hashed first so the comparison length never depends on
// the presented value.
func (s *Server) tokenEqual(given string) bool {
	sum := sha256.Sum256([]byte(given))
	return subtle.ConstantTimeCompare(sum[:], s.tokenSum[:]) == 1
}

// cookieEqual compares a supplied cookie against the stored bytes with constant-time comparison.
func (s *Server) cookieEqual(given string) bool {
	return subtle.ConstantTimeCompare([]byte(given), s.cookieVal) == 1
}

// authorized checks the bearer header, the session cookie, then ?token= (the
// way a browser is handed the token the first time). A token in the URL is
// turned into a cookie and stripped by a redirect so it does not stay in the
// address bar, history or referrers.
func (s *Server) authorized(w http.ResponseWriter, r *http.Request) bool {
	if a := r.Header.Get("Authorization"); len(a) > 7 && strings.EqualFold(a[:7], "bearer ") && s.tokenEqual(strings.TrimSpace(a[7:])) {
		return true
	}
	if c, err := r.Cookie(cookieName); err == nil && s.cookieEqual(c.Value) {
		return true
	}
	if t := r.URL.Query().Get("token"); t != "" && s.tokenEqual(t) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			return true
		}
		http.SetCookie(w, &http.Cookie{Name: cookieName, Value: string(s.cookieVal), Path: "/", HttpOnly: true,
			SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil, MaxAge: 7 * 24 * 3600})
		q := r.URL.Query()
		q.Del("token")
		// The target is one of this server's own pages, never something taken from the
		// request line: "//host/x" or "/\host" must not become a redirect to another origin.
		loc := "/"
		if s.assets[r.URL.Path] != nil {
			loc = r.URL.Path
		}
		if rq := q.Encode(); rq != "" {
			loc += "?" + rq
		}
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, loc, http.StatusSeeOther)
		return false
	}
	w.Header().Set("WWW-Authenticate", `Bearer realm="sleipnir-inspect"`)
	s.fail(w, http.StatusUnauthorized, "a token is required: open the URL printed by `sleipnir inspect`, or send Authorization: Bearer <token>")
	return false
}

// ---- responses ------------------------------------------------------------------------

// writeJSON encodes before sending headers, HTML-escapes log text, disables caching, and sends a
// generic error if encoding fails.
func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf) // HTML-escapes <, > and &: log strings stay inert even if sniffed
	if err := enc.Encode(v); err != nil {
		s.logf("inspect: encoding response: %v", err)
		s.fail(w, http.StatusInternalServerError, "could not encode the response")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// fail writes an uncached JSON error response with the specified status, ignoring response-write
// errors.
func (s *Server) fail(w http.ResponseWriter, status int, msg string) {
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	b, _ := json.Marshal(map[string]any{"error": msg})
	_, _ = w.Write(append(b, '\n'))
}

// handleHealth replies with HTTP 200 and a JSON health indicator.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleStatic serves known embedded assets with ETags, mapping the root path to index.html and
// honoring conditional requests.
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	if p == "/" {
		p = "/index.html"
	}
	a := s.assets[p]
	if a == nil {
		s.fail(w, http.StatusNotFound, "not found")
		return
	}
	h := w.Header()
	h.Set("Content-Type", a.ctype)
	h.Set("Cache-Control", "no-cache")
	h.Set("ETag", a.etag)
	if r.Header.Get("If-None-Match") == a.etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = w.Write(a.data)
}

// ---- API handlers -----------------------------------------------------------------------

const maxParam = 512

// handleSessions returns the registered session list and uses a relative current-session marker in
// single-session mode.
func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	current := ""
	if e := s.reg.only(); e != nil {
		current = e.id
	}
	list := s.reg.list(current)
	if s.reg.single {
		list.Current = "."
	}
	s.writeJSON(w, http.StatusOK, list)
}

// withSession resolves ?session= to a loaded Session. The id is a lookup key into
// the set of sessions the registry found; it is never used as a path.
func (s *Server) withSession(fn func(http.ResponseWriter, *http.Request, *Session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("session")
		if len(id) > maxParam || strings.ContainsRune(id, 0) {
			s.fail(w, http.StatusBadRequest, "bad session parameter")
			return
		}
		e := s.reg.get(id)
		if e == nil && id == "" {
			e = s.reg.only()
		}
		if e == nil {
			if id == "" {
				s.fail(w, http.StatusBadRequest, "session parameter required: this directory holds several sessions")
			} else {
				s.fail(w, http.StatusNotFound, "unknown session")
			}
			return
		}
		sess, ready, err := e.ensure(s.cfg.Options, s.cfg.LoadWait, s.logf)
		switch {
		case !ready:
			s.writeJSON(w, http.StatusAccepted, map[string]any{"loading": true, "read": e.read.Load(), "total": e.total.Load()})
			return
		case err != nil || sess == nil:
			s.fail(w, http.StatusInternalServerError, "cannot read this session's log")
			return
		}
		s.reg.trim()
		fn(w, r, sess)
	}
}

// handleAgent validates agent ID length and returns agent detail or a JSON not-found response.
func (s *Server) handleAgent(w http.ResponseWriter, r *http.Request, sess *Session) {
	id := r.PathValue("id")
	if len(id) > maxParam {
		s.fail(w, http.StatusBadRequest, "bad agent id")
		return
	}
	d, ok := sess.Agent(id)
	if !ok {
		s.fail(w, http.StatusNotFound, "unknown agent")
		return
	}
	s.writeJSON(w, http.StatusOK, d)
}

// uintParam parses an optional unsigned query parameter.
func uintParam(r *http.Request, name string, def, max uint64) (uint64, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("bad %s parameter", name)
	}
	return min(n, max), nil
}

// textParam reads a query value and rejects oversized values or embedded NUL bytes.
func textParam(r *http.Request, name string) (string, error) {
	v := r.URL.Query().Get(name)
	if len(v) > maxParam || strings.ContainsRune(v, 0) {
		return "", fmt.Errorf("bad %s parameter", name)
	}
	return v, nil
}

func (s *Server) handleRequests(w http.ResponseWriter, r *http.Request, sess *Session) {
	q := RequestQuery{}
	var err error
	if q.Agent, err = textParam(r, "agent"); err != nil {
		s.fail(w, http.StatusBadRequest, err.Error())
		return
	}
	switch k := r.URL.Query().Get("kind"); k {
	case "", "main", "compactor", "side":
		q.Kind = k
	default:
		s.fail(w, http.StatusBadRequest, "kind must be main, compactor or side")
		return
	}
	since, err := uintParam(r, "since", 0, 1<<62)
	if err != nil {
		s.fail(w, http.StatusBadRequest, err.Error())
		return
	}
	limit, err := uintParam(r, "limit", 1000, 20000)
	if err != nil {
		s.fail(w, http.StatusBadRequest, err.Error())
		return
	}
	q.Since, q.Limit = since, int(limit)
	switch r.URL.Query().Get("tail") {
	case "1", "true":
		q.Tail = true
	}
	s.writeJSON(w, http.StatusOK, sess.Requests(q))
}

// handleLayers requires a valid request ID and optionally includes layer text for text=1 or
// text=true.
func (s *Server) handleLayers(w http.ResponseWriter, r *http.Request, sess *Session) {
	id, err := textParam(r, "req")
	if err != nil || id == "" {
		s.fail(w, http.StatusBadRequest, "req parameter required")
		return
	}
	text := false
	switch r.URL.Query().Get("text") {
	case "1", "true":
		text = true
	}
	rep, ok := sess.Layers(id, text)
	if !ok {
		s.fail(w, http.StatusNotFound, "unknown request")
		return
	}
	s.writeJSON(w, http.StatusOK, rep)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request, sess *Session) {
	q := EventQuery{}
	var err error
	if q.Agent, err = textParam(r, "agent"); err != nil {
		s.fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if q.Type, err = textParam(r, "type"); err != nil {
		s.fail(w, http.StatusBadRequest, err.Error())
		return
	}
	limit, err := uintParam(r, "limit", 200, 2000)
	if err != nil {
		s.fail(w, http.StatusBadRequest, err.Error())
		return
	}
	q.Limit = int(limit)
	if r.URL.Query().Get("since") == "" {
		q.Tail = true
	} else if q.Since, err = uintParam(r, "since", 0, 1<<62); err != nil {
		s.fail(w, http.StatusBadRequest, err.Error())
		return
	}
	page, err := sess.Events(q)
	if err != nil {
		s.logf("inspect: events: %v", err)
		s.fail(w, http.StatusInternalServerError, "cannot read the event log")
		return
	}
	s.writeJSON(w, http.StatusOK, page)
}

// ---- running ----------------------------------------------------------------------------

// Serve serves on ln until ctx is cancelled, polling live logs every Interval.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	if ta, ok := ln.Addr().(*net.TCPAddr); ok && !s.hasToken && !ta.IP.IsLoopback() {
		return fmt.Errorf("inspect: refusing to serve on non-loopback address %s without a token", ta)
	}
	hs := &http.Server{
		Handler:           s,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	tctx, stop := context.WithCancel(ctx)
	defer stop()
	go s.tickLoop(tctx)
	errc := make(chan error, 1)
	go func() { errc <- hs.Serve(ln) }()
	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = hs.Shutdown(shutdown)
		return nil
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// tickLoop follows live logs and, in multi-session mode, notices new sessions.
func (s *Server) tickLoop(ctx context.Context) {
	t := time.NewTicker(s.cfg.Interval)
	defer t.Stop()
	go s.reg.indexPending(ctx)
	n := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.reg.tailAll()
			n++
			if n%5 == 0 {
				if err := s.reg.scan(); err != nil {
					s.logf("inspect: rescanning: %v", err)
				}
				go s.reg.indexPending(ctx)
			}
		}
	}
}

// Tick polls every loaded session once (for callers that drive the loop
// themselves, and for tests).
func (s *Server) Tick() { s.reg.tailAll() }
