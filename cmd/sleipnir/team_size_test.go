package main

import (
	"slices"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// The number a person types or reads for a team counts workers, never the manager: the default chat is a manager and eight
// workers, --swarm 9 is a manager and nine workers, --swarm 0 is a single agent, and rl's swarm:3 is the same team as chat's --swarm 3.
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

	nine := build(9)
	if nine.Swarm == nil || nine.Swarm.MaxWorkers() != 9 {
		t.Fatalf("--swarm 9 is not nine workers: %v", nine.Swarm)
	}
	if f := nine.StartFlags(); f[slices.Index(f, "--swarm")+1] != "9" {
		t.Errorf("a restart of --swarm 9 asks for another size: %v", f)
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

// A restart keeps when the manager answers for the person: a wait, a 0 given (never, over the configuration's wait) and the level,
// each read back by the chat flags as the session had it.
func TestARestartKeepsWhenTheManagerAnswers(t *testing.T) {
	projectDir(t)
	for _, c := range []struct {
		after time.Duration
		upTo  perm.Danger
	}{{-1, ""}, {5 * time.Minute, perm.DangerHigh}, {0, perm.DangerLow}} {
		s := chatSessionWith(t, false, nil, func(o *session.Options) {
			o.Swarm, o.Workers, o.ManagerAnswersAfter, o.ManagerAnswersUpTo = true, 8, c.after, c.upTo
		}, nil)
		cf, err := parseChatFlags(s.StartFlags())
		if err != nil {
			t.Fatalf("%v: %v", s.StartFlags(), err)
		}
		if o := cf.options(); o.ManagerAnswersAfter != c.after || o.ManagerAnswersUpTo != c.upTo {
			t.Errorf("after %v up to %q came back as %v up to %q from %v", c.after, c.upTo, o.ManagerAnswersAfter, o.ManagerAnswersUpTo, s.StartFlags())
		}
	}
}
