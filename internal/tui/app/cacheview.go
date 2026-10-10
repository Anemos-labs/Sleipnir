package app

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
	"github.com/anemos-labs/sleipnir/internal/tui/state"
	"github.com/anemos-labs/sleipnir/internal/tui/widget"
)

// The animations of the cache view, in frames (UIFPS a second).
const (
	sweepFrames = 10 // the light that runs along the prompt when an answer says how much of it was read
	foldFrames  = 24 // the thread folding into its resume
	flashFrames = 18 // a new anomaly is bold
)

// pick is the agent a view is about: the one named, else the first that is not a service of the harness.
func pick(sn *state.Snapshot, id string) *state.Agent {
	var first *state.Agent
	for i := range sn.Agents {
		a := &sn.Agents[i]
		if id != "" && a.ID == id {
			return a
		}
		if first == nil && !a.Service {
			first = a
		}
	}
	if first == nil && len(sn.Agents) > 0 {
		first = &sn.Agents[0]
	}
	return first
}

// Newest is the agent whose latest answer is the newest: the one the cache view follows when nobody has chosen another.
func Newest(sn *state.Snapshot) string {
	if sn == nil {
		return ""
	}
	id, seq := "", uint64(0)
	for _, a := range sn.Agents {
		if !a.Service && a.Stack.RespSeq > seq {
			id, seq = a.ID, a.Stack.RespSeq
		}
	}
	return id
}

// Agents are the ids the views can be about, in the order the program steps through them.
func Agents(sn *state.Snapshot) []string {
	var ids []string
	if sn == nil {
		return nil
	}
	for _, a := range sn.Agents {
		if !a.Service {
			ids = append(ids, a.ID)
		}
	}
	return ids
}

// cacheView is the screen that shows what makes this harness different: what the prompt of one agent is made of, how much of it the
// provider had kept, how long the cache will last, what happened to the hit ratio over the session, the compactions and the misses.
func cacheView(s Scene) []cell.Line {
	sn := s.Snap
	a := pick(sn, s.Agent)
	if a == nil {
		return frame(s, "cache", []widget.ViewBand{{Draw: func(w []int) [][]cell.Line {
			return [][]cell.Line{{cell.Styled(stylesOf(s.Pal).dim, "no agent has started yet")}}
		}}})
	}
	title := "cache · " + clean(a.ID)
	bands := []widget.ViewBand{
		{Draw: func(w []int) [][]cell.Line { return [][]cell.Line{cacheHeadline(s, a, w[0])} }},
		{Titles: []string{"its latest prompt"}, Draw: func(w []int) [][]cell.Line { return [][]cell.Line{promptPanel(s, a, w[0])} }},
		{Titles: []string{"layer by layer"}, Prio: 6, Draw: func(w []int) [][]cell.Line { return [][]cell.Line{layerTable(s, a, w[0])} }},
		{Titles: []string{"hit ratio per request", "its prefix"}, Weights: []int{3, 2}, Prio: 1,
			Draw: func(w []int) [][]cell.Line { return [][]cell.Line{sparkPanel(s, a, w[0]), prefixPanel(s, a, w[1])} }},
	}
	busy := len(a.Compacts) > 0 || len(a.Anomalies) > 0
	prio := 5
	if busy {
		prio = 2
	}
	bands = append(bands, widget.ViewBand{Titles: []string{"compactions", "cache anomalies"}, Weights: []int{3, 2}, Prio: prio,
		Draw: func(w []int) [][]cell.Line { return [][]cell.Line{compactPanel(s, a, w[0]), anomalyPanel(s, a, w[1])} }})
	if len(Agents(sn)) > 1 {
		bands = append(bands, widget.ViewBand{Titles: []string{"every agent's cache"}, Prio: 3, MinRows: 4,
			Draw: func(w []int) [][]cell.Line { return [][]cell.Line{agentsPanel(s, a, w[0], 0)} },
			Fit:  func(w []int, rows int) [][]cell.Line { return [][]cell.Line{agentsPanel(s, a, w[0], rows)} }})
	}
	bands = append(bands, widget.ViewBand{Titles: []string{"the session"}, Prio: 4,
		Draw: func(w []int) [][]cell.Line { return [][]cell.Line{sessionPanel(s, w[0])} }})
	return frame(s, title, bands)
}

