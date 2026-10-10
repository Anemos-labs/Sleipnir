package main

// Starting, restarting and closing the sessions of `sleipnir web`'s tabs, in process: what `sleipnir chat` does by ending its
// process and starting another (restartArgs, runAgain) is done here by closing the tab's session (an isolated team's run is finished
// first, up to five minutes) and building the next one from the same chat arguments with the same parser.

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/swarm"
	"github.com/anemos-labs/sleipnir/internal/trust"
	"github.com/anemos-labs/sleipnir/internal/web/approvals"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// startSpec says how a generation of a tab starts.
type startSpec struct {
	args        []string // chat arguments
	name        string   // a name the person gave (New session dialog)
	goalText    string   // a first goal, set once the session has started
	effort      string   // a reasoning effort, set once the session has started
	note        string   // a row shown once the session has started
	integration string   // how the previous generation's isolated run ended
	restart     bool     // a restart: the new generation says so
	first       bool     // the first tab of the server: if it cannot start, it is removed
	// trusted is the footprint of the project's files that the person confirmed with this start (the New session dialog's trust
	// step); nil when there was none.
	trusted *trust.Footprint
}

// callLog keeps the tool call each agent of a tab started last (the sink's ToolStart): a question about an edit shows the change of
// that call, which the permission request itself does not carry.
type callLog struct {
	mu   sync.Mutex
	last map[string]core.Block
}

// wrap returns a sink that records the calls it is told about and passes everything on.
func (c *callLog) wrap(s agent.Sink) agent.Sink { return recordingSink{Sink: s, log: c} }

// input is the input of the call of tool that agent started last, if that is its last call.
func (c *callLog) input(agentID, tool string) (json.RawMessage, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.last[agentID]
	if !ok || b.ToolName != tool {
		return nil, false
	}
	return b.Input, true
}

// recordingSink is a sink that records each tool call before passing it on.
type recordingSink struct {
	agent.Sink
	log *callLog
}

// ToolStart records the call and passes it on.
func (s recordingSink) ToolStart(agentID string, call core.Block) {
	s.log.mu.Lock()
	if s.log.last == nil {
		s.log.last = map[string]core.Block{}
	}
	s.log.last[agentID] = call
	s.log.mu.Unlock()
	s.Sink.ToolStart(agentID, call)
}

// Reset passes a retried request on to a sink that takes it back.
func (s recordingSink) Reset(agentID string) {
	if r, ok := s.Sink.(agent.Resetter); ok {
		r.Reset(agentID)
	}
}

// mergeOptions lays parsed chat flags over a tab's base options: what flags say comes from them, what they cannot say (an injected
// provider, a configuration, a home directory) from the base. A model other than the base's drops an injected provider.
func mergeOptions(base session.Options, f chatFlags, defaultCwd string) session.Options {
	o := f.options()
	out := base
	out.Cwd = o.Cwd
	if out.Cwd == "" {
		out.Cwd = defaultCwd
	}
	if abs, err := filepath.Abs(out.Cwd); err == nil {
		out.Cwd = abs
	}
	if base.Root != "" && out.Cwd != base.Cwd {
		out.Root = ""
	}
	if o.Model != "" && o.Model != base.Model {
		out.Provider, out.ModelInfo = nil, nil
	}
	if o.Model != "" {
		out.Model = o.Model
	}
	out.Mode, out.Swarm, out.Workers, out.TrustProject = o.Mode, o.Swarm, o.Workers, o.TrustProject || base.TrustProject
	out.BudgetUSD, out.Resume, out.NoMCP = o.BudgetUSD, o.Resume, o.NoMCP || base.NoMCP
	out.Verify, out.Isolation, out.Commit, out.Mailman = o.Verify, o.Isolation, o.Commit, o.Mailman
	out.RoleModels, out.Allow, out.Interactive = o.RoleModels, o.Allow, true
	out.ID, out.Dir = "", ""
	out.Sink, out.NewSink, out.Prompter = nil, nil, nil
	return out
}

// describeAgent gives a question the agent's task and the globs of its scope.
func describeAgent(s *session.Session, id string) (task, scope string) {
	if s == nil || s.Swarm == nil {
		return "", ""
	}
	snap := s.Swarm.Board.Snapshot()
	a, ok := snap.Agent(id)
	if !ok || a.Task == "" {
		return "", ""
	}
	if t, ok := snap.Task(a.Task); ok && len(t.Files) > 0 {
		return a.Task, strings.Join(t.Files, ", ")
	}
	return a.Task, ""
}

