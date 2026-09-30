package swarm

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/reee344/sleipnir/internal/events"
)

// Waking the manager.
//
// In an interactive session (sleipnir chat --swarm N) a person talks to the manager
// between turns, and workers legitimately outlive a turn. When the manager is idle
// and something happens that it has not seen (a worker finished or failed, a task
// reached review, mail arrived for it), the swarm starts one manager run with a
// short harness-written note. Events are coalesced: a burst starts one run, not one
// per event. The bound on automatic runs between two human inputs keeps a busy swarm
// from spending the person's budget while they are away, and the swarm budget still
// applies (no run is started once it is spent).
//
// The note is delivered as harness mail, not as a user turn: a user turn would be
// preserved by compaction as something the person asked for, once per wake. It
// contains only ids and statuses the harness derived; nothing an agent wrote.

// maxWakeItems bounds how many changes one note lists.
const maxWakeItems = 8

// waker is the wake state of a swarm. Guarded by its own mutex.
type waker struct {
	mu      sync.Mutex
	timer   *time.Timer
	first   time.Time // the first event of the burst the timer is waiting out
	wakes   int       // automatic runs since the last human input
	told    bool      // the person was told the bound was reached
	stopped bool      // the swarm shut down
}

func (w *waker) reset() {
	w.mu.Lock()
	w.wakes, w.told = 0, false
	w.mu.Unlock()
}

func (w *waker) stop() {
	w.mu.Lock()
	w.stopped = true
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
	w.mu.Unlock()
}

// HumanInput tells the swarm a person just typed to the manager: the count of
// automatic manager runs starts again. RunManager does it itself; the session calls
// it for steering sent to a running manager.
func (s *Swarm) HumanInput() { s.wk.reset() }

// Wakes reports how many automatic manager runs have started since the last human
// input.
func (s *Swarm) Wakes() int {
	s.wk.mu.Lock()
	defer s.wk.mu.Unlock()
	return s.wk.wakes
}

// managerEvent notes that something happened that the manager may want to know.
// It is called from wherever the swarm learns of it and never blocks: it starts (or
// pushes back) the timer that ends the burst.
func (s *Swarm) managerEvent() {
	if !s.cfg.WakeManager {
		return
	}
	w := &s.wk
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped {
		return
	}
	now := time.Now()
	if w.timer == nil {
		w.first = now
		w.timer = time.AfterFunc(s.cfg.WakeQuiet, s.fireWake)
		return
	}
	// A burst is going on: wait for it to go quiet, but never past WakeMax after its
	// first event, so a steady trickle cannot postpone the wake for ever.
	wait := s.cfg.WakeQuiet
	if left := s.cfg.WakeMax - now.Sub(w.first); left < wait {
		wait = max(left, 0)
	}
	w.timer.Reset(wait)
}

func (s *Swarm) fireWake() {
	w := &s.wk
	w.mu.Lock()
	w.timer = nil
	stopped := w.stopped
	w.mu.Unlock()
	if stopped {
		return
	}
	s.guard("wake", s.tryWake)
}

// tryWake starts a manager run if there is a reason to, and it is allowed: the
// swarm is running, its budget is not spent, the manager is idle (never while
// another run of it is active), something is new to it, and the bound on automatic
// runs is not reached.
func (s *Swarm) tryWake() {
	if s.alive() != nil || s.budgetErr() != nil {
		return
	}
	m := s.get(s.ManagerID())
	if m == nil {
		return
	}
	note := s.wakeNote(m)
	if note == "" {
		return
	}
	w := &s.wk
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		return
	}
	if w.wakes >= s.cfg.MaxWakes {
		tell := !w.told
		w.told = true
		n := w.wakes
		w.mu.Unlock()
		if tell {
			s.emitAs(m.id, events.TypeSwarmWakePaused, map[string]any{"n": n})
			s.managerNotice(m, "warn", fmt.Sprintf("the manager was woken %d times without input from you and will not be again until you write to it; waiting for it: %s", n, note))
		}
		return
	}
	w.wakes++
	n := w.wakes
	w.mu.Unlock()

	// Never while another run of the manager is active: that run sees the board itself.
	s.mu.Lock()
	ctx := s.rootCtx
	s.mu.Unlock()
	if !s.reserveManager(m) || !s.track(func() {
		s.emitAs(m.id, events.TypeSwarmWake, map[string]any{"n": n, "note": note})
		s.managerNotice(m, "wake", note)
		mail := Message{ID: s.nextHarnessID(), From: harnessSender, To: m.id, Kind: "info", Text: note}
		_, _ = s.runManager(ctx, m, "", mail.Frame())
	}) {
		w.mu.Lock()
		w.wakes--
		w.mu.Unlock()
		s.releaseManager(m)
	}
}

