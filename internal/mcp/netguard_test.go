package mcp

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		addr  string
		class addrClass
	}{
		// public
		{"8.8.8.8", classPublic},
		{"1.1.1.1", classPublic},
		{"93.184.216.34", classPublic},
		{"2606:4700:4700::1111", classPublic},
		{"::ffff:8.8.8.8", classPublic},
		// private: only with allow_private
		{"127.0.0.1", classPrivate},
		{"127.255.255.254", classPrivate},
		{"::1", classPrivate},
		{"10.0.0.1", classPrivate},
		{"172.16.5.5", classPrivate},
		{"192.168.1.1", classPrivate},
		{"100.64.0.1", classPrivate}, // carrier-grade NAT, Tailscale
		{"100.127.255.255", classPrivate},
		{"fc00::1", classPrivate},
		{"fd12:3456::1", classPrivate},
		{"0.0.0.0", classPrivate},
		{"::", classPrivate},
		{"::ffff:127.0.0.1", classPrivate},
		{"::ffff:10.1.2.3", classPrivate},
		{"64:ff9b::7f00:1", classPrivate}, // NAT64 wrapping 127.0.0.1
		{"2002:7f00:1::", classPrivate},   // 6to4 wrapping 127.0.0.1
		{"::7f00:1", classPrivate},        // IPv4-compatible
		{"fec0::1", classPrivate},
		// never, even with allow_private
		{"169.254.169.254", classNever}, // cloud metadata
		{"169.254.0.1", classNever},
		{"fe80::1", classNever},
		{"::ffff:169.254.169.254", classNever},
		{"64:ff9b::a9fe:a9fe", classNever}, // NAT64 wrapping the metadata address
		{"100.100.100.200", classNever},    // a metadata address inside the CGNAT range
		{"168.63.129.16", classNever},
		{"fd00:ec2::254", classNever}, // AWS IPv6 metadata, inside unique-local
		{"224.0.0.1", classNever},
		{"ff02::1", classNever},
		{"240.0.0.1", classNever},
		{"192.0.2.1", classNever},
		{"198.51.100.7", classNever},
		{"203.0.113.9", classNever},
		{"198.18.0.1", classNever},
		{"0.1.2.3", classNever},
		{"2001:db8::1", classNever},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			got, why := classify(netip.MustParseAddr(tt.addr))
			if got != tt.class {
				t.Errorf("classify(%s) = %d (%s), want %d", tt.addr, got, why, tt.class)
			}
			if got != classPublic && why == "" {
				t.Error("a refusal needs a reason")
			}
		})
	}
}

func TestLiteralIP(t *testing.T) {
	tests := []struct {
		host string
		ip   string // "" = not an IP
		bad  bool
	}{
		{"127.0.0.1", "127.0.0.1", false},
		{"2130706433", "127.0.0.1", false},
		{"0x7f000001", "127.0.0.1", false},
		{"0x7f.1", "127.0.0.1", false},
		{"0177.0.0.1", "127.0.0.1", false},
		{"127.1", "127.0.0.1", false},
		{"10.1", "10.0.0.1", false},
		{"[::1]", "", false}, // brackets are stripped before this point; not an IP literal here
		{"::1", "::1", false},
		{"example.com", "", false},
		{"a.b.c.d.e", "", false},
		{"1.2.3.4.5", "", true},
		{"256.1.1.1", "", true},
		{"4294967296", "", true},
		{"0xzz", "", false},
		{"1.2.3", "1.2.0.3", false},
	}
	for _, tt := range tests {
		ip, isIP, err := literalIP(tt.host)
		switch {
		case tt.bad:
			if err == nil {
				t.Errorf("%s: no error (got %v %v)", tt.host, ip, isIP)
			}
		case tt.ip == "":
			if isIP || err != nil {
				t.Errorf("%s: isIP=%v err=%v", tt.host, isIP, err)
			}
		default:
			if !isIP || err != nil || ip.String() != tt.ip {
				t.Errorf("%s: got %v %v %v, want %s", tt.host, ip, isIP, err, tt.ip)
			}
		}
	}
}

