package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// An API key is issued for one provider, and the harness sends it wherever the
// provider's base URL points (credential-bearing request headers configured for the
// provider travel the same way and are held to the same rules). The base URL can
// come from places the user did not
// write: an environment variable (set by a .envrc or a CI job of a repository being
// worked on), a project's configuration file. CheckEndpoint is the rule that stops
// such a URL from carrying a key to a host the user never chose:
//
//   - a key travels over https, or over http to a loopback address (a local
//     server); anything else is refused unless the user allowed it for that
//     provider (AllowInsecureHTTP, from the user's own configuration);
//   - a base URL that came from the environment or a project file must point at a
//     host this provider is known to use (a built-in default, or the URL the user's
//     own configuration gives it), at a loopback address, or at a host the user
//     listed for it (AllowHosts, from the user's own configuration); otherwise the
//     key would follow the URL to a stranger, and the request is refused;
//   - a URL the user typed (a command-line flag) or wrote in their own
//     configuration is theirs to choose, subject to the first rule.
//
// A refusal is an error that says what was refused and how to allow it
// deliberately. Nothing is ever downgraded silently.

// EndpointSource says where a base URL came from, which decides how far it is
// trusted with a key.
type EndpointSource int

const (
	// SourceConfigured: a compiled-in default, the user's own configuration, or
	// code that built the provider. Trusted.
	SourceConfigured EndpointSource = iota
	// SourceFlag: a command-line flag the user typed. Trusted, like the user's own
	// configuration.
	SourceFlag
	// SourceProject: a project-level configuration file (which arrives with a
	// repository) that the user chose to trust.
	SourceProject
	// SourceEnv: an environment variable.
	SourceEnv
)

// Endpoint is one candidate destination for a provider's requests, with what is
// needed to judge it.
type Endpoint struct {
	// Name is the provider's name, for messages and for the configuration snippet a
	// refusal suggests.
	Name string
	// BaseURL is where requests would go.
	BaseURL string
	Source  EndpointSource
	// EnvVar is the environment variable BaseURL came from (SourceEnv).
	EnvVar string
	// KeyEnv names the variable holding the API key that would be sent to BaseURL.
	// Empty means no key is sent.
	KeyEnv string
	// CredentialHeaders names the configured request headers that carry a credential
	// (see CredentialHeaders): they go wherever the key goes, so they are held to the
	// same rules. With neither a key nor such a header, nothing needs protecting and
	// only the shape of the URL is checked.
	CredentialHeaders []string
	// Anchors are base URLs that are trusted for this provider whatever the source
	// of BaseURL: the compiled-in default of a built-in provider of this name, and
	// the URL the user's own configuration gives it.
	Anchors []string
	// AllowHosts and AllowInsecureHTTP are the user's deliberate exceptions. They
	// must come from the user's own configuration, never from a project file.
	AllowHosts        []string
	AllowInsecureHTTP bool
}

// EndpointError is the refusal of an endpoint. Error() explains it and says how to
// lift it.
type EndpointError struct {
	msg string
}

func (e *EndpointError) Error() string { return e.msg }

// CheckEndpoint applies the rules above. It returns nil when the endpoint may
// receive the provider's key (or when there is no key to protect and the URL is
// well formed), and an *EndpointError otherwise.
func CheckEndpoint(e Endpoint) error {
	u, err := url.Parse(e.BaseURL)
	if err != nil || u.Hostname() == "" || (!strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https")) {
		return &EndpointError{msg: fmt.Sprintf("provider %q: the base URL%s is not an absolute http(s) URL such as https://api.example.com/v1",
			SanitizeText(e.Name, 64), e.sourceClause())}
	}
	what := e.credential()
	if what == "" {
		return nil
	}
	host := SanitizeText(u.Host, 256)
	loopback := IsLoopbackHost(u.Hostname())
	untrusted := e.Source == SourceEnv || e.Source == SourceProject

	foreign := untrusted && !loopback && !anchoredAt(u, e.Anchors) && !hostListed(u, e.AllowHosts)
	insecure := !e.AllowInsecureHTTP && CheckKeyTransport(e.BaseURL) != nil

	switch {
	case foreign:
		var b strings.Builder
		fmt.Fprintf(&b, "refusing to send %s to %s: provider %q is pointed there by %s, and that is not a host this provider is known to use%s.\n",
			what, host, SanitizeText(e.Name, 64), e.sourceName(), knownHosts(e.Anchors))
		switch e.Source {
		case SourceEnv:
			b.WriteString("Environment variables can be set by a project you did not write (a .envrc, CI settings), and the credential was issued for another host.\n")
		default:
			b.WriteString("A project's configuration arrives with the repository, which may not be yours, and the credential was issued for another host.\n")
		}
		b.WriteString("To use it deliberately, allow the host in your user configuration (~/.sleipnir/config.json):\n  ")
		b.WriteString(snippet(e.Name, map[string]any{"allow_hosts": []string{u.Host}}, insecure))
		b.WriteString("\n")
		switch e.Source {
		case SourceEnv:
			fmt.Fprintf(&b, "or unset %s.", SanitizeText(e.EnvVar, 64))
		default:
			b.WriteString("or run without --trust-project.")
		}
		return &EndpointError{msg: b.String()}
	case insecure:
		return &EndpointError{msg: fmt.Sprintf("refusing to send %s over plain http to %s: it would cross the network unencrypted.\n"+
			"Use an https URL (a loopback address is fine over http), or allow it deliberately in your user configuration (~/.sleipnir/config.json):\n  %s",
			what, host, snippet(e.Name, map[string]any{"allow_insecure_http": true}, false))}
	}
	return nil
}

