package translate

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/inspect"
	"github.com/anemos-labs/sleipnir/internal/swarm"
	"github.com/anemos-labs/sleipnir/internal/tui/state"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// taskOut is what was last sent about a task.
type taskOut struct {
	ev      *TaskX
	status  string // the board's word last seen (todo, doing, blocked, review, done, failed)
	lastCol string // the last column that was not a failure's
}

// syncTask sends a task event when the task's column, title, owner, deps, scope or closure changed, and a note when its owner
// submitted it. force sends it whatever it says. A task the board failed stays in the todo column with Failed set and its closure; one
// that was superseded or canceled keeps the column it had.
func (t *Translator) syncTask(id string, ts float64, force bool) {
	tk, ok := t.st.Task(id)
	if !ok {
		return
	}
	o := t.d.tasks[id]
	if o == nil {
		o = &taskOut{}
		t.d.tasks[id] = o
	}
	col := "todo"
	switch tk.State {
	case state.TaskRunning:
		col = "running"
		switch {
		case tk.Merge == "merged" || tk.Merge == "empty":
			col = "merged" // its work is in the integration branch; the board's own word follows
		case t.d.queue.merging[id]:
			col = "verify" // between the queue's verdict and the task's merge record
		}
	case state.TaskVerifying:
		col = "verify"
	case state.TaskMerged:
		col = "merged"
	case state.TaskFailed:
		col = "todo"
		if k := closureKind(tk.Closure); (k == "superseded" || k == "canceled") && o.lastCol != "" {
			col = o.lastCol
		}
	}
	if tk.State != state.TaskFailed {
		o.lastCol = col
	}
	closure := tk.Closure
	if closure == "" && tk.Status == "blocked" && tk.BlockedOn != "" {
		closure = "blocked_on(" + tk.BlockedOn + ")"
	}
	scope := "-"
	if len(tk.Files) > 0 {
		scope = strings.Join(tk.Files, ", ")
	}
	ev := &TaskX{Task: wire.Task{ID: id, Title: line(tk.Title, capArg), Owner: uiID(tk.Owner), Deps: tk.Deps, Scope: line(scope, capPath), S: col,
		Closure: line(closure, capID)}, Failed: tk.State == state.TaskFailed, Attempts: tk.Attempts, Blocked: tk.Status == "blocked"}
	if len(ev.Deps) == 0 {
		ev.Deps = nil
	}
	if !t.isolated() && tk.VerificationFailures != t.d.queue.vfails[id] {
		if n := tk.VerificationFailures - t.d.queue.vfails[id]; n > 0 && !t.d.history && !force { // a history's are not counted
			t.d.queue.bounced += n
			t.put(&wire.Queue{Conflicts: t.d.queue.conflicts, Bounced: t.d.queue.bounced}, ts, 0, nil)
		}
		t.d.queue.vfails[id] = tk.VerificationFailures
	}
	submitted := tk.Status == "review" && o.status != "review" && tk.Owner != "" && !t.d.history
	o.status = tk.Status
	if !force && o.ev != nil && sameTask(o.ev, ev) {
		return
	}
	if submitted {
		t.put(&wire.Note{ID: uiID(tk.Owner), G: "done", Text: "submitted " + id, Task: id}, ts, 0, nil)
	}
	c := *ev
	c.Deps = append([]string(nil), ev.Deps...)
	o.ev = &c
	t.put(ev, ts, 0, nil)
}

// sameTask reports whether two task events say the same thing.
func sameTask(a, b *TaskX) bool {
	return a.ID == b.ID && a.Title == b.Title && a.Owner == b.Owner && a.Scope == b.Scope && a.S == b.S && a.Closure == b.Closure &&
		a.Failed == b.Failed && a.Attempts == b.Attempts && a.Blocked == b.Blocked && reflect.DeepEqual(a.Deps, b.Deps)
}

// closureKind is the kind of a closure written as kind or kind(target).
func closureKind(c string) string {
	if i := strings.IndexByte(c, '('); i >= 0 {
		return c[:i]
	}
	return c
}

// isolated reports whether the team works in trees of its own (it has a merge queue).
func (t *Translator) isolated() bool {
	if t.cfg.Verify != nil {
		if _, iso := t.cfg.Verify(); iso {
			return true
		}
	}
	return t.st.Session().Isolation == "worktree"
}