// cacheHeadline says whose cache this is and what its latest answer said.
func cacheHeadline(s Scene, a *state.Agent, w int) []cell.Line {
	st := stylesOf(s.Pal)
	var l1 row
	l1.add(roleStyle(s.Pal, a.Role), clean(a.ID))
	if a.Role != "" {
		l1.add(st.dim, " "+clean(a.Role))
	}
	if m := clean(modelOf(a, s.Snap)); m != "" {
		l1.add(st.dim, " · ").add(cell.Style{}, m)
	}
	l1.add(st.dim, fmt.Sprintf(" · %d %s", a.Requests, plural(a.Requests, "request", "requests")))
	if a.Tokens.Prompt() > 0 {
		l1.add(st.dim, " · ").add(hitStyle(st, a.Tokens.HitRatio()), widget.Percent(a.Tokens.HitRatio())).add(st.dim, " of its prompt from the cache")
	}
	var l2 row
	stk := a.Stack
	switch {
	case stk.RespSeq == 0:
		l2.add(st.dim, "no answer yet: what the cache did is known when the provider says")
	default:
		l2.add(st.dim, "latest answer: ").add(cell.Style{}, widget.Tokens(stk.Prompt)).add(st.dim, " prompt · ").
			add(hitStyle(st, hitOf(stk)), widget.Tokens(stk.Read)).add(st.dim, " from the cache · ")
		if stk.Write > 0 {
			l2.add(st.warn, widget.Tokens(stk.Write)).add(st.dim, " written · ")
		}
		l2.add(cell.Style{}, widget.Tokens(stk.Fresh)).add(st.dim, " paid in full")
		if a.SavedUSD > 0 {
			l2.add(st.dim, " · saved ").add(st.good, widget.USD(a.SavedUSD))
		}
	}
	return []cell.Line{fit(l1.line(), w), fit(l2.line(), w)}
}

// modelOf is the model an agent runs, from the agent, else from its latest request.
func modelOf(a *state.Agent, sn *state.Snapshot) string {
	if a.Model != "" {
		return a.Model
	}
	if a.Stack.Model != "" {
		return a.Stack.Model
	}
	return sn.Session.Model
}

// hitOf is the share of the latest answer's prompt that was read from the cache.
func hitOf(stk state.Stack) float64 {
	if stk.Prompt <= 0 {
		return 0
	}
	return float64(stk.Read) / float64(stk.Prompt)
}

// plural selects the singular display label only for one item.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// roleStyle selects a role color cyclically from the palette and applies bold; RoleColors must be
// nonempty.
func roleStyle(p widget.Palette, role string) cell.Style {
	i := roleColor(role)
	return cell.Style{FG: p.RoleColors[i%len(p.RoleColors)], Attr: cell.Bold}
}

// hitStyle colours a hit ratio the way the sparkline does: green from 85 percent, amber from 50, red below.
func hitStyle(st styles, r float64) cell.Style {
	switch {
	case r >= 0.85:
		return st.good
	case r >= 0.5:
		return st.warn
	}
	return st.bad
}

// layerRank orders the sections of a request the way the prompt is laid out, stable layers first.
var layerNames = map[string]string{"shared": "G1 shared", "role": "G2 role", "notes": "G3 notes", "spine": "G4 spine"}

// promptLayers are the layers of an agent's latest prompt for the stack bar. The request sizes G1 to G4 (the sections); what it
// does not size, the constitution and the tools, the thread and the hot tail, is left as one amount, which is split into G0 by
// the estimate the memory holds and G5 for the rest.
func promptLayers(a *state.Agent, g0 int) []widget.Layer {
	stk := a.Stack
	if len(stk.Sections) == 0 && stk.Unsectioned == 0 {
		return nil
	}
	marked := map[string]bool{} // the layers a cache marker follows, as the planner labelled them
	for _, bp := range stk.Breakpoints {
		marked[bp.Label] = true
	}
	g0 = state.LayerSplit(*a, g0)[0] // the constitution estimate, bounded by what the request did not size
	layers := []widget.Layer{{Name: "G0 const", Tokens: g0, Breakpoint: marked["const"]}}
	secs := append([]state.Section(nil), stk.Sections...)
	rank := func(n string) int {
		switch n {
		case "shared":
			return 1
		case "role":
			return 2
		case "notes":
			return 3
		case "spine":
			return 4
		}
		return 5
	}
	sort.SliceStable(secs, func(i, j int) bool { return rank(secs[i].Name) < rank(secs[j].Name) })
	for _, sec := range secs {
		name := layerNames[sec.Name]
		if name == "" {
			name = "G? " + clean(sec.Name)
		}
		layers = append(layers, widget.Layer{Name: name, Tokens: sec.Tokens, Breakpoint: sec.Breakpoint || marked[sec.Name], Hash: sec.Hash})
	}
	layers = append(layers, widget.Layer{Name: "G5 thread", Tokens: stk.Unsectioned - g0, Breakpoint: marked["thread"]})
	kept := layers[:0]
	for _, l := range layers {
		if l.Tokens > 0 { // a layer with nothing in it is not a layer of this prompt
			kept = append(kept, l)
		}
	}
	return kept
}

