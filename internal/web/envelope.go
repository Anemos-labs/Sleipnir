package web

import (
	"context"
	"net"
	"net/http"
	"path"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
)

// contentSecurityPolicy is what the page may do. Scripts, styles, fonts, images and connections
// come from this origin only. Inline style attributes are allowed (style-src-attr) because the
// UI builds markup with style attributes; inline scripts, inline style elements, framing, forms
// and <base> stay forbidden. data: images are allowed for small inline icons.
const contentSecurityPolicy = "default-src 'none'; script-src 'self'; style-src 'self'; style-src-attr 'unsafe-inline'; " +
	"font-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; form-action 'none'; base-uri 'none'"

// setSecurityHeaders sets the headers that every response carries, errors and streams included.
func setSecurityHeaders(h http.Header) {
	h.Set("Content-Security-Policy", contentSecurityPolicy)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=(), display-capture=()")
	h.Set("Cache-Control", "no-store")
}

// statusWriter notes whether a response has started, so that the panic handler knows whether it
// can still send an error. It passes Flush through and exposes the writer it wraps to
// http.ResponseController.
type statusWriter struct {
	http.ResponseWriter
	wrote bool
}

// WriteHeader records that the response has started.
func (w *statusWriter) WriteHeader(code int) {
	if code >= 200 {
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(code)
}

// Write records that the response has started. A body whose handler named no content type is labelled as bytes: Go would otherwise
// label it by sniffing its first bytes, and a body that looks like HTML would be served as HTML.
func (w *statusWriter) Write(b []byte) (int, error) {
	w.wrote = true
	if h := w.ResponseWriter.Header(); h.Get("Content-Type") == "" {
		h.Set("Content-Type", "application/octet-stream")
	}
	return w.ResponseWriter.Write(b)
}

// Flush sends buffered data to the client.
func (w *statusWriter) Flush() {
	w.wrote = true
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

// Unwrap returns the writer that is wrapped, for http.ResponseController.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// ServeHTTP is the envelope: one chain, in a fixed order, for every request. See the package
// documentation for what each step does and why they are in this order.
func (s *Server) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	w := &statusWriter{ResponseWriter: rw}
	info := &reqInfo{id: newRequestID(), srv: s}
	setSecurityHeaders(w.Header())
	w.Header().Set("X-Request-Id", info.id)
	r = r.WithContext(context.WithValue(r.Context(), infoKey{}, info))
	defer s.recoverPanic(w, r, info)
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(defaultWriteTimeout))

	select {
	case s.sem <- struct{}{}:
		var once sync.Once
		info.release = func() { once.Do(func() { <-s.sem }) }
		defer info.release()
	default:
		w.Header().Set("Retry-After", "1")
		Error(w, http.StatusServiceUnavailable, "busy", "the server is handling as many requests as it accepts; try again")
		return
	}

	if !s.hostAllowed(r.Host) {
		Error(w, http.StatusForbidden, "bad_host", "unexpected Host header")
		return
	}
	if !s.checkSite(w, r, info) {
		return
	}
	if !s.authenticate(w, r, info) {
		return
	}
	if info.needBearer && (info.p == nil || info.p.kind != credBearer) {
		Error(w, http.StatusForbidden, "forbidden_origin",
			"a request without an Origin header is not from a browser page; it must authenticate with Authorization: Bearer")
		return
	}
	s.dispatch(w, r)
}

// recoverPanic turns a panic in the chain into a 500 and a log line. The panic value and a short
// stack go to the log (masked); the client learns nothing. If the response has already started the
// connection is aborted, so that the client sees a broken stream instead of one that ended well.
func (s *Server) recoverPanic(w *statusWriter, r *http.Request, info *reqInfo) {
	p := recover()
	if p == nil {
		return
	}
	if p == http.ErrAbortHandler {
		panic(p)
	}
	stack := debug.Stack()
	if len(stack) > 2048 {
		stack = stack[:2048]
	}
	route := info.route
	if route == "" {
		route = "-"
	}
	s.Logf("panic in %s %s [%s]: %v\n%s", r.Method, route, info.id, p, stack)
	if w.wrote {
		panic(http.ErrAbortHandler)
	}
	Error(w, http.StatusInternalServerError, "internal", "internal error")
}

