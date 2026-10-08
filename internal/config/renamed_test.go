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

	p = newProj(t)
	p.user(`{"swarm": {"max_workers": 8}}`)
	cfg, _, err := p.load()
	if err != nil || cfg.Swarm.MaxWorkers != 8 {
		t.Fatalf("the new name: %v %+v", err, cfg)
	}
}
