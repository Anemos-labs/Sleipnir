package web

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		addr   string
		reason string // "" means allowed; otherwise a substring of the reason
	}{
		// Public addresses, including ones right next to blocked ranges.
		{"8.8.8.8", ""}, {"1.1.1.1", ""}, {"93.184.216.34", ""}, {"2606:4700:4700::1111", ""},
		{"172.15.255.255", ""}, {"172.32.0.1", ""}, {"100.63.255.255", ""}, {"100.128.0.1", ""},
		{"192.167.255.255", ""}, {"192.169.0.1", ""}, {"169.253.255.255", ""}, {"169.255.0.1", ""},
		{"11.0.0.1", ""}, {"9.255.255.255", ""}, {"126.255.255.255", ""}, {"128.0.0.1", ""},
		{"223.255.255.255", ""}, {"198.17.255.255", ""}, {"198.20.0.1", ""}, {"2a00:1450:4001::1", ""},
		// Loopback.
		{"127.0.0.1", "loopback"}, {"127.255.255.254", "loopback"}, {"127.1.2.3", "loopback"}, {"::1", "loopback"},
		// RFC 1918 and unique-local.
		{"10.0.0.1", "private"}, {"10.255.255.255", "private"}, {"172.16.0.1", "private"}, {"172.31.255.255", "private"},
		{"192.168.0.1", "private"}, {"192.168.255.255", "private"}, {"fc00::1", "private"}, {"fd12:3456::1", "private"},
		{"fd00:ec2::254", "private"}, {"fec0::1", "private"},
		// Link-local, including the cloud metadata address.
		{"169.254.169.254", "link-local"}, {"169.254.0.1", "link-local"}, {"fe80::1", "link-local"}, {"fe80::1%eth0", "link-local"},
		{"168.63.129.16", "cloud metadata"},
		// CGNAT (includes Alibaba's metadata address).
		{"100.64.0.1", "carrier-grade"}, {"100.100.100.200", "carrier-grade"}, {"100.127.255.255", "carrier-grade"},
		// Unspecified, multicast, reserved.
		{"0.0.0.0", "unspecified"}, {"::", "unspecified"}, {"0.1.2.3", "unspecified"},
		{"224.0.0.1", "multicast"}, {"239.255.255.255", "multicast"}, {"ff02::1", "multicast"}, {"ff05::2", "multicast"},
		{"255.255.255.255", "reserved"}, {"240.0.0.1", "reserved"}, {"192.0.0.1", "reserved"}, {"192.0.2.1", "reserved"},
		{"198.18.0.1", "reserved"}, {"198.19.255.255", "reserved"}, {"198.51.100.1", "reserved"}, {"203.0.113.1", "reserved"},
		{"2001:db8::1", "reserved"}, {"2001::1", "reserved"}, {"100::1", "reserved"},
		// IPv4 hidden inside IPv6: judged by the IPv4 address.
		{"::ffff:127.0.0.1", "loopback"}, {"::ffff:10.1.2.3", "private"}, {"::ffff:169.254.169.254", "link-local"},
		{"::ffff:8.8.8.8", ""},
		{"64:ff9b::7f00:1", "loopback"}, {"64:ff9b::a00:1", "private"}, {"64:ff9b::808:808", ""},
		{"2002:7f00:1::", "loopback"}, {"2002:a9fe:a9fe::", "link-local"}, {"2002:808:808::", ""},
		{"::7f00:1", "loopback"}, {"::a00:1", "private"}, {"::808:808", ""},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			got := classify(netip.MustParseAddr(tt.addr))
			switch {
			case tt.reason == "" && got != "":
				t.Errorf("classify(%s) = %q, want allowed", tt.addr, got)
			case tt.reason != "" && !strings.Contains(got, tt.reason):
				t.Errorf("classify(%s) = %q, want a reason containing %q", tt.addr, got, tt.reason)
			}
		})
	}
}

