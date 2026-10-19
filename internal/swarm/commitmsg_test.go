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
	msg := taskCommitMessage(Task{ID: "T7", Title: "T7: add a dark theme"}, "Added a dark theme toggle\nthat follows the system setting.")
	want := "Add a dark theme\n\nAdded a dark theme toggle\nthat follows the system setting.\n\nSleipnir-Task: T7"
	if msg != want {
		t.Fatalf("message:\n%s\nwant:\n%s", msg, want)
	}
	if got := taskCommitMessage(Task{ID: "T2", Title: "Write the README"}, "completed"); got != "Write the README\n\nSleipnir-Task: T2" {
		t.Fatalf("a bare \"completed\" should not become the body: %q", got)
	}
	if got := handoverCommitMessage(Task{ID: "T2", Title: "write the README"}, "w1"); !strings.HasPrefix(got, "Work in progress: Write the README\n") || strings.Contains(strings.SplitN(got, "\n", 2)[0], "T2") {
		t.Fatalf("handover message: %q", got)
	}
}

func TestFilesCommitSubject(t *testing.T) {
	if got := filesCommitSubject([]string{"README.md", "a.go", "b.go", "c.go"}); got != "Update README.md, a.go, b.go and 1 more" {
		t.Fatalf("got %q", got)
	}
}
