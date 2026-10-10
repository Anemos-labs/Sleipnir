package weburl

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// legacyParse is web_fetch's parser as it was before this package existed, kept here
// as the reference the shared parser must agree with.
func legacyParse(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) > 8192 {
		return nil, fmt.Errorf("the URL is %d characters long; the limit is %d", len(raw), 8192)
	}
	if strings.ContainsAny(raw, "\r\n\t\x00") {
		return nil, errors.New("the URL contains control characters")
	}
	scheme := regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)
	port := func(rest string) bool { return rest != "" && rest[0] >= '0' && rest[0] <= '9' }
	switch {
	case strings.HasPrefix(raw, "//"):
		raw = "https:" + raw
	case strings.Contains(raw, "://"):
	case scheme.MatchString(raw) && !port(raw[strings.IndexByte(raw, ':')+1:]):
		return nil, fmt.Errorf("unsupported URL scheme %q: only http and https are allowed", raw[:strings.IndexByte(raw, ':')])
	default:
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %s", err)
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

// corpus are the spellings a model (or an attack) may send.
var corpus = []string{
	"https://docs.example/page", "http://docs.example", "docs.example/page", "//docs.example/x",
	" https://docs.example/", "\thttps://evil.example/", "\nhttps://evil.example/", "https://evil.example/\n", " https://evil.example/",
	" 　https://evil.example/", "\x00https://evil.example/", "https://ev\til.example/", "https://evil.example/\x00",
	"HTTP://EVIL.EXAMPLE/X", "HtTpS://Docs.Example./", "https://docs.example./page", "https://docs.example../x",
	"http://user@docs.example/", "http://user:pw@docs.example/", "http://docs.example@evil.example/", "https://@evil.example/",
	"http://[::1]/", "http://[::1]:8080/x", "https://[2001:db8::1]./", "http://127.0.0.1:9/", "localhost:8080/x", "docs.example:443/x",
	"https://docs.example:8443/a?b=c#frag", "https://ex%61mple.com/", "https://example.com/%2e%2e/x", "https://xn--bcher-kva.example/",
	"https://bücher.example/", "https://ДОМЕН.example/", "mailto:a@b", "ftp://files.example/", "javascript:alert(1)", "file:///etc/passwd",
	"", "   ", "https://", "http:///x", "https://docs.example\\@evil.example/", "https://docs.example%00.evil/", strings.Repeat("a", 9000),
	"https://evil.example#@docs.example/", "https://evil.example?@docs.example/", "https://docs.example.evil.example/",
}

func TestParseAgreesWithTheToolsFormerParser(t *testing.T) {
	for _, raw := range corpus {
		u, err := Parse(raw)
		lu, lerr := legacyParse(raw)
		if (err == nil) != (lerr == nil) {
			t.Errorf("%q: Parse err=%v, former err=%v", raw, err, lerr)
			continue
		}
		if err != nil {
			continue
		}
		// The one intended difference: an internationalised host is in its ASCII form.
		if h := lu.Hostname(); !isASCII(h) {
			a, _ := ASCIIName(h)
			lu.Host = a
		}
		if u.String() != lu.String() {
			t.Errorf("%q: Parse %q, former %q", raw, u, lu)
		}
		if got, want := Host(u), strings.ToLower(strings.TrimSuffix(lu.Hostname(), ".")); got != want {
			t.Errorf("%q: Host %q, want %q", raw, got, want)
		}
	}
}

func TestHostAndCanonical(t *testing.T) {
	for _, c := range []struct{ raw, host, canon string }{
		{"HTTP://Docs.Example./A?b=1#f", "docs.example", "http://docs.example/A?b=1"},
		{"docs.example:8443/x", "docs.example", "https://docs.example:8443/x"},
		{"http://[::1]:8080/x", "::1", "http://[::1]:8080/x"},
		{"\t https://evil.example", "evil.example", "https://evil.example/"},
	} {
		u, err := Parse(c.raw)
		if err != nil {
			t.Fatalf("%q: %v", c.raw, err)
		}
		if Host(u) != c.host || Canonical(u) != c.canon {
			t.Errorf("%q: host %q canonical %q, want %q %q", c.raw, Host(u), Canonical(u), c.host, c.canon)
		}
	}
	for _, bad := range []string{"\x00https://evil.example/", "http://docs.example@evil.example/", "ftp://x/", "https://", strings.Repeat("a", MaxLen+1)} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
}

// Internationalised names are compared and fetched in their ASCII form (RFC 3492 vectors
// and Python's punycode codec).
func TestASCIIName(t *testing.T) {
	for _, c := range []struct {
		in, want string
		ok       bool
	}{
		{"bücher.test", "xn--bcher-kva.test", true},
		{"BÜCHER.Test", "xn--bcher-kva.test", true},
		{"münchen.example.", "xn--mnchen-3ya.example.", true},
		{"例え.テスト", "xn--r8jz45g.xn--zckzah", true},
		{"他们为什么不说中文", "xn--ihqwcrb4cv8a8dqg056pqjye", true},
		{"straße.test", "xn--strae-oqa.test", true},
		{"ελληνικά.test", "xn--hxargifdar.test", true},
		{"правительство.рф", "xn--80aealotwbjpid2k.xn--p1ai", true},
		{"ｅｘａｍｐｌｅ。com", "example.com", true},
		{"Docs.Example", "docs.example", true},
		{"xn--bcher-kva.test", "xn--bcher-kva.test", true},
		{"bü$cher.test", "bü$cher.test", false},
		{"bü cher.test", "bü cher.test", false},
		{"", "", false},
	} {
		got, ok := ASCIIName(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("ASCIIName(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

// Parse hands the HTTP client the ASCII name the rules judged, and refuses a name that has
// none.
func TestParseInternationalisedHosts(t *testing.T) {
	for raw, want := range map[string]string{
		"https://Bücher.test:8443/a?b=ü": "https://xn--bcher-kva.test:8443/a?b=ü",
		"bücher.test/x":                  "https://xn--bcher-kva.test/x",
		"https://ｅｘａｍｐｌｅ。com/":           "https://example.com/",
	} {
		u, err := Parse(raw)
		if err != nil {
			t.Fatalf("%q: %v", raw, err)
		}
		if u.String() != want {
			t.Errorf("%q: %q, want %q", raw, u, want)
		}
		if h, ok := HostChecked(u); !ok || !isASCII(h) {
			t.Errorf("%q: host %q, %v", raw, h, ok)
		}
	}
	if _, err := Parse("https://bü$cher.test/"); err == nil {
		t.Error("a host IDNA refuses must be refused")
	}
}