func TestLiteralIP(t *testing.T) {
	tests := []struct {
		host string
		want string // address, "" for "not an IP", "bad" for a refused numeric host
	}{
		{"127.0.0.1", "127.0.0.1"},
		{"::1", "::1"},
		{"fe80::1%eth0", "fe80::1%eth0"},
		{"2130706433", "127.0.0.1"},
		{"0x7f000001", "127.0.0.1"},
		{"0x7f.1", "127.0.0.1"},
		{"0177.0.0.1", "127.0.0.1"},
		{"0177.0.0.01", "127.0.0.1"},
		{"127.1", "127.0.0.1"},
		{"127.0.1", "127.0.0.1"},
		{"10.1", "10.0.0.1"},
		{"1.2.3", "1.2.0.3"},
		{"256", "0.0.1.0"},
		{"0", "0.0.0.0"},
		{"0x0a.0x00.0x00.0x01", "10.0.0.1"},
		{"169.254.43518", "169.254.169.254"},
		{"3232235777", "192.168.1.1"},
		// Numeric-looking but invalid: refused rather than handed to a resolver.
		{"1.2.3.4.5", "bad"},
		{"999.1.1.1", "bad"},
		{"08.0.0.1", "bad"},
		{"4294967296", "bad"},
		{"1.2.3.256", "bad"},
		{"1.2.65536", "bad"},
		{"1.16777216", "bad"},
		// Ordinary names.
		{"example.com", ""},
		{"localhost", ""},
		{"1e3.example.com", ""},
		{"a.b", ""},
		{"0x", ""},
		{"123abc", ""},
		{"1.2.3.example", ""},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			addr, isIP, err := literalIP(tt.host)
			switch tt.want {
			case "":
				if isIP || err != nil {
					t.Errorf("literalIP(%q) = %v,%v,%v; want a plain name", tt.host, addr, isIP, err)
				}
			case "bad":
				if !errors.Is(err, errBadIP) {
					t.Errorf("literalIP(%q) = %v,%v,%v; want errBadIP", tt.host, addr, isIP, err)
				}
			default:
				if err != nil || !isIP || addr.String() != tt.want {
					t.Errorf("literalIP(%q) = %v,%v,%v; want %s", tt.host, addr, isIP, err, tt.want)
				}
			}
		})
	}
}

func TestHostRules(t *testing.T) {
	g := newGuard(false, []string{
		"docs.internal", "  Wiki.Example.COM.  ", "10.1.2.3", "192.168.0.5:8080", "[::1]", "[fd00::5]:9000",
		"*.corp.test", "localhost:3000", "", "   ",
	})
	tests := []struct {
		host, port string
		want       bool
	}{
		{"docs.internal", "80", true},
		{"docs.internal", "8443", true},
		{"DOCS.internal", "80", false}, // callers normalise first; matching is exact on normalised input
		{"wiki.example.com", "443", true},
		{"10.1.2.3", "80", true},
		{"10.1.2.4", "80", false},
		{"192.168.0.5", "8080", true},
		{"192.168.0.5", "80", false},
		{"::1", "80", true},
		{"::1", "9999", true},
		{"fd00::5", "9000", true},
		{"fd00::5", "9001", false},
		{"a.corp.test", "80", true},
		{"a.b.corp.test", "80", true},
		{"corp.test", "80", false}, // wildcard means subdomains only
		{"evilcorp.test", "80", false},
		{"localhost", "3000", true},
		{"localhost", "3001", false},
		{"example.com", "80", false},
	}
	for _, tt := range tests {
		if got := g.allowed(tt.host, tt.port); got != tt.want {
			t.Errorf("allowed(%q, %q) = %v, want %v", tt.host, tt.port, got, tt.want)
		}
	}
	if len(g.rules) != 8 {
		t.Errorf("%d rules parsed, want 8 (blank entries ignored)", len(g.rules))
	}
	if !newGuard(true, nil).allowed("anything", "1") {
		t.Error("AllowPrivate must allow everything")
	}
}

func stubLookup(m map[string][]string) func(context.Context, string) ([]netip.Addr, error) {
	return func(_ context.Context, host string) ([]netip.Addr, error) {
		v, ok := m[host]
		if !ok {
			return nil, errors.New("no such host")
		}
		var out []netip.Addr
		for _, s := range v {
			out = append(out, netip.MustParseAddr(s))
		}
		return out, nil
	}
}

