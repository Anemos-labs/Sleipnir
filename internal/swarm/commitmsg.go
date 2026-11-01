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

// boardIDList matches a list of board ids in parentheses or brackets that a title carries as a label, as the harness's
// own tasks do ("docs: catch up (T1, T4 and 2 more)"): without it the title is a subject, where the whole title used to
// give way to "Update project files" on every documentation pass.
var boardIDList = regexp.MustCompile(`\s*[(\[]\s*T\d+(?:\s*(?:,|and)\s*(?:T\d+|\d+ more))*\s*[)\]]`)

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

// taskCommitMessage is the message of the commit that records a worker's work on t.
// A message the worker wrote for the commit (task done's commit) is used as it is
// when it names no team member or board task; otherwise the subject is the task's
// title and the body what the worker said it did, without the sentences that name
// the team (ids) or the board, which mean nothing to a reader of the history. The
// task id rides along as a trailer for whoever has the run's board.
func taskCommitMessage(t Task, summary, commit string, ids []string) string {
	trailer := "Sleipnir-Task: " + t.ID
	if msg := cleanBlock(commit, 4000); msg != "" && !mentionsRun(msg, ids) {
		subject, body, _ := strings.Cut(msg, "\n")
		if subject = strings.TrimRight(truncRunes(strings.TrimSpace(subject), commitSubjectMax), " ."); subject != "" {
			if body = strings.TrimSpace(body); body != "" {
				return subject + "\n\n" + body + "\n\n" + trailer
			}
			return subject + "\n\n" + trailer
		}
	}
	subject := commitSubject(t.Title)
	if subject == "" || mentionsRun(subject, ids) {
		subject = commitSubject(boardIDList.ReplaceAllString(t.Title, ""))
	}
	if subject == "" || mentionsRun(subject, ids) {
		subject = "Update project files"
	}
	var b strings.Builder
	b.WriteString(subject)
	if body := scrubRunRefs(cleanBlock(summary, 2000), ids); body != "" && !strings.EqualFold(body, "completed") && !strings.EqualFold(body, subject) {
		b.WriteString("\n\n" + body)
	}
	b.WriteString("\n\n" + trailer)
	return b.String()
}

// handoverCommitMessage records the unfinished work on t when its worker hands the
// task over. The worker is not named: the next commit continues the same change.
func handoverCommitMessage(t Task) string {
	subject := commitSubject("Work in progress: " + commitSubject(t.Title))
	return fmt.Sprintf("%s\n\nUnfinished; continued by a later commit.\n\nSleipnir-Task: %s", subject, t.ID)
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
