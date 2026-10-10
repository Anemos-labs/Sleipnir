package translate

import (
	"encoding/json"
	"errors"
	"sort"

	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// The mirror is a Go reduction of what the page's world model holds, minus the transcript rows: per agent its state,
// token table, ratio history and prompt layers; the tasks and the merged list; the plan, the goal and the verdict; the checkpoints;
// the open questions; the last mails, anomalies and compactions; the warm clock, the governor, the merge queue; the board's alerts,
// the open stall findings, the mail counts and what the harness's service agents used. The journal folds into one mirror every
// event it evicts, and the keyframe of a snapshot is that mirror written back as events: a page that reduces keyframe ⧺ retained
// events ends where one that reduced every event would, for everything but the transcript.

// The caps of the mirror.
const (
	mirrorRatios = 256 // ratio history per agent (state.HistCap)
	mirrorMails  = 400 // the page keeps 400 mails
	mirrorAnoms  = 100 // anomalies and compactions kept
	mirrorCkpts  = 256 // checkpoints kept
	mirrorAlerts = 64  // open alerts kept
	mirrorStalls = 64  // open stall findings kept
	mirrorAgents = 512 // agents kept
	mirrorTasks  = 2000
)

// mAgent is one agent of the mirror.
type mAgent struct {
	state   *StateX
	use     *UseX
	layers  *wire.Layers
	ratios  []float64
	marks   []string // the mark of each request in ratios ("" for none)
	lastReq float64
}

// mirror is the reduction. The zero value is not usable: make one with newMirror.
type mirror struct {
	agents   map[string]*mAgent
	aorder   []string
	tasks    map[string]*TaskX
	torder   []string
	merged   []string
	plan     *wire.Plan
	goal     *wire.Goal
	verdict  *wire.Verdict
	ckpts    map[string]*wire.Ckpt
	corder   []string
	open     map[string]*wire.Ask
	qorder   []string
	mails    []*MailX
	anoms    []*wire.Break
	comps    []*CompactX
	warm     *wire.Warm
	gov      *wire.Gov
	queue    *wire.Queue
	alerts   map[string]*Alert
	alorder  []string
	stalls   map[string]*wire.Stall
	sorder   []string
	mailstat *MailStat
	svc      *SvcUse
	t        float64 // the t of the newest event folded
	n        uint64  // events folded
}

// newMirror returns an empty mirror.
func newMirror() *mirror {
	return &mirror{agents: map[string]*mAgent{}, tasks: map[string]*TaskX{}, ckpts: map[string]*wire.Ckpt{}, open: map[string]*wire.Ask{},
		alerts: map[string]*Alert{}, stalls: map[string]*wire.Stall{}}
}

// agent returns the mirror's agent with the id, creating it (nil when the mirror holds mirrorAgents already).
func (m *mirror) agent(id string) *mAgent {
	if a := m.agents[id]; a != nil {
		return a
	}
	if len(m.agents) >= mirrorAgents || id == "" {
		return nil
	}
	a := &mAgent{}
	m.agents[id] = a
	m.aorder = append(m.aorder, id)
	return a
}

// fold applies one event of the vocabulary to the mirror.
func (m *mirror) fold(e wire.Event) {
	h := baseOf(e)
	if h == nil {
		return
	}
	m.n++
	if h.T > m.t {
		m.t = h.T
	}
	switch v := e.(type) {
	case *StateX:
		if a := m.agent(v.ID); a != nil {
			c := *v
			a.state = &c
		}
	case *wire.State:
		if a := m.agent(v.ID); a != nil {
			a.state = &StateX{State: *v}
		}
	case *UseX:
		if a := m.agent(v.ID); a != nil {
			c := *v
			a.use = &c
		}
	case *wire.Use:
		if a := m.agent(v.ID); a != nil {
			a.use = &UseX{Use: *v}
		}
	case *wire.Layers:
		if a := m.agent(v.ID); a != nil {
			c := *v
			a.layers = &c
		}
	case *ReqX:
		m.request(&v.Req, v.Mark)
	case *wire.Req:
		m.request(v, "")
	case *TaskX:
		m.task(v)
	case *wire.Task:
		m.task(&TaskX{Task: *v})
	case *wire.Merge:
		if t := m.tasks[v.ID]; t != nil {
			t.S = "merged"
			m.markMerged(v.ID)
		}
	case *wire.Plan:
		if v.Steps != nil {
			c := *v
			c.Steps, c.St = append([]string(nil), v.Steps...), append([]string(nil), v.St...)
			m.plan = &c
		}
	case *wire.Goal:
		c := *v
		m.goal = &c
	case *wire.Verdict:
		c := *v
		c.Left = append([]string(nil), v.Left...)
		m.verdict = &c
	case *wire.Ckpt:
		if _, ok := m.ckpts[v.CID]; !ok {
			if len(m.corder) >= mirrorCkpts {
				delete(m.ckpts, m.corder[0])
				m.corder = m.corder[1:]
			}
			m.corder = append(m.corder, v.CID)
		}
		c := *v
		c.Agents = append([]string(nil), v.Agents...)
		m.ckpts[v.CID] = &c
	case *wire.Ask:
		if _, ok := m.open[v.Q.ID]; !ok {
			m.qorder = append(m.qorder, v.Q.ID)
		}
		c := *v
		m.open[v.Q.ID] = &c
	case *wire.Answer:
		if _, ok := m.open[v.QID]; ok {
			delete(m.open, v.QID)
			m.qorder = remove(m.qorder, v.QID)
		}
	case *MailX:
		c := *v
		m.mails = appendCapped(m.mails, &c, mirrorMails)
	case *wire.Mail:
		m.mails = appendCapped(m.mails, &MailX{Mail: *v}, mirrorMails)
	case *wire.Break:
		c := *v
		m.anoms = appendCapped(m.anoms, &c, mirrorAnoms)
	case *CompactX:
		c := *v
		m.comps = appendCapped(m.comps, &c, mirrorAnoms)
	case *wire.Compact:
		m.comps = appendCapped(m.comps, &CompactX{Compact: *v}, mirrorAnoms)
	case *wire.Warm:
		c := *v
		m.warm = &c
	case *wire.Gov:
		c := *v
		m.gov = &c
	case *wire.Queue:
		c := *v
		m.queue = &c
	case *MailStat:
		c := *v
		m.mailstat = &c
	case *SvcUse:
		c := *v
		m.svc = &c
	case *Alert:
		k := v.Kind + "|" + v.Key
		if v.S == "clear" {
			if _, ok := m.alerts[k]; ok {
				delete(m.alerts, k)
				m.alorder = remove(m.alorder, k)
			}
			return
		}
		if _, ok := m.alerts[k]; !ok {
			if len(m.alorder) >= mirrorAlerts {
				delete(m.alerts, m.alorder[0])
				m.alorder = m.alorder[1:]
			}
			m.alorder = append(m.alorder, k)
		}
		c := *v
		m.alerts[k] = &c
	case *wire.Stall:
		k := v.ID + "|" + v.Kind + "|" + v.Task
		if v.S == "clear" {
			if _, ok := m.stalls[k]; ok {
				delete(m.stalls, k)
				m.sorder = remove(m.sorder, k)
			}
			return
		}
		if _, ok := m.stalls[k]; !ok {
			if len(m.sorder) >= mirrorStalls {
				delete(m.stalls, m.sorder[0])
				m.sorder = m.sorder[1:]
			}
			m.sorder = append(m.sorder, k)
		}
		c := *v
		m.stalls[k] = &c
	}
}

// request folds a req event: the ratio of an answered request of the agent, with its mark.
func (m *mirror) request(v *wire.Req, mark string) {
	if a := m.agent(v.ID); a != nil {
		a.ratios = appendCapped(a.ratios, v.Ratio, mirrorRatios)
		a.marks = appendCapped(a.marks, mark, mirrorRatios)
		a.lastReq = v.T
	}
}

// task folds a task event: the first creates the task, later ones change what they carry.
func (m *mirror) task(v *TaskX) {
	t := m.tasks[v.ID]
	if t == nil {
		if len(m.tasks) >= mirrorTasks || v.ID == "" {
			return
		}
		c := *v
		c.Deps = append([]string(nil), v.Deps...)
		m.tasks[v.ID] = &c
		m.torder = append(m.torder, v.ID)
		t = &c
	} else {
		if v.Title != "" {
			t.Title = v.Title
		}
		if v.Owner != "" {
			t.Owner = v.Owner
		}
		if v.Deps != nil {
			t.Deps = append([]string(nil), v.Deps...)
		}
		if v.Scope != "" {
			t.Scope = v.Scope
		}
		if v.S != "" {
			t.S = v.S
		}
		t.Closure, t.Failed, t.Blocked = v.Closure, v.Failed, v.Blocked
		if v.Attempts != 0 {
			t.Attempts = v.Attempts
		}
	}
	if t.S == "merged" {
		m.markMerged(v.ID)
	}
}

// markMerged adds a task to the merged list once.
func (m *mirror) markMerged(id string) {
	for _, x := range m.merged {
		if x == id {
			return
		}
	}
	m.merged = append(m.merged, id)
}

// keyframe writes the mirror back as events (seq 0): the dated ones (open questions, mails, anomalies, compactions, checkpoints)
// at their own t, oldest first, then everything else at t0, which is not before any of them.
func (m *mirror) keyframe(t0 float64) []wire.Event {
	var dated []wire.Event
	for _, id := range m.qorder {
		c := *m.open[id]
		dated = append(dated, &c)
	}
	for _, v := range lastN(m.mails, mirrorMails) {
		c := *v
		dated = append(dated, &c)
	}
	for _, v := range lastN(m.anoms, mirrorAnoms) {
		c := *v
		dated = append(dated, &c)
	}
	for _, v := range lastN(m.comps, mirrorAnoms) {
		c := *v
		dated = append(dated, &c)
	}
	for _, id := range m.corder {
		c := *m.ckpts[id]
		dated = append(dated, &c)
	}
	for _, e := range dated {
		h := baseOf(e)
		h.Seq = 0
		if h.T > t0 {
			t0 = h.T
		}
	}
	sort.SliceStable(dated, func(i, j int) bool { return baseOf(dated[i]).T < baseOf(dated[j]).T })
	out := dated
	at := func(e wire.Event) {
		h := baseOf(e)
		h.T, h.Seq, h.K = t0, 0, kindOf(e)
		out = append(out, e)
	}
	for _, id := range m.aorder {
		a := m.agents[id]
		if a.use != nil {
			c := *a.use
			at(&c)
		}
		if a.layers != nil {
			c := *a.layers
			at(&c)
		}
		marks := lastN(a.marks, mirrorRatios)
		for i, r := range lastN(a.ratios, mirrorRatios) {
			at(&ReqX{Req: wire.Req{ID: id, Ratio: r, Hist: true}, Mark: marks[i]})
		}
		if a.state != nil {
			c := *a.state
			at(&c)
		}
	}
	merged := map[string]bool{}
	for _, id := range m.merged {
		merged[id] = true
	}
	for _, id := range m.torder {
		c := *m.tasks[id]
		c.Deps = append([]string(nil), c.Deps...)
		if merged[id] {
			c.S = "" // created here; merged below, in the order the tasks were merged
		}
		at(&c)
	}
	for _, id := range m.merged {
		if t, ok := m.tasks[id]; ok {
			c := *t
			c.Deps = append([]string(nil), c.Deps...)
			c.S = "merged"
			at(&c)
			if t.S != "merged" { // merged once, and back at work since
				d := *t
				d.Deps = append([]string(nil), d.Deps...)
				at(&d)
			}
		}
	}
	if m.plan != nil {
		c := *m.plan
		at(&c)
	}
	if m.goal != nil {
		c := *m.goal
		at(&c)
	}
	if m.verdict != nil {
		c := *m.verdict
		at(&c)
	}
	if m.warm != nil {
		c := *m.warm
		at(&c)
	}
	if m.gov != nil {
		c := *m.gov
		at(&c)
	}
	if m.queue != nil {
		c := *m.queue
		at(&c)
	}
	if m.mailstat != nil {
		c := *m.mailstat
		at(&c)
	}
	if m.svc != nil {
		c := *m.svc
		at(&c)
	}
	for _, k := range m.alorder {
		c := *m.alerts[k]
		at(&c)
	}
	for _, k := range m.sorder {
		c := *m.stalls[k]
		at(&c)
	}
	return out
}

// errUnknownKind is a journal entry of a kind the mirror cannot decode.
var errUnknownKind = errors.New("translate: unknown event kind")

// decodeEvent reads one encoded event of the vocabulary back into its type.
func decodeEvent(raw []byte) (wire.Event, error) {
	var h struct {
		K string `json:"k"`
	}
	if err := json.Unmarshal(raw, &h); err != nil {
		return nil, err
	}
	e := newOfKind(h.K)
	if e == nil {
		return nil, errUnknownKind
	}
	if err := json.Unmarshal(raw, e); err != nil {
		return nil, err
	}
	return e, nil
}

// newOfKind is an empty event of the kind: the extension type where there is one, so that nothing the journal wrote is lost.
func newOfKind(k string) wire.Event {
	switch k {
	case "say":
		return &wire.Say{}
	case "sys":
		return &wire.Sys{}
	case "tool":
		return &ToolX{}
	case "note":
		return &wire.Note{}
	case "state":
		return &StateX{}
	case "task":
		return &TaskX{}
	case "plan":
		return &wire.Plan{}
	case "verdict":
		return &wire.Verdict{}
	case "req":
		return &ReqX{}
	case "use":
		return &UseX{}
	case "warm":
		return &wire.Warm{}
	case "gov":
		return &wire.Gov{}
	case "mail":
		return &MailX{}
	case "ckpt":
		return &wire.Ckpt{}
	case "ask":
		return &wire.Ask{}
	case "answer":
		return &wire.Answer{}
	case "queue":
		return &wire.Queue{}
	case "merge":
		return &wire.Merge{}
	case "break":
		return &wire.Break{}
	case "compact":
		return &CompactX{}
	case "stream":
		return &wire.Stream{}
	case "diff":
		return &wire.Diff{}
	case "goal":
		return &wire.Goal{}
	case "final":
		return &wire.Final{}
	case "steer":
		return &wire.Steer{}
	case "interrupt":
		return &wire.Interrupt{}
	case "refuse":
		return &wire.Refuse{}
	case "more":
		return &wire.More{}
	case "turn":
		return &wire.Turn{}
	case "stall":
		return &wire.Stall{}
	case "handover":
		return &wire.Handover{}
	case "layers":
		return &wire.Layers{}
	case "alert":
		return &Alert{}
	case "mailstat":
		return &MailStat{}
	case "svc":
		return &SvcUse{}
	}
	return nil
}

// appendCapped appends v and keeps at least the newest n (at most 2n: the slice is trimmed to n when it reaches 2n, so that an append
// costs constant time on average). Readers take lastN.
func appendCapped[T any](s []T, v T, n int) []T {
	s = append(s, v)
	if len(s) >= 2*n {
		s = append(s[:0], s[len(s)-n:]...)
	}
	return s
}

// lastN is the newest n elements of s.
func lastN[T any](s []T, n int) []T {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

// remove takes the first x out of s.
func remove(s []string, x string) []string {
	for i, v := range s {
		if v == x {
			return append(s[:i], s[i+1:]...)
		}
	}
	return s
}
