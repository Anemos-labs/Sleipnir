package perm

import "strings"

// The answers to a question about an action, as every interface offers them: the chat's dialog, the line prompter of run and the plain
// chat, and the web page (internal/web/ui/js/85-ui-approvals.js holds the same words and numbers). Each has the number that chooses it,
// the same everywhere: 1 yes; 2 yes, and don't ask again, offered only when the engine can remember a rule that would let the request
// through (Request.RememberRules); 3 yes, and allow the builds and tests of the tests preset, offered for a build or test command
// (Request.OffersTests); and no, last, 3 or 4.

// Labels of the answers.
const (
	AnswerYesLabel   = "Yes"
	AnswerTestsLabel = "Yes, and allow builds and tests (go, npm, cargo, pytest, make…) for this session"
	AnswerNoLabel    = "No, and tell Sleipnir what to do instead"
)

// AnswerRememberLabel is the label of the answer that remembers: "Yes, and don't ask again for <what> this session".
func AnswerRememberLabel(what string) string {
	return "Yes, and don't ask again for " + what + " this session"
}

// Decisions of the answers a person gives; the line prompter and the web page add the instruction of a no (DeclinedWith).
var (
	DecisionYes      = Decision{Allow: true, Reason: "allowed by user"}
	DecisionRemember = Decision{Allow: true, Reason: "allowed by user for the session", Remember: ScopeSession}
	DecisionTests    = Decision{Allow: true, Reason: "builds and tests allowed by user for the session", Remember: ScopeSession, Preset: PresetTests}
	DecisionNo       = Decision{Allow: false, Reason: "denied by user"}
)

// Answer is one answer a question offers.
type Answer struct {
	Key      int    // the number that chooses it
	Letter   string // the letter the line prompter also takes for it (y, a, t, n)
	Label    string
	Decision Decision
}

// Answers lists the answers a question about an action offers, in order (see the head of this file). r is the request as the engine
// put it to the person.
func Answers(r Request) []Answer {
	out := []Answer{{Key: 1, Letter: "y", Label: AnswerYesLabel, Decision: DecisionYes}}
	if len(r.RememberRules) > 0 {
		out = append(out, Answer{Key: 2, Letter: "a", Label: AnswerRememberLabel(RememberWhat(r)), Decision: DecisionRemember})
	}
	no := 3
	if r.OffersTests {
		out = append(out, Answer{Key: 3, Letter: "t", Label: AnswerTestsLabel, Decision: DecisionTests})
		no = 4
	}
	return append(out, Answer{Key: no, Letter: "n", Label: AnswerNoLabel, Decision: DecisionNo})
}

// RememberWhat is what the answer that remembers names: Request.Remembers ("go test" commands, edits in this project), or the request
// itself by its kind (this command, this change, this request).
func RememberWhat(r Request) string {
	if r.Remembers != "" {
		return r.Remembers
	}
	switch strings.ToLower(r.Tool) {
	case "bash":
		return "this command"
	case "edit", "write", "apply_patch":
		return "this change"
	}
	return "this request"
}
