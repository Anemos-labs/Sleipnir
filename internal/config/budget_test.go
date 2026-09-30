package config

import "testing"

// A swarm run has a built-in spending cap. It is a safety net the user chooses to
// raise or remove; a repository cannot, so the key is one a project's file may not set
// unless the project is trusted (lowering it would be harmless, but one rule is easier
// to hold in mind than "lower is fine").
func TestSwarmBudgetDefaultsToACapAThatOnlyTheUserCanChange(t *testing.T) {
	if d := Defaults(); d.Swarm.BudgetUSD != DefaultSwarmBudgetUSD || DefaultSwarmBudgetUSD <= 0 {
		t.Fatalf("the default swarm budget is %v", d.Swarm.BudgetUSD)
	}

	// An untrusted project's attempts, up or off, change nothing and are reported.
	for _, body := range []string{`{"swarm": {"budget_usd": 100000}}`, `{"swarm": {"budget_usd": 0}}`, `{"swarm": {"budget_usd": 5}}`} {
		p := newProj(t)
		p.project(body)
		cfg, rep, err := p.load(func(o *LoadOpts) { o.UntrustedProject = true })
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Swarm.BudgetUSD != DefaultSwarmBudgetUSD {
			t.Errorf("an untrusted project changed the swarm budget with %s: %v", body, cfg.Swarm.BudgetUSD)
		}
		found := false
		for _, r := range rep.ProjectRisks {
			found = found || r.Path == "swarm.budget_usd"
		}
		if !found {
			t.Errorf("%s: the ignored setting is not reported: %+v", body, rep.ProjectRisks)
		}
	}

	// A trusted project may set it, and so may the user, in a file or in the environment;
	// 0 in the user's own file removes the cap.
	p := newProj(t)
	p.project(`{"swarm": {"budget_usd": 7.5}}`)
	if cfg, _, err := p.load(func(o *LoadOpts) { o.UntrustedProject = false }); err != nil || cfg.Swarm.BudgetUSD != 7.5 {
		t.Errorf("a trusted project's budget: %v %v", cfg, err)
	}
	p = newProj(t)
	p.user(`{"swarm": {"budget_usd": 0}}`)
	p.project(`{"swarm": {"budget_usd": 3}}`)
	if cfg, _, err := p.load(func(o *LoadOpts) { o.UntrustedProject = true }); err != nil || cfg.Swarm.BudgetUSD != 0 {
		t.Errorf("the user's 0 must remove the cap and stand against an untrusted project: %v %v", cfg.Swarm.BudgetUSD, err)
	}
	p = newProj(t)
	if cfg, _, err := p.load(func(o *LoadOpts) {
		o.Environ = func() []string { return []string{"SLEIPNIR_SWARM_BUDGET_USD=200"} }
	}); err != nil || cfg.Swarm.BudgetUSD != 200 {
		t.Errorf("the environment sets the budget: %v %v", cfg.Swarm.BudgetUSD, err)
	}
}
