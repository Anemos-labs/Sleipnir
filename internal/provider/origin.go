package provider

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// A model request is the whole conversation (source code, tool output, notes)
// plus headers that may carry credentials. It goes to one origin, the configured
// endpoint, and nowhere else: not by redirect, not over plain http when a key is
// attached.

// MaxRedirects is how many same-origin redirects a client follows.
const MaxRedirects = 4

// RedirectError is returned by the redirect policy when an endpoint tries to send
// the request to another origin.
type RedirectError struct {
	// Target is scheme://host[:port] of the refused redirect: never a path, a query
	// or credentials.
	Target string
}

func (e *RedirectError) Error() string {
	return "the endpoint redirected the request to another origin (" + e.Target +
		"); the prompt and headers are never re-sent to a different origin"
}

// AsRedirect finds a refused redirect in err (net/http wraps it in a *url.Error).
func AsRedirect(err error) (*RedirectError, bool) {
	var re *RedirectError
	if errors.As(err, &re) {
		return re, true
	}
	return nil, false
}

// RedirectFailure converts a refused redirect into the provider error callers
// return. It is not retried: the endpoint would answer the same again.
func RedirectFailure(re *RedirectError) *Error {
	return &Error{Kind: ErrBadRequest, Message: re.Error(), Err: re, NoRetry: true}
}

// SameOriginRedirects is an http.Client CheckRedirect that follows a redirect
// only while it stays on the origin (scheme, host and port) of the original
// request, and only a few times. net/http on its own re-sends the body to the new
// host and forwards every header except Authorization and Cookie, so a redirect
// from a gateway would hand the prompt and any custom key header to a third
// party. A refused redirect is not followed at all: nothing is sent to the
// target.
func SameOriginRedirects(req *http.Request, via []*http.Request) error {
	if len(via) > MaxRedirects {
		return fmt.Errorf("stopped after %d redirects", MaxRedirects)
	}
	if len(via) == 0 {
		return nil
	}
	if !SameOrigin(via[0].URL, req.URL) {
		return &RedirectError{Target: SanitizeText(originOf(req.URL), 256)}
	}
	return nil
}

// HardenClient returns a copy of hc whose redirect policy is SameOriginRedirects
// (after hc's own policy, if it has one, so it can only get stricter). A nil hc
// gets a plain client. hc itself is not modified.
func HardenClient(hc *http.Client) *http.Client {
	var c http.Client
	if hc != nil {
		c = *hc
	}
	inner := c.CheckRedirect
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if inner != nil {
			if err := inner(req, via); err != nil {
				return err
			}
		}
		return SameOriginRedirects(req, via)
	}
	return &c
}

// SameOrigin reports whether two URLs share scheme, host and port (default ports
// filled in, host compared case-insensitively).
func SameOrigin(a, b *url.URL) bool {
	if a == nil || b == nil {
		return false
	}
	return strings.EqualFold(a.Scheme, b.Scheme) && hostOf(a) == hostOf(b) && portOf(a) == portOf(b)
}

func hostOf(u *url.URL) string { return strings.TrimSuffix(strings.ToLower(u.Hostname()), ".") }

func portOf(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	switch strings.ToLower(u.Scheme) {
	case "http":
		return "80"
	case "https":
		return "443"
	}
	return ""
}

func originOf(u *url.URL) string {
	return strings.ToLower(u.Scheme) + "://" + u.Host
}

// IsLoopbackHost reports whether host (no port) names this machine: "localhost"
// or a loopback IP literal (127.0.0.0/8, ::1, and their IPv4-mapped forms). Other
// spellings (127.1, 0x7f000001, 0.0.0.0, names that merely resolve to loopback)
// are not recognised: this decides whether a credential may travel unencrypted,
// so it errs on the side of "no".
func IsLoopbackHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// InsecureKeyError says that an API key was about to be sent over plain http to a
// host other than this machine.
type InsecureKeyError struct{ Host string }

func (e *InsecureKeyError) Error() string {
	return "refusing to send an API key over plain http to " + SanitizeText(e.Host, 256) +
		": the key would cross the network unencrypted"
}

// CheckKeyTransport reports whether an API key may be sent to baseURL: https
// anywhere, or http to a loopback host (a local server). Everything else, and any
// URL that does not parse as an absolute http(s) URL, is refused. Callers that
// need an exception (a trusted LAN proxy) make it deliberately, before calling.
func CheckKeyTransport(baseURL string) error {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return errors.New("the base URL is not an absolute http(s) URL")
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return nil
	case "http":
		if IsLoopbackHost(u.Hostname()) {
			return nil
		}
		return &InsecureKeyError{Host: u.Hostname()}
	}
	return fmt.Errorf("the base URL scheme %q is not http or https", SanitizeText(u.Scheme, 32))
}

// RedactURL renders raw for messages and logs: scheme, host and path only, with
// no user name, password, query or fragment.
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "<invalid URL>"
	}
	return SanitizeText(strings.ToLower(u.Scheme)+"://"+u.Host+u.EscapedPath(), 512)
}