// track runs fn on a goroutine the swarm waits for at Shutdown. It reports false
// (and runs nothing) once the swarm is shutting down.
func (s *Swarm) track(fn func()) bool {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return false
	}
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		defer func() { _ = recover() }()
		fn()
	}()
	return true
}

// reserveManager claims the idle manager for a run (the same compare-and-set as a
// worker's reserve): false when it is already running.
func (s *Swarm) reserveManager(m *member) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.life != lifeIdle {
		return false
	}
	m.life = lifeRunning
	return true
}

func (s *Swarm) releaseManager(m *member) {
	m.mu.Lock()
	m.life = lifeIdle
	m.mu.Unlock()
}

// managerNotice shows the person something about the manager through the session's
// sink. Levels other than "info" are printed even when notices are quiet.
func (s *Swarm) managerNotice(m *member, level, msg string) {
	defer func() { _ = recover() }()
	m.sink.Notice(m.id, level, msg)
}

// noteManagerSeen records the board as the manager's newest request showed it: what
// it has looked at. Changes after that are news to it.
func (s *Swarm) noteManagerSeen(snap *Snapshot) { s.mgrSeen.Store(snap) }

// wakeNote is the harness-written note for the manager, or "" when nothing is new
// to it. It lists task and agent changes since the board the manager last saw, and
// whether mail is waiting; only ids and statuses, sorted, bounded.
func (s *Swarm) wakeNote(m *member) string {
	seen := s.mgrSeen.Load()
	if seen == nil {
		return "" // the manager has not made a request yet: nothing it could have missed
	}
	items := wakeItems(seen, s.Board.Snapshot())
	m.mu.Lock()
	mail := !m.box.empty() || m.a.PendingInbox() > 0
	m.mu.Unlock()
	if mail {
		items = append(items, "mail is waiting for you")
	}
	if len(items) == 0 {
		return ""
	}
	more := ""
	if len(items) > maxWakeItems {
		more = " (+" + strconv.Itoa(len(items)-maxWakeItems) + " more)"
		items = items[:maxWakeItems]
	}
	return truncRunes("While you were idle: "+strings.Join(items, "; ")+more+
		". Check the board, accept or reject what is in review, and tell the person briefly what happened.", 500)
}

// wakeItems lists the changes between two views of the board that the manager
// should act on: tasks that reached review, failed or were blocked, tasks returned to
// the pool, and workers that failed. Accepted tasks are not news (the manager did
// it). Ids and status words only, in task order, then agents sorted.
func wakeItems(a, b *Snapshot) []string {
	before := make(map[string]Task, len(a.Tasks))
	for _, t := range a.Tasks {
		before[t.ID] = t
	}
	var out []string
	for _, t := range b.Tasks {
		old, existed := before[t.ID]
		if existed && old.Status == t.Status && old.Rev == t.Rev {
			continue
		}
		id := safeToken(t.ID, 24)
		owner := safeToken(t.Owner, 24)
		switch {
		case t.Status == StatusReview && (!existed || old.Status != StatusReview || old.Rev != t.Rev):
			out = append(out, id+" is in review ("+owner+")")
		case t.Status == StatusFailed && old.Status != StatusFailed:
			out = append(out, id+" failed")
		case t.Status == StatusBlocked && old.Status != StatusBlocked:
			out = append(out, id+" is blocked ("+owner+")")
		case t.Status == StatusTodo && existed && (old.Status == StatusDoing || old.Status == StatusBlocked || old.Status == StatusReview):
			out = append(out, id+" went back to todo")
		}
	}
	var failed []string
	for _, ag := range b.Agents {
		old, ok := a.Agent(ag.ID)
		if ag.State == "failed" && (!ok || old.State != "failed") {
			failed = append(failed, safeToken(ag.ID, 24)+" failed")
		}
	}
	sort.Strings(failed)
	return append(out, failed...)
}
