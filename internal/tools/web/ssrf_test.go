package web

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

// noDial makes any dial a test failure: blocked targets must be refused before a
// connection is even attempted.
func noDial(t *testing.T, dials *atomic.Int32) func(context.Context, string, string) (net.Conn, error) {
	return func(_ context.Context, network, address string) (net.Conn, error) {
		dials.Add(1)
		t.Errorf("a connection to %s was attempted", address)
		return nil, fmt.Errorf("dial not expected")
	}
}

func TestSSRFBlockedURLs(t *testing.T) {
	secret, hits := serve(t, "text/plain", "SECRET")
	port := tcpPort(secret)
	h := newHarness(t, Config{}) // the default: guard on
	h.f.guard.lookup = staticLookup(map[string]string{
		"localhost":     "127.0.0.1",
		"internal.test": "10.0.0.5",
		"meta.test":     "169.254.169.254",
		"v6local.test":  "::1",
		"cgnat.test":    "100.64.0.1",
		"mapped.test":   "::ffff:127.0.0.1",
	})
	var dials atomic.Int32
	h.f.guard.dial = noDial(t, &dials)

	urls := []string{
		// Loopback in every spelling.
		"http://127.0.0.1:PORT/", "http://127.0.0.1/", "http://127.255.255.254:PORT/", "http://[::1]:PORT/", "http://[::1]/",
		"http://[::ffff:127.0.0.1]:PORT/", "http://[0:0:0:0:0:ffff:7f00:1]:PORT/", "http://[64:ff9b::7f00:1]/", "http://[2002:7f00:1::]/",
		"http://2130706433:PORT/", "http://0x7f000001:PORT/", "http://0x7f.1:PORT/", "http://0177.0.0.1:PORT/", "http://127.1:PORT/",
		"http://localhost:PORT/", "http://LOCALHOST.:PORT/", "http://localhost/", "http://v6local.test/", "http://mapped.test/",
		// Private networks.
		"http://10.1.2.3/", "http://10.255.255.255:8080/", "http://172.16.0.1/", "http://172.31.255.255/", "http://192.168.1.1/",
		"http://[fd00::1]/", "http://[fc00::1]:8080/", "http://[::ffff:10.0.0.1]/", "http://internal.test/",
		// Link-local and cloud metadata.
		"http://169.254.169.254/latest/meta-data/", "http://169.254.169.254/computeMetadata/v1/", "http://[fe80::1]/",
		"http://[fd00:ec2::254]/latest/meta-data/", "http://[::ffff:169.254.169.254]/", "http://meta.test/latest/meta-data/",
		"http://168.63.129.16/", "http://100.100.100.200/latest/meta-data/", "http://2852039166/",
		// CGNAT, unspecified, multicast, reserved.
		"http://100.64.0.1/", "http://cgnat.test/", "http://0.0.0.0:PORT/", "http://0.0.0.0/", "http://0/", "http://[::]/",
		"http://224.0.0.1/", "http://239.255.255.250:1900/", "http://255.255.255.255/", "http://240.0.0.1/", "http://192.0.0.192/", "http://198.18.0.1/",
		// Unicode look-alikes of dots and digits (what a browser or proxy would normalise).
		"http://127" + string(rune(0x3002)) + "0" + string(rune(0x3002)) + "0" + string(rune(0x3002)) + "1:PORT/",
		"http://" + string(rune(0xFF11)) + string(rune(0xFF12)) + string(rune(0xFF17)) + ".0.0.1:PORT/",
		"http://127" + string(rune(0xFF0E)) + "1:PORT/",
		// Credentials do not change where it goes; they are refused separately.
		"http://user@127.0.0.1:PORT/",
	}
	for _, raw := range urls {
		target := strings.ReplaceAll(raw, "PORT", port)
		t.Run(raw, func(t *testing.T) {
			res := h.get(target)
			if !res.IsError {
				t.Fatalf("%s was fetched: %q", target, res.Text)
			}
			if !strings.Contains(res.Text, "blocked:") && !strings.Contains(res.Text, "embedded credentials") {
				t.Errorf("error = %q, want a blocked address error", res.Text)
			}
		})
	}
	if hits.Load() != 0 {
		t.Errorf("the protected server was contacted %d times", hits.Load())
	}
	if dials.Load() != 0 {
		t.Errorf("%d dials for blocked URLs", dials.Load())
	}

	// Numeric hosts that are not valid addresses are refused, not resolved.
	for _, raw := range []string{"http://1.2.3.4.5/", "http://999.1.1.1/", "http://08.0.0.1/"} {
		res := h.get(raw)
		if !res.IsError || !strings.Contains(res.Text, "invalid IP address") {
			t.Errorf("%s: %q", raw, res.Text)
		}
	}
	// The error explains itself to the model.
	res := h.get("http://169.254.169.254/latest/meta-data/")
	if want := "web_fetch: blocked: 169.254.169.254 is a link-local (cloud metadata range) address. Private, loopback and link-local addresses cannot be fetched"; res.Text != want {
		t.Errorf("text = %q\nwant   %q", res.Text, want)
	}
}

