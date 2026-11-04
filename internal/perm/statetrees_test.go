package perm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The agents' trees of a team in worktree isolation are kept in the state directory (<state>/worktrees/<session>/<agent>), beside
// what the person trusted and every session's record. The protection of the state directory is about what steers a later session;
// an agent's own tree is its workspace, held to the project's rules (the tree's .sleipnir/ still asks), not to that protection. The
// rest of the state directory, and the trees of other sessions, are as protected as before.
func TestTheStateDirectoryProtectionStopsAtTheAgentsTrees(t *testing.T) {
	f := newFixture(t)
	state := filepath.Join(f.home, ".sleipnir")
	trees := filepath.Join(state, "worktrees", "s1")
	wt := filepath.Join(trees, "be-1")
	other := filepath.Join(state, "worktrees", "s2", "be-1")
	for _, d := range []string{filepath.Join(wt, "src"), filepath.Join(wt, ".sleipnir"), other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(state, "config.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(wt, "src", "cfg.json")
	if err := os.Symlink(filepath.Join(state, "config.json"), link); err != nil {
		t.Skip("no symbolic links here:", err)
	}
	for _, mode := range []Mode{ModeAcceptEdits, ModeBypass} {
		e := f.engine(t, Config{Mode: mode, StateDir: state, TreeParents: []string{trees}, Ask: guardedAsk})
		e.Confine("be-1", wt)
		for _, c := range []struct {
			name, cmd, want, why string
			paths                []string
		}{
			{name: "an edit in the agent's tree", paths: []string{filepath.Join(wt, "src", "main.go")}, want: "allow"},
			{name: "a build in the agent's tree", cmd: "cd " + wt + " && go build -o bin/x ./cmd/x", want: "allow"},
			{name: "the project's own protection inside the tree", paths: []string{filepath.Join(wt, ".sleipnir", "config.json")}, want: "ask", why: "Edit(./.sleipnir/**)"},
			{name: "the person's settings", paths: []string{filepath.Join(state, "config.json")}, want: "ask", why: "Edit("},
			{name: "a session's record", paths: []string{filepath.Join(state, "sessions", "s1", "events.jsonl")}, want: "ask", why: "Edit("},
			{name: "a link in the tree to the settings", paths: []string{link}, want: "ask", why: "Edit("},
			{name: "the folder of every session's trees", cmd: "rm -rf " + filepath.Join(state, "worktrees"), want: "not allow"},
		} {
			var d Decision
			if c.cmd != "" {
				d = e.Check(context.Background(), Request{Agent: "be-1", Role: "backend", Tool: "Bash", Command: c.cmd, Cwd: wt, Summary: "bash"})
			} else {
				d = e.Check(context.Background(), Request{Agent: "be-1", Role: "backend", Tool: "Write", Paths: c.paths, Writes: true, Cwd: wt, Summary: "Write"})
			}
			if got := outcome(d); got != c.want && !(c.want == "not allow" && got != "allow") {
				t.Errorf("%s, %s: %s (%q), want %s", mode, c.name, got, d.Reason, c.want)
			}
			if c.why != "" && !strings.Contains(d.Reason, c.why) {
				t.Errorf("%s, %s: reason %q does not say %q", mode, c.name, d.Reason, c.why)
			}
		}
	}
	// Another session's trees are no tree of this one: the protection holds there.
	e := f.engine(t, Config{Mode: ModeBypass, StateDir: state, TreeParents: []string{trees}, Ask: guardedAsk})
	if d := e.Check(context.Background(), Request{Agent: "lead", Tool: "Write", Paths: []string{filepath.Join(other, "x.go")}, Writes: true, Cwd: f.root, Summary: "Write"}); outcome(d) != "ask" {
		t.Errorf("a write into another session's tree: %s (%q), want ask", outcome(d), d.Reason)
	}
}
