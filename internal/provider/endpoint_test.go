package provider

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

func openaiEndpoint(base string, src EndpointSource) Endpoint {
	e := Endpoint{
		Name: "openai", BaseURL: base, Source: src, KeyEnv: "OPENAI_API_KEY",
		Anchors: []string{"https://api.openai.com/v1"},
	}
	if src == SourceEnv {
		e.EnvVar = "OPENAI_BASE_URL"
	}
	return e
}

func TestCheckEndpoint(t *testing.T) {
	type mod func(*Endpoint)
	allow := func(hosts ...string) mod { return func(e *Endpoint) { e.AllowHosts = hosts } }
	insecure := func(e *Endpoint) { e.AllowInsecureHTTP = true }
	nokey := func(e *Endpoint) { e.KeyEnv = "" }
	for _, tc := range []struct {
		name string
		base string
		src  EndpointSource
		mods []mod
		bad  []string // substrings the refusal must contain; nil means allowed
	}{
		// The built-in default, and what is really the same place.
		{"the default", "https://api.openai.com/v1", SourceConfigured, nil, nil},
		{"env: same origin, other path", "https://api.openai.com/v2/beta", SourceEnv, nil, nil},
		{"env: same origin, other case and explicit port", "https://API.OpenAI.com:443/v1", SourceEnv, nil, nil},
		{"project: same origin", "https://api.openai.com/proxy", SourceProject, nil, nil},

		// S45: the environment points the key at a stranger.
		{"env: plain http to an unknown host", "http://collector.attacker.example/v1", SourceEnv, nil,
			[]string{"refusing to send the API key from $OPENAI_API_KEY to collector.attacker.example", "OPENAI_BASE_URL",
				"api.openai.com", "allow_hosts", "~/.sleipnir/config.json", "unset OPENAI_BASE_URL", "allow_insecure_http"}},
		{"env: https to an unknown host", "https://collector.attacker.example/v1", SourceEnv, nil,
			[]string{"collector.attacker.example", "not a host this provider is known to use", `"allow_hosts":["collector.attacker.example"]`, "unset OPENAI_BASE_URL"}},
		{"env: a look-alike of the real host", "https://api.openai.com.attacker.example/v1", SourceEnv, nil, []string{"api.openai.com.attacker.example"}},
		{"env: a sibling subdomain", "https://evil.api.openai.com/v1", SourceEnv, nil, []string{"evil.api.openai.com"}},
		{"env: the real host on another port", "https://api.openai.com:8443/v1", SourceEnv, nil, []string{"api.openai.com:8443"}},
		{"env: the real host over plain http is a transport problem, not a foreign host", "http://api.openai.com/v1", SourceEnv, nil,
			[]string{"over plain http to api.openai.com", "allow_insecure_http"}},
		{"env: the real host on port 80", "http://api.openai.com:80/v1", SourceEnv, nil, []string{"over plain http to api.openai.com:80"}},
		{"env: the real host on port 443", "https://api.openai.com:443/v1", SourceEnv, nil, nil},
		{"env: an internal address", "https://10.0.0.5/v1", SourceEnv, nil, []string{"10.0.0.5"}},
		{"env: 0.0.0.0 is not loopback", "http://0.0.0.0:8000/v1", SourceEnv, nil, []string{"0.0.0.0:8000"}},

		// Local servers are always fine.
		{"env: localhost over http", "http://localhost:8000/v1", SourceEnv, nil, nil},
		{"env: 127.0.0.1 over http", "http://127.0.0.1:11434/v1", SourceEnv, nil, nil},
		{"env: ::1 over http", "http://[::1]:8080/v1", SourceEnv, nil, nil},
		{"env: loopback over https", "https://localhost/v1", SourceEnv, nil, nil},
		{"env: localhost look-alike", "http://localhost.attacker.example/v1", SourceEnv, nil, []string{"localhost.attacker.example"}},

		// The user's deliberate exceptions.
		{"env: a listed host", "https://proxy.corp.example/v1", SourceEnv, []mod{allow("proxy.corp.example")}, nil},
		{"env: a listed host, other case", "https://Proxy.Corp.Example/v1", SourceEnv, []mod{allow("proxy.CORP.example")}, nil},
		{"env: a listed host with a trailing dot", "https://proxy.corp.example./v1", SourceEnv, []mod{allow("proxy.corp.example")}, nil},
		{"env: a listed host, any port", "https://proxy.corp.example:9443/v1", SourceEnv, []mod{allow("proxy.corp.example")}, nil},
		{"env: a listed host:port", "https://proxy.corp.example:9443/v1", SourceEnv, []mod{allow("proxy.corp.example:9443")}, nil},
		{"env: a listed host:port, other port", "https://proxy.corp.example:9444/v1", SourceEnv, []mod{allow("proxy.corp.example:9443")}, []string{"proxy.corp.example:9444"}},
		{"env: another host is not covered by the list", "https://other.example/v1", SourceEnv, []mod{allow("proxy.corp.example")}, []string{"other.example"}},
		{"env: a listed IPv6 host", "https://[2001:db8::1]:8443/v1", SourceEnv, []mod{allow("[2001:db8::1]:8443")}, nil},
		{"env: a listed host over plain http still needs its own permission", "http://proxy.corp.example/v1", SourceEnv, []mod{allow("proxy.corp.example")},
			[]string{"plain http", `"allow_insecure_http":true`}},
		{"env: a listed host over plain http with permission", "http://proxy.corp.example/v1", SourceEnv, []mod{allow("proxy.corp.example"), insecure}, nil},
		{"env: insecure permission alone does not allow a stranger", "http://collector.attacker.example/v1", SourceEnv, []mod{insecure}, []string{"allow_hosts"}},

		// A project file.
		{"project: a stranger", "https://collector.attacker.example/v1", SourceProject, nil,
			[]string{"the project's configuration", "not your own file", "--trust-project", "allow_hosts"}},
		{"project: a listed host", "https://proxy.corp.example/v1", SourceProject, []mod{allow("proxy.corp.example")}, nil},
		{"project: loopback", "http://localhost:8080/v1", SourceProject, nil, nil},

		// What the user typed or wrote themselves.
		{"flag: any https host", "https://my-llm.example/v1", SourceFlag, nil, nil},
		{"flag: plain http to a stranger", "http://my-llm.example/v1", SourceFlag, nil, []string{"plain http", "allow_insecure_http"}},
		{"flag: plain http with permission", "http://my-llm.example/v1", SourceFlag, []mod{insecure}, nil},
		{"flag: loopback over http", "http://127.0.0.1:8000/v1", SourceFlag, nil, nil},
		{"configured: https anywhere", "https://gateway.corp.example/v1", SourceConfigured, nil, nil},
		{"configured: plain http to a LAN host", "http://gpu-box.lan:8000/v1", SourceConfigured, nil, []string{"plain http to gpu-box.lan:8000", "allow_insecure_http"}},
		{"configured: plain http to a LAN host, permitted", "http://gpu-box.lan:8000/v1", SourceConfigured, []mod{insecure}, nil},

		// No key, nothing to protect.
		{"no key: env, plain http, stranger", "http://collector.attacker.example/v1", SourceEnv, []mod{nokey}, nil},
		{"no key: project stranger", "https://collector.attacker.example/v1", SourceProject, []mod{nokey}, nil},

		// URLs that are not URLs.
		{"empty", "", SourceConfigured, nil, []string{"not an absolute http(s) URL"}},
		{"garbage", "not a url", SourceEnv, nil, []string{"not an absolute http(s) URL", "OPENAI_BASE_URL"}},
		{"ftp", "ftp://example.com/v1", SourceConfigured, nil, []string{"not an absolute http(s) URL"}},
		{"scheme-relative", "//example.com/v1", SourceConfigured, nil, []string{"not an absolute http(s) URL"}},
		{"no host", "https://", SourceConfigured, nil, []string{"not an absolute http(s) URL"}},
		{"no key, still malformed", "example.com", SourceConfigured, []mod{nokey}, []string{"not an absolute http(s) URL"}},
		{"control characters in the host", "https://evil\x1b[2J.example/v1", SourceEnv, nil, []string{"not an absolute http(s) URL"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := openaiEndpoint(tc.base, tc.src)
			for _, m := range tc.mods {
				m(&e)
			}
			err := CheckEndpoint(e)
			if tc.bad == nil {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("allowed")
			}
			var ee *EndpointError
			if !asEndpointError(err, &ee) {
				t.Fatalf("not an *EndpointError: %T", err)
			}
			for _, want := range tc.bad {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal lacks %q:\n%s", want, err)
				}
			}
			if strings.ContainsAny(err.Error(), "\x1b\a\r") {
				t.Errorf("the refusal carries control characters: %q", err.Error())
			}
		})
	}
}

