// Package web provides the web_fetch and web_search tools.
//
// web_fetch reads one URL and returns it as compact, pageable text. It exists
// because models cannot browse but constantly need documentation, issues and API
// responses; its two hard problems are keeping the developer's network safe from
// URLs the model was talked into (see guard.go) and keeping hostile pages from
// costing more than they are worth (size, time and parse-depth limits).
//
// web_search is a thin, pluggable wrapper over a search API; it is registered
// only when a backend is configured.
package web

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/perm"
	"github.com/reee344/sleipnir/internal/tools"
)

// Config configures the web tools. The zero value is safe: private networks are
// blocked, there is no proxy and no search backend.
type Config struct {
	// AllowPrivate disables the SSRF guard entirely. For sandboxes and tests.
	AllowPrivate bool
	// AllowHosts exempts specific hosts from the guard: names ("docs.internal"),
	// IP literals, "host:port" pairs or "*.suffix" wildcards.
	AllowHosts []string
	// Backend powers web_search; the tool is registered only when it is non-nil.
	// BackendFromEnv builds one from the environment.
	Backend Backend
	// Proxy, when set, routes requests it selects through an HTTP(S) proxy
	// (http.ProxyFromEnvironment is the usual value). A proxy resolves names
	// itself, so those requests cannot be pinned to a vetted address: literal
	// addresses and locally resolvable names are still refused, but the guarantee
	// is weaker. Leave nil to connect directly with full pinning.
	Proxy func(*http.Request) (*url.URL, error)

	// Timeout bounds one fetch including redirects and reading the body
	// (default 30s). MaxBody is the largest response accepted (default 10 MiB).
	// CacheTTL (default 15m) and CacheEntries (default 64) size the page cache.
	Timeout      time.Duration
	MaxBody      int64
	CacheTTL     time.Duration
	CacheEntries int
}

// ConfigFromEnv returns a Config wired from the process environment: the search
// backend comes from BackendFromEnv, and requests go through the proxy named by
// HTTPS_PROXY / HTTP_PROXY (honouring NO_PROXY) when one is set. Behind such a
// proxy the connection cannot be pinned to a vetted address (the proxy resolves
// names itself, and sandboxes that force a proxy usually have no DNS of their
// own), so the SSRF guard falls back to refusing literal and locally resolvable
// private addresses and leaves the rest to the proxy's egress policy. Without a
// proxy the guard pins every connection.
func ConfigFromEnv() Config {
	cfg := Config{Backend: BackendFromEnv()}
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		if strings.TrimSpace(os.Getenv(k)) != "" {
			cfg.Proxy = http.ProxyFromEnvironment
			break
		}
	}
	return cfg
}

const (
	userAgent      = "Sleipnir/1.0"
	maxRedirects   = 5
	defaultLimit   = 20000
	maxLimit       = 200000
	maxURLLen      = 8192
	cacheMaxBytes  = 64 << 20
	errorSnippetLn = 500
)

// Register adds web_fetch, and web_search when cfg.Backend is set, to r.
func Register(r *tools.Registry, cfg Config) {
	r.Register(&fetchTool{f: newFetcher(cfg)})
	if cfg.Backend != nil {
		r.Register(&searchTool{b: cfg.Backend})
	}
}

// document is a fetched page after conversion to text. It is immutable once
// cached, so any number of agents may page through the same one.
type document struct {
	url        string // final URL
	requested  string
	status     int
	ctype      string // media type, e.g. "text/html"
	title      string
	text       string
	runes      int
	redirected bool
	crossHost  bool
}

func (d *document) size() int64 {
	return int64(len(d.text)+len(d.title)+len(d.url)+len(d.requested)) + 256
}

type fetcher struct {
	guard   *guard
	client  *http.Client
	cache   *cache
	timeout time.Duration
	maxBody int64
	proxy   func(*http.Request) (*url.URL, error)
}