// breakLayer is the index in layers of the layer where the cache broke in the latest answer, or -1: the layer a cache anomaly of the
// latest request says diverged, else (for a miss, where the provider read less than the harness expected) the layer in which the read
// prefix ended, which is where the prompt stopped matching what the provider had kept.
func breakLayer(a *state.Agent, layers []widget.Layer) int {
	for i := len(a.Anomalies) - 1; i >= 0; i-- {
		an := a.Anomalies[i]
		if an.At < a.Hits.Len()-1 {
			break // an anomaly of an earlier request: the bar shows the latest one
		}
		name := strings.ToLower(strings.TrimSpace(an.Layer))
		for j, l := range layers {
			f := strings.Fields(strings.ToLower(l.Name))
			if len(f) == 0 {
				continue
			}
			if name != "" && (name == f[0] || (len(f) > 1 && name == f[1]) || name == "g"+fmt.Sprint(j)) {
				return j
			}
		}
		if name == "tools" || name == "system" || name == "const" || name == "constitution" {
			return 0
		}
	}
	if a.Stack.Miss && a.Stack.RespSeq != 0 {
		read := a.Stack.Read
		for j, l := range layers {
			if read < l.Tokens {
				return j
			}
			read -= l.Tokens
		}
	}
	return -1
}

// ttlOf is the clock of a cache entry of the snapshot: how long it has left at the snapshot's moment and its lifetime.
func ttlOf(sn *state.Snapshot, kind, key string) (left, total time.Duration, ok bool) {
	for _, e := range sn.TTL {
		if e.Kind == kind && e.Key == key {
			return e.Remaining(sn.Now), time.Duration(e.TTLSeconds) * time.Second, true
		}
	}
	return 0, 0, false
}

// promptPanel is the stack bar of the agent's latest request: the layers sized by tokens, bright where the provider read them from
// its cache and dim where they were paid in full; a light runs along it when an answer arrives, a broken layer flashes, a cooling
// cache fades.
func promptPanel(s Scene, a *state.Agent, w int) []cell.Line {
	st := stylesOf(s.Pal)
	layers := promptLayers(a, s.Mem.g0est())
	if len(layers) == 0 {
		return []cell.Line{cell.Styled(st.dim, "nothing has been sent yet")}
	}
	stk := a.Stack
	o := widget.NewStackOpts(w, stk.Read)
	if stk.RespSeq != 0 && !s.NoAnim {
		if age := s.Mem.born().Age(stk.RespSeq, s.Frame); age < sweepFrames && stk.Prompt > 0 {
			o.Sweep = float64(stk.Read) / float64(stk.Prompt) * float64(age+1) / sweepFrames
		}
	}
	o.BreakAt = breakLayer(a, layers)
	if left, total, ok := ttlOf(s.Snap, "agent", a.ID); ok && total > 0 {
		o.Warm = float64(left) / float64(total)
	}
	bar, labels := widget.StackBar(layers, o, s.Pal)
	out := []cell.Line{bar, labels}

	var leg row
	leg.add(st.dim, "█ cached ").add(cell.Style{}, widget.Tokens(stk.Read))
	leg.add(st.dim, "  ░ paid ").add(cell.Style{}, widget.Tokens(max(0, stk.Prompt-stk.Read)))
	leg.add(st.dim, "  ▏ breakpoint")
	var right cell.Line
	if left, total, ok := ttlOf(s.Snap, "agent", a.ID); ok {
		right = widget.TTL(left, total, 24, s.Pal)
	}
	out = append(out, alignRight(leg.line(), right, w))
	if stk.Miss {
		var m row
		m.add(st.bad, "⚠ cache miss: ").add(st.dim, fmt.Sprintf("the harness expected %s read from the cache and got %s", widget.Tokens(stk.ExpectedRead), widget.Tokens(stk.Read)))
		if stk.ExpectedCold {
			m.add(st.dim, " (the entry had expired, so it was no surprise)")
		}
		out = append(out, fit(m.line(), w))
	}
	return out
}

