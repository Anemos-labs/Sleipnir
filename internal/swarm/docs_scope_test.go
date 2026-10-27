package swarm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A catch-up task's scope names the Markdown that exists beside the changed code (a module's README is what its change made
// stale), besides the root Markdown and docs/: named files, never globs. A docs worker scoped to the root alone had to mail the
// manager to widen its scope in every run that changed a module with a README.
func TestDocsCatchUpScopeNamesTheMarkdownBesideTheChange(t *testing.T) {
	for in, want := range map[string]string{
		"lru/lru.go": "lru", "svc/ratelimit/limiter.go": "svc/ratelimit", "wordfreq/": "wordfreq", "web": "web",
		"internal/api/**": "internal/api", "**/*.go": "", "*.md": "", "/abs/path.go": "", "docs/**": "docs", "README.md": ".",
	} {
		if got := docsDirOf(in); got != want {
			t.Errorf("docsDirOf(%q) = %q, want %q", in, got, want)
		}
	}
	cfg := Config{MaxWorkers: 8, AutoDocs: true, Team: []string{"backend", "docs"}}
	r := newRVRigWith(t, cfg, func(context.Context, *rvCall) rvReply { return rvReply{Text: "noted"} }, nil)
	root := r.sw.deps.Root
	for _, f := range []string{"lru/README.md", "lru/NOTES.md", "svc/ratelimit/README.md", "svc/README.md", "README.md", "docs/guide.md"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, f)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, f), []byte("# x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	batch := []docsChange{{task: "T1", files: []string{"lru/lru.go"}}, {task: "T2", files: []string{"svc/ratelimit/limiter.go", "svc/ratelimit/keyed.go"}}, {task: "T3", files: []string{"web/**"}}}
	got := strings.Join(r.sw.docsScopeFor(batch), " ")
	if want := "*.md docs/** lru/NOTES.md lru/README.md svc/ratelimit/README.md"; got != want {
		t.Fatalf("scope = %q, want %q", got, want)
	}
	if got := strings.Join(r.sw.docsScopeFor(nil), " "); got != "*.md docs/**" {
		t.Fatalf("empty batch scope = %q", got)
	}
}

// A change whose scope names test files alone leaves no document stale and starts no docs pass.
func TestTestOnlyChangesStartNoDocsPass(t *testing.T) {
	for in, want := range map[string]bool{
		"lru/lru_test.go": true, "tests/api_test.py": true, "src/__tests__/app.test.tsx": true, "web/app.spec.ts": true,
		"spec/models/user_spec.rb": true, "test_api.py": true, "testdata/golden.json": true,
		"lru/lru.go": false, "lru/": false, "lru": false, "**/*_test.go": false, "README.md": false, "": false,
	} {
		if got := testFile(in); got != want {
			t.Errorf("testFile(%q) = %v, want %v", in, got, want)
		}
	}
	if testsOnly(nil) || testsOnly([]string{"lru/lru_test.go", "lru/lru.go"}) || !testsOnly([]string{"lru/lru_test.go", "tests/x_test.py"}) {
		t.Fatal("testsOnly")
	}
	cfg := Config{MaxWorkers: 8, AutoDocs: true, Team: []string{"backend", "docs"}}
	r := newRVRigWith(t, cfg, func(context.Context, *rvCall) rvReply { return rvReply{Text: "noted"} }, nil)
	b := r.sw.Board
	code, _ := b.CreateTask("mgr", TaskSpec{Title: "fix lru", Files: []string{"lru/lru.go"}})
	tests, _ := b.CreateTask("mgr", TaskSpec{Title: "cover lru", Files: []string{"lru/conv_test.go"}})
	for _, id := range []string{code.ID, tests.ID} {
		if err := b.Assign("mgr", "be-1", id); err != nil {
			t.Fatal(err)
		}
		if err := b.Submit("be-1", id, "done", "edited"); err != nil {
			t.Fatal(err)
		}
		if err := b.Accept("mgr", id, ""); err != nil {
			t.Fatal(err)
		}
	}
	r.sw.docs.mu.Lock()
	r.sw.docs.primed = true
	r.sw.docs.mu.Unlock()
	r.sw.docsTick(r.sw.deps.Now())
	r.sw.docs.mu.Lock()
	defer r.sw.docs.mu.Unlock()
	var ids []string
	for _, c := range r.sw.docs.pending {
		ids = append(ids, c.task)
	}
	if open := r.sw.docs.open; open != "" {
		if task, ok := b.Snapshot().Task(open); ok && !strings.Contains(task.Desc, code.ID) || strings.Contains(task.Desc, tests.ID) {
			t.Fatalf("docs pass %q covers %q", open, task.Desc)
		}
		return
	}
	if strings.Join(ids, " ") != code.ID {
		t.Fatalf("pending changes = %v, want only %s", ids, code.ID)
	}
}
