package mcp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SSRF guard for the HTTP transports.
//
// The URL of a remote MCP server comes from configuration, and configuration
// can come from a repository. A definition pointing at http://169.254.169.254/
// (the cloud metadata service), at localhost, or at an internal admin API would
// otherwise turn "connect to an MCP server" into "make the developer's machine
// POST to an address of the attacker's choosing". And the endpoint of a legacy
// SSE server is chosen by the server itself.
//
// So, as in the web fetch tool: the address that is vetted is the address that
// is dialed. The transport's DialContext resolves the name itself, drops every
// address the policy refuses and connects to a survivor by IP literal, which
// leaves no gap for DNS rebinding (a name that answers "public" to a check and
// "private" to the connection). Redirects need no special handling for the
// same reason: every new connection passes through the same dialer.
//
// The policy has three classes:
//
//   - public addresses are always allowed;
//   - private ones (loopback, RFC 1918, unique-local, carrier-grade NAT such as
//     Tailscale, the unspecified address) are refused unless the server is
//     configured AllowPrivate: that is what a local development server needs;
//   - the rest is refused even then: link-local (the cloud metadata range),
//     multicast, reserved and documentation ranges, and the metadata addresses
//     of the big clouds that are not link-local. Nobody who writes allow_private
//     for a local server means the metadata service.

type addrClass int

const (
	classPublic addrClass = iota
	classPrivate
	classNever
)

type blockedError struct {
	Host   string
	Addr   netip.Addr
	Reason string
}

// Error describes a blocked address and includes the original hostname when it differs from the
// resolved address.
func (e *blockedError) Error() string {
	if e.Host == e.Addr.String() || e.Host == "" {
		return fmt.Sprintf("blocked: %s is a %s address", e.Addr, e.Reason)
	}
	return fmt.Sprintf("blocked: %s resolves to %s, a %s address", e.Host, e.Addr, e.Reason)
}

// Unwrap exposes ErrBlocked so callers can recognize network-policy refusals.
func (e *blockedError) Unwrap() error { return ErrBlocked }

var (
	errBadIP     = errors.New("invalid IP address")
	errEmptyHost = errors.New("empty host")
)

// neverNets are ranges refused even for private-friendly servers.
var neverNets = func() []struct {
	prefix netip.Prefix
	why    string
} {
	spec := []struct{ cidr, why string }{
		{"0.0.0.0/8", "reserved"}, // 0.0.0.0 itself is handled as private
		{"100.100.100.200/32", "cloud metadata"},
		{"168.63.129.16/32", "cloud metadata"},
		{"192.0.0.0/24", "reserved"},
		{"192.0.2.0/24", "reserved (documentation)"},
		{"198.18.0.0/15", "reserved (benchmarking)"},
		{"198.51.100.0/24", "reserved (documentation)"},
		{"203.0.113.0/24", "reserved (documentation)"},
		{"240.0.0.0/4", "reserved"},
		{"fd00:ec2::254/128", "cloud metadata"},
		{"64:ff9b:1::/48", "reserved"},
		{"100::/64", "reserved"},
		{"2001::/23", "reserved"},
		{"2001:db8::/32", "reserved (documentation)"},
	}
	out := make([]struct {
		prefix netip.Prefix
		why    string
	}, len(spec))
	for i, s := range spec {
		out[i].prefix = netip.MustParsePrefix(s.cidr)
		out[i].why = s.why
	}
	return out
}()

var (
	nat64Prefix = netip.MustParsePrefix("64:ff9b::/96")
	sixToFour   = netip.MustParsePrefix("2002::/16")
	v4Compat    = netip.MustParsePrefix("::/96")
	cgnat       = netip.MustParsePrefix("100.64.0.0/10")
	siteLocal6  = netip.MustParsePrefix("fec0::/10")
)

// classify returns the policy class of addr and a short reason.
func classify(addr netip.Addr) (addrClass, string) {
	addr = addr.WithZone("")
	if addr.Is4In6() {
		addr = addr.Unmap()
	}
	if addr.Is6() {
		// Forms that smuggle an IPv4 address inside an IPv6 one: judge the IPv4.
		b := addr.As16()
		switch {
		case nat64Prefix.Contains(addr):
			return classify(netip.AddrFrom4([4]byte(b[12:16])))
		case sixToFour.Contains(addr):
			return classify(netip.AddrFrom4([4]byte(b[2:6])))
		case v4Compat.Contains(addr) && !addr.IsUnspecified() && addr != netip.IPv6Loopback():
			return classify(netip.AddrFrom4([4]byte(b[12:16])))
		}
	}
	if addr.IsUnspecified() { // 0.0.0.0 reaches the local machine; the rest of 0/8 is reserved
		return classPrivate, "unspecified"
	}
	for _, n := range neverNets { // metadata addresses first: some sit inside private ranges
		if n.prefix.Contains(addr) {
			return classNever, n.why
		}
	}
	switch {
	case addr.IsLoopback():
		return classPrivate, "loopback"
	case addr.IsPrivate():
		return classPrivate, "private network"
	case cgnat.Contains(addr):
		return classPrivate, "carrier-grade NAT"
	case siteLocal6.Contains(addr):
		return classPrivate, "private network"
	case addr.IsLinkLocalUnicast():
		return classNever, "link-local (cloud metadata range)"
	case addr.IsMulticast(), addr.IsLinkLocalMulticast(), addr.IsInterfaceLocalMulticast():
		return classNever, "multicast"
	}
	return classPublic, ""
}

