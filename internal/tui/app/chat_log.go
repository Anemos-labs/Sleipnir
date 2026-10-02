package app

// The session's log as the chat reads it: events folded into the state and the blocks they print.

import (
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/tui/cell"
	"github.com/anemos-labs/sleipnir/internal/tui/state"
)

// ---- the log ----

func (m *chatModel) event(e events.Event) {
	m.st.Apply(e)
	m.snapOK = false
}

// snapshot is the state as of now, taken once per change.
func (m *chatModel) snapshot() *state.Snapshot {
	if !m.snapOK {
		m.snap = m.st.SnapshotAt(m.clock())
		m.snapOK = true
		m.mem.Observe(m.snap, m.frame)
		m.scanSnapshot()
	}
	return m.snap
}

// maxEndpointBreaksShown is how many cache breaks that are the endpoint's own the scrollback says before it says that it will not say more.
const maxEndpointBreaksShown = 3

// materialMissUSD is what a cache break that is the endpoint's own must have cost, at list price, to be said in the scrollback without /verbose:
// on a cheap endpoint it is a fraction of a cent, nothing the person can act on, and the chat page is for what they can.
const materialMissUSD = 0.01

// scanSnapshot finds what the log has added that the scrollback should say: a compaction, a cache break.
func (m *chatModel) scanSnapshot() {
	sn := m.snap
	type found struct {
		seq  uint64
		fold *state.Compaction
		anom *state.Anomaly
		who  string
	}
	var news []found
	main := sn.Main()
	for i := range sn.Agents {
		a := &sn.Agents[i]
		who := ""
		if a.ID != main {
			who = a.ID
		}
		for j := range a.Compacts {
			if c := &a.Compacts[j]; c.Seq > m.compactSeen {
				news = append(news, found{seq: c.Seq, fold: c, who: who})
			}
		}
		for j := range a.Anomalies {
			if an := &a.Anomalies[j]; an.Seq > m.anomalySeen {
				news = append(news, found{seq: an.Seq, anom: an, who: who})
			}
		}
	}
	if len(news) == 0 {
		return
	}
	sortFound := func() {
		for i := 1; i < len(news); i++ {
			for j := i; j > 0 && news[j].seq < news[j-1].seq; j-- {
				news[j], news[j-1] = news[j-1], news[j]
			}
		}
	}
	sortFound()
	for _, f := range news {
		switch {
		case f.fold != nil:
			m.compactSeen = max(m.compactSeen, f.seq)
			if m.k.Anim {
				m.folds = append(m.folds, &foldAnim{comp: *f.fold, who: f.who, start: m.frame})
			} else {
				m.printFold(*f.fold, f.who)
			}
		case f.anom != nil:
			m.anomalySeen = max(m.anomalySeen, f.seq)
			m.syncStream()
			if f.anom.Layer == "" && f.anom.Kind == "low_hit" {
				// the prompt did not change: it is the endpoint's cache, and on some it breaks all the time. The person cannot mend it: it is
				// said when it cost real money or when they asked for the notices (/verbose); /stats has the hit ratio and the inspector each miss
				if !m.c.Verbose && !(f.anom.MissKnown && f.anom.MissUSD >= materialMissUSD) {
					continue
				}
				m.endpointBreaks++
				if m.endpointBreaks > maxEndpointBreaksShown {
					if m.endpointBreaks == maxEndpointBreaksShown+1 {
						m.block(bkNote, []cell.Line{cell.Styled(m.k.st.dim, "  "+m.k.g.warn+" this endpoint's cache keeps missing the prompt prefix; further misses are not said here (/stats has the hit ratio, `sleipnir inspect` each one)")})
					}
					continue
				}
			}
			m.block(bkNote, m.k.anomalyLines(*f.anom, f.who, m.cols))
			if m.k.Anim {
				m.flashUntil = m.frame + flashFrames
			}
		}
	}
}

func (m *chatModel) printFold(c state.Compaction, who string) {
	m.syncStream()
	m.block(bkNote, m.k.foldRecord(c, who, m.cols))
}

// flushFolds writes the records of the folds that are still playing: the turn is over, or the program is.
func (m *chatModel) flushFolds() {
	for _, f := range m.folds {
		m.printFold(f.comp, f.who)
	}
	m.folds = nil
}

// stepFolds finishes the folds whose animation is over: each leaves its record in the scrollback.
func (m *chatModel) stepFolds() {
	kept := m.folds[:0]
	for _, f := range m.folds {
		if m.frame-f.start >= foldFrames {
			m.printFold(f.comp, f.who)
			continue
		}
		kept = append(kept, f)
	}
	m.folds = kept
}
