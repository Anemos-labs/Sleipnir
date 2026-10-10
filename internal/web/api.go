package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/harden"
)

// DefaultMaxBody is the request body cap of a route that does not set RouteOpts.MaxBody.
const DefaultMaxBody int64 = 64 << 10

// jsonType is the Content-Type of every JSON response.
const jsonType = "application/json; charset=utf-8"

// maxErrorMessage bounds the message of an error body, in bytes.
const maxErrorMessage = 512

// errorBody is the uniform shape of every error response.
type errorBody struct {
	Error  string `json:"error"`
	Code   string `json:"code"`
	Detail any    `json:"detail,omitempty"`
}

// Error writes the uniform error response: status, then {"error": msg, "code": code} as JSON,
// uncacheable. code is a stable machine-readable identifier (doc.go lists the ones the envelope
// uses); msg is for people and must not carry secrets, paths of other users, or anything a
// request supplied that has not been bounded. A message longer than 512 bytes is cut.
func Error(w http.ResponseWriter, status int, code, msg string) {
	ErrorDetail(w, status, code, msg, nil)
}

// ErrorDetail is Error with structured data for the caller: {"error": msg, "code": code, "detail": detail}. The detail is what a
// client needs to act on the refusal (a trust challenge, a retry delay); it is encoded like every other body, with the same
// escaping, and must hold no secret. A nil detail, or one that cannot be encoded, is left out and the plain error is sent.
func ErrorDetail(w http.ResponseWriter, status int, code, msg string, detail any) {
	if len(msg) > maxErrorMessage {
		msg = strings.ToValidUTF8(msg[:maxErrorMessage], "") + "..."
	}
	b, err := json.Marshal(errorBody{Error: msg, Code: code, Detail: detail})
	if err != nil {
		b, _ = json.Marshal(errorBody{Error: msg, Code: code})
	}
	h := w.Header()
	h.Set("Content-Type", jsonType)
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Length", fmt.Sprint(len(b)+1))
	w.WriteHeader(status)
	_, _ = w.Write(append(b, '\n'))
}

// WriteJSON writes v as JSON with the given status. The value is encoded before any header is
// sent, so that an encoding failure produces a clean 500 (and is returned) instead of a truncated
// body. Strings are HTML-escaped (the angle brackets and the ampersand become unicode escapes) so that text that came
// from a repository or a model stays inert even if a client misreads the content type. The
// response is marked uncacheable.
func WriteJSON(w http.ResponseWriter, status int, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		Error(w, http.StatusInternalServerError, "internal", "could not encode the response")
		return err
	}
	b = append(b, '\n')
	h := w.Header()
	h.Set("Content-Type", jsonType)
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Length", fmt.Sprint(len(b)))
	w.WriteHeader(status)
	_, _ = w.Write(b)
	return nil
}

// DecodeJSON reads the request body into dst, strictly: exactly one JSON value, no unknown
// fields, within the route's body cap (RouteOpts.MaxBody, else DefaultMaxBody). On any problem
// it writes the error response (400 bad_json, 413 body_too_large) and returns false; the handler
// returns without writing anything more. dst should be a pointer to a struct; fields the handler
// does not expect are an error, which is what makes a typo or an attack visible. The decoder
// bounds nesting (10000 levels in encoding/json) and the cap bounds everything else.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	limit := DefaultMaxBody
	if info := infoFrom(r.Context()); info != nil && info.maxBody > 0 {
		limit = info.maxBody
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeDecodeError(w, err)
		return false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		if isTooLarge(err) {
			writeDecodeError(w, err)
			return false
		}
		Error(w, http.StatusBadRequest, "bad_json", "the body must hold exactly one JSON value")
		return false
	}
	return true
}

// isTooLarge reports whether err is the body cap being exceeded.
func isTooLarge(err error) bool {
	var tooBig *http.MaxBytesError
	return errors.As(err, &tooBig)
}

// writeDecodeError turns a decoding error into the response for it, without echoing the body.
func writeDecodeError(w http.ResponseWriter, err error) {
	var (
		syntax  *json.SyntaxError
		typeErr *json.UnmarshalTypeError
	)
	switch {
	case isTooLarge(err):
		Error(w, http.StatusRequestEntityTooLarge, "body_too_large", "the request body is larger than this route accepts")
	case errors.Is(err, io.EOF):
		Error(w, http.StatusBadRequest, "bad_json", "a JSON body is required")
	case errors.Is(err, io.ErrUnexpectedEOF):
		Error(w, http.StatusBadRequest, "bad_json", "the JSON body is incomplete")
	case errors.As(err, &syntax):
		Error(w, http.StatusBadRequest, "bad_json", fmt.Sprintf("malformed JSON at byte %d", syntax.Offset))
	case errors.As(err, &typeErr):
		Error(w, http.StatusBadRequest, "bad_json", "field "+quoteShort(typeErr.Field)+" has the wrong type")
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		Error(w, http.StatusBadRequest, "bad_json", "unknown field "+quoteShort(strings.Trim(strings.TrimPrefix(err.Error(), "json: unknown field "), `"`)))
	default:
		Error(w, http.StatusBadRequest, "bad_json", "the JSON body is not valid for this route")
	}
}