// literalIP interprets host as an IP address if it is one. Besides the normal
// forms it understands the historic inet_aton spellings ("2130706433",
// "0x7f.1", "0177.0.0.1", "127.1") that C resolvers accept and Go's parser
// rejects: relying on whichever resolver happens to be linked in is not a
// security control. A host that looks numeric but is not a valid address is
// refused outright (numeric hosts are never real domain names).
func literalIP(host string) (addr netip.Addr, isIP bool, err error) {
	if a, perr := netip.ParseAddr(host); perr == nil {
		return a, true, nil
	}
	labels := strings.Split(host, ".")
	if len(labels) > 4 {
		if allNumeric(labels) {
			return netip.Addr{}, false, errBadIP
		}
		return netip.Addr{}, false, nil
	}
	if !allNumeric(labels) {
		return netip.Addr{}, false, nil
	}
	vals := make([]uint64, len(labels))
	for i, l := range labels {
		v, perr := strconv.ParseUint(l, 0, 32)
		if perr != nil {
			return netip.Addr{}, false, errBadIP
		}
		vals[i] = v
	}
	var n uint64
	last := len(vals) - 1
	for i := 0; i < last; i++ {
		if vals[i] > 255 {
			return netip.Addr{}, false, errBadIP
		}
		n |= vals[i] << (8 * uint(3-i))
	}
	if vals[last] >= uint64(1)<<(8*uint(4-last)) {
		return netip.Addr{}, false, errBadIP
	}
	n |= vals[last]
	return netip.AddrFrom4([4]byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}), true, nil
}

func allNumeric(labels []string) bool {
	if len(labels) == 0 {
		return false
	}
	for _, l := range labels {
		if l == "" {
			return false
		}
		if len(l) > 2 && (l[:2] == "0x" || l[:2] == "0X") {
			for _, c := range l[2:] {
				if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
					return false
				}
			}
			continue
		}
		for _, c := range l {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}

// normalizeHost lower-cases, drops a trailing dot, and folds the Unicode
// look-alikes of ASCII that hostname processing (IDNA) treats as the real
// thing: "127" + ideographic full stops + "0.0.1" is the loopback address to a browser.
func normalizeHost(h string) string {
	h = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
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
		case r == 0x3002 || r == 0xFF0E || r == 0xFF61: // ideographic, fullwidth, halfwidth ideographic full stops
			return '.'
		case r >= 0xFF01 && r <= 0xFF5E:
			return r - 0xFEE0
		}
		return r
	}, h)
}

// guard vets and dials addresses.
type guard struct {
	allowPrivate bool
	// lookup and dial are fields so tests can stage DNS answers and observe
	// exactly which address a connection is made to.
	lookup func(ctx context.Context, host string) ([]netip.Addr, error)
	dial   func(ctx context.Context, network, address string) (net.Conn, error)
}

// newGuard initializes DNS resolution and TCP dialing with fixed timeouts and the requested
// private-network policy.
func newGuard(allowPrivate bool) *guard {
	d := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	return &guard{
		allowPrivate: allowPrivate,
		dial:         d.DialContext,
		lookup: func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		},
	}
}

// usable filters addrs by policy. It returns the survivors, or the first
// refusal when none survive.
func (g *guard) usable(host string, addrs []netip.Addr) ([]netip.Addr, error) {
	var kept []netip.Addr
	var first *blockedError
	for _, a := range addrs {
		class, why := classify(a)
		if class == classNever || class == classPrivate && !g.allowPrivate {
			if first == nil {
				first = &blockedError{Host: host, Addr: a.WithZone(""), Reason: why}
			}
			continue
		}
		kept = append(kept, a)
	}
	if len(kept) == 0 {
		if first == nil {
			return nil, errors.New("no addresses to connect to")
		}
		return nil, first
	}
	// IPv4 first (stable): a blackholed IPv6 route would otherwise cost a full
	// connect timeout before the working address is tried.
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].Is4() && !kept[j].Is4() })
	return kept, nil
}

func (g *guard) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	host = normalizeHost(host)
	if host == "" {
		return nil, errEmptyHost
	}
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
			return nil, fmt.Errorf("could not resolve host %q", host)
		}
		if len(addrs) == 0 {
			return nil, fmt.Errorf("could not resolve host %q: no addresses", host)
		}
	}
	return g.usable(host, addrs)
}

