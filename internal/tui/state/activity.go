package state

import "time"

// actRow is one agent's lane of the activity matrix: a ring of ActivitySeconds one-second cells. A cell holds the absolute second it
// stands for, so a cell that is older than the window is recognised and taken over, and nothing has to be swept as time passes.
type actRow struct {
	secs [ActivitySeconds]int64
	busy [ActivitySeconds]uint16 // milliseconds of the second that a model request or a tool call was in flight
	mark [ActivitySeconds]uint8  // the Act* bits of what happened in it
}

// slot maps signed seconds into the nonnegative circular activity-history index.
func slot(sec int64) int {
	n := int64(ActivitySeconds)
	return int(((sec % n) + n) % n)
}

// floorDiv rounds signed integer division toward negative infinity; b must be nonzero.
func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

// cell returns the cell for the second, taking a stale one over; ok is false when the cell already holds a newer second, which means
// the event is older than the window.
func (r *actRow) cell(sec int64) (int, bool) {
	i := slot(sec)
	switch {
	case r.secs[i] == sec:
		return i, true
	case r.secs[i] > sec:
		return i, false
	}
	r.secs[i], r.busy[i], r.mark[i] = sec, 0, 0
	return i, true
}

// addBusy credits the interval [from, to) to the seconds it covers, at most the last window of it. An interval that has no length
// counts for a millisecond, so that a call that returned at once still shows.
func (r *actRow) addBusy(from, to time.Time) {
	lo, hi := from.UnixMilli(), to.UnixMilli()
	if hi <= lo {
		hi = lo + 1
	}
	if hi-lo > int64(ActivitySeconds)*1000 {
		lo = hi - int64(ActivitySeconds)*1000
	}
	for lo < hi {
		sec := floorDiv(lo, 1000)
		end := min(hi, (sec+1)*1000)
		if i, ok := r.cell(sec); ok {
			r.busy[i] = uint16(min(1000, int64(r.busy[i])+end-lo))
		}
		lo = end
	}
}

// markAt sets marker bits in the cell of the second of t.
func (r *actRow) markAt(t time.Time, bits uint8) {
	if i, ok := r.cell(t.Unix()); ok {
		r.mark[i] |= bits
	}
}

// busyLevel turns the milliseconds a second was busy into 0 (idle) to 8 (the whole second), rounding up so that any activity shows.
func busyLevel(ms int64) uint8 {
	if ms <= 0 {
		return 0
	}
	return uint8(min(8, (ms*8+999)/1000))
}

// activitySnapshot builds the matrix as of now: ActivitySeconds cells per agent ending at the second of the later of now and the
// State's clock. An agent that is busy right now has its open interval credited up to that second. The rows are in display order.
func (s *State) activitySnapshot(ags []*agentState, now time.Time) Activity {
	end := now
	if s.clock.After(end) {
		end = s.clock
	}
	if end.IsZero() {
		return Activity{}
	}
	endSec := end.Unix()
	act := Activity{End: time.Unix(endSec, 0).UTC(), Rows: make([]ActivityRow, 0, len(ags))}
	for _, a := range ags {
		row := ActivityRow{Agent: a.ID, Levels: make(Levels, ActivitySeconds), Marks: make(Marks, ActivitySeconds)}
		var ms [ActivitySeconds]int64
		for k := 0; k < ActivitySeconds; k++ {
			sec := endSec - int64(ActivitySeconds-1-k)
			if i := slot(sec); a.row.secs[i] == sec {
				ms[k] = int64(a.row.busy[i])
				row.Marks[k] = a.row.mark[i]
			}
		}
		if !a.busySince.IsZero() {
			lo, hi := a.busySince.UnixMilli(), end.UnixMilli()
			if hi <= lo {
				hi = lo + 1
			}
			first := endSec - int64(ActivitySeconds-1)
			lo = max(lo, first*1000) // only the window is credited, however long the agent has been busy
			for lo < hi {
				sec := floorDiv(lo, 1000)
				next := min(hi, (sec+1)*1000)
				if sec >= first && sec <= endSec {
					k := int(sec - first)
					ms[k] = min(1000, ms[k]+next-lo)
				}
				lo = next
			}
		}
		for k := range ms {
			row.Levels[k] = busyLevel(ms[k])
		}
		act.Rows = append(act.Rows, row)
	}
	return act
}
