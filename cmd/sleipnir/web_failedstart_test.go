package main

import (
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// A New session that names no model, where nothing configures one, is refused before a tab opens: the session could only fail.
func TestWebNewSessionWithoutAnyModelIsRefused(t *testing.T) {
	project := webWorld(t)
	t.Setenv("SLEIPNIR_MODEL", "")
	base := session.Options{Config: config.Defaults(), NoWeb: true, Offline: true, NoMCP: true, TrustProject: true}
	r := newWebRig(t, soloDefaults(project), base)
	code, body := r.do("POST", "/api/sessions", map[string]any{"cwd": project, "swarm": 0})
	if code != 422 || !strings.Contains(string(body), `"code":"model"`) || !strings.Contains(string(body), "choose a model") {
		t.Fatalf("a session without a model = %d %s, want 422 model", code, body)
	}
	if n := len(r.h.Tabs()); n != 0 {
		t.Errorf("the refusal left %d tabs", n)
	}
}

// A tab whose start failed (here: a provider with no key) refuses a message with the reason instead of queueing it for good, and a
// model chosen for it starts it again on that model instead of being refused as "starting".
func TestWebFailedStartTakesAModel(t *testing.T) {
	project := webWorld(t)
	t.Setenv("GROQ_API_KEY", "")
	_, base := newWebModel(t, sayScript("ok"))
	d := soloDefaults(project)
	d.Model = "groq/no-key" // the first tab fails as well, and is removed: no session runs to check the model of the next
	r := newWebRig(t, d, base)
	deadline := time.Now().Add(webGuard)
	for len(r.h.Tabs()) > 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	r.json("POST", "/api/sessions", map[string]any{"cwd": project, "swarm": 0, "name": "dead", "model": "groq/no-key"}, 201, nil)
	tab := r.h.tab("dead")
	if tab == nil {
		t.Fatal("no tab dead")
	}
	for !tab.startFailed() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !tab.startFailed() {
		t.Fatal("the start of a provider with no key did not fail")
	}
	code, body := r.do("POST", "/api/sessions/dead/messages", wire.MessageRequest{Text: "hello"})
	if code != 409 || !strings.Contains(string(body), `"code":"no_session"`) || !strings.Contains(string(body), "GROQ_API_KEY") {
		t.Errorf("a message to a tab that did not start = %d %s, want 409 no_session with the reason", code, body)
	}
	var model struct{ Restarted bool }
	r.json("POST", "/api/sessions/dead/model", wire.ModelRequest{Roles: map[string]string{"manager": "mock/mock-1"}}, 200, &model)
	if !model.Restarted {
		t.Error("a model for a tab that did not start did not start it again")
	}
	r.waitForSID("dead", 2)
	tab.mu.Lock()
	args := strings.Join(tab.args, " ")
	tab.mu.Unlock()
	if strings.Count(args, "--model") != 1 || !strings.Contains(args, "--model mock/mock-1") {
		t.Errorf("the start again has the arguments %q, want the new model alone", args)
	}
	r.json("POST", "/api/sessions/dead/messages", wire.MessageRequest{Text: "hello"}, 200, nil)
	r.waitEv("dead", "final", nil)
}
