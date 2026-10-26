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
