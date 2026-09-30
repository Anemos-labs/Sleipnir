package config

import (
	"strings"
	"testing"
)

// swarm.isolation is "none" (default) or "worktree"; "shared" is the older spelling
// of none. Anything else is an error with its location.
func TestIsolationValues(t *testing.T) {
	for _, tc := range []struct {
		value string
		mode  string
		valid bool
	}{
		{"", IsolationNone, true},
		{"none", IsolationNone, true},
		{"shared", IsolationNone, true},
		{"worktree", IsolationWorktree, true},
		{"container", IsolationNone, false},
		{"Worktree", IsolationNone, false},
	} {
		c := Defaults()
		c.Swarm.Isolation = tc.value
		if got := c.Swarm.IsolationMode(); got != tc.mode {
			t.Errorf("IsolationMode(%q) = %q, want %q", tc.value, got, tc.mode)
		}
		var errs []string
		for _, is := range c.Validate() {
			if is.Severity == SeverityError {
				errs = append(errs, is.Error())
			}
		}
		if tc.valid && len(errs) > 0 {
			t.Errorf("%q: unexpected errors %v", tc.value, errs)
		}
		if !tc.valid && (len(errs) != 1 || !strings.Contains(errs[0], `swarm.isolation: must be "none" or "worktree"`)) {
			t.Errorf("%q: errors %v", tc.value, errs)
		}
	}
	if d := Defaults(); d.Swarm.Isolation != "none" || d.Swarm.IsolationMode() != IsolationNone {
		t.Fatalf("the default must be today's behaviour: %+v", d.Swarm)
	}
}

// A project's file may turn isolation on (it only reduces risk): the key is not
// security-sensitive, even in a project that is not trusted. But a project cannot
// choose where trees live: there is no such key, so whatever it names is an unknown
// key that is reported and never applied.
func TestProjectMaySetIsolationButCannotChooseWhereTreesLive(t *testing.T) {
	p := newProj(t)
	p.project(`{"swarm": {"isolation": "worktree", "trees_dir": "/etc", "worktree_dir": "../elsewhere"}}`)
	cfg, rep, err := p.load(func(o *LoadOpts) { o.UntrustedProject = true })
	if err != nil {
		t.Fatalf("an unknown key is a warning, not an error: %v", err)
	}
	if cfg.Swarm.IsolationMode() != IsolationWorktree {
		t.Fatalf("an untrusted project's isolation was dropped: %+v", cfg.Swarm)
	}
	for _, r := range rep.ProjectRisks {
		if strings.HasPrefix(r.Path, "swarm.isolation") {
			t.Fatalf("swarm.isolation must not be a security-sensitive project setting: %v", r)
		}
	}
	var unknown []string
	for _, w := range rep.Warnings {
		unknown = append(unknown, w.Path)
	}
	joined := strings.Join(unknown, " ")
	for _, key := range []string{"swarm.trees_dir", "swarm.worktree_dir"} {
		if !strings.Contains(joined, key) {
			t.Errorf("%s should be reported as an unknown key; warnings: %v", key, rep.Warnings)
		}
	}
	for _, s := range SensitivePaths() {
		if strings.HasPrefix(s, "swarm") && s != "swarm.budget_usd" { // the budget is the one: budget_test.go
			t.Errorf("no other swarm setting is security-sensitive, got %s", s)
		}
	}
	// The environment layer maps the field like any other.
	c, _, err := p.load(func(o *LoadOpts) {
		o.Environ = func() []string { return []string{"SLEIPNIR_SWARM_ISOLATION=none"} }
	})
	if err != nil || c.Swarm.IsolationMode() != IsolationNone {
		t.Fatalf("SLEIPNIR_SWARM_ISOLATION must override the project's file: %+v %v", c.Swarm, err)
	}
}