// queueOut is the merge queue's counters and what is known of its submissions.
type queueOut struct {
	conflicts, bounced int
	at                 map[string]time.Time // task -> when its submission was queued
	ms                 map[string]int64     // task -> how long its last merge took
	cmd                map[string]string
	vfails             map[string]int  // task -> the verification failures counted (shared tree)
	merging            map[string]bool // task -> a submission is in the queue and has no outcome yet
	gate               string          // the command the harness's verification gate last ran (verify.run), when --verify is not known
}

// taskRef reads the task id out of the name the swarm gives a submission ("T3: the task's title").
func taskRef(subject string) string {
	if len(subject) < 2 || subject[0] != 'T' {
		return ""
	}
	i := 1
	for i < len(subject) && subject[i] >= '0' && subject[i] <= '9' {
		i++
	}
	if i == 1 || (i < len(subject) && subject[i] != ':' && subject[i] != ' ') {
		return ""
	}
	return subject[:i]
}

// verifyCmd is the command the queue verifies a task's merge with: the session's --verify with {dirs} expanded to the task's
// directories, or "merge (no verify)".
func (t *Translator) verifyCmd(task string) string {
	var cmd string
	if t.cfg.Verify != nil {
		cmd, _ = t.cfg.Verify()
	}
	if strings.TrimSpace(cmd) == "" {
		cmd = t.d.queue.gate
	}
	if strings.TrimSpace(cmd) == "" {
		return "merge (no verify)"
	}
	var files []string
	if tk, ok := t.st.Task(task); ok {
		files = tk.Files
	}
	return line(swarm.ExpandVerify(cmd, t.root(), files), capArg)
}

// merge translates the merge queue's events (isolated teams) into queue events (head, step, counters) and merge events, and the
// conflicts, refusals and failed verifications into system rows.
func (t *Translator) merge(e events.Event, ts float64, at int64) {
	if t.d.history {
		return
	}
	var p struct {
		Task     string   `json:"task"`
		Files    []string `json:"files"`
		Reason   string   `json:"reason"`
		Empty    bool     `json:"empty"`
		Cmd      string   `json:"cmd"`
		ExitCode *int     `json:"exit_code"`
		TimedOut bool     `json:"timed_out"`
		Outcome  string   `json:"outcome"`
	}
	if json.Unmarshal(e.Data, &p) != nil {
		return
	}
	q := &t.d.queue
	if q.cmd == nil {
		q.cmd, q.merging = map[string]string{}, map[string]bool{}
	}
	id := taskRef(p.Task)
	if e.Type == events.TypeTaskMerge {
		id = line(p.Task, capID)
	}
	empty := func() { t.put(&wire.Queue{Conflicts: q.conflicts, Bounced: q.bounced}, ts, 0, nil) }
	switch e.Type {
	case events.TypeMergeQueued:
		if id == "" {
			return
		}
		if len(q.at) > 4096 {
			q.at, q.ms, q.cmd, q.merging = map[string]time.Time{}, map[string]int64{}, map[string]string{}, map[string]bool{}
		}
		q.at[id], q.cmd[id], q.merging[id] = e.TS, t.verifyCmd(id), true
		head := id
		t.put(&wire.Queue{QHead: &head, Cmd: q.cmd[id], Step: "verifying", Conflicts: q.conflicts, Bounced: q.bounced}, ts, 0, nil)
	case events.TypeMergeMerged:
		if id == "" {
			return
		}
		var ms int64
		if s, ok := q.at[id]; ok && !s.IsZero() && e.TS.After(s) {
			ms = e.TS.Sub(s).Milliseconds()
		}
		q.ms[id] = ms
		head := id
		t.put(&wire.Queue{QHead: &head, Cmd: t.cmdOf(id), Step: "verified", Ms: ms, Conflicts: q.conflicts, Bounced: q.bounced}, ts, 0, nil)
	case events.TypeTaskMerge:
		delete(q.merging, id)
		if p.Outcome == "merged" || p.Outcome == "empty" {
			t.put(&wire.Merge{ID: id, Cmd: t.cmdOf(id), Ms: q.ms[id]}, ts, 0, nil)
			empty()
		}
	case events.TypeMergeConflict:
		delete(q.merging, id)
		q.conflicts++
		empty()
		t.sysRow("mgr", "⚠", firstNonEmpty(id, "work")+": merge conflict in "+strings.Join(p.Files, ", "), uiID(e.Agent), id, ts, at)
	case events.TypeMergeRejected:
		if p.Empty {
			return
		}
		delete(q.merging, id)
		q.conflicts++
		empty()
		t.sysRow("mgr", "⚠", firstNonEmpty(id, "work")+": the merge queue refused it: "+p.Reason, uiID(e.Agent), id, ts, at)
	case events.TypeMergeVerifyFail:
		delete(q.merging, id)
		q.bounced++
		empty()
		how := "failed"
		switch {
		case p.TimedOut:
			how = "timed out"
		case p.ExitCode != nil:
			how = "failed (exit " + strconv.Itoa(*p.ExitCode) + ")"
		}
		t.sysRow("mgr", "⚠", firstNonEmpty(id, "work")+": "+firstNonEmpty(p.Cmd, "the verification")+" "+how, uiID(e.Agent), id, ts, at)
	}
}

