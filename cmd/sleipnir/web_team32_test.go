package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/swarm"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// team32 is a team of 32 workers, four of each built-in role, the first two slots of a role on model a and the other two on model b.
func team32(a, b string) []wire.TeamSlot {
	var team []wire.TeamSlot
	for _, role := range swarm.WorkerRoleOrder {
		for i := range 4 {
			m := a
			if i >= 2 {
				m = b
			}
			team = append(team, wire.TeamSlot{Role: role, Model: m})
		}
	}
	return team
}

// A team of 32 is a size like any other: the New session request becomes one --role-model per slot, each with its own model, and the
// chat's parser gives a session of 32 workers whose slots keep their roles and models in order.
func TestATeamOf32WithModelsPerSlotIsOneRoleModelPerSlot(t *testing.T) {
	n := 32
	team := team32("mock/a", "mock/b")
	args := argsFor(wire.NewSessionRequest{Cwd: "/p", Model: "mock/mock-1", Swarm: &n, Isolation: "none", Team: team})
	var slots []string
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--role-model" {
			slots = append(slots, args[i+1])
		}
	}
	if len(slots) != 32 || slots[0] != "architect=mock/a" || slots[2] != "architect=mock/b" || slots[31] != "reviewer=mock/b" {
		t.Fatalf("--role-model values: %d %v", len(slots), slots)
	}
	f, err := parseChatFlags(args)
	if err != nil {
		t.Fatal(err)
	}
	o := f.options()
	if !o.Swarm || o.Workers != 32 || len(o.Team) != 32 {
		t.Fatalf("options: swarm %v workers %d team %d", o.Swarm, o.Workers, len(o.Team))
	}
	for i, s := range o.Team {
		if s.Role != team[i].Role || s.Model != team[i].Model {
			t.Errorf("slot %d = %+v, want %+v", i, s, team[i])
		}
	}
}

// The page's New session request for a team of 32 is accepted: the server opens the tab, and the session's meta names the 32 slots,
// four of each role, with the model chosen for each slot ("" is the role's own).
func TestWebHostStartsATeamOf32(t *testing.T) {
	project := webWorld(t)
	other := filepath.Join(filepath.Dir(project), "big")
	if err := os.MkdirAll(filepath.Join(other, ".sleipnir"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, base := newWebModel(t, sayScript("ok"))
	d := soloDefaults(project)
	d.Projects = []string{other}
	r := newWebRig(t, d, base)
	r.ready("")
	n := 32
	team := team32("mock/mock-1", "")
	var created struct{ Tab wire.TabSummary }
	r.json("POST", "/api/sessions", newSessionRequest{NewSessionRequest: wire.NewSessionRequest{Cwd: other, Model: "mock/mock-1", Swarm: &n, Isolation: "none", Team: team, Name: "big"}, ClientID: "c32"}, 201, &created)
	tab := r.ready(created.Tab.ID)
	var snap struct {
		Meta struct {
			Swarm int             `json:"swarm"`
			Team  []wire.TeamSlot `json:"team"`
		} `json:"meta"`
	}
	r.json("GET", "/api/sessions/"+tab.ID+"/snapshot", nil, 200, &snap)
	if snap.Meta.Swarm != 32 || len(snap.Meta.Team) != 32 {
		t.Fatalf("meta: swarm %d, %d slots", snap.Meta.Swarm, len(snap.Meta.Team))
	}
	count := map[string]int{}
	for _, s := range snap.Meta.Team {
		count[s.Role]++
	}
	for _, role := range swarm.WorkerRoleOrder {
		if count[role] != 4 {
			t.Errorf("%s: %d slots, want 4 (%s)", role, count[role], fmt.Sprint(count))
		}
	}
	if snap.Meta.Team[0].Model != "mock/mock-1" || snap.Meta.Team[2].Model != "" {
		t.Errorf("the models of the slots: %+v %+v", snap.Meta.Team[0], snap.Meta.Team[2])
	}
}
