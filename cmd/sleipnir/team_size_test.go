package main

import (
	"slices"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/session"
)

// The number a person types or reads for a team counts workers, never the manager: the default chat is a manager and eight
// workers, --swarm 3 is a manager and three workers (four agents: internal/swarm's TestConcurrentSpawnRespectsWriterAndAgentCaps
// registers exactly that many), --swarm 0 is a single agent, and rl's swarm:3 is the same team as chat's --swarm 3.
func TestTeamSizeCountsWorkersNotTheManager(t *testing.T) {
	projectDir(t)
	if n := defaultTeam(); n != 8 {
		t.Fatalf("the default chat has %d workers, want 8 and the manager", n)
	}
	build := func(n int) *session.Session {
		team, workers := teamOf(n)
		return chatSessionWith(t, false, nil, func(o *session.Options) { o.Swarm, o.Workers = team, workers }, nil)
	}

	def := build(defaultTeam())
	if def.Swarm == nil || def.Swarm.MaxWorkers() != 8 || chatInfo(def, "/p").Workers != 8 {
		t.Fatalf("the default chat is not a manager and 8 workers: %+v", chatInfo(def, "/p"))
	}

	three := build(3)
	if three.Swarm == nil || three.Swarm.MaxWorkers() != 3 {
		t.Fatalf("--swarm 3 is not three workers: %v", three.Swarm)
	}
	if f := three.StartFlags(); f[slices.Index(f, "--swarm")+1] != "3" {
		t.Errorf("a restart of --swarm 3 asks for another size: %v", f)
	}

	if solo := build(0); solo.Swarm != nil {
		t.Error("--swarm 0 is a team, want a single agent")
	}

	p := policyFlags{mode: "swarm:3"}
	swarm, workers, single, err := p.team()
	if err != nil || !swarm || single {
		t.Fatalf("rl swarm:3: %v %v %v", swarm, single, err)
	}
	if _, chat := teamOf(3); workers != chat {
		t.Errorf("rl swarm:3 asks for %d workers and chat --swarm 3 for %d", workers, chat)
	}
}
