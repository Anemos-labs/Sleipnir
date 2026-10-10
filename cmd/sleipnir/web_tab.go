package main

// A tab of `sleipnir web`: one hosted harness session, its translator and journal, and the person's actions on it. The session of a
// tab changes on a restart (a new generation, with a new translator, a new journal and possibly a new session id); the tab's id does
// not. Turns, queued lines and the commands that change the session run one at a time on the tab's turn goroutine (loop), the way
// the terminal chat runs them; what only looks (the "look" commands, the mode, the rules, a steer, an interrupt) runs beside a turn.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/goal"
	"github.com/anemos-labs/sleipnir/internal/mcp"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/swarm"
	"github.com/anemos-labs/sleipnir/internal/trust"
	"github.com/anemos-labs/sleipnir/internal/web/approvals"
	"github.com/anemos-labs/sleipnir/internal/web/seam"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// Limits of the person's input: the length of a name, a rule, a goal and a steering message, and the lines of input history a tab keeps.
const (
	maxNameLen  = 60
	maxRuleLen  = 500
	maxGoalLen  = 4000
	maxSteerLen = 4000
	maxHist     = 200
	// closeLimit bounds the end of an isolated run when a tab closes or restarts (Session.Finish's own limit).
	closeLimit = 5 * time.Minute
)

// werr is a refusal as the routes send it.
func werr(status int, code, msg string) *wire.Error {
	return &wire.Error{Status: status, Code: code, Msg: msg}
}

// queued is a line typed while something runs, or an action that waits for its turn: a message (prompt, display), a slash
// command (line), or a host action (op).
type queued struct {
	id      string
	text    string // what meta.queued shows
	prompt  string // a message: what reaches the agent
	display string // a message: what the transcript shows
	echo    bool   // a message: echo display as the person's row
	line    string // a slash command
	op      func(ctx context.Context, s *session.Session)
}

// webTab is a tab: see the top of this file. Its methods are safe for concurrent use.
type webTab struct {
	h         *webHostImpl
	id        string
	createdAt int64
	order     int
	base      session.Options // what a restart keeps that flags cannot say (an injected provider of the fixture backend)

	mu         sync.Mutex
	cond       *sync.Cond // signalled when an item ends, an exclusive section ends or a restart ends
	name       string
	gen        uint64
	s          *session.Session
	tr         seam.Translator
	detach     func()
	startedAt  time.Time
	starting   bool
	restarting bool
	closed     bool
	args       []string // the chat arguments of the current generation
	staged     wire.LaunchPatch
	teamBudget *float64 // a budget set for a team, applied when it starts again
	queue      []queued
	nq         int
	active     bool
	excl       int
	cancel     context.CancelFunc // cancels the running item
	cancelOp   context.CancelFunc // cancels a running compaction
	hist       []string
	origins    map[string]string // "allow Bash(x)" -> where a session rule came from
	resumed    *wire.RecordedSession
	trusted    *trust.Footprint // the project files the person confirmed for trustedDir, which the session uses (nil: none)
	trustedDir string
	lastMeta   wire.MetaPatch
	lastRoster []wire.RosterEntry
	shownQueue int

	sessMu sync.Mutex // calls into the session that are not safe beside each other (the budget, the model)
	calls  callLog    // the tool call each agent started last
	goals  goalLoop
	wake   chan struct{}
	ctx    context.Context
	stop   context.CancelFunc
	done   chan struct{}
}

// newTab makes a tab and starts its turn goroutine; the caller starts its first generation.
func newTab(h *webHostImpl, id, name string, base session.Options) *webTab {
	t := &webTab{h: h, id: id, name: name, base: base, createdAt: time.Now().UnixMilli(), origins: map[string]string{},
		wake: make(chan struct{}, 1), done: make(chan struct{})}
	t.cond = sync.NewCond(&t.mu)
	t.ctx, t.stop = context.WithCancel(context.WithoutCancel(h.ctx))
	t.goals.yield = func() bool {
		t.mu.Lock()
		defer t.mu.Unlock()
		return len(t.queue) > 0
	}
	go t.loop()
	return t
}

// ---- reading -----------------------------------------------------------------------------------------------------------------

// Summary describes the tab.
func (t *webTab) Summary() wire.TabSummary {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.summaryLocked()
}

// summaryLocked is Summary under the lock.
func (t *webTab) summaryLocked() wire.TabSummary {
	sum := wire.TabSummary{ID: t.id, Name: t.name, Gen: t.gen, CreatedAt: t.createdAt, Order: t.order, Cwd: t.cwdLocked(), Headless: t.base.AskTimeout > 0}
	if t.s != nil {
		sum.SID = t.s.ID
	}
	return sum
}

// cwdLocked is the directory the tab works in.
func (t *webTab) cwdLocked() string {
	if t.s != nil {
		return t.s.Cwd()
	}
	if f, err := parseChatFlags(t.args); err == nil && f.cwd != "" {
		return f.cwd
	}
	return t.h.d.Cwd
}

// session returns the current harness session (nil while the tab starts or restarts).
func (t *webTab) session() *session.Session {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.s
}

// translator returns the current generation's translator.
func (t *webTab) translator() seam.Translator {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.tr
}

// emit adds host-originated events to the current generation's journal.
func (t *webTab) emit(evs ...wire.Event) {
	if tr := t.translator(); tr != nil {
		tr.Emit(evs...)
	}
}

// sys emits a sys row of the manager's channel.
func (t *webTab) sys(glyph, text string) {
	t.emit(&wire.Sys{Ch: "mgr", Glyph: glyph, Text: clip(text, 2000)})
}

// note emits a host status line in the transcript.
func (t *webTab) note(glyph, text string) {
	t.emit(&wire.Say{Who: "sys", Glyph: glyph, Text: clip(text, 16<<10)})
}

// Snapshot returns the tab's state for a late joiner.
func (t *webTab) Snapshot(ctx context.Context) (wire.TabSnapshot, error) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return wire.TabSnapshot{}, werr(http.StatusNotFound, "not_found", "no such session")
	}
	tr, sum := t.tr, t.summaryLocked()
	hist := append([]string{}, t.hist...)
	t.mu.Unlock()
	meta := t.meta()
	snap := wire.TabSnapshot{Tab: sum, Gen: sum.Gen, Meta: meta, Hist: hist, Keyframe: []wire.Raw{}, Events: []wire.Raw{}, Roster: []wire.RosterEntry{}}
	if tr != nil {
		kf, evs, seq, now := tr.Journal()
		snap.Keyframe, snap.Events, snap.Seq, snap.Now = kf, evs, seq, now
		if r := tr.Roster(); r != nil {
			snap.Roster = r
		}
	}
	if snap.Keyframe == nil {
		snap.Keyframe = []wire.Raw{}
	}
	if snap.Events == nil {
		snap.Events = []wire.Raw{}
	}
	snap.Questions = t.h.bridge.OpenFor(t.id)
	return snap, nil
}

// Access gives the workspace and settings routes the current harness session of the tab.
func (t *webTab) Access() seam.SessionAccess { return tabAccess{t} }

// ---- the turn goroutine ----------------------------------------------------------------------------------------------------

// kick wakes the turn goroutine.
func (t *webTab) kick() {
	select {
	case t.wake <- struct{}{}:
	default:
	}
}

// idleLocked says whether nothing runs and nothing waits: a message sent now starts at once.
func (t *webTab) idleLocked() bool {
	return !t.active && t.excl == 0 && len(t.queue) == 0 && t.s != nil && !t.restarting && !t.starting
}

// loop runs the queued items, one at a time, until the tab closes.
func (t *webTab) loop() {
	defer close(t.done)
	for {
		it, ctx, ok := t.next()
		if !ok {
			return
		}
		t.run(ctx, it)
		t.mu.Lock()
		t.active, t.cancel = false, nil
		t.cond.Broadcast()
		t.mu.Unlock()
		t.publishMeta()
	}
}

