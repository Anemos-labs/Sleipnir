package agentdefs

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/perm"
)

// Role must keep the fields of swarm.Role, in order, so that the swarm can
// convert with swarm.Role(def.ToRole()). This package may not import the swarm,
// so the test reads its source instead; it is skipped when the file is not
// where the repository layout puts it.
func TestRoleMatchesSwarmRole(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join("..", "swarm", "roles.go"), nil, 0)
	if err != nil {
		t.Skipf("cannot read the swarm's roles.go: %v", err)
	}
	var want []string
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != "Role" {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return true
		}
		for _, fld := range st.Fields.List {
			typ, _ := fld.Type.(*ast.Ident)
			for _, name := range fld.Names {
				want = append(want, name.Name+" "+typ.Name)
			}
		}
		return false
	})
	if len(want) == 0 {
		t.Skip("swarm.Role not found in roles.go")
	}
	var got []string
	rt := reflect.TypeOf(Role{})
	for i := 0; i < rt.NumField(); i++ {
		got = append(got, rt.Field(i).Name+" "+rt.Field(i).Type.String())
	}
	if strings.Join(got, ", ") != strings.Join(want, ", ") {
		t.Fatalf("agentdefs.Role no longer matches swarm.Role:\n got %v\nwant %v", got, want)
	}
}

func TestProfileRules(t *testing.T) {
	tests := []struct {
		name string
		def  Def
		mode perm.Mode
		deny string
	}{
		{"no restrictions", Def{}, "", ""},
		{"read only tools", Def{Tools: []string{"Read", "Grep"}, ReadOnly: true}, perm.ModePlan, "Bash,Edit,WebFetch,WebSearch,mcp__*"},
		{"shell without writes", Def{Tools: []string{"Read", "Bash"}}, "", "Edit,WebFetch,WebSearch,mcp__*"},
		{"shell with a pattern counts as the tool", Def{Tools: []string{"Read", "Bash(git diff:*)"}}, "", "Edit,WebFetch,WebSearch,mcp__*"},
		{"writer without shell", Def{Tools: []string{"Read", "Edit", "Write"}}, "", "Bash,WebFetch,WebSearch,mcp__*"},
		{"web and mcp", Def{Tools: []string{"Read", "WebFetch", "mcp__x__y"}}, "", "Bash,Edit"},
		{"sleipnir names", Def{Tools: []string{"read", "web_fetch", "apply_patch", "bash"}}, "", "mcp__*"},
		{"plan mode", Def{PermissionMode: "plan"}, perm.ModePlan, ""},
		{"disallowed", Def{DisallowedTools: []string{"Write", "Bash(rm:*)"}}, "", "Write,Bash(rm:*)"},
	}
	for _, tc := range tests {
		p := tc.def.Profile()
		if p.Mode != tc.mode || strings.Join(p.Deny, ",") != tc.deny || len(p.Allow) != 0 || len(p.Ask) != 0 {
			t.Errorf("%s: profile = %+v, want mode %q deny %q", tc.name, p, tc.mode, tc.deny)
		}
	}
}

