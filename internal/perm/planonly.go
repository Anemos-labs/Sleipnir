package perm

// PlanOnly returns an engine for the same workspace that is in plan mode and has no allow rule: it allows what plan mode can prove to
// read and change nothing, and no exception that the person carved for the session (Edit(docs/plan.md), Bash(go test:*)). A caller that
// holds an agent to a policy of its own uses it to tell a request that plan mode allows from one that only a rule allows. It returns nil
// when the configuration cannot be built again.
func (e *Engine) PlanOnly() *Engine {
	cfg := e.cfg
	cfg.Mode, cfg.Allow, cfg.Ask, cfg.Roles, cfg.Prompter = ModePlan, nil, nil, nil, nil
	p, err := NewEngine(cfg)
	if err != nil {
		return nil
	}
	return p
}