// cmdOf is the verify command recorded for a task's submission.
func (t *Translator) cmdOf(id string) string {
	if c := t.d.queue.cmd[id]; c != "" {
		return c
	}
	return t.verifyCmd(id)
}

// sysRow sends a system line of a channel.
func (t *Translator) sysRow(ch, glyph, txt, ag, task string, ts float64, at int64) {
	if ch == "" {
		ch = "mgr"
	}
	t.put(&wire.Sys{Ch: ch, Glyph: glyph, Text: line(txt, capReason), Ag: ag, Task: task}, ts, at, nil)
}

// noticeGate is the rate limit of the notice and lease rows: at most gateMax per gateWindow seconds; the rest are counted into one
// row.
type noticeGate struct {
	start   float64
	n       int
	skipped int
}

// The notice rate limit: at most gateMax gated rows in each window of gateWindow seconds.
const (
	gateMax    = 20
	gateWindow = 10.0
)

// pass reports whether one more gated row may be sent now.
func (t *Translator) pass(ts float64) bool {
	if t.d.history {
		return true
	}
	g := &t.d.gate
	if ts-g.start >= gateWindow {
		t.gateSummary(ts)
		g.start, g.n = ts, 0
	}
	if g.n < gateMax {
		g.n++
		return true
	}
	g.skipped++
	return false
}

// gateSummary sends the row that counts the rows the gate held back.
func (t *Translator) gateSummary(ts float64) {
	g := &t.d.gate
	if g.skipped == 0 {
		return
	}
	n := g.skipped
	g.skipped = 0
	word := "notices"
	if n == 1 {
		word = "notice"
	}
	t.put(&wire.Sys{Ch: "mgr", Glyph: "◇", Text: fmt.Sprintf("%d more %s", n, word)}, ts, 0, nil)
}

// noticeDedupe pairs the notices the session gives both to the sink and to the log, so that each is one row.
type noticeDedupe struct {
	n map[string]*dedupeEntry
}

// dedupeEntry counts the notices of one text seen from one side and not yet from the other.
type dedupeEntry struct {
	sink, log int
	at        float64
}

// dedupeKeep is how long a notice waits for its twin from the other side (session.New can take this long with MCP servers).
const dedupeKeep = 60.0

// twin reports whether the notice was already shown from the other side (and consumes that), else records it.
func (t *Translator) twin(uid, msg string, fromSink bool, ts float64) bool {
	if t.d.logOnly {
		return false
	}
	d := &t.d.dedupe
	k := uid + "|" + msg
	e := d.n[k]
	if e == nil {
		if len(d.n) >= 512 {
			d.n = map[string]*dedupeEntry{}
		}
		e = &dedupeEntry{}
		d.n[k] = e
	}
	e.at = ts
	if fromSink {
		if e.log > 0 {
			e.log--
			return true
		}
		e.sink++
		return false
	}
	if e.sink > 0 {
		e.sink--
		return true
	}
	e.log++
	return false
}

// expireTwins forgets the notices that waited too long for a twin.
func (t *Translator) expireTwins(ts float64) {
	for k, e := range t.d.dedupe.n {
		if ts-e.at >= dedupeKeep {
			delete(t.d.dedupe.n, k)
		}
	}
}

// notice is a notice of the sink.
func (t *Translator) notice(uid, level, msg string, ts float64, fromSink bool) {
	t.noticeAt(uid, level, msg, ts, 0, fromSink)
}