func TestResolve(t *testing.T) {
	dns := map[string][]string{
		"public.test":      {"8.8.8.8"},
		"dual.test":        {"2606:4700:4700::1111", "1.1.1.1"},
		"private.test":     {"10.0.0.5"},
		"loop.test":        {"127.0.0.1", "::1"},
		"mixed.test":       {"10.0.0.5", "8.8.8.8", "127.0.0.1"},
		"meta.test":        {"169.254.169.254"},
		"localhost":        {"127.0.0.1", "::1"},
		"empty.test":       {},
		"rebind.test":      {"8.8.4.4", "192.168.1.1"},
		"mapped.test":      {"::ffff:127.0.0.1"},
		"ports.test":       {"10.9.9.9"},
		"docs.internal":    {"10.0.0.9"},
		"a.corp.test":      {"172.16.1.1"},
		"weird.example":    {"100.64.1.1"},
		"internal-v6.test": {"fd00::1", "2606:4700:4700::1111"},
	}
	tests := []struct {
		name    string
		allow   []string
		private bool
		host    string
		port    string
		want    []string
		blocked string // substring expected in the *blockedError
		errIs   error
		lookErr bool
	}{
		{name: "public name", host: "public.test", want: []string{"8.8.8.8"}},
		{name: "dual stack prefers IPv4", host: "dual.test", want: []string{"1.1.1.1", "2606:4700:4700::1111"}},
		{name: "private name is blocked", host: "private.test", blocked: "private network"},
		{name: "localhost is blocked", host: "localhost", blocked: "loopback"},
		{name: "loopback pair", host: "loop.test", blocked: "loopback"},
		{name: "metadata name", host: "meta.test", blocked: "link-local"},
		{name: "mixed answers keep only the public one", host: "mixed.test", want: []string{"8.8.8.8"}},
		{name: "v6 ULA filtered, public v6 kept", host: "internal-v6.test", want: []string{"2606:4700:4700::1111"}},
		{name: "mapped loopback", host: "mapped.test", blocked: "loopback"},
		{name: "uppercase and trailing dot are normalised", host: "LOCALHOST.", blocked: "loopback"},
		{name: "literal loopback", host: "127.0.0.1", blocked: "loopback"},
		{name: "literal metadata", host: "169.254.169.254", blocked: "link-local"},
		{name: "literal public", host: "8.8.8.8", want: []string{"8.8.8.8"}},
		{name: "literal v6 loopback", host: "::1", blocked: "loopback"},
		{name: "decimal loopback", host: "2130706433", blocked: "loopback"},
		{name: "hex loopback", host: "0x7f.1", blocked: "loopback"},
		{name: "octal loopback", host: "0177.0.0.1", blocked: "loopback"},
		{name: "short loopback", host: "127.1", blocked: "loopback"},
		{name: "bad numeric host", host: "1.2.3.4.5", errIs: errBadIP},
		{name: "unresolvable", host: "nx.test", lookErr: true},
		{name: "no addresses", host: "empty.test", lookErr: true},
		{name: "empty host", host: "", errIs: errEmptyHost},
		{name: "allowed by name", allow: []string{"docs.internal"}, host: "docs.internal", want: []string{"10.0.0.9"}},
		{name: "allowed by wildcard", allow: []string{"*.corp.test"}, host: "a.corp.test", want: []string{"172.16.1.1"}},
		{name: "allowed by literal", allow: []string{"127.0.0.1"}, host: "127.0.0.1", want: []string{"127.0.0.1"}},
		{name: "allowed by host:port", allow: []string{"ports.test:8080"}, host: "ports.test", port: "8080", want: []string{"10.9.9.9"}},
		{name: "wrong port is still blocked", allow: []string{"ports.test:8080"}, host: "ports.test", port: "9090", blocked: "private network"},
		{name: "allow list does not leak to other names", allow: []string{"docs.internal"}, host: "private.test", blocked: "private network"},
		{name: "AllowPrivate", private: true, host: "private.test", want: []string{"10.0.0.5"}},
		{name: "AllowPrivate literal", private: true, host: "127.0.0.1", want: []string{"127.0.0.1"}},
		{name: "AllowPrivate still refuses garbage numerics", private: true, host: "1.2.3.4.5", errIs: errBadIP},
		{name: "cgnat", host: "weird.example", blocked: "carrier-grade"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := newGuard(tt.private, tt.allow)
			g.lookup = stubLookup(dns)
			port := tt.port
			if port == "" {
				port = "80"
			}
			addrs, err := g.resolve(context.Background(), tt.host, port)
			switch {
			case tt.blocked != "":
				var be *blockedError
				if !errors.As(err, &be) || !strings.Contains(be.Reason, tt.blocked) {
					t.Fatalf("err = %v, want a *blockedError with reason containing %q", err, tt.blocked)
				}
				if !strings.HasPrefix(be.Error(), "blocked: ") {
					t.Errorf("message = %q", be.Error())
				}
			case tt.lookErr:
				var le *lookupError
				if !errors.As(err, &le) {
					t.Fatalf("err = %v, want a *lookupError", err)
				}
			case tt.errIs != nil:
				if !errors.Is(err, tt.errIs) {
					t.Fatalf("err = %v, want %v", err, tt.errIs)
				}
			default:
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				var got []string
				for _, a := range addrs {
					got = append(got, a.String())
				}
				if strings.Join(got, ",") != strings.Join(tt.want, ",") {
					t.Errorf("addrs = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestBlockedErrorMessages(t *testing.T) {
	g := newGuard(false, nil)
	g.lookup = stubLookup(map[string][]string{"internal.test": {"10.1.1.1"}})
	_, err := g.resolve(context.Background(), "internal.test", "80")
	if want := "blocked: internal.test resolves to 10.1.1.1, a private network address"; err == nil || err.Error() != want {
		t.Errorf("err = %v, want %q", err, want)
	}
	_, err = g.resolve(context.Background(), "127.0.0.1", "80")
	if want := "blocked: 127.0.0.1 is a loopback address"; err == nil || err.Error() != want {
		t.Errorf("err = %v, want %q", err, want)
	}
	_, err = g.resolve(context.Background(), "2130706433", "80")
	if err == nil || !strings.Contains(err.Error(), "2130706433 resolves to 127.0.0.1") {
		t.Errorf("err = %v", err)
	}
}

// The address that is vetted is the address that is dialed: the connection goes
// to an IP literal from the vetted set, never to the name.
func TestDialContextPinsTheVettedAddress(t *testing.T) {
	g := newGuard(false, nil)
	var lookups atomic.Int32
	answers := [][]string{{"8.8.4.4"}, {"127.0.0.1"}, {"8.8.8.8", "1.1.1.1"}}
	g.lookup = func(_ context.Context, host string) ([]netip.Addr, error) {
		i := int(lookups.Add(1)) - 1
		var out []netip.Addr
		for _, s := range answers[min(i, len(answers)-1)] {
			out = append(out, netip.MustParseAddr(s))
		}
		return out, nil
	}
	var dialed []string
	g.dial = func(_ context.Context, network, address string) (net.Conn, error) {
		dialed = append(dialed, address)
		c1, c2 := net.Pipe()
		c2.Close()
		return c1, nil
	}

	// First connection: the name resolves to a public address and is pinned to it.
	conn, err := g.dialContext(context.Background(), "tcp", "rebind.test:443")
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if len(dialed) != 1 || dialed[0] != "8.8.4.4:443" {
		t.Fatalf("dialed %v, want the vetted IP literal 8.8.4.4:443", dialed)
	}
	if lookups.Load() != 1 {
		t.Errorf("%d lookups for one connection; the answer must not be re-resolved after vetting", lookups.Load())
	}

	// DNS rebinding: the same name now answers with loopback. A new connection
	// is vetted afresh and refused, and nothing is dialed.
	_, err = g.dialContext(context.Background(), "tcp", "rebind.test:443")
	var be *blockedError
	if !errors.As(err, &be) || be.Reason != "loopback" {
		t.Fatalf("err = %v, want blocked loopback", err)
	}
	if len(dialed) != 1 {
		t.Errorf("a blocked address was dialed: %v", dialed)
	}

	// Several vetted addresses are tried in order (IPv4 first).
	g.dial = func(_ context.Context, network, address string) (net.Conn, error) {
		dialed = append(dialed, address)
		if strings.HasPrefix(address, "8.8.8.8") {
			return nil, errors.New("connection refused")
		}
		c1, c2 := net.Pipe()
		c2.Close()
		return c1, nil
	}
	dialed = nil
	conn, err = g.dialContext(context.Background(), "tcp", "multi.test:80")
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if strings.Join(dialed, ",") != "8.8.8.8:80,1.1.1.1:80" {
		t.Errorf("dialed %v", dialed)
	}
}

func TestDialContextIPv6AndFailures(t *testing.T) {
	g := newGuard(false, nil)
	g.lookup = stubLookup(map[string][]string{"v6.test": {"2606:4700:4700::1111"}})
	var dialed []string
	g.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		dialed = append(dialed, address)
		return nil, errors.New("boom")
	}
	if _, err := g.dialContext(context.Background(), "tcp", "v6.test:8443"); err == nil || err.Error() != "boom" {
		t.Errorf("err = %v, want the dial error", err)
	}
	if len(dialed) != 1 || dialed[0] != "[2606:4700:4700::1111]:8443" {
		t.Errorf("dialed %v", dialed)
	}
	// Malformed addresses fail cleanly.
	for _, addr := range []string{"nohost", "", ":80", "[::1"} {
		if _, err := g.dialContext(context.Background(), "tcp", addr); err == nil {
			t.Errorf("dialContext(%q) succeeded", addr)
		}
	}
	// A cancelled context stops the attempts.
	g.lookup = stubLookup(map[string][]string{"many.test": {"8.8.8.8", "8.8.4.4", "1.1.1.1"}})
	ctx, cancel := context.WithCancel(context.Background())
	dialed = nil
	g.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		dialed = append(dialed, address)
		cancel()
		return nil, ctx.Err()
	}
	if _, err := g.dialContext(ctx, "tcp", "many.test:80"); err == nil {
		t.Error("expected an error")
	}
	if len(dialed) != 1 {
		t.Errorf("kept dialing after cancellation: %v", dialed)
	}
}

func TestVetOnly(t *testing.T) {
	g := newGuard(false, []string{"ok.internal"})
	g.lookup = stubLookup(map[string][]string{
		"public.test": {"8.8.8.8"}, "private.test": {"10.0.0.1"}, "mixed.test": {"8.8.8.8", "127.0.0.1"}, "ok.internal": {"10.0.0.2"},
	})
	tests := []struct {
		host    string
		blocked bool
		err     bool
	}{
		{"public.test", false, false},
		{"private.test", true, false},
		{"mixed.test", true, false}, // without pinning, any bad answer is grounds to refuse
		{"ok.internal", false, false},
		{"unresolvable.test", false, false}, // left to the proxy
		{"127.0.0.1", true, false},
		{"2130706433", true, false},
		{"8.8.8.8", false, false},
		{"1.2.3.4.5", false, true},
		{"", false, true},
	}
	for _, tt := range tests {
		err := g.vetOnly(context.Background(), tt.host, "80")
		var be *blockedError
		switch {
		case tt.blocked && !errors.As(err, &be):
			t.Errorf("vetOnly(%q) = %v, want blocked", tt.host, err)
		case tt.err && err == nil:
			t.Errorf("vetOnly(%q) = nil, want an error", tt.host)
		case !tt.blocked && !tt.err && err != nil:
			t.Errorf("vetOnly(%q) = %v, want nil", tt.host, err)
		}
	}
}

func TestAsciiFold(t *testing.T) {
	ideo, fullDot, halfIdeo := string(rune(0x3002)), string(rune(0xFF0E)), string(rune(0xFF61))
	fw := func(s string) string { // fullwidth form of an ASCII string
		out := []rune(s)
		for i, r := range out {
			if r >= 0x21 && r <= 0x7E {
				out[i] = r + 0xFEE0
			}
		}
		return string(out)
	}
	tests := []struct{ in, want string }{
		{"example.com", "example.com"},
		{"127" + ideo + "0" + ideo + "0" + ideo + "1", "127.0.0.1"},
		{"127" + fullDot + "0" + fullDot + "0" + fullDot + "1", "127.0.0.1"},
		{"127" + halfIdeo + "1", "127.1"},
		{fw("127") + ".0.0.1", "127.0.0.1"},
		{fw("0x7f") + ".1", "0x7f.1"},
		{fw("LOCALHOST"), "LOCALHOST"},
		{"caf\u00e9.example", "caf\u00e9.example"}, // real IDN labels are left alone
		{"", ""},
	}
	for _, tt := range tests {
		if got := asciiFold(tt.in); got != tt.want {
			t.Errorf("asciiFold(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	// The guard sees through the look-alikes, in both direct and proxy mode.
	g := newGuard(false, nil)
	g.lookup = stubLookup(map[string][]string{"localhost": {"127.0.0.1"}})
	for _, host := range []string{
		"127" + ideo + "0" + ideo + "0" + ideo + "1",
		fw("127") + ".0.0.1",
		fw("2130706433"),
		fw("localhost"),
		"127" + fullDot + "1",
	} {
		if _, err := g.resolve(context.Background(), host, "80"); !isBlocked(err) {
			t.Errorf("resolve(%q) = %v, want blocked", host, err)
		}
		if err := g.vetOnly(context.Background(), host, "80"); !isBlocked(err) {
			t.Errorf("vetOnly(%q) = %v, want blocked", host, err)
		}
	}
}

func isBlocked(err error) bool {
	var be *blockedError
	return errors.As(err, &be)
}
