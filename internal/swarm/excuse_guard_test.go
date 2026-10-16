package swarm

import (
	"context"
	"strings"
	"testing"
)

// The manager may not excuse a pool member that is running and has made progress within the stall bound: it may simply be out of the
// manager's view or quiet in mail. One that has stopped, has made no progress at all, or has gone the bound without progress (a loop
// of reads included), may be.
func TestAWorkingMemberIsNotExcused(t *testing.T) {
	r := newRVRig(t, Config{PersistentPools: true, MaxWorkers: 2, Team: []string{"tester", "tester"}}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "waiting"} })
	if _, err := r.sw.Spawn(SpawnReq{Role: "tester", Title: "acceptance suite", Files: []string{"tests/**"}, By: "mgr"}); err != nil {
		t.Fatal(err)
	}
	member := r.sw.Board.Snapshot().Tasks[0].Pool.Members[1]
	m := r.sw.get(member)
	set := func(running bool, quiet int64) {
		m.mu.Lock()
		if running {
			m.life = lifeRunning
		} else {
			m.life = lifeIdle
		}
		m.mu.Unlock()
		m.turns.Store(100)
		m.worked.Store(100 - quiet + 1)
	}
	k := r.sw.stallTurns()
	for _, c := range []struct {
		running bool
		quiet   int64
		refused bool
	}{{true, 2, true}, {true, k - 1, true}, {true, k, false}, {false, 2, false}} {
		set(c.running, c.quiet)
		why := r.sw.excuseRefusal(member)
		if (why != "") != c.refused {
			t.Errorf("running %v, %d turns without progress: refusal %q, want refused %v", c.running, c.quiet, why, c.refused)
		}
		if c.refused && (!strings.Contains(why, member+" is working") || !strings.Contains(why, "mail")) {
			t.Errorf("the refusal does not say who is working or what to do instead: %q", why)
		}
	}
	set(true, 0)
	m.worked.Store(0) // running, but has done nothing yet: nothing to protect
	if why := r.sw.excuseRefusal(member); why != "" {
		t.Errorf("a member that has made no progress is shielded: %q", why)
	}
	if why := r.sw.excuseRefusal("nobody-1"); why != "" {
		t.Errorf("an unknown member is refused here, not by the pool's own check: %q", why)
	}
}

// The stall nudge offers a handover only where one is possible: persistent pools refuse it.
func TestTheStallNudgeOffersWhatTheSwarmAllows(t *testing.T) {
	f := Stall{Kind: StallClaimedNoProgress, Task: "T42"}
	if got := stallNudge(f, 12, false); !strings.Contains(got, "task handover") {
		t.Errorf("a swarm with handovers is not offered one: %q", got)
	}
	got := stallNudge(f, 12, true)
	if strings.Contains(got, "handover") || !strings.Contains(got, "task update") {
		t.Errorf("a persistent pool is offered a handover it refuses: %q", got)
	}
}