func asEndpointError(err error, target **EndpointError) bool {
	ee, ok := err.(*EndpointError)
	*target = ee
	return ok
}

// The configuration a refusal suggests is valid JSON that names the provider, and
// following it makes the same endpoint pass.
func TestTheSuggestedConfigurationWorks(t *testing.T) {
	for _, tc := range []struct {
		base string
		src  EndpointSource
	}{
		{"https://proxy.corp.example:8443/v1", SourceEnv},
		{"http://proxy.corp.example/v1", SourceEnv}, // needs both
		{"http://gpu-box.lan:8000/v1", SourceFlag},
		{"https://proxy.corp.example/v1", SourceProject},
	} {
		e := openaiEndpoint(tc.base, tc.src)
		err := CheckEndpoint(e)
		if err == nil {
			t.Fatalf("%s: expected a refusal", tc.base)
		}
		var cfg struct {
			Providers map[string]struct {
				AllowHosts        []string `json:"allow_hosts"`
				AllowInsecureHTTP bool     `json:"allow_insecure_http"`
			} `json:"providers"`
		}
		var found bool
		for _, line := range strings.Split(err.Error(), "\n") {
			if l := strings.TrimSpace(line); strings.HasPrefix(l, "{") && json.Unmarshal([]byte(l), &cfg) == nil {
				found = true
			}
		}
		if !found || cfg.Providers["openai"].AllowHosts == nil && !cfg.Providers["openai"].AllowInsecureHTTP {
			t.Fatalf("%s: no usable configuration in the refusal:\n%s", tc.base, err)
		}
		e.AllowHosts = cfg.Providers["openai"].AllowHosts
		e.AllowInsecureHTTP = cfg.Providers["openai"].AllowInsecureHTTP
		if err := CheckEndpoint(e); err != nil {
			t.Errorf("%s: following the advice does not help: %v", tc.base, err)
		}
	}
}

