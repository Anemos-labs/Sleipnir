package perm

// ReadOnlyAllow are the allow rules of a read-only posture that still researches and verifies: a reviewer, a scout or an architect
// runs the project's tests and static checks (with the project's own Python virtual environment too, Bash(python:venv-tests): running
// the tests with it, never creating it or installing into it) and reads the web (an outside repository, an API's documentation, a
// package registry). Everything that writes or executes anything else stays denied by plan mode; the engine understands shell syntax, so
// "go test ./... && rm -rf x" is not allowed by its first half. Callers must not change the slice.
var ReadOnlyAllow = []string{
	"Bash(go test:*)", "Bash(go vet:*)", "Bash(pytest:*)", "Bash(npm test)", "Bash(npm run test:*)",
	"Bash(python -m pytest:*)", "Bash(python3 -m pytest:*)", "Bash(python -m unittest:*)", "Bash(python3 -m unittest:*)",
	"Bash(python:venv-tests)", "Bash(node --test:*)",
	"Bash(cargo test:*)", "Bash(cargo check:*)", "Bash(make test)",
	"WebFetch", "WebSearch",
}

// PlanOnly returns an engine for the same workspace that is in plan mode and has only the given allow rules: it allows what plan mode
// can prove to read and change nothing, what those rules allow, and no exception that the person carved for the session
// (Edit(docs/plan.md), Bash(go test:*)). A caller that holds an agent to a policy of its own uses it to tell a request that plan mode
// allows from one that only a rule allows; what it allows is not yet allowed by the session, whose own engine still decides. It is a
// probe: it asks no one (no Prompter, no Delegate to the manager), records nothing in the session's audit and keeps no rule, so a
// request it would ask about is refused by it alone, without a question anyone sees or a "no one" answer in the log. It returns nil when
// the configuration cannot be built again.
func (e *Engine) PlanOnly(allow ...string) *Engine {
	cfg := e.cfg
	cfg.Mode, cfg.Allow, cfg.Ask, cfg.Roles, cfg.Prompter = ModePlan, allow, nil, nil, nil
	cfg.Delegate, cfg.Audit, cfg.Persist = nil, nil, nil
	p, err := NewEngine(cfg)
	if err != nil {
		return nil
	}
	return p
}
