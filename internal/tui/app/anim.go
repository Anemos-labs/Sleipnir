package app

import "github.com/anemos-labs/sleipnir/internal/tui/state"

// Born remembers at which animation frame each thing that arrives with a seq (a message, a response, a compaction, an anomaly, a
// spawn) was first shown. The animations of the views are functions of the frame they are given; the arrival of a thing is the
// frame now less the frame it was born at, so the recorder and the terminal program, which both fill a Born as they go, draw
// the same pictures from the same log, and nothing in a view reads a clock.
//
// It stays small: once it holds more than bornMax entries, the old ones that the snapshot no longer holds are forgotten.
type Born map[uint64]int

const (
	// bornKeep is how many frames an entry is kept at least: more than any animation of this package takes.
	bornKeep = 900
	bornMax  = 4096
	// Settled is the age of a thing that arrived before anyone looked: its animation is over.
	Settled = 1 << 30
)

// Observe notes the things in the snapshot that have not been seen before, as born at frame.
func (b Born) Observe(sn *state.Snapshot, frame int) { b.note(sn, frame) }

// Seed notes everything in the snapshot as settled: a program that opens a session that is already under way does not play the
// arrival of what happened before it looked.
func (b Born) Seed(sn *state.Snapshot) { b.note(sn, -Settled) }

func (b Born) note(sn *state.Snapshot, frame int) {
	if sn == nil {
		return
	}
	each(sn, func(seq uint64) {
		if _, ok := b[seq]; !ok {
			b[seq] = frame
		}
	})
	if len(b) > bornMax { // forget what is no longer in the snapshot (it cannot be drawn) and is old
		live := map[uint64]struct{}{}
		each(sn, func(seq uint64) { live[seq] = struct{}{} })
		for k, f := range b {
			if _, ok := live[k]; !ok && frame-f > bornKeep {
				delete(b, k)
			}
		}
	}
}

// each calls fn with the seq of everything in the snapshot whose arrival is animated.
func each(sn *state.Snapshot, fn func(seq uint64)) {
	call := func(seq uint64) {
		if seq != 0 {
			fn(seq)
		}
	}
	for _, m := range sn.Mail.Recent {
		call(m.Seq)
	}
	for _, a := range sn.Agents {
		call(a.SpawnSeq)
		call(a.Stack.RespSeq)
		for _, c := range a.Compacts {
			call(c.Seq)
		}
		for _, an := range a.Anomalies {
			call(an.Seq)
		}
	}
	for _, an := range sn.Anomalies {
		call(an.Seq)
	}
}

// Age is how many frames ago the thing with this seq was first shown: 0 when it has just arrived, Settled when it was there
// before anyone looked or is not known.
func (b Born) Age(seq uint64, frame int) int {
	f, ok := b[seq]
	if !ok {
		return Settled
	}
	return max(0, frame-f)
}

// Memory is what the views keep from one frame of the program to the next: when things arrived, and how big the constitution and
// the tool list are. A recorder and a terminal program each hold one and pass it in every Scene; a nil Memory is a screen with no
// history (nothing is animated and G0 is not known).
type Memory struct {
	Born Born
	// G0 is the estimate of the size of G0 (the constitution and the tools): the smallest prompt that any agent sent, less the
	// sections, which is G0 plus the little the agent had been told. The requests do not size G0, so this is the best the events say.
	G0 int
}

// NewMemory is an empty memory.
func NewMemory() *Memory { return &Memory{Born: Born{}} }

// Observe notes what is new in the snapshot, as it is at frame.
func (m *Memory) Observe(sn *state.Snapshot, frame int) {
	if m == nil || sn == nil {
		return
	}
	m.Born.Observe(sn, frame)
	m.g0(sn)
}

// Seed notes what the snapshot holds as settled (see Born.Seed).
func (m *Memory) Seed(sn *state.Snapshot) {
	if m == nil || sn == nil {
		return
	}
	m.Born.Seed(sn)
	m.g0(sn)
}

func (m *Memory) g0(sn *state.Snapshot) {
	for _, a := range sn.Agents {
		if u := a.Stack.Unsectioned; u > 0 && (m.G0 == 0 || u < m.G0) {
			m.G0 = u
		}
	}
}

func (m *Memory) born() Born {
	if m == nil {
		return nil
	}
	return m.Born
}

func (m *Memory) g0est() int {
	if m == nil {
		return 0
	}
	return m.G0
}