// prompter is the perm.Prompter of the tab: the approvals bridge, which refuses the project's own files and tool servers while the
// session is starting; a tool server refused then is said in a row. Paths in questions are shown relative to root.
func (t *webTab) prompter(root string) perm.Prompter {
	starting := func() bool {
		t.mu.Lock()
		defer t.mu.Unlock()
		return t.starting
	}
	p := t.h.bridge.Prompter(t.id, root, func(agent string) (string, string) { return describeAgent(t.session(), agent) }, starting)
	return func(ctx context.Context, r perm.Request) perm.Decision {
		d := p(ctx, r)
		if r.Tool == perm.ToolMCPServer && !d.Allow && starting() {
			t.sys("⚠", "a tool server of this project was not started: it needs your approval (Settings › MCP servers); it starts when the team starts again")
		}
		return d
	}
}

// rootOf is the project root of a directory: the nearest directory up with a .git or .sleipnir, else the directory itself.
func rootOf(dir string) string {
	if root, _ := config.FindRoot(dir); root != "" {
		return root
	}
	return dir
}

// startGen starts the tab's current generation (t.gen) from spec, on the calling goroutine: the translator first (its sink and the
// prompter exist before session.New), then the session. A start that fails leaves the tab without a session and says why in a row.
func (t *webTab) startGen(spec startSpec) error {
	started := time.Now()
	t.mu.Lock()
	gen := t.gen
	cwd := t.h.d.Cwd
	tr := newTranslator(translatorConfig{
		Tab: t.id, Gen: gen, StartedAt: started, Publish: t.h.Publish, Now: time.Now, Session: t.session,
		Verify: func() (string, bool) {
			if s := t.session(); s != nil {
				return s.Options().Verify, s.WorktreeIsolation()
			}
			return "", false
		},
	})
	t.tr, t.startedAt, t.starting, t.args, t.resumed = tr, started, true, append([]string(nil), spec.args...), nil
	t.lastRoster = nil
	sum := t.summaryLocked()
	t.mu.Unlock()
	if gen > 1 {
		t.h.Publish(wire.Frame{Type: "reset", Tab: t.id, Data: wire.ResetFrame{Tab: t.id, Gen: gen}, Critical: true})
	}
	t.h.Publish(wire.Frame{Type: "tab", Data: wire.TabFrame{Op: "update", Tab: sum}, Critical: true})
	if spec.integration != "" {
		tr.Emit(&wire.Sys{Ch: "mgr", Glyph: "◇", Text: clip(spec.integration, 2000)})
	}
	if spec.restart {
		tr.Emit(&wire.Say{Who: "sys", Glyph: "↺", Text: clip("started again: sleipnir chat "+commandLine(spec.args), 4000)})
	}
	tr.Emit(&wire.Say{Who: "sys", Glyph: "◇", Text: "starting…"})
	t.publishMeta()

	f, err := parseChatFlags(spec.args)
	var s *session.Session
	if err == nil {
		o := mergeOptions(t.base, f, cwd)
		root := o.Root
		if root == "" {
			root = rootOf(o.Cwd)
		}
		o.Sink, o.NewSink, o.Prompter = t.calls.wrap(tr.Sink()), func(id string) agent.Sink { return t.calls.wrap(tr.NewSink(id)) }, t.prompter(root)
		if o.AskTimeout == 0 {
			o.AskTimeout = t.h.askTimeout
		}
		s, err = session.New(t.ctx, o)
	}
	t.mu.Lock()
	t.starting = false
	t.cond.Broadcast()
	if err == nil && t.closed {
		t.mu.Unlock()
		s.SetEndReason(session.EndExit)
		s.Close()
		return context.Canceled
	}
	if err != nil {
		t.cond.Broadcast()
		t.mu.Unlock()
		tr.Emit(hintRow("the session did not start: "+err.Error(), err))
		t.publishMeta()
		t.h.Publish(wire.Frame{Type: "tab", Data: wire.TabFrame{Op: "update", Tab: t.Summary()}, Critical: true})
		return err
	}
	t.s = s
	t.mu.Unlock()
	detach := tr.Attach(s.Log, s.Dir)
	t.mu.Lock()
	t.detach = detach
	t.mu.Unlock()
	t.afterStart(s, spec)
	t.mu.Lock()
	t.cond.Broadcast()
	t.mu.Unlock()
	t.kick()
	return nil
}

