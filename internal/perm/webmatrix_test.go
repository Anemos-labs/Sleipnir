package perm

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/weburl"
)

// legacyMatchWeb is how a web rule matched a fetch whose input has only "url" before the
// engine read URLs with the tool's own parser: domain rules took the host of the raw
// value ("//" put in front when it had no scheme), URL rules matched the raw value.
func legacyMatchWeb(pattern, raw string) bool {
	switch {
	case pattern == "" || pattern == "*":
		return true
	case strings.HasPrefix(pattern, "domain:"):
		domain := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(strings.TrimPrefix(pattern, "domain:")), "."))
		s := raw
		if !strings.Contains(s, "://") {
			s = "//" + s
		}
		u, err := url.Parse(s)
		if err != nil || u.Hostname() == "" {
			return false
		}
		host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
		if strings.HasPrefix(domain, "*.") {
			return strings.HasSuffix(host, domain[1:])
		}
		return host == domain || strings.HasSuffix(host, "."+domain)
	}
	return raw != "" && wildMatch(pattern, raw)
}

var matrixPatterns = []string{
	"https://docs.example", "https://docs.example/", "https://docs.example/*", "docs.example/*", "docs.example", "*docs.example*",
	"http://docs.example/*", "https://docs.example/a", "https://docs.example/a?*", "*://docs.example/*", "https://*.docs.example/*",
	"domain:docs.example", "domain:*.docs.example", "*", "https://DOCS.example/*", "https://docs.example:443/*",
}

var matrixURLs = []string{
	"https://docs.example", "https://docs.example/", "https://docs.example/a", "https://docs.example/a?x=1", "http://docs.example/a",
	"docs.example", "docs.example/a", "HTTPS://DOCS.EXAMPLE/a", "https://docs.example./a", "https://api.docs.example/a",
	" https://docs.example/a", "https://docs.example:443/a", "https://user@docs.example/", "//docs.example/a",
	"https://evil.example/?docs.example", "ftp://docs.example/a", "https://docs.example/a#frag",
}