// next waits for an item that may run now and marks it running.
func (t *webTab) next() (queued, context.Context, bool) {
	for {
		t.mu.Lock()
		if t.closed {
			t.mu.Unlock()
			return queued{}, nil, false
		}
		if !t.active && t.excl == 0 && t.s != nil && !t.restarting && !t.starting && len(t.queue) > 0 {
			it := t.queue[0]
			t.queue = t.queue[1:]
			ctx, cancel := context.WithCancel(t.ctx)
			t.active, t.cancel = true, cancel
			t.mu.Unlock()
			t.publishMeta()
			return it, ctx, true
		}
		t.mu.Unlock()
		select {
		case <-t.wake:
		case <-t.ctx.Done():
			return queued{}, nil, false
		}
	}
}

// enqueue adds an item behind what waits and reports its id, its place (1 is next) and whether it starts at once.
func (t *webTab) enqueue(it queued) (id string, pos int, now bool, err error) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return "", 0, false, werr(http.StatusNotFound, "not_found", "this session was closed")
	}
	now = t.idleLocked()
	t.nq++
	if it.id == "" {
		it.id = "q" + strconv.Itoa(t.nq)
	}
	t.queue = append(t.queue, it)
	id, pos = it.id, len(t.queue)
	t.mu.Unlock()
	t.kick()
	if !now {
		t.publishMeta()
	}
	return id, pos, now, nil
}

// enqueueFront puts an item before everything that waits (/goal pause and clear go first, as in the terminal chat).
func (t *webTab) enqueueFront(it queued) {
	t.mu.Lock()
	t.nq++
	it.id = "q" + strconv.Itoa(t.nq)
	t.queue = append([]queued{it}, t.queue...)
	t.mu.Unlock()
	t.kick()
}

// run runs one item on the turn goroutine.
func (t *webTab) run(ctx context.Context, it queued) {
	s := t.session()
	if s == nil {
		return
	}
	defer func() {
		if p := recover(); p != nil {
			t.sys("⚠", fmt.Sprintf("the host stopped an action that crashed: %v", p))
		}
	}()
	switch {
	case it.op != nil:
		it.op(ctx, s)
	case it.line != "":
		res, err := t.runCommand(ctx, s, it.line)
		if err != nil {
			res.Output = err.Error()
		}
		if res.Output != "" {
			t.note("◇", strings.TrimRight(res.Output, "\n"))
		}
	default:
		t.turn(ctx, s, it.prompt, it.display, it.echo)
	}
}

// turn runs a turn of the agent the person talks to, with the goal's continuations, and reports it as the page expects: the
// person's row, turn start, the run's events, the goal's verdicts, final and turn end.
func (t *webTab) turn(ctx context.Context, s *session.Session, prompt, display string, echo bool) {
	if echo {
		t.emit(&wire.Say{Who: "you", Text: clip(display, 16<<10)})
		t.addHist(display)
	}
	t.emit(&wire.Turn{S: "start"})
	t.publishMeta()
	err := t.goals.Turn(ctx, s, prompt, func(e goalEvent) { t.goalEvent(s, e) })
	_ = err
	t.emit(&wire.Final{}, &wire.Turn{S: "end"})
	t.publishRoster()
}

// addHist records a sent line for the history (ctrl+r, up arrow).
func (t *webTab) addHist(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	t.mu.Lock()
	t.hist = append(t.hist, clip(line, 4000))
	if len(t.hist) > maxHist {
		t.hist = append([]string(nil), t.hist[len(t.hist)-maxHist:]...)
	}
	t.mu.Unlock()
}

// goalEvent turns what the goal loop reports into the page's events.
func (t *webTab) goalEvent(s *session.Session, e goalEvent) {
	switch e.Kind {
	case "ran":
		t.runEnded(s, e.Result, e.Err)
	case "judging":
		t.emit(&wire.Verdict{Kind: "checking", Text: "checking: the judge reads the evidence of this turn"})
	case "judged":
		j := e.Judgment
		switch {
		case j.Judged:
			reason := clip(j.Verdict.Reason, 300)
			text := "not yet: " + reason
			switch j.Verdict.Kind {
			case goal.Done:
				text = "met: " + reason
			case goal.Blocked:
				text = "blocked: " + reason
			}
			left := make([]string, 0, len(j.Verdict.Left))
			for _, l := range j.Verdict.Left {
				left = append(left, clip(l, 160))
			}
			t.emit(&wire.Verdict{Kind: j.Verdict.Kind, Text: text, Left: left})
		case j.Err != nil:
			t.emit(&wire.Verdict{Kind: goal.Continue, Text: "not yet: the judge could not be asked (" + clip(j.Err.Error(), 100) + ")"})
		}
		if e.Note != "" {
			t.note("◇", e.Note)
		}
	case "state":
		t.emit(goalWire(e.Goal))
		t.publishMeta()
	}
}

// runEnded reports how a run of the session ended when it did not end well.
func (t *webTab) runEnded(s *session.Session, res *session.Result, err error) {
	switch {
	case err == nil, errors.Is(err, context.Canceled):
	case errors.Is(err, agent.ErrBudget):
		t.sys("⚠", "budget reached: "+budgetStopped(s, res).Error())
	default:
		msg := "error: " + err.Error()
		if hint := providerHint(err, "Settings › Providers & login"); hint != "" {
			msg += "\n" + hint
		}
		t.emit(hintRow(msg, err))
	}
}

// hintRow is a warning row that, when it is about a provider's key or a model, names the Settings page that sets it ("providers" or
// "models", the page's "open" field: a button that opens it).
func hintRow(text string, err error) *wire.Sys {
	return &wire.Sys{Ch: "mgr", Glyph: "⚠", Text: clip(text, 2000), Open: settingsFor(err)}
}

// settingsFor is the Settings page that fixes an error of a provider or of the model ("" for any other error).
func settingsFor(err error) string {
	if err == nil {
		return ""
	}
	if pe, ok := provider.AsError(err); ok {
		switch {
		case pe.Kind == provider.ErrAuth:
			return "providers"
		case pe.Kind == provider.ErrBadRequest && pe.Status == http.StatusNotFound:
			return "models"
		}
	}
	msg := err.Error()
	switch {
	case errors.Is(err, session.ErrNoModel):
		return "models"
	case strings.Contains(msg, "has no key") || strings.Contains(msg, "is not signed in") || strings.Contains(msg, "unknown provider"):
		return "providers"
	}
	return ""
}

// goalWire is the page's goal event of a goal state (nil: cleared).
func goalWire(g *goal.State) *wire.Goal {
	if g == nil {
		return &wire.Goal{S: "cleared"}
	}
	ev := &wire.Goal{S: "active", Objective: clip(g.Objective, maxGoalLen), Turns: g.Turns, Max: g.Max, Paused: clip(g.Paused, 300), Reason: clip(g.Reason, 300)}
	switch {
	case g.Done:
		ev.S = "met"
	case g.Paused != "":
		ev.S = "paused"
	}
	return ev
}

// ---- chat ------------------------------------------------------------------------------------------------------------------

// Send delivers a message now, or queues it behind the running turn.
func (t *webTab) Send(ctx context.Context, req wire.MessageRequest) (wire.SendResult, error) {
	if strings.TrimSpace(req.Text) == "" {
		return wire.SendResult{}, werr(http.StatusBadRequest, "empty", "the message is empty")
	}
	display := req.Display
	if strings.TrimSpace(display) == "" {
		display = req.Text
	}
	it := queued{text: oneLineCLI(display, 200), prompt: req.Text, display: display, echo: true}
	id, pos, now, err := t.enqueue(it)
	if err != nil {
		return wire.SendResult{}, err
	}
	if now {
		return wire.SendResult{ID: id}, nil
	}
	return wire.SendResult{Queued: true, Position: pos, ID: id}, nil
}

// isLookLine reports whether a slash line answers at once, beside a running turn (internal/tui/app/chat.go isLookCommand); everything
// else waits for the turn.
func isLookLine(line string) bool {
	f := strings.Fields(line)
	if len(f) == 0 {
		return false
	}
	switch f[0] {
	case "/cost", "/stats", "/context", "/agents", "/help", "/?", "/skills", "/recon", "/status", "/permissions", "/trust", "/allow", "/steer", "/effort":
		return true
	case "/mode", "/mcp":
		return len(f) == 1
	}
	return false
}

