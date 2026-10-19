package web

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/weburl"
)

// SSRF guard.
//
// web_fetch takes URLs from a model, and models read untrusted text: a README,
// an issue, a fetched page can all say "now fetch http://169.254.169.254/...".
// Reaching the loopback interface, the local network or a cloud metadata
// service from the developer's machine must therefore be impossible by default.
//
// Vetting a hostname and then letting the HTTP stack resolve it again is the
// classic DNS-rebinding hole: the name answers with a public address for the
// check and a private one for the connection. So the address that is vetted is
// the address that is dialed: the transport's DialContext resolves the name
// itself, filters out every blocked address and connects to the survivors by
// IP literal. Redirects need no special handling for the same reason: every new
// connection goes through the same dialer.

// blockedError reports a refused address.
type blockedError struct {
	Host   string
	Addr   netip.Addr
	Reason string
}

// Error explains an address block, including the resolving hostname when distinct from the
// address.
func (e *blockedError) Error() string {
	if e.Host == e.Addr.String() || e.Host == "" {
		return fmt.Sprintf("blocked: %s is a %s address", e.Addr, e.Reason)
	}
	return fmt.Sprintf("blocked: %s resolves to %s, a %s address", e.Host, e.Addr, e.Reason)
}

// lookupError reports a name that could not be resolved.
type lookupError struct {
	Host string
	Err  error
}

// Error identifies the hostname whose lookup failed without exposing the underlying resolver
// detail.
func (e *lookupError) Error() string { return "could not resolve host " + strconv.Quote(e.Host) }

// Unwrap exposes the underlying host-resolution error.
func (e *lookupError) Unwrap() error { return e.Err }

var (
	errBadIP     = weburl.ErrBadIP
	errEmptyHost = errors.New("empty host")
)

// classify returns why addr must not be fetched, or "" if it is fine (weburl.BlockedAddr).
func classify(addr netip.Addr) string { return weburl.BlockedAddr(addr) }

// literalIP interprets host as an IP address if it is one (weburl.LiteralIP).
func literalIP(host string) (netip.Addr, bool, error) { return weburl.LiteralIP(host) }

// hostRule is one Config.AllowHosts entry.
type hostRule struct {
	host   string // lower-case, no brackets, no trailing dot
	port   string // "" matches any port
	suffix bool   // "*.example.com": matches subdomains only
}

func parseRule(s string) (hostRule, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return hostRule{}, false
	}
	var r hostRule
	switch {
	case strings.HasPrefix(s, "["): // [::1] or [::1]:8080
		h, p, err := net.SplitHostPort(s)
		if err != nil {
			h, p = strings.Trim(s, "[]"), ""
		}
		r.host, r.port = h, p
	case strings.Count(s, ":") == 1:
		h, p, _ := strings.Cut(s, ":")
		r.host, r.port = h, p
	default:
		r.host = s
	}
	if strings.HasPrefix(r.host, "*.") {
		r.suffix = true
		r.host = r.host[1:] // keep the leading dot: ".example.com"
	}
	r.host = strings.TrimSuffix(r.host, ".")
	return r, r.host != ""
}

// matches enforces an optional port and then checks an exact host or precompiled hostname suffix.
func (r hostRule) matches(host, port string) bool {
	if r.port != "" && r.port != port {
		return false
	}
	if r.suffix {
		return strings.HasSuffix(host, r.host)
	}
	return host == r.host
}

// guard vets and dials addresses.
type guard struct {
	allowPrivate bool
	rules        []hostRule
	// lookup and dial are fields so tests can stage DNS answers and observe
	// exactly which address a connection is made to.
	lookup func(ctx context.Context, host string) ([]netip.Addr, error)
	dial   func(ctx context.Context, network, address string) (net.Conn, error)
}

func newGuard(allowPrivate bool, allow []string) *guard {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	g := &guard{
		allowPrivate: allowPrivate,
		dial:         dialer.DialContext,
		lookup: func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		},
	}
	for _, a := range allow {
		if r, ok := parseRule(a); ok {
			g.rules = append(g.rules, r)
		}
	}
	return g
}

// normalizeHost trims whitespace and a final DNS dot, lowercases the hostname, and applies ASCII
// folding.
func normalizeHost(h string) string {
	return asciiFold(strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), "."))
}