func newFetcher(cfg Config) *fetcher {
	f := &fetcher{
		guard:   newGuard(cfg.AllowPrivate, cfg.AllowHosts),
		timeout: cfg.Timeout,
		maxBody: cfg.MaxBody,
		proxy:   cfg.Proxy,
	}
	if f.timeout <= 0 {
		f.timeout = 30 * time.Second
	}
	if f.maxBody <= 0 {
		f.maxBody = 10 << 20
	}
	ttl, entries := cfg.CacheTTL, cfg.CacheEntries
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	if entries <= 0 {
		entries = 64
	}
	f.cache = newCache(entries, cacheMaxBytes, ttl)

	newTransport := func() *http.Transport {
		return &http.Transport{
			ForceAttemptHTTP2:      true,
			TLSHandshakeTimeout:    10 * time.Second,
			ResponseHeaderTimeout:  f.timeout,
			IdleConnTimeout:        30 * time.Second,
			MaxIdleConns:           32,
			MaxIdleConnsPerHost:    4,
			MaxResponseHeaderBytes: 1 << 20,
			ExpectContinueTimeout:  time.Second,
			TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
		}
	}
	direct := newTransport()
	direct.DialContext = f.guard.dialContext // resolve, vet, pin
	var rt http.RoundTripper = direct
	if cfg.Proxy != nil {
		proxied := newTransport()
		proxied.Proxy = cfg.Proxy
		proxied.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
		rt = routedTransport{direct: direct, proxied: proxied, proxy: cfg.Proxy}
	}
	f.client = &http.Client{
		Transport: rt,
		// Redirects are followed by hand so that every hop is counted, vetted and
		// reported.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return f
}

// routedTransport sends requests the proxy function selects through the proxy
// and everything else (NO_PROXY hosts) through the pinned direct transport.
type routedTransport struct {
	direct, proxied http.RoundTripper
	proxy           func(*http.Request) (*url.URL, error)
}

func (t routedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if u, err := t.proxy(req); err == nil && u != nil {
		return t.proxied.RoundTrip(req)
	}
	return t.direct.RoundTrip(req)
}

// ------------------------------------------------------------------ the tool

type fetchTool struct{ f *fetcher }

func (*fetchTool) Spec() core.ToolSpec {
	return core.ToolSpec{
		Name: "web_fetch",
		Description: "Fetch a web page or API endpoint over HTTP(S) (GET only) and return it as readable text: " +
			"HTML becomes compact markdown, JSON is pretty-printed, plain text is returned as is. " +
			"Long documents are paged; the trailer gives the offset of the next page. " +
			"Private, loopback and link-local addresses are blocked. Results are cached for 15 minutes. " +
			"Use web_search first when you do not have a URL.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"url":{"type":"string","description":"http or https URL to fetch"},` +
			`"offset":{"type":"integer","description":"Character offset to start from when paging"},` +
			`"limit":{"type":"integer","description":"Maximum characters to return (default 20000)"}},` +
			`"required":["url"]}`),
		ReadOnly: true,
	}
}

func fail(env *tools.Env, format string, args ...any) *tools.Result {
	return env.Finish(fmt.Sprintf(format, args...), true)
}

func (t *fetchTool) Run(ctx context.Context, c *tools.Call) (*tools.Result, error) {
	if c.Env == nil {
		c.Env = &tools.Env{}
	}
	env := c.Env.Defaults()
	f := t.f

	a, err := parseArgs(c.Input)
	if err != nil {
		return fail(env, "web_fetch: %v", err), nil
	}
	known := []string{"url", "offset", "limit"}
	raw, present, err := a.str("url")
	switch {
	case err != nil:
		return fail(env, "web_fetch: %v", err), nil
	case !present || strings.TrimSpace(raw) == "":
		return fail(env, "web_fetch: %v", a.missing("url", known...)), nil
	}
	offset, _, err := a.integer("offset")
	if err != nil {
		return fail(env, "web_fetch: %v", err), nil
	}
	limit, _, err := a.integer("limit")
	if err != nil {
		return fail(env, "web_fetch: %v", err), nil
	}
	if offset < 0 {
		return fail(env, "web_fetch: field \"offset\" must not be negative"), nil
	}
	u, err := parseFetchURL(raw)
	if err != nil {
		return fail(env, "web_fetch: %v", err), nil
	}

	// Permission is checked before the cache is consulted: a cached page was
	// fetched on somebody's authority, not on this agent's.
	summary := u.String()
	if len(summary) > 300 {
		summary = summary[:300] + "…"
	}
	dec := env.Perm.Check(ctx, perm.Request{
		Agent:   env.Agent,
		Role:    env.Role,
		Tool:    "web_fetch",
		Input:   c.Input,
		Summary: "fetch " + summary,
		Network: true,
	})
	if !dec.Allow {
		if dec.Reason != "" {
			return fail(env, "permission denied: %s", dec.Reason), nil
		}
		return fail(env, "permission denied"), nil
	}

	key := cacheKey(u)
	doc, cached := f.cache.get(key, env.Now())
	if !cached {
		var herr *httpStatusError
		doc, err = f.fetch(ctx, u)
		switch {
		case errors.As(err, &herr):
			return fail(env, "web_fetch: %v", herr), nil
		case err != nil:
			return fail(env, "web_fetch: %s", f.describeError(err)), nil
		}
		f.cache.put(key, doc, env.Now())
	}

	text, meta, err := f.render(env, doc, offset, limit)
	if err != nil {
		return fail(env, "web_fetch: %v", err), nil
	}
	res := env.Finish(text, false)
	meta["cached"] = cached
	res.Meta = meta
	return res, nil
}