// Command runs a slash line the page has no handler for: a look command at once, anything else when nothing runs (or queued behind
// the running turn, its output then shown as a row).
func (t *webTab) Command(ctx context.Context, req wire.CommandRequest) (wire.CommandResult, error) {
	line := strings.TrimSpace(req.Line)
	if !strings.HasPrefix(line, "/") || len(line) < 2 {
		return wire.CommandResult{}, werr(http.StatusBadRequest, "empty", "a command starts with /")
	}
	if len(line) > 64<<10 {
		return wire.CommandResult{}, werr(http.StatusBadRequest, "empty", "the command is too long")
	}
	s := t.session()
	if s == nil {
		return wire.CommandResult{}, werr(http.StatusConflict, "busy", "the session is starting: try again in a moment")
	}
	name := strings.Fields(line)[0]
	if !knownCommand(s, name) {
		return wire.CommandResult{}, werr(http.StatusNotFound, "unknown_command", "unknown command "+clip(name, 60)+": "+didYouMean(name)+"/ lists them")
	}
	if isLookLine(line) || isImmediateLine(line) {
		return t.runCommand(ctx, s, line)
	}
	var res wire.CommandResult
	err := t.exclusive(ctx, func(s *session.Session) error {
		var rerr error
		res, rerr = t.runCommand(ctx, s, line)
		return rerr
	})
	if errors.Is(err, errTabBusy) {
		_, pos, _, qerr := t.enqueue(queued{text: oneLineCLI(line, 200), line: line})
		if qerr != nil {
			return wire.CommandResult{}, qerr
		}
		return wire.CommandResult{Output: fmt.Sprintf("queued (%d): it runs when the turn is over", pos)}, nil
	}
	return res, err
}

// isImmediateLine reports whether a slash line acts at once, beside a running turn, instead of waiting for it: a mode it names (as the
// mode menu does) and the restart family (which ends the running turn, as Run settings do). These are the lines that can raise
// privilege, so they are confirmed by the request that sent them and never run later on its behalf.
func isImmediateLine(line string) bool {
	f := strings.Fields(line)
	if len(f) == 0 {
		return false
	}
	switch f[0] {
	case "/restart", "/swarm", "/new", "/clear", "/resume":
		return true
	case "/mode":
		return len(f) > 1
	}
	return false
}

// knownCommand says whether a slash command exists in the tab: the chat's own, the custom commands, the skills and the prompts of the
// tool servers.
func knownCommand(s *session.Session, name string) bool {
	n := strings.TrimPrefix(name, "/")
	if n == "?" || n == "quit" {
		return true
	}
	for _, c := range chatCommands {
		if c.name == n {
			return true
		}
	}
	if reg := s.Commands(); reg != nil {
		if _, ok := reg.Get(n); ok {
			return true
		}
	}
	if s.Skills != nil {
		if _, ok := s.Skills.Get(n); ok {
			return true
		}
	}
	for _, p := range s.MCPPrompts() {
		if p.Command == name {
			return true
		}
	}
	return false
}

// webBuffer collects what a command writes while it may run beside a turn (the terminal chat's lockedBuffer).
type webBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

// Write appends p.
func (l *webBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.b.Len() > 256<<10 {
		return len(p), nil
	}
	return l.b.Write(p)
}

// String is what was written.
func (l *webBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// runCommand runs a slash command against the session and returns what it said: the host's own commands (the goal, the restart
// family, the terminal's display settings) and then slashTo. A command that expands to a prompt (a custom command, a skill, a tool
// server's prompt, /plan with a prompt) sends it as a message. The error is only a refusal for want of a confirmation (what the
// command would raise is authorized as the dedicated routes authorize it) or by policy (403); any other failure is the command's
// output.
func (t *webTab) runCommand(ctx context.Context, s *session.Session, line string) (wire.CommandResult, error) {
	f := strings.Fields(line)
	var out webBuffer
	res := wire.CommandResult{Title: f[0]}
	arg := strings.TrimSpace(strings.TrimPrefix(line, f[0]))
	failed := func(err error, prefix string) (wire.CommandResult, error) {
		var we *wire.Error
		if isAuthErr(err) || errors.As(err, &we) && we.Status == http.StatusForbidden {
			return res, err // a refusal by policy is the request's answer, as on the dedicated routes
		}
		res.Output = prefix + err.Error()
		return res, nil
	}
	switch f[0] {
	case "/goal":
		act := strings.ToLower(arg)
		req := wire.GoalRequest{Action: act}
		switch act {
		case "":
			goalCommandStatus(s, t.goals.State(), &out)
			res.Output = out.String()
			return res, nil
		case "pause", "resume", "clear":
		default:
			req = wire.GoalRequest{Action: "set", Text: arg}
		}
		if err := t.goalAction(ctx, s, req, false); err != nil {
			return failed(err, "")
		}
		return res, nil
	case "/new", "/clear":
		if err := t.Restart(ctx, wire.RestartRequest{Kind: "new", Fresh: true}); err != nil {
			return failed(err, "")
		}
		return res, nil
	case "/resume":
		flags := []string{"--continue"}
		if arg != "" {
			flags = []string{"--resume", f[1]}
		}
		if err := s.CheckResume(arg); err != nil {
			res.Output = "resume: " + clip(err.Error(), 400)
			return res, nil
		}
		if err := t.restart(ctx, "resume", flags, true); err != nil {
			return failed(err, "")
		}
		return res, nil
	case "/restart", "/swarm":
		rest := splitArgs(arg)
		if f[0] == "/swarm" {
			if len(rest) == 0 {
				res.Output = "usage: /swarm <workers> [flags]: start again as a manager and up to that many workers, e.g. /swarm 8 --verify \"go test {dirs}\" --isolation worktree"
				return res, nil
			}
			n, err := strconv.Atoi(rest[0])
			if err != nil || n < 0 {
				res.Output = "/swarm: the number of workers is a number, got " + clip(rest[0], 40)
				return res, nil
			}
			if err := t.Restart(ctx, wire.RestartRequest{Kind: "swarm", Swarm: &n, Flags: rest[1:]}); err != nil {
				return failed(err, "")
			}
			return res, nil
		}
		if err := t.Restart(ctx, wire.RestartRequest{Kind: "restart", Flags: rest}); err != nil {
			return failed(err, "")
		}
		return res, nil
	case "/roles":
		if len(f) > 1 {
			rm := map[string]string{}
			for _, kv := range f[1:] {
				role, ref, ok := strings.Cut(kv, "=")
				if !ok || role == "" || ref == "" {
					res.Output = "usage: /roles [role=provider/model ...]"
					return res, nil
				}
				rm[role] = ref
			}
			for role, ref := range rm {
				if err := t.SetModel(ctx, wire.ModelRequest{Ref: ref, Role: role}); err != nil {
					return failed(err, "")
				}
			}
			return res, nil
		}
		fmt.Fprintln(&out, "who runs on which model:")
		for _, r := range s.RoleModels() {
			fmt.Fprintf(&out, "  %-10s %-44s %s\n", r.Role, r.Model, r.From)
		}
		res.Output = out.String()
		return res, nil
	case "/model":
		if len(f) > 1 {
			if err := t.SetModel(ctx, wire.ModelRequest{Ref: f[1]}); err != nil {
				return failed(err, "")
			}
			return res, nil
		}
	case "/verbose", "/anim":
		res.Output = f[0] + " is a setting of this page: Settings › Appearance & motion"
		return res, nil
	case "/login":
		res.Output = "sign in from Settings › Providers & login, or run `sleipnir login` in a terminal"
		return res, nil
	case "/exit", "/quit":
		res.Output = "close this session with × on its tab, or from the session menu"
		return res, nil
	case "/mode":
		if len(f) > 1 {
			if err := t.SetMode(ctx, wire.ModeRequest{Mode: f[1]}); err != nil {
				return failed(err, "")
			}
			return res, nil
		}
	case "/budget":
		if len(f) > 1 {
			usd, err := parseBudget(f[1])
			if err == nil {
				_, err = t.setBudget(ctx, usd)
			}
			if err != nil {
				return failed(err, "budget: ")
			}
			return res, nil
		}
	case "/effort":
		if len(f) > 1 {
			if _, err := t.SetEffort(ctx, wire.EffortRequest{Level: f[1]}); err != nil {
				return failed(err, "")
			}
			return res, nil
		}
	case "/allow":
		if len(f) > 1 {
			// one confirmation for the whole line: the rules it allows are authorized together
			if _, err := t.addRules(ctx, "allow", f[1:], "/allow"); err != nil {
				return failed(err, "")
			}
			return res, nil
		}
	case "/steer":
		if err := t.Steer(ctx, arg); err != nil {
			return failed(err, "")
		}
		return res, nil
	case "/rewind":
		if len(f) > 1 {
			// a restore puts files back as they were: the Workspace's restore route asks for the same confirmation
			if err := authorize(ctx, "restore:"+t.id+":"+f[1], []string{"restore the files of checkpoint " + clip(f[1], 40)}); err != nil {
				return failed(err, "")
			}
		}
	case "/compact":
		r, err := t.compact(ctx, s, arg)
		if err != nil {
			res.Output = err.Error()
		} else {
			res.Output = fmt.Sprintf("compacted: %d → %d tokens", r.TokensBefore, r.TokensAfter)
		}
		return res, nil
	}
	t.sessMu.Lock()
	quit, send := slashTo(ctx, s, line, &out, &out)
	t.sessMu.Unlock()
	_ = quit
	res.Output = out.String()
	if send != "" {
		if _, _, _, err := t.enqueue(queued{text: oneLineCLI(line, 200), prompt: send, display: line, echo: true}); err == nil {
			res.Sent = true
		}
	}
	t.publishMeta()
	return res, nil
}

// goalCommandStatus writes where the goal stands, as /goal alone does in the terminal.
func goalCommandStatus(s *session.Session, g *goal.State, out io.Writer) {
	gp := g
	goalCommandTo(s, &gp, "", out)
}

// Steer tells the running turn of the manager something without stopping it.
func (t *webTab) Steer(ctx context.Context, text string) error {
	text = strings.TrimSpace(text)
	switch {
	case text == "":
		return werr(http.StatusBadRequest, "empty", "/steer needs text: what the agent should read with its next step")
	case utf8.RuneCountInString(text) > maxSteerLen:
		return werr(http.StatusBadRequest, "empty", "the steer is longer than 4,000 characters")
	}
	t.mu.Lock()
	running, s := t.active && t.cancel != nil, t.s
	t.mu.Unlock()
	if !running || s == nil || s.Main() == nil {
		return werr(http.StatusConflict, "idle", "no turn is running: send it as a message")
	}
	s.Main().Steer(text)
	_, _ = s.Log.Emit("", "user.steer", map[string]any{"text": text})
	t.emit(&wire.Steer{To: "mgr", Text: clip(text, maxSteerLen)})
	return nil
}

// mainAgent is the harness id of the agent the person talks to.
func mainAgent(s *session.Session) string {
	if s != nil && s.Swarm != nil {
		return s.Swarm.ManagerID()
	}
	return "main"
}

// Interrupt cancels the running turn (target "turn"), refuses the questions of the agent the person talks to, and with them a
// compaction that runs; the goal pauses ("you interrupted it").
func (t *webTab) Interrupt(ctx context.Context, target string) error {
	if target != "" && target != "turn" && target != "mgr" {
		return werr(http.StatusBadRequest, "bad_request", "only the turn can be interrupted")
	}
	t.mu.Lock()
	cancel, cancelOp, s := t.cancel, t.cancelOp, t.s
	running := t.active && cancel != nil
	t.mu.Unlock()
	if cancelOp != nil {
		cancelOp()
		t.sys("◇", "compaction cancelled")
		if !running {
			return nil
		}
	}
	if !running {
		return werr(http.StatusConflict, "idle", "nothing is running to interrupt")
	}
	cancel()
	t.h.bridge.CancelAgent(t.id, mainAgent(s), approvals.ByCanceled)
	t.emit(&wire.Interrupt{ID: "turn"})
	return nil
}

// ---- exclusive sections ----------------------------------------------------------------------------------------------------

// errTabBusy is the refusal of an exclusive section while a turn runs.
var errTabBusy = werr(http.StatusConflict, "busy", "a turn is running: try again when it is over")

// exclusive runs fn while no turn starts and no queued item runs; it fails with busy when one runs or the session is not there.
func (t *webTab) exclusive(ctx context.Context, fn func(s *session.Session) error) error {
	t.mu.Lock()
	switch {
	case t.closed:
		t.mu.Unlock()
		return werr(http.StatusNotFound, "not_found", "this session was closed")
	case t.s == nil || t.restarting || t.starting:
		t.mu.Unlock()
		return werr(http.StatusConflict, "busy", "the session is starting: try again in a moment")
	case t.active || len(t.queue) > 0:
		t.mu.Unlock()
		return errTabBusy
	}
	t.excl++
	s := t.s
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		t.excl--
		t.cond.Broadcast()
		t.mu.Unlock()
		t.kick()
	}()
	return fn(s)
}