// noticeAt sends a notice as a sys row of the agent's channel (the manager's for the session's own), once whichever side it came
// from, within the rate limit.
func (t *Translator) noticeAt(uid, level, msg string, ts float64, at int64, fromSink bool) {
	if msg == "" || t.twin(uid, msg, fromSink, ts) || !t.pass(ts) {
		return
	}
	glyph := "◇"
	if level == "warn" || level == "error" {
		glyph = "⚠"
	}
	ag := ""
	if uid != "mgr" {
		ag = uid
	}
	t.sysRow(uid, glyph, msg, ag, "", ts, at)
}

// lease sends the lease warnings a person should see: a write refused because another agent holds the file, or outside its task's
// scope.
func (t *Translator) lease(e events.Event, ts float64, at int64) {
	var p struct {
		Action string `json:"action"`
		Agent  string `json:"agent"`
		Path   string `json:"path"`
		Holder string `json:"holder"`
	}
	if json.Unmarshal(e.Data, &p) != nil {
		return
	}
	who := uiID(firstNonEmpty(p.Agent, e.Agent))
	path := relPath(t.root(), p.Path)
	var task string
	if a, ok := t.st.AgentLite(firstNonEmpty(p.Agent, e.Agent)); ok {
		task = a.Task
	}
	switch p.Action {
	case "conflict":
		if t.pass(ts) {
			t.sysRow("mgr", "⚠", "lease conflict: "+firstNonEmpty(path, "a file")+" held by "+uiID(p.Holder), who, task, ts, at)
		}
	case "scope":
		if t.pass(ts) {
			t.sysRow("mgr", "⚠", who+" tried to write outside its task's scope: "+path, who, task, ts, at)
		}
	}
}

// stall is swarm.stall: the finding, and the row that shows it.
func (t *Translator) stall(e events.Event, ts float64, at int64) {
	var p struct {
		Action string `json:"action"`
		Kind   string `json:"kind"`
		Task   string `json:"task"`
		Agent  string `json:"agent"`
		Detail string `json:"detail"`
	}
	if json.Unmarshal(e.Data, &p) != nil || p.Kind == "" {
		return
	}
	s := "raise"
	if p.Action == "clear" {
		s = "clear"
	}
	who := ""
	if p.Agent != "" {
		who = uiID(p.Agent)
	}
	kind := line(p.Kind, capID)
	t.put(&wire.Stall{ID: who, Task: line(p.Task, capID), Kind: kind, S: s, Text: line(p.Detail, capWhy)}, ts, at, nil)
	words := strings.ReplaceAll(kind, "_", " ")
	if s == "clear" {
		t.sysRow("mgr", "◇", words+" cleared", who, p.Task, ts, at)
		return
	}
	t.sysRow("mgr", "⚠", firstNonEmpty(p.Detail, words), who, p.Task, ts, at)
}

// handover is swarm.handover: the phase, and the row that shows it (the owner change follows as a task event).
func (t *Translator) handover(e events.Event, ts float64, at int64) {
	var p struct {
		Phase   string          `json:"phase"`
		Task    string          `json:"task"`
		From    string          `json:"from"`
		To      string          `json:"to"`
		Closure json.RawMessage `json:"closure"`
		Error   string          `json:"error"`
	}
	if json.Unmarshal(e.Data, &p) != nil || p.Task == "" || p.Phase == "" {
		return
	}
	from, to := uiID(p.From), uiID(p.To)
	c := closureString(p.Closure)
	t.put(&wire.Handover{Task: line(p.Task, capID), From: from, To: to, S: line(p.Phase, capID), Closure: line(c, capID), Error: line(p.Error, capWhy)}, ts, at, nil)
	switch p.Phase {
	case "begin":
		t.sysRow("mgr", "↺", p.Task+": handing over from "+firstNonEmpty(from, "its worker")+" to "+firstNonEmpty(to, "another worker"), to, p.Task, ts, at)
	case "done":
		t.sysRow("mgr", "↺", p.Task+" handed from "+firstNonEmpty(from, "its worker")+" to "+firstNonEmpty(to, "another worker"), to, p.Task, ts, at)
	case "abort":
		t.sysRow("mgr", "⚠", p.Task+": the handover from "+firstNonEmpty(from, "its worker")+" failed: "+p.Error, from, p.Task, ts, at)
	}
}

