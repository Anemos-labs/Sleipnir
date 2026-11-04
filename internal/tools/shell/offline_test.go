package shell

import "testing"

func TestNetworkUnreachable(t *testing.T) {
	for out, want := range map[string]bool{
		"go: github.com/x/y@v1.0.0: Get \"https://proxy.golang.org/github.com/x/y/@v/v1.0.0.mod\": dial tcp: lookup proxy.golang.org on 127.0.0.53:53: no such host": true,
		"dial tcp: lookup proxy.golang.org: server misbehaving":                                                       true,
		"curl: (6) Could not resolve host: example.com":                                                               true,
		"fatal: unable to access 'https://github.com/a/b/': Could not resolve host":                                   true,
		"npm ERR! request to https://registry.npmjs.org/x failed, reason: getaddrinfo ENOTFOUND registry.npmjs.org":   true,
		"WARNING: Retrying ... Failed to establish a new connection: [Errno -3] Temporary failure in name resolution": true,
		"connect: Network is unreachable":                                                                             true,
		"--- FAIL: TestLookup\n    lookup_test.go:12: got 3, want 4":                                                  false,
		"open config.yaml: no such file or directory":                                                                 false,
		"": false,
	} {
		if got := networkUnreachable(out); got != want {
			t.Errorf("networkUnreachable(%q) = %v, want %v", out, got, want)
		}
	}
}