// whenIdle runs op now, exclusively, when nothing runs; otherwise it queues it behind the turn (shown as text in the queue). It
// reports whether it waits.
func (t *webTab) whenIdle(ctx context.Context, text string, op func(ctx context.Context, s *session.Session)) (bool, error) {
	err := t.exclusive(ctx, func(s *session.Session) error {
		op(ctx, s)
		return nil
	})
	if errors.Is(err, errTabBusy) {
		_, _, _, qerr := t.enqueue(queued{text: text, op: op})
		return true, qerr
	}
	return false, err
}

// ---- rename, goal ----------------------------------------------------------------------------------------------------------

// validName checks a tab name: 1 to 60 characters, no control characters.
func validName(name string) error {
	n := utf8.RuneCountInString(name)
	if strings.TrimSpace(name) == "" || n > maxNameLen || !utf8.ValidString(name) || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return werr(http.StatusBadRequest, "bad_name", "a name is 1 to 60 characters, without control characters")
	}
	return nil
}

// Rename changes the tab's name, kept in the session's sidecar.
func (t *webTab) Rename(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	if err := validName(name); err != nil {
		return err
	}
	t.mu.Lock()
	t.name = name
	s := t.s
	sum := t.summaryLocked()
	t.mu.Unlock()
	if s != nil {
		_ = session.UpdateMeta(s.Dir, func(m *session.Meta) { m.Name = name })
		webAction(s, "rename", name)
	}
	t.h.Publish(wire.Frame{Type: "tab", Data: wire.TabFrame{Op: "update", Tab: sum}, Critical: true})
	return nil
}

// webAction records a person's action in the session's log (web.action): what was done, never a secret or a file's content.
func webAction(s *session.Session, action, detail string) {
	if s == nil || s.Log == nil {
		return
	}
	_, _ = s.Log.Emit("", "web.action", map[string]any{"action": action, "detail": clip(detail, 300)})
}

// Goal sets, pauses, resumes or clears the standing goal.
func (t *webTab) Goal(ctx context.Context, req wire.GoalRequest) error {
	s := t.session()
	if s == nil {
		return werr(http.StatusConflict, "busy", "the session is starting: try again in a moment")
	}
	return t.goalAction(ctx, s, req, true)
}

// goalState is the goal's state word for the goal route's answer.
func (t *webTab) goalState() string {
	g := t.goals.State()
	if g == nil {
		return "none"
	}
	return goalWire(g).S
}