func TestSSRFAllowLists(t *testing.T) {
	srv, hits := serve(t, "text/plain", "reachable")
	port := tcpPort(srv)
	tests := []struct {
		name  string
		cfg   Config
		url   string
		names map[string]string
		ok    bool
	}{
		{"blocked by default", Config{}, "http://127.0.0.1:" + port + "/", nil, false},
		{"AllowPrivate", Config{AllowPrivate: true}, "http://127.0.0.1:" + port + "/", nil, true},
		{"allowed literal", Config{AllowHosts: []string{"127.0.0.1"}}, "http://127.0.0.1:" + port + "/", nil, true},
		{"allowed host:port", Config{AllowHosts: []string{"127.0.0.1:" + port}}, "http://127.0.0.1:" + port + "/", nil, true},
		{"wrong port is blocked", Config{AllowHosts: []string{"127.0.0.1:1"}}, "http://127.0.0.1:" + port + "/", nil, false},
		{"another host is blocked", Config{AllowHosts: []string{"127.0.0.2"}}, "http://127.0.0.1:" + port + "/", nil, false},
		{"allowed name", Config{AllowHosts: []string{"docs.internal"}}, "http://docs.internal:" + port + "/", map[string]string{"docs.internal": "127.0.0.1"}, true},
		{"allowed wildcard", Config{AllowHosts: []string{"*.corp.test"}}, "http://wiki.corp.test:" + port + "/", map[string]string{"wiki.corp.test": "127.0.0.1"}, true},
		{"wildcard does not match the apex", Config{AllowHosts: []string{"*.corp.test"}}, "http://corp.test:" + port + "/", map[string]string{"corp.test": "127.0.0.1"}, false},
		{"allow-listing a name does not allow the address", Config{AllowHosts: []string{"docs.internal"}}, "http://127.0.0.1:" + port + "/", nil, false},
		{"decimal spelling of an allowed address", Config{AllowHosts: []string{"127.0.0.1"}}, "http://2130706433:" + port + "/", nil, false},
		{"localhost by name", Config{AllowHosts: []string{"localhost"}}, "http://localhost:" + port + "/", map[string]string{"localhost": "127.0.0.1"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := hits.Load()
			h := newHarness(t, tt.cfg)
			if tt.names != nil {
				h.f.guard.lookup = staticLookup(tt.names)
			}
			res := h.get(tt.url)
			if tt.ok {
				if res.IsError || !strings.Contains(res.Text, "reachable") {
					t.Fatalf("result = %v %q", res.IsError, res.Text)
				}
				return
			}
			if !res.IsError || !strings.Contains(res.Text, "blocked:") {
				t.Errorf("result = %v %q, want blocked", res.IsError, res.Text)
			}
			if hits.Load() != before {
				t.Error("a blocked URL reached the server")
			}
		})
	}
}