// dialContext is the transport's DialContext: resolve, vet, then connect to a
// vetted IP literal.
func (g *guard) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	addrs, err := g.resolve(ctx, host)
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
// not resolve locally is left to the proxy (in sandboxes where only the proxy
// resolves DNS it never will). The proxy is operator-configured infrastructure
// and is expected to enforce its own egress policy.
func (g *guard) vetOnly(ctx context.Context, host string) error {
	host = normalizeHost(host)
	if host == "" {
		return errEmptyHost
	}
	ip, isIP, err := literalIP(host)
	var addrs []netip.Addr
	switch {
	case err != nil:
		return fmt.Errorf("%w: %q", err, host)
	case isIP:
		addrs = []netip.Addr{ip}
	default:
		if addrs, err = g.lookup(ctx, host); err != nil {
			return nil
		}
	}
	if len(addrs) == 0 {
		return nil // nothing to judge; the proxy will resolve
	}
	_, err = g.usable(host, addrs)
	return err
}

// NetOptions is how the HTTP transports reach the network.
type NetOptions struct {
	// Dial replaces the built-in address guard as the transport's dialer. The
	// caller then owns the SSRF policy (Sleipnir passes the same guard its web
	// tools use); AllowPrivate is ignored for address decisions but still
	// decides whether cleartext http:// is acceptable.
	Dial func(ctx context.Context, network, address string) (net.Conn, error)
	// AllowPrivate lets the built-in guard connect to private and loopback
	// addresses (never link-local, metadata or reserved ones).
	AllowPrivate bool
	// Proxy routes requests through a proxy. It is nil by default: the process
	// environment (HTTP_PROXY) is not consulted implicitly. With a proxy the
	// destination cannot be pinned, so only a pre-check (vetOnly) applies to it.
	Proxy func(*http.Request) (*url.URL, error)

	// test seams
	lookup func(ctx context.Context, host string) ([]netip.Addr, error)
	dial   func(ctx context.Context, network, address string) (net.Conn, error)
}

// newHTTPClient builds the client of one HTTP transport. It never follows a
// redirect to another origin: net/http strips Authorization on cross-domain
// redirects but forwards every other header, which is exactly where an
// X-Api-Key lives.
func newHTTPClient(n NetOptions) *http.Client {
	g := newGuard(n.AllowPrivate)
	if n.lookup != nil {
		g.lookup = n.lookup
	}
	if n.dial != nil {
		g.dial = n.dial
	}
	tr := &http.Transport{
		ForceAttemptHTTP2:      true,
		MaxIdleConns:           8,
		MaxIdleConnsPerHost:    8,
		IdleConnTimeout:        90 * time.Second,
		TLSHandshakeTimeout:    15 * time.Second,
		ExpectContinueTimeout:  time.Second,
		DisableCompression:     true, // streams must not be buffered by a decompressor
		MaxResponseHeaderBytes: 64 << 10,
	}
	switch {
	case n.Proxy != nil:
		tr.DialContext = g.dial
		tr.Proxy = func(r *http.Request) (*url.URL, error) {
			if err := g.vetOnly(r.Context(), r.URL.Hostname()); err != nil {
				return nil, err
			}
			return n.Proxy(r)
		}
	case n.Dial != nil:
		tr.DialContext = n.Dial
	default:
		tr.DialContext = g.dialContext
	}
	return &http.Client{
		Transport: tr,
		// No overall Timeout: responses are streams that legitimately last as long
		// as a tool runs. Per-call deadlines come from contexts.
		CheckRedirect: sameOriginRedirects,
	}
}

// sameOriginRedirects follows at most three redirects, and only within the
// origin (scheme, host, port) of the original request. Anything else stops at
// the redirect response, which the transport reports.
func sameOriginRedirects(req *http.Request, via []*http.Request) error {
	if len(via) > 3 || !sameOrigin(via[0].URL, req.URL) {
		return http.ErrUseLastResponse
	}
	return nil
}

// sameOrigin compares URL schemes case-insensitively and compares normalized hosts and effective
// ports.
func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) &&
		normalizeHost(a.Hostname()) == normalizeHost(b.Hostname()) &&
		effectivePort(a) == effectivePort(b)
}

// effectivePort returns an explicit URL port or defaults to 443 for HTTPS and 80 otherwise.
func effectivePort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if strings.EqualFold(u.Scheme, "https") {
		return "443"
	}
	return "80"
}

// checkTransportURL applies the scheme rule: credentials and tool traffic must
// not cross the network in clear text. http:// is for local servers, which is
// what AllowPrivate declares.
func checkTransportURL(u *url.URL, allowPrivate bool) error {
	if u.Scheme == "http" && !allowPrivate {
		return errors.New("plain http:// is only allowed for local servers (set allow_private); use https://")
	}
	return nil
}

// transportError reduces a net/http error to its cause. *url.Error quotes the
// full request URL, and remote MCP URLs can carry a token in the path or query.
func transportError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		if errors.Is(ue.Err, context.Canceled) || errors.Is(ue.Err, context.DeadlineExceeded) {
			return ue.Err
		}
		return fmt.Errorf("%s: %w", strings.ToLower(ue.Op), ue.Err)
	}
	return err
}

// netError is transportError plus text hygiene: the message is sanitised and
// secrets are masked, and the error chain is kept.
func netError(r *redactor, err error) error {
	te := transportError(err)
	msg := r.apply(cleanText(te.Error()))
	return &redactedError{msg: msg, err: te}
}
