package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// startTeam starts a team of two in the project with the budget of the request and returns its tab.
func (r *webRig) startTeam(project string, budget *float64) string {
	r.t.Helper()
	two := 2
	var created struct{ Tab wire.TabSummary }
	r.json("POST", "/api/sessions", wire.NewSessionRequest{Cwd: project, Swarm: &two, Budget: budget}, 201, &created)
	r.ready(created.Tab.ID)
	return created.Tab.ID
}

// teamBudget is the budget a team started with that of the request runs with (0: no limit).
func (r *webRig) teamBudget(project string, budget *float64) float64 {
	r.t.Helper()
	return r.h.tab(r.startTeam(project, budget)).session().Budget()
}

// A session started in the page has no spending cap unless one is asked for: the terminal caps a team at the built-in $50 where nobody
// chose a budget, the page does not, and the New session dialog starts at off. A budget in the request is the budget; 0 is off.
func TestSessionsOfThePageHaveNoCapUnlessOneIsAsked(t *testing.T) {
	project := webWorld(t)
	_, base := newWebModel(t, sayScript("ok"))
	r := newWebRig(t, soloDefaults(project), base)
	r.ready("")
	var hello helloBody
	r.json("GET", "/api/hello", nil, 200, &hello)
	if hello.Defaults == nil || hello.Defaults.Budget != nil {
		t.Errorf("the dialog starts with a budget: %+v", hello.Defaults)
	}
	if got := r.teamBudget(project, nil); got != 0 {
		t.Errorf("a team started with no budget in the request has the cap %v", got)
	}
	zero, twenty := 0.0, 20.0
	if got := r.teamBudget(project, &zero); got != 0 {
		t.Errorf("off: %v", got)
	}
	if got := r.teamBudget(project, &twenty); got != 20 {
		t.Errorf("$20: %v", got)
	}
}

// What a person set is kept: swarm.budget_usd of the configuration (not the built-in cap) is the dialog's budget and the cap of a
// team that says nothing, off in the request lifts it for that session alone, and a team that was started off takes a budget when it
// is told off, which a team under a configured cap cannot be.
func TestAConfiguredBudgetIsKeptAndOffLiftsItForTheSession(t *testing.T) {
	project := webWorld(t)
	home, _ := os.UserHomeDir()
	if err := os.MkdirAll(filepath.Join(home, ".sleipnir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".sleipnir", "config.json"), []byte(`{"swarm":{"budget_usd":12.5}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, base := newWebModel(t, sayScript("ok"))
	base.Config.Swarm.BudgetUSD = 12.5 // what the session reads (the rig injects its own configuration)
	r := newWebRig(t, soloDefaults(project), base)
	r.ready("")
	var hello helloBody
	r.json("GET", "/api/hello", nil, 200, &hello)
	if hello.Defaults == nil || hello.Defaults.Budget == nil || *hello.Defaults.Budget != 12.5 {
		t.Fatalf("the dialog's budget is %+v, want the configured 12.5", hello.Defaults)
	}
	if got := r.teamBudget(project, nil); got != 12.5 {
		t.Errorf("a team that says nothing: %v", got)
	}
	zero := 0.0
	if got := r.teamBudget(project, &zero); got != 0 {
		t.Fatalf("off in the request: %v", got)
	}
	// started off, a team is told off again without a refusal; one under the configured cap cannot be (the next start of the team
	// would take the cap again)
	off, capped := r.startTeam(project, &zero), r.startTeam(project, nil)
	r.json("POST", "/api/sessions/"+off+"/budget", wire.BudgetRequest{Off: true}, 200, nil)
	if code, body := r.do("POST", "/api/sessions/"+capped+"/budget", wire.BudgetRequest{Off: true}); code != 409 || !strings.Contains(string(body), "budget_configured") {
		t.Errorf("off under a configured cap = %d %s", code, body)
	}
}

// The setting the command line of the dialog starts with for a team with no budget is the one that gives the terminal that: the
// configuration reads swarm.budget_usd from it.
func TestTheSettingThatLiftsTheTeamCapIsOneTheConfigurationReads(t *testing.T) {
	name, value, _ := strings.Cut(unlimitedEnv, "=")
	t.Setenv(name, value)
	cfg, _, err := config.Load(config.LoadOpts{Cwd: t.TempDir(), UntrustedProject: true})
	if err != nil || cfg.Swarm.BudgetUSD != 0 {
		t.Errorf("with %s the configuration says %v, %v: a team has the cap of %v", unlimitedEnv, cfg.Swarm.BudgetUSD, err, config.DefaultSwarmBudgetUSD)
	}
}
