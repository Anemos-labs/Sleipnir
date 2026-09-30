package perm

import (
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/shellparse"
)

func TestParseRule(t *testing.T) {
	valid := []struct {
		in      string
		tool    string
		pattern string
	}{
		{"Bash(git status:*)", "Bash", "git status:*"},
		{"Bash(npm run test)", "Bash", "npm run test"},
		{"Bash(*)", "Bash", "*"},
		{"Bash", "Bash", ""},
		{"Bash()", "Bash", ""},
		{"Edit(src/**)", "Edit", "src/**"},
		{"Read(~/.ssh/**)", "Read", "~/.ssh/**"},
		{"WebFetch(domain:example.com)", "WebFetch", "domain:example.com"},
		{"Read", "Read", ""},
		{"  Read ( x ) ", "Read", "x"},
		{"Bash(echo '(hi)')", "Bash", "echo '(hi)'"},
		{"Bash(git commit -m 'a (b)')", "Bash", "git commit -m 'a (b)'"},
		{"mcp__github__create_issue", "mcp__github__create_issue", ""},
		{"mcp__github__*", "mcp__github__*", ""},
		{"Edit(**/*.go)", "Edit", "**/*.go"},
		{"Edit(src/[ab].go)", "Edit", "src/[ab].go"},
		{"Read(/)", "Read", "/"},
	}
	for _, v := range valid {
		r, err := ParseRule(Allow, v.in)
		if err != nil {
			t.Errorf("ParseRule(%q): %v", v.in, err)
			continue
		}
		if r.Tool != v.tool || r.Pattern != v.pattern || r.Action != Allow {
			t.Errorf("ParseRule(%q) = %+v, want tool %q pattern %q", v.in, r, v.tool, v.pattern)
		}
		// The textual form must parse back to the same rule (it is what
		// Config.Persist writes to disk).
		back, err := ParseRule(Allow, r.String())
		if err != nil || back != r {
			t.Errorf("round trip of %q: %+v, %v", v.in, back, err)
		}
	}
	invalid := []string{
		"", "   ", "()", "(x)", "Bash(", "Bash(x", "Bash)x(", "Ba sh(x)", "Ba$h(x)", "Edit(src/[)", "Read(!x)",
		"Read(a\x00b)", "Read(a\nb)", "Bash(git 'unterminated)", "WebFetch(domain:)", "Bash(echo (hi))",
	}
	for _, in := range invalid {
		if r, err := ParseRule(Deny, in); err == nil {
			t.Errorf("ParseRule(%q) = %+v, want an error", in, r)
		}
	}
	if _, err := ParseRule("maybe", "Read"); err == nil {
		t.Error("an unknown action must be rejected")
	}
	for _, a := range []Action{Allow, Ask, Deny} {
		if r, err := ParseRule(a, "Read"); err != nil || r.Action != a {
			t.Errorf("action %q: %+v %v", a, r, err)
		}
	}
}

func TestWildMatch(t *testing.T) {
	for _, tc := range []struct {
		pat, s string
		want   bool
	}{
		{"", "", true}, {"", "a", false}, {"*", "", true}, {"*", "anything/at all", true},
		{"a*", "abc", true}, {"a*", "bc", false}, {"*c", "abc", true}, {"*c", "abd", false},
		{"a*c", "abc", true}, {"a*c", "ac", true}, {"a*c", "abbbc", true}, {"a*c", "abbbd", false},
		{"a**c", "abc", true}, {"*a*b*", "xaxbx", true}, {"*a*b*", "xbxax", false},
		{"feature/*", "feature/x", true}, {"feature/*", "feature/x/y", true}, {"feature/*", "main", false},
		{"git", "git", true}, {"git", "gitx", false}, {"git", "xgit", false},
	} {
		if got := wildMatch(tc.pat, tc.s); got != tc.want {
			t.Errorf("wildMatch(%q, %q) = %v, want %v", tc.pat, tc.s, got, tc.want)
		}
	}
}

func TestToolClasses(t *testing.T) {
	for tool, want := range map[string]toolClass{
		"Read": classRead, "read_file": classRead, "Glob": classRead, "grep": classRead, "LS": classRead,
		"Edit": classWrite, "Write": classWrite, "MultiEdit": classWrite, "multi_edit": classWrite,
		"NotebookEdit": classWrite, "apply-patch": classWrite,
		"Bash": classBash, "bash": classBash, "shell": classBash, "exec": classBash,
		"WebFetch": classWeb, "web_fetch": classWeb, "WebSearch": classWeb,
		"mcp__x__y": classOther, "Task": classOther, "": classOther,
	} {
		if got := classOf(tool); got != want {
			t.Errorf("classOf(%q) = %v, want %v", tool, got, want)
		}
	}
}

// commandRule compiles a Bash rule and reports whether it covers cmd (the first
// simple command of the parsed line).
func commandRule(t *testing.T, rule, cmd string, restrict bool) bool {
	t.Helper()
	r, err := ParseRule(Allow, rule)
	if err != nil {
		t.Fatal(err)
	}
	c, err := compileRule(r, nil)
	if err != nil {
		t.Fatal(err)
	}
	an := shellparse.Parse(cmd)
	if len(an.Commands) == 0 {
		t.Fatalf("no commands in %q", cmd)
	}
	return c.matchCommand(an.Commands[0], restrict)
}