// layerTable is the prompt layer by layer: size, share, how much of it came from the cache, its hash and the epoch of its change.
func layerTable(s Scene, a *state.Agent, w int) []cell.Line {
	layers := promptLayers(a, s.Mem.g0est())
	if len(layers) == 0 {
		return nil
	}
	o := widget.NewStackOpts(w, a.Stack.Read)
	o.BreakAt = breakLayer(a, layers)
	return widget.StackTable(layers, o, s.Pal)
}

// sparkPanel is the hit ratio of every request of the agent as a row of bars, with the compactions, the breaks and the epochs
// marked above it.
func sparkPanel(s Scene, a *state.Agent, w int) []cell.Line {
	st := stylesOf(s.Pal)
	ratios := a.Hits.Ratios
	if len(ratios) == 0 {
		return []cell.Line{cell.Styled(st.dim, "no request has been answered yet")}
	}
	var marks []widget.Mark
	for _, m := range a.Hits.Marks {
		i := m.At - a.Hits.First
		if i < 0 || i >= len(ratios) {
			continue
		}
		k := widget.MarkEpoch
		switch m.Kind {
		case state.MarkAnomaly:
			k = widget.MarkBreak
		case state.MarkCompaction:
			k = widget.MarkCompact
		}
		marks = append(marks, widget.Mark{At: i, Kind: k})
	}
	out := widget.Spark(ratios, w, marks, s.Pal)
	low, at := 2.0, 0
	sum := 0.0
	for i, r := range ratios {
		sum += r
		if r < low {
			low, at = r, a.Hits.First+i+1
		}
	}
	var sm row
	sm.add(st.dim, fmt.Sprintf("%d %s · mean ", a.Hits.Len(), plural(a.Hits.Len(), "request", "requests"))).
		add(hitStyle(st, sum/float64(len(ratios))), widget.Percent(sum/float64(len(ratios)))).
		add(st.dim, " · lowest ").add(hitStyle(st, low), widget.Percent(low)).add(st.dim, fmt.Sprintf(" at request %d", at))
	out = append(out, fit(sm.line(), w))
	var lg row
	lg.add(st.bad, "⚠").add(st.dim, " cache break   ").add(st.accent, "◆").add(st.dim, " compaction   ").add(st.info, "↻").add(st.dim, " new epoch")
	return append(out, fit(lg.line(), w))
}

// prefixPanel is the prefix the agent rides: who shares it, what it weighs, how long it will be kept.
func prefixPanel(s Scene, a *state.Agent, w int) []cell.Line {
	st := stylesOf(s.Pal)
	sn := s.Snap
	var pf *state.Prefix
	for i := range sn.Prefixes {
		for _, id := range sn.Prefixes[i].Agents {
			if id == a.ID {
				pf = &sn.Prefixes[i]
			}
		}
	}
	if pf == nil {
		return paragraph(st.dim, "no shared prefix yet: it is named when the agent sends its first request", w)
	}
	var out []cell.Line
	var h row
	h.add(st.bold, "shared ").add(cell.Style{}, widget.Tokens(pf.Tokens)).add(st.dim, " tokens of G1–G2 · ").
		add(st.bold, fmt.Sprint(pf.Riders)).add(st.dim, " "+plural(pf.Riders, "rider", "riders"))
	out = append(out, fit(h.line(), w))
	riders := make([]string, 0, len(pf.Agents))
	for _, id := range pf.Agents {
		riders = append(riders, clean(id))
	}
	shown := strings.Join(riders, " ")
	out = append(out, fit(cell.Styled(st.dim, shown), w))
	if left, total, ok := ttlOf(sn, "prefix", pf.Key); ok {
		out = append(out, widget.TTL(left, total, w, s.Pal))
	}
	if a.Stack.Inherited {
		out = append(out, paragraph(st.good, "joined a warm prefix: its shared layers were read, not written", w)...)
	}
	return out
}

