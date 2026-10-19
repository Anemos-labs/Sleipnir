package perm

import "testing"

// webFetch is a request of the built-in web_fetch tool for url.
func webFetch(url string) rq {
	return rq{tool: "web_fetch", network: true, input: `{"url":"` + url + `"}`}
}

// webSearch is a request of the built-in web_search tool.
func webSearch(query string) rq {
	return rq{tool: "web_search", network: true, input: `{"query":"` + query + `"}`}
}

// accept-edits mode reads public websites with the web tools without asking. A host on this machine, the local network or a cloud
// metadata service still asks; so does every other way to reach the network. default mode still asks, plan mode still refuses, and
// a deny rule still wins.
func TestAcceptEditsReadsPublicWebsites(t *testing.T) {
	f := newFixture(t)
	const reading = "accept-edits mode: reading a public website"
	cases := []tc{
		{name: "web_fetch of a public host", mode: ModeAcceptEdits, req: webFetch("https://example.com"), want: "allow", why: reading},
		{name: "WebFetch spelling of the tool", mode: ModeAcceptEdits, req: fetch("https://docs.example.org/a?b=c"), want: "allow", why: reading},
		{name: "public IP literal", mode: ModeAcceptEdits, req: webFetch("https://93.184.216.34/"), want: "allow", why: reading},
		{name: "web_search", mode: ModeAcceptEdits, req: webSearch("go netip"), want: "allow", why: reading},
		{name: "WebSearch spelling of the tool", mode: ModeAcceptEdits, req: rq{tool: "WebSearch", network: true, input: `{"query":"x"}`}, want: "allow", why: reading},

		{name: "default mode asks for a public host", req: webFetch("https://example.com"), want: "ask", why: "example.com needs approval"},
		{name: "default mode asks for a search", req: webSearch("x"), want: "ask"},
		{name: "plan mode refuses a public host", mode: ModePlan, req: webFetch("https://example.com"), want: "deny", why: "plan mode"},
		{name: "plan mode refuses a search", mode: ModePlan, req: webSearch("x"), want: "deny", why: "plan mode"},

		{name: "a deny rule for the domain wins", mode: ModeAcceptEdits, deny: []string{"WebFetch(domain:example.com)"}, req: webFetch("https://example.com/x"), want: "deny", why: "denied by rule"},
		{name: "a deny rule covers a subdomain", mode: ModeAcceptEdits, deny: []string{"WebFetch(domain:example.com)"}, req: webFetch("https://docs.example.com/x"), want: "deny"},
		{name: "a deny rule on the search tool wins", mode: ModeAcceptEdits, deny: []string{"WebSearch"}, req: webSearch("x"), want: "deny", why: "denied by rule"},
		{name: "an ask rule for the domain still asks", mode: ModeAcceptEdits, ask: []string{"WebFetch(domain:example.com)"}, req: webFetch("https://example.com/x"), want: "ask"},
		{name: "another public host is unaffected by the deny rule", mode: ModeAcceptEdits, deny: []string{"WebFetch(domain:example.com)"}, req: webFetch("https://example.org/"), want: "allow", why: reading},

		{name: "the fetch tool writes", mode: ModeAcceptEdits, req: rq{tool: "web_fetch", network: true, writes: true, input: `{"url":"https://example.com"}`}, want: "ask"},
		{name: "a generic http tool", mode: ModeAcceptEdits, req: rq{tool: "http", network: true, input: `{"url":"https://example.com"}`}, want: "ask"},
		{name: "an MCP tool that reaches the network", mode: ModeAcceptEdits, req: rq{tool: "mcp__browser__open", network: true, input: `{"url":"https://example.com"}`}, want: "ask"},
		{name: "a tool with only the network flag", mode: ModeAcceptEdits, req: rq{tool: "Download", network: true}, want: "ask"},
		{name: "curl in the shell", mode: ModeAcceptEdits, req: bash("curl -s https://example.com"), want: "ask"},
		{name: "a fetch with no url", mode: ModeAcceptEdits, req: rq{tool: "web_fetch", network: true, input: `{"href":"https://example.com"}`}, want: "ask"},
	}
	for _, u := range []string{
		"http://127.0.0.1", "http://127.0.0.1:3000/x", "http://2130706433/", "http://0x7f.1/",
		"http://localhost:8080", "http://LOCALHOST/", "http://app.localhost/", "http://localhost./",
		"http://10.0.0.5", "http://192.168.1.1/admin", "http://172.16.0.1/", "http://100.64.0.1/",
		"http://169.254.169.254/latest/meta-data/", "http://[fd00:ec2::254]/", "http://metadata.google.internal/computeMetadata/v1/",
		"http://[::1]/", "http://[::ffff:127.0.0.1]/", "http://[fe80::1]/", "http://0.0.0.0/", "http://[::]/", "http://224.0.0.1/",
		"http://intranet", "http://intranet:8080/wiki", "http://printer.local", "http://nas.lan/", "http://db.corp.internal/",
		"http://router.home.arpa/",
	} {
		cases = append(cases, tc{name: "still asks for " + u, mode: ModeAcceptEdits, req: webFetch(u), want: "ask", why: "accept-edits mode reads public websites only"})
	}
	runCases(t, f, cases)
}