// goalAction does a goal request (set, pause, resume or clear; wire.GoalRequest). pause and clear interrupt the running turn first and
// take effect when it has stopped; set and resume start a turn, now or behind the running one.
func (t *webTab) goalAction(ctx context.Context, s *session.Session, req wire.GoalRequest, fromRoute bool) error {
	switch req.Action {
	case "set":
		text := strings.TrimSpace(req.Text)
		switch {
		case text == "":
			return werr(http.StatusBadRequest, "empty", "/goal needs text")
		case utf8.RuneCountInString(text) > maxGoalLen:
			return werr(http.StatusBadRequest, "empty", "the goal is longer than 4,000 characters")
		}
		_, _, _, err := t.enqueue(queued{text: oneLineCLI("/goal "+text, 200), op: func(ctx context.Context, s *session.Session) {
			start, err := t.goals.Set(s, text)
			if err != nil {
				return
			}
			webAction(s, "goal", "set")
			t.emit(&wire.Say{Who: "you", Text: clip("/goal "+text, 16<<10)})
			t.addHist("/goal " + text)
			t.emit(goalWire(t.goals.State()))
			t.emit(&wire.Say{Who: "sys", Glyph: "◇", Text: "goal set; plan:", Plan: true})
			t.publishMeta()
			t.turn(ctx, s, start, "", false)
		}})
		return err
	case "pause", "clear":
		g := t.goals.State()
		switch {
		case g == nil:
			return werr(http.StatusConflict, "no_goal", "there is no goal: /goal TEXT sets one")
		case req.Action == "pause" && g.Paused != "":
			return werr(http.StatusConflict, "not_active", "the goal is already paused")
		}
		apply := func(_ context.Context, s *session.Session) {
			if req.Action == "clear" {
				if t.goals.Clear(s) == nil {
					webAction(s, "goal", "clear")
					t.emit(goalWire(nil))
				}
			} else {
				_ = t.goals.Pause(s, "you paused it")
				webAction(s, "goal", "pause")
				t.emit(goalWire(t.goals.State()))
			}
			t.publishMeta()
		}
		t.mu.Lock()
		running, cancel := t.active, t.cancel
		t.mu.Unlock()
		if running && cancel != nil {
			t.enqueueFront(queued{text: "/goal " + req.Action, op: apply})
			cancel()
			t.h.bridge.CancelAgent(t.id, mainAgent(s), approvals.ByCanceled)
			t.emit(&wire.Interrupt{ID: "turn"})
			return nil
		}
		_, err := t.whenIdle(ctx, "/goal "+req.Action, apply)
		return err
	case "resume":
		g := t.goals.State()
		switch {
		case g == nil:
			return werr(http.StatusConflict, "no_goal", "there is no goal: /goal TEXT sets one")
		case g.Paused == "":
			return werr(http.StatusConflict, "not_paused", "the goal is not paused")
		}
		_, _, _, err := t.enqueue(queued{text: "/goal resume", op: func(ctx context.Context, s *session.Session) {
			next, err := t.goals.Resume(s)
			if err != nil {
				return
			}
			webAction(s, "goal", "resume")
			t.emit(goalWire(t.goals.State()))
			t.publishMeta()
			t.turn(ctx, s, next, "", false)
		}})
		return err
	}
	return werr(http.StatusBadRequest, "bad_request", "the action is set, pause, resume or clear")
}

// ---- session settings ------------------------------------------------------------------------------------------------------

// modeText is the acknowledgment of a mode change.
func modeText(m perm.Mode) (glyph, text string) {
	switch m {
	case perm.ModePlan:
		return "◇", "mode: plan (read-only)"
	case perm.ModeBypass:
		return "⚠", "mode: bypass (dangerous: everything is allowed but hard denies, deny rules and the high-risk commands, which still ask)"
	case perm.ModeYolo:
		return "⚠", "mode: yolo (dangerous: nothing asks; only hard denies and deny rules refuse)"
	}
	return "◇", "mode: " + string(m)
}

// SetMode changes the permission mode; bypass and yolo need the request's confirmation (scope "mode:<mode>:<tab>").
func (t *webTab) SetMode(ctx context.Context, req wire.ModeRequest) error {
	m := perm.Mode(req.Mode)
	switch m {
	case perm.ModeDefault, perm.ModeAcceptEdits, perm.ModePlan, perm.ModeBypass, perm.ModeYolo:
	default:
		return werr(http.StatusBadRequest, "bad_mode", "the mode is default, accept-edits, plan, bypass or yolo")
	}
	s := t.session()
	if s == nil {
		return werr(http.StatusConflict, "busy", "the session is starting: try again in a moment")
	}
	if (m == perm.ModeBypass || m == perm.ModeYolo) && s.Perm.Mode() != m {
		if err := authorize(ctx, "mode:"+string(m)+":"+t.id, []string{"permission mode " + string(m)}); err != nil {
			return err
		}
	}
	s.Perm.SetMode(m)
	webAction(s, "mode", string(m))
	g, text := modeText(m)
	t.sys(g, text)
	t.publishMeta()
	return nil
}

// SetModel changes the manager's model or a role's: a single agent moves its conversation; a team, or a role, starts again with it.
func (t *webTab) SetModel(ctx context.Context, req wire.ModelRequest) error {
	_, err := t.setModel(ctx, req)
	return err
}

// setModel is SetModel, reporting whether the team starts again.
func (t *webTab) setModel(ctx context.Context, req wire.ModelRequest) (bool, error) {
	ref := strings.TrimSpace(req.Ref)
	if ref == "" || len(ref) > 300 {
		return false, werr(http.StatusUnprocessableEntity, "model", "name a model: provider/model")
	}
	s := t.session()
	if s == nil {
		return false, werr(http.StatusConflict, "busy", "the session is starting: try again in a moment")
	}
	role := req.Role
	if role == "manager" && s.Swarm == nil {
		role = ""
	}
	if _, err := s.CheckModel(ref); err != nil {
		return false, werr(http.StatusUnprocessableEntity, "model", clip(err.Error(), 400))
	}
	switch {
	case role == "" && s.Swarm == nil:
		_, err := t.whenIdle(ctx, "/model "+ref, func(ctx context.Context, s *session.Session) {
			t.sessMu.Lock()
			got, err := s.SwitchModel(ctx, ref)
			t.sessMu.Unlock()
			if err != nil {
				t.sys("⚠", "model: "+err.Error())
				return
			}
			webAction(s, "model", got)
			text := "model: " + got + " (the conversation carries over; the prompt cache starts over)"
			if s.Model.Price.InputPerM == 0 && s.Model.Price.OutputPerM == 0 {
				text += " (price unknown)"
			}
			t.sys("◇", text)
			t.publishMeta()
			t.publishRoster()
		})
		return false, err
	case role == "" || role == "manager":
		return true, t.Restart(ctx, wire.RestartRequest{Kind: "model", Model: ref})
	case s.Swarm == nil && role != session.CompactorRole:
		return false, werr(http.StatusUnprocessableEntity, "model", "a single agent runs on one model; a team has roles: start a team first (/swarm 8). Only the compactor can differ here")
	}
	return true, t.Restart(ctx, wire.RestartRequest{Kind: "roles", RoleModels: map[string]string{role: ref}})
}

// effortLevels are the levels of /effort.
var effortLevels = []string{"default", "none", "minimal", "low", "medium", "high", "xhigh", "max"}

// SetEffort changes the reasoning effort for the requests that follow.
func (t *webTab) SetEffort(ctx context.Context, req wire.EffortRequest) (wire.EffortResult, error) {
	lv := strings.ToLower(strings.TrimSpace(req.Level))
	if !slices.Contains(effortLevels, lv) {
		return wire.EffortResult{}, werr(http.StatusBadRequest, "bad_level", "the level is default, none, minimal, low, medium, high, xhigh or max")
	}
	s := t.session()
	if s == nil {
		return wire.EffortResult{}, werr(http.StatusConflict, "busy", "the session is starting: try again in a moment")
	}
	s.SetEffort(lv)
	wanted, applied := s.Effort()
	if wanted == "" {
		wanted = "default"
	}
	if applied == "" {
		applied = "provider default"
	}
	webAction(s, "effort", wanted)
	t.sys("◇", "reasoning effort: "+wanted+" (this model: "+applied+"; each role uses its closest supported level)")
	t.publishMeta()
	return wire.EffortResult{Requested: wanted, Applied: applied}, nil
}

// SetBudget changes the budget: a single agent's for the turns from now on; a team's when it starts again.
func (t *webTab) SetBudget(ctx context.Context, req wire.BudgetRequest) error {
	_, err := t.budgetRoute(ctx, req)
	return err
}

// budgetRoute is SetBudget, reporting whether the goal was paused because the spend is already over the new budget.
func (t *webTab) budgetRoute(ctx context.Context, req wire.BudgetRequest) (bool, error) {
	usd := req.USD
	if req.Off {
		usd = 0
	}
	if usd < 0 || math.IsNaN(usd) || math.IsInf(usd, 0) {
		return false, werr(http.StatusBadRequest, "bad_budget", "a budget is a number of dollars, or off")
	}
	return t.setBudget(ctx, usd)
}

