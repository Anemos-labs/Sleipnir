package swarm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/kv"
	"github.com/anemos-labs/sleipnir/internal/workspace"
)

// RecoveredWorker describes a worker to restore, newest assignment first. A nil
// snapshot means the process stopped before its initial context was recorded;
// its preserved worktree and task remain available, with fresh read-state checks.
type RecoveredWorker struct {
	ID, Role, Task string
	Snapshot       *agent.Snapshot
}

// ReserveIDs makes the agent ids of an earlier process (agent id -> role, from
// its log) unavailable to new spawns: a worker started after a resume gets a
// number past them, so two agents never share an id, or request ids, in one
// log. It must run before Start.
func (s *Swarm) ReserveIDs(ids map[string]string) {
	for id, role := range ids {
		if r, ok := s.roles[role]; ok {
			if suffix, ok := strings.CutPrefix(id, r.Short+"-"); ok {
				if n, err := strconv.Atoi(suffix); err == nil {
					s.seq[role] = max(s.seq[role], n)
				}
			}
		}
	}
}

// RestoreTeam reconnects idle workers to their private trees and reserves their
// unfinished tasks. It must run before Start, against an empty task board. IDs
// includes retired workers so a new spawn cannot collide with their branches.
func (s *Swarm) RestoreTeam(ctx context.Context, prev *Snapshot, workers []RecoveredWorker, ids map[string]string, retiredCost float64) error {
	// Configuration order is preserved across assignments and process restarts.
	workers = append([]RecoveredWorker(nil), workers...)
	sort.SliceStable(workers, func(i, j int) bool {
		a, b := workers[i].ID, workers[j].ID
		return len(a) < len(b) || len(a) == len(b) && a < b
	})
	if len(workers) > s.cfg.MaxWorkers {
		return fmt.Errorf("this team needs %d worker slots to recover its workers; resume with --swarm %d", len(workers), len(workers))
	}
	for _, w := range workers {
		r, ok := s.roles[w.Role]
		if !ok || r.Name == "manager" || s.isService(w.Role) {
			return fmt.Errorf("cannot recover worker %s: role %q is unavailable", w.ID, w.Role)
		}
		if err := workspace.ValidateAgentID(w.ID); err != nil {
			return err
		}
		if s.get(w.ID) != nil {
			return fmt.Errorf("worker %s is already registered", w.ID)
		}
		if w.Snapshot != nil && w.Snapshot.Role != w.Role {
			return fmt.Errorf("worker %s's recorded role changed", w.ID)
		}
		if s.isolated() && r.ReadOnly {
			path := filepath.Join(s.deps.Isolation.Manager.Dir, w.ID)
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				return fmt.Errorf("cannot recover %s as read-only while its private worktree exists at %s", w.ID, path)
			}
		}
	}
	s.ReserveIDs(ids)
	owners := map[string]string{}
	var restored []*member
	for _, w := range workers {
		role := s.roles[w.Role]
		var tree *workspace.Tree
		var err error
		task, hasTask := prev.Task(w.Task)
		unfinished := hasTask && task.Status != StatusDone && task.Status != StatusFailed
		if s.isolated() && !role.ReadOnly {
			path := filepath.Join(s.deps.Isolation.Manager.Dir, w.ID)
			if _, err := os.Lstat(path); os.IsNotExist(err) && unfinished && w.Snapshot != nil {
				return fmt.Errorf("worker %s's unfinished worktree is missing at %s; restore it before resuming", w.ID, path)
			}
			tree, err = s.deps.Isolation.Manager.Create(ctx, w.ID, workspace.CreateOptions{Reuse: true, Base: s.deps.Isolation.Queue.Tip()})
			if err != nil {
				return fmt.Errorf("recover worker %s: %w", w.ID, err)
			}
		}
		card := "The previous process stopped. Inspect the task and current files before continuing; previous reads no longer authorize edits."
		if hasTask && task.Owner == w.ID {
			card += "\n" + taskCard(task, w.ID, true)
		}
		if tree != nil {
			card += isolationCard(integrationRefOf(tree))
		}
		notes := kv.NewLayer("notes:"+w.ID, kv.KindNotes, 1, []kv.Segment{{Key: "assignment", Text: card, Vol: kv.VolFrozen}})
		m, err := s.newMember(w.ID, role, notes, NewEvidence(), tree)
		if err != nil {
			return err
		}
		if w.Snapshot != nil {
			if err := m.a.Restore(*w.Snapshot); err != nil {
				_ = m.a.Close()
				return err
			}
		}
		m.recovered = true
		if hasTask && task.Owner == w.ID {
			m.task = w.Task
		}
		s.register(m, s.currentShared())
		s.bindTree(m)
		restored = append(restored, m)
		if unfinished && task.Owner == w.ID && owners[task.ID] == "" {
			owners[task.ID] = w.ID
		}
	}
	s.spent += retiredCost
	// A pool leader can own its parent and a child simultaneously. Recover all
	// still-owned assignments, not only the worker's most recent task card.
	for _, task := range prev.Tasks {
		if task.Status == StatusDone || task.Status == StatusFailed || task.Owner == "" {
			continue
		}
		for _, m := range restored {
			if task.Owner == m.id {
				owners[task.ID] = m.id
				break
			}
		}
	}
	s.Board.RestoreOwners(prev, owners)
	// Only a worker that holds an unfinished task waits for it to restart; one whose last task is settled is plain idle, free for new
	// work (a line saying it waits would tell the manager it is taken).
	held := map[string][]string{}
	for task, owner := range owners {
		held[owner] = append(held[owner], task)
	}
	for _, m := range restored {
		line := ""
		if ts := held[m.id]; len(ts) > 0 {
			sort.Strings(ts)
			line = "restored; waiting for " + strings.Join(ts, ", ") + " to restart"
		}
		m.setState(s, "idle", line)
	}
	return nil
}
