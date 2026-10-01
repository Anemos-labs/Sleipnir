package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/kv"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
)

func workLoop(steps int) mock.Responder {
	return func(c *mock.Call) mock.Reply {
		if isCompactor(c) {
			return mock.Reply{Text: `{"keep_from":"t1","spine":[],"mask":[],"notes":[],"promote":[]}`}
		}
		n := 0
		for _, m := range c.Messages {
			if m.Role == "assistant" {
				n++
			}
		}
		if n < steps {
			return mock.Reply{Text: fmt.Sprintf("step %d", n+1), ToolCalls: []mock.ToolCall{{ID: fmt.Sprintf("call_%d", n+1), Name: "echo", Args: fmt.Sprintf(`{"n":%d}`, n+1)}}}
		}
		return mock.Reply{Text: fmt.Sprintf("finished after %d steps", n)}
	}
}

func TestSnapshotResumesTheConversationInAFreshAgent(t *testing.T) {
	blobs := events.NewMemBlobs()
	a := newRig(t, rigOpts{noCompact: true, blobs: blobs}, workLoop(3))
	if _, err := a.agent.Run(context.Background(), "investigate the flaky test"); err != nil {
		t.Fatal(err)
	}
	// Every Run ends with a snapshot in the log, pointing at a blob.
	evs := a.log.OfType(events.TypeAgentSnapshot)
	if len(evs) == 0 {
		t.Fatal("no agent.snapshot event after a Run")
	}
	usageBefore, costBefore := a.agent.Usage()

	// The snapshot survives a JSON round trip (it is stored as a blob).
	b, err := json.Marshal(a.agent.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	var snap agent.Snapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		t.Fatal(err)
	}

	// A fresh agent (another process, later) continues from it.
	var lastCall *mock.Call
	var seenMessages []string
	bRig := newRig(t, rigOpts{noCompact: true}, func(c *mock.Call) mock.Reply {
		lastCall = c
		seenMessages = nil
		for _, m := range c.Messages {
			seenMessages = append(seenMessages, m.Content)
		}
		return mock.Reply{Text: "picking up where we left off"}
	})
	if err := bRig.agent.Restore(snap); err != nil {
		t.Fatal(err)
	}
	if u, c := bRig.agent.Usage(); u != usageBefore || c != costBefore {
		t.Errorf("usage and cost must continue: %+v %v vs %+v %v", u, c, usageBefore, costBefore)
	}
	if _, err := bRig.agent.Run(context.Background(), "and now fix it"); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(seenMessages, "\n")
	for _, want := range []string{"investigate the flaky test", `echo:{"n":1}`, `echo:{"n":3}`, "finished after 3 steps", "and now fix it"} {
		if !strings.Contains(all, want) {
			t.Errorf("the resumed agent's prompt lacks %q", want)
		}
	}
	if lastCall == nil {
		t.Fatal("no request reached the model")
	}
	// Ids continue: the new turns follow the restored ones, and request ids do not restart.
	turns := bRig.agent.Thread().Snapshot().Turns
	for i := 1; i < len(turns); i++ {
		if turns[i].ID <= turns[i-1].ID {
			t.Fatalf("turn ids must keep increasing across a resume: %v", turns[i-1:i+1])
		}
	}
	var req string
	for _, e := range bRig.log.OfType(events.TypeModelRequest) {
		var m struct {
			Agent string `json:"agent"`
		}
		_ = json.Unmarshal(e.Data, &m)
		req = string(e.Data)
	}
	if !strings.Contains(req, `"be-1.`) || strings.Contains(req, `"req":"be-1.1"`) {
		t.Logf("last request event: %.200s", req)
	}
	if len(bRig.log.OfType(events.TypeAgentRestore)) != 1 {
		t.Error("a resume must be recorded")
	}
}

