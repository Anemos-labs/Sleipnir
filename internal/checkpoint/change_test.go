package checkpoint

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestPropose(t *testing.T) {
	files := map[string]string{"a.go": "package a\n\nfunc A() {}\n"}
	cur := func(p string) (string, bool) { s, ok := files[p]; return s, ok }
	in := func(v any) []byte { b, _ := json.Marshal(v); return b }

	cases := []struct {
		name, tool string
		input      []byte
		ok         bool
		path       string
		add, del   int
		has        []string
	}{
		{"write over a file", "write", in(map[string]any{"path": "a.go", "content": "package a\n\nfunc B() {}\n"}), true, "a.go", 1, 1,
			[]string{"--- a/a.go", "+++ b/a.go", "-func A() {}", "+func B() {}"}},
		{"write a new file", "write", in(map[string]any{"file_path": "n.go", "content": "x\ny\n"}), true, "n.go", 2, 0,
			[]string{"--- /dev/null", "+++ b/n.go", "+x", "+y"}},
		{"edit in context", "edit", in(map[string]any{"path": "a.go", "old_string": "A()", "new_string": "AA()"}), true, "a.go", 1, 1,
			[]string{" package a", "-func A() {}", "+func AA() {}"}},
		{"edits that do not apply", "edit", in(map[string]any{"path": "a.go", "edits": []map[string]any{{"old_string": "zzz", "new_string": "q"}, {"old_string": "w", "new_string": "e"}}}), true, "a.go", 2, 2,
			[]string{"-zzz", "+q", "-w", "+e"}},
		{"ambiguous edit", "edit", in(map[string]any{"path": "a.go", "old_string": "a", "new_string": "b"}), true, "a.go", 1, 1, []string{"-a", "+b"}},
		{"patch", "apply_patch", in(map[string]any{"patch": "*** Begin Patch\n*** Add File: x.txt\n+one\n+two\n*** Update File: a.go\n@@\n-func A() {}\n+func A() { return }\n*** Delete File: old.go\n*** End Patch\n"}), true, "x.txt", 3, 1,
			[]string{"--- /dev/null", "+++ b/x.txt", "+one", "--- a/a.go", "+func A() { return }", "--- a/old.go", "+++ /dev/null"}},
		{"another tool", "bash", in(map[string]any{"command": "ls"}), false, "", 0, 0, nil},
		{"bad json", "write", []byte("{"), false, "", 0, 0, nil},
		{"write without content", "write", in(map[string]any{"path": "a.go"}), false, "", 0, 0, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := Propose(c.tool, c.input, cur)
			if ok != c.ok {
				t.Fatalf("ok = %v", ok)
			}
			if !ok {
				return
			}
			if got.Path != c.path || got.Added != c.add || got.Removed != c.del {
				t.Fatalf("got %+v", got)
			}
			for _, h := range c.has {
				if !strings.Contains(got.Unified, h+"\n") {
					t.Errorf("missing %q in:\n%s", h, got.Unified)
				}
			}
		})
	}
	// A huge write is cut.
	big := strings.Repeat("line\n", 200_000)
	got, _ := Propose("write", in(map[string]any{"path": "big.txt", "content": big}), cur)
	if len(got.Unified) > maxDiffOutput+4096 || !strings.HasSuffix(got.Unified, "[diff truncated]\n") || got.Added != 200_000 {
		t.Fatalf("big write: %d bytes, +%d", len(got.Unified), got.Added)
	}
}

// A post-write fingerprint survives a restart: a restarted process still refuses to
// overwrite an edit made right after the agents' last write (within the mtime grace).
func TestFingerprintSurvivesReopen(t *testing.T) {
	e := newEnv(t)
	e.write("f.txt", "orig")
	cp := e.s.Begin("p")
	e.editAfter("a", "f.txt", "agent")
	s2 := e.reopen()
	if err := os.WriteFile(e.abs("f.txt"), []byte("human edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep := mustRestore(t, s2, cp, RestoreOpts{})
	if r := result(t, rep, "f.txt"); r.Outcome != OutcomeConflict {
		t.Fatalf("f.txt = %+v", r)
	}
}
