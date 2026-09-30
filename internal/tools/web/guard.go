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

func (e *lookupError) Error() string { return "could not resolve host " + strconv.Quote(e.Host) }
func (e *lookupError) Unwrap() error { return e.Err }

var (
	errBadIP     = errors.New("invalid IP address")
	errEmptyHost = errors.New("empty host")
)

// blockedNets are ranges that net/netip's predicates do not cover.
var blockedNets = func() []struct {
	prefix netip.Prefix
	why    string
} {
	spec := []struct{ cidr, why string }{
		{"0.0.0.0/8", "unspecified"},
		{"100.64.0.0/10", "carrier-grade NAT"},
		{"192.0.0.0/24", "reserved"},
		{"192.0.2.0/24", "reserved (documentation)"},
		{"198.18.0.0/15", "reserved (benchmarking)"},
		{"198.51.100.0/24", "reserved (documentation)"},
		{"203.0.113.0/24", "reserved (documentation)"},
		{"240.0.0.0/4", "reserved"},
		{"168.63.129.16/32", "cloud metadata"}, // Azure wire server
		{"64:ff9b:1::/48", "reserved"},
		{"100::/64", "reserved"},
		{"2001::/23", "reserved"}, // includes Teredo
		{"2001:db8::/32", "reserved (documentation)"},
		{"fec0::/10", "private network"},
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
)

// classify returns why addr must not be fetched, or "" if it is fine.
func classify(addr netip.Addr) string {
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
	switch {
	case addr.IsUnspecified():
		return "unspecified"
	case addr.IsLoopback():
		return "loopback"
	case addr.IsPrivate():
		return "private network"
	case addr.IsLinkLocalUnicast():
		return "link-local (cloud metadata range)"
	case addr.IsMulticast(), addr.IsLinkLocalMulticast(), addr.IsInterfaceLocalMulticast():
		return "multicast"
	}
	for _, n := range blockedNets {
		if n.prefix.Contains(addr) {
			return n.why
		}
	}
	return ""
}

// literalIP interprets host as an IP address if it is one. Besides the normal
// forms it understands the historic inet_aton spellings ("2130706433",
// "0x7f.1", "0177.0.0.1", "127.1") that C resolvers accept and Go's parser
// rejects: relying on whichever resolver happens to be linked in to reject or
// translate them is not a security control. A host that looks numeric but is not
// a valid address is refused outright (numeric hosts are never real domain names).
func literalIP(host string) (addr netip.Addr, isIP bool, err error) {
	if a, perr := netip.ParseAddr(host); perr == nil {
		return a, true, nil
	}
	labels := strings.Split(host, ".")
	if len(labels) == 0 || len(labels) > 4 {
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

// allNumeric reports whether every label is a decimal or 0x-hex number.
func allNumeric(labels []string) bool {
	if len(labels) == 0 {
		return false
	}
	for _, l := range labels {
		if l == "" {
			return false
		}
		digits := l
		if len(l) > 2 && (l[:2] == "0x" || l[:2] == "0X") {
			for _, c := range l[2:] {
				if !isHex(c) {
					return false
				}
			}
			continue
		}
		for _, c := range digits {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}

func isHex(c rune) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

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
