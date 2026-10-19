package weburl

import "strings"

// localSuffixes are the name suffixes of hosts on a local network or in a private namespace: the names RFC 6761 keeps for
// this machine (.localhost), mDNS (.local), the namespace of private networks and of cloud metadata services (.internal,
// metadata.google.internal), the common router-assigned suffix (.lan) and the home network (.home.arpa).
var localSuffixes = []string{".localhost", ".local", ".internal", ".lan", ".home.arpa"}

// LocalHost returns why host, as Host returns it (lower case, no trailing dot; brackets are tolerated), is not a public
// internet host, or "" when it reads as one. An IP literal, in any spelling LiteralIP accepts, is judged by BlockedAddr;
// a numeric host that is no valid address is not public; a name is local when it is "localhost", a single label
// ("intranet", which a resolver completes with a local search domain) or ends in one of localSuffixes. The judgement is
// lexical: no name is resolved, so a public name whose DNS answer is a private address reads as public here (the
// web_fetch dialer still refuses to connect to it).
func LocalHost(host string) string {
	host = strings.TrimSuffix(strings.ToLower(strings.Trim(host, "[]")), ".")
	if i := strings.IndexByte(host, '%'); i >= 0 {
		host = host[:i] // an IPv6 zone
	}
	if host == "" {
		return "no host"
	}
	addr, isIP, err := LiteralIP(host)
	switch {
	case err != nil:
		return "not a valid IP address"
	case isIP:
		if why := BlockedAddr(addr); why != "" {
			return "a " + why + " address"
		}
		return ""
	case host == "localhost":
		return "this machine"
	case !strings.Contains(host, "."):
		return "a single-label name of the local network"
	}
	for _, s := range localSuffixes {
		if strings.HasSuffix(host, s) {
			return "a name of the local network (" + s + ")"
		}
	}
	return ""
}