// A credential in a header is as good as the key: it goes wherever the request goes.
func TestCredentialHeadersAreHeldToTheSameRules(t *testing.T) {
	hdr := func(e *Endpoint) { e.KeyEnv, e.CredentialHeaders = "", []string{"X-Api-Key"} }
	both := func(e *Endpoint) { e.CredentialHeaders = []string{"Authorization", "X-Api-Key"} }
	for _, tc := range []struct {
		name string
		base string
		src  EndpointSource
		mod  func(*Endpoint)
		bad  []string
	}{
		{"header only, env, stranger", "https://collector.attacker.example/v1", SourceEnv, hdr,
			[]string{"refusing to send the credential header X-Api-Key to collector.attacker.example", "allow_hosts", "unset OPENAI_BASE_URL"}},
		{"header only, env, plain http", "http://localhost.attacker.example/v1", SourceEnv, hdr, []string{"the credential header X-Api-Key"}},
		{"header only, project, stranger", "https://collector.attacker.example/v1", SourceProject, hdr, []string{"the credential header X-Api-Key", "--trust-project"}},
		{"header only, configured, plain http to a LAN host", "http://gpu-box.lan:8000/v1", SourceConfigured, hdr,
			[]string{"refusing to send the credential header X-Api-Key over plain http to gpu-box.lan:8000", "allow_insecure_http"}},
		{"key and headers, env, stranger", "https://collector.attacker.example/v1", SourceEnv, both,
			[]string{"the API key from $OPENAI_API_KEY and the credential headers Authorization, X-Api-Key"}},
		{"header only, env, the built-in host", "https://api.openai.com/v1", SourceEnv, hdr, nil},
		{"header only, env, loopback", "http://127.0.0.1:8000/v1", SourceEnv, hdr, nil},
		{"header only, configured, https", "https://gateway.corp.example/v1", SourceConfigured, hdr, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := openaiEndpoint(tc.base, tc.src)
			tc.mod(&e)
			err := CheckEndpoint(e)
			if tc.bad == nil {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("allowed")
			}
			for _, want := range tc.bad {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal lacks %q:\n%s", want, err)
				}
			}
		})
	}
	// The user's exceptions lift the refusal for headers too.
	e := openaiEndpoint("https://proxy.corp.example/v1", SourceEnv)
	hdr(&e)
	e.AllowHosts = []string{"proxy.corp.example"}
	if err := CheckEndpoint(e); err != nil {
		t.Fatalf("a listed host: %v", err)
	}
	// With neither a key nor a credential header there is nothing to protect.
	e = openaiEndpoint("https://collector.attacker.example/v1", SourceEnv)
	e.KeyEnv, e.CredentialHeaders = "", nil
	if err := CheckEndpoint(e); err != nil {
		t.Fatalf("nothing to protect: %v", err)
	}
}

