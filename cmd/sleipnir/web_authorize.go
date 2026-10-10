package main

// Privilege-raising effects of the page are authorized where they happen, on what will take effect: the final argument list of a new
// session or a restart (after restartArgs, the staged flags and the parser), the mode a tab is set to, the rules it is given. An
// effect that raises privilege above what the session (or, for a new one, the server's own command line) already has needs a
// confirmation id for a scope bound to exactly what is raised: a dangerous mode (bypass, yolo), trusting a project's own files,
// allow rules the session does not have, a --verify command (a shell command the harness runs), the removal of a deny or ask rule.
//
// The id is minted by POST /api/confirm for any scope a signed-in client asks for (or issued with a trust challenge): the
// confirmation stops forged, replayed, stale and cross-page requests and gives the page the moment to show the person what is
// raised; it is not a second credential against a client that holds the session.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/trust"
	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/approvals"
)

// confirmKey is the context key of a request's confirmation gate.
type confirmKey struct{}

// confirmGate is how an effect asks for the confirmation of the request that caused it.
type confirmGate struct {
	srv *web.Server
	w   http.ResponseWriter
	r   *http.Request
}

// withConfirmGate returns r with a context through which the effects it causes can require its confirmation.
func withConfirmGate(srv *web.Server, w http.ResponseWriter, r *http.Request) *http.Request {
	g := &confirmGate{srv: srv, w: w}
	r = r.WithContext(context.WithValue(r.Context(), confirmKey{}, g))
	g.r = r
	return r
}

// confirmRequired is the refusal of an effect that needs a confirmation: the scope to ask for and what it raises. It is answered 428
// confirm_required with the scope in X-Confirm-Scope and in the detail.
type confirmRequired struct {
	Scope   string   `json:"scope"`
	Reasons []string `json:"reasons"`
}

// Error says what needs the person's confirmation.
func (e *confirmRequired) Error() string {
	return "this needs your confirmation (" + strings.Join(e.Reasons, "; ") + "): do it from the page, which asks for it"
}

// errConfirmAnswered is an effect refused because its confirmation id was not good; the refusal (403 confirm_invalid) was written.
var errConfirmAnswered = errors.New("the confirmation was refused")

// isAuthErr reports whether err is a refusal for want of a confirmation, which a route answers as such.
func isAuthErr(err error) bool {
	var cr *confirmRequired
	return errors.As(err, &cr) || errors.Is(err, errConfirmAnswered)
}

// authorize requires the request's confirmation for scope when reasons says something is raised. With no X-Confirm it is
// confirmRequired; with one that is not good for scope, the refusal is written and errConfirmAnswered returned. An effect that no
// request caused (a queued command) has nothing to confirm with and is refused.
func authorize(ctx context.Context, scope string, reasons []string) error {
	if len(reasons) == 0 {
		return nil
	}
	shown, err := showReasons(reasons)
	if err != nil {
		return err
	}
	g, _ := ctx.Value(confirmKey{}).(*confirmGate)
	if g == nil || g.r.Header.Get(web.ConfirmHeader) == "" {
		return &confirmRequired{Scope: scope, Reasons: shown}
	}
	if g.srv.RequireConfirm(g.w, g.r, scope) {
		return nil
	}
	return errConfirmAnswered
}

// maxReason bounds one line of what a confirmation raises, in bytes.
const maxReason = 16 << 10

// showReasons renders what a confirmation raises for the person, each line whole as a question's fields are (approvals.Shown):
// controls and characters that reorder text shown as escapes, never removed. An action whose reasons cannot be shown whole (too long,
// shaped like a secret) is refused, never confirmed half seen.
func showReasons(reasons []string) ([]string, error) {
	out := make([]string, 0, len(reasons))
	for _, r := range reasons {
		v, why := approvals.Shown("setting to confirm", r, maxReason)
		if why != "" {
			return nil, werr(http.StatusBadRequest, "bad_request", "this cannot be confirmed in the page: "+why)
		}
		out = append(out, v)
	}
	return out, nil
}

