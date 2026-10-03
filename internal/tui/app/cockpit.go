package app

import (
	"strconv"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
	"github.com/anemos-labs/sleipnir/internal/tui/state"
	"github.com/anemos-labs/sleipnir/internal/tui/widget"
)

// CockpitOptions tune how a snapshot becomes the data of the cockpit.
type CockpitOptions struct {
	// NoAnim shows the standing horse and leaves out the arrival animation of mail.
	NoAnim bool
	// Born says at which animation frame a message (by its seq) was first shown, for the arrival animation of the newest one; a
	// message that is not in it is settled.
	Born Born
	// G0 is the size of the constitution and the tool list in tokens, when the caller knows it (the stack of a request does not
	// size it). Zero means an estimate: the smallest unsectioned prompt of any agent, which is G0 plus the little the newest
	// agent had been told.
	G0 int
	// Hints are the keys shown in the bottom row (nil: the keys of the design, which a program that has fewer of them does not
	// want), Status the first of the stats of the title bar ("● live").
	Hints  []widget.Hint
	Status string
}

// Cockpit draws the swarm cockpit of a snapshot on a screen of cols x rows at animation frame.
func Cockpit(sn *state.Snapshot, cols, rows, frame int, p widget.Palette, opt CockpitOptions) []cell.Line {
	return widget.Dashboard(CockpitData(sn, opt), cols, rows, frame, p)
}

// CockpitData is the data of the cockpit, read from a snapshot: nothing in it is invented, and what the events do not say
// (a provider's rate limit, how many times faster than one agent the swarm is) is left at zero, which the widgets leave out.
func CockpitData(sn *state.Snapshot, opt CockpitOptions) widget.DashboardData {
	if sn == nil {
		return widget.DashboardData{}
	}
	d := widget.DashboardData{
		Title:    sn.Session.Goal,
		Elapsed:  elapsed(sn),
		Spend:    sn.Totals.CostUSD,
		Budget:   sn.Totals.BudgetUSD,
		HitRatio: sn.Totals.HitRatio(),
		NoAnim:   opt.NoAnim,
		Hints:    opt.Hints,
		Status:   opt.Status,
		Governor: widget.Governor{RPM: sn.Governor.RPM, Err429: sn.Governor.RateLimited, Retries: sn.Governor.Retries},
	}
	if sn.Governor.RatePerMin > 0 {
		d.Governor.RPMLimit = int(sn.Governor.RatePerMin)
	}
	roles := map[string]string{}
	for _, a := range sn.Agents {
		roles[a.ID] = a.Role
	}
	d.Shared, d.PrefixLeft, d.PrefixTTL = sharedPrefix(sn, opt)
	d.Agents = agentRows(sn, opt)
	d.NowBucket = widget.GanttBuckets - 1
	d.Kanban = kanban(sn)
	d.Merge = mergeItems(sn)
	d.Mail, d.MailStats = mail(sn, opt)
	d.Feed = feed(sn, roles)
	return d
}

// elapsed is how long the session has been going at the snapshot's moment.
func elapsed(sn *state.Snapshot) time.Duration {
	start := sn.Session.Started
	if start.IsZero() {
		start = sn.First
	}
	if start.IsZero() {
		return 0
	}
	end := sn.Now
	if sn.Session.Ended && !sn.Session.EndedAt.IsZero() && sn.Session.EndedAt.Before(end) {
		end = sn.Session.EndedAt
	}
	return max(0, end.Sub(start))
}

// roleColor is the index into the palette's role colours the UX design gives a role: manager violet, the writers blue, the
// tester green, the reviewer amber, docs orange; the others take the spares.
func roleColor(role string) int {
	switch role {
	case "manager":
		return 0
	case "backend", "fullstack":
		return 1
	case "frontend":
		return 2
	case "tester":
		return 3
	case "reviewer":
		return 4
	case "docs":
		return 5
	case "scout":
		return 6
	}
	return 7
}

