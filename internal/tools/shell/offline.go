package shell

import "strings"

// networkUnreachable reports whether the output of a command that failed says that the network could not be reached: a name that
// did not resolve or a host or network that could not be reached, in the words of the common resolvers and package managers (go,
// curl, git, pip, npm). Behind a sandbox or a firewall that blocks the network, a model otherwise retries the download, the proxy
// and the DNS lookup in turn, and spends its first minutes on it.
func networkUnreachable(out string) bool {
	low := strings.ToLower(out)
	for _, s := range []string{
		"temporary failure in name resolution", // glibc: getaddrinfo EAI_AGAIN
		"name or service not known",            // glibc: EAI_NONAME
		"could not resolve host",               // curl, git
		"could not resolve proxy",              // curl
		"no address associated with hostname",  // glibc
		"network is unreachable",               // ENETUNREACH
		"getaddrinfo enotfound",                // node, npm
		"getaddrinfo eai_again",                // node, npm
		"failed to establish a new connection", // pip, requests
		"nodename nor servname provided",       // macOS resolver
	} {
		if strings.Contains(low, s) {
			return true
		}
	}
	// go: "dial tcp: lookup proxy.golang.org on 127.0.0.53:53: no such host" (or ": server misbehaving")
	return strings.Contains(low, "dial tcp: lookup ") && (strings.Contains(low, "no such host") || strings.Contains(low, "server misbehaving"))
}

const offlineNote = "[note: the network looks unreachable from here (a name did not resolve or a host could not be reached); retrying, " +
	"or another proxy or mirror, is unlikely to help. Work with what is installed: Go with GOFLAGS=-mod=mod GOPROXY=off uses the " +
	"module cache, npm with --offline, pip with --no-index; or use the standard library, and say in your report what needed the network]"