// afterStart is what follows a start: the name, the goal the log kept, the resumed session's line, the effort and first goal of
// the New session dialog, and the tab's frames.
func (t *webTab) afterStart(s *session.Session, spec startSpec) {
	t.noteTrust(s, spec.trusted)
	name := strings.TrimSpace(spec.name)
	if name != "" {
		_ = session.UpdateMeta(s.Dir, func(m *session.Meta) { m.Name = name })
	} else if m, err := session.LoadMeta(s.Dir); err == nil && m.Name != "" && s.Resumed() {
		name = m.Name
	}
	t.mu.Lock()
	if name != "" && validName(name) == nil {
		t.name = name
	}
	t.mu.Unlock()
	t.goals.Load(s)
	if g := t.goals.State(); g != nil {
		t.emit(goalWire(g))
	}
	if a := s.Main(); s.Resumed() && a != nil {
		t.note("↺", resumedLine(s, a))
		sum := session.Summarize(eventsPath(s.Dir))
		rec := wire.RecordedSession{ID: s.ID, First: clip(sum.First, 300), Model: sum.Model, Cost: sum.CostUSD, Cwd: sum.Cwd, Resumable: true}
		if !sum.Started.IsZero() && !sum.Last.IsZero() {
			rec.Dur = sum.Last.Sub(sum.Started).Seconds()
			rec.LastWritten = sum.Last.UnixMilli()
		}
		t.mu.Lock()
		t.resumed = &rec
		t.mu.Unlock()
	}
	if spec.effort != "" && spec.effort != "default" {
		s.SetEffort(spec.effort)
	}
	if spec.note != "" {
		t.sys("◇", spec.note)
	}
	t.note("◇", "ready: "+s.Model.ID+" · "+modeName(s)+" · session "+s.ID)
	t.h.Publish(wire.Frame{Type: "tab", Data: wire.TabFrame{Op: "update", Tab: t.Summary()}, Critical: true})
	t.publishMeta()
	t.publishRoster()
	if g := strings.TrimSpace(spec.goalText); g != "" {
		_ = t.goalAction(t.ctx, s, wire.GoalRequest{Action: "set", Text: g}, true)
	}
}

// restartTyped is the restart request as the flags a person would type after /restart, and whether the chat starts empty: /new and
// /clear always do; /swarm, Run settings Apply and "run it again" carry the conversation unless the request says fresh.
func restartTyped(req wire.RestartRequest) (typed []string, fresh bool, err error) {
	for _, f := range req.Flags {
		if len(f) > 4000 {
			return nil, false, werr(http.StatusBadRequest, "bad_flags", "a flag is too long")
		}
	}
	switch req.Kind {
	case "new", "clear":
		return nil, true, nil
	case "swarm":
		if req.Swarm == nil || *req.Swarm < 0 {
			return nil, false, werr(http.StatusBadRequest, "bad_flags", "a team restart names its number of workers (0 is a single agent)")
		}
		return append([]string{"--swarm", strconv.Itoa(*req.Swarm)}, req.Flags...), req.Fresh, nil
	case "restart":
		return append([]string(nil), req.Flags...), req.Fresh, nil
	case "model":
		if strings.TrimSpace(req.Model) == "" {
			return nil, false, werr(http.StatusBadRequest, "bad_flags", "a model restart names the model")
		}
		return append([]string{"--model", req.Model}, req.Flags...), req.Fresh, nil
	case "roles":
		if len(req.RoleModels) == 0 {
			return nil, false, werr(http.StatusBadRequest, "bad_flags", "a roles restart names a role and its model")
		}
		roles := make([]string, 0, len(req.RoleModels))
		for r := range req.RoleModels {
			roles = append(roles, r)
		}
		slices.Sort(roles)
		for _, r := range roles {
			if r == "" || req.RoleModels[r] == "" {
				return nil, false, werr(http.StatusBadRequest, "bad_flags", "a role and its model are both needed")
			}
			typed = append(typed, "--role-model", r+"="+req.RoleModels[r])
		}
		return append(typed, req.Flags...), req.Fresh, nil
	}
	return nil, false, werr(http.StatusBadRequest, "bad_flags", "the kind is new, clear, swarm, restart, model or roles")
}