// The matrix of web rules against URLs, old matcher against new. An allow rule matches
// what it matched before for every URL the tool fetches, plus the intended gains listed
// below; it stops matching only URLs the tool refuses to fetch (those are asked about). A
// deny or ask rule loses no match at all.
func TestWebRuleMatrixAgainstTheFormerMatcher(t *testing.T) {
	var diff []string
	for _, p := range matrixPatterns {
		r, err := ParseRule(Allow, "WebFetch("+p+")")
		if p == "*" {
			r, err = ParseRule(Allow, "WebFetch")
		}
		if err != nil {
			t.Fatal(err)
		}
		c, err := compileRule(r, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, u := range matrixURLs {
			in, _ := json.Marshal(map[string]string{"url": u})
			req := Request{Tool: "web_fetch", Input: in}
			was, allow, restrict := legacyMatchWeb(p, u), c.matchWeb(req, false), c.matchWeb(req, true)
			switch {
			case was && !allow:
				if _, err := weburl.Parse(u); err == nil {
					t.Errorf("allow %s no longer matches %q, which the tool fetches", p, u)
				}
				diff = append(diff, "- "+p+" | "+u)
			case !was && allow:
				diff = append(diff, "+ "+p+" | "+u)
			}
			if was && !restrict {
				t.Errorf("deny %s no longer matches %q", p, u)
			}
			if allow && !restrict {
				t.Errorf("%s | %q: an allow match must be a restrict match", p, u)
			}
		}
	}
	sort.Strings(diff)
	if strings.Join(diff, "\n") != strings.Join(intendedDifferences, "\n") {
		t.Errorf("allow-rule differences:\n%s\nwant (the intended ones):\n%s", strings.Join(diff, "\n"), strings.Join(intendedDifferences, "\n"))
	}
}

// intendedDifferences are the allow-rule matches that changed ("+" gained, "-" lost).
// Gains: the URL is matched as the tool fetches it (https added to a bare host, scheme
// and host lower case, the trailing dot of the host and the fragment dropped, the space a
// model left trimmed), without the "/" of an empty path, and without its scheme, so a
// pattern written without a scheme ("docs.example/*") matches every scheme, as a domain
// rule does. Losses: only URLs the tool refuses to fetch (another scheme, credentials),
// which are asked about.
var intendedDifferences = []string{
	"+ *://docs.example/* | //docs.example/a",
	"+ *://docs.example/* | HTTPS://DOCS.EXAMPLE/a",
	"+ *://docs.example/* | docs.example",
	"+ *://docs.example/* | docs.example/a",
	"+ *://docs.example/* | https://docs.example",
	"+ *://docs.example/* | https://docs.example./a",
	"+ *docs.example* | HTTPS://DOCS.EXAMPLE/a",
	"+ docs.example | https://docs.example",
	"+ docs.example | https://docs.example/",
	"+ docs.example/* |  https://docs.example/a",
	"+ docs.example/* | //docs.example/a",
	"+ docs.example/* | HTTPS://DOCS.EXAMPLE/a",
	"+ docs.example/* | docs.example",
	"+ docs.example/* | http://docs.example/a",
	"+ docs.example/* | https://docs.example",
	"+ docs.example/* | https://docs.example./a",
	"+ docs.example/* | https://docs.example/",
	"+ docs.example/* | https://docs.example/a",
	"+ docs.example/* | https://docs.example/a#frag",
	"+ docs.example/* | https://docs.example/a?x=1",
	"+ domain:docs.example |  https://docs.example/a",
	"+ domain:docs.example | //docs.example/a",
	"+ https://docs.example | docs.example",
	"+ https://docs.example | https://docs.example/",
	"+ https://docs.example/ | docs.example",
	"+ https://docs.example/ | https://docs.example",
	"+ https://docs.example/* |  https://docs.example/a",
	"+ https://docs.example/* | //docs.example/a",
	"+ https://docs.example/* | HTTPS://DOCS.EXAMPLE/a",
	"+ https://docs.example/* | docs.example",
	"+ https://docs.example/* | docs.example/a",
	"+ https://docs.example/* | https://docs.example",
	"+ https://docs.example/* | https://docs.example./a",
	"+ https://docs.example/a |  https://docs.example/a",
	"+ https://docs.example/a | //docs.example/a",
	"+ https://docs.example/a | HTTPS://DOCS.EXAMPLE/a",
	"+ https://docs.example/a | docs.example/a",
	"+ https://docs.example/a | https://docs.example./a",
	"+ https://docs.example/a | https://docs.example/a#frag",
	"- *://docs.example/* | ftp://docs.example/a",
	"- *docs.example* | ftp://docs.example/a",
	"- *docs.example* | https://user@docs.example/",
	"- domain:docs.example | ftp://docs.example/a",
	"- domain:docs.example | https://user@docs.example/",
}

// A domain rule and a URL name a host in either form, Unicode or ASCII (punycode), and
// meet in the ASCII form the HTTP client sends. A name with no ASCII form is refused by
// the tool and matches no allow rule.
func TestWebRulesAndInternationalisedNames(t *testing.T) {
	f := newFixture(t)
	cases := []struct {
		rule string // "Allow X" or "Deny X"
		url  string
		want Action
	}{
		{"Deny domain:bücher.test", "https://xn--bcher-kva.test/", Deny},
		{"Deny domain:bücher.test", "https://BÜCHER.test/a", Deny},
		{"Deny domain:xn--bcher-kva.test", "https://bücher.test/a", Deny},
		{"Deny domain:*.bücher.test", "https://api.xn--bcher-kva.test/a", Deny},
		{"Deny domain:example.com", "https://ｅｘａｍｐｌｅ。com/", Deny},
		{"Deny https://xn--bcher-kva.test/*", "https://bücher.test/a", Deny},
		{"Allow domain:xn--bcher-kva.test", "https://bücher.test/a", Allow},
		{"Allow domain:bücher.test", "https://xn--bcher-kva.test/a", Allow},
		{"Allow domain:test", "https://bü$cher.test/", Ask},
		{"Allow https://bü$cher.test/*", "https://bü$cher.test/a", Ask},
		{"Deny domain:test", "https://bü$cher.test/", Deny},
	}
	for _, c := range cases {
		action, pattern, _ := strings.Cut(c.rule, " ")
		cfg := Config{Allow: []string{"WebFetch(" + pattern + ")"}}
		if action == "Deny" {
			cfg = Config{Deny: []string{"WebFetch(" + pattern + ")"}, Allow: []string{"WebFetch"}}
		}
		e := f.engine(t, cfg)
		cl := e.Classify(fetchReq(fmt.Sprintf(`{"url":%q}`, c.url)))
		got := cl.Verdict
		if cl.NoOneToAsk {
			got = Ask
		}
		if got != c.want {
			t.Errorf("%s, %s: %v, want %v", c.rule, c.url, got, c.want)
		}
	}
}