// The profile is only worth anything if the permission engine enforces it, and
// the engine refuses to start on a rule it cannot parse.
func TestProfileIsEnforcedByThePermissionEngine(t *testing.T) {
	w := newWorld(t)
	w.proj(".claude", "scout.md", "---\nname: scout\ntools: Read, Grep, Glob\n---\nScout the code.")
	w.proj(".claude", "tester.md", "---\nname: tester\ntools: Read, Bash\n---\nRun the tests.")
	w.proj(".claude", "guarded.md", "---\nname: guarded\ndisallowedTools: Write, Bash(rm:*)\n---\nCareful work.")
	w.proj(".claude", "planner.md", "---\nname: planner\npermissionMode: plan\n---\nPlan only.")
	w.proj(".claude", "free.md", "---\nname: free\n---\nUnrestricted.")
	defs, warns := w.load()
	if len(defs) != 5 || len(warns) != 0 {
		t.Fatalf("%d defs, warnings %s", len(defs), warnText(warns))
	}
	roles := map[string]perm.RoleProfile{}
	for _, d := range defs {
		roles[d.Name] = d.Profile()
	}
	eng, err := perm.NewEngine(perm.Config{Mode: perm.ModeAcceptEdits, Root: w.root, Home: w.home, Roles: roles})
	if err != nil {
		t.Fatalf("the engine rejected a generated profile: %v", err)
	}
	file := filepath.Join(w.root, "notes.txt")
	write := func(role string) perm.Request {
		return perm.Request{Agent: role + "-1", Role: role, Tool: "write", Paths: []string{file}, Writes: true, Summary: "write"}
	}
	edit := func(role string) perm.Request {
		return perm.Request{Agent: role + "-1", Role: role, Tool: "edit", Paths: []string{file}, Writes: true, Summary: "edit"}
	}
	shell := func(role, cmd string) perm.Request {
		return perm.Request{Agent: role + "-1", Role: role, Tool: "bash", Command: cmd, Cwd: w.root, Summary: cmd}
	}
	read := func(role string) perm.Request {
		return perm.Request{Agent: role + "-1", Role: role, Tool: "read", Paths: []string{file}, Summary: "read"}
	}
	fetch := func(role string) perm.Request {
		return perm.Request{Agent: role + "-1", Role: role, Tool: "web_fetch", Network: true, Summary: "fetch"}
	}
	mcp := func(role string) perm.Request {
		return perm.Request{Agent: role + "-1", Role: role, Tool: "mcp__github__create_issue", Summary: "mcp"}
	}
	put(t, file, "x")

	cases := []struct {
		what  string
		req   perm.Request
		allow bool
	}{
		{"free may write", write("free"), true},
		{"free may run a command", shell("free", "ls"), true},

		{"scout may read", read("scout"), true},
		{"scout may not write", write("scout"), false},
		{"scout may not edit", edit("scout"), false},
		{"scout may not run a command", shell("scout", "ls"), false},
		{"scout may not fetch", fetch("scout"), false},
		{"scout may not call an mcp tool", mcp("scout"), false},

		{"tester may read", read("tester"), true},
		{"tester may run a command", shell("tester", "ls"), true},
		{"tester may not write", write("tester"), false},
		{"tester may not edit", edit("tester"), false},
		{"tester may not fetch", fetch("tester"), false},

		{"guarded may edit", edit("guarded"), false}, // Write is disallowed: every write is
		{"guarded may not rm", shell("guarded", "rm notes.txt"), false},
		{"guarded may ls", shell("guarded", "ls"), true},

		{"planner may read", read("planner"), true},
		{"planner may not write", write("planner"), false},
	}
	for _, tc := range cases {
		d := eng.Check(context.Background(), tc.req)
		if d.Allow != tc.allow {
			t.Errorf("%s: allow = %v (%s), want %v", tc.what, d.Allow, d.Reason, tc.allow)
		}
	}
}

func TestReadOnlyClassification(t *testing.T) {
	for _, tc := range []struct {
		tool string
		want toolClass
	}{
		{"Read", classReadOnly}, {"read_file", classReadOnly}, {"Bash", classShell}, {"bash_output", classShell},
		{"apply_patch", classWrite}, {"MultiEdit", classWrite}, {"WebFetch", classWeb}, {"web-search", classWeb},
		{"mcp__github__x", classMCP}, {"MCP_thing", classMCP}, {"Task", classOther}, {"spawn", classOther},
		{"Frobnicate", classOther}, {"Bash(git diff:*)", classShell}, {"Read(src/**)", classReadOnly},
	} {
		if got := classOf(tc.tool); got != tc.want {
			t.Errorf("classOf(%q) = %d, want %d", tc.tool, got, tc.want)
		}
	}
	if allReadOnly(nil) || allReadOnly([]string{}) {
		t.Error("an empty allowlist is not read-only: it means unrestricted")
	}
}

func TestEscapeFramingKeepsOtherTags(t *testing.T) {
	// Pins use tags to structure instructions; only the harness's own framing is forged-able.
	w := newWorld(t)
	w.proj(".claude", "a.md", "---\nname: a\n---\n<example>\n<user>hello</user>\n</example>\n<live>x</live>\n<username>ok</username>")
	defs, _ := w.load()
	want := "<example>\n‹user>hello‹/user>\n</example>\n‹live>x‹/live>\n<username>ok</username>"
	if defs[0].Pin != want {
		t.Errorf("pin = %q\nwant %q", defs[0].Pin, want)
	}
}
