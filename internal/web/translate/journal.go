package translate

import (
	"encoding/json"

	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// The journal of a tab generation: every UI event with its seq, in order, bounded by a number of events and a number of bytes of
// JSON. What it evicts from the front is folded into the front mirror, whose keyframe stands in for it in a snapshot.
// It also folds every event it appends into a tail mirror and keeps a keyframe of that every keyframeEvery events, so that the state
// at any retained seq is a keyframe and a short run of events away (Seek: the scrubber, a late joiner that wants a moment).

// entryOverhead is what an entry costs beyond its JSON, counted against the byte bound.
const entryOverhead = 64

// keyframeEvery is the distance between two periodic keyframes, in events; maxKeyframeBytes bounds the bytes they hold together.
const (
	keyframeEvery    = 2000
	maxKeyframeBytes = 8 << 20
)

// entry is one journalled event.
type entry struct {
	seq  uint64
	t    float64
	kind string
	raw  []byte
}

// point is a periodic keyframe: the state through seq.
type point struct {
	seq   uint64
	t     float64
	kf    []wire.Raw
	bytes int
}

// journal holds the retained events. Its methods are called with the translator's lock held.
type journal struct {
	maxEvents, maxBytes int

	ents  []entry // ring
	head  int
	n     int
	bytes int

	lastSeq uint64
	lastT   float64

	front    *mirror // everything evicted
	frontSeq uint64
	kf       []wire.Raw // the encoded keyframe of front, while kfOK
	kfOK     bool
	evicted  uint64

	tail    *mirror // everything appended
	points  []point
	pbytes  int
	sinceKF int
}

// newJournal returns an empty journal with the bounds.
func newJournal(maxEvents, maxBytes int) *journal {
	return &journal{maxEvents: maxEvents, maxBytes: maxBytes, front: newMirror(), tail: newMirror()}
}

// at is the i-th oldest retained entry.
func (j *journal) at(i int) *entry { return &j.ents[(j.head+i)%len(j.ents)] }

// add appends an encoded event (its seq is lastSeq+1) and folds e into the tail mirror; it evicts from the front while a bound is
// exceeded, down to nine tenths of it, so that eviction costs constant time per event on average.
func (j *journal) add(seq uint64, t float64, kind string, raw []byte, e wire.Event) {
	if j.n == len(j.ents) {
		j.grow()
	}
	*j.at(j.n) = entry{seq: seq, t: t, kind: kind, raw: raw}
	j.n++
	j.bytes += len(raw) + entryOverhead
	j.lastSeq, j.lastT = seq, t
	j.tail.fold(e)
	if j.sinceKF++; j.sinceKF >= keyframeEvery {
		j.sinceKF = 0
		j.mark()
	}
	if j.n > j.maxEvents || j.bytes > j.maxBytes {
		j.evict(j.maxEvents*9/10, j.maxBytes*9/10)
	}
}

// grow doubles the ring (up to the event bound and a little more).
func (j *journal) grow() {
	size := max(64, 2*len(j.ents))
	if size > j.maxEvents+1 {
		size = j.maxEvents + 1
	}
	if size <= len(j.ents) {
		size = len(j.ents) + 1
	}
	ents := make([]entry, size)
	for i := 0; i < j.n; i++ {
		ents[i] = *j.at(i)
	}
	j.ents, j.head = ents, 0
}

// evict folds the oldest entries into the front mirror until at most events and bytes remain.
func (j *journal) evict(events, bytes int) {
	for j.n > 0 && (j.n > events || j.bytes > bytes) {
		e := j.at(0)
		if ev, err := decodeEvent(e.raw); err == nil {
			j.front.fold(ev)
		}
		j.frontSeq = e.seq
		j.bytes -= len(e.raw) + entryOverhead
		*e = entry{}
		j.head = (j.head + 1) % len(j.ents)
		j.n--
		j.evicted++
		j.kfOK = false
	}
	for len(j.points) > 0 && j.points[0].seq <= j.frontSeq {
		j.pbytes -= j.points[0].bytes
		j.points = j.points[1:]
	}
}

// mark keeps a keyframe of the tail mirror at the newest seq, dropping the oldest ones past maxKeyframeBytes.
func (j *journal) mark() {
	kf := encodeAll(j.tail.keyframe(j.lastT))
	p := point{seq: j.lastSeq, t: j.lastT, kf: kf}
	for _, r := range kf {
		p.bytes += len(r)
	}
	j.points = append(j.points, p)
	j.pbytes += p.bytes
	for len(j.points) > 1 && j.pbytes > maxKeyframeBytes {
		j.pbytes -= j.points[0].bytes
		j.points = j.points[1:]
	}
}

// keyframe is the encoded keyframe of everything evicted, stamped at the t of the first retained event.
func (j *journal) keyframe() []wire.Raw {
	if j.evicted == 0 {
		return nil
	}
	if !j.kfOK {
		t0 := j.front.t
		if j.n > 0 && j.at(0).t > t0 {
			t0 = j.at(0).t
		}
		j.kf, j.kfOK = encodeAll(j.front.keyframe(t0)), true
	}
	return j.kf
}

// retained copies the retained events, oldest first, from the first one after seq.
func (j *journal) retained(after uint64) []wire.Raw {
	out := make([]wire.Raw, 0, j.n)
	for i := 0; i < j.n; i++ {
		if e := j.at(i); e.seq > after {
			out = append(out, e.raw)
		}
	}
	return out
}

// snapshot is the keyframe, the retained events and the last seq.
func (j *journal) snapshot() (kf, evs []wire.Raw, seq uint64) {
	return j.keyframe(), j.retained(0), j.lastSeq
}

// seek is the state at seq as a keyframe and the retained events that follow it up to seq: the newest periodic keyframe at or
// before seq, else the front's. ok is false when seq is older than what the journal retains.
func (j *journal) seek(seq uint64) (kf, evs []wire.Raw, ok bool) {
	if seq > j.lastSeq {
		seq = j.lastSeq
	}
	if seq < j.frontSeq {
		return nil, nil, false
	}
	from := j.frontSeq
	kf = j.keyframe()
	for i := len(j.points) - 1; i >= 0; i-- {
		if p := j.points[i]; p.seq <= seq && p.seq > j.frontSeq {
			from, kf = p.seq, p.kf
			break
		}
	}
	for i := 0; i < j.n; i++ {
		e := j.at(i)
		if e.seq > seq {
			break
		}
		if e.seq > from {
			evs = append(evs, e.raw)
		}
	}
	return kf, evs, true
}

// encodeAll encodes events (keyframe events: seq 0).
func encodeAll(evs []wire.Event) []wire.Raw {
	out := make([]wire.Raw, 0, len(evs))
	for _, e := range evs {
		if b, err := json.Marshal(e); err == nil {
			out = append(out, b)
		}
	}
	return out
}