func TestCredentialHeaders(t *testing.T) {
	got := CredentialHeaders(map[string]string{
		"Authorization": "Bearer x", "X-Api-Key": "k", "api-key": "k", "Proxy-Authorization": "Basic x", "Cookie": "a=b",
		"X-Auth-Token": "t", "X-Client-Secret": "s", "X-Goog-Signature": "g",
		"X-Title": "Sleipnir", "User-Agent": "x", "Accept": "application/json", "HTTP-Referer": "https://example.com", "X-Team": "core",
		"X-Empty-Key": "", // no value, nothing carried
	})
	want := []string{"Authorization", "Cookie", "Proxy-Authorization", "X-Api-Key", "X-Auth-Token", "X-Client-Secret", "X-Goog-Signature", "api-key"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("CredentialHeaders = %v, want %v", got, want)
	}
	if got := CredentialHeaders(nil); len(got) != 0 {
		t.Fatalf("nil map: %v", got)
	}
}

func TestEndpointMessagesSanitiseWhatTheyEcho(t *testing.T) {
	e := openaiEndpoint("https://collector.attacker.example/v1", SourceEnv)
	e.Name = "open\x1b[31mai"
	e.KeyEnv = "KEY\x1b]52;c;AAAA\a"
	e.EnvVar = "BASE\x1b[2J"
	e.CredentialHeaders = []string{"X-Key\x1b]52;c;AAAA\a"}
	err := CheckEndpoint(e)
	if err == nil || strings.ContainsAny(err.Error(), "\x1b\a") {
		t.Fatalf("%q", err)
	}
}

// Whatever URL an environment variable holds, CheckEndpoint neither panics nor lets a key
// through to a place the rules forbid, and its message carries no control characters.
func FuzzCheckEndpoint(f *testing.F) {
	for _, s := range []string{
		"https://api.openai.com/v1", "http://collector.attacker.example/v1", "http://[::1]:80/v1", "http://127.0.0.1:8000",
		"https://user:pw@host:99999/x", "//x", "http://\x1b[2J.example", "https://api.openai.com@evil.example/v1",
		"http://127.0.0.1.evil.example", "https://api.openai.com.:443/v1", "HTTPS://API.OPENAI.COM/v1", "http://localhost\x00.evil.example",
		"https://[fe80::1%25eth0]/v1", "http://0x7f000001/v1", "http://2130706433/v1", "http://[::ffff:127.0.0.1]/v1", "",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, base string) {
		for _, src := range []EndpointSource{SourceConfigured, SourceFlag, SourceProject, SourceEnv} {
			e := openaiEndpoint(base, src)
			err := CheckEndpoint(e)
			if err != nil {
				// A refusal is printed on a terminal: valid text, and no control character
				// except the line breaks the message itself uses.
				msg := err.Error()
				if !utf8.ValidString(msg) {
					t.Fatalf("%q from %d: the refusal is not valid UTF-8: %q", base, src, msg)
				}
				for _, r := range msg {
					if r != '\n' && unicode.IsControl(r) {
						t.Fatalf("%q from %d: control character U+%04X in the refusal %q", base, src, r, msg)
					}
				}
				continue
			}
			// Accepted: it must be a well-formed http(s) URL that a key may travel to.
			u, perr := url.Parse(base)
			if perr != nil || u.Hostname() == "" || (!strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https")) {
				t.Fatalf("%q from %d: accepted a URL that is not an absolute http(s) URL", base, src)
			}
			if CheckKeyTransport(base) != nil {
				t.Fatalf("%q from %d: accepted a key over a transport that is not allowed", base, src)
			}
			if (src == SourceEnv || src == SourceProject) && !IsLoopbackHost(u.Hostname()) && !anchoredAt(u, e.Anchors) {
				t.Fatalf("%q from %d: a key was allowed to follow an untrusted URL to a stranger", base, src)
			}
		}
	})
}

func TestValidAllowHost(t *testing.T) {
	for _, ok := range []string{"gateway.example.com", "gateway.example.com:8443", "localhost", "10.0.0.5", "10.0.0.5:80", "[2001:db8::1]", "[2001:db8::1]:8443", "a", "Gateway.Example.COM"} {
		if err := ValidAllowHost(ok); err != nil {
			t.Errorf("ValidAllowHost(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{
		"", " gateway.example.com", "gateway.example.com ", "https://gateway.example.com", "gateway.example.com/v1", "user@gateway.example.com",
		"gateway.example.com?x=1", "gateway.example.com#f", "gateway.example.com:", "gateway.example.com:0", "gateway.example.com:99999",
		"gateway.example.com:http", "a b", "gate\x1bway", ":8080", "[::1", "gateway.example.com\\x",
	} {
		if err := ValidAllowHost(bad); err == nil {
			t.Errorf("ValidAllowHost(%q) accepted", bad)
		}
	}
}
