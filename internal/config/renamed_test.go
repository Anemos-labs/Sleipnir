package config

import (
	"strings"
	"testing"
)

// swarm.max_agents counted the manager; swarm.max_workers does not. A file or variable that still uses the old name is refused,
// with the new name and its value, instead of being dropped as an unknown key: dropping it would lift the ceiling it set.
func TestTheOldNameOfTheWorkerCeilingIsRefusedWithTheNewOne(t *testing.T) {
	p := newProj(t)
	src := `{"swarm": {"max_agents": 9}}`
	path := p.user(src)
	_, _, err := p.load()
	want := path + ":" + at(t, src, `"max_agents"`) + `: swarm.max_agents: renamed to swarm.max_workers (it counts workers, the manager not included): write "max_workers": 8 instead`
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("got %v\nwant %s", err, want)
	}

	p = newProj(t)
	_, _, err = p.load(withEnv("SLEIPNIR_SWARM_MAX_AGENTS=5"))
	if err == nil || !strings.Contains(err.Error(), "env:SLEIPNIR_SWARM_MAX_AGENTS") || !strings.Contains(err.Error(), "set SLEIPNIR_SWARM_MAX_WORKERS=4 instead") {
		t.Fatalf("the old variable: %v", err)
	}

	p = newProj(t)
	p.user(`{"swarm": {"max_agents": "lots"}}`)
	if _, _, err = p.load(); err == nil || !strings.Contains(err.Error(), `write "max_workers": the old value minus one instead`) {
		t.Fatalf("an old value that is no number: %v", err)
	}

	// A value with no equal is never converted into one that changes what the person set: 1 meant "the manager alone", and a
	// worker ceiling of 0 means no ceiling at all, so the refusal asks for a choice. 0 stays 0 (no ceiling before, none now).
	for _, c := range []struct{ old, want string }{
		{"1", `write "max_workers": a worker count of 1 or more (the old value 1 allowed no workers, and 0 means no ceiling; --swarm 0 runs a single agent) instead`},
		{"2", `write "max_workers": 1 instead`},
		{"0", `write "max_workers": 0 instead`},
		{"-3", `write "max_workers": a worker count of 0 or more (a negative ceiling is not valid) instead`},
	} {
		p = newProj(t)
		p.user(`{"swarm": {"max_agents": ` + c.old + `}}`)
		if _, _, err = p.load(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("max_agents %s: got %v\nwant a message containing %s", c.old, err, c.want)
		}
	}
	p = newProj(t)
	if _, _, err = p.load(withEnv("SLEIPNIR_SWARM_MAX_AGENTS=1")); err == nil || !strings.Contains(err.Error(), "set SLEIPNIR_SWARM_MAX_WORKERS=<a worker count of 1 or more") || strings.Contains(err.Error(), "MAX_WORKERS=0") {
		t.Errorf("the old variable with the value 1: %v", err)
	}

	p = newProj(t)
	p.user(`{"swarm": {"max_workers": 8}}`)
	cfg, _, err := p.load()
	if err != nil || cfg.Swarm.MaxWorkers != 8 {
		t.Fatalf("the new name: %v %+v", err, cfg)
	}
}