// Every hop of a redirect chain is vetted: an allowed server cannot bounce the
// fetch into the internal network.
func TestSSRFRedirectToBlockedTargets(t *testing.T) {
	secret, secretHits := serve(t, "text/plain", "SECRET")
	origin, originHits := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if to := r.URL.Query().Get("to"); to != "" {
			http.Redirect(w, r, to, http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "origin content")
	})
	sport, oport := tcpPort(secret), tcpPort(origin)
	h := newHarness(t, Config{AllowHosts: []string{"127.0.0.1:" + oport, "*.test"}})
	h.f.guard.lookup = staticLookup(map[string]string{
		"localhost": "127.0.0.1", "rebound.test": "127.0.0.1", "internal.test": "10.0.0.5",
		"allowed.test": "127.0.0.1",
	})

	targets := []string{
		"http://127.0.0.1:" + sport + "/secret",
		"http://[::1]:" + sport + "/",
		"http://localhost:" + sport + "/",
		"http://2130706433:" + sport + "/",
		"http://[::ffff:127.0.0.1]:" + sport + "/",
		"http://0.0.0.0:" + sport + "/",
		"http://169.254.169.254/latest/meta-data/",
		"http://10.0.0.5/",
		"http://192.168.0.1/admin",
		"http://[fd00::1]/",
		"http://100.64.0.1/",
	}
	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			before := originHits.Load()
			res := h.get(origin.URL + "/r?to=" + url.QueryEscape(target))
			if !res.IsError || !strings.Contains(res.Text, "blocked:") {
				t.Fatalf("redirect to %s: result = %v %q", target, res.IsError, res.Text)
			}
			if originHits.Load() != before+1 {
				t.Errorf("origin hits = %d, want exactly the one request that issued the redirect", originHits.Load()-before)
			}
		})
	}
	if secretHits.Load() != 0 {
		t.Errorf("the protected server was reached %d times through redirects", secretHits.Load())
	}

	t.Run("second hop", func(t *testing.T) {
		mid := "http://allowed.test:" + oport + "/r?to=" + url.QueryEscape("http://169.254.169.254/x")
		res := h.get(origin.URL + "/r?to=" + url.QueryEscape(mid))
		if !res.IsError || !strings.Contains(res.Text, "blocked: 169.254.169.254") {
			t.Errorf("result = %v %q", res.IsError, res.Text)
		}
	})
	t.Run("a redirect within the allowed origin works", func(t *testing.T) {
		res := h.get(origin.URL + "/r?to=" + url.QueryEscape(origin.URL+"/landing"))
		if res.IsError || !strings.Contains(res.Text, "origin content") {
			t.Errorf("result = %v %q", res.IsError, res.Text)
		}
	})
	t.Run("allowed to allowed works", func(t *testing.T) {
		res := h.get(origin.URL + "/r?to=" + url.QueryEscape("http://allowed.test:"+oport+"/"))
		if res.IsError || !strings.Contains(res.Text, "origin content") {
			t.Errorf("result = %v %q", res.IsError, res.Text)
		}
	})
}

// The connection goes to the address that was vetted, and every new connection
// is vetted again, so a name that changes its answer cannot slip through.
func TestSSRFDNSRebindingEndToEnd(t *testing.T) {
	srv, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Connection", "close") // force a new connection next time
		io.WriteString(w, "served for "+r.Host)
	})
	h := newHarness(t, Config{})
	var lookups atomic.Int32
	var dialed []string
	h.f.guard.lookup = func(_ context.Context, host string) ([]netip.Addr, error) {
		if lookups.Add(1) == 1 {
			return []netip.Addr{netip.MustParseAddr("8.8.4.4")}, nil // looks public
		}
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil // then rebinds to loopback
	}
	h.f.guard.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		dialed = append(dialed, address)
		var d net.Dialer
		return d.DialContext(ctx, network, srv.Listener.Addr().String()) // stand-in for the public host
	}
	res := h.get("http://rebind.test:8080/first")
	if res.IsError || !strings.Contains(res.Text, "served for rebind.test:8080") {
		t.Fatalf("first fetch = %v %q", res.IsError, res.Text)
	}
	if len(dialed) != 1 || dialed[0] != "8.8.4.4:8080" {
		t.Fatalf("dialed %v: the connection must go to the vetted IP literal, not the name", dialed)
	}
	res = h.get("http://rebind.test:8080/second")
	if !res.IsError || !strings.Contains(res.Text, "blocked: rebind.test resolves to 127.0.0.1") {
		t.Fatalf("second fetch = %v %q", res.IsError, res.Text)
	}
	if len(dialed) != 1 {
		t.Errorf("dialed %v after the rebind", dialed)
	}
}

