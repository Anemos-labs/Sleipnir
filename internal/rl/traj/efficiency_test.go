package traj_test

import (
	"testing"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/rl"
	"github.com/anemos-labs/sleipnir/internal/rl/traj"
	"github.com/anemos-labs/sleipnir/internal/rl/traj/trajtest"
)

// checkSignals compares the named signals of an episode.
func checkSignals(t *testing.T, ep *rl.Episode, want map[string]float64) {
	t.Helper()
	for k, v := range want {
		if got, ok := ep.Signals[k]; !ok || got != v {
			t.Errorf("%s = %v (present %v), want %v", k, got, ok, v)
		}
	}
}

func TestLoopingRunCountsGuardEventsAndToolCalls(t *testing.T) {
	b := trajtest.New()
	a := b.Agent("main", "worker", "m")
	a.User("fix the build")
	for i := 1; i <= 8; i++ {
		a.Step(trajtest.Call{Text: "again", Tool: "bash", Input: `{"command":"make"}`, Result: "make: *** no rule", IsError: true})
		if i == 4 {
			b.Emit("main", events.TypeAgentStuck, map[string]any{"phase": "nudge", "guard": "repeat", "note": "[harness] bash has now failed the same way 4 times"})
		}
	}
	// A test-weakening note and the other supervision phases are not repetition.
	b.Emit("main", events.TypeAgentStuck, map[string]any{"phase": "nudge", "guard": "tests", "note": "[harness] The last test run failed and you changed only test files."})
	b.Emit("main", events.TypeAgentStuck, map[string]any{"phase": "verify", "note": "[harness] run the tests"})
	b.Emit("main", events.TypeAgentStuck, map[string]any{"phase": "stop", "error": "agent main: agent stuck: bash failed the same way 8 times"})
	b.Outcome("verifier", false, 0)

	ep := episode(t, traj.OpenWith(b.Events(), b.Blobs), traj.Options{Policy: rl.PolicyRef{Model: "m"}})
	checkSignals(t, ep, map[string]float64{
		rl.SigToolCalls: 8, rl.SigToolErrors: 8, rl.SigStuckWarnings: 1, rl.SigStuckStops: 1,
		rl.SigRepeatedReads: 0, rl.SigFinalAnswerChars: 0,
	})
	if !ep.Has(rl.FlagLooped) || rl.HardFlag(rl.FlagLooped) {
		t.Errorf("flags = %v: a run the guard stopped is looped, and looped is not a hard flag", ep.Flags)
	}
}

func TestLegacyNudgeWithoutGuardIsReadByItsWording(t *testing.T) {
	b := trajtest.New()
	a := b.Agent("main", "worker", "m")
	a.User("go")
	b.Emit("main", events.TypeAgentStuck, map[string]any{"phase": "nudge", "note": "[harness] bash has now failed the same way 4 times"})
	b.Emit("main", events.TypeAgentStuck, map[string]any{"phase": "nudge", "note": "[harness] The last test run failed and you changed only test files. Change a test only when"})
	a.Step(trajtest.Call{Text: "ok"})
	ep := episode(t, traj.OpenWith(b.Events(), b.Blobs), traj.Options{Policy: rl.PolicyRef{Model: "m"}})
	checkSignals(t, ep, map[string]float64{rl.SigStuckWarnings: 1, rl.SigStuckStops: 0, rl.SigFinalAnswerChars: 2})
}

func TestRepeatedReadsOfUnchangedFiles(t *testing.T) {
	b := trajtest.New()
	a := b.Agent("main", "worker", "m")
	a.User("fix a.go")
	read := func(in string) {
		a.Step(trajtest.Call{Text: "look", Tool: "read", Input: in, Result: "package a"})
	}
	read(`{"path":"a.go"}`)
	read(`{"path":"./a.go"}`)           // the same file, spelled differently: a re-read
	read(`{"path":"a.go","offset":40}`) // another part of it: not a re-read
	a.Step(trajtest.Call{Text: "missing", Tool: "read", Input: `{"path":"nope.go"}`, Result: "no such file", IsError: true})
	a.Step(trajtest.Call{Text: "missing", Tool: "read", Input: `{"path":"nope.go"}`, Result: "no such file", IsError: true})
	a.Step(trajtest.Call{Text: "fix", Tool: "edit", Input: `{"path":"a.go","old_string":"a","new_string":"b"}`, Result: "ok"})
	read(`{"path":"a.go"}`) // changed since: worth reading again
	read(`{"path":"a.go"}`) // unchanged since the last read: a re-read
	a.Step(trajtest.Call{Text: "Fixed a.go."})
	b.Outcome("verifier", true, 1)

	ep := episode(t, traj.OpenWith(b.Events(), b.Blobs), traj.Options{Policy: rl.PolicyRef{Model: "m"}})
	checkSignals(t, ep, map[string]float64{
		rl.SigToolCalls: 8, rl.SigRepeatedReads: 2, rl.SigToolErrors: 2,
		rl.SigStuckWarnings: 0, rl.SigStuckStops: 0, rl.SigFinalAnswerChars: float64(len("Fixed a.go.")),
	})
	if ep.Has(rl.FlagLooped) {
		t.Errorf("flags = %v: re-reads alone are not a loop", ep.Flags)
	}
}

func TestSwarmEfficiencySignalsSumOverAgents(t *testing.T) {
	b := trajtest.New()
	mgr := b.SpawnRoot("mgr", "manager", "m")
	mgr.User("go")
	mgr.Step(trajtest.Call{Text: "spawn", Tool: "spawn", Input: `{}`, Result: "ok"})
	w := b.Spawn("mgr", "w-1", "backend", "m", "T1")
	w.User("t1")
	w.Step(trajtest.Call{Text: "read", Tool: "read", Input: `{"path":"a.go"}`, Result: "package a"})
	w.Step(trajtest.Call{Text: "read", Tool: "read", Input: `{"path":"a.go"}`, Result: "package a"})
	b.Emit("w-1", events.TypeAgentStuck, map[string]any{"phase": "nudge", "guard": "repeat", "note": "n"})
	w.Step(trajtest.Call{Text: "done"})
	b.End(w, "idle")
	// The manager reads the same file: a different agent's first read is not a re-read.
	mgr.Step(trajtest.Call{Text: "check", Tool: "read", Input: `{"path":"a.go"}`, Result: "package a"})
	b.Emit("mgr", events.TypeAgentStuck, map[string]any{"phase": "nudge", "guard": "repeat", "note": "n"})
	mgr.Step(trajtest.Call{Text: "All done"})

	ep := episode(t, traj.OpenWith(b.Events(), b.Blobs), traj.Options{Policy: rl.PolicyRef{Model: "m"}})
	checkSignals(t, ep, map[string]float64{
		rl.SigToolCalls: 4, rl.SigRepeatedReads: 1, rl.SigStuckWarnings: 2, rl.SigFinalAnswerChars: float64(len("All done")),
	})
}
