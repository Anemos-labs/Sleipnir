package swarm

import (
	"strings"
	"testing"
)

func TestCommitSubjectDropsBoardID(t *testing.T) {
	for in, want := range map[string]string{
		"T12: add CSV export to reports": "Add CSV export to reports",
		"[T3] Wire the login form":       "Wire the login form",
		"T4 - fix the rate limiter.":     "Fix the rate limiter",
		"T9 support matrix":              "T9 support matrix",
		"  ":                             "",
	} {
		if got := commitSubject(in); got != want {
			t.Errorf("commitSubject(%q) = %q, want %q", in, got, want)
		}
	}
	if got := commitSubject(strings.Repeat("word ", 40)); len([]rune(got)) > commitSubjectMax {
		t.Errorf("subject longer than %d runes: %q", commitSubjectMax, got)
	}
}

func TestTaskCommitMessageDescribesTheChange(t *testing.T) {
	msg := taskCommitMessage(Task{ID: "T7", Title: "T7: add a dark theme"}, "Added a dark theme toggle\nthat follows the system setting.", "", nil)
	want := "Add a dark theme\n\nAdded a dark theme toggle\nthat follows the system setting.\n\nSleipnir-Task: T7"
	if msg != want {
		t.Fatalf("message:\n%s\nwant:\n%s", msg, want)
	}
	if got := taskCommitMessage(Task{ID: "T2", Title: "Write the README"}, "completed", "", nil); got != "Write the README\n\nSleipnir-Task: T2" {
		t.Fatalf("a bare \"completed\" should not become the body: %q", got)
	}
	if got := handoverCommitMessage(Task{ID: "T2", Title: "write the README"}); !strings.HasPrefix(got, "Work in progress: Write the README\n") || strings.Contains(strings.SplitN(got, "\n", 2)[0], "T2") {
		t.Fatalf("handover message: %q", got)
	}
}

func TestTaskCommitMessageKeepsTheRunOut(t *testing.T) {
	ids := []string{"manager", "fe-1", "be-2"}
	task := Task{ID: "T4", Title: "add CSV export"}

	got := taskCommitMessage(task, "Added CSV export in report.go. Told be-2 about the new column. Unblocks T5.\nTested with go test ./...", "", ids)
	want := "Add CSV export\n\nAdded CSV export in report.go.\nTested with go test ./...\n\nSleipnir-Task: T4"
	if got != want {
		t.Fatalf("message:\n%s\nwant:\n%s", got, want)
	}

	own := "feat(report): add CSV export\n\nThe report command writes CSV with --format csv."
	if got := taskCommitMessage(task, "done", own, ids); got != own+"\n\nSleipnir-Task: T4" {
		t.Fatalf("the worker's own message should be used: %q", got)
	}
	if got := taskCommitMessage(task, "", "feat: finish fe-1's export for T4", ids); !strings.HasPrefix(got, "Add CSV export\n") {
		t.Fatalf("a message naming the run should fall back to the title: %q", got)
	}
	if got := handoverCommitMessage(Task{ID: "T2", Title: "write the README"}); strings.Contains(got, "fe-") || strings.Contains(got, "handed over by") {
		t.Fatalf("handover message names the worker: %q", got)
	}
}

func TestFindRunRefs(t *testing.T) {
	patch := "diff --git a/src/app.ts b/src/app.ts\n--- a/src/app.ts\n+++ b/src/app.ts\n@@ -10,2 +10,4 @@\n const a = 1\n+// wired by fe-1, see task T3\n+function pick<T1, T2>(a: T1): T2 {}\n+const safe = \"safe-1\" + fe10\n" +
		"diff --git a/package-lock.json b/package-lock.json\n--- a/package-lock.json\n+++ b/package-lock.json\n@@ -1 +1 @@\n+\"fe-1\"\n"
	leaks := findRunRefs(patch, []string{"manager", "fe-1"}, 5)
	if len(leaks) != 1 || leaks[0].Path != "src/app.ts" || leaks[0].Line != 11 {
		t.Fatalf("leaks = %+v, want only src/app.ts:11", leaks)
	}
	if got := findRunRefs("+++ b/a.go\n@@ -0,0 +1 @@\n+// see [T12]\n", nil, 5); len(got) != 1 {
		t.Fatalf("a bracketed board id should be found without a roster: %+v", got)
	}
	if got := findRunRefs("+++ b/a.go\n@@ -0,0 +1 @@\n+// the manager decides\n", []string{"manager"}, 5); len(got) != 0 {
		t.Fatalf("an id that is an ordinary word should not be looked for: %+v", got)
	}
}

func TestFilesCommitSubject(t *testing.T) {
	if got := filesCommitSubject([]string{"README.md", "a.go", "b.go", "c.go"}); got != "Update README.md, a.go, b.go and 1 more" {
		t.Fatalf("got %q", got)
	}
}

// A member that retired is still matched: its successor may name it.
func TestEveryMemberKeepsRetiredIDs(t *testing.T) {
	s := &Swarm{members: map[string]*member{}}
	s.register(&member{id: "fe-1"}, nil)
	s.register(&member{id: "mailman", service: true}, nil)
	delete(s.members, "fe-1") // retired
	ids := s.everyMember()
	if len(ids) != 1 || ids[0] != "fe-1" {
		t.Fatalf("everyMember = %v, want [fe-1]", ids)
	}
	if got := findRunRefs("+++ b/a.go\n@@ -0,0 +1 @@\n+// started by fe-1\n", ids, 5); len(got) != 1 {
		t.Fatalf("a retired member's id should be found: %+v", got)
	}
}
