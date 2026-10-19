package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/swarm"
	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// A session resumed from the page after the server restarted keeps what its last /restart gave it: the verify command, which the
// resume raises for a confirmation since the server's own flags do not run it, and the team's slots on the models the restart moved
// them to, not the server's team or the team the session first started with.
func TestAResumedSessionKeepsTheVerifierAndTeamItLastRanWith(t *testing.T) {
	project := webWorld(t)
	if err := os.WriteFile(filepath.Join(project, "gate.sh"), []byte("exit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, base := newWebModel(t, sayScript("ok"))
	d := teamDefaults(project)
	var team, want []session.TeamSlot // one of each role and a second backend; the restart moves both backends to mock/mock-1
	for _, role := range append(slices.Clone(swarm.WorkerRoleOrder), "backend") {
		team = append(team, session.TeamSlot{Role: role})
		if role == "backend" {
			want = append(want, session.TeamSlot{Role: role, Model: "mock/mock-1"})
		} else {
			want = append(want, session.TeamSlot{Role: role})
		}
	}
	d.Workers, d.Team = len(team), team
	r := newWebRig(t, d, base)
	tab := r.ready("")
	path := "/api/sessions/" + tab.ID
	r.json("POST", path+"/messages", wire.MessageRequest{Text: "one"}, 200, nil)
	r.waitEv(tab.ID, "final", nil)

	verify := "sh " + filepath.Join(project, "gate.sh")
	line := "/restart --verify '" + verify + "' --role-model backend=mock/mock-1"
	scope := r.needsConfirm("POST", path+"/command", wire.CommandRequest{Line: line})
	r.json("POST", path+"/command", wire.CommandRequest{Line: line}, 200, nil, web.ConfirmHeader, r.confirmFor(scope))
	if sid := r.waitForSID(tab.ID, 2); sid != tab.SID {
		t.Fatalf("the restart did not carry the conversation: sid %s, want %s", sid, tab.SID)
	}
	s := r.h.tab(tab.ID).session()
	if o := s.Options(); o.Verify != verify || !slices.Equal(o.Team, want) {
		t.Fatalf("the restarted session: verify %q team %+v", o.Verify, o.Team)
	}
	r.json("POST", path+"/messages", wire.MessageRequest{Text: "two"}, 200, nil) // the restarted generation records its start
	finals := 0
	r.waitFor("the second turn's final", func(f frame) bool {
		if e := f.ev(); e != nil && e["k"] == "final" && e["_tab"] == tab.ID {
			finals++
		}
		return finals == 2
	})

	// The server's process ends and a new one starts with its default team (a size of its own, no custom slots): the session is
	// resumed from its record, team and all.
	r.stop()
	d2 := d
	d2.Workers, d2.Team = len(swarm.WorkerRoleOrder), nil
	r2 := newWebRig(t, d2, base)
	r2.ready("")
	code, body := r2.do("POST", "/api/sessions/resume", wire.ResumeRequest{From: tab.SID})
	var e struct {
		Code   string
		Detail struct {
			Scope   string
			Reasons []string
		}
	}
	if code != 428 || json.Unmarshal(body, &e) != nil || e.Code != "confirm_required" || !strings.Contains(strings.Join(e.Detail.Reasons, "\n"), verify) {
		t.Fatalf("resuming a session whose verify command the server does not run = %d %s, want 428 naming it", code, body)
	}
	var resumed struct{ Tab wire.TabSummary }
	r2.json("POST", "/api/sessions/resume", wire.ResumeRequest{From: tab.SID}, 201, &resumed, web.ConfirmHeader, r2.confirmFor(e.Detail.Scope))
	got := r2.ready(resumed.Tab.ID)
	if got.SID != tab.SID {
		t.Fatalf("the resumed tab has sid %s, want %s", got.SID, tab.SID)
	}
	if o := r2.h.tab(got.ID).session().Options(); o.Verify != verify || !slices.Equal(o.Team, want) {
		t.Errorf("the resumed session: verify %q team %+v, want %q %+v", o.Verify, o.Team, verify, want)
	}
}