// setBudget sets the budget and reports whether the goal was paused for it.
func (t *webTab) setBudget(ctx context.Context, usd float64) (bool, error) {
	s := t.session()
	if s == nil {
		return false, werr(http.StatusConflict, "busy", "the session is starting: try again in a moment")
	}
	if s.Swarm != nil {
		t.mu.Lock()
		t.teamBudget = &usd
		t.mu.Unlock()
		if usd == 0 {
			t.sys("◇", "budget: none for the team (applies when the team starts again)")
		} else {
			t.sys("◇", fmt.Sprintf("budget: $%.2f for the team (applies when the team starts again)", usd))
		}
	} else {
		t.sessMu.Lock()
		err := s.SetBudget(usd)
		t.sessMu.Unlock()
		if err != nil {
			return false, werr(http.StatusBadRequest, "bad_budget", clip(err.Error(), 300))
		}
		if usd == 0 {
			t.sys("◇", "budget: removed")
		} else {
			t.sys("◇", fmt.Sprintf("budget: $%.2f for the turns from now on (spent so far $%.4f)", usd, s.Cost()))
		}
	}
	webAction(s, "budget", strconv.FormatFloat(usd, 'f', 2, 64))
	paused := false
	if g := t.goals.State(); usd > 0 && g != nil && g.Paused == "" && s.Cost() >= usd {
		if t.goals.Pause(s, budgetReached) == nil {
			paused = true
			t.emit(goalWire(t.goals.State()))
			t.sys("⚠", "budget reached: the goal is paused")
		}
	}
	t.publishMeta()
	return paused, nil
}

// Compact folds the manager's thread now.
func (t *webTab) Compact(ctx context.Context, req wire.CompactRequest) error {
	_, _, err := t.CompactNow(ctx, req.Focus)
	return err
}

// CompactNow folds the thread now and returns the tokens before and after; it is refused while a turn runs.
func (t *webTab) CompactNow(ctx context.Context, focus string) (from, to int, err error) {
	err = t.exclusive(ctx, func(s *session.Session) error {
		rep, cerr := t.compact(ctx, s, focus)
		from, to = rep.TokensBefore, rep.TokensAfter
		return cerr
	})
	return from, to, err
}

// compact runs a compaction under a context that an interrupt cancels.
func (t *webTab) compact(ctx context.Context, s *session.Session, focus string) (agent.CompactReport, error) {
	cctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	t.mu.Lock()
	if t.cancelOp != nil {
		t.mu.Unlock()
		cancel()
		return agent.CompactReport{}, werr(http.StatusConflict, "busy", "a compaction is running")
	}
	t.cancelOp = cancel
	t.mu.Unlock()
	defer func() {
		cancel()
		t.mu.Lock()
		t.cancelOp = nil
		t.mu.Unlock()
	}()
	t.sessMu.Lock()
	rep, err := s.Compact(cctx, clip(focus, 2000))
	t.sessMu.Unlock()
	switch {
	case errors.Is(err, context.Canceled):
		return rep, werr(http.StatusConflict, "nothing", "the compaction was cancelled")
	case err != nil:
		if strings.Contains(err.Error(), "nothing to compact") {
			return rep, werr(http.StatusConflict, "nothing", "nothing to compact yet: send a goal first")
		}
		return rep, werr(http.StatusConflict, "nothing", clip("compact: "+err.Error(), 400))
	case rep.Mode == "none":
		return rep, werr(http.StatusConflict, "nothing", "nothing to compact yet")
	}
	webAction(s, "compact", focus)
	t.sys("◇", fmt.Sprintf("compacted (%s): %d turns folded, %d → %d tokens; the next request writes the cached prefix again, once", rep.Mode, rep.FoldedTurns, rep.TokensBefore, rep.TokensAfter))
	return rep, nil
}

// StageLaunch stages flags for the next start of the team.
func (t *webTab) StageLaunch(ctx context.Context, p wire.LaunchPatch) error {
	if p.Isolation != nil && *p.Isolation != "none" && *p.Isolation != "worktree" {
		return werr(http.StatusBadRequest, "bad_isolation", "isolation is none or worktree")
	}
	if p.Verify != nil && len(*p.Verify) > 2000 {
		return werr(http.StatusBadRequest, "bad_flags", "the verify command is too long")
	}
	t.mu.Lock()
	if p.Isolation != nil {
		v := *p.Isolation
		t.staged.Isolation = &v
	}
	if p.Verify != nil {
		v := *p.Verify
		t.staged.Verify = &v
	}
	for _, pair := range []struct{ from, to **bool }{{&p.Commit, &t.staged.Commit}, {&p.Mailman, &t.staged.Mailman}, {&p.NoMcp, &t.staged.NoMcp}, {&p.TrustProject, &t.staged.TrustProject}} {
		if *pair.from != nil {
			v := **pair.from
			*pair.to = &v
		}
	}
	s := t.s
	t.mu.Unlock()
	onOff := func(b bool) string {
		if b {
			return "on"
		}
		return "off"
	}
	switch {
	case p.Isolation != nil:
		t.sys("◇", "--isolation "+*p.Isolation+" (applies when the team starts again)")
	case p.Verify != nil:
		t.sys("◇", "--verify \""+clip(*p.Verify, 300)+"\" (applies when the team starts again)")
	case p.Commit != nil:
		t.sys("◇", "--commit "+onOff(*p.Commit)+" (applies when the team starts again)")
	case p.Mailman != nil:
		t.sys("◇", "--mailman "+onOff(*p.Mailman)+" (applies when the team starts again)")
	case p.NoMcp != nil:
		t.sys("◇", "--no-mcp "+onOff(*p.NoMcp)+" (applies when the team starts again)")
	case p.TrustProject != nil:
		t.sys("◇", "--trust-project "+onOff(*p.TrustProject)+" (applies when the team starts again)")
	}
	webAction(s, "launch", "staged")
	t.publishMeta()
	return nil
}

// stagedFlags are the staged launch settings as chat flags.
func (t *webTab) stagedFlags() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var f []string
	st := t.staged
	if st.Isolation != nil {
		f = append(f, "--isolation", *st.Isolation)
	}
	if st.Verify != nil {
		f = append(f, "--verify", *st.Verify)
	}
	if st.Commit != nil {
		f = append(f, "--commit="+strconv.FormatBool(*st.Commit))
	}
	if st.Mailman != nil {
		f = append(f, "--mailman="+strconv.FormatBool(*st.Mailman))
	}
	if st.NoMcp != nil {
		f = append(f, "--no-mcp="+strconv.FormatBool(*st.NoMcp))
	}
	if st.TrustProject != nil {
		f = append(f, "--trust-project="+strconv.FormatBool(*st.TrustProject))
	}
	if t.teamBudget != nil {
		f = append(f, "--budget-usd", strconv.FormatFloat(*t.teamBudget, 'f', -1, 64))
	}
	return f
}

// ---- rules -----------------------------------------------------------------------------------------------------------------

// actionOf is the permission action of a rule effect.
func actionOf(effect string) (perm.Action, bool) {
	switch effect {
	case "", "allow":
		return perm.Allow, true
	case "deny":
		return perm.Deny, true
	case "ask":
		return perm.Ask, true
	}
	return perm.Allow, false
}

// AddRule adds a session rule (allow, deny, ask; "tests" expands to the tests preset). An allow rule the session does not have yet
// raises its privilege and needs the request's confirmation.
func (t *webTab) AddRule(ctx context.Context, req wire.RuleRequest) (wire.RuleResult, error) {
	return t.addRules(ctx, req.Effect, []string{req.Rule}, req.Origin)
}

