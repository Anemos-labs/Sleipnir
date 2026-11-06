package swarm

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// In a shared tree the manager's accept says to commit the change with its worker's message, in the repository that holds the
// work (a project in a subdirectory of the session's directory included); nothing is said when the work is in no repository. A run
// accepted seven tasks and left the tree dirty at the starting commit.
func TestSharedTreeAcceptSaysToCommitWithTheWorkersMessage(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "check")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "internal", "store"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := New(Config{SessionID: "t"}, Deps{Root: root}, nil)
	task := Task{ID: "T3", Title: "store", Files: []string{"check/internal/store/**"}}
	s.noteSharedCommit(task, "feat(store): keep on-the-day records\n\nThe store keeps a record dated today.")
	got := s.sharedCommitHint(task, s.sharedCommitMessage(task.ID))
	if !strings.Contains(got, "commit this change now in check (below the project root)") || !strings.Contains(got, `"feat(store): keep on-the-day records"`) || !strings.Contains(got, "check/internal/store/**") {
		t.Fatalf("hint = %q", got)
	}
	if again := s.sharedCommitHint(task, s.sharedCommitMessage(task.ID)); !strings.Contains(again, `"Store"`) {
		t.Fatalf("without a kept message the subject comes from the title: %q", again)
	}
	if none := s.sharedCommitHint(Task{ID: "T4", Title: "web", Files: []string{"web/**"}}, ""); none != "" {
		t.Fatalf("hint for work in no repository below the root: %q", none)
	}
	plan := Task{ID: "T5", Title: "plan", Kind: TaskKindPlan, Files: []string{"check/**"}}
	if got := s.sharedCommitHint(plan, s.sharedCommitMessage(plan.ID)); got != "" {
		t.Fatalf("a plan commits nothing: %q", got)
	}
}

// The repository is named so the manager can run git there whatever the length of its path: relative to the project root, or
// "the project root" itself, never cut.
func TestSharedCommitHintNamesTheRepositoryWhole(t *testing.T) {
	root := filepath.Join(t.TempDir(), strings.Repeat("deep", 40))
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := New(Config{SessionID: "t"}, Deps{Root: root}, nil)
	got := s.sharedCommitHint(Task{ID: "T1", Title: "store", Files: []string{"internal/store/**"}}, "")
	if !strings.Contains(got, "commit this change now in the project root,") || strings.Contains(got, "…") {
		t.Fatalf("hint = %q", got)
	}
}

// The kept messages are bounded, and the messages of submissions that were never accepted do not crowd out a current one: at the
// bound they are dropped first. A message kept for a task is replaced by its next submission's.
func TestSharedCommitMessagesStayBoundedWithoutLosingCurrentOnes(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := New(Config{SessionID: "t"}, Deps{Root: root}, nil)
	for i := range maxSharedCommits {
		s.noteSharedCommit(Task{ID: "gone-" + strconv.Itoa(i)}, "fix: an abandoned submission")
	}
	cur := Task{ID: "T1", Title: "store", Files: []string{"internal/store/**"}}
	s.noteSharedCommit(cur, "feat(store): first try")
	s.noteSharedCommit(cur, "feat(store): keep on-the-day records")
	if n := sharedCommitsKept(s); n > maxSharedCommits {
		t.Fatalf("%d messages kept after the submission, bound %d", n, maxSharedCommits)
	}
	if got := s.sharedCommitHint(cur, s.sharedCommitMessage(cur.ID)); !strings.Contains(got, `"feat(store): keep on-the-day records"`) {
		t.Fatalf("stale messages crowded out the current one: %q", got)
	}
	if n := sharedCommitsKept(s); n > maxSharedCommits {
		t.Fatalf("%d messages kept, bound %d", n, maxSharedCommits)
	}
}

// A cleanup at the bound must not take the message of a task submitting now (still doing: its message is kept before it enters
// review) or of a task being accepted (done once the board accepts it, its message read before that).
func TestSharedCommitCleanupKeepsInFlightSubmissionsAndAccepts(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := New(Config{SessionID: "t"}, Deps{Root: root}, nil)
	submitting := Task{ID: "T1", Title: "store", Status: StatusDoing, Owner: "be-1", Files: []string{"internal/store/**"}}
	accepting := Task{ID: "T2", Title: "web", Status: StatusReview, Owner: "be-2", Files: []string{"web/**"}}
	s.noteSharedCommit(submitting, "feat(store): keep on-the-day records")
	s.noteSharedCommit(accepting, "feat(web): show the day")
	msg := s.sharedCommitMessage(accepting.ID) // the accept reads the message while the task is in review
	accepting.Status = StatusDone              // then the board accepts it, before the hint drops the message
	s.Board.RestoreKeeping(&Snapshot{Tasks: []Task{submitting, accepting}}, nil, map[string]bool{"T1": true, "T2": true})
	if got, _ := s.Board.Snapshot().Task("T2"); got.Status != StatusDone {
		t.Fatalf("T2 is %s, want done", got.Status)
	}
	for i := range maxSharedCommits {
		s.noteSharedCommit(Task{ID: "gone-" + strconv.Itoa(i)}, "fix: an abandoned submission")
	}
	if n := sharedCommitsKept(s); n > maxSharedCommits {
		t.Fatalf("%d messages kept, bound %d", n, maxSharedCommits)
	}
	if got := s.sharedCommitMessage(submitting.ID); got != "feat(store): keep on-the-day records" {
		t.Fatalf("the cleanup took the message of a task submitting now: %q", got)
	}
	if got := s.sharedCommitHint(accepting, msg); !strings.Contains(got, `"feat(web): show the day"`) {
		t.Fatalf("the cleanup took the message of a task being accepted: %q", got)
	}
}

func sharedCommitsKept(s *Swarm) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sharedCommits)
}

// Through the task tool: the manager's accept of a submission in a shared tree carries its worker's commit subject, though the
// kept messages are at their bound.
func TestTheAcceptToolCarriesTheWorkersSubjectAtTheBound(t *testing.T) {
	r := newRVRig(t, Config{}, gatedWorkers(nil))
	if err := os.MkdirAll(filepath.Join(r.sw.deps.Root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	task := inReview(t, r.sw.Board, "frontend", "fe-4", `edited 1 (Alerts.tsx); last test "npm test" passed`)
	for i := range maxSharedCommits - 1 {
		r.sw.noteSharedCommit(Task{ID: "gone-" + strconv.Itoa(i)}, "fix: an abandoned submission")
	}
	r.sw.noteSharedCommit(task, "feat(web): show the alerts page")
	res := r.callTool(context.Background(), "task", "mgr", "manager", map[string]any{"action": "accept", "id": task.ID, "text": "looks good"})
	if res.IsError || !strings.Contains(res.Text, `"feat(web): show the alerts page"`) {
		t.Fatalf("the accept does not carry the worker's subject: %s", res.Text)
	}
	if got := r.sw.sharedCommitMessage(task.ID); got != "" {
		t.Fatalf("the accepted task's message is still kept: %q", got)
	}
}
