// Package weburl is the one reading of a URL a model asks the web_fetch tool for: the
// tool fetches what Parse returns, and the permission engine judges the same value, so
// a rule about a host can never be matched against one URL while another is fetched.
package weburl

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxLen bounds the URL a model may pass, in bytes.
const MaxLen = 8192

// schemeLike recognises a leading "scheme:".
var schemeLike = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)

// Parse validates and normalises what the model passed: surrounding white space is
// trimmed, control characters inside are refused, a missing scheme means https, only
// http and https are fetched, the URL must name a host and must not carry credentials,
// an internationalised host is put in its ASCII form (ASCIIName; a host that has none is
// refused), and the fragment is dropped. It is tolerant of a missing scheme ("example.com/page")
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
	if err := ToASCII(u); err != nil {
		return nil, err
	}
	u.Fragment, u.RawFragment = "", ""
	return u, nil
}

// startsWithPort tells "localhost:8080/x" (host and port) from "mailto:x@y" (scheme and
// path).
func startsWithPort(rest string) bool {
	return rest != "" && rest[0] >= '0' && rest[0] <= '9'
}

// ToASCII puts an internationalised host of u in its ASCII form (ASCIIName), keeping the
// port, so that the HTTP client sends the name the rules judged; a host that has no ASCII
// form is an error and u is left as it was.
func ToASCII(u *url.URL) error {
	h := u.Hostname()
	if isASCII(h) {
		return nil
	}
	a, ok := ASCIIName(h)
	if !ok {
		return fmt.Errorf("the host %q is not a valid internationalised domain name", clipName(h))
	}
	if p := u.Port(); p != "" {
		a += ":" + p
	}
	u.Host = a
	return nil
}

// Host is the host a parsed URL goes to, as rules name hosts: lower case, without the
// trailing dot of a fully qualified name, an internationalised name in its ASCII
// (punycode) form, as the HTTP client sends it. A name that cannot be converted is
// returned lower case as it is (HostChecked says so).
func Host(u *url.URL) string {
	h, _ := HostChecked(u)
	return h
}

// HostChecked is Host and whether the name is an IP address or a name whose ASCII form
// could be made: a rule that allows a domain matches only such a name.
func HostChecked(u *url.URL) (string, bool) {
	h := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if net.ParseIP(h) != nil {
		return h, true
	}
	return ASCIIName(h)
}

// ASCIIName is a domain name in the ASCII form DNS and HTTP use: lower case, each label
// that holds other characters encoded with punycode ("bücher.test" is
// "xn--bcher-kva.test"), after the full-width forms of ASCII characters and the
// ideographic full stops are folded (foldWidth). A label of characters a host name
// cannot hold (not letters, digits, marks or "-") is refused: the name is returned lower
// case as given, and false; so is "". The mapping is IDNA's for the common case; it does
// not apply Unicode normalisation, so Parse hands the HTTP client the ASCII form it
// judged, and the client sends that name as it is.
func ASCIIName(name string) (string, bool) {
	name = strings.ToLower(name)
	if name == "" || isASCII(name) {
		return name, name != ""
	}
	folded := strings.Map(foldWidth, name)
	labels := strings.Split(folded, ".")
	for i, l := range labels {
		if isASCII(l) {
			continue
		}
		for _, r := range l {
			if r != '-' && !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.IsMark(r) {
				return name, false
			}
		}
		enc, good := punycode(l)
		if !good {
			return name, false
		}
		labels[i] = "xn--" + enc
	}
	return strings.Join(labels, "."), true
}

// foldWidth maps the full-width forms of ASCII characters to ASCII and the ideographic
// full stops to ".", as IDNA's mapping does: "ｅｘａｍｐｌｅ。com" is "example.com".
func foldWidth(r rune) rune {
	switch {
	case r == '\u3002' || r == '\uff0e' || r == '\uff61':
		return '.'
	case r >= '\uff01' && r <= '\uff5e':
		return unicode.ToLower(r - 0xfee0)
	}
	return r
}

// isASCII reports whether s holds only ASCII characters.
func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// clipName shortens a host name for an error message.
func clipName(s string) string {
	if len(s) > 100 {
		return strings.ToValidUTF8(s[:100], "") + "…"
	}
	return s
}

// Punycode parameters (RFC 3492).
const (
	pBase        = 36
	pTMin        = 1
	pTMax        = 26
	pSkew        = 38
	pDamp        = 700
	pInitialBias = 72
	pInitialN    = 128
)

// punycode encodes one label (RFC 3492); false on overflow.
func punycode(label string) (string, bool) {
	runes := []rune(label)
	var out []byte
	for _, r := range runes {
		if r < 0x80 {
			out = append(out, byte(r))
		}
	}
	b := len(out)
	h := b
	if b > 0 {
		out = append(out, '-')
	}
	n, delta, bias := pInitialN, 0, pInitialBias
	for h < len(runes) {
		m := -1
		for _, r := range runes {
			if int(r) >= n && (m < 0 || int(r) < m) {
				m = int(r)
			}
		}
		if m < 0 || (m-n) > (1<<30)/(h+1) {
			return "", false
		}
		delta += (m - n) * (h + 1)
		n = m
		for _, r := range runes {
			if int(r) < n {
				delta++
				if delta > 1<<30 {
					return "", false
				}
			}
			if int(r) != n {
				continue
			}
			q := delta
			for k := pBase; ; k += pBase {
				t := k - bias
				if t < pTMin {
					t = pTMin
				} else if t > pTMax {
					t = pTMax
				}
				if q < t {
					break
				}
				out = append(out, punyDigit(t+(q-t)%(pBase-t)))
				q = (q - t) / (pBase - t)
			}
			out = append(out, punyDigit(q))
			bias = punyAdapt(delta, h+1, h == b)
			delta = 0
			h++
		}
		delta++
		n++
	}
	return string(out), true
}

// punyDigit is the character of a punycode digit.
func punyDigit(d int) byte {
	if d < 26 {
		return byte('a' + d)
	}
	return byte('0' + d - 26)
}

// punyAdapt is RFC 3492's bias adaptation.
func punyAdapt(delta, points int, first bool) int {
	if first {
		delta /= pDamp
	} else {
		delta /= 2
	}
	delta += delta / points
	k := 0
	for delta > ((pBase-pTMin)*pTMax)/2 {
		delta /= pBase - pTMin
		k += pBase
	}
	return k + (pBase-pTMin+1)*delta/(delta+pSkew)
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
