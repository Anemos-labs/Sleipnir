package swarm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/workspace"
)

// "**" is aligned the way matching aligns it: two scopes overlap only when some path could lie in both. Before, any "**" on either
// side was an overlap, so a docs task scoped "**/*.md" collided with every writer in the tree (web/package.json, Makefile, a page
// in web/src) and waited hours for the root README.
func TestScopesOverlapAlignsGlobstar(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"**/*.md", "packaging/darwin/**", true}, // packaging/darwin/README.md
		{"**/*.md", "docs/**", true},
		{"**/*.md", "README.md", true},
		{"**/*.md", "**/*.md", true},
		{"**/*.md", "web/*.ts", false},     // a .md file is never a .ts file
		{"**/*.md", "**/*_test.go", false}, // nor a Go test
		{"**/*.md", "web/*", true},         // web/README.md
		{"*.md", "web/**", false},          // "*" stays in the root
		{"*.md", "docs/*.md", false},
		{"**/x.go", "src/y.go/**", true}, // src/y.go/x.go, if src/y.go were a directory
		{"src/**/test/*.go", "src/a/b/test/x.go", true},
		{"src/*/x.go", "src/a/b/x.go", false},
		{"src/*/x.go", "src/a/x.go", true},
		{"a/**/b", "a/c", true}, // a/c/b: a literal path covers what is below it
		{"a/**/b/*.go", "a/c/*.md", false},
		{"**", "anything/at/all", true},
		{"web/**", "api/**", false},
		{"/repo/docs/*.md", "**/*.go", false},
		{"/repo/docs/*.md", "docs/**", true},
	} {
		if got := scopesOverlap(tc.a, tc.b); got != tc.want {
			t.Errorf("scopesOverlap(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
		if got := scopesOverlap(tc.b, tc.a); got != tc.want {
			t.Errorf("scopesOverlap(%q, %q) = %v, want %v (swapped)", tc.b, tc.a, got, tc.want)
		}
	}
	// Many "**" on both sides stay fast: each pair of positions is explored once.
	start := time.Now()
	if scopesOverlap(strings.Repeat("**/", 30)+"*.go", strings.Repeat("**/", 30)+"*.md") {
		t.Error("patterns whose last segments never match one name were reported overlapping")
	}
	if !scopesOverlap(strings.Repeat("**/", 30)+"a", strings.Repeat("**/", 30)+"b.go") {
		t.Error("a/b.go lies in both")
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("deep globstars took %v", d)
	}
}

// A path that names a file has nothing below it, so it covers that file alone: "**/*.md" and the file web/package.json are
// disjoint. A directory, or a path that does not exist yet, keeps the reading that covers what is below it.
func TestScopesOverlapKnowsFiles(t *testing.T) {
	files := map[string]bool{"web/package.json": true, "Makefile": true, "web/src/pages/Overview.tsx": true, "README.md": true}
	file := func(p string) bool { return files[p] }
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"**/*.md", "web/package.json", false},
		{"**/*.md", "Makefile", false},
		{"**/*.md", "web/src/pages/Overview.tsx", false},
		{"**/*.md", "README.md", true},
		{"**/*.md", "web/new.tsx", true}, // not there yet: it could become a directory of notes
		{"**/*.md", "web/src", true},     // a directory
		{"web/package.json", "web/package.json", true},
		{"web/package.json", "web/**", true},
		{"web/{package.json,Makefile}", "**/*.md", true}, // web/Makefile is not a file here
		{"Web/Package.json", "**/*.md", true},            // another spelling is not known to be the file
	} {
		if got := scopesOverlapIn(tc.a, tc.b, file); got != tc.want {
			t.Errorf("scopesOverlapIn(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
		if got := scopesOverlapIn(tc.b, tc.a, file); got != tc.want {
			t.Errorf("scopesOverlapIn(%q, %q) = %v, want %v (swapped)", tc.b, tc.a, got, tc.want)
		}
	}
	// Without knowing, a path covers what is below it, as before.
	if !scopesOverlap("**/*.md", "web/package.json") {
		t.Error("without file knowledge web/package.json/x.md is a common path")
	}
}

// A child's scope lies inside its parent's when every path it names does. "README.md" lies inside "**/*.md" (the parent of the
// root docs in a long run refused it 25 times); "docs/**" does not.
func TestScopeContainsChildPaths(t *testing.T) {
	for _, tc := range []struct {
		parents []string
		child   string
		want    bool
	}{
		{[]string{"**/*.md", "docs/**"}, "README.md", true},
		{[]string{"**/*.md"}, "AGENTS.md", true},
		{[]string{"**/*.md"}, "docs/guide/x.md", true},
		{[]string{"**/*.md"}, "*.md", true},
		{[]string{"**/*.md"}, "docs/*.md", true},
		{[]string{"**/*.md"}, "web/*/README.md", true},
		{[]string{"**/*.md"}, "**/*.md", true},
		{[]string{"**/*.md"}, "docs/**", false},
		{[]string{"**/*.md"}, "**", false},
		{[]string{"**/*.md"}, "web/package.json", false},
		{[]string{"**/*.md"}, "docs", false},
		{[]string{"**/*.md"}, "**/*", false},
		{[]string{"src/**"}, "src/a.go", true},
		{[]string{"src/**"}, "src/a/**", true},
		{[]string{"src/**"}, "src", true},
		{[]string{"src/**"}, "src/**/*.go", true},
		{[]string{"src/**"}, "srcx/a.go", false},
		{[]string{"src/**"}, "**", false},
		{[]string{"src"}, "src/a.go", true}, // a directory holds what is below it
		{[]string{"src"}, "src/**", true},
		{[]string{"src/*.go"}, "src/a.go", true},
		{[]string{"src/*.go"}, "src/*_test.go", true},
		{[]string{"src/*.go"}, "src/sub/a.go", false},
		{[]string{"src/*.go"}, "src/*", false},
		{[]string{"src/*.go"}, "src/**", false},
		{[]string{"src/*.go"}, "src/[", false}, // not a valid pattern
		{[]string{"**"}, "any/thing/**", true},
		{[]string{"src/{a,b}/**"}, "src/a/x.go", true},
		{[]string{"src/**"}, "src/{a,b}/x.go", true},
		{[]string{"src/a/**"}, "src/{a,b}/x.go", false},
		{[]string{"src/a/**", "src/b/**"}, "src/{a,b}/x.go", true},
		{[]string{"internal/control/**"}, "internal/control/*test.go", true},
		{[]string{"web/**"}, "web/src/**", true},
		{[]string{"api/**"}, "other/file.go", false},
		{[]string{"**"}, "/etc/passwd", false},
	} {
		if got := scopeContains(tc.parents, tc.child); got != tc.want {
			t.Errorf("scopeContains(%q, %q) = %v, want %v", tc.parents, tc.child, got, tc.want)
		}
	}
}

// A writer on a file and the docs task on "**/*.md" run side by side once the file is known to be one; a writer on a directory
// still collides with it, and so does a path that is not there yet.
func TestSpawnLetsDocsRunBesideAFileWriter(t *testing.T) {
	r := newRVRig(t, Config{MaxWriters: 4}, func(ctx context.Context, c *rvCall) rvReply {
		time.Sleep(200 * time.Millisecond)
		return rvReply{Text: "ok"}
	})
	r.sw.StartManager()
	for _, f := range []string{"web/package.json", "Makefile"} {
		p := filepath.Join(r.sw.deps.Root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.sw.Spawn(SpawnReq{Role: "frontend", Title: "deps", Files: []string{"web/package.json", "Makefile"}, By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.sw.Spawn(SpawnReq{Role: "docs", Title: "root docs", Files: []string{"**/*.md"}, By: "mgr"}); err != nil {
		t.Fatalf("the docs task collided with writers of files: %v", err)
	}
	for _, f := range []string{"web/new.tsx", "web/**"} {
		// a task the spawn would create has no id yet: the refusal names it in words
		if _, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "clash " + f, Files: []string{f}, By: "mgr"}); err == nil || !strings.Contains(err.Error(), "overlaps") ||
			!strings.Contains(err.Error(), "make this task depend on") {
			t.Errorf("scope %q beside **/*.md: %v", f, err)
		}
	}
}

// A pool leader scopes children to the paths its parent's globs hold: README.md and AGENTS.md under "**/*.md". The manager may
// narrow such a child to them too.
func TestAPoolChildMayNameFilesInsideItsParentsGlob(t *testing.T) {
	r := newRVRig(t, Config{PersistentPools: true, MaxWorkers: 2, Team: []string{"backend", "backend"}}, idleModel)
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "docs", Files: []string{"**/*.md", "docs/**"}, By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	parent := r.sw.Board.Snapshot().Tasks[0]
	lead := parent.Pool.Members[0]
	if err := r.sw.Board.poolUpdate("mgr", parent.ID, func(_ *Snapshot, _ *Task, p *PoolState) error { p.Phase = "executing"; return nil }); err != nil {
		t.Fatal(err)
	}
	res := r.callTool(context.Background(), "task", lead, "backend", map[string]any{"action": "create", "parent": parent.ID, "role": "backend",
		"title": "root docs", "files": []string{"README.md", "AGENTS.md"}})
	if res.IsError {
		t.Fatalf("a child scoped to root Markdown files under **/*.md: %s", res.Text)
	}
	wide := r.callTool(context.Background(), "task", lead, "backend", map[string]any{"action": "create", "parent": parent.ID, "role": "backend",
		"title": "everything", "files": []string{"web/**"}})
	if !wide.IsError || !strings.Contains(wide.Text, "outside the scope of its parent "+parent.ID+" (**/*.md, docs/**)") {
		t.Errorf("a child wider than its parent: %q", wide.Text)
	}
	child := r.sw.Board.Snapshot().Tasks[1]
	if err := r.sw.Board.revise("mgr", child.ID, TaskSpec{Files: []string{"README.md"}}, "only the README"); err != nil {
		t.Errorf("narrowing the child to README.md: %v", err)
	}
}

// A child's literal path lies inside its parent's glob as the path it names; if it names a directory, the child still writes only
// what the parent's scope holds, at write time in a shared tree and in the merge queue of an isolated one.
func TestAPoolChildWritesOnlyInsideItsParentsScope(t *testing.T) {
	r := newRVRig(t, Config{PersistentPools: true, MaxWorkers: 2, Team: []string{"backend", "backend"}}, idleModel)
	if _, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "docs", Files: []string{"docs/*"}, By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	parent := r.sw.Board.Snapshot().Tasks[0]
	lead, other := parent.Pool.Members[0], parent.Pool.Members[1]
	if err := r.sw.Board.poolUpdate("mgr", parent.ID, func(_ *Snapshot, _ *Task, p *PoolState) error { p.Phase = "executing"; return nil }); err != nil {
		t.Fatal(err)
	}
	child, err := r.sw.Board.CreateTask(lead, TaskSpec{Parent: parent.ID, Title: "pkg", Role: "backend", Files: []string{"docs/pkg"}})
	if err != nil {
		t.Fatalf("a child naming a path the parent's glob holds: %v", err)
	}
	if _, err := r.sw.Board.assignTask(assignReq{by: lead, agent: other, id: child.ID}); err != nil {
		t.Fatal(err)
	}
	root := r.sw.deps.Root
	if err := r.sw.Leases.BeforeWrite(other, root+"/docs/pkg"); err != nil {
		t.Errorf("the path the child names: %v", err)
	}
	err = r.sw.Leases.BeforeWrite(other, root+"/docs/pkg/deep/file.go")
	if err == nil || !strings.Contains(err.Error(), "outside the scope of its parent "+parent.ID+" (docs/*)") {
		t.Errorf("a write below the child's path, outside the parent's scope: %v", err)
	}
	if err := r.sw.Leases.BeforeWrite(other, root+"/web/x.go"); err == nil || !strings.Contains(err.Error(), "outside the scope of your task") {
		t.Errorf("a write outside both: %v", err)
	}
	refuse := r.sw.submitRefusal("backend", child)
	if refuse == nil || refuse("docs/pkg") != "" || !strings.Contains(refuse("docs/pkg/deep/file.go"), "outside the scope of the parent "+parent.ID) {
		t.Error("the merge queue's refusal does not hold a child to its parent's scope")
	}
	if loose, _ := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "loose", Role: "backend", Files: []string{"x/**"}}); r.sw.submitRefusal("backend", loose) != nil {
		t.Error("a task with no parent is refused files by its parent")
	}
}

// In an isolated run the merge queue holds a pool child to its parent's scope: a child "docs/pkg" of a parent "docs/*" that
// changed docs/pkg/deep/file.go is refused, naming the parent; one that changed docs/pkg itself merges.
func TestTheMergeQueueHoldsAPoolChildToItsParentsScope(t *testing.T) {
	r := newIsoRig(t, isoOpts{}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "idle"} })
	parent, err := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "docs", Role: "backend", Files: []string{"docs/*"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.sw.Board.beginPool("mgr", parent.ID, []string{"be-1", "be-2"}); err != nil {
		t.Fatal(err)
	}
	if err := r.sw.Board.poolUpdate("mgr", parent.ID, func(_ *Snapshot, _ *Task, p *PoolState) error { p.Phase = "executing"; return nil }); err != nil {
		t.Fatal(err)
	}
	child, err := r.sw.Board.CreateTask("be-1", TaskSpec{Parent: parent.ID, Title: "pkg", Role: "backend", Files: []string{"docs/pkg"}})
	if err != nil {
		t.Fatal(err)
	}
	submit := func(name, file string) workspace.Result {
		t.Helper()
		ctx := context.Background()
		w, err := r.mgr.Create(ctx, name, workspace.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(w.Path, filepath.FromSlash(file))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Commit(ctx, "child change"); err != nil {
			t.Fatal(err)
		}
		res, err := r.q.Submit(ctx, workspace.Submission{Agent: name, Tree: w, Task: child.ID, Scope: child.Files, EnforceScope: true, Refuse: r.sw.submitRefusal("backend", child)})
		if err != nil {
			t.Fatal(err)
		}
		return *res
	}
	if res := submit("be-2", "docs/pkg/deep/file.go"); res.Outcome != workspace.OutcomeRejected || !strings.Contains(res.Reason, "outside the scope of the parent "+parent.ID+" (docs/*)") {
		t.Errorf("a change below the child's path, outside the parent's scope: %s %q", res.Outcome, res.Reason)
	}
	if res := submit("be-3", "docs/pkg"); !res.Merged() {
		t.Errorf("a change of the path the child names: %s %q", res.Outcome, res.Reason)
	}
}