// hostAllowed reports whether the Host header names this server: a loopback name or address on
// the port that was bound. With remote access allowed there is no allowlist (the token is the
// protection, and a cookie belongs to one host name).
func (s *Server) hostAllowed(hostport string) bool {
	if s.hosts == nil {
		return hostport != ""
	}
	host, port := hostport, ""
	if h, p, err := net.SplitHostPort(hostport); err == nil {
		host, port = h, p
	}
	host = strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(host, "["), "]"))
	if !s.hosts[host] {
		return false
	}
	want := int(s.port.Load())
	if want == 0 {
		return true // not bound yet: the handler is being driven directly
	}
	if port == "" {
		return want == 80
	}
	return port == strconv.Itoa(want)
}

// checkSite applies Fetch Metadata and Origin rules. A request from another site, or from another
// origin on this machine (another port is "same-site"), may only be a top-level navigation to a
// page. An unsafe request must carry the Origin of this server exactly. An unsafe request with
// neither Origin nor Fetch Metadata is not from a browser and is marked as needing the bearer
// token (checked once the caller is known).
func (s *Server) checkSite(w http.ResponseWriter, r *http.Request, info *reqInfo) bool {
	safe := r.Method == http.MethodGet || r.Method == http.MethodHead
	site := r.Header.Get("Sec-Fetch-Site")
	if site != "" && site != "same-origin" && site != "none" {
		nav := safe && r.Header.Get("Sec-Fetch-Mode") == "navigate" && !strings.HasPrefix(r.URL.Path, "/api/")
		if !nav {
			Error(w, http.StatusForbidden, "forbidden_site", "requests from another site or origin are not accepted")
			return false
		}
	}
	if safe {
		return true
	}
	origins := r.Header.Values("Origin")
	switch {
	case len(origins) > 1:
		Error(w, http.StatusForbidden, "forbidden_origin", "more than one Origin header")
		return false
	case len(origins) == 1:
		if !sameOrigin(r, origins[0]) {
			Error(w, http.StatusForbidden, "forbidden_origin", "the Origin of this request is not this server")
			return false
		}
	case site == "":
		info.needBearer = true
	}
	return true
}

// sameOrigin reports whether an Origin header is exactly the origin the request was addressed to:
// same scheme, same host, same port. The Host header has already been checked against the
// allowlist and the bound port.
func sameOrigin(r *http.Request, origin string) bool {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return strings.EqualFold(origin, scheme+"://"+r.Host)
}

// cleanPath reports whether a request path is in canonical form: absolute, with no empty,
// dot or dot-dot elements, no backslash and no NUL. A trailing slash is allowed.
func cleanPath(p string) bool {
	if !strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\\x00") {
		return false
	}
	c := path.Clean(p)
	if strings.HasSuffix(p, "/") && c != "/" {
		c += "/"
	}
	return c == p
}

// dispatch routes the request. A request that matches no pattern is answered by miss. Requests
// with a path that is not in canonical form are not routed at all, so the mux never redirects.
func (s *Server) dispatch(w http.ResponseWriter, r *http.Request) {
	if !cleanPath(r.URL.Path) {
		Error(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	if _, pattern := s.mux.Handler(r); pattern == "" {
		s.miss(w, r)
		return
	}
	s.mux.ServeHTTP(w, r)
}

// catchAll reports whether a pattern is one that exists to answer what no route does: a method on "/" or on "/api/" (the built-in
// GET ones, and the fallbacks a route package may add for other methods). A path that only those match has no methods of its own.
func catchAll(pattern string) bool {
	_, path, ok := strings.Cut(pattern, " ")
	return ok && (path == "/" || path == "/api/")
}

// miss answers a request that no route handles, as JSON: 405 with the methods the path does have
// when it has others, else 404.
func (s *Server) miss(w http.ResponseWriter, r *http.Request) {
	var allow []string
	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		probe := new(http.Request)
		*probe = *r
		probe.Method = m
		if _, pattern := s.mux.Handler(probe); pattern != "" && !catchAll(pattern) {
			allow = append(allow, m)
			if m == http.MethodGet {
				allow = append(allow, http.MethodHead)
			}
		}
	}
	if len(allow) > 0 {
		w.Header().Set("Allow", strings.Join(allow, ", "))
		Error(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed for this path")
		return
	}
	Error(w, http.StatusNotFound, "not_found", "not found")
}