func TestNormalizeHost(t *testing.T) {
	// Built from code points: the point is what the guard sees, not what the source shows.
	fullwidthDots := "127" + string(rune(0x3002)) + "0" + string(rune(0x3002)) + "0" + string(rune(0x3002)) + "1"
	fullwidthDigits := string(rune(0xFF11)) + string(rune(0xFF12)) + string(rune(0xFF17)) + ".0.0.1"
	for in, want := range map[string]string{
		"Example.COM.":  "example.com",
		" host ":        "host",
		fullwidthDots:   "127.0.0.1",
		fullwidthDigits: "127.0.0.1",
	} {
		if got := normalizeHost(in); got != want {
			t.Errorf("normalizeHost(%q) = %q, want %q", in, got, want)
		}
	}
}

// fakeNet stages DNS answers and records every address a connection is made to.
type fakeNet struct {
	mu      sync.Mutex
	answers map[string][]netip.Addr
	dialed  []string
	failing map[string]bool
}

func (f *fakeNet) guard(allowPrivate bool) *guard {
	g := newGuard(allowPrivate)
	g.lookup = func(_ context.Context, host string) ([]netip.Addr, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if a, ok := f.answers[host]; ok {
			return a, nil
		}
		return nil, errors.New("no such host")
	}
	g.dial = func(_ context.Context, _, address string) (net.Conn, error) {
		f.mu.Lock()
		f.dialed = append(f.dialed, address)
		fail := f.failing[address]
		f.mu.Unlock()
		if fail {
			return nil, errors.New("refused")
		}
		c1, c2 := net.Pipe()
		go func() { _ = c2.Close() }()
		return c1, nil
	}
	return g
}

func addrs(s ...string) []netip.Addr {
	var out []netip.Addr
	for _, a := range s {
		out = append(out, netip.MustParseAddr(a))
	}
	return out
}

func TestGuardDialsOnlyVettedAddresses(t *testing.T) {
	f := &fakeNet{answers: map[string][]netip.Addr{
		"public.example":   addrs("93.184.216.34"),
		"rebind.example":   addrs("127.0.0.1", "93.184.216.34"), // one bad address among good ones
		"internal.example": addrs("10.0.0.5"),
		"meta.example":     addrs("169.254.169.254"),
		"mixed.example":    addrs("2606:4700::1", "93.184.216.35"),
		"nothing.example":  {},
	}, failing: map[string]bool{}}

	tests := []struct {
		name         string
		allowPrivate bool
		addr         string
		wantDial     string // "" = must not dial
		wantErr      string
	}{
		{"public", false, "public.example:443", "93.184.216.34:443", ""},
		{"private name", false, "internal.example:80", "", "private network"},
		{"private name allowed", true, "internal.example:80", "10.0.0.5:80", ""},
		{"loopback literal", false, "127.0.0.1:80", "", "loopback"},
		{"loopback literal allowed", true, "127.0.0.1:80", "127.0.0.1:80", ""},
		{"ipv6 loopback", false, "[::1]:80", "", "loopback"},
		{"metadata literal", false, "169.254.169.254:80", "", "metadata"},
		{"metadata literal even when private is allowed", true, "169.254.169.254:80", "", "link-local"},
		{"metadata by name even when private is allowed", true, "meta.example:80", "", "link-local"},
		{"legacy spelling of loopback", false, "2130706433:80", "", "loopback"},
		{"short spelling of loopback", false, "127.1:80", "", "loopback"},
		{"rebinding: the bad address is skipped, never dialed", false, "rebind.example:443", "93.184.216.34:443", ""},
		{"ipv4 first", false, "mixed.example:443", "93.184.216.35:443", ""},
		{"unresolvable", false, "missing.example:443", "", "could not resolve"},
		{"empty answer", false, "nothing.example:443", "", "could not resolve"},
		{"bad numeric host", false, "1.2.3.4.5:80", "", "invalid IP"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f.mu.Lock()
			f.dialed = nil
			f.mu.Unlock()
			g := f.guard(tt.allowPrivate)
			conn, err := g.dialContext(context.Background(), "tcp", tt.addr)
			if conn != nil {
				conn.Close()
			}
			f.mu.Lock()
			dialed := append([]string(nil), f.dialed...)
			f.mu.Unlock()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				if len(dialed) != 0 {
					t.Errorf("dialed %v despite refusing", dialed)
				}
				if strings.Contains(err.Error(), "blocked") && !errors.Is(err, ErrBlocked) {
					t.Errorf("a policy refusal must satisfy errors.Is(ErrBlocked): %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(dialed) != 1 || dialed[0] != tt.wantDial {
				t.Errorf("dialed %v, want only %s", dialed, tt.wantDial)
			}
		})
	}
}