func TestRestoreRefusesWhatCannotBeRestored(t *testing.T) {
	src := newRig(t, rigOpts{noCompact: true}, workLoop(1))
	if _, err := src.agent.Run(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	snap := src.agent.Snapshot()

	// An agent that has already run.
	used := newRig(t, rigOpts{noCompact: true}, workLoop(1))
	if _, err := used.agent.Run(context.Background(), "already busy"); err != nil {
		t.Fatal(err)
	}
	if err := used.agent.Restore(snap); err == nil {
		t.Error("restoring into an agent that has run would discard its history")
	}
	// A snapshot of another format or another agent.
	fresh := newRig(t, rigOpts{noCompact: true}, workLoop(1))
	bad := snap
	bad.Version = 99
	if err := fresh.agent.Restore(bad); err == nil {
		t.Error("an unknown snapshot version must be refused")
	}
	other := snap
	other.Agent = "someone-else"
	if err := fresh.agent.Restore(other); err == nil {
		t.Error("a snapshot of another agent must be refused")
	}
	if err := fresh.agent.Restore(snap); err != nil {
		t.Fatalf("the intact snapshot restores: %v", err)
	}
}

func TestLatestSnapshotReadsTheSessionDirectory(t *testing.T) {
	dir := t.TempDir()
	log, err := events.Open(dir, "s1")
	if err != nil {
		t.Fatal(err)
	}
	blobs, err := events.NewDirBlobs(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	put := func(turns int) core.Hash {
		s := agent.Snapshot{Version: 1, Agent: "main", Turns: make([]core.Turn, turns), NextTurn: core.TurnID(turns + 1)}
		b, _ := json.Marshal(s)
		h, err := blobs.Put(b)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	_, _ = log.Emit("main", events.TypeAgentSnapshot, map[string]any{"blob": put(2)})
	_, _ = log.Emit("other", events.TypeAgentSnapshot, map[string]any{"blob": put(9)})
	_, _ = log.Emit("main", events.TypeAgentSnapshot, map[string]any{"blob": put(4)})
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := agent.LatestSnapshot(dir, "main")
	if err != nil || len(s.Turns) != 4 {
		t.Fatalf("the newest snapshot of the agent wins: %v %+v", err, s)
	}
	if _, err := agent.LatestSnapshot(dir, "nobody"); err == nil || !strings.Contains(err.Error(), "no snapshot") {
		t.Errorf("a session without a snapshot must say so: %v", err)
	}
}

func TestCompactNowFoldsTheThreadOnRequestAndKeepsWorking(t *testing.T) {
	var sawFocus bool
	pl := kv.DefaultPlanner()
	pl.SoftThreadTokens = 1_000_000 // the background planner stays out of it
	pl.HardThreadTokens = 2_000_000
	r := newRig(t, rigOpts{planner: pl}, scriptedWork(8, func(c *mock.Call) string {
		if strings.Contains(c.LastUser(), "keep the build flags") {
			sawFocus = true
		}
		n := 0
		for _, m := range c.Messages {
			if m.Role == "assistant" {
				n++
			}
		}
		return compactorPatch(2*n - 3)(c)
	}))
	if _, err := r.agent.Run(context.Background(), "build everything"); err != nil {
		t.Fatal(err)
	}
	before := len(r.agent.Thread().Snapshot().Turns)
	rep, err := r.agent.CompactNow(context.Background(), "keep the build flags")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Mode != "model" || rep.FoldedTurns == 0 || rep.TokensAfter >= rep.TokensBefore {
		t.Fatalf("report: %+v", rep)
	}
	if !sawFocus {
		t.Error("the user's focus never reached the compactor")
	}
	if after := len(r.agent.Thread().Snapshot().Turns); after >= before {
		t.Errorf("the thread did not shrink: %d -> %d", before, after)
	}
	if !strings.Contains(r.agent.Stack().Spine.Text(), "Built the project repeatedly") {
		t.Error("the spine does not carry the compactor's digest")
	}
	if len(r.log.OfType(events.TypeCompactCommit)) != 1 {
		t.Error("a manual compaction is a declared commit like any other")
	}
	// The agent carries on afterwards and its snapshot reflects the folded state.
	if _, err := r.agent.Run(context.Background(), "carry on"); err != nil {
		t.Fatal(err)
	}
	if got := r.agent.Snapshot(); got.Spine == nil || got.Compactions != 1 {
		t.Errorf("snapshot after compaction: %+v", got.Compactions)
	}
	// A second /compact with nothing new to fold does not pretend to work.
	rep2, err := r.agent.CompactNow(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if rep2.Mode == "model" && rep2.FoldedTurns > 4 {
		t.Errorf("second compaction folded too much: %+v", rep2)
	}
}

func TestCompactNowOnAFreshAgentSaysThereIsNothingToFold(t *testing.T) {
	r := newRig(t, rigOpts{}, workLoop(0))
	rep, err := r.agent.CompactNow(context.Background(), "")
	if err != nil || rep.Mode != "none" {
		t.Fatalf("%+v %v", rep, err)
	}
}
