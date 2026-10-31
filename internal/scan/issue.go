package scan

import (
	"errors"
	"fmt"
	"strings"
)

// Issue is a finding written up for a project's issue tracker: what it is, where it is, how the scan knows, and the fix. The
// person opens it; nothing here sends anything.
func Issue(r *Run, l *Lead) (title, body string, err error) {
	if err := fixable(r, l); err != nil {
		return "", "", err
	}
	f := FindingOf(r, l)
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }
	title = fmt.Sprintf("%s: %s", strings.ToUpper(f.Severity), f.Title)
	w("%s\n\n", f.Claim)
	w("| Severity | Category | Reachability | Exploitability |\n|---|---|---|---|\n| %s | %s | %s | %s |\n\n", SeverityLabel(f), f.Category, f.Reach, f.Exploit)
	w("**Score**: %s\n\n", scoreMarkdown(f))
	w("## Where\n\nFrom where the input comes in to where it does harm, at commit `%s`:\n\n", short(r.Commit))
	for i, s := range f.Trace {
		w("%d. `%s:%d` %s\n\n", i+1, s.File, s.Line, oneLine(s.Note))
		writeFence(&b, "   ", s.Quote)
	}
	w("## How this was checked\n\n")
	confirms, _, defended := l.Tally()
	w("Confirmed by %d %s that did not report it", confirms, plural(confirms, "reviewer", "reviewers"))
	if defended {
		w(", one of them looking for the guard that would make it false")
	}
	w(".\n")
	if p := reproducing(l); p != nil {
		w("Reproduced by the script below, run against the commit in a sandbox with no network:\n\n<details><summary>Reproducer</summary>\n\n")
		writeFence(&b, "", p.Script)
		w("Output:\n\n")
		writeFence(&b, "", p.Output)
		w("</details>\n")
	}
	if f.Fix != "" {
		w("\n## Suggested fix\n\n%s\n", f.Fix)
	}
	w("\n---\n_Found by a Sleipnir deep scan (`%s`, finding %s). Every quote above was checked against the commit._\n", r.Slug, f.ID)
	return title, b.String(), nil
}

// FixObjective is the goal that has a team fix a finding: the cause, a regression test, and the reproducer run again on the
// fixed tree.
func FixObjective(r *Run, l *Lead) (string, error) {
	if err := fixable(r, l); err != nil {
		return "", err
	}
	f := FindingOf(r, l)
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }
	w("Fix finding %s of deep scan %s: %s (%s, %s).\n\n%s\n\n", f.ID, r.Slug, f.Title, f.Severity, f.Category, oneLine(f.Claim))
	w("Trace at commit %s, from the input to the harm:\n", short(r.Commit))
	for i, s := range f.Trace {
		w("%d. %s:%d %s: %s\n", i+1, s.File, s.Line, oneLine(s.Note), oneLine(s.Quote))
	}
	if f.Fix != "" {
		w("\nSuggested fix: %s\n", oneLine(f.Fix))
	}
	w("\nFix the cause (the missing or wrong check on the trace), not the symptom, and change nothing the finding does not need. ")
	w("Add a regression test that fails without the fix and passes with it.")
	if p := reproducing(l); p != nil {
		w(" Then run the reproducer below from the project's root and show it no longer prints %s:\n\n", MarkerReproduced)
		writeFence(&b, "", p.Script)
	} else {
		w("\n")
	}
	return b.String(), nil
}

// fixable refuses a lead that is not a finding: only what the verifiers confirmed is written up or fixed.
func fixable(r *Run, l *Lead) error {
	if l == nil {
		return errors.New("no such lead")
	}
	switch l.Status(r.DepthInfo()) {
	case LeadConfirmed, LeadUnproven:
		return nil
	case LeadRejected:
		return fmt.Errorf("%s was rejected (%s)", l.ID, l.RejectReason())
	}
	return fmt.Errorf("%s is still being verified: only a confirmed finding is written up or fixed", l.ID)
}

// reproducing is the lead's first reproducer that reproduced it, or nil.
func reproducing(l *Lead) *Proof {
	for i := range l.Proofs {
		if l.Proofs[i].Outcome == ProofReproduced {
			return &l.Proofs[i]
		}
	}
	return nil
}

// writeFence writes text in a code fence long enough that no line of it closes the fence.
func writeFence(b *strings.Builder, indent, text string) {
	fence := "```"
	for strings.Contains(text, fence) {
		fence += "`"
	}
	fmt.Fprintf(b, "%s%s\n", indent, fence)
	for _, l := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		fmt.Fprintf(b, "%s%s\n", indent, l)
	}
	fmt.Fprintf(b, "%s%s\n\n", indent, fence)
}