func TestGuardFallsBackToTheNextVettedAddress(t *testing.T) {
	f := &fakeNet{
		answers: map[string][]netip.Addr{"h.example": addrs("93.184.216.1", "93.184.216.2")},
		failing: map[string]bool{"93.184.216.1:443": true},
	}
	conn, err := f.guard(false).dialContext(context.Background(), "tcp", "h.example:443")
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if len(f.dialed) != 2 || f.dialed[1] != "93.184.216.2:443" {
		t.Errorf("dialed %v", f.dialed)
	}
}

func TestVetOnlyForProxies(t *testing.T) {
	f := &fakeNet{answers: map[string][]netip.Addr{"internal.example": addrs("10.0.0.5"), "public.example": addrs("93.184.216.34")}}
	g := f.guard(false)
	ctx := context.Background()
	if err := g.vetOnly(ctx, "public.example"); err != nil {
		t.Errorf("public: %v", err)
	}
	if err := g.vetOnly(ctx, "internal.example"); !errors.Is(err, ErrBlocked) {
		t.Errorf("internal: %v", err)
	}
	if err := g.vetOnly(ctx, "127.0.0.1"); !errors.Is(err, ErrBlocked) {
		t.Errorf("literal: %v", err)
	}
	// A name the local resolver cannot answer is left to the proxy.
	if err := g.vetOnly(ctx, "only-the-proxy-resolves-this.example"); err != nil {
		t.Errorf("unresolvable name: %v", err)
	}
	if err := f.guard(true).vetOnly(ctx, "internal.example"); err != nil {
		t.Errorf("allow_private: %v", err)
	}
}

func TestSameOrigin(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"https://h.example/a", "https://h.example/b", true},
		{"https://h.example", "https://h.example:443/x", true},
		{"http://h.example", "http://h.example:80/x", true},
		{"https://H.Example./a", "https://h.example/b", true},
		{"https://h.example", "http://h.example", false},
		{"https://h.example", "https://h.example:8443", false},
		{"https://h.example", "https://other.example", false},
		{"https://h.example", "https://sub.h.example", false},
		{"https://h.example", "https://h.example.evil.example", false},
	}
	for _, tt := range tests {
		a, _ := parseServerURL(tt.a)
		b, _ := parseServerURL(tt.b)
		if got := sameOrigin(a, b); got != tt.want {
			t.Errorf("sameOrigin(%s, %s) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestTransportErrorDropsTheURL(t *testing.T) {
	_, err := NewHTTPTransport(HTTPOptions{URL: "https://user:" + placeholder + "@h.example/x?k=" + placeholder})
	if err == nil || strings.Contains(err.Error(), placeholder) {
		t.Errorf("err = %v", err)
	}
	_, err = NewHTTPTransport(HTTPOptions{URL: "ftp://h.example/" + placeholder})
	if err == nil || strings.Contains(err.Error(), placeholder) {
		t.Errorf("err = %v", err)
	}
}