// addRules adds session rules of one effect, all or none: the allow rules among them that the session does not have yet are
// authorized together (scope "rules:<tab>:<d16 of the effect and the rules>").
func (t *webTab) addRules(ctx context.Context, effect string, given []string, origin string) (wire.RuleResult, error) {
	act, ok := actionOf(effect)
	if !ok {
		return wire.RuleResult{}, werr(http.StatusBadRequest, "bad_rule", "the effect is allow, deny or ask")
	}
	s := t.session()
	if s == nil {
		return wire.RuleResult{}, werr(http.StatusConflict, "busy", "the session is starting: try again in a moment")
	}
	origin = strings.TrimSpace(origin)
	if origin == "" || len(origin) > 60 {
		origin = "this session"
	}
	type rule struct {
		pr     perm.Rule
		origin string
	}
	var parsed []rule
	for _, r := range given {
		r = strings.TrimSpace(r)
		switch {
		case r == "":
			return wire.RuleResult{}, werr(http.StatusBadRequest, "bad_rule", "a rule needs text, e.g. tests or Bash(go test:*)")
		case len(r) > maxRuleLen:
			return wire.RuleResult{}, werr(http.StatusBadRequest, "bad_rule", "a rule is at most 500 characters")
		case r == config.TestsPreset && act != perm.Allow:
			return wire.RuleResult{}, werr(http.StatusBadRequest, "bad_rule", "tests is a set of allow rules")
		}
		rules, o := []string{r}, origin
		if r == config.TestsPreset {
			rules, o = config.ExpandAllow([]string{config.TestsPreset}), "the tests preset"
		}
		for _, x := range rules {
			pr, err := perm.ParseRule(act, x)
			if err != nil {
				return wire.RuleResult{}, werr(http.StatusBadRequest, "bad_rule", clip(err.Error(), 300))
			}
			parsed = append(parsed, rule{pr, o})
		}
	}
	if act == perm.Allow {
		have := baselineOfSession(s).allow
		var raises []string
		for _, r := range parsed {
			if !have[r.pr.String()] && !slices.Contains(raises, r.pr.String()) {
				raises = append(raises, r.pr.String())
			}
		}
		slices.Sort(raises)
		if len(raises) > 0 {
			scope := "rules:" + t.id + ":" + d16(map[string]any{"effect": string(act), "rules": raises})
			if err := authorize(ctx, scope, []string{"allow " + strings.Join(raises, ", ")}); err != nil {
				return wire.RuleResult{}, err
			}
		}
	}
	t.mu.Lock()
	for _, r := range parsed {
		key := string(act) + " " + r.pr.String()
		if _, seen := t.origins[key]; !seen {
			t.origins[key] = r.origin
		}
	}
	t.mu.Unlock()
	for _, r := range parsed {
		s.Perm.AddRule(perm.ScopeSession, r.pr)
	}
	webAction(s, "rule", string(act)+" "+strings.Join(given, " "))
	for _, g := range given {
		g = strings.TrimSpace(g)
		if g == config.TestsPreset {
			t.sys("◇", fmt.Sprintf("%s this session: tests (%d rules) · from the tests preset", act, len(config.ExpandAllow([]string{g}))))
		} else {
			t.sys("◇", fmt.Sprintf("%s this session: %s · from %s", act, clip(g, maxRuleLen), origin))
		}
	}
	t.publishMeta()
	return wire.RuleResult{Added: len(parsed)}, nil
}

// RemoveRule removes a session rule.
func (t *webTab) RemoveRule(ctx context.Context, req wire.RuleRequest) (wire.RuleResult, error) {
	rule := strings.TrimSpace(req.Rule)
	if rule == "" || len(rule) > maxRuleLen {
		return wire.RuleResult{}, werr(http.StatusBadRequest, "bad_rule", "name the rule to remove")
	}
	s := t.session()
	if s == nil {
		return wire.RuleResult{}, werr(http.StatusConflict, "busy", "the session is starting: try again in a moment")
	}
	var found *wire.Rule
	rules := t.rulesOf(s)
	for i := range rules {
		if rules[i].Rule == rule && (req.Effect == "" || req.Effect == rules[i].Effect) {
			found = &rules[i]
			break
		}
	}
	switch {
	case found == nil:
		return wire.RuleResult{}, werr(http.StatusNotFound, "no_rule", "there is no such rule in force")
	case found.Fixed:
		return wire.RuleResult{}, werr(http.StatusConflict, "fixed", "built in or from a file: change it there")
	}
	if found.Effect != string(perm.Allow) {
		// taking back a deny or ask rule lets through what it held: it raises privilege
		scope := "rules.remove:" + t.id + ":" + d16(map[string]any{"effect": found.Effect, "rule": found.Rule})
		if err := authorize(ctx, scope, []string{"no longer " + found.Effect + " " + found.Rule}); err != nil {
			return wire.RuleResult{}, err
		}
	}
	if !s.Perm.RemoveRule(rule) {
		return wire.RuleResult{}, werr(http.StatusNotFound, "no_rule", "there is no such session rule")
	}
	t.mu.Lock()
	delete(t.origins, found.Effect+" "+rule)
	t.mu.Unlock()
	webAction(s, "rule removed", rule)
	t.sys("◇", "rule removed: "+clip(rule, maxRuleLen))
	t.publishMeta()
	return wire.RuleResult{Removed: 1}, nil
}

// Rules lists the rules in force for the tab with their origins.
func (t *webTab) Rules(ctx context.Context) ([]wire.Rule, error) {
	s := t.session()
	if s == nil {
		return []wire.Rule{}, nil
	}
	return t.rulesOf(s), nil
}

// rulesOf is every rule of the session's engine with where it came from (perm.RuleInfos, refined by the session and by what this
// tab recorded: /allow, a custom origin of the page); a rule that was not added while the session ran cannot be removed here.
func (t *webTab) rulesOf(s *session.Session) []wire.Rule {
	t.mu.Lock()
	origins := make(map[string]string, len(t.origins))
	for k, v := range t.origins {
		origins[k] = v
	}
	t.mu.Unlock()
	out := []wire.Rule{}
	for _, ri := range s.Perm.RuleInfos() {
		wr := wire.Rule{Effect: string(ri.Action), Rule: clip(ri.Rule, maxRuleLen), Origin: ri.Origin, Fixed: !ri.Runtime}
		if o, ok := origins[string(ri.Action)+" "+ri.Rule]; ok && ri.Runtime {
			wr.Origin = o
		} else if o := s.RuleOrigin(ri.Rule); o != "" {
			wr.Origin = o
		}
		out = append(out, wr)
	}
	return out
}

// ---- slash -----------------------------------------------------------------------------------------------------------------

// Slash lists the slash commands of the tab: the built-ins (grouped as /help groups them), then its custom commands, skills and
// tool server prompts.
func (t *webTab) Slash(ctx context.Context) ([]wire.SlashEntry, error) {
	out := builtinSlash()
	s := t.session()
	if s == nil {
		return out, nil
	}
	have := map[string]bool{}
	for _, e := range out {
		have[e.Cmd] = true
	}
	add := func(cmd, args, desc string) {
		if have[cmd] {
			return
		}
		have[cmd] = true
		out = append(out, wire.SlashEntry{Cmd: cmd, Args: clip(args, 80), Desc: clip(desc, 200), Group: "your commands", Custom: true})
	}
	if reg := s.Commands(); reg != nil {
		for _, c := range reg.List() {
			add("/"+c.Name, c.ArgumentHint, c.Description)
		}
	}
	if s.Skills != nil {
		for _, k := range s.Skills.Skills() {
			add("/"+k.Name, "", k.Summary())
		}
	}
	prompts := s.MCPPrompts()
	slices.SortFunc(prompts, func(a, b mcp.PromptEntry) int { return strings.Compare(a.Command, b.Command) })
	for _, p := range prompts {
		add(p.Command, "", p.Description)
	}
	return out, nil
}

// builtinSlash is the chat's own slash commands as /help lists them (chatHelp: the group headings and the two columns), with the
// palette's arguments and descriptions (chatCommands).
func builtinSlash() []wire.SlashEntry {
	pal := map[string]chatCommand{}
	for _, c := range chatCommands {
		pal[c.name] = c
	}
	var out []wire.SlashEntry
	group := ""
	for _, l := range strings.Split(chatHelp, "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if !strings.HasPrefix(l, "/") {
			group = strings.TrimSpace(l)
			continue
		}
		left, desc := l, ""
		if len(l) > 19 {
			left, desc = l[:19], strings.TrimSpace(l[19:])
		}
		words := strings.Fields(left)
		e := wire.SlashEntry{Cmd: words[0], Args: strings.Join(words[1:], " "), Desc: desc, Group: group}
		if c, ok := pal[strings.TrimPrefix(words[0], "/")]; ok {
			e.PaletteArgs, e.PaletteDesc = c.args, c.desc
		}
		out = append(out, e)
	}
	return out
}

// ---- meta and roster -------------------------------------------------------------------------------------------------------

// ptr returns a pointer to a copy of v.
func ptr[T any](v T) *T { return &v }

