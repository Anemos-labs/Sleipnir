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
		"tests/.env": false, "tests/**": false, "tests/": false, "tests": false, "docs/api_spec.md": false, "fixtures/x_test.json": false,
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
	cur, _ := b.Snapshot().Task(code.ID)
	r.sw.docs.seen[code.ID] = cur.Rev // the code change is known already: only the test-only one is new
	r.sw.docs.mu.Unlock()
	if r.sw.docsWillStart(b.Snapshot()) {
		t.Fatal("a test-only change made the pre-tick check say a docs pass is due")
	}
	r.sw.docs.mu.Lock()
	delete(r.sw.docs.seen, code.ID)
	r.sw.docs.mu.Unlock()
	if !r.sw.docsWillStart(b.Snapshot()) {
		t.Fatal("the code change does not start a docs pass")
	}
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

// The manager's own edits to product files start a docs pass of their own once they settle (or once the manager means to
// finish): no task's acceptance covers them. Its test and documentation edits start none.
func TestTheManagersOwnEditsStartADocsPassOnceTheySettle(t *testing.T) {
	cfg := Config{MaxWorkers: 8, AutoDocs: true, Team: []string{"backend", "docs"}}
	r := newRVRigWith(t, cfg, func(context.Context, *rvCall) rvReply { return rvReply{Text: "noted"} }, nil)
	root := r.sw.deps.Root
	if err := os.MkdirAll(filepath.Join(root, "wordfreq"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "wordfreq", "README.md"), []byte("# x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"wordfreq/wordfreq_test.go", "wordfreq/README.md", "docs/guide.md"} {
		r.sw.noteManagerEdit(p)
	}
	if r.sw.docsPending() {
		t.Fatal("test and documentation edits are a docs change")
	}
	for _, p := range []string{"wordfreq/wordfreq.go", "jsonpath/jsonpath.go", "wordfreq/wordfreq.go"} {
		r.sw.noteManagerEdit(p)
	}
	if !r.sw.docsPending() {
		t.Fatal("the manager's code edits leave nothing for auto-docs to do")
	}
	now := r.sw.deps.Now()
	r.sw.docsTick(now)
	r.sw.docs.mu.Lock()
	open, pending := r.sw.docs.open, len(r.sw.docs.pending)
	r.sw.docs.mu.Unlock()
	if open != "" || pending != 0 {
		t.Fatalf("a pass started while the manager was still editing: open %q, %d pending", open, pending)
	}
	r.sw.docsTick(now.Add(docsEditQuiet))
	r.sw.docs.mu.Lock()
	open, last, edited := r.sw.docs.open, r.sw.docs.last, len(r.sw.docs.edited)
	r.sw.docs.mu.Unlock()
	if open == "" || edited != 0 {
		t.Fatalf("settled edits started no pass: open %q, %d edited", open, edited)
	}
	if last != "" {
		t.Fatalf("last pass covered up to %q: the manager's edits are no task", last)
	}
	task, _ := r.sw.Board.Snapshot().Task(open)
	if !strings.Contains(task.Desc, "wordfreq/wordfreq.go, jsonpath/jsonpath.go)") || !strings.Contains(strings.Join(task.Files, " "), "wordfreq/README.md") {
		t.Fatalf("pass %q: %q, scope %v", task.Title, task.Desc, task.Files)
	}

	// A manager that means to finish has no more edits coming: they are a change at once.
	r2 := newRVRigWith(t, cfg, func(context.Context, *rvCall) rvReply { return rvReply{Text: "noted"} }, nil)
	r2.sw.noteManagerEdit("lru/lru.go")
	r2.sw.flushManagerEdits()
	r2.sw.docs.mu.Lock()
	defer r2.sw.docs.mu.Unlock()
	if len(r2.sw.docs.pending) != 1 || r2.sw.docs.pending[0].task != managerEdits || !r2.sw.docs.coded {
		t.Fatalf("flushed edits: %+v", r2.sw.docs.pending)
	}
}

// With worktree isolation the manager edits its own tree, and its changes reach the integration branch when they are flushed: the
// files that flush integrates are the manager's edits auto-docs counts.
func TestAnIsolatedManagersIntegratedEditsAreADocsChange(t *testing.T) {
	r := newIsoRig(t, isoOpts{cfg: Config{MaxWorkers: 8, AutoDocs: true, Team: []string{"backend", "docs"}}, files: map[string]string{"api/x.go": "package api\n", "api/x_test.go": "package api\n"}},
		func(context.Context, *rvCall) rvReply { return rvReply{Text: "done"} })
	if _, err := r.sw.StartManager(); err != nil {
		t.Fatal(err)
	}
	m := r.sw.get(r.sw.ManagerID())
	for f, body := range map[string]string{"api/x.go": "package api\n// changed\n", "api/x_test.go": "package api\n// changed\n"} {
		if err := writeFileString(filepath.Join(m.dir, f), body); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.sw.flushManager(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	r.sw.docs.mu.Lock()
	defer r.sw.docs.mu.Unlock()
	if got := strings.Join(r.sw.docs.edited, " "); got != "api/x.go" {
		t.Fatalf("edits auto-docs counts = %q, want the product file alone", got)
	}
}

// A task resolved as no longer needed landed nothing: it starts no docs pass and is not listed in one.
func TestAResolvedTaskStartsNoDocsPass(t *testing.T) {
	cfg := Config{MaxWorkers: 8, AutoDocs: true, Team: []string{"backend", "docs"}}
	r := newRVRigWith(t, cfg, func(context.Context, *rvCall) rvReply { return rvReply{Text: "noted"} }, nil)
	b := r.sw.Board
	code, _ := b.CreateTask("mgr", TaskSpec{Title: "ws hub", Files: []string{"cp/ws.go"}})
	dup, _ := b.CreateTask("mgr", TaskSpec{Title: "ws hub (solo)", Files: []string{"cp/ws.go"}})
	if err := b.Assign("mgr", "be-1", code.ID); err != nil {
		t.Fatal(err)
	}
	if err := b.Submit("be-1", code.ID, "done", "edited"); err != nil {
		t.Fatal(err)
	}
	if err := b.Accept("mgr", code.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := b.resolve("mgr", dup.ID, "duplicate of "+code.ID); err != nil {
		t.Fatal(err)
	}
	r.sw.docs.mu.Lock()
	r.sw.docs.primed = true
	cur, _ := b.Snapshot().Task(code.ID)
	r.sw.docs.seen[code.ID] = cur.Rev // the accepted change is known already: only the resolved one is new
	r.sw.docs.coded = true
	r.sw.docs.mu.Unlock()
	if r.sw.docsWillStart(b.Snapshot()) {
		t.Fatal("a resolved task made the pre-tick check say a docs pass is due")
	}
	r.sw.docsTick(r.sw.deps.Now())
	r.sw.docs.mu.Lock()
	if len(r.sw.docs.pending) != 0 || r.sw.docs.open != "" {
		t.Fatalf("a resolved task became a docs change: pending %v, open %q", r.sw.docs.pending, r.sw.docs.open)
	}
	r.sw.docs.mu.Unlock()

	// A task resolved after its work merged (under isolation a submission merges before review) ships that work: it is a change.
	merged, _ := b.CreateTask("mgr", TaskSpec{Title: "ws topics", Files: []string{"cp/topics.go"}})
	if err := b.Assign("mgr", "be-1", merged.ID); err != nil {
		t.Fatal(err)
	}
	if err := b.Submit("be-1", merged.ID, "done", "edited"); err != nil {
		t.Fatal(err)
	}
	r.sw.review.mu.Lock()
	r.sw.review.landLocked(merged.ID, []string{"cp/topics.go"}, 0)
	r.sw.review.mu.Unlock()
	if err := b.resolve("mgr", merged.ID, "superseded after merge"); err != nil {
		t.Fatal(err)
	}
	if !r.sw.docsWillStart(b.Snapshot()) {
		t.Fatal("a task resolved after its work merged does not start a docs pass")
	}
}

// A resumed session's resolved implementation task does not count as accepted code: an agreement accepted later still waits for
// the first implementation to land before it starts a pass.
func TestAResolvedTaskDoesNotPrimeAutoDocsAsCoded(t *testing.T) {
	cfg := Config{MaxWorkers: 8, AutoDocs: true, Team: []string{"backend", "docs"}}
	r := newRVRigWith(t, cfg, func(context.Context, *rvCall) rvReply { return rvReply{Text: "noted"} }, nil)
	b := r.sw.Board
	dup, _ := b.CreateTask("mgr", TaskSpec{Title: "ws hub", Files: []string{"cp/ws.go"}})
	if err := b.resolve("mgr", dup.ID, "not needed"); err != nil {
		t.Fatal(err)
	}
	r.sw.docsTick(r.sw.deps.Now()) // primes on the resumed board
	r.sw.docs.mu.Lock()
	defer r.sw.docs.mu.Unlock()
	if r.sw.docs.coded {
		t.Fatal("a resolved implementation task primed the keeper as coded")
	}
}
