package shell

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// StrayPrefix starts the id of a stray process group (StrayInfo.ID): "p" and the group's id, as in "p4242".
const StrayPrefix = "p"

// maxStrayMembers bounds the processes listed for one stray group.
const maxStrayMembers = 16

// StrayInfo is what a person is shown of a stray: a process group that an agent's finished command left running (a server
// started with "server &" or "nohup server >log 2>&1 &" inside an ordinary command, or what a background job left once it ended).
// The harness ends strays when the agent's run ends (EndAgent) or the session stops (Shutdown); until then a person can see and
// stop them. A process that left its group on purpose (setsid, a double fork into a new session) is not seen.
type StrayInfo struct {
	ID      string    `json:"id"`              // StrayPrefix + the process-group id
	Agent   string    `json:"agent"`           // the agent whose command started it
	Command string    `json:"command"`         // that command's first line, clipped
	Started time.Time `json:"started"`         // when that command began
	FromJob bool      `json:"fromJob"`         // left by a background job that has ended
	Pipes   bool      `json:"pipes,omitempty"` // still holds the command's output pipes
	Procs   []Member  `json:"procs,omitempty"` // the live processes of the group, where the platform can list them
}

// Member is one live process of a stray group.
type Member struct {
	PID int    `json:"pid"`
	Cmd string `json:"cmd"` // its command line, clipped
}

// Strays lists the process groups that agents' finished commands left running, oldest first. A nil Manager has none.
func (m *Manager) Strays() []StrayInfo {
	if m == nil {
		return nil
	}
	type cand struct {
		pgid int
		info StrayInfo
	}
	var cs []cand
	m.mu.Lock()
	for p := range m.procs {
		if p.job || !p.leaderDone() || p.pumpsFinished() {
			continue
		}
		cs = append(cs, cand{p.pid, StrayInfo{Agent: p.agent, Command: p.command, Started: p.started, Pipes: true}})
	}
	for g, l := range m.lingering {
		cs = append(cs, cand{g, StrayInfo{Agent: l.agent, Command: l.command, Started: l.started, FromJob: l.job}})
	}
	m.mu.Unlock()
	if len(cs) == 0 {
		return nil
	}
	pgids := make([]int, len(cs))
	for i, c := range cs {
		pgids[i] = c.pgid
	}
	members, listed := groupMembers(pgids, maxStrayMembers)
	out := make([]StrayInfo, 0, len(cs))
	for _, c := range cs {
		if listed && len(members[c.pgid]) == 0 || !listed && probesGroup && !groupAlive(c.pgid) {
			continue // gone since; untrack forgets it on its next pass
		}
		c.info.ID = StrayPrefix + strconv.Itoa(c.pgid)
		c.info.Command = clip(firstLine(c.info.Command), 300)
		c.info.Procs = members[c.pgid]
		out = append(out, c.info)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Started.Equal(out[j].Started) {
			return out[i].Started.Before(out[j].Started)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// stopStray ends the stray process group id names (StrayPrefix + its group id): SIGTERM, then SIGKILL after the kill grace.
// ok is false when id does not name a stray the Manager knows.
func (m *Manager) stopStray(id string) (ok bool) {
	n, err := strconv.Atoi(strings.TrimPrefix(id, StrayPrefix))
	if !strings.HasPrefix(id, StrayPrefix) || err != nil || n <= 0 {
		return false
	}
	m.mu.Lock()
	var holder *proc
	for p := range m.procs {
		if p.pid == n && !p.job && p.leaderDone() {
			holder = p
			break
		}
	}
	_, lingers := m.lingering[n]
	delete(m.lingering, n)
	m.mu.Unlock()
	switch {
	case holder != nil:
		if !holder.pumpsFinished() {
			holder.killTree()
		} else {
			m.endGroup(n)
		}
		holder.closeReaders()
	case lingers:
		m.endGroup(n)
	default:
		return false
	}
	return true
}

// JobOutput is the end of a background job's output, at most max bytes of it (all of the buffer when max <= 0), as a person
// reads it: control sequences already taken out, with the number of earlier bytes it leaves out. It does not move any agent's
// read position (bash_output's cursor).
func (m *Manager) JobOutput(id string, max int) (text string, omitted int64, err error) {
	if m == nil {
		return "", 0, errNoJob(id)
	}
	j := m.job(strings.TrimSpace(id))
	if j == nil {
		return "", 0, errNoJob(id)
	}
	from := int64(0)
	if total := j.buf.total(); max > 0 && total > int64(max) {
		from = total - int64(max)
	}
	data, dropped, _ := j.buf.read(from)
	return tidyOutput(string(data)), from + dropped, nil
}

// JobCommand is a background job's whole command, as the agent wrote it.
func (m *Manager) JobCommand(id string) (string, error) {
	if m == nil {
		return "", errNoJob(id)
	}
	j := m.job(strings.TrimSpace(id))
	if j == nil {
		return "", errNoJob(id)
	}
	return j.command, nil
}
