package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// swarm.mailman is a plain boolean, off by default: mail is delivered at once unless
// the person asks for the mailman.
func TestMailmanIsOffByDefaultAndSettableFromEveryLayer(t *testing.T) {
	if Defaults().Swarm.Mailman {
		t.Fatal("the mailman must be off by default: it costs requests")
	}
	// Absent from the JSON of a default configuration (omitempty), present when on.
	b, _ := json.Marshal(Defaults().Swarm)
	if strings.Contains(string(b), "mailman") {
		t.Fatalf("a default configuration mentions the mailman: %s", b)
	}
	on := Defaults()
	on.Swarm.Mailman = true
	if b, _ := json.Marshal(on.Swarm); !strings.Contains(string(b), `"mailman":true`) {
		t.Fatalf("swarm.mailman does not round-trip: %s", b)
	}

	p := newProj(t)
	p.user(`{"swarm": {"mailman": true}}`)
	cfg, rep, err := p.load()
	if err != nil || !cfg.Swarm.Mailman {
		t.Fatalf("the user's file: %+v %v", cfg.Swarm, err)
	}
	if rep.Origins["swarm.mailman"] == "" {
		t.Fatalf("the origin of swarm.mailman is not reported: %v", rep.Origins)
	}
	// The environment overrides the file, in both directions.
	off, _, err := p.load(withEnv("SLEIPNIR_SWARM_MAILMAN=false"))
	if err != nil || off.Swarm.Mailman {
		t.Fatalf("SLEIPNIR_SWARM_MAILMAN=false: %+v %v", off.Swarm, err)
	}
	p2 := newProj(t)
	env, _, err := p2.load(withEnv("SLEIPNIR_SWARM_MAILMAN=1"))
	if err != nil || !env.Swarm.Mailman {
		t.Fatalf("SLEIPNIR_SWARM_MAILMAN=1: %+v %v", env.Swarm, err)
	}
	// A wrong value is an error naming the key, not a silent off.
	if _, _, err := p2.load(withEnv("SLEIPNIR_SWARM_MAILMAN=sometimes")); err == nil || !strings.Contains(err.Error(), "SLEIPNIR_SWARM_MAILMAN") {
		t.Fatalf("a bad value: %v", err)
	}
}

// Like isolation, the mailman gives a project's files no reach they did not have (it
// spends the person's budget, bounded by the swarm's own limits), so a project may set
// it and it is not a security-sensitive setting.
func TestProjectMaySetTheMailman(t *testing.T) {
	p := newProj(t)
	p.project(`{"swarm": {"mailman": true}}`)
	cfg, rep, err := p.load(func(o *LoadOpts) { o.UntrustedProject = true })
	if err != nil || !cfg.Swarm.Mailman {
		t.Fatalf("an untrusted project's mailman setting was dropped: %+v %v", cfg.Swarm, err)
	}
	for _, r := range rep.ProjectRisks {
		if strings.HasPrefix(r.Path, "swarm") {
			t.Fatalf("swarm.mailman is not security-sensitive: %v", r)
		}
	}
	// A wrong type is an error with its location.
	bad := newProj(t)
	bad.user(`{"swarm": {"mailman": "yes"}}`)
	if _, _, err := bad.load(); err == nil {
		t.Fatal("a string for a boolean must be rejected")
	}
}
