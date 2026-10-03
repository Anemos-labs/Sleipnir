package session

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/swarm"
)

// ResolveResume finds the session directory a resume request names: "latest" is
// the newest session of this project that has something to resume, anything with
// a path separator is a directory, and anything else is a session id under the
// state directory.
func ResolveResume(home, root, spec string) (string, error) {
	spec = strings.TrimSpace(spec)
	sessions := filepath.Join(stateRoot(home), "sessions")
	switch {
	case spec == "" || spec == "latest":
		ents, err := os.ReadDir(sessions)
		if err != nil {
			return "", fmt.Errorf("no sessions to resume (%s)", sessions)
		}
		names := make([]string, 0, len(ents))
		for _, e := range ents {
			if e.IsDir() {
				names = append(names, e.Name())
			}
		}
		sort.Sort(sort.Reverse(sort.StringSlice(names))) // ids start with a timestamp
		for _, n := range names {
			d := filepath.Join(sessions, n)
			li := inspectLog(d)
			if li.root != root {
				continue
			}
			if li.isolated && !li.recovery {
				return "", fmt.Errorf("the newest session of %s (%s) predates isolated-team recovery metadata; its worktrees and log remain available for inspection", root, n)
			}
			if li.resumable() {
				return d, nil
			}
		}
		return "", fmt.Errorf("no session of %s has a finished turn to resume from", root)
	case strings.ContainsAny(spec, `/\`):
		if !hasLog(spec) {
			return "", fmt.Errorf("%s is not a session directory (no events.jsonl)", spec)
		}
		return checkResumable(spec)
	default:
		d := filepath.Join(sessions, spec)
		if !hasLog(d) {
			return "", fmt.Errorf("no session %q (looked in %s)", spec, sessions)
		}
		return checkResumable(d)
	}
}

// ResumedAsTeam says what shape the session a resume request names had: a team (true) or a single agent (false). known is false when the request
// names no session that can be resumed (the caller goes on with the default and the resume reports why it cannot).
func ResumedAsTeam(home, root, spec string) (team, known bool) {
	dir, err := ResolveResume(home, root, spec)
	if err != nil {
		return false, false
	}
	return inspectLog(dir).swarm, true
}

// checkResumable refuses a session that cannot be continued, in words that say why.
func checkResumable(dir string) (string, error) {
	li := inspectLog(dir)
	switch {
	case li.isolated && !li.recovery:
		return "", fmt.Errorf("%s predates isolated-team recovery metadata; its worktrees, log and checkpoints remain available for inspection", dir)
	case !li.resumable():
		return "", fmt.Errorf("%s has no snapshot of its agent (or manager): no turn finished, so there is nothing to resume from", dir)
	}
	return dir, nil
}

// Resumable reports whether a session has primary-agent context and, when it
// uses private worktrees, the isolation metadata needed to recover its base.
func Resumable(dir string) bool { return inspectLog(dir).resumable() }

// hasLog reports whether events.jsonl exists and is not a directory in a session directory.
func hasLog(dir string) bool {
	st, err := os.Stat(filepath.Join(dir, "events.jsonl"))
	return err == nil && !st.IsDir()
}

// scanHead calls fn for the events of a log until it returns false.
func scanHead(dir string, fn func(events.Event) bool) {
	f, err := os.Open(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 32<<20)
	for sc.Scan() {
		var e events.Event
		if json.Unmarshal(sc.Bytes(), &e) == nil && !fn(e) {
			return
		}
	}
}

// The agents a person talks to: a single agent is "main", and a team's manager is "mgr" (its role's short name).
const (
	soloAgent    = "main"
	managerAgent = "mgr"
)

// logInfo is what a resume decision needs to know about a session log.
type logInfo struct {
	id       string          // original session id, independent of a custom log directory
	root     string          // project root the session was started in ("" when unknown)
	swarm    bool            // the session ran a team
	isolated bool            // the team worked in git worktrees
	recovery bool            // the log fixes the original isolation base and mode
	snaps    map[string]bool // the agents that saved a snapshot
}

// resumable requires a primary snapshot and any necessary isolation metadata.
func (l logInfo) resumable() bool {
	return (l.snaps[soloAgent] || l.snaps[managerAgent]) && (!l.isolated || l.recovery)
}

// inspectLog reads the head of a session log: session.start (the first one
// decides root, swarm and isolation) and which agents saved a snapshot.
func inspectLog(dir string) logInfo {
	li := logInfo{snaps: map[string]bool{}}
	started := false
	scanHead(dir, func(e events.Event) bool {
		switch e.Type {
		case isolationEvent:
			li.recovery = true
			li.id = e.Session
		case events.TypeSessionStart:
			if !started {
				started = true
				var d struct {
					Root      string `json:"root"`
					Swarm     bool   `json:"swarm"`
					Isolation string `json:"isolation"`
				}
				_ = json.Unmarshal(e.Data, &d)
				li.root, li.swarm, li.isolated = d.Root, d.Swarm, d.Isolation != ""
			}
		case events.TypeAgentSnapshot:
			li.snaps[e.Agent] = true
		}
		return !(started && li.resumable())
	})
	return li
}

// restore recovers the primary conversation and task board. Isolated teams also
// recover their integration cursor and idle workers. Shared-tree sessions may
// switch between a solo agent and a manager; isolated sessions keep their shape.
func (s *Session) restore() error {
	var evs []events.Event
	if s.Swarm != nil {
		if err := events.Scan(filepath.Join(s.Dir, "events.jsonl"), func(e events.Event) error {
			evs = append(evs, e)
			return nil
		}); err != nil {
			var corrupt *events.CorruptError
			if s.iso != nil || !errors.As(err, &corrupt) {
				return fmt.Errorf("read team state: %w", err)
			}
			// planIsolation already reported skipped lines for ordinary sessions.
		}
		prev, err := swarm.ReplayBoard(evs)
		if err != nil {
			return err
		}
		if s.iso != nil && s.iso.resume != nil {
			if err := s.Swarm.RestoreIntegration(context.Background(), s.iso.applied); err != nil {
				return err
			}
			if err := s.restoreWorkers(prev, evs); err != nil {
				return err
			}
		} else {
			s.Swarm.Board.Restore(prev)
		}
	}
	a, from := s.Agent, ""
	if s.Swarm != nil {
		m, err := s.Swarm.StartIdleManager()
		if err != nil {
			return err
		}
		a = m
	}
	snap, from, err := primarySnapshot(s.Dir, a.ID())
	if err != nil {
		return err
	}
	if err := a.Restore(*snap); err != nil {
		return err
	}
	// What the restored thread points at must still resolve: folded turns (recall
	// turns=...) and truncated output (recall handle=...) live in the blob store,
	// their indexes in memory.
	if _, err := agent.RebuildArchiveFrom(s.Dir, from, a.ID(), s.archive); err != nil {
		s.notice("", "resume: the archive could not be re-indexed, so recall of folded turns may miss: "+err.Error())
	}
	if _, err := agent.RebuildHandles(s.Dir, s.handles); err != nil {
		s.notice("", "resume: recall handles could not be restored: "+err.Error())
	}
	return nil
}

// restoreWorkers joins roster, assignment, tree and snapshot records before any
// model runs. Retired IDs and costs remain part of the session's history.
func (s *Session) restoreWorkers(prev *swarm.Snapshot, evs []events.Event) error {
	workers := map[string]swarm.RecoveredWorker{}
	order := map[string]uint64{}
	snaps := map[string]bool{}
	selected := map[string]bool{}
	prepared := map[string]bool{}
	published := map[string]bool{}
	for _, a := range prev.Agents {
		if a.ID != managerAgent && a.Role != "mailman" {
			selected[a.ID] = true
		}
	}
	for _, e := range evs {
		switch e.Type {
		case events.TypeBoardOp:
			var op struct{ Op, Agent string }
			if err := json.Unmarshal(e.Data, &op); err != nil {
				return err
			}
			if op.Op == "agent" || op.Op == "agent-remove" {
				published[op.Agent] = true
			}
		case events.TypeAgentSnapshot:
			snaps[e.Agent] = true
		case events.TypeAgentSpawn, "agent.assign", "agent.prepare":
			var w struct{ ID, Role, Task string }
			if err := json.Unmarshal(e.Data, &w); err != nil {
				return err
			}
			if w.ID != "" && w.ID != managerAgent {
				workers[w.ID] = swarm.RecoveredWorker{ID: w.ID, Role: w.Role, Task: w.Task}
				order[w.ID] = e.Seq
				if e.Type == "agent.prepare" {
					prepared[w.ID] = true
				}
			}
		}
	}
	for _, task := range prev.Tasks {
		if task.Owner != "" && task.Owner != managerAgent && task.Status != swarm.StatusDone && task.Status != swarm.StatusFailed {
			// A prepare record and an owned task recover the window before roster
			// publication. Once published, only current roster membership counts:
			// retired workers can leave both dirty trees and review tasks behind.
			if !selected[task.Owner] && !(prepared[task.Owner] && !published[task.Owner]) {
				continue
			}
			selected[task.Owner] = true
			w := workers[task.Owner]
			w.Task = task.ID
			workers[task.Owner] = w
		}
	}
	for id := range selected {
		if workers[id].Role == "" {
			return fmt.Errorf("cannot recover %s: its worker role was not recorded", id)
		}
	}
	ids := map[string]string{}
	var restored []swarm.RecoveredWorker
	var retiredCost float64
	for id, w := range workers {
		ids[id] = w.Role
		if snaps[id] {
			var err error
			w.Snapshot, err = agent.LatestSnapshot(s.Dir, id)
			if err != nil {
				return err
			}
		}
		if selected[id] {
			restored = append(restored, w)
		} else if w.Snapshot != nil {
			retiredCost += w.Snapshot.CostUSD
		}
	}
	sort.Slice(restored, func(i, j int) bool { return order[restored[i].ID] > order[restored[j].ID] })
	if err := s.Swarm.RestoreTeam(context.Background(), prev, restored, ids, retiredCost); err != nil {
		return err
	}
	for _, w := range restored {
		if _, err := agent.RebuildArchiveFrom(s.Dir, w.ID, w.ID, s.archive); err != nil {
			s.notice(w.ID, "resume: archive could not be restored: "+err.Error())
		}
	}
	return nil
}

// primarySnapshot is the newest snapshot of the agent called want in a session directory, or, when that agent never saved one, of the other
// agent a person talks to (a session of a team resumed as one agent, and the other way round), made over to want. from names whose it was.
func primarySnapshot(dir, want string) (snap *agent.Snapshot, from string, err error) {
	snap, err = agent.LatestSnapshot(dir, want)
	if err == nil {
		return snap, want, nil
	}
	other := managerAgent
	if want == managerAgent {
		other = soloAgent
	}
	if alt, aerr := agent.LatestSnapshot(dir, other); aerr == nil {
		alt.Agent = want
		return alt, other, nil
	}
	return nil, "", err
}

// EarlierSessions are the directories of the sessions of this project that can be continued, newest first, at most n; the session itself
// is not among them. It reads only the head of a log, and only of the newest few hundred sessions.
func (s *Session) EarlierSessions(n int) []string {
	sessions := filepath.Join(stateRoot(s.opts.Home), "sessions")
	ents, err := os.ReadDir(sessions)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range ents {
		if e.IsDir() && e.Name() != s.ID {
			names = append(names, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names))) // ids start with a timestamp
	names = names[:min(len(names), 300)]
	var out []string
	for _, name := range names {
		d := filepath.Join(sessions, name)
		if li := inspectLog(d); li.root == s.opts.Root && li.resumable() {
			if out = append(out, d); len(out) == n {
				break
			}
		}
	}
	return out
}

// CheckResume says whether a resume request (what /resume was given: "" or "latest", an id, a directory) names something that can be
// resumed, so that a chat can say so and stay open instead of ending in the error of a start that cannot happen.
func (s *Session) CheckResume(spec string) error {
	_, err := ResolveResume(s.opts.Home, s.opts.Root, spec)
	return err
}