// credential describes what would be sent to the endpoint, for the refusal: the API
// key, the credential headers, both, or "" when there is nothing to protect.
func (e Endpoint) credential() string {
	var parts []string
	if e.KeyEnv != "" {
		parts = append(parts, "the API key from $"+SanitizeText(e.KeyEnv, 64))
	}
	if len(e.CredentialHeaders) > 0 {
		names := make([]string, len(e.CredentialHeaders))
		for i, h := range e.CredentialHeaders {
			names[i] = SanitizeText(h, 64)
		}
		noun := "the credential header "
		if len(names) > 1 {
			noun = "the credential headers "
		}
		parts = append(parts, noun+strings.Join(names, ", "))
	}
	return strings.Join(parts, " and ")
}

// credentialWords are the fragments of a header name that mark it as carrying a
// credential.
var credentialWords = []string{"auth", "key", "token", "secret", "password", "passwd", "credential", "cookie", "signature"}

// CredentialHeaders returns, sorted, the names among the request headers h that carry
// a credential: Authorization, X-Api-Key, a cookie, anything named like a token or a
// secret. A header with an empty value carries nothing. The match is by name and
// errs towards protecting: a false positive only asks the user to allow a host they
// pointed a header-bearing provider at through the environment.
func CredentialHeaders(h map[string]string) []string {
	var out []string
	for name, v := range h {
		if v == "" {
			continue
		}
		low := strings.ToLower(name)
		for _, w := range credentialWords {
			if strings.Contains(low, w) {
				out = append(out, name)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

func (e Endpoint) sourceName() string {
	switch e.Source {
	case SourceEnv:
		return "the environment variable " + SanitizeText(e.EnvVar, 64)
	case SourceProject:
		return "the project's configuration (.sleipnir/config.json), which is not your own file"
	case SourceFlag:
		return "a command-line flag"
	}
	return "its configuration"
}

func (e Endpoint) sourceClause() string {
	if e.Source == SourceEnv && e.EnvVar != "" {
		return " from " + SanitizeText(e.EnvVar, 64)
	}
	return ""
}

// knownHosts renders the hosts the provider is known to use, for the refusal.
func knownHosts(anchors []string) string {
	var hosts []string
	for _, a := range anchors {
		if u, err := url.Parse(a); err == nil && u.Host != "" {
			hosts = append(hosts, SanitizeText(u.Host, 128))
		}
	}
	if len(hosts) == 0 {
		return " (it has no default host)"
	}
	return " (" + strings.Join(hosts, ", ") + ")"
}

// snippet renders the configuration that would allow what was refused. plainToo
// adds allow_insecure_http, for a host that also needs it.
func snippet(name string, fields map[string]any, plainToo bool) string {
	if plainToo {
		fields["allow_insecure_http"] = true
	}
	b, err := json.Marshal(map[string]any{"providers": map[string]any{name: fields}})
	if err != nil {
		return "{}"
	}
	return string(b)
}

// anchoredAt reports whether u is a place one of the trusted base URLs already
// names: the same host, on the same port (or both on their scheme's default port,
// so http://api.example.com and https://api.example.com are the same host; the
// transport rule, not this one, judges the plain-http downgrade).
func anchoredAt(u *url.URL, anchors []string) bool {
	for _, a := range anchors {
		au, err := url.Parse(a)
		if err != nil || hostOf(au) != hostOf(u) {
			continue
		}
		if portOf(au) == portOf(u) || (defaultPort(au) && defaultPort(u)) {
			return true
		}
	}
	return false
}

// defaultPort reports whether u names no port, or its scheme's default one.
func defaultPort(u *url.URL) bool {
	p := u.Port()
	return p == "" || (p == "80" && strings.EqualFold(u.Scheme, "http")) || (p == "443" && strings.EqualFold(u.Scheme, "https"))
}

// hostListed reports whether u's host matches an AllowHosts entry: "host" (any
// port) or "host:port", compared case-insensitively.
func hostListed(u *url.URL, entries []string) bool {
	host, port := hostOf(u), portOf(u)
	for _, ent := range entries {
		eh, ep := splitHostPort(strings.TrimSpace(ent))
		if eh != "" && strings.EqualFold(strings.TrimSuffix(eh, "."), host) && (ep == "" || ep == port) {
			return true
		}
	}
	return false
}

// splitHostPort splits "host", "host:port", "[v6]" and "[v6]:port"; a bare IPv6
// literal without brackets is a host.
func splitHostPort(s string) (host, port string) {
	if h, p, err := net.SplitHostPort(s); err == nil {
		return h, p
	}
	return strings.Trim(s, "[]"), ""
}

// ValidAllowHost reports whether s is a usable AllowHosts entry: a host name or IP
// literal with an optional port, and nothing else (no scheme, credentials or path).
func ValidAllowHost(s string) error {
	if s == "" || strings.TrimSpace(s) != s {
		return errors.New("must not be empty or padded with spaces")
	}
	if strings.ContainsAny(s, "/\\@?# \t\r\n") || strings.Contains(s, "://") {
		return errors.New("must be a host such as gateway.example.com or gateway.example.com:8443, not a URL")
	}
	u, err := url.Parse("//" + s)
	if err != nil || u.Hostname() == "" || u.Host != s {
		return errors.New("is not a valid host name or address")
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
			return errors.New("has an invalid port")
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return errors.New("has an empty port")
	}
	return nil
}
