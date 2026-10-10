package state

import (
	"strconv"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// mergeState is the verified merge queue of a worktree-isolated run, as the events show it.
type mergeState struct {
	waiting []MergeEntry
	recent  ring[MergeEntry]
	counts  MergeCounts
	trees   int
	integ   *Integration
	seen    bool
}

// newMergeState initializes bounded recent merge history.
func newMergeState() mergeState { return mergeState{recent: newRing[MergeEntry](MergeCap)} }

// taskRef reads the task id out of the name the swarm gives a submission, "T3: the task's title" (swarm.integrate), "" if it has none.
func taskRef(subject string) string {
	i := 0
	if len(subject) < 2 || subject[0] != 'T' {
		return ""
	}
	for i = 1; i < len(subject) && isDigit(subject[i]); i++ {
	}
	if i == 1 || (i < len(subject) && subject[i] != ':' && subject[i] != ' ') {
		return ""
	}
	return subject[:i]
}

// mergeWire is what the workspace layer and the swarm write for the merge queue and the trees (internal/workspace/queue.go,
// internal/swarm/isolate.go); one struct holds the fields of all of those events.
type mergeWire struct {
	Task      string   `json:"task"`
	Position  int      `json:"position"`
	Files     []string `json:"files"`
	FileCount int      `json:"file_count"`
	Hunks     int      `json:"hunks"`
	Reason    string   `json:"reason"`
	Empty     bool     `json:"empty"`
	Cmd       string   `json:"cmd"`
	ExitCode  *int     `json:"exit_code"`
	TimedOut  bool     `json:"timed_out"`
	After     string   `json:"after"`
	Commit    string   `json:"commit"`
	Outcome   string   `json:"outcome"`
	Error     string   `json:"error"`
	Verified  bool     `json:"verified"`
	Branch    string   `json:"branch"`
	Tip       string   `json:"tip"`
	Applied   bool     `json:"applied"`
	Committed bool     `json:"committed"`
	To        string   `json:"to"`
}

func (s *State) onMerge(e events.Event, t time.Time) {
	var p mergeWire
	if !s.decode(e.Data, maxPayload, &p) {
		return
	}
	m := &s.merge
	m.seen = true
	agent := clip(e.Agent, textID)
	switch e.Type {
	case events.TypeWorkspaceCreate:
		m.trees++
	case events.TypeWorkspaceRemove:
		m.trees = max(m.trees-1, 0)
	case events.TypeWorkspacePrune, events.TypeWorkspaceCommit, events.TypeWorkspaceReset:
		// Housekeeping of the trees: nothing to show.
	case events.TypeMergeQueued:
		ent := MergeEntry{Seq: e.Seq, T: t, Agent: agent, Task: taskRef(p.Task), Title: clean(p.Task, textLine), Stage: MergeQueued, Position: clampInt(p.Position)}
		if len(m.waiting) >= MaxMergeWaiting {
			m.waiting = append(m.waiting[:0], m.waiting[1:]...)
		}
		m.waiting = append(m.waiting, ent)
		m.counts.Queued++
		s.setQueued(ent.Task, true)
		s.line(e.Seq, t, agent, FeedMerge, GlyphMerge, firstOf(ent.Task, "work")+" queued for merge", "position "+strconv.Itoa(ent.Position))
	case events.TypeMergeMerged:
		ent := s.settle(e, t, p.Task, MergeMerged)
		ent.Commit, ent.Verified = commitID(p.After), p.Verified
		ent.Files, ent.FileCount = clipList(p.Files, MaxFiles, textPath), max(clampInt(p.FileCount), len(p.Files))
		m.counts.Merged++
		s.landed(ent.Task, "merged", ent.Commit)
		s.finish(ent)
		s.line(e.Seq, t, agent, FeedMerge, GlyphOK, firstOf(ent.Task, "work")+" merged ("+ent.Commit+")", strconv.Itoa(ent.FileCount)+" files")
	case events.TypeMergeConflict:
		ent := s.settle(e, t, p.Task, MergeConflict)
		ent.Files, ent.FileCount = clipList(p.Files, MaxFiles, textPath), len(p.Files)
		ent.Reason = strconv.Itoa(len(p.Files)) + " files, " + strconv.Itoa(clampInt(p.Hunks)) + " hunks"
		m.counts.Conflicts++
		m.counts.Bounced++
		s.finish(ent)
		s.line(e.Seq, t, agent, FeedMerge, GlyphWarn, firstOf(ent.Task, "work")+" conflicts with what was merged: sent back to "+firstOf(agent, "its worker"), ent.Reason)
	case events.TypeMergeVerifyFail:
		ent := s.settle(e, t, p.Task, MergeVerifyFail)
		ent.ExitCode = p.ExitCode
		ent.Reason = clean(p.Cmd, textShort)
		if p.TimedOut {
			ent.Reason += " (timed out)"
		} else if p.ExitCode != nil {
			ent.Reason += " (exit " + strconv.Itoa(*p.ExitCode) + ")"
		}
		m.counts.VerifyFail++
		m.counts.Bounced++
		s.finish(ent)
		s.line(e.Seq, t, agent, FeedMerge, GlyphFail, firstOf(ent.Task, "work")+" failed verification after the merge: rolled back", ent.Reason)
	case events.TypeMergeRolledBack:
		m.counts.RolledBack++
		for i := m.recent.len() - 1; i >= 0; i-- {
			if ent := m.recent.at(i); ent.Agent == agent && ent.Stage == MergeVerifyFail {
				ent.Reason = clean(ent.Reason+" · rolled back", textLine)
				break
			}
		}
	case events.TypeMergeRejected:
		stage := MergeRejected
		if p.Empty {
			stage = MergeEmpty
		}
		ent := s.settle(e, t, p.Task, stage)
		ent.Reason = clean(p.Reason, textLine)
		if p.Empty {
			m.counts.Empty++
			s.landed(ent.Task, "empty", "")
			s.finish(ent)
			s.line(e.Seq, t, agent, FeedMerge, GlyphMerge, firstOf(ent.Task, "work")+" had nothing to merge", ent.Reason)
		} else {
			m.counts.Rejected++
			m.counts.Bounced++
			s.finish(ent)
			s.line(e.Seq, t, agent, FeedMerge, GlyphFail, firstOf(ent.Task, "work")+" was refused by the merge queue", ent.Reason)
		}
	case events.TypeMergeFastFwd:
		s.line(e.Seq, t, agent, FeedMerge, GlyphMerge, "branch "+clip(p.Branch, textID)+" moved to the integration tip", commitID(p.To))
	case events.TypeTaskMerge:
		id := clip(p.Task, textID)
		switch p.Outcome {
		case "merged", "empty":
			s.landed(id, p.Outcome, commitID(p.Commit))
		case "error":
			ent := s.settle(e, t, id, MergeError)
			ent.Reason = clean(p.Error, textLine)
			m.counts.Errors++
			s.finish(ent)
			s.line(e.Seq, t, agent, FeedMerge, GlyphFail, id+": the merge could not run", ent.Reason)
		}
		s.setQueued(id, false)
	case events.TypeSwarmIntegration:
		m.integ = &Integration{T: t, Branch: clip(p.Branch, textPath), Tip: commitID(p.Tip), Applied: p.Applied, Committed: p.Committed, Files: len(p.Files), Reason: clean(p.Reason, textLine)}
		text, glyph := "the merged work was applied to your checkout", GlyphOK
		if !p.Applied {
			text, glyph = "the merged work was NOT applied to your checkout", GlyphWarn
		}
		s.line(e.Seq, t, "", FeedMerge, glyph, text, m.integ.Reason)
	}
}

// settle takes the waiting submission the event is about (the agent's, and the task's when it is named) off the queue and returns a
// new entry for its outcome; an outcome whose submission was never seen queued still gets an entry.
func (s *State) settle(e events.Event, t time.Time, subject string, stage MergeStage) *MergeEntry {
	m := &s.merge
	agent, task := clip(e.Agent, textID), taskRef(subject)
	ent := MergeEntry{Seq: e.Seq, T: t, Agent: agent, Task: task, Title: clean(subject, textLine), Stage: stage}
	for i := range m.waiting {
		w := &m.waiting[i]
		if w.Agent == agent && (task == "" || w.Task == task || w.Title == ent.Title) {
			ent.Position = w.Position
			if ent.Task == "" {
				ent.Task = w.Task
			}
			m.waiting = append(m.waiting[:i], m.waiting[i+1:]...)
			break
		}
	}
	s.setQueued(ent.Task, false)
	return &ent
}

// finish puts a settled entry in the ring of recent ones.
func (s *State) finish(ent *MergeEntry) { s.merge.recent.push(*ent) }

// setQueued tells the task whether a submission of it is waiting in the merge queue.
func (s *State) setQueued(id string, queued bool) {
	if ts := s.tasks[id]; ts != nil {
		ts.queued = queued
		s.touchTask(id)
	}
}

// landed records that the work of the task is in the integration branch.
func (s *State) landed(id, outcome, commit string) {
	if ts := s.tasks[id]; ts != nil {
		ts.Merge, ts.Commit = outcome, commit
		s.touchTask(id)
	}
}

// mergeSnapshot copies the merge queue out.
func (s *State) mergeSnapshot() MergeQueue {
	m := &s.merge
	out := MergeQueue{Counts: m.counts, Trees: m.trees, Seen: m.seen}
	cp := func(in []MergeEntry) []MergeEntry {
		if len(in) == 0 {
			return nil
		}
		out := make([]MergeEntry, len(in))
		for i, e := range in {
			e.Files = append([]string(nil), e.Files...)
			if len(e.Files) == 0 {
				e.Files = nil
			}
			if e.ExitCode != nil {
				v := *e.ExitCode
				e.ExitCode = &v
			}
			out[i] = e
		}
		return out
	}
	out.Waiting, out.Recent = cp(m.waiting), cp(m.recent.slice())
	if m.integ != nil {
		v := *m.integ
		out.Integration = &v
	}
	return out
}

// commitID is the first 12 characters of a commit id, kept only if it is one (hex): what a payload names as a commit is shown, so
// it must look like one.
func commitID(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 12 {
		s = s[:12]
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return ""
		}
	}
	return s
}

// sharedSeen and noteShared remember which shared layers (G1 hashes) have been sent, so that an agent whose first request carries
// one that another agent already used can be shown as inheriting it.
func (s *State) sharedSeen(hash string) bool {
	if hash == "" {
		return false
	}
	for _, h := range s.shared {
		if h == hash {
			return true
		}
	}
	return false
}

// noteShared retains distinct nonempty shared-prefix hashes up to MaxPrefixes, evicting the oldest
// when full.
func (s *State) noteShared(hash string) {
	if hash == "" || s.sharedSeen(hash) {
		return
	}
	if len(s.shared) >= MaxPrefixes {
		s.shared = append(s.shared[:0], s.shared[1:]...)
	}
	s.shared = append(s.shared, hash)
}
