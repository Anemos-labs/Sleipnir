package weburl

import (
	"errors"
	"net/netip"
	"strconv"
	"strings"
)

// ErrBadIP reports a host that looks numeric but is not a valid IP address (LiteralIP).
var ErrBadIP = errors.New("invalid IP address")

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

// BlockedAddr returns why addr must not be fetched (a loopback, private, link-local, multicast, unspecified, reserved or cloud
// metadata address, also when an IPv6 form carries such an IPv4 address), or "" for a public address.
func BlockedAddr(addr netip.Addr) string {
	addr = addr.WithZone("")
	if addr.Is4In6() {
		addr = addr.Unmap()
	}
	if addr.Is6() {
		// Forms that smuggle an IPv4 address inside an IPv6 one: judge the IPv4.
		b := addr.As16()
		switch {
		case nat64Prefix.Contains(addr):
			return BlockedAddr(netip.AddrFrom4([4]byte(b[12:16])))
		case sixToFour.Contains(addr):
			return BlockedAddr(netip.AddrFrom4([4]byte(b[2:6])))
		case v4Compat.Contains(addr) && !addr.IsUnspecified() && addr != netip.IPv6Loopback():
			return BlockedAddr(netip.AddrFrom4([4]byte(b[12:16])))
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

// LiteralIP interprets host as an IP address if it is one. Besides the normal
// forms it understands the historic inet_aton spellings ("2130706433",
// "0x7f.1", "0177.0.0.1", "127.1") that C resolvers accept and Go's parser
// rejects: relying on whichever resolver happens to be linked in to reject or
// translate them is not a security control. A host that looks numeric but is not
// a valid address is refused outright (numeric hosts are never real domain names).
func LiteralIP(host string) (addr netip.Addr, isIP bool, err error) {
	if a, perr := netip.ParseAddr(host); perr == nil {
		return a, true, nil
	}
	labels := strings.Split(host, ".")
	if len(labels) == 0 || len(labels) > 4 {
		if allNumeric(labels) {
			return netip.Addr{}, false, ErrBadIP
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
			return netip.Addr{}, false, ErrBadIP
		}
		vals[i] = v
	}
	var n uint64
	last := len(vals) - 1
	for i := 0; i < last; i++ {
		if vals[i] > 255 {
			return netip.Addr{}, false, ErrBadIP
		}
		n |= vals[i] << (8 * uint(3-i))
	}
	if vals[last] >= uint64(1)<<(8*uint(4-last)) {
		return netip.Addr{}, false, ErrBadIP
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

// isHex recognizes ASCII hexadecimal digits in either case.
func isHex(c rune) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}