// privileges are what a session's settings raise above a baseline.
type privileges struct {
	Dir    string   `json:"dir"`
	Mode   string   `json:"mode,omitempty"`
	Allow  []string `json:"allow,omitempty"`
	Verify string   `json:"verify,omitempty"`
	Trust  string   `json:"trust,omitempty"` // the digest of the project's files that would be trusted
	// Changed says how those files differ from the ones the person confirmed for the session, when it trusts them already.
	Changed string `json:"changed,omitempty"`
}

// reasons says, one line each, what is raised (none: nothing).
func (p privileges) reasons() []string {
	var out []string
	if p.Mode != "" {
		out = append(out, "permission mode "+p.Mode)
	}
	if len(p.Allow) > 0 {
		out = append(out, "allow "+strings.Join(p.Allow, ", "))
	}
	if p.Verify != "" {
		out = append(out, "run the verify command "+p.Verify)
	}
	switch {
	case p.Trust != "" && p.Changed != "":
		out = append(out, "use this project's own instructions and settings, which changed since you trusted them: "+p.Changed)
	case p.Trust != "":
		out = append(out, "use this project's own instructions and settings")
	}
	return out
}

// baseline is what a session already has: its mode, its allow rules, its verify command, whether it trusts its project, its
// directory.
type baseline struct {
	mode, verify, cwd string
	allow             map[string]bool
	trusted           bool
}

// canonRule is a rule as the engine writes it (the text as given when it does not parse).
func canonRule(action perm.Action, r string) string {
	if pr, err := perm.ParseRule(action, r); err == nil {
		return pr.String()
	}
	return strings.TrimSpace(r)
}

// baselineOfFlags is the baseline of parsed chat flags (the server's own command line, or a tab's last arguments).
func baselineOfFlags(f chatFlags, cwd string) baseline {
	b := baseline{mode: f.mode, verify: f.verify, cwd: cwd, trusted: f.trust, allow: map[string]bool{}}
	for _, r := range expandAllow(f.allow) {
		b.allow[canonRule(perm.Allow, r)] = true
	}
	return b
}

// baselineOfSession is what a running session has.
func baselineOfSession(s *session.Session) baseline {
	o := s.Options()
	b := baseline{mode: string(s.Perm.Mode()), verify: o.Verify, cwd: s.Cwd(), trusted: o.TrustProject, allow: map[string]bool{}}
	for _, r := range o.Allow {
		b.allow[canonRule(perm.Allow, r)] = true
	}
	for _, r := range s.Perm.Granted() {
		b.allow[r] = true
	}
	for _, r := range s.Perm.Rules(perm.Allow) {
		b.allow[r] = true
	}
	return b
}

// footprint is the trust state of a directory's own files: their digest when they are not trusted ("" when they are, or when the
// project has none). A footprint that could not be read whole (a link that leaves the project, more than a scan reads) is never taken
// for one with nothing in it; one that cannot be scanned at all is a footprint of its own ("unreadable").
func footprint(dir string) string {
	home, _ := os.UserHomeDir()
	fp, err := trust.Scan(rootOf(dir), dir, home)
	switch {
	case err != nil:
		return "unreadable"
	case fp.Empty() && !fp.Partial:
		return ""
	}
	if st, _ := trust.OpenLedger(session.TrustLedgerPath(home)).Check(dir, fp); st == trust.Trusted {
		return ""
	}
	return fp.Digest
}

// raised is what parsed flags raise above a baseline, in dir.
func raised(f chatFlags, base baseline, dir string) privileges {
	p := privileges{Dir: dir}
	if (f.mode == string(perm.ModeBypass) || f.mode == string(perm.ModeYolo)) && f.mode != base.mode {
		p.Mode = f.mode
	}
	for _, r := range expandAllow(f.allow) {
		if c := canonRule(perm.Allow, r); !base.allow[c] && !slices.Contains(p.Allow, c) {
			p.Allow = append(p.Allow, c)
		}
	}
	slices.Sort(p.Allow)
	if f.verify != "" && f.verify != base.verify {
		p.Verify = f.verify
	}
	if f.trust && (!base.trusted || dir != cleanDir(base.cwd)) {
		p.Trust = footprint(dir) // trust established for one directory does not follow the session into another
	}
	return p
}

