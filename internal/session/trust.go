package session

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/trust"
)

// How the project's own files came to be used by a session (trustInfo.How), or why they were not.
const (
	// trustFlag: --trust-project said so for this run.
	trustFlag = "flag"
	// trustRemembered: the person said yes before, to exactly these files (internal/trust).
	trustRemembered = "remembered"
	// trustAsked: the person was asked at the start and said yes for this session.
	trustAsked = "asked"
	// trustAskedAndSaved: asked, and said yes and to remember it until the files change.
	trustAskedAndSaved = "asked-and-remembered"
	// trustDeclined: asked, and the answer was no.
	trustDeclined = "declined"
	// trustNotAsked: not trusted, and there was nobody to ask (a run, a swarm, a rollout).
	trustNotAsked = "not-asked"
)

// trustHint is the end of a notice that says that the project's own files are not used: what to do about it. verb is "load" or "apply".
func trustHint(verb string) string {
	return "pass --trust-project to " + verb + " them for this run, or run `sleipnir trust add` to remember them until they change"
}

// trustInfo is what a session knows about the trust in its project: how it came about and, for /status and the log, what it covers.
type trustInfo struct {
	How string
	// Digest and Files are those of the footprint that was looked at ("" and 0 when the project has nothing that trust unlocks).
	Digest string
	Files  int
	// Saved is the day the person said yes, for a remembered answer.
	Saved string
	// Changed is how the files differ from what was trusted, when the answer that was remembered no longer applies.
	Changed string
}

// TrustLedgerPath is where the answers are kept: the user's state directory, never the project.
func TrustLedgerPath(home string) string { return filepath.Join(stateRoot(home), "trust.json") }

// resolveTrust decides whether the session may use what the project itself says (its instruction files, its settings, its skills):
// the flag says so for one run; an answer that the person gave before holds for exactly the files they saw (a changed file voids it, and
// they are told which); a chat asks when it has nothing else to go by, and a run, a swarm or a rollout, which have no one to ask, go
// without. On "yes" it sets o.TrustProject. The notices are for the person (the sink exists, the log does not yet).
func resolveTrust(ctx context.Context, o *Options) (*trustInfo, error) {
	if o.TrustProject {
		return &trustInfo{How: trustFlag}, nil
	}
	fp, err := trust.Scan(o.Root, o.Cwd, o.Home)
	if err != nil || fp.Empty() {
		return nil, nil // nothing that trust would unlock, or nowhere to look: the same as before
	}
	info := &trustInfo{Digest: fp.Digest, Files: len(fp.Files)}
	ledger := trust.OpenLedger(TrustLedgerPath(o.Home))
	state, entry := ledger.Check(o.Cwd, fp)
	switch state {
	case trust.Trusted:
		o.TrustProject = true
		info.How, info.Saved = trustRemembered, entry.Saved
		info.Files = len(fp.Files)
		sayTrust(o, "info", fmt.Sprintf("using this project's own instructions and settings, as you trusted them on %s (`sleipnir trust forget` undoes it)", entry.Saved))
		return info, nil
	case trust.Changed:
		info.Changed = trust.DescribeChanges(trust.Changes(entry, fp))
	}

	if !o.Interactive || o.Prompter == nil {
		info.How = trustNotAsked
		if info.Changed != "" {
			sayTrust(o, "warn", fmt.Sprintf("this project's files changed since you trusted them (%s), so they are not used; `sleipnir trust` shows what they are and remembers them again", info.Changed))
		}
		return info, nil
	}

	d := o.Prompter(ctx, perm.Request{Tool: perm.ToolProjectTrust, Cwd: o.Cwd, Risk: perm.RiskHigh, Summary: trustQuestion(fp, info.Changed)})
	if err := ctx.Err(); err != nil {
		return nil, err // the start was cancelled while the question was open: no session
	}
	switch {
	case d.Allow && d.Remember == perm.ScopeProject && !fp.Partial:
		o.TrustProject = true
		info.How = trustAskedAndSaved
		if err := ledger.Remember(o.Cwd, fp, o.Now()); err != nil {
			info.How = trustAsked
			sayTrust(o, "warn", "not remembered: "+err.Error())
		}
	case d.Allow:
		o.TrustProject = true
		info.How = trustAsked
		if d.Remember == perm.ScopeProject {
			sayTrust(o, "warn", "this project has more files than can be remembered, so it is trusted for this session only")
		}
	default:
		info.How = trustDeclined
	}
	return info, nil
}

// sayTrust emits a session-wide trust notice when a sink is configured.
func sayTrust(o *Options, level, msg string) {
	if o.Sink != nil {
		o.Sink.Notice("", level, msg)
	}
}

// trustQuestion is the question as the person reads it: the question itself, what the project has that would be used (a line for each,
// in the words of the footprint), a blank line and what that means, and when an answer was given before, what is not as it was (the last
// bracket is where the engine puts its reason, and the dialog sets it apart).
func trustQuestion(fp *trust.Footprint, changed string) string {
	var b strings.Builder
	b.WriteString("use this project's own instructions and settings?\n")
	b.WriteString(fp.Describe())
	b.WriteString("\n\nThese are added to every prompt and can start tool servers and run hooks. The repository's code is not covered by the answer.")
	if changed != "" {
		b.WriteString(" [changed since you trusted it: " + changed + "]")
	}
	return b.String()
}

// trustRecord is the part of session.start that says how the project came to be trusted; nothing for a flag (that is every run of a
// script and says nothing) and for a project that has nothing to trust.
func (t *trustInfo) record() map[string]any {
	if t == nil || t.How == "" || t.How == trustFlag {
		return nil
	}
	m := map[string]any{"how": t.How, "digest": t.Digest, "files": t.Files}
	if t.Changed != "" {
		m["changed"] = t.Changed
	}
	if t.Saved != "" {
		m["saved"] = t.Saved
	}
	return m
}
