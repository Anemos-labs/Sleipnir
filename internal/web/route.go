package web

import (
	"fmt"
	"mime"
	"net/http"
	"strings"
	"time"
)

const (
	// RequestHeader is the custom header every request other than GET and HEAD must carry. A
	// browser cannot add it to a cross-origin request without a CORS preflight, which this server
	// never answers, so a page on another origin cannot send a request that has it.
	RequestHeader = "X-Sleipnir-Web"
	// RequestHeaderValue is the value the header must have.
	RequestHeaderValue = "1"

	// defaultWriteTimeout is how long a response to an ordinary request may take to be written.
	defaultWriteTimeout = 30 * time.Second
)

// RouteOpts declares what a route needs beyond its method and path. The zero value is right for
// a GET route and for a POST route with a small JSON body.
type RouteOpts struct {
	// MaxBody is the largest request body, in bytes, that the route reads: more is refused with
	// 413 before the handler runs, and DecodeJSON enforces it on bodies of unknown length. Zero
	// means DefaultMaxBody. It is ignored for GET routes, whose bodies are never read.
	MaxBody int64
	// NoBody refuses any request body (400), for routes that act on the URL alone.
	NoBody bool
	// NeedsConfirm requires a confirmation id for ConfirmScope in the X-Confirm header (see
	// Server.IssueConfirm), spent by the request. It is for routes that raise privilege.
	NeedsConfirm bool
	// ConfirmScope is the scope the confirmation must have been issued for. Empty means the
	// route's pattern.
	ConfirmScope string
	// WriteTimeout is how long writing the response may take, counted from the start of the
	// request. Zero means 30 seconds. Streams manage their own deadlines.
	WriteTimeout time.Duration

	// public routes skip authentication. Only this package declares them.
	public bool
}

// route is a registered pattern with its options.
type route struct {
	pattern string
	method  string
	safe    bool
	opts    RouteOpts
	h       http.Handler
	stream  bool
}

// streamer is implemented by the handlers of event streams, so that Handle can account for them
// apart from ordinary requests.
type streamer interface{ isStream() }

// splitPattern separates the method of a pattern ("POST /api/x") from its path. Patterns must
// name one of GET, POST, PUT, PATCH or DELETE and an absolute path, and may not name a host.
func splitPattern(pattern string) (method, path string, err error) {
	method, path, ok := strings.Cut(pattern, " ")
	if !ok {
		return "", "", fmt.Errorf("web: pattern %q has no method; write it as \"GET /path\"", pattern)
	}
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
	default:
		return "", "", fmt.Errorf("web: pattern %q: method must be GET, POST, PUT, PATCH or DELETE", pattern)
	}
	path = strings.TrimLeft(path, " ")
	if !strings.HasPrefix(path, "/") {
		return "", "", fmt.Errorf("web: pattern %q: the path must start with / and name no host", pattern)
	}
	return method, path, nil
}

// Handle registers h for a pattern of the form "METHOD /path", with Go's ServeMux path syntax
// ("/api/things/{id}"; the value is r.PathValue("id")). The method is required: GET (which also
// serves HEAD) is a safe method and must not change state; POST, PUT, PATCH and DELETE are unsafe
// and get the full set of checks described in the package documentation. Like http.ServeMux it
// panics on a malformed or conflicting pattern, which is a programming error found at start.
// Handle may be called while the server is running, but patterns that this package registers
// (/healthz, /api/ping, /api/confirm, /api/auth/*, the UI at "/") are taken.
func (s *Server) Handle(pattern string, h http.Handler, opts RouteOpts) {
	if h == nil {
		panic("web: Handle with a nil handler")
	}
	method, _, err := splitPattern(pattern)
	if err != nil {
		panic(err.Error())
	}
	rt := &route{pattern: pattern, method: method, safe: method == http.MethodGet, opts: opts, h: h}
	if _, ok := h.(streamer); ok {
		rt.stream = true
	}
	if opts.NeedsConfirm && rt.safe {
		panic(fmt.Sprintf("web: pattern %q: a GET route cannot need a confirmation; GET must not change state", pattern))
	}
	s.mux.Handle(pattern, s.wrap(rt))
}

// HandleFunc is Handle for a function.
func (s *Server) HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request), opts RouteOpts) {
	s.Handle(pattern, http.HandlerFunc(h), opts)
}

// wrap applies the route-level checks of the envelope around a handler.
func (s *Server) wrap(rt *route) http.Handler {
	maxBody := rt.opts.MaxBody
	if maxBody <= 0 {
		maxBody = DefaultMaxBody
	}
	timeout := rt.opts.WriteTimeout
	if timeout <= 0 {
		timeout = defaultWriteTimeout
	}
	scope := rt.opts.ConfirmScope
	if scope == "" {
		scope = rt.pattern
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info := infoFrom(r.Context())
		if info != nil {
			info.route = rt.pattern
			info.maxBody = maxBody
			if rt.stream && info.release != nil {
				info.release() // a stream is not a request in flight; the hub bounds streams
			}
		}
		if !rt.stream {
			_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(timeout))
		}
		if !rt.safe && !s.checkUnsafe(w, r, rt, maxBody) {
			return
		}
		if rt.opts.NeedsConfirm && !s.RequireConfirm(w, r, scope) {
			return
		}
		rt.h.ServeHTTP(w, r)
	})
}

// checkUnsafe applies the checks for POST, PUT, PATCH and DELETE: the custom header, the content
// type and the body cap. It writes the refusal and returns false when one fails.
func (s *Server) checkUnsafe(w http.ResponseWriter, r *http.Request, rt *route, maxBody int64) bool {
	if r.Header.Get(RequestHeader) != RequestHeaderValue {
		Error(w, http.StatusForbidden, "csrf", "this request must carry the "+RequestHeader+": "+RequestHeaderValue+" header")
		return false
	}
	hasBody := r.Body != nil && r.Body != http.NoBody
	if hasBody {
		if rt.opts.NoBody {
			Error(w, http.StatusBadRequest, "bad_request", "this route takes no request body")
			return false
		}
		if !isJSONContentType(r.Header.Get("Content-Type")) {
			Error(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "the body must be JSON with Content-Type: application/json")
			return false
		}
		if r.ContentLength > maxBody {
			Error(w, http.StatusRequestEntityTooLarge, "body_too_large", "the request body is larger than this route accepts")
			return false
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	}
	return true
}

// isJSONContentType reports whether a Content-Type header names application/json (with at most a
// UTF-8 charset).
func isJSONContentType(v string) bool {
	mt, params, err := mime.ParseMediaType(v)
	if err != nil || mt != "application/json" {
		return false
	}
	for k, val := range params {
		if k != "charset" || !strings.EqualFold(val, "utf-8") {
			return false
		}
	}
	return true
}