// recheckTrust is the trust step of a restart whose arguments trust the project's files (f). When the session trusts them already
// in dir, they are read again and compared with those the person confirmed for this tab: when they differ, or cannot be read whole,
// p.Trust (and p.Changed, what changed) raise them again, so that the restart needs a confirmation that names the change and the new
// generation does not start trusting files nobody confirmed. It returns the footprint that a confirmation of p covers, to be recorded
// once confirmed (nil when trust is not raised, or the files cannot be read).
func (t *webTab) recheckTrust(f chatFlags, base baseline, dir string, p *privileges) *trust.Footprint {
	if !f.trust {
		return nil
	}
	home, _ := os.UserHomeDir()
	cur, err := trust.Scan(rootOf(dir), dir, home)
	switch {
	case p.Trust != "": // raised already (another directory, a session that does not trust): its confirmation covers these files
		if err != nil {
			return nil
		}
		return cur
	case !base.trusted || dir != cleanDir(base.cwd):
		return nil // nothing to trust here, or files the trust ledger holds as the person trusted them
	case err != nil:
		p.Trust = "unreadable"
		return nil
	case cur.Empty() && !cur.Partial:
		return nil // nothing that trust unlocks
	}
	t.mu.Lock()
	prev, prevDir := t.trusted, t.trustedDir
	t.mu.Unlock()
	if prev != nil && prevDir == dir && prev.Digest == cur.Digest && !cur.Partial {
		return nil // the files the person confirmed
	}
	p.Trust = cur.Digest
	if prev != nil && prevDir == dir {
		p.Changed = trust.DescribeChanges(trust.Changes(entryOf(prev), cur))
	}
	return cur
}

// entryOf is a footprint as the trust ledger records it, to name how other files differ from it.
func entryOf(fp *trust.Footprint) trust.Entry {
	files := make(map[string]string, len(fp.Files))
	for _, f := range fp.Files {
		files[f.Path] = f.Sum[:min(16, len(f.Sum))]
	}
	return trust.Entry{Digest: fp.Digest, Files: files}
}

// noteTrust records the project files that this tab's session uses as the person trusted them: the footprint they confirmed with
// this start, else the one recorded for the directory already, else (a start that trusted without a step of the page: the server's
// --trust-project, an answer the trust ledger remembers) the files as they are now. A session that does not use them clears it.
func (t *webTab) noteTrust(s *session.Session, confirmed *trust.Footprint) {
	dir := cleanDir(s.Cwd())
	t.mu.Lock()
	known := t.trusted != nil && t.trustedDir == dir
	t.mu.Unlock()
	fp := confirmed
	switch {
	case !s.Options().TrustProject:
		fp, dir = nil, ""
	case fp == nil && known:
		return
	case fp == nil:
		home, _ := os.UserHomeDir()
		f, err := trust.Scan(rootOf(dir), dir, home)
		if err != nil {
			return // nothing recorded: the next restart that trusts asks
		}
		fp = f
	}
	t.mu.Lock()
	t.trusted, t.trustedDir = fp, dir
	t.mu.Unlock()
}

// cleanDir is an absolute, clean directory.
func cleanDir(dir string) string {
	if abs, err := filepath.Abs(dir); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(dir)
}

// newSessionScope is the confirmation scope of a new session's raised privileges.
func newSessionScope(p privileges) string { return "session:" + d16(p) }

// restartScope is the confirmation scope of a restart's raised privileges.
func restartScope(tab string, p privileges) string { return fmt.Sprintf("restart:%s:%s", tab, d16(p)) }
