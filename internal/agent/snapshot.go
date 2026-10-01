package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/kv"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// snapshotVersion is the format of Snapshot. A snapshot of another version is
// refused rather than guessed at.
const snapshotVersion = 1

// Snapshot is what a later process needs to resume this agent: its thread, notes
// and spine, and the counters that keep request ids and cost totals continuous.
// It is written as a blob at the end of every Run and after every compaction
// commit, and referenced by an agent.snapshot event.
//
// The cached prefix is not part of it. A resumed agent renders the shared layers
// from the project as it is now, which may differ from the session's, so
// Restore is a declared rebase: thinking blocks bound to the old prefix are
// stripped where the route binds them, and the first request re-writes the
// prefix once. The file-read state is not kept either: after a resume an edit
// needs the file to be read again, which is what the staleness check exists to
// guarantee.
type Snapshot struct {
	Version     int         `json:"version"`
	Agent       string      `json:"agent"`
	Role        string      `json:"role"`
	Model       string      `json:"model"`
	Renderer    string      `json:"renderer"`
	Turns       []core.Turn `json:"turns"`
	NextTurn    core.TurnID `json:"next_turn"`
	ThreadEpoch uint64      `json:"thread_epoch"`
	Spine       *LayerState `json:"spine,omitempty"`
	Notes       *LayerState `json:"notes,omitempty"`
	Epoch       uint64      `json:"epoch"`
	Requests    int         `json:"requests"`
	Forks       int         `json:"forks"`
	Compactions int         `json:"compactions"`
	Usage       core.Usage  `json:"usage"`
	CostUSD     float64     `json:"cost_usd"`
}

// LayerState is a layer's content, enough to rebuild it.
type LayerState struct {
	ID       string       `json:"id"`
	Kind     kv.Kind      `json:"kind"`
	Version  uint64       `json:"version"`
	Segments []kv.Segment `json:"segments"`
}

func layerState(l *kv.Layer) *LayerState {
	if l.Empty() {
		return nil
	}
	return &LayerState{ID: l.ID, Kind: l.Kind, Version: l.Version, Segments: append([]kv.Segment(nil), l.Segments...)}
}

func (s *LayerState) layer() *kv.Layer {
	if s == nil {
		return nil
	}
	return kv.NewLayer(s.ID, s.Kind, s.Version, s.Segments)
}

// Snapshot captures the agent at a quiescent point (between runs, or at a turn
// boundary).
func (a *Agent) Snapshot() Snapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	th := a.thread.Snapshot()
	return Snapshot{
		Version: snapshotVersion, Agent: a.cfg.ID, Role: a.cfg.Role, Model: a.cfg.Model.ID, Renderer: kv.RendererVersion,
		Turns: th.Turns, NextTurn: th.NextID, ThreadEpoch: th.Epoch,
		Spine: layerState(a.stack.Spine), Notes: layerState(a.stack.Notes),
		Epoch: a.epoch, Requests: a.reqN, Forks: a.forkN, Compactions: a.comp.count,
		Usage: a.usage, CostUSD: a.costUSD,
	}
}

// ErrNotEmpty is returned by Restore for an agent that has already run.
var ErrNotEmpty = errors.New("agent: only a fresh agent can be restored")