// closureString renders a typed closure ({"kind","target"} or null) as kind or kind(target).
func closureString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var c struct {
		Kind   string `json:"kind"`
		Target string `json:"target"`
	}
	if json.Unmarshal(raw, &c) != nil || c.Kind == "" {
		var s string
		_ = json.Unmarshal(raw, &s)
		return s
	}
	if c.Target == "" {
		return c.Kind
	}
	return c.Kind + "(" + c.Target + ")"
}

// digestInfo is a mailman's digest as its mail.send carried it.
type digestInfo struct {
	from, to, text string
	origins        []string
}

// mail translates the mail events: a message between agents, a mailman's digest, a message that was not delivered, and the counts.
func (t *Translator) mail(e events.Event, ts float64, at int64) {
	switch e.Type {
	case events.TypeMailSend:
		var p struct {
			ID      string   `json:"id"`
			From    string   `json:"from"`
			To      string   `json:"to"`
			Text    string   `json:"text"`
			Via     string   `json:"via"`
			Origins []string `json:"origins"`
		}
		if json.Unmarshal(e.Data, &p) != nil {
			return
		}
		from := firstNonEmpty(p.From, e.Agent)
		if nonAgent(from) || p.To == "" {
			break // the harness's own mail is not agent mail
		}
		if p.Via != "" {
			if len(t.d.digests) >= maxDigests {
				t.d.digests = map[string]digestInfo{}
			}
			t.d.digests[p.ID] = digestInfo{from: from, to: p.To, text: p.Text, origins: p.Origins}
			break
		}
		if t.agentOutOf(uiID(from)).service {
			break
		}
		t.put(&MailX{Mail: wire.Mail{From: uiID(from), To: uiID(p.To), Text: line(p.Text, capMail)}, Tok: estTokens(len(p.Text))}, ts, at, nil)
	case events.TypeMailDigest:
		var p struct {
			ID      string   `json:"id"`
			To      string   `json:"to"`
			Mailman string   `json:"mailman"`
			Parcels []string `json:"parcels"`
			Senders []string `json:"senders"`
		}
		if json.Unmarshal(e.Data, &p) != nil {
			return
		}
		d, ok := t.d.digests[p.ID]
		if !ok {
			break
		}
		delete(t.d.digests, p.ID)
		from := firstNonEmpty(first(p.Senders), first(d.origins), p.Mailman, d.from)
		n := max(len(p.Parcels), 1)
		t.put(&MailX{Mail: wire.Mail{From: uiID(from), To: uiID(firstNonEmpty(p.To, d.to)), Text: line(fmt.Sprintf("digest of %d: %s", n, d.text), capMail)},
			Tok: estTokens(len(d.text)), Digest: n}, ts, at, nil)
	case "mail.drop":
		var p struct {
			ID     string `json:"id"`
			Reason string `json:"reason"`
		}
		if json.Unmarshal(e.Data, &p) == nil {
			to := uiID(e.Agent)
			t.sysRow("mgr", "⚠", "mail "+p.ID+" was not delivered to "+firstNonEmpty(to, "its recipient")+": "+firstNonEmpty(p.Reason, "no reason given"), to, "", ts, at)
		}
	}
	if !t.d.history {
		t.syncMailStat(ts)
	}
}