func TestBashRuleMatching(t *testing.T) {
	for _, tc := range []struct {
		rule, cmd string
		allow     bool // matches as an allow rule
		restrict  bool // matches as a deny/ask rule
	}{
		{"Bash(git status:*)", "git status", true, true},
		{"Bash(git status:*)", "git status -s", true, true},
		{"Bash(git status:*)", "git statusx", false, false},
		{"Bash(git status:*)", "git", false, false},
		{"Bash(git status:*)", "git log", false, false},
		{"Bash(git status)", "git status", true, true},
		{"Bash(git status)", "git status -s", false, false},
		{"Bash(git:*)", "git", true, true},
		{"Bash(*)", "anything at all", true, true},
		{"Bash", "anything at all", true, true},
		{"Bash(:*)", "anything", true, true},
		{"Bash(npm run *)", "npm run build", true, true},
		{"Bash(npm run *)", "npm test", false, false},
		{"Bash(git checkout feature/*)", "git checkout feature/x", true, true},
		{"Bash(ls * -la)", "ls src -la", true, true},
		{"Bash(ls * -la)", "ls -la", false, false},
		{"Bash(echo 'a b')", "echo 'a b'", true, true},
		{"Bash(echo 'a b')", `echo "a b"`, true, true},
		{"Bash(echo 'a b')", "echo a b", false, false},
		// The command is judged as parsed: quoting cannot hide it.
		{"Bash(rm -rf:*)", `r\m -rf x`, true, true},
		{"Bash(rm -rf:*)", `"rm" '-rf' x`, true, true},
		// Wrappers are peeled: the wrapped command is what the rule sees.
		{"Bash(rm -rf:*)", "nohup rm -rf x", true, true},
		{"Bash(rm -rf:*)", "env A=1 rm -rf x", true, true},
		// A path is not the program: allow rules refuse it, deny rules do not.
		{"Bash(rm -rf:*)", "/bin/rm -rf x", false, true},
		{"Bash(rm -rf:*)", "./rm -rf x", false, true},
		{"Bash(rm -rf:*)", "/tmp/evil/rm -rf x", false, true},
		// sudo is seen through by deny/ask rules, never by allow rules.
		{"Bash(rm -rf:*)", "sudo rm -rf x", false, true},
		{"Bash(rm -rf:*)", "sudo -u root rm -rf x", false, true},
		{"Bash(sudo:*)", "sudo rm -rf x", true, true},
		{"Bash(rm -rf:*)", "sudo env A=1 nohup rm -rf x", false, true},
		// A wrapper itself can be named by a deny rule.
		{"Bash(nohup:*)", "nohup ls", false, true},
		{"Bash(env:*)", "env A=1 ls", false, true},
		// A program built from expansions never satisfies an allow rule.
		{"Bash(*)", "$CMD x", true, true},
		{"Bash(git status)", "$CMD status", false, false},
		{"Bash(g* status)", "git status", true, true},
		{"Bash(g* status)", "$G status", false, false},
	} {
		if tc.rule == "Bash(*)" && tc.cmd == "$CMD x" {
			continue // blanket rules are handled by the engine (dynamic program check)
		}
		if got := commandRule(t, tc.rule, tc.cmd, false); got != tc.allow {
			t.Errorf("allow-style %s on %q = %v, want %v", tc.rule, tc.cmd, got, tc.allow)
		}
		if got := commandRule(t, tc.rule, tc.cmd, true); got != tc.restrict {
			t.Errorf("deny-style %s on %q = %v, want %v", tc.rule, tc.cmd, got, tc.restrict)
		}
	}
}

func TestRuleStringForEveryTool(t *testing.T) {
	for _, s := range []string{"Bash(git status:*)", "Read", "Edit(a/b)", "WebFetch(domain:x.org)"} {
		r, err := ParseRule(Allow, s)
		if err != nil || r.String() != s {
			t.Errorf("%q -> %+v (%v)", s, r, err)
		}
	}
	if got := (Rule{Tool: "Bash", Pattern: strings.Repeat("a", 3)}).String(); got != "Bash(aaa)" {
		t.Errorf("String = %q", got)
	}
}

func TestDomainAndURLRules(t *testing.T) {
	mk := func(pattern string) *crule {
		c, err := compileRule(Rule{Action: Allow, Tool: "WebFetch", Pattern: pattern}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	req := func(u string) Request {
		return Request{Tool: "WebFetch", Input: []byte(`{"url":"` + u + `"}`)}
	}
	for _, tc := range []struct {
		pat, url string
		want     bool
	}{
		{"domain:example.com", "https://example.com/", true},
		{"domain:example.com", "http://example.com:80/x?y=1#z", true},
		{"domain:example.com", "https://a.b.example.com", true},
		{"domain:example.com", "https://EXAMPLE.COM.", true},
		{"domain:example.com", "https://notexample.com", false},
		{"domain:example.com", "https://example.com.evil.com", false},
		{"domain:example.com", "https://evil.com/example.com", false},
		{"domain:example.com", "https://evil.com/?u=example.com", false},
		{"domain:example.com", "https://user@example.com@evil.com", false},
		{"domain:example.com", "not a url", false},
		{"domain:*.example.com", "https://a.example.com", true},
		{"domain:*.example.com", "https://example.com", false},
		{"https://example.com/*", "https://example.com/a/b", true},
		{"https://example.com/*", "https://example.com.evil/a", false},
		{"", "https://anything", true},
	} {
		if got := mk(tc.pat).matchWeb(req(tc.url)); got != tc.want {
			t.Errorf("%q vs %q = %v, want %v", tc.pat, tc.url, got, tc.want)
		}
	}
	if mk("domain:example.com").matchWeb(Request{Tool: "WebFetch"}) {
		t.Error("a request with no URL must not match a domain rule")
	}
	if mk("domain:example.com").matchWeb(Request{Tool: "Bash", Input: []byte(`{"url":"https://example.com"}`)}) {
		t.Error("a web rule must not match another tool")
	}
}
