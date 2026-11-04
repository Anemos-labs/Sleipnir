package swarm

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// The looking guard.
//
// A manager that reads while the team waits on it costs every idle worker its time. The live view says so in a line of its own
// (the staffing lines, emptyBoardLine), and models read past it: a manager that had decided to spawn T131 to T136 ended forty
// minutes of turns with reads and git checks only, and another ran eleven exploration commands before its first task. Measured
// with sleipnir tooleval on the cases those runs left (internal/tooleval/cases), a line on the result of the read itself that
// names the next call ("your next calls are spawn task=T131 ...") turned such a turn into spawns in most samples, where the
// same facts on the live view did not.
//
// So a manager's run of looking calls (lookingTool) carries, every lookEvery calls, the first of: the submissions waiting for its
// decision; the ready tasks a free slot or an idle member could start now; and, once the board has stayed empty and the team idle
// for lookIdleAfter calls, the way to hand the reading to the team. Any other call (a spawn, a task change, mail, wait, an edit)
// ends the run and starts the count again.

// lookEvery is how many looking calls in a row bring the line again; lookIdleAfter is how many it takes on an empty board.
const (
	lookEvery     = 3
	lookIdleAfter = 6
)

// lookingTool reports whether a call of the manager only looks: it reads, searches or runs a command, or reads the board,
// and starts or decides nothing. A tool that is neither (plan, memory, skill) is neutral (counts reports false for it).
func lookingTool(tool string, input json.RawMessage) (looking, counts bool) {
	switch tool {
	case "read", "grep", "glob", "ls", "bash", "bash_output", "recall":
		return true, true
	case "task":
		var in struct {
			Action string `json:"action"`
		}
		_ = json.Unmarshal(input, &in)
		return in.Action == "get" || in.Action == "list", true
	case "plan", "memory", "skill", "sleipnir":
		return false, false
	}
	return false, true
}

// lookLine counts the manager's looking calls and, every lookEvery of them in a row, returns the line their results carry; ""
// otherwise. It is called before each of the manager's calls.
func (h *holdHooks) lookLine(tool string, input json.RawMessage) string {
	looking, counts := lookingTool(tool, input)
	if !counts {
		return ""
	}
	h.notedMu.Lock()
	if !looking {
		h.looked = 0
		h.notedMu.Unlock()
		return ""
	}
	h.looked++
	n := h.looked
	h.notedMu.Unlock()
	if n%lookEvery != 0 {
		return ""
	}
	snap := h.s.Board.Snapshot()
	if waiting := h.s.submissionsWaiting(snap, h.s.deps.Now()); len(waiting) > 0 {
		var names, decide []string
		for i, w := range waiting {
			if i == holdListCap {
				names = append(names, fmt.Sprintf("%d more", len(waiting)-i))
				break
			}
			if m := int(w.Waited / time.Minute); m >= 1 {
				names = append(names, fmt.Sprintf("%s (%d min)", w.ID, m))
			} else {
				names = append(names, w.ID)
			}
			decide = append(decide, "task accept id="+w.ID)
		}
		verb := "waits"
		if len(waiting) > 1 {
			verb = "wait"
		}
		return fmt.Sprintf("Stop reading: %s %s in review, and the harness has already built and tested each submission on the merged tree. "+
			"Decide from its evidence now: %s, or task reject with what to fix; read more only for a doubt the evidence leaves.",
			joinNames(names), verb, strings.Join(decide, ", "))
	}
	if ready, free := h.s.startable(snap); len(ready) > 0 {
		shown := ready
		more := ""
		if len(shown) > holdListCap {
			shown, more = shown[:holdListCap], fmt.Sprintf(" (and %d more)", len(ready)-holdListCap)
		}
		calls := make([]string, len(shown))
		for i, id := range shown {
			calls[i] = "spawn task=" + id
		}
		return fmt.Sprintf("Stop reading: %s %s ready with no owner, and %s could start now. Your next calls are %s%s, all in this turn; "+
			"read what you still need once they run.", joinNames(shown), pluralWord(len(ready), "is", "are"), free, strings.Join(calls, ", "), more)
	}
	if n >= lookIdleAfter && h.s.teamLimited() && !openTask(snap) && !h.s.workerRunning() {
		scout := ""
		if slots, _ := TeamCounts(h.s.cfg.Team); slots["scout"] > 0 {
			scout = "spawn role=scout with what to map and what to report, or "
		}
		return fmt.Sprintf("Stop exploring: %d calls of your own in a row while the whole team is idle. Unless you handle this request yourself, "+
			"your next call is %stask create for the parts of the goal; each worker reads the code its own task needs.", n, scout)
	}
	return ""
}

// startable returns the ready tasks (to do, unowned, their dependencies done, not already queued for a member) that a member of
// their role could start now, a free slot or an idle member no pool keeps, with what could start them ("3 slots", "be-2").
// A team that does not limit roles can start every ready task.
func (s *Swarm) startable(snap *Snapshot) ([]string, string) {
	slots, _ := TeamCounts(s.cfg.Team)
	used, spare := map[string]int{}, map[string][]string{}
	s.mu.Lock()
	for _, m := range s.members {
		if m.manager || m.service {
			continue
		}
		used[m.role]++
		m.mu.Lock()
		if m.life == lifeIdle && poolHolder(snap, m.id) == "" {
			spare[m.role] = append(spare[m.role], m.id)
		}
		m.mu.Unlock()
	}
	queued := make(map[string]bool, len(s.queued))
	for id := range s.queued {
		queued[id] = true
	}
	s.mu.Unlock()
	var ready []string
	freeSlots, idle := 0, map[string]bool{}
	counted := map[string]bool{}
	for _, t := range snap.Tasks {
		if t.Status != StatusTodo || t.Owner != "" || queued[t.ID] || t.Parent != "" {
			continue
		}
		if ok, _ := depsDone(snap, t); !ok {
			continue
		}
		open := !s.teamLimited() || slots[t.Role]-used[t.Role] > 0 || len(spare[t.Role]) > 0
		if !open {
			continue
		}
		ready = append(ready, t.ID)
		if !counted[t.Role] {
			counted[t.Role] = true
			freeSlots += max(0, slots[t.Role]-used[t.Role])
			for _, id := range spare[t.Role] {
				idle[id] = true
			}
		}
	}
	var what []string
	if freeSlots > 0 {
		what = append(what, fmt.Sprintf("%d free %s", freeSlots, pluralWord(freeSlots, "slot", "slots")))
	}
	if len(idle) > 0 {
		what = append(what, fmt.Sprintf("%d idle %s", len(idle), pluralWord(len(idle), "member", "members")))
	}
	if len(what) == 0 {
		what = []string{"a member"}
	}
	return ready, strings.Join(what, " and ")
}