// first is the first element of s, or "".
func first(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

// estTokens is the size of a text in tokens at the 3.6 bytes a token the harness's own estimator starts from.
func estTokens(n int) int { return (n*10 + 35) / 36 }

// syncMailStat sends the mail counts when they changed.
func (t *Translator) syncMailStat(ts float64) {
	m := t.st.Mail()
	ev := MailStat{Sent: m.Counts.Sent, Delivered: m.Counts.Delivered, Dropped: m.Counts.Ignored, Routed: m.Counts.Routed,
		Digests: m.Counts.Digests, Batches: m.Counts.Batches, Mailman: m.Mailman.State}
	if t.d.mstatOK && ev == t.d.mstat {
		return
	}
	if !t.d.mstatOK && ev == (MailStat{}) {
		return
	}
	t.d.mstat, t.d.mstatOK = ev, true
	t.put(&ev, ts, 0, nil)
}

// alertKey tells the board's alerts apart: kind and key, or kind and seq for an alert without a key.
func alertKey(a state.Alert) string {
	if a.Key != "" {
		return a.Key
	}
	return "#" + strconv.FormatUint(a.Seq, 10)
}

// syncAlerts sends the board's alerts raised and cleared since before (nil: every open alert is raised).
func (t *Translator) syncAlerts(before []state.Alert, ts float64) {
	after := t.st.Board().Alerts
	was := map[string]state.Alert{}
	for _, a := range before {
		was[a.Kind+"|"+alertKey(a)] = a
	}
	now := map[string]bool{}
	for _, a := range after {
		k := a.Kind + "|" + alertKey(a)
		now[k] = true
		if _, ok := was[k]; !ok {
			t.put(&Alert{S: "raise", Kind: line(a.Kind, capID), Key: line(alertKey(a), capID), Text: line(a.Text, capReason)}, ts, 0, nil)
		}
	}
	for _, a := range before {
		if k := a.Kind + "|" + alertKey(a); !now[k] {
			t.put(&Alert{S: "clear", Kind: line(a.Kind, capID), Key: line(alertKey(a), capID)}, ts, 0, nil)
		}
	}
}

// compact is compact.commit: an agent's thread folded, in tokens before and after.
func (t *Translator) compact(e events.Event, ts float64, at int64) {
	var p struct {
		RemovedTokens  int `json:"removed_tokens"`
		RetainedTokens int `json:"retained_tokens"`
		SnapTokens     int `json:"snap_tokens"`
		SpineAdded     int `json:"spine_added"`
	}
	if json.Unmarshal(e.Data, &p) != nil || nonAgent(e.Agent) {
		return
	}
	from := max(p.SnapTokens, 0)
	if from == 0 {
		from = max(p.RemovedTokens, 0) + max(p.RetainedTokens, 0)
	}
	to := max(p.SpineAdded, 0) + max(p.RetainedTokens, 0)
	pct := 0
	if from > 0 {
		pct = int(roundHalfAway(float64(to-from) / float64(from) * 100))
	}
	t.put(&wire.Compact{ID: uiID(e.Agent), From: from, To: to, Pct: pct}, ts, at, nil)
}

// roundHalfAway rounds to the nearest integer, halves away from zero.
func roundHalfAway(f float64) float64 {
	if f < 0 {
		return -float64(int64(-f + 0.5))
	}
	return float64(int64(f + 0.5))
}

// anomalyWhy is the one-line explanation of a cache anomaly's kind.
func anomalyWhy(kind string) string { return inspect.AnomalyTitle(kind) }

// goalKey is the identity of a goal event, for sending each state once whichever of the host and the log says it.
func goalKey(g *wire.Goal) string {
	return g.S + "\x00" + g.Objective + "\x00" + strconv.Itoa(g.Turns) + "\x00" + strconv.Itoa(g.Max) + "\x00" + g.Paused + "\x00" + g.Reason
}

// verdictKey is the identity of a verdict event.
func verdictKey(v *wire.Verdict) string {
	return v.Kind + "\x00" + v.Text + "\x00" + strings.Join(v.Left, "\x00")
}

// goalState translates goal.state into a goal event: the standing goal after a change (active, paused, met or cleared).
func (t *Translator) goalState(e events.Event, ts float64, at int64) {
	var p struct {
		Goal *struct {
			Objective string
			Turns     int
			Max       int
			Paused    string
			Reason    string
			Done      bool
		} `json:"goal"`
	}
	if json.Unmarshal(e.Data, &p) != nil {
		return
	}
	var g *wire.Goal
	if p.Goal == nil {
		gs := t.st.Goal()
		if gs == nil {
			return
		}
		g = &wire.Goal{S: "cleared", Objective: line(gs.Objective, capReason)}
	} else {
		s := "active"
		switch {
		case p.Goal.Done:
			s = "met"
		case p.Goal.Paused != "":
			s = "paused"
		}
		g = &wire.Goal{S: s, Objective: line(p.Goal.Objective, capReason), Turns: max(p.Goal.Turns, 0), Max: max(p.Goal.Max, 0),
			Paused: line(p.Goal.Paused, capReason), Reason: line(p.Goal.Reason, capReason)}
	}
	if k := goalKey(g); k != t.d.goalKey {
		t.d.goalKey = k
		t.put(g, ts, at, nil)
	}
}

// goalJudge translates goal.judge into a verdict event: the judge's verdict on a turn.
func (t *Translator) goalJudge(e events.Event, ts float64, at int64) {
	var p struct {
		Verdict string   `json:"verdict"`
		Reason  string   `json:"reason"`
		Left    []string `json:"left"`
	}
	if json.Unmarshal(e.Data, &p) != nil {
		return
	}
	v := verdictOf(p.Verdict, p.Reason, p.Left)
	if k := verdictKey(v); k != t.d.verdKey {
		t.d.verdKey = k
		t.put(v, ts, at, nil)
	}
}

// verdictOf is the verdict event of a judge's verdict: "not yet: ", "met: " or "blocked: " and the reason, its kind and what is left.
func verdictOf(kind, reason string, left []string) *wire.Verdict {
	head := "not yet: "
	switch kind {
	case "done":
		head = "met: "
	case "blocked":
		head = "blocked: "
	default:
		kind = "continue"
	}
	v := &wire.Verdict{Text: line(head+reason, capReason), Kind: kind}
	for i, l := range left {
		if i >= 8 {
			break
		}
		v.Left = append(v.Left, line(l, capNote))
	}
	return v
}

// ckptOut is a checkpoint as last sent, and an update held back by the debounce.
type ckptOut struct {
	lastT float64
	pend  *wire.Ckpt
	at    int64
}

// ckptGap is the debounce of a checkpoint's updates, in seconds.
const ckptGap = 1.0

// cidOf is the id of a checkpoint as the page shows it: c and the store's number, at least two digits (cp_0007 is c07).
func cidOf(id string) string {
	num, ok := strings.CutPrefix(id, "cp_")
	if !ok {
		return line(id, capID)
	}
	n, err := strconv.Atoi(num)
	if err != nil || n < 0 {
		return line(id, capID)
	}
	return fmt.Sprintf("c%02d", n)
}

// checkpoint is the checkpoint event: a checkpoint began (no file yet) or its file set changed. The first event of a checkpoint is
// sent at once; its updates at most once a second.
func (t *Translator) checkpoint(e events.Event, ts float64, at int64) {
	var p struct {
		ID      string   `json:"id"`
		Label   string   `json:"label"`
		Files   []string `json:"files"` // logs that listed every file
		Count   int      `json:"count"` // logs that count them
		Agents  []string `json:"agents"`
		Time    string   `json:"time"`
		Safety  bool     `json:"safety"`
		Added   int      `json:"added"`
		Removed int      `json:"removed"`
	}
	if json.Unmarshal(e.Data, &p) != nil || p.ID == "" {
		return
	}
	tm := e.TS
	if v, err := time.Parse(time.RFC3339Nano, p.Time); err == nil {
		tm = v
	}
	cid := cidOf(p.ID)
	n := max(len(p.Files), p.Count, 0)
	ev := &wire.Ckpt{CID: cid, TS: tm.Local().Format("15:04:05"), Files: n, Note: line(p.Label, capNote), Skipped: n == 0,
		Safety: p.Safety, Add: max(p.Added, 0), Del: max(p.Removed, 0)}
	for _, a := range p.Agents {
		if len(ev.Agents) < 16 {
			ev.Agents = append(ev.Agents, uiID(line(a, capID)))
		}
	}
	atMs := tm.UnixMilli()
	c := t.d.ckpts[cid]
	switch {
	case c == nil:
		if len(t.d.ckpts) >= 1024 {
			t.d.ckpts = map[string]*ckptOut{}
		}
		t.d.ckpts[cid] = &ckptOut{lastT: ts}
		t.put(ev, ts, atMs, nil)
	case ts-c.lastT >= ckptGap && !t.d.history:
		c.lastT, c.pend = ts, nil
		t.put(ev, ts, atMs, nil)
	default:
		c.pend, c.at = ev, atMs
	}
	_ = at
}

// flushCkpts sends the held-back checkpoint updates whose debounce passed (all of them, with all).
func (t *Translator) flushCkpts(ts float64, all bool) {
	for _, cid := range sortedKeys(t.d.ckpts) {
		c := t.d.ckpts[cid]
		if c.pend != nil && (all || ts-c.lastT >= ckptGap) {
			ev := c.pend
			c.pend, c.lastT = nil, ts
			t.put(ev, ts, c.at, nil)
		}
	}
}