// compactPanel shows the agent's latest compaction as the fold of its thread, and what the earlier ones did.
func compactPanel(s Scene, a *state.Agent, w int) []cell.Line {
	st := stylesOf(s.Pal)
	if len(a.Compacts) == 0 {
		return paragraph(st.dim, "none yet. When the thread outgrows its budget it is folded into a resume, at the moment the cache is coldest, so the rewrite costs nothing extra.", w)
	}
	c := a.Compacts[len(a.Compacts)-1]
	progress := 1.0
	if !s.NoAnim {
		if age := s.Mem.born().Age(c.Seq, s.Frame); age < foldFrames {
			progress = float64(age+1) / foldFrames
		}
	}
	out := widget.Fold(c.Before, c.After, progress, w, s.Pal)
	var m row
	m.add(st.dim, c.Mode)
	switch c.Moment {
	case "cold":
		m.add(st.dim, " · ").add(st.good, "the cache was cold").add(st.dim, ", so the rewrite cost nothing extra")
	case "warm":
		m.add(st.dim, " · ").add(st.warn, "the cache was warm").add(st.dim, ": a declared, priced rebase")
	}
	if c.Fallback {
		m.add(st.dim, " · the harness's own patch was used")
	}
	out = append(out, fit(m.line(), w))
	if n := len(a.Compacts) - 1; n > 0 {
		var e row
		e.add(st.dim, fmt.Sprintf("%d earlier: ", n))
		for i := n - 1; i >= 0 && i >= n-3; i-- {
			e.add(st.dim, fmt.Sprintf("%s → %s  ", widget.Tokens(a.Compacts[i].Before), widget.Tokens(a.Compacts[i].After)))
		}
		out = append(out, fit(e.line(), w))
	}
	return out
}

// anomalyPanel lists what the harness saw the cache do that its prompt did not say it should, newest first.
func anomalyPanel(s Scene, a *state.Agent, w int) []cell.Line {
	st := stylesOf(s.Pal)
	if len(a.Anomalies) == 0 {
		other := len(s.Snap.Anomalies)
		out := paragraph(st.good, "none: every request read what its prompt said it should", w)
		if other > 0 {
			out = append(out, paragraph(st.dim, fmt.Sprintf("(%d in the session, on other agents)", other), w)...)
		}
		return out
	}
	var out []cell.Line
	for i := len(a.Anomalies) - 1; i >= 0 && len(out) < 6; i-- {
		an := a.Anomalies[i]
		sty := st.warn
		if !s.NoAnim && s.Mem.born().Age(an.Seq, s.Frame) < flashFrames {
			sty = st.bad
		}
		var r row
		r.add(sty, "⚠ "+clean(an.Kind))
		if an.Layer != "" {
			r.add(st.dim, " in ").add(cell.Style{}, clean(an.Layer))
		}
		if an.Expected > 0 {
			r.add(st.dim, fmt.Sprintf(" · %s/%s read", widget.Tokens(an.Actual), widget.Tokens(an.Expected)))
		}
		if an.MissKnown && an.MissUSD > 0 {
			r.add(st.dim, " · ").add(st.warn, widget.USD(an.MissUSD))
		}
		if an.Note != "" {
			r.add(st.dim, " · "+clean(an.Note))
		}
		out = append(out, fit(r.line(), w))
	}
	return out
}

