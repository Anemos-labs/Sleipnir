package swarm

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The commits the harness records end up in the project's history, where the next
// person (or agent) reads them with git log and searches them by what they did. A
// board id such as T12 means nothing outside the run that made it, so the subject
// says what the change is and the id rides along as a trailer.

// commitSubjectMax keeps a subject within what git log --oneline and forges show.
const commitSubjectMax = 72

// leadingTaskID matches a board id a title starts with ("T12: ", "[T3] ", "T4 - ").
var leadingTaskID = regexp.MustCompile(`^\s*(?:\[T\d+\]\s*|T\d+\s*[:\-–—]\s*)`)

// commitSubject is title as a commit subject: one line, without a leading board id,
// starting with a capital letter.
func commitSubject(title string) string {
	s := strings.TrimSpace(leadingTaskID.ReplaceAllString(cleanText(title, 200), ""))
	if s == "" {
		return ""
	}
	r, n := utf8.DecodeRuneInString(s)
	s = string(unicode.ToUpper(r)) + s[n:]
	return strings.TrimRight(truncRunes(s, commitSubjectMax), " .")
}

// taskCommitMessage is the message of the commit that records a worker's work on t:
// the task's title as the subject, what the worker said it did as the body, and the
// task id as a trailer for whoever has the run's board.
func taskCommitMessage(t Task, summary string) string {
	subject := commitSubject(t.Title)
	if subject == "" {
		subject = "Work on " + t.ID
	}
	var b strings.Builder
	b.WriteString(subject)
	if body := cleanBlock(summary, 2000); body != "" && !strings.EqualFold(body, "completed") && !strings.EqualFold(body, subject) {
		b.WriteString("\n\n" + body)
	}
	fmt.Fprintf(&b, "\n\nSleipnir-Task: %s", t.ID)
	return b.String()
}

// handoverCommitMessage records the unfinished work of agent on t when it hands the
// task over.
func handoverCommitMessage(t Task, agent string) string {
	subject := commitSubject("Work in progress: " + commitSubject(t.Title))
	return fmt.Sprintf("%s\n\nUnfinished work handed over by %s.\n\nSleipnir-Task: %s", subject, agent, t.ID)
}

// filesCommitSubject names a change by the files it touches, for work that has no
// task title (the manager's own edits).
func filesCommitSubject(files []string) string {
	if len(files) == 0 {
		return "Update project files"
	}
	n := min(len(files), 3)
	s := "Update " + strings.Join(files[:n], ", ")
	if more := len(files) - n; more > 0 {
		s += fmt.Sprintf(" and %d more", more)
	}
	return commitSubject(s)
}

// stripTaskID drops the "T12: " a merge-queue task label starts with.
func stripTaskID(label string) string {
	return strings.TrimSpace(leadingTaskID.ReplaceAllString(label, ""))
}