var schemeLike = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)

// parseFetchURL validates and normalises what the model passed. It is tolerant
// of a missing scheme ("example.com/page") and strict about everything that
// could change where the request goes.
func parseFetchURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) > maxURLLen {
		return nil, fmt.Errorf("the URL is %d characters long; the limit is %d", len(raw), maxURLLen)
	}
	if strings.ContainsAny(raw, "\r\n\t\x00") {
		return nil, errors.New("the URL contains control characters")
	}
	switch {
	case strings.HasPrefix(raw, "//"):
		raw = "https:" + raw
	case strings.Contains(raw, "://"):
	case schemeLike.MatchString(raw) && !startsWithPort(raw[strings.IndexByte(raw, ':')+1:]):
		scheme := raw[:strings.IndexByte(raw, ':')]
		return nil, fmt.Errorf("unsupported URL scheme %q: only http and https are allowed", scheme)
	default:
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		msg := err.Error()
		if len(msg) > 200 {
			msg = msg[:200] + "…"
		}
		return nil, fmt.Errorf("invalid URL: %s", msg)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return nil, fmt.Errorf("unsupported URL scheme %q: only http and https are allowed", u.Scheme)
	}
	if u.Hostname() == "" {
		return nil, errors.New("the URL has no host")
	}
	if u.User != nil {
		return nil, errors.New("URLs with embedded credentials are not supported")
	}
	u.Fragment, u.RawFragment = "", ""
	return u, nil
}

// startsWithPort tells "localhost:8080/x" (host and port) from "mailto:x@y"
// (scheme and path).
func startsWithPort(rest string) bool {
	return rest != "" && rest[0] >= '0' && rest[0] <= '9'
}

func cacheKey(u *url.URL) string {
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host) + u.RequestURI()
}

// ------------------------------------------------------------------ fetching

// httpStatusError is a non-2xx response; its message is what the model sees.
type httpStatusError struct {
	URL     string
	Status  int
	Snippet string
}

func (e *httpStatusError) Error() string {
	msg := fmt.Sprintf("HTTP %d %s for %s", e.Status, http.StatusText(e.Status), e.URL)
	if e.Snippet != "" {
		msg += "\n" + e.Snippet
	}
	return msg
}

type tooLargeError struct{ limit int64 }

func (e *tooLargeError) Error() string {
	return fmt.Sprintf("the response is larger than %s; not fetched", humanBytes(e.limit))
}

func humanBytes(n int64) string {
	if n >= 1<<20 && n%(1<<20) == 0 {
		return fmt.Sprintf("%d MiB", n>>20)
	}
	if n >= 1<<10 {
		return fmt.Sprintf("%d KiB", n>>10)
	}
	return fmt.Sprintf("%d bytes", n)
}

type unsupportedError struct{ ctype string }

func (e *unsupportedError) Error() string {
	return fmt.Sprintf("unsupported content-type %q: web_fetch reads HTML, JSON and text only", e.ctype)
}

func isRedirect(code int) bool {
	switch code {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	}
	return false
}