// agentsPanel is every agent's cache side by side, the one the view is about marked.
func agentsPanel(s Scene, sel *state.Agent, w, rows int) []cell.Line {
	st := stylesOf(s.Pal)
	type col struct {
		head  string
		width int
		left  bool
	}
	cols := []col{{"", 1, true}, {"AGENT", 8, true}, {"ROLE", 9, true}, {"REQ", 4, false}, {"HIT", 5, false}, {"LAST", 5, false}, {"READ", 7, false}, {"PAID", 7, false}, {"WRITE", 7, false}, {"SAVED", 8, false}}
	total := func(n int) int {
		t := 0
		for i := 0; i < n; i++ {
			t += cols[i].width
			if i > 0 {
				t++
			}
		}
		return t
	}
	n := len(cols)
	for n > 5 && total(n) > w {
		n--
	}
	cols = cols[:n]
	cellOf := func(c col, text string, sty cell.Style) cell.Line {
		t := fit(cell.Styled(sty, text), c.width)
		if c.left {
			return t.Pad(c.width, cell.Style{})
		}
		return cell.Join(cell.Spaces(c.width-t.Width(), cell.Style{}), t)
	}
	var head row
	for i, c := range cols {
		if i > 0 {
			head.space(1)
		}
		head.addLine(cellOf(c, c.head, st.dim))
	}
	out := []cell.Line{head.line()}
	var list []state.Agent
	selAt := 0
	for _, ag := range s.Snap.Agents {
		if !ag.Service {
			if ag.ID == sel.ID {
				selAt = len(list)
			}
			list = append(list, ag)
		}
	}
	more := 0
	if rows > 0 && len(list) > rows-1 { // the header is one of the rows; with more agents than room, the last row says how many are left out
		k := max(rows-2, 1)
		start := 0
		if selAt >= k {
			start = selAt - k + 1 // the agent the view is about stays on the screen
		}
		more = len(list) - k
		list = list[start : start+k]
	}
	for _, ag := range list {
		mark, idst := " ", cell.Style{}
		if ag.ID == sel.ID {
			mark, idst = "›", st.accent
		}
		vals := []struct {
			t string
			s cell.Style
		}{
			{mark, st.accent}, {clean(ag.ID), idst}, {clean(ag.Role), roleStyle(s.Pal, ag.Role)}, {fmt.Sprint(ag.Requests), cell.Style{}},
			{widget.Percent(ag.Tokens.HitRatio()), hitStyle(st, ag.Tokens.HitRatio())}, {widget.Percent(ag.Stack.Hit), hitStyle(st, ag.Stack.Hit)},
			{widget.Tokens(int(ag.Tokens.CacheRead)), st.good}, {widget.Tokens(int(ag.Tokens.Input)), cell.Style{}},
			{widget.Tokens(int(ag.Tokens.CacheWrite)), st.warn}, {widget.USD(ag.SavedUSD), st.good},
		}
		var r row
		for i, c := range cols {
			if i > 0 {
				r.space(1)
			}
			r.addLine(cellOf(c, vals[i].t, vals[i].s))
		}
		out = append(out, r.line())
	}
	if more > 0 {
		out = append(out, cell.Styled(st.dim, fmt.Sprintf("… and %d more (↑↓ chooses the agent)", more)))
	}
	return out
}

// sessionPanel sums up what the cache did for the whole session, on two lines.
func sessionPanel(s Scene, w int) []cell.Line {
	st := stylesOf(s.Pal)
	t := s.Snap.Totals
	if t.Tokens.Prompt() == 0 {
		return []cell.Line{cell.Styled(st.dim, "no answer yet")}
	}
	var a row
	a.add(hitStyle(st, t.HitRatio()), widget.Percent(t.HitRatio())).add(st.dim, " of the session's prompt came from the cache")
	if t.Savings.Known() && t.Savings.SavedUSD > 0 {
		lead := " · saved "
		if !t.Savings.Complete() {
			lead = " · saved at least "
		}
		a.add(st.dim, lead).add(st.good, widget.USD(t.Savings.SavedUSD)).add(st.dim, " (list price)")
	}
	out := []cell.Line{fit(a.line(), w)}
	var b row
	b.add(st.dim, fmt.Sprintf("%d %s", t.Compactions, plural(t.Compactions, "compaction", "compactions")))
	if t.Anomalies > 0 {
		b.add(st.dim, " · ").add(st.warn, fmt.Sprintf("%d %s", t.Anomalies, plural(t.Anomalies, "anomaly", "anomalies")))
	} else {
		b.add(st.dim, " · 0 anomalies")
	}
	if t.RateLimited > 0 {
		b.add(st.dim, fmt.Sprintf(" · %d rate limited", t.RateLimited))
	}
	if t.Retries > 0 {
		b.add(st.dim, fmt.Sprintf(" · %d retries", t.Retries))
	}
	return append(out, fit(b.line(), w))
}