// Restart is the restart family (/new, /clear, /swarm, /restart, a team's model or role change): it answers at once and the tab
// starts its next generation in the background.
func (t *webTab) Restart(ctx context.Context, req wire.RestartRequest) error {
	typed, fresh, err := restartTyped(req)
	if err != nil {
		return err
	}
	return t.restart(ctx, req.Kind, typed, fresh)
}

// typedFlag returns the value of a flag in a typed argument list ("" when it is not there).
func typedFlag(typed []string, name string) string {
	for i, a := range typed {
		if a == name && i+1 < len(typed) {
			return typed[i+1]
		}
		if v, ok := strings.CutPrefix(a, name+"="); ok {
			return v
		}
	}
	return ""
}

// restart checks and authorizes the arguments the restart would start with (the staged launch flags included), then ends the current
// generation and starts the next in the background.
func (t *webTab) restart(ctx context.Context, kind string, typed []string, fresh bool) error {
	typed = append(t.stagedFlags(), typed...) // what the line says wins over what Run settings staged
	t.mu.Lock()
	s, closed, busy, prev := t.s, t.closed, t.restarting || t.starting, append([]string(nil), t.args...)
	t.mu.Unlock()
	switch {
	case closed:
		return werr(http.StatusNotFound, "not_found", "this session was closed")
	case busy:
		return werr(http.StatusConflict, "busy", "the session is starting: try again when it has started")
	}
	var args []string
	var err error
	if s != nil {
		args, err = restartArgs(s, typed, fresh)
	} else {
		args = append(launchArgs(prev, nil), typed...)
	}
	if err != nil {
		return werr(http.StatusBadRequest, "bad_flags", clip(err.Error(), 400))
	}
	// The session stays in its directory unless the line names another (restartArgs does not carry --cwd).
	var base baseline
	if s != nil {
		base = baselineOfSession(s)
		if typedFlag(typed, "--cwd") == "" {
			args = append(args, "--cwd", s.Cwd())
		}
	} else {
		pf, _ := parseChatFlags(launchArgs(prev, nil))
		base = baselineOfFlags(pf, cleanDir(firstNonEmpty(pf.cwd, t.h.d.Cwd)))
	}
	f, err := parseChatFlags(args)
	if err != nil {
		return werr(http.StatusBadRequest, "bad_flags", clip(err.Error(), 400))
	}
	if ref := typedFlag(typed, "--model"); ref != "" && s != nil {
		if _, err := s.CheckModel(ref); err != nil {
			return werr(http.StatusUnprocessableEntity, "model", clip(err.Error(), 400))
		}
	}
	// Authorized here, on the arguments the next generation starts with: another directory must be a project of the server, and what
	// they raise above this session needs the request's confirmation (web_authorize.go).
	dir := cleanDir(firstNonEmpty(f.cwd, base.cwd))
	if dir != cleanDir(base.cwd) {
		if _, ok := t.h.isProject(ctx, dir); !ok {
			return werr(http.StatusForbidden, "not_a_project", "start a session in one of the listed projects")
		}
	}
	p := raised(f, base, dir)
	confirmed := t.recheckTrust(f, base, dir, &p)
	if err := authorize(ctx, restartScope(t.id, p), p.reasons()); err != nil {
		return err
	}
	t.mu.Lock()
	if t.restarting || t.starting || t.closed {
		t.mu.Unlock()
		return werr(http.StatusConflict, "busy", "the session is starting: try again when it has started")
	}
	if confirmed != nil {
		t.trusted, t.trustedDir = confirmed, dir // the files this confirmation covered
	}
	t.restarting = true
	t.gen++
	t.staged, t.teamBudget = wire.LaunchPatch{}, nil
	t.queue = nil
	t.mu.Unlock()
	webAction(s, "restart", kind+" "+commandLine(args))
	t.h.track(func() { t.doRestart(s, args) })
	return nil
}

