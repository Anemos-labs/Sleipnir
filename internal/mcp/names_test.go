package mcp

import (
	"fmt"
	"math/rand"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

var validToolName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func TestExposedNameTable(t *testing.T) {
	tests := []struct {
		server, tool string
		want         string // "" = only check the shape
		prefix       string
	}{
		{"github", "create_issue", "mcp__github__create_issue", ""},
		{"my-server", "do.it", "mcp__my-server__do_it", ""},
		{"a", "b__c", "mcp__a__b__c", ""},
		{"srv", "_private", "mcp__srv___private", ""},
		{"srv", "with space", "mcp__srv__with_space", ""},
		{"srv", u(0xE9, 0x74, 0xE9), "mcp__srv___t_", ""},
		{"my server", "x", "", "mcp__my_server-"},
		{"a__b", "x", "", "mcp__a_b-"},
		{"trail_", "x", "", "mcp__trail-"},
		{"_lead", "x", "", "mcp__lead-"},
		{"", "x", "", "mcp__srv-"},
		{"...", "x", "", "mcp__srv-"},
		{u(0x65E5, 0x672C), "x", "", "mcp__srv-"},
		{strings.Repeat("s", 60), "x", "", "mcp__" + strings.Repeat("s", 15) + "-"},
		{"srv", strings.Repeat("t", 100), "", "mcp__srv__" + strings.Repeat("t", 10)},
	}
	for _, tt := range tests {
		got := exposedName(tt.server, tt.tool)
		if !validToolName.MatchString(got) {
			t.Errorf("(%q,%q) -> %q is not a valid tool name", tt.server, tt.tool, got)
		}
		if tt.want != "" && got != tt.want {
			t.Errorf("(%q,%q) = %q, want %q", tt.server, tt.tool, got, tt.want)
		}
		if tt.prefix != "" && !strings.HasPrefix(got, tt.prefix) {
			t.Errorf("(%q,%q) = %q, want prefix %q", tt.server, tt.tool, got, tt.prefix)
		}
		if exposedName(tt.server, tt.tool) != got {
			t.Errorf("(%q,%q) is not deterministic", tt.server, tt.tool)
		}
	}
}

func TestLongNamesStayDistinct(t *testing.T) {
	a := exposedName("srv", strings.Repeat("t", 100)+"1")
	b := exposedName("srv", strings.Repeat("t", 100)+"2")
	if a == b {
		t.Fatalf("two long tool names collapsed into %q", a)
	}
	if len(a) > maxToolNameLen || len(b) > maxToolNameLen {
		t.Errorf("too long: %d %d", len(a), len(b))
	}
}

func TestParseName(t *testing.T) {
	tests := []struct {
		name         string
		server, tool string
		ok           bool
	}{
		{"mcp__github__create_issue", "github", "create_issue", true},
		{"mcp__a__b__c", "a", "b__c", true},
		{"mcp__srv___private", "srv", "_private", true},
		{"mcp__my_server-1a2b3c4d__x", "my_server-1a2b3c4d", "x", true},
		{"read", "", "", false},
		{"mcp__", "", "", false},
		{"mcp__srv", "", "", false},
		{"mcp__srv__", "", "", false},
		{"mcp____tool", "", "", false},
		{"other__srv__tool", "", "", false},
	}
	for _, tt := range tests {
		s, tool, ok := ParseName(tt.name)
		if s != tt.server || tool != tt.tool || ok != tt.ok {
			t.Errorf("ParseName(%q) = %q,%q,%v want %q,%q,%v", tt.name, s, tool, ok, tt.server, tt.tool, tt.ok)
		}
	}
}

// Permission rules are written against these names ("mcp__srv__*"), so the
// mapping must never let one server's tools fall under another server's prefix.
func TestServerSegmentsAreUnambiguous(t *testing.T) {
	servers := []string{
		"a", "a_", "_a", "a__b", "a_b", "a-b", "a b", "a.b", "a  b", "a___b", "", " ", "__", "-",
		"github", "GitHub", "github ", "github-", "git__hub", "a_b-1a2b3c4d", "a_b-1a2b3c4d-1a2b3c4d",
		strings.Repeat("x", 24), strings.Repeat("x", 25), strings.Repeat("x", 100), u(0x65E5, 0x672C), u(0x65E5, 0x672D),
		"mcp", "mcp__", "srv__tool",
	}
	segs := map[string]string{}
	for _, s := range servers {
		seg := serverSegment(s)
		if strings.Contains(seg, "__") || strings.HasSuffix(seg, "_") || seg == "" || len(seg) > maxServerSeg {
			t.Errorf("segment %q of %q is ambiguous or over-long", seg, s)
		}
		if !regexp.MustCompile(`^[A-Za-z0-9_-]+$`).MatchString(seg) {
			t.Errorf("segment %q of %q has bad characters", seg, s)
		}
		if prev, dup := segs[seg]; dup {
			t.Errorf("servers %q and %q share the segment %q", prev, s, seg)
		}
		segs[seg] = s
	}
	// No tool of one server falls under the wildcard prefix of another.
	for _, s1 := range servers {
		name := exposedName(s1, "tool__x")
		for _, s2 := range servers {
			if serverSegment(s1) == serverSegment(s2) {
				continue
			}
			if strings.HasPrefix(name, toolPrefix+serverSegment(s2)+"__") {
				t.Errorf("tool of %q (%s) matches the wildcard of %q (mcp__%s__*)", s1, name, s2, serverSegment(s2))
			}
		}
		// And the server part of a parsed name is the server's own segment.
		if sv, tool, ok := ParseName(name); !ok || sv != serverSegment(s1) || tool != "tool__x" {
			t.Errorf("ParseName(%q) = %q,%q,%v", name, sv, tool, ok)
		}
	}
}

func TestExposedNamesRandom(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	alphabet := []rune("abcXYZ019_-. /\\\t" + u(0xE9, 0x65E5, 0x1F600, 0x200B))
	gen := func() string {
		n := rng.Intn(80)
		rs := make([]rune, n)
		for i := range rs {
			rs[i] = alphabet[rng.Intn(len(alphabet))]
		}
		return string(rs)
	}
	seen := map[string][2]string{}
	for i := 0; i < 5000; i++ {
		srv, tool := gen(), gen()
		if tool == "" {
			tool = "t"
		}
		name := exposedName(srv, tool)
		if !validToolName.MatchString(name) {
			t.Fatalf("(%q,%q) -> invalid %q", srv, tool, name)
		}
		if !utf8.ValidString(name) {
			t.Fatalf("invalid utf8")
		}
		sv, _, ok := ParseName(name)
		if !ok || sv != serverSegment(srv) {
			t.Fatalf("(%q,%q) -> %q parses to %q,%v", srv, tool, name, sv, ok)
		}
		if prev, dup := seen[name]; dup && (prev[0] != srv || prev[1] != tool) {
			// Different pairs may share a plain name (a.b vs a_b); the snapshot
			// resolves that with hashedName, which must then differ.
			if hashedName(srv, tool) == hashedName(prev[0], prev[1]) {
				t.Fatalf("pairs %q and %q collide even hashed: %s", prev, [2]string{srv, tool}, name)
			}
		}
		seen[name] = [2]string{srv, tool}
	}
}

func TestHashedNameFitsAndDiffers(t *testing.T) {
	for _, tool := range []string{"x", strings.Repeat("t", 200), "a.b"} {
		h := hashedName("srv", tool)
		if len(h) > maxToolNameLen || !validToolName.MatchString(h) {
			t.Errorf("hashedName(%q) = %q", tool, h)
		}
		if h == exposedName("srv", tool) && len(tool) < 20 {
			t.Errorf("hashed form equals plain form for %q", tool)
		}
	}
	if fmt.Sprint(hashedName("a", "x")) == hashedName("b", "x") {
		t.Error("hash must depend on the server")
	}
}