// Restore brings a snapshot back before the agent's first Run. It is a declared
// rebase (see Snapshot): the epoch advances, thinking that the new prefix voids is
// stripped in the same critical section that installs the thread, and the hot
// notice bookkeeping is re-derived from the restored bytes, which are kept
// verbatim.
func (a *Agent) Restore(s Snapshot) error {
	if s.Version != snapshotVersion {
		return fmt.Errorf("agent: snapshot version %d is not supported (this build reads %d)", s.Version, snapshotVersion)
	}
	if s.Agent != a.cfg.ID {
		return fmt.Errorf("agent: snapshot belongs to %q, not %q", s.Agent, a.cfg.ID)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.reqN != 0 || len(a.thread.Snapshot().Turns) != 0 {
		return ErrNotEmpty
	}
	if err := a.thread.Restore(s.Turns, s.NextTurn, s.ThreadEpoch); err != nil {
		return err
	}
	if sp := s.Spine.layer(); sp != nil {
		a.stack.Spine = sp
	}
	if n := s.Notes.layer(); n != nil {
		a.stack.Notes = n
	}
	a.epoch = s.Epoch + 1
	a.reqN, a.forkN, a.comp.count = s.Requests, s.Forks, s.Compactions
	a.usage, a.costUSD = s.Usage, s.CostUSD
	a.man = core.ManifestState{} // the next request is encoded in full: a new segment of the log
	if a.cfg.Provider.Profile().ReplayThinking && a.cfg.ApplyPolicy.StripThinking {
		a.thread.Rewrite(func(t core.Turn) (core.Turn, bool) { return kv.StripThinkingTurn(t) })
	}
	a.resyncHotLocked()
	a.emit(events.TypeAgentRestore, map[string]any{
		"turns": len(s.Turns), "requests": s.Requests, "spine": s.Spine != nil, "notes": s.Notes != nil,
	})
	return nil
}

// saveSnapshot records a snapshot in the blob store and the log, unless nothing
// changed since the last one. A failure is reported and never stops the agent: a
// missing snapshot costs a resume, not the work in hand.
func (a *Agent) saveSnapshot() {
	snap := a.Snapshot()
	b, err := json.Marshal(snap)
	if err != nil {
		a.cfg.Sink.Notice(a.cfg.ID, "warn", "snapshot: "+err.Error())
		return
	}
	h, err := a.cfg.Blobs.Put(b)
	if err != nil {
		a.cfg.Sink.Notice(a.cfg.ID, "warn", "snapshot: "+err.Error())
		return
	}
	a.mu.Lock()
	same := a.lastSnap == h
	a.lastSnap = h
	a.mu.Unlock()
	if same {
		return
	}
	a.emit(events.TypeAgentSnapshot, map[string]any{"blob": h, "turns": len(snap.Turns), "bytes": len(b), "requests": snap.Requests})
}

// LatestSnapshot finds the newest snapshot of agentID in a session directory
// (its events.jsonl and blobs/), for resuming. A missing snapshot is an error
// naming the reason; an old session that never finished a turn has none.
func LatestSnapshot(dir, agentID string) (*Snapshot, error) {
	blobs, err := events.NewDirBlobs(filepath.Join(dir, "blobs"))
	if err != nil {
		return nil, err
	}
	var last core.Hash
	err = events.Scan(filepath.Join(dir, "events.jsonl"), func(e events.Event) error {
		if e.Type == events.TypeAgentSnapshot && e.Agent == agentID {
			var d struct {
				Blob core.Hash `json:"blob"`
			}
			if json.Unmarshal(e.Data, &d) == nil && d.Blob != "" {
				last = d.Blob
			}
		}
		return nil
	})
	var ce *events.CorruptError
	if err != nil && !errors.As(err, &ce) {
		return nil, err
	}
	if last == "" {
		return nil, fmt.Errorf("no snapshot of %s in %s: the session never finished a turn", agentID, dir)
	}
	raw, err := blobs.Get(last)
	if err != nil {
		return nil, fmt.Errorf("snapshot %s: %w", last.Short(), err)
	}
	var s Snapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("snapshot %s: %w", last.Short(), err)
	}
	return &s, nil
}

// RebuildArchive re-indexes every turn an agent appended to a session log, so that
// recall keeps working after a resume for the turns compaction folded away before
// it: the archive's index lives in memory, the turns themselves in the blob store.
// It returns how many turns it indexed. A damaged tail of the log is not an error
// (the same rule as LatestSnapshot).
func RebuildArchive(dir, agentID string, ar *kv.Archive) (int, error) {
	n := 0
	err := events.Scan(filepath.Join(dir, "events.jsonl"), func(e events.Event) error {
		if e.Type != events.TypeTurnAppend || e.Agent != agentID {
			return nil
		}
		var t core.Turn
		if json.Unmarshal(e.Data, &t) == nil && t.ID > 0 {
			if ar.Put(agentID, t) == nil {
				n++
			}
		}
		return nil
	})
	var ce *events.CorruptError
	if err != nil && !errors.As(err, &ce) {
		return n, err
	}
	return n, nil
}

// RebuildHandles mints again the recall handles (out_...) that earlier runs of the
// session issued for truncated and spilled tool output, from the log, in the order
// they were issued: handles are a function of the blob hashes and that order, so the
// ones the restored thread mentions resolve to the same blobs as before.
func RebuildHandles(dir string, h *tools.Handles) (int, error) {
	n := 0
	err := events.Scan(filepath.Join(dir, "events.jsonl"), func(e events.Event) error {
		if e.Type != events.TypeToolResult && e.Type != "tool.spill" {
			return nil
		}
		var d struct {
			Handle    string    `json:"handle"`
			Ref       core.Hash `json:"ref"`
			Chars     int       `json:"chars"`      // tool.spill: the whole result
			FullChars int       `json:"full_chars"` // tool.result: the whole output behind a truncated one
		}
		if json.Unmarshal(e.Data, &d) != nil || d.Handle == "" || d.Ref == "" {
			return nil
		}
		size := d.FullChars
		if e.Type == "tool.spill" {
			size = d.Chars
		}
		if h.Add(d.Ref, size) == d.Handle {
			n++
		}
		return nil
	})
	var ce *events.CorruptError
	if err != nil && !errors.As(err, &ce) {
		return n, err
	}
	return n, nil
}