// fetch performs the GET, following up to maxRedirects redirects by hand.
func (f *fetcher) fetch(ctx context.Context, start *url.URL) (*document, error) {
	ctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()

	cur := start
	crossHost := false
	for hop := 0; ; hop++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, cur.String(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/json;q=0.9,text/*;q=0.8,*/*;q=0.1")
		if f.proxy != nil {
			// Proxied requests cannot be pinned; vet the target ourselves first.
			if pu, perr := f.proxy(req); perr == nil && pu != nil {
				if err := f.guard.vetOnly(ctx, cur.Hostname(), portOf(cur)); err != nil {
					return nil, err
				}
			}
		}
		resp, err := f.client.Do(req)
		if err != nil {
			return nil, err
		}
		if isRedirect(resp.StatusCode) {
			loc := resp.Header.Get("Location")
			io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			if loc == "" {
				return nil, fmt.Errorf("HTTP %d redirect without a Location header from %s", resp.StatusCode, cur)
			}
			if hop >= maxRedirects {
				return nil, fmt.Errorf("too many redirects (more than %d) starting from %s", maxRedirects, start)
			}
			next, err := cur.Parse(loc)
			if err != nil {
				return nil, fmt.Errorf("invalid redirect target %q", clip(loc, 200))
			}
			switch strings.ToLower(next.Scheme) {
			case "http", "https":
			default:
				return nil, fmt.Errorf("redirect to unsupported URL scheme %q", next.Scheme)
			}
			if next.Hostname() == "" {
				return nil, errors.New("redirect target has no host")
			}
			if next.User != nil {
				return nil, errors.New("redirect target carries embedded credentials")
			}
			next.Fragment, next.RawFragment = "", ""
			if !strings.EqualFold(cur.Hostname(), next.Hostname()) {
				crossHost = true
			}
			cur = next
			continue
		}

		doc, err := f.readDocument(resp, cur)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		doc.requested = start.String()
		doc.redirected = cur.String() != start.String()
		doc.crossHost = crossHost
		return doc, nil
	}
}

func portOf(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if strings.EqualFold(u.Scheme, "https") {
		return "443"
	}
	return "80"
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "…"
}

