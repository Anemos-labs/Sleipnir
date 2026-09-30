package inspect

import (
	"testing"

	"github.com/reee344/sleipnir/internal/events"
)

// The swarm's worktrees and merge queue, its mailman and the supervision of its manager
// are in the log as events; the inspector shows what they add up to, and shows nothing
// of them for a run that had none.
func TestInspectorShowsIsolationMailmanAndSupervision(t *testing.T) {
	dir := t.TempDir()
	log, err := events.Open(dir, "iso")
	if err != nil {
		t.Fatal(err)
	}
	log.Emit("", events.TypeSessionStart, map[string]any{"swarm": true, "isolation": "worktree", "mailman": true})
	for range 2 {
		log.Emit("swarm", events.TypeWorkspaceCreate, map[string]any{"agent": "be-1"})
	}
	log.Emit("swarm", events.TypeWorkspaceCommit, map[string]any{"agent": "be-1"})
	log.Emit("swarm", events.TypeMergeQueued, map[string]any{"agent": "be-1"})
	log.Emit("swarm", events.TypeMergeMerged, map[string]any{"agent": "be-1"})
	log.Emit("swarm", events.TypeMergeConflict, map[string]any{"agent": "fe-1"})
	log.Emit("be-1", events.TypeTaskMerge, map[string]any{"task": "T1", "outcome": "merged", "commit": "abc1234def5678", "files": []string{"a.go", "b.go"}})
	log.Emit("fe-1", events.TypeTaskMerge, map[string]any{"task": "T2", "outcome": "conflict", "reason": "a.go: both changed the same lines"})
	log.Emit("swarm", events.TypeSwarmIntegration, map[string]any{"branch": "sleipnir/s1/_integration", "tip": "abc1234def5678", "applied": true, "files": []string{"a.go", "b.go"}})
	for range 3 {
		log.Emit("be-1", events.TypeMailRoute, map[string]any{"id": "m1", "from": "be-1", "to": "mgr", "kind": "info"})
	}
	log.Emit("swarm", events.TypeMailBatch, map[string]any{"batch": "b1", "mailman": "mm-1", "recipients": 1, "parcels": 3})
	log.Emit("mm-1", events.TypeMailDigest, map[string]any{"id": "h1", "to": "mgr", "mailman": "mm-1", "parcels": []string{"m1", "m2", "m3"}})
	log.Emit("swarm", events.TypeMailDirect, map[string]any{"reason": "the mailman is down", "n": 2, "ids": []string{"m4", "m5"}})
	log.Emit("swarm", events.TypeMailmanState, map[string]any{"state": "down", "reason": "no digest within the bound"})
	log.Emit("mgr", events.TypeSwarmHold, map[string]any{"reason": "Not finished: running: be-1 (T1)"})
	log.Emit("mgr", events.TypeSwarmWake, map[string]any{"n": 1, "note": "While you were idle: T1 is in review (be-1)"})
	log.Emit("be-1", events.TypeSwarmWakeLimit, map[string]any{"limit": 40, "task": "T1"})
	log.Emit("", events.TypeSessionEnd, map[string]any{"cost_usd": 0.5, "reason": "completed"})
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	sess := mustLoad(t, dir)
	rep := sess.Swarm()
	iso := rep.Isolation
	if iso == nil {
		t.Fatal("no isolation section for a worktree run")
	}
	if iso.Trees.Created != 2 || iso.Trees.Commits != 1 || iso.Queue["queued"] != 1 || iso.Queue["merged"] != 1 || iso.Queue["conflict"] != 1 {
		t.Errorf("trees and queue: %+v %+v", iso.Trees, iso.Queue)
	}
	if iso.Submissions["merged"] != 1 || iso.Submissions["conflict"] != 1 || len(iso.Merges) != 2 || iso.Merges[0].Task != "T2" || iso.Merges[1].Commit != "abc1234def56" || len(iso.Merges[1].Files) != 2 {
		t.Errorf("submissions: %+v %+v", iso.Submissions, iso.Merges)
	}
	if in := iso.Integration; in == nil || !in.Applied || in.Files != 2 || in.Branch != "sleipnir/s1/_integration" {
		t.Errorf("integration: %+v", iso.Integration)
	}
	mm := rep.Mailman
	if mm == nil || mm.Routed != 3 || mm.Batches != 1 || mm.Parcels != 3 || mm.Digests != 1 || mm.Digested != 3 || mm.DirectMessages != 2 || mm.Outages != 1 || mm.State != "down" || mm.Direct["the mailman is down"] != 2 {
		t.Errorf("mailman: %+v", mm)
	}
	sup := rep.Supervision
	if sup == nil || sup.Holds != 1 || sup.Wakes != 1 || sup.WakeLimits != 1 || sup.LastHold == "" || sup.LastWake == "" {
		t.Errorf("supervision: %+v", sup)
	}
	meta := sess.Summary().Session
	if meta.Isolation != "worktree" || !meta.Mailman || meta.EndReason != "completed" {
		t.Errorf("session meta: %+v", meta)
	}
}

// A run with neither has neither section.
func TestInspectorHasNoIsolationOrMailmanSectionForAPlainSwarm(t *testing.T) {
	dir := t.TempDir()
	log, err := events.Open(dir, "plain")
	if err != nil {
		t.Fatal(err)
	}
	log.Emit("", events.TypeSessionStart, map[string]any{"swarm": true})
	log.Emit("", events.TypeSessionEnd, map[string]any{"cost_usd": 0.1})
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	rep := mustLoad(t, dir).Swarm()
	if rep.Isolation != nil || rep.Mailman != nil || rep.Supervision != nil {
		t.Errorf("sections for a plain swarm: %+v %+v %+v", rep.Isolation, rep.Mailman, rep.Supervision)
	}
}

// A worktree run that ended before any tree was made still says it was isolated.
func TestInspectorShowsIsolationFromTheSessionStartAlone(t *testing.T) {
	dir := t.TempDir()
	log, err := events.Open(dir, "early")
	if err != nil {
		t.Fatal(err)
	}
	log.Emit("", events.TypeSessionStart, map[string]any{"swarm": true, "isolation": "worktree"})
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	if iso := mustLoad(t, dir).Swarm().Isolation; iso == nil || len(iso.Merges) != 0 {
		t.Errorf("isolation from session.start alone: %+v", iso)
	}
}
