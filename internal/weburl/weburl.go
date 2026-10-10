// Package weburl is the one reading of a URL a model asks the web_fetch tool for: the
// tool fetches what Parse returns, and the permission engine judges the same value, so
// a rule about a host can never be matched against one URL while another is fetched.
package weburl

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// MaxLen bounds the URL a model may pass, in bytes.
const MaxLen = 8192

// schemeLike recognises a leading "scheme:".
var schemeLike = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)

// Parse validates and normalises what the model passed: surrounding white space is
// trimmed, control characters inside are refused, a missing scheme means https, only
// http and https are fetched, the URL must name a host and must not carry credentials,
// and the fragment is dropped. It is tolerant of a missing scheme ("example.com/page")
// and strict about everything that could change where the request goes.
func Parse(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) > MaxLen {
		return nil, fmt.Errorf("the URL is %d characters long; the limit is %d", len(raw), MaxLen)
	}
	if strings.ContainsAny(raw, "\r\n\t\x00") {
		return nil, errors.New("the URL contains control characters")
	}
	switch {
	case strings.HasPrefix(raw, "//"):
		raw = "https:" + raw
	case strings.Contains(raw, "://"):
	case schemeLike.MatchString(raw) && !startsWithPort(raw[strings.IndexByte(raw, ':')+1:]):
		scheme := raw[:strings.IndexByte(raw, ':')]
		return nil, fmt.Errorf("unsupported URL scheme %q: only http and https are allowed", scheme)
	default:
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		msg := err.Error()
		if len(msg) > 200 {
			msg = msg[:200] + "…"
		}
		return nil, fmt.Errorf("invalid URL: %s", msg)
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

// startsWithPort tells "localhost:8080/x" (host and port) from "mailto:x@y" (scheme and
// path).
func startsWithPort(rest string) bool {
	return rest != "" && rest[0] >= '0' && rest[0] <= '9'
}

// Host is the host a parsed URL goes to, as rules name hosts: lower case, without the
// trailing dot of a fully qualified name.
func Host(u *url.URL) string {
	return strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
}

// Canonical is the URL as rules about URLs see it: the scheme and host in lower case
// (the host without a trailing dot, with its port when it names one), then the path and
// query as the request sends them.
func Canonical(u *url.URL) string {
	host := Host(u)
	if strings.Contains(host, ":") {
		host = "[" + host + "]" // an IPv6 address
	}
	if p := u.Port(); p != "" {
		host += ":" + p
	}
	return strings.ToLower(u.Scheme) + "://" + host + u.RequestURI()
}