// doRestart ends the current generation and starts the next.
func (t *webTab) doRestart(old *session.Session, args []string) {
	t.sys("↺", "restarting: sleipnir chat "+commandLine(args))
	if old != nil && old.WorktreeIsolation() {
		t.sys("◇", "closing the run first: its verified work is applied to your checkout (this can take a few minutes)")
	}
	t.mu.Lock()
	if t.cancel != nil {
		t.cancel()
	}
	if t.cancelOp != nil {
		t.cancelOp()
	}
	t.mu.Unlock()
	t.h.bridge.CancelTab(t.id, approvals.ByCanceled)
	t.mu.Lock()
	for t.active || t.excl > 0 {
		t.cond.Wait()
	}
	oldTr, detach := t.tr, t.detach
	t.s, t.detach = nil, nil
	t.mu.Unlock()
	rep := closeSession(old, session.EndExit)
	if detach != nil {
		detach()
	}
	if oldTr != nil {
		oldTr.Close()
	}
	_ = t.startGen(startSpec{args: args, integration: integrationText(rep), restart: true})
	t.mu.Lock()
	t.restarting = false
	t.cond.Broadcast()
	t.mu.Unlock()
	t.kick()
	t.publishMeta()
}

// closeSession ends a session as the chat does: an isolated team's verified work is applied first (bounded), then the session is
// closed. It returns the report of an isolated run (nil for any other).
func closeSession(s *session.Session, reason string) *swarm.IntegrationReport {
	if s == nil {
		return nil
	}
	s.SetEndReason(reason)
	rep := finishRun(context.Background(), s)
	s.Close()
	return rep
}

// shutdown ends the tab: what runs is cancelled, its questions are refused, its turn goroutine stops, and its session is closed (an
// isolated run is finished first). It returns how an isolated run ended.
func (t *webTab) shutdown(by string) *swarm.IntegrationReport {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	cancel, cancelOp := t.cancel, t.cancelOp
	t.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if cancelOp != nil {
		cancelOp()
	}
	t.h.bridge.CancelTab(t.id, by)
	t.stop()
	<-t.done
	t.mu.Lock()
	for t.excl > 0 || t.restarting || t.starting {
		t.cond.Wait()
	}
	s, tr, detach := t.s, t.tr, t.detach
	t.s, t.detach = nil, nil
	t.mu.Unlock()
	rep := closeSession(s, session.EndExit)
	if detach != nil {
		detach()
	}
	if tr != nil {
		tr.Close()
	}
	return rep
}

// chatArgsOf are the chat arguments of the server's defaults (sleipnir web's own flags): the first tab's, and the base of the
// defaults the New session dialog shows.
func chatArgsOf(d webDefaults, resume string) []string {
	a := []string{"--cwd", d.Cwd}
	if d.Model != "" {
		a = append(a, "--model", d.Model)
	}
	if d.Mode != "" {
		a = append(a, "--mode", d.Mode)
	}
	workers := d.Workers
	if resume != "" && !d.WorkersGiven {
		home, _ := os.UserHomeDir()
		root, _ := filepath.Abs(d.Cwd)
		if team, known := session.ResumedAsTeam(home, rootOf(root), resume); known && !team {
			workers = 0
		}
	}
	a = append(a, "--swarm", strconv.Itoa(workers))
	if d.BudgetUSD > 0 {
		a = append(a, "--budget-usd", strconv.FormatFloat(d.BudgetUSD, 'f', -1, 64))
	}
	if d.Isolation != "" {
		a = append(a, "--isolation", d.Isolation)
	}
	if d.Verify != "" {
		a = append(a, "--verify", d.Verify)
	}
	if d.Commit {
		a = append(a, "--commit")
	}
	if d.Mailman != nil {
		a = append(a, "--mailman="+strconv.FormatBool(*d.Mailman))
	}
	roles := make([]string, 0, len(d.RoleModels))
	for r := range d.RoleModels {
		roles = append(roles, r)
	}
	slices.Sort(roles)
	for _, r := range roles {
		a = append(a, "--role-model", r+"="+d.RoleModels[r])
	}
	for _, r := range d.Allow {
		a = append(a, "--allow", r)
	}
	if d.TrustProject {
		a = append(a, "--trust-project")
	}
	if d.NoMCP {
		a = append(a, "--no-mcp")
	}
	if resume != "" {
		a = append(a, "--resume", resume)
	}
	return a
}

// sessionTime is the session time of a moment in a tab, in seconds (0 before its start).
func (t *webTab) sessionTime(at time.Time) float64 {
	t.mu.Lock()
	start := t.startedAt
	t.mu.Unlock()
	if start.IsZero() || at.Before(start) {
		return 0
	}
	return float64(at.Sub(start).Milliseconds()) / 1000
}