func TestProxyMode(t *testing.T) {
	var proxied atomic.Int32
	var lastHost atomic.Value
	proxy, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		proxied.Add(1)
		lastHost.Store(r.URL.Host)
		if r.URL.Path == "/redir" {
			http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "via proxy: "+r.URL.String())
	})
	proxyURL, _ := url.Parse(proxy.URL)
	direct, directHits := serve(t, "text/plain", "direct content")

	cfg := Config{
		Proxy: func(r *http.Request) (*url.URL, error) {
			if r.URL.Hostname() == "bypass.test" {
				return nil, nil // NO_PROXY
			}
			return proxyURL, nil
		},
		AllowHosts: []string{"bypass.test"},
	}
	h := newHarness(t, cfg)
	h.f.guard.lookup = staticLookup(map[string]string{
		"internal.test": "10.0.0.5",
		"bypass.test":   "127.0.0.1",
		"private.test":  "127.0.0.1",
		// public.test does not resolve locally: in proxy sandboxes the proxy resolves it
	})

	t.Run("public name goes through the proxy", func(t *testing.T) {
		res := h.get("http://public.test/page?x=1")
		if res.IsError || !strings.Contains(res.Text, "via proxy: http://public.test/page?x=1") {
			t.Fatalf("result = %v %q", res.IsError, res.Text)
		}
		if proxied.Load() != 1 {
			t.Errorf("proxy saw %d requests", proxied.Load())
		}
	})
	t.Run("private targets are refused even with a proxy", func(t *testing.T) {
		before := proxied.Load()
		for _, target := range []string{
			"http://127.0.0.1:" + tcpPort(direct) + "/", "http://10.0.0.5/", "http://internal.test/", "http://private.test/",
			"http://169.254.169.254/latest/meta-data/", "http://2130706433/", "http://[::1]/",
		} {
			res := h.get(target)
			if !res.IsError || !strings.Contains(res.Text, "blocked:") {
				t.Errorf("%s: %v %q", target, res.IsError, res.Text)
			}
		}
		if proxied.Load() != before {
			t.Error("a blocked request reached the proxy")
		}
		if directHits.Load() != 0 {
			t.Error("a blocked request reached the target")
		}
	})
	t.Run("hosts the proxy function bypasses are dialed directly and pinned", func(t *testing.T) {
		var dialed []string
		h.f.guard.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
			dialed = append(dialed, address)
			var d net.Dialer
			return d.DialContext(ctx, network, direct.Listener.Addr().String())
		}
		before := proxied.Load()
		res := h.get("http://bypass.test:" + tcpPort(direct) + "/")
		if res.IsError || !strings.Contains(res.Text, "direct content") {
			t.Fatalf("result = %v %q", res.IsError, res.Text)
		}
		if proxied.Load() != before {
			t.Error("a bypassed host went through the proxy")
		}
		if len(dialed) != 1 || dialed[0] != "127.0.0.1:"+tcpPort(direct) {
			t.Errorf("dialed %v", dialed)
		}
	})
	t.Run("redirects are vetted in proxy mode too", func(t *testing.T) {
		before := proxied.Load()
		res := h.get("http://public.test/redir")
		if !res.IsError || !strings.Contains(res.Text, "blocked: 169.254.169.254") {
			t.Errorf("result = %v %q", res.IsError, res.Text)
		}
		if got := proxied.Load() - before; got != 1 {
			t.Errorf("proxy saw %d requests, want only the one that redirected", got)
		}
	})
}