// asciiFold maps the Unicode look-alikes of ASCII that hostname processing
// (IDNA, UTS 46) treats as the real thing: ideographic and fullwidth full stops
// and the fullwidth ASCII block. "127。0。0。1" and "１２７.0.0.1" are the loopback
// address to a browser and to a proxy, so the guard must see them that way
// rather than as an unresolvable name. net/http already applies IDNA to what
// its dialer receives; this covers the paths where it does not (proxies).
func asciiFold(h string) string {
	ascii := true
	for i := 0; i < len(h); i++ {
		if h[i] >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return h
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\u3002' || r == '\uFF0E' || r == '\uFF61': // ideographic, fullwidth, halfwidth ideographic full stops
			return '.'
		case r >= 0xFF01 && r <= 0xFF5E: // fullwidth ASCII
			return r - 0xFEE0
		}
		return r
	}, h)
}

func (g *guard) allowed(host, port string) bool {
	if g.allowPrivate {
		return true
	}
	for _, r := range g.rules {
		if r.matches(host, port) {
			return true
		}
	}
	return false
}

// resolve returns the addresses a connection to host:port may use: the
// resolved set minus every blocked address (unless the host is allowed). It
// fails with a *blockedError when nothing usable is left.
func (g *guard) resolve(ctx context.Context, host, port string) ([]netip.Addr, error) {
	host = normalizeHost(host)
	if host == "" {
		return nil, errEmptyHost
	}
	allowed := g.allowed(host, port)

	var addrs []netip.Addr
	ip, isIP, err := literalIP(host)
	switch {
	case err != nil:
		return nil, fmt.Errorf("%w: %q", err, host)
	case isIP:
		addrs = []netip.Addr{ip}
	default:
		addrs, err = g.lookup(ctx, host)
		if err != nil {
			return nil, &lookupError{Host: host, Err: err}
		}
		if len(addrs) == 0 {
			return nil, &lookupError{Host: host, Err: errors.New("no addresses")}
		}
	}
	if allowed {
		return orderAddrs(addrs), nil
	}
	var kept []netip.Addr
	var first *blockedError
	for _, a := range addrs {
		if why := classify(a); why != "" {
			if first == nil {
				first = &blockedError{Host: host, Addr: a.WithZone(""), Reason: why}
			}
			continue
		}
		kept = append(kept, a)
	}
	if len(kept) == 0 {
		return nil, first
	}
	return orderAddrs(kept), nil
}

// orderAddrs puts IPv4 first (stable): a blackholed IPv6 route would otherwise
// cost a full connect timeout before the working address is tried.
func orderAddrs(in []netip.Addr) []netip.Addr {
	out := append([]netip.Addr(nil), in...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Is4() && !out[j].Is4() })
	return out
}

// dialContext is the transport's DialContext: resolve, vet, then connect to a
// vetted IP literal.
func (g *guard) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	addrs, err := g.resolve(ctx, host, port)
	if err != nil {
		return nil, err
	}
	var firstErr error
	for _, a := range addrs {
		conn, derr := g.dial(ctx, network, net.JoinHostPort(a.String(), port))
		if derr == nil {
			return conn, nil
		}
		if firstErr == nil {
			firstErr = derr
		}
		if ctx.Err() != nil {
			break
		}
	}
	return nil, firstErr
}

// vetOnly is the weaker check used when a request goes through a proxy: the
// proxy resolves the name, so the connection cannot be pinned. Literal
// addresses and locally resolvable names are still refused; a name that does
// not resolve locally is left to the proxy (in proxy-resolves-DNS sandboxes it
// never will). The proxy is operator-configured infrastructure and is expected
// to enforce its own egress policy.
func (g *guard) vetOnly(ctx context.Context, host, port string) error {
	host = normalizeHost(host)
	if host == "" {
		return errEmptyHost
	}
	if g.allowed(host, port) {
		return nil
	}
	ip, isIP, err := literalIP(host)
	var addrs []netip.Addr
	switch {
	case err != nil:
		return fmt.Errorf("%w: %q", err, host)
	case isIP:
		addrs = []netip.Addr{ip}
	default:
		// In a sandbox without DNS this lookup can only fail, so it must not be
		// allowed to stall every request for a resolver timeout.
		lctx, cancel := context.WithTimeout(ctx, vetLookupTimeout)
		defer cancel()
		addrs, err = g.lookup(lctx, host)
		if err != nil {
			return nil
		}
	}
	for _, a := range addrs {
		if why := classify(a); why != "" {
			return &blockedError{Host: host, Addr: a.WithZone(""), Reason: why}
		}
	}
	return nil
}

// vetLookupTimeout bounds the local DNS lookup done in proxy mode.
const vetLookupTimeout = 2 * time.Second