// meta is the tab's whole meta (the page's S.meta).
func (t *webTab) meta() wire.MetaPatch {
	t.mu.Lock()
	s, args, startedAt, running := t.s, append([]string(nil), t.args...), t.startedAt, t.active
	staged, teamBudget := t.staged, t.teamBudget
	queue := make([]wire.QueuedLine, 0, len(t.queue))
	for _, q := range t.queue {
		queue = append(queue, wire.QueuedLine{ID: q.id, Text: clip(q.text, 300)})
	}
	resumed := t.resumed
	t.mu.Unlock()

	m := wire.MetaPatch{Queued: &queue, Running: ptr(running), Headless: ptr(t.base.AskTimeout > 0)}
	if !startedAt.IsZero() {
		m.StartedAt = ptr(startedAt.UnixMilli())
	}
	if at := firstDuration(t.base.AskTimeout, t.h.askTimeout); at > 0 {
		m.AskTimeout = ptr(at.String())
	}
	f, _ := parseChatFlags(args)
	o := f.options()
	if s != nil {
		o = s.Options()
		model := s.ModelRef()
		if model == "" {
			model = s.Model.ID
		}
		m.Model = ptr(model)
		m.Mode = ptr(string(s.Perm.Mode()))
		wanted, _ := s.Effort()
		if wanted == "" {
			wanted = "default"
		}
		m.Effort = ptr(wanted)
		t.sessMu.Lock()
		m.Budget = ptr(s.Budget())
		t.sessMu.Unlock()
		workers := 0
		if s.Swarm != nil {
			workers = s.Swarm.MaxWorkers()
		}
		m.Swarm = ptr(workers)
		iso := "none"
		if s.WorktreeIsolation() {
			iso = "worktree"
		}
		m.Isolation = ptr(iso)
		m.Rules = ptr(t.rulesOf(s))
	} else {
		m.Model, m.Mode = ptr(o.Model), ptr(string(o.Mode))
		m.Swarm = ptr(o.Workers)
		m.Budget = ptr(o.BudgetUSD)
		m.Isolation = ptr(o.Isolation)
		m.Rules = ptr([]wire.Rule{})
	}
	m.Cwd = ptr(o.Cwd)
	if s != nil {
		m.SessionDir = ptr(s.Dir) // /status's session directory
	}
	m.Verify = ptr(o.Verify)
	m.Commit = ptr(o.Commit)
	m.Mailman = ptr(o.Mailman != nil && *o.Mailman)
	m.TrustProject = ptr(o.TrustProject)
	m.NoMcp = ptr(o.NoMCP)
	rm := map[string]string{}
	for k, v := range o.RoleModels {
		rm[k] = v
	}
	m.RoleModels = &rm
	if staged.Isolation != nil {
		m.Isolation = ptr(*staged.Isolation)
	}
	if staged.Verify != nil {
		m.Verify = ptr(*staged.Verify)
	}
	if staged.Commit != nil {
		m.Commit = ptr(*staged.Commit)
	}
	if staged.Mailman != nil {
		m.Mailman = ptr(*staged.Mailman)
	}
	if staged.NoMcp != nil {
		m.NoMcp = ptr(*staged.NoMcp)
	}
	if staged.TrustProject != nil {
		m.TrustProject = ptr(*staged.TrustProject)
	}
	if teamBudget != nil {
		m.Budget = ptr(*teamBudget)
	}
	goalText := ""
	if g := t.goals.State(); g != nil {
		goalText = clip(g.Objective, maxGoalLen)
	}
	m.GoalText = &goalText
	launch := "sleipnir chat"
	if la := launchArgs(args, t.stagedFlags()); len(la) > 0 {
		launch += " " + commandLine(la)
	}
	m.Launch = &launch
	if resumed != nil {
		r := *resumed
		m.ResumedFrom = &r
	}
	return m
}

// launchArgs are the chat arguments of a generation with the staged flags and without the resume of its own session: the command
// line that starts the same team again.
func launchArgs(args, staged []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--resume" || args[i] == "--continue" {
			if args[i] == "--resume" {
				i++
			}
			continue
		}
		out = append(out, args[i])
	}
	return append(out, staged...)
}

// metaDiff is the patch of the fields that differ between two metas (nil pointers count as unset).
func metaDiff(old, cur wire.MetaPatch) (wire.MetaPatch, bool) {
	var patch wire.MetaPatch
	changed := false
	ov, cv, pv := reflect.ValueOf(old), reflect.ValueOf(cur), reflect.ValueOf(&patch).Elem()
	for i := 0; i < cv.NumField(); i++ {
		c, o := cv.Field(i), ov.Field(i)
		if c.IsNil() {
			continue
		}
		if !o.IsNil() && reflect.DeepEqual(o.Elem().Interface(), c.Elem().Interface()) {
			continue
		}
		pv.Field(i).Set(c)
		changed = true
	}
	return patch, changed
}

// publishMeta sends what changed in the tab's meta to the pages.
func (t *webTab) publishMeta() {
	cur := t.meta()
	t.mu.Lock()
	patch, changed := metaDiff(t.lastMeta, cur)
	t.lastMeta = cur
	closed := t.closed
	t.mu.Unlock()
	if changed && !closed {
		t.h.Publish(wire.Frame{Type: "meta", Tab: t.id, Data: wire.MetaFrame{Tab: t.id, Patch: patch}, Critical: true})
	}
}

// publishRoster sends the roster when it changed.
func (t *webTab) publishRoster() {
	tr := t.translator()
	if tr == nil {
		return
	}
	r := tr.Roster()
	if r == nil {
		r = []wire.RosterEntry{}
	}
	t.mu.Lock()
	same := reflect.DeepEqual(r, t.lastRoster)
	t.lastRoster = r
	closed := t.closed
	t.mu.Unlock()
	if !same && !closed {
		t.h.Publish(wire.Frame{Type: "roster", Tab: t.id, Data: wire.RosterFrame{Tab: t.id, Roster: r}, Critical: true})
	}
}

// busy says whether a turn runs or an agent of the team is working.
func (t *webTab) busy() bool {
	t.mu.Lock()
	active, s := t.active, t.s
	t.mu.Unlock()
	if active {
		return true
	}
	if s != nil && s.Swarm != nil {
		for _, a := range s.Swarm.Board.Snapshot().Agents {
			if a.State == "running" {
				return true
			}
		}
	}
	return false
}

// ---- the session access of the route packages ------------------------------------------------------------------------------

// tabAccess is seam.SessionAccess over a tab.
type tabAccess struct{ t *webTab }

// TabID names the tab.
func (a tabAccess) TabID() string { return a.t.id }

// Session returns the current harness session (nil while the tab restarts).
func (a tabAccess) Session() *session.Session { return a.t.session() }

// Busy says whether a turn runs or any agent of the team is working.
func (a tabAccess) Busy() bool { return a.t.busy() }

// Exclusive runs fn while no turn starts and no command of the tab runs; it fails with busy when a turn is running.
func (a tabAccess) Exclusive(ctx context.Context, fn func(s *session.Session) error) error {
	return a.t.exclusive(ctx, fn)
}

// Emit adds host-originated UI events to the tab's journal.
func (a tabAccess) Emit(evs ...wire.Event) { a.t.emit(evs...) }

// Notify sends a note to the agents that touched files, as /rewind does after a restore: the agent the person talks to reads it
// with its next step.
func (a tabAccess) Notify(text string) {
	if s := a.t.session(); s != nil && strings.TrimSpace(text) != "" {
		s.Send(text)
	}
}

// Meta patches the tab's meta and publishes it.
func (a tabAccess) Meta(p wire.MetaPatch) {
	a.t.h.Publish(wire.Frame{Type: "meta", Tab: a.t.id, Data: wire.MetaFrame{Tab: a.t.id, Patch: p}, Critical: true})
}

// ---- helpers ---------------------------------------------------------------------------------------------------------------

// integrationText is what the end of an isolated run says ("" when it was not one).
func integrationText(rep *swarm.IntegrationReport) string {
	if rep == nil {
		return ""
	}
	text := "integration: " + rep.Message
	if !rep.Applied && rep.Hint != "" {
		text += " (" + rep.Hint + ")"
	}
	return text
}

// firstDuration is the first duration that is not zero.
func firstDuration(ds ...time.Duration) time.Duration {
	for _, d := range ds {
		if d != 0 {
			return d
		}
	}
	return 0
}

// eventsPath is the events file of a session directory.
func eventsPath(dir string) string { return filepath.Join(dir, "events.jsonl") }
