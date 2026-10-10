package translate

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// response is model.response: for a main request the req row of the agent's hit ratio (VOCAB.md 5.10), its prompt by layer and the
// warm clock; for every request its token table.
func (t *Translator) response(e events.Event, ts float64, at int64) {
	var p struct {
		Req   string `json:"req"`
		Side  bool   `json:"side"`
		Usage struct {
			Input   int64 `json:"input_tokens"`
			Read    int64 `json:"cache_read_tokens"`
			Write5m int64 `json:"cache_write_5m_tokens"`
			Write1h int64 `json:"cache_write_1h_tokens"`
			Output  int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(e.Data, &p) != nil || nonAgent(e.Agent) {
		return
	}
	hid := e.Agent
	key := hid + "|" + p.Req
	side := p.Side || t.d.reqSide[key]
	delete(t.d.reqSide, key)
	if t.d.history || t.agentOutOf(uiID(hid)).service {
		return
	}
	if !side {
		c := func(v int64) int64 { return max(v, 0) }
		prompt := c(p.Usage.Input) + c(p.Usage.Read) + c(p.Usage.Write5m) + c(p.Usage.Write1h)
		ratio := 0.0
		if prompt > 0 {
			ratio = float64(c(p.Usage.Read)) / float64(prompt)
		}
		t.put(&wire.Req{ID: uiID(hid), Ratio: ratio, P: prompt, O: c(p.Usage.Output)}, ts, 0, nil)
	}
	t.sendUse(hid, ts, false)
	if !side {
		t.sendLayers(hid, ts, false)
		t.sendWarm(hid)
	}
}

// sendWarm sends the warm clock when the shared prefix the agent rides (the main agent's own prompt when it rides none) was read
// or written later than the clock last said (VOCAB.md 5.12).
func (t *Translator) sendWarm(hid string) {
	e, ok := t.st.PrefixTouch(hid)
	if !ok || (e.Kind != "prefix" && !isMain(hid)) || !e.Last.After(t.d.warm) {
		return
	}
	t.d.warm = e.Last
	t.put(&wire.Warm{TTL: e.TTLSeconds}, t.sessT(e.Last), 0, nil)
}

// modelError is model.error: a retry moves the governor's counters at once; a failure is remembered for the agent's failed state.
func (t *Translator) modelError(e events.Event, ts float64) {
	var p struct {
		Error   string `json:"error"`
		Attempt int    `json:"attempt"`
		DelayMs int64  `json:"delay_ms"`
	}
	if json.Unmarshal(e.Data, &p) != nil {
		return
	}
	if p.Attempt > 0 || p.DelayMs > 0 {
		if !t.d.history {
			t.checkGov(e.TS, ts)
		}
		return
	}
	if p.Error != "" && !nonAgent(e.Agent) {
		t.d.lastErr[uiID(e.Agent)] = line(p.Error, 100)
	}
}

// govOut is the governor's gauge as last sent.
type govOut struct {
	last  wire.Gov
	lastT float64
	sent  bool
}

// govGap is the least time between two gov events while only the request rate changed.
const govGap = 5.0

// checkGov sends the governor's gauge (VOCAB.md 5.13): at once when the rate limits or the retries changed, else at most every
// govGap seconds while anything changed.
func (t *Translator) checkGov(now time.Time, ts float64) {
	g := t.st.GovernorAt(now)
	ev := wire.Gov{RPM: g.RPM, R429: g.RateLimited, Retries: g.Retries, Inflight: g.Inflight, Queued: g.Queued}
	o := &t.d.gov
	if o.sent && ev == o.last {
		return
	}
	if !o.sent && ev == (wire.Gov{}) {
		return
	}
	urgent := !o.sent || ev.R429 != o.last.R429 || ev.Retries != o.last.Retries
	if !urgent && ts-o.lastT < govGap {
		return
	}
	o.last, o.lastT, o.sent = ev, ts, true
	c := ev
	t.put(&c, ts, 0, nil)
}

// tick is the translator's clock: the streamed text that waited 100 ms, the held-back states and checkpoint updates, the notice
// gate's count, the governor, the holds of the sink that the log did not confirm, the waiting log-only tool rows and, once a second,
// a pass over every agent (what changes with no event of its own, such as the manager waiting for its team).
func (t *Translator) tick(now time.Time) {
	ts := t.sessT(now)
	t.flushMessages(ts)
	t.flushStates(ts)
	t.flushCkpts(ts, false)
	if g := &t.d.gate; g.skipped > 0 && ts-g.start >= gateWindow {
		t.gateSummary(ts)
		g.start, g.n = ts, 0
	}
	t.checkGov(now, ts)
	t.expireTwins(ts)
	if t.d.logOnly {
		t.flushPend(ts, false)
	}
	for _, uid := range sortedKeys(t.d.hold) {
		if h := t.d.hold[uid]; ts-h.since >= 2 {
			delete(t.d.hold, uid)
			t.refreshAgent(t.harnessID(uid), ts, false)
		}
	}
	if ts-t.d.full >= 1 {
		t.d.full = ts
		for _, hid := range t.st.AgentIDs() {
			if !nonAgent(hid) {
				t.refreshAgent(hid, ts, false)
			}
		}
	}
	t.publishRoster()
}

// hostEvent journals an event of the host (VOCAB.md 4, source H): the person's messages and actions, the goal loop, turns. It ends
// the manager's open message at a turn's end, sends a goal or verdict state once whichever of the host and the log says it, and
// cleans the text it carries.
func (t *Translator) hostEvent(e wire.Event, ts float64) {
	switch v := e.(type) {
	case *wire.Turn:
		if v.S == "end" {
			t.endMsg("mgr", ts, false)
		}
		if v.S == "start" {
			t.d.turnOpen = true
		}
	case *wire.Final:
		t.endMsg("mgr", ts, false)
	case *wire.Goal:
		v.Objective, v.Paused, v.Reason = line(v.Objective, capReason), line(v.Paused, capReason), line(v.Reason, capReason)
		k := goalKey(v)
		if k == t.d.goalKey {
			return
		}
		t.d.goalKey = k
	case *wire.Verdict:
		v.Text = line(v.Text, capReason)
		for i := range v.Left {
			v.Left[i] = line(v.Left[i], capNote)
		}
		if len(v.Left) > 8 {
			v.Left = v.Left[:8]
		}
		k := verdictKey(v)
		if k == t.d.verdKey {
			return
		}
		t.d.verdKey = k
	case *wire.Say:
		v.Text = text(v.Text, capEvent)
	case *wire.Sys:
		v.Text = line(v.Text, capReason)
		v.Ch, v.Ag = uiIDOr(v.Ch, "mgr"), uiIDOr(v.Ag, "")
	case *wire.Steer:
		v.To, v.Text = uiIDOr(v.To, "mgr"), line(v.Text, capReason)
	case *wire.Interrupt:
		if v.ID != "turn" {
			v.ID = uiID(v.ID)
		}
	case *wire.State:
		t.hostState(&StateX{State: *v}, ts)
		return
	case *StateX:
		t.hostState(v, ts)
		return
	case *wire.Ask:
		t.question(v.Q, t.now())
		return
	case *wire.Answer:
		t.answered(*v, t.now())
		return
	case *wire.Ckpt:
		if c := t.d.ckpts[v.CID]; c == nil {
			t.d.ckpts[v.CID] = &ckptOut{lastT: ts}
		} else {
			c.lastT, c.pend = ts, nil
		}
	}
	t.put(e, ts, 0, nil)
}

// hostState sends a state the host set (an interrupted agent): it becomes the agent's last state, so that the log's view of it
// replaces it only when that differs.
func (t *Translator) hostState(s *StateX, ts float64) {
	s.ID = uiID(s.ID)
	s.Doing = line(s.Doing, capDoing)
	a := t.agentOutOf(s.ID)
	t.ensureRoster(s.ID)
	t.publishRoster()
	t.sendState(s.ID, a, s, ts)
}

// uiIDOr is uiID of id, or dflt for an empty id.
func uiIDOr(id, dflt string) string {
	if strings.TrimSpace(id) == "" {
		return dflt
	}
	return uiID(id)
}
