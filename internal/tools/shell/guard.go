package shell

import (
	"fmt"
	"time"

	"github.com/anemos-labs/sleipnir/internal/machine"
)

// guardVictimMin is the least memory a command must hold for the guard to stop it: when even the largest of the agents'
// commands is smaller, something else is eating the machine, and stopping a small command would free nothing.
const guardVictimMin = 64 << 20

// guardCooldown is how long the guard waits after stopping a command before it looks again: the memory takes a moment to
// come back, and stopping a second command for the shortage the first one caused would be one too many.
const guardCooldown = 5 * time.Second

// guardMemory stops the agents' command that holds the most memory whenever the machine's free memory falls below half the
// reserve (machine.Budget.MemoryLow), one command at a time, until Shutdown. A machine that runs out of memory does not fail
// one command: it swaps until nothing on it answers, the person's own programs included, or the kernel kills whatever it
// likes. The command stopped gets a result that says why, so its agent can run less at once. It returns once the Manager
// has no process left (track starts it again with the next one).
func (m *Manager) guardMemory() {
	t := time.NewTicker(m.opts.MemoryCheck)
	defer t.Stop()
	var quiet time.Time
	for {
		select {
		case <-m.stop:
			m.mu.Lock()
			m.guarding = false
			m.mu.Unlock()
			return
		case <-t.C:
		}
		m.mu.Lock()
		if len(m.procs) == 0 && len(m.lingering) == 0 {
			m.guarding = false
			m.mu.Unlock()
			return
		}
		m.mu.Unlock()
		if time.Now().Before(quiet) {
			continue
		}
		free, low := m.opts.Machine.MemoryLow()
		if !low {
			continue
		}
		if m.stopLargest(free) {
			quiet = time.Now().Add(guardCooldown)
		}
	}
}

// stopLargest stops the process group of the Manager's that holds the most memory, if it holds at least guardVictimMin:
// a running command or background job, what a finished command left running, or a lingering group. It reports whether it
// stopped one.
func (m *Manager) stopLargest(free int64) bool {
	m.mu.Lock()
	byPgid := map[int]*proc{}
	pgids := make([]int, 0, len(m.procs)+len(m.lingering))
	for p := range m.procs {
		byPgid[p.pid] = p
		pgids = append(pgids, p.pid)
	}
	for g := range m.lingering {
		if byPgid[g] == nil {
			pgids = append(pgids, g)
		}
	}
	m.mu.Unlock()
	if len(pgids) == 0 {
		return false
	}
	victim, most := 0, int64(0)
	for g, rss := range machine.GroupRSS(pgids) {
		if rss > most || (rss == most && g < victim) {
			victim, most = g, rss
		}
	}
	if most < guardVictimMin {
		return false
	}
	note := fmt.Sprintf("[stopped by Sleipnir: the machine was running out of memory (%s free) and this command's processes used the most of any agent command (%s). Run fewer memory-hungry commands at once, or make this one use less]",
		machine.Bytes(free), machine.Bytes(most))
	if p := byPgid[victim]; p != nil {
		p.memNote.Store(&note)
		if p.leaderDone() {
			go p.killTree() // what the command left running: no supervisor is listening any more
		} else {
			p.requestKill(killMemory)
		}
		return true
	}
	m.mu.Lock()
	delete(m.lingering, victim)
	m.mu.Unlock()
	go m.endGroup(victim)
	return true
}