// status is the state the cockpit shows for an agent.
func status(a state.Agent) widget.AgentState {
	switch a.Status {
	case state.StatusStarting, state.StatusThinking:
		return widget.StateThinking
	case state.StatusTool:
		return widget.StateTool
	case state.StatusEditing:
		return widget.StateEdit
	case state.StatusWaiting, state.StatusAsking:
		return widget.StateWait // asking is waiting for a person: the amber the widget gives to waiting
	case state.StatusStuck, state.StatusError:
		return widget.StateStuck
	case state.StatusDone:
		return widget.StateDone
	}
	return widget.StateIdle
}

// sharedPrefix is the prefix that the most agents ride: its layers (G0 estimated when the caller does not know it, G1 the shared
// pin every agent has, G2 the role's) and how long the provider will keep it.
func sharedPrefix(sn *state.Snapshot, opt CockpitOptions) (layers []widget.Layer, left, ttl time.Duration) {
	g0 := opt.G0
	if g0 <= 0 {
		for _, a := range sn.Agents {
			if u := a.Stack.Unsectioned; u > 0 && (g0 == 0 || u < g0) {
				g0 = u
			}
		}
	}
	var g1, g2 int
	if len(sn.Prefixes) > 0 {
		pf := sn.Prefixes[0]
		for _, a := range sn.Agents {
			if a.ID == firstOf(pf.Agents) {
				for _, s := range a.Stack.Sections {
					if s.Name == "shared" {
						g1 = s.Tokens
					}
				}
			}
		}
		g2 = max(0, pf.Tokens-g1)
	}
	layers = []widget.Layer{
		{Name: "G0 constitution", Tokens: g0, Breakpoint: true},
		{Name: "G1 shared", Tokens: g1, Breakpoint: true},
		{Name: "G2 role", Tokens: g2, Breakpoint: true},
	}
	// the prefix that was used last sets the clock
	var newest state.TTLEntry
	for _, e := range sn.TTL {
		if e.Kind == "prefix" && e.Last.After(newest.Last) {
			newest = e
		}
	}
	if !newest.Last.IsZero() {
		ttl = time.Duration(newest.TTLSeconds) * time.Second
		left = newest.Remaining(sn.Now)
	}
	return layers, left, ttl
}

