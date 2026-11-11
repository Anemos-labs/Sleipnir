package swarm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/tools"
	"github.com/anemos-labs/sleipnir/internal/workspace"
)

// stubTool answers every call with err as its error text, or with success when err is "".
type stubTool struct{ err *string }

func (s stubTool) Spec() core.ToolSpec { return core.ToolSpec{Name: "mail"} }
func (s stubTool) Run(context.Context, *tools.Call) (*tools.Result, error) {
	if *s.err != "" {
		return &tools.Result{IsError: true, Text: *s.err}, nil
	}
	return &tools.Result{Text: "sent"}, nil
}

// The wrapper records a refused delivery in the agent's view and clears it when the same delivery succeeds.
func TestUnsentToolRecordsARefusalAndClearsItOnSuccess(t *testing.T) {
	s := &Swarm{}
	s.unsent.now = time.Now
	why := "message too long"
	tool := unsentTool{inner: stubTool{err: &why}, s: s}
	call := &tools.Call{Input: json.RawMessage(`{"to":"fe-1","text":"x"}`), Env: &tools.Env{Agent: "be-1"}}
	if _, err := tool.Run(context.Background(), call); err != nil {
		t.Fatal(err)
	}
	if lines := s.unsent.lines("be-1"); len(lines) != 2 || !strings.Contains(lines[1], "your mail to fe-1") || !strings.Contains(lines[1], why) {
		t.Fatalf("view after the refusal: %q", lines)
	}
	why = ""
	if _, err := tool.Run(context.Background(), call); err != nil {
		t.Fatal(err)
	}
	if lines := s.unsent.lines("be-1"); lines != nil {
		t.Fatalf("view after the retry: %q", lines)
	}
}

// The manager coordinates the whole team and is not held to the per-sender rate; a worker is.
func TestRouterSenderRateSparesTheManager(t *testing.T) {
	cfg := DefaultRouterConfig()
	var ids []string
	for i := range cfg.MaxPerMinute + 2 {
		ids = append(ids, fmt.Sprintf("w-%d", i))
	}
	r := NewRouter(cfg, nil, func() []string { return append([]string{"mgr", "be-1"}, ids...) }, func() string { return "mgr" }, func(Message) {})
	for i, id := range ids {
		if _, err := r.Send("mgr", id, "info", fmt.Sprintf("note %d", i)); err != nil {
			t.Fatalf("the manager's mail %d: %v", i, err)
		}
	}
	var refused error
	for i, id := range ids {
		if _, err := r.Send("be-1", id, "info", fmt.Sprintf("fact %d", i)); err != nil {
			refused = err
			break
		}
	}
	if refused == nil {
		t.Fatal("a worker was not held to the per-sender rate")
	}
}

// A batch of narrow commands is not combined into a check of the whole tree; one that already checks the whole tree is.
func TestCombineVerifyDoesNotWidenNarrowCommandsToTheWholeTree(t *testing.T) {
	dir := t.TempDir()
	combine := CombineVerify("go test {dirs}")
	var subs []workspace.Submission
	for i := range maxVerifyDirs + 5 {
		scope := []string{fmt.Sprintf("p%02d/x.go", i)}
		subs = append(subs, workspace.Submission{VerifyScope: scope, VerifyCmd: ExpandVerify("go test {dirs}", dir, scope)})
	}
	if cmd, ok := combine(dir, subs); ok {
		t.Fatalf("narrow commands were combined into %q", cmd)
	}
	if cmd, ok := combine(dir, subs[:2]); !ok || !strings.Contains(cmd, "p00") || !strings.Contains(cmd, "p01") {
		t.Fatalf("two narrow commands: %q %v", cmd, ok)
	}
	subs[0].VerifyCmd = ExpandVerify("go test {dirs}", dir, nil)
	if _, ok := combine(dir, subs); !ok {
		t.Fatal("a batch with a whole-tree member was not combined")
	}
}

// A byte-bounded opening never splits a rune, and a budget below one rune shows nothing.
func TestInlineHeadBytesCutsOnARuneBoundary(t *testing.T) {
	text := strings.Repeat("契約", 20000) // 3 bytes a rune
	head, rest := inlineHeadBytes(text, 1000)
	if len(head) > 1000 || !utf8.ValidString(head) || utf8.RuneCountInString(head)+rest != utf8.RuneCountInString(text) {
		t.Fatalf("head of %d bytes, %d runes left", len(head), rest)
	}
	if head, rest := inlineHeadBytes(text, 2); head != "" || rest != utf8.RuneCountInString(text) {
		t.Fatalf("a 2-byte budget gave %q, %d left", head, rest)
	}
	if head, rest := inlineHeadBytes("ab  cd", 4); head != "ab" || rest != 4 {
		t.Fatalf("trimmed spaces are not counted as left out: %q, %d", head, rest)
	}
	if head, rest := inlineHead("ab  cd", 4); head != "ab" || rest != 4 {
		t.Fatalf("trimmed spaces are not counted as left out: %q, %d", head, rest)
	}
	card := agreementCard([]TaskAgreement{{Task: "T1", Text: text}, {Task: "T2", Text: text}, {Task: "T3", Text: "x"}})
	if len(card) > agreementCardBytes+2000 || !utf8.ValidString(card) {
		t.Fatalf("a card of %d bytes", len(card))
	}
}