// readDocument turns the terminal response into a document (or an error the
// model can act on).
func (f *fetcher) readDocument(resp *http.Response, final *url.URL) (*document, error) {
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// The body of an error page often says what went wrong (API error JSON).
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		snippet := ""
		if d, err := f.convert(body, resp.Header, final); err == nil {
			snippet = clipRunes(oneLine(d.text), errorSnippetLn)
		}
		return nil, &httpStatusError{URL: final.String(), Status: resp.StatusCode, Snippet: snippet}
	}
	if resp.ContentLength > f.maxBody {
		return nil, &tooLargeError{f.maxBody}
	}
	// One byte past the limit distinguishes "exactly at" from "over"; reading is
	// bounded after decompression, which is what defeats compression bombs.
	body, err := io.ReadAll(io.LimitReader(resp.Body, f.maxBody+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > f.maxBody {
		return nil, &tooLargeError{f.maxBody}
	}
	doc, err := f.convert(body, resp.Header, final)
	if err != nil {
		return nil, err
	}
	doc.url = final.String()
	doc.status = resp.StatusCode
	return doc, nil
}

func clipRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// describeError turns a transport error into one short line for the model.
func (f *fetcher) describeError(err error) string {
	var be *blockedError
	if errors.As(err, &be) {
		return be.Error() + ". Private, loopback and link-local addresses cannot be fetched"
	}
	var le *lookupError
	if errors.As(err, &le) {
		return le.Error()
	}
	var tl *tooLargeError
	if errors.As(err, &tl) {
		return tl.Error()
	}
	var ue *unsupportedError
	if errors.As(err, &ue) {
		return ue.Error()
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Sprintf("the request timed out after %s", fmtDuration(f.timeout))
	case errors.Is(err, context.Canceled):
		return "the request was cancelled"
	}
	var uaErr x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	var certErr x509.CertificateInvalidError
	switch {
	case errors.As(err, &uaErr), errors.As(err, &hostErr), errors.As(err, &certErr):
		return "TLS certificate error: " + unwrapURLError(err).Error()
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "the connection timed out"
	}
	msg := unwrapURLError(err).Error()
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	return msg
}

func unwrapURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

func fmtDuration(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', -1, 64) + "s"
}

// ------------------------------------------------------------------ rendering

// render pages doc for the model: a one-line header (final URL, status, type,
// which characters follow, whether a redirect crossed hosts), the text, and a
// trailer telling the model where to continue. The page is sized so that
// Env.Finish never has to truncate it: truncating a page in the middle would
// silently lose text that the trailer says was delivered.
func (f *fetcher) render(env *tools.Env, doc *document, offset, limit int64) (string, map[string]any, error) {
	total := int64(doc.runes)
	if offset > total || (offset == total && total > 0) {
		return "", nil, fmt.Errorf("offset %d is beyond the end of the document (%d characters)", offset, total)
	}
	if limit <= 0 {
		limit = defaultLimit
	}
	limit = min(limit, maxLimit)

	// Everything except the body has a size too.
	title := ""
	if doc.title != "" && !strings.Contains(firstLines(doc.text, 3), doc.title) {
		title = "Title: " + clipRunes(doc.title, 200) + "\n"
	}
	head := func(start, end int64) string {
		var sb strings.Builder
		fmt.Fprintf(&sb, "[fetched %s | HTTP %d %s | characters %d-%d of %d", doc.url, doc.status, doc.ctype, start, end, total)
		if doc.redirected {
			fmt.Fprintf(&sb, " | redirected from %s", doc.requested)
			if doc.crossHost {
				sb.WriteString(" (different host)")
			}
		}
		sb.WriteString("]\n")
		return sb.String()
	}
	overhead := len(head(offset, total)) + len(title) + 200 // 200: blank line + trailer
	maxBytes := len(doc.text)
	if max := env.Limits.MaxOutputChars; max > 0 {
		maxBytes = max - overhead
		if maxBytes < 500 {
			maxBytes = 500
		}
	}

	startB := byteOffset(doc.text, doc.runes, offset)
	endB := pageEnd(doc.text, startB, limit, maxBytes)
	end := offset + int64(utf8.RuneCountInString(doc.text[startB:endB]))
	body := doc.text[startB:endB]
	if end < total {
		// Pages end at paragraph boundaries, so they usually end in blank lines;
		// the trailer supplies its own separation. The trimmed newlines still
		// count towards the offsets.
		if trimmed := strings.TrimRight(body, "\n"); trimmed != "" {
			body = trimmed
		}
	}

	var sb strings.Builder
	sb.WriteString(head(offset, end))
	sb.WriteString(title)
	sb.WriteByte('\n')
	if body == "" {
		sb.WriteString("[empty page]")
	} else {
		sb.WriteString(body)
	}
	if end < total {
		fmt.Fprintf(&sb, "\n\n[showing characters %d-%d of %d; call web_fetch again with offset=%d to continue]", offset, end, total, end)
	}
	meta := map[string]any{
		"url":          doc.url,
		"status":       doc.status,
		"content_type": doc.ctype,
		"chars":        total,
		"start":        offset,
		"end":          end,
		"redirected":   doc.redirected,
		"cross_host":   doc.crossHost,
	}
	return sb.String(), meta, nil
}

// byteOffset returns the byte index of the n-th character of s, given that s
// holds runes characters in total.
func byteOffset(s string, runes int, n int64) int {
	if n <= 0 {
		return 0
	}
	if int64(runes) == int64(len(s)) { // pure ASCII: characters are bytes
		return int(min(n, int64(len(s))))
	}
	var i int
	for ; n > 0 && i < len(s); n-- {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return i
}

// pageEnd returns where a page starting at byte start ends: at most limit
// characters and maxBytes bytes, cut at a line (or word) boundary near the end
// when that costs little, and never inside a character.
func pageEnd(s string, start int, limit int64, maxBytes int) int {
	end := start
	for n := int64(0); end < len(s) && n < limit && end-start < maxBytes; n++ {
		_, size := utf8.DecodeRuneInString(s[end:])
		if end-start+size > maxBytes {
			break
		}
		end += size
	}
	if end >= len(s) {
		return len(s)
	}
	span := end - start
	if i := strings.LastIndexByte(s[start:end], '\n'); i >= 0 && i > span*85/100 {
		return start + i + 1
	}
	if i := strings.LastIndexByte(s[start:end], ' '); i >= 0 && i > span*95/100 {
		return start + i + 1
	}
	return end
}