// firstOf returns the first string or empty for an empty slice.
func firstOf(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

// agentRows are the rows of the agent table, the manager first, each with its lane of activity.
func agentRows(sn *state.Snapshot, opt CockpitOptions) []widget.AgentRow {
	prefixOf := map[string]state.Prefix{}
	for _, p := range sn.Prefixes {
		for _, id := range p.Agents {
			prefixOf[id] = p
		}
	}
	lanes := map[string]state.ActivityRow{}
	for _, r := range sn.Activity.Rows {
		lanes[r.Agent] = r
	}
	tasks := map[string]state.Task{}
	for _, t := range sn.Board.Tasks {
		tasks[t.ID] = t
	}
	rows := make([]widget.AgentRow, 0, len(sn.Agents))
	for _, a := range sn.Agents {
		if a.Service {
			continue
		}
		shared, own := 0, a.Stack.Prompt
		if p, ok := prefixOf[a.ID]; ok && p.Riders > 1 {
			shared = min(p.Tokens, a.Stack.Prompt)
			own = a.Stack.Prompt - shared
		}
		r := widget.AgentRow{
			ID: a.ID, Role: a.Role, RoleColor: roleColor(a.Role), State: status(a),
			Doing: doing(a, tasks), Shared: shared, Own: own, Hit: a.Stack.Hit, Cost: a.CostUSD, Scope: scope(a),
		}
		if lane, ok := lanes[a.ID]; ok {
			r.Levels, r.Marks = lane8(lane)
		}
		rows = append(rows, r)
	}
	return rows
}

// doing says in a few words what an agent is doing.
func doing(a state.Agent, tasks map[string]state.Task) string {
	switch a.Status {
	case state.StatusTool, state.StatusEditing:
		if a.ToolSummary != "" {
			return a.ToolSummary
		}
		return a.Tool
	case state.StatusStuck:
		if a.Stuck.Note != "" {
			return "stuck: " + a.Stuck.Note
		}
		return "stuck"
	case state.StatusWaiting:
		if t, ok := tasks[a.Task]; ok && t.Title != "" {
			return "waits: " + t.Title
		}
		return "waits for the team"
	case state.StatusAsking:
		// The tool call that is held at the question is the agent's current one: what it asked to do.
		if what := firstNonEmpty(a.ToolSummary, a.Tool); what != "" {
			return "asks: " + what
		}
		return "asks permission"
	case state.StatusDone:
		if t, ok := tasks[a.Task]; ok && t.Result != "" {
			return t.Result
		}
		return "done"
	case state.StatusIdle:
		return "waits for work"
	case state.StatusError:
		if a.EndState == "failed" && a.Line != "" {
			return strings.TrimPrefix(a.Line, "stopped with an error: ")
		}
		return "stopped by an error (see the feed)"
	}
	if a.Compacting {
		return "folds its thread"
	}
	if t, ok := tasks[a.Task]; ok && t.Title != "" {
		return t.Title
	}
	if a.Line != "" {
		return a.Line
	}
	return "thinking"
}

// scope summarizes the paths assigned to the agent's current tasks. A write
// lease on an individual file does not assign its enclosing directory.
func scope(a state.Agent) string {
	switch {
	case len(a.Scope) == 1:
		return a.Scope[0]
	case len(a.Scope) > 1:
		return a.Scope[0] + " +" + strconv.Itoa(len(a.Scope)-1)
	}
	return ""
}

// lane8 turns a row of the activity matrix (a bucket a second, the last 120) into the lane of the gantt (the last 60).
func lane8(r state.ActivityRow) ([]uint8, []widget.LaneMark) {
	n := widget.GanttBuckets
	levels := []uint8(r.Levels)
	marks := []uint8(r.Marks)
	from := max(0, len(levels)-n)
	out := make([]uint8, len(levels)-from)
	copy(out, levels[from:])
	var lm []widget.LaneMark
	for i := from; i < len(marks) && i < len(levels); i++ {
		b := i - from
		switch m := marks[i]; {
		case m&state.ActStuck != 0:
			lm = append(lm, widget.LaneMark{Bucket: b, Kind: widget.LaneStuck})
		case m&state.ActCompact != 0:
			lm = append(lm, widget.LaneMark{Bucket: b, Kind: widget.LaneCompact})
		case m&state.ActMail != 0:
			lm = append(lm, widget.LaneMark{Bucket: b, Kind: widget.LaneMail})
		}
	}
	return out, lm
}

// kanban groups tasks by their current state. Terminal failures get a separate
// column only when present, so they never inflate the running count.
func kanban(sn *state.Snapshot) []widget.KanbanCol {
	cols := []widget.KanbanCol{{Kind: widget.ColTodo}, {Kind: widget.ColRunning}, {Kind: widget.ColVerifying}, {Kind: widget.ColMerged}}
	failed := widget.KanbanCol{Kind: widget.ColFailed}
	for _, t := range sn.Board.Tasks {
		card := widget.KanbanCard{ID: t.ID, Label: taskLabel(t), Failed: t.State == state.TaskFailed}
		switch t.State {
		case state.TaskRunning:
			cols[1].Cards = append(cols[1].Cards, card)
		case state.TaskFailed:
			failed.Cards = append(failed.Cards, card)
		case state.TaskVerifying:
			cols[2].Cards = append(cols[2].Cards, card)
		case state.TaskMerged:
			cols[3].Cards = append(cols[3].Cards, card)
		default:
			cols[0].Cards = append(cols[0].Cards, card)
		}
	}
	if len(failed.Cards) > 0 {
		cols = append(cols, failed)
	}
	return cols
}

// taskLabel is what a task is about in four letters: the first directory of its scope, else the first long word of its title.
func taskLabel(t state.Task) string {
	if len(t.Files) > 0 {
		f := strings.TrimLeft(t.Files[0], "./")
		if i := strings.IndexAny(f, "/*"); i > 0 {
			f = f[:i]
		}
		if f != "" {
			return short(strings.ToLower(f), 4)
		}
	}
	for _, w := range strings.Fields(t.Title) {
		w = strings.ToLower(strings.Trim(w, ".,:;()[]\"'"))
		if len(w) >= 4 && !stopWords[w] {
			return short(w, 4)
		}
	}
	return short(strings.ToLower(t.Role), 4)
}

var stopWords = map[string]bool{"survey": true, "write": true, "review": true, "every": true, "with": true, "from": true, "that": true, "add": true}

// short retains at most n runes without a truncation marker; n must be nonnegative.
func short(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// mergeItems are the verified merge queue: what waits (the head of the line is being verified, for the queue is serial), what
// was sent back and what went in.
func mergeItems(sn *state.Snapshot) []widget.MergeItem {
	if !sn.Merge.Seen {
		return nil
	}
	var out []widget.MergeItem
	for _, e := range sn.Merge.Recent {
		it := widget.MergeItem{ID: e.Task, Stage: widget.MergeDone, Worker: e.Agent}
		if e.Stage.Bounced() {
			it.Failed, it.Note = true, e.Reason
			switch e.Stage {
			case state.MergeConflict:
				it.Stage = widget.MergeRebase
			case state.MergeRejected:
				it.Stage = widget.MergeQueued
			default:
				it.Stage = widget.MergeVerify
			}
		}
		out = append(out, it)
	}
	for i, e := range sn.Merge.Waiting {
		it := widget.MergeItem{ID: e.Task, Stage: widget.MergeQueued, Worker: e.Agent}
		if i == 0 {
			it.Stage = widget.MergeVerify
		}
		out = append(out, it)
	}
	return out
}

// mail is the newest messages (oldest of them first, the order the widget draws) and the router's counts.
func mail(sn *state.Snapshot, opt CockpitOptions) ([]widget.Mail, widget.MailStats) {
	recent := sn.Mail.Recent
	if len(recent) > 6 {
		recent = recent[len(recent)-6:]
	}
	out := make([]widget.Mail, 0, len(recent))
	for _, m := range recent {
		born := -1
		if b, ok := opt.Born[m.Seq]; ok && !opt.NoAnim {
			born = b
		}
		out = append(out, widget.Mail{From: m.From, To: m.To, Subject: m.Summary, Tokens: m.Tokens, Born: born})
	}
	c := sn.Mail.Counts
	return out, widget.MailStats{Routed: max(c.Routed, c.Sent), Delivered: c.Delivered, Ignored: c.Ignored}
}

// feed is the live feed, the newest line first.
func feed(sn *state.Snapshot, roles map[string]string) []widget.FeedLine {
	start := sn.Session.Started
	if start.IsZero() {
		start = sn.First
	}
	out := make([]widget.FeedLine, 0, len(sn.Feed))
	for i := len(sn.Feed) - 1; i >= 0; i-- {
		f := sn.Feed[i]
		out = append(out, widget.FeedLine{
			At: max(0, f.T.Sub(start)), Agent: f.Agent, Color: roleColor(roles[f.Agent]), Kind: feedKind(f), Text: f.Text, Tail: f.Detail,
		})
	}
	return out
}

func feedKind(f state.FeedLine) widget.FeedKind {
	switch f.Kind {
	case state.FeedPerm:
		// A permission line is the question (a person is wanted), the answer yes, or a refusal: only a yes went well.
		if f.Glyph == state.GlyphOK {
			return widget.FeedOK
		}
		return widget.FeedWarn
	case state.FeedTool, state.FeedEnd:
		return widget.FeedOK
	case state.FeedToolErr, state.FeedError, state.FeedStuck, state.FeedCache, state.FeedRetry:
		return widget.FeedWarn
	case state.FeedCompact:
		return widget.FeedCompact
	case state.FeedMail:
		return widget.FeedMail
	}
	return widget.FeedInfo
}

// firstNonEmpty is the first argument that is not empty.
func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