// A file no task claims is not free for a child whose parent's scope leaves it out: the merge queue refuses it for that child
// (submitRefusal), so the write is refused too. A lockfile stays free.
func TestUnclaimedFileOutsideTheParentScopeIsNotFree(t *testing.T) {
	b := NewBoard(nil)
	parent, err := b.CreateTask("mgr", TaskSpec{Title: "web", Role: "frontend", Files: []string{"web/**"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.beginPool("mgr", parent.ID, []string{"fe-1", "fe-2"}); err != nil {
		t.Fatal(err)
	}
	if err := b.poolUpdate("mgr", parent.ID, func(_ *Snapshot, _ *Task, p *PoolState) error { p.Phase = "executing"; return nil }); err != nil {
		t.Fatal(err)
	}
	child, err := b.CreateTask("fe-1", TaskSpec{Parent: parent.ID, Title: "reader", Role: "frontend", Files: []string{"web/reader/**"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.assignTask(assignReq{by: "fe-1", agent: "fe-1", id: child.ID}); err != nil {
		t.Fatal(err)
	}
	l := NewLeases(0, b)
	l.Isolate()
	tree := t.TempDir()
	l.BindTree("fe-1", tree)
	write := func(rel string) error { return l.BeforeWrite("fe-1", filepath.Join(tree, filepath.FromSlash(rel))) }
	if !l.outsideParent("fe-1", "docs/notes.md") {
		t.Fatal("a path outside the parent's scope counted as free")
	}
	if err := write("docs/notes.md"); err == nil {
		t.Fatal("the write guard let a pool child write an unclaimed file outside its parent's scope")
	}
	if l.outsideParent("fe-1", "web/shared/helper.ts") || l.outsideParent("fe-1", "package-lock.json") {
		t.Fatal("a path inside the parent's scope, or a lockfile, was held to it")
	}
	if l.outsideParent("be-1", "docs/notes.md") {
		t.Fatal("an agent with no parent-scoped task was held to one")
	}
	// a task of its own that no parent bounds can carry the path, so the write is free again
	own, err := b.CreateTask("mgr", TaskSpec{Title: "notes", Role: "frontend", Files: []string{"notes/**"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.assignTask(assignReq{by: "mgr", agent: "fe-1", id: own.ID}); err != nil {
		t.Fatal(err)
	}
	if l.outsideParent("fe-1", "docs/notes.md") {
		t.Fatal("a path a parentless held task can submit was held to the child's parent")
	}
	if err := write("docs/notes.md"); err != nil {
		t.Fatalf("the write guard refused an unclaimed file the parentless task can submit: %v", err)
	}
	ownNow, _ := b.Snapshot().Task(own.ID)
	if refuse := (&Swarm{Board: b}).submitRefusal("frontend", ownNow); refuse != nil && refuse("docs/notes.md") != "" {
		t.Fatalf("the merge queue refuses the parentless task's submission of it: %s", refuse("docs/notes.md"))
	}
	if !Unclaimed(b.Snapshot(), "fe-1", "docs/notes.md") {
		t.Fatal("the merge queue's AllowOutside does not let the parentless task carry it")
	}
	got, _ := b.Snapshot().Task(child.ID)
	refuse := (&Swarm{Board: b}).submitRefusal("frontend", got)
	if refuse == nil || refuse("docs/notes.md") == "" {
		t.Fatal("the merge queue's refusal and the write guard disagree")
	}
}

// A refused task deliver is kept in the manager's view like any other delivery, and a call with no agent records nothing.
func TestUnsentKeepsADeliverAndSkipsACallWithoutAnAgent(t *testing.T) {
	if key, what, ok := unsentKey("task", json.RawMessage(`{"action":"deliver"}`)); !ok || key != "task:deliver" || what != "your task deliver" {
		t.Fatalf("deliver: %q %q %v", key, what, ok)
	}
	s := &Swarm{}
	s.unsent.now = time.Now
	why := "refused"
	tool := unsentTool{inner: stubTool{err: &why}, s: s}
	for _, call := range []*tools.Call{nil, {Input: json.RawMessage(`{"to":"fe-1"}`)}, {Input: json.RawMessage(`{"to":"fe-1"}`), Env: &tools.Env{}}} {
		if _, err := tool.Run(context.Background(), call); err != nil {
			t.Fatal(err)
		}
	}
	if len(s.unsent.by) != 0 {
		t.Fatalf("a call without an agent was recorded: %v", s.unsent.by)
	}
}

// {dirs} covers what the tree changed outside the task's scope, but not a lockfile a build rewrote; when the changes cannot
// be listed it covers the whole tree.
func TestVerifyFilesAddsOutsideChangesButNotLockfiles(t *testing.T) {
	r := newIsoRig(t, isoOpts{dirsVerify: true, files: map[string]string{"pkg/a.txt": "a\n", "lib/b.txt": "b\n"}}, script{}.fn())
	ctx := context.Background()
	tree, err := r.mgr.Create(ctx, "be-9", workspace.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"pkg/a.txt": "a2\n", "lib/b.txt": "b2\n", "go.sum": "x\n"} {
		if err := os.WriteFile(filepath.Join(tree.Path, filepath.FromSlash(name)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m := &member{id: "be-9", tree: tree}
	task := Task{ID: "T9", Files: []string{"pkg/"}}
	got := r.sw.verifyFiles(ctx, m, task)
	if !slices.Equal(got, []string{"pkg/", "lib/b.txt"}) {
		t.Fatalf("verifyFiles = %q", got)
	}
	if err := tree.Remove(ctx, true); err != nil {
		t.Fatal(err)
	}
	if got := r.sw.verifyFiles(ctx, m, task); got != nil {
		t.Fatalf("verifyFiles of a tree whose changes cannot be listed = %q, want nil (the whole tree)", got)
	}
}