// quoteShort quotes s for a message, cut at 64 bytes.
func quoteShort(s string) string {
	if len(s) > 64 {
		s = strings.ToValidUTF8(s[:64], "") + "..."
	}
	return fmt.Sprintf("%q", s)
}

// ---- request information and logging --------------------------------------------------------

// infoKey is the context key of the per-request record.
type infoKey struct{}

// reqInfo is what the envelope learns about a request and hands to later steps and to helpers.
type reqInfo struct {
	id         string
	srv        *Server
	route      string
	maxBody    int64
	p          *principal
	needBearer bool
	release    func()
}

// infoFrom returns the per-request record, or nil for a request that did not come through the
// envelope.
func infoFrom(ctx context.Context) *reqInfo {
	info, _ := ctx.Value(infoKey{}).(*reqInfo)
	return info
}

// newRequestID returns a short random identifier for correlating a log line with a response
// (it is sent as X-Request-Id).
func newRequestID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "000000000000"
	}
	return hex.EncodeToString(b[:])
}

// RequestID returns the identifier the envelope gave the request (also sent as X-Request-Id), or
// "" when the request did not come through the envelope.
func RequestID(r *http.Request) string {
	if info := infoFrom(r.Context()); info != nil {
		return info.id
	}
	return ""
}

// Logf writes an operational message about a request to the server's log. The line is tagged with
// the method, the route pattern and the request id, never with the query string, headers or body;
// the run token, bearer and cookie values, confirmation ids and the provider keys the process holds
// are masked wherever they appear in the message; control characters are replaced so that a
// request cannot forge log lines. Messages should still describe what happened, not what was sent.
func Logf(r *http.Request, format string, args ...any) {
	info := infoFrom(r.Context())
	if info == nil || info.srv == nil {
		return
	}
	route := info.route
	if route == "" {
		route = "-"
	}
	info.srv.Logf("%s %s [%s] %s", r.Method, route, info.id, fmt.Sprintf(format, args...))
}

// Logf writes an operational message to the log the server was configured with, masking
// credentials as the package-level Logf does.
func (s *Server) Logf(format string, args ...any) {
	if s.cfg.Logf == nil {
		return
	}
	s.cfg.Logf("%s", s.redact(fmt.Sprintf(format, args...)))
}

// redactPatterns match credentials by shape, for the values the server cannot know.
var redactPatterns = []struct {
	re   *regexp.Regexp
	with string
}{
	{regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]{8,}`), "${1}[redacted]"},
	{regexp.MustCompile(`(?i)(token=)[^&\s"']+`), "${1}[redacted]"},
	{regexp.MustCompile(`(?i)(` + CookieName + `=)[^;\s"']+`), "${1}[redacted]"},
	{regexp.MustCompile(`(?i)(x-confirm:?\s*)[A-Za-z0-9_-]{8,}`), "${1}[redacted]"},
}

// maxLogLine bounds one log message, in bytes.
const maxLogLine = 4096

// redact masks the credentials the server knows (the run token, the provider keys held by the
// process) and those it recognises by shape, replaces control characters and bounds the length.
func (s *Server) redact(msg string) string {
	if t := s.auth.currentToken(); len(t) >= 8 {
		msg = strings.ReplaceAll(msg, t, "[redacted]")
	}
	for _, name := range harden.Held() {
		if v, ok := harden.LookupSecret(name); ok && len(v) >= 8 {
			msg = strings.ReplaceAll(msg, v, "[redacted]")
		}
	}
	for _, p := range redactPatterns {
		msg = p.re.ReplaceAllString(msg, p.with)
	}
	if len(msg) > maxLogLine {
		msg = strings.ToValidUTF8(msg[:maxLogLine], "") + "..."
	}
	return strings.Map(func(r rune) rune {
		if r == utf8.RuneError || (r < 0x20 && r != '\t') || r == 0x7f {
			return ' '
		}
		return r
	}, msg)
}
