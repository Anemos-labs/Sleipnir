//go:build unix

package hooks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/perm"
)

// engineWith builds a real permission engine whose prompter is the hooks'
// Prompter in front of next.
func engineWith(t *testing.T, r *Runner, next perm.Prompter, cfg perm.Config) *perm.Engine {
	t.Helper()
	cfg.Root = r.Dir
	cfg.Home = realTemp(t)
	cfg.Prompter = r.Prompter(next)
	e, err := perm.NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func bashReq(r *Runner, cmd string) perm.Request {
	in, _ := json.Marshal(map[string]string{"command": cmd})
	return perm.Request{Agent: "be-1", Role: "backend", Tool: "bash", Input: in, Command: cmd, Cwd: r.Dir, Summary: cmd}
}

func TestPrompterAnswersQuestionsOnTheUsersBehalf(t *testing.T) {
	allow := heredoc(`{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"}}}`)
	deny := heredoc(`{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny","message":"policy says no"}}}`)
	var asked atomic.Int32
	human := func(context.Context, perm.Request) perm.Decision {
		asked.Add(1)
		return perm.Decision{Allow: true, Reason: "the human said yes"}
	}

	tests := []struct {
		name    string
		hook    string
		cmd     string
		allow   bool
		reason  string
		human   int32
		fileRan bool
	}{
		{"hook allows", allow, "make deploy", true, "allowed by a hook", 0, true},
		{"hook denies", deny, "make deploy", false, "denied by a hook: policy says no", 0, true},
		{"exit 2 denies", "echo 'blocked by policy' >&2; exit 2", "make deploy", false, "denied by a hook: blocked by policy", 0, true},
		{"no opinion falls through", "exit 0", "make deploy", true, "the human said yes", 1, true},
		{"a failing hook falls through", "exit 1", "make deploy", true, "the human said yes", 1, true},
		{"ask falls through", heredoc(`{"hookSpecificOutput":{"decision":{"behavior":"ask"}}}`), "make deploy", true, "the human said yes", 1, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			asked.Store(0)
			r := newRunner(t, settings(t, PermissionRequest, group{hooks: []hookSpec{cmdHook(tc.hook + "\necho ran >> hook.log")}}))
			e := engineWith(t, r, human, perm.Config{Mode: perm.ModeDefault})
			d := e.Check(context.Background(), bashReq(r, tc.cmd))
			if d.Allow != tc.allow || !strings.Contains(d.Reason, tc.reason) {
				t.Errorf("decision = %+v, want allow=%v reason %q", d, tc.allow, tc.reason)
			}
			if asked.Load() != tc.human {
				t.Errorf("the human was asked %d times, want %d", asked.Load(), tc.human)
			}
			if d.Remember != perm.ScopeOnce {
				t.Errorf("a hook's answer must never be remembered as a rule: %+v", d)
			}
		})
	}
}

// The engine calls its prompter only for questions its rules leave open, so a
// hook can answer a question but never overrule a denial.
func TestHooksCannotOverrideTheEnginesDenials(t *testing.T) {
	r := newRunner(t, settings(t, PermissionRequest, group{hooks: []hookSpec{
		cmdHook(heredoc(`{"hookSpecificOutput":{"decision":{"behavior":"allow"}}}`) + "\necho consulted >> consulted.txt"),
	}}))
	e := engineWith(t, r, nil, perm.Config{Mode: perm.ModeBypass, Deny: []string{"Bash(rm:*)"}})
	if d := e.Check(context.Background(), bashReq(r, "rm -rf build")); d.Allow {
		t.Errorf("a deny rule was overridden by a hook: %+v", d)
	}
	if d := e.Check(context.Background(), bashReq(r, "cat ~/.ssh/id_rsa")); d.Allow {
		t.Errorf("a built-in protection was overridden by a hook: %+v", d)
	}
	if _, err := os.Stat(filepath.Join(r.Dir, "consulted.txt")); err == nil {
		t.Error("hooks were consulted about requests the engine had already denied")
	}
}

func TestPrompterWithoutANextPrompterRefuses(t *testing.T) {
	r := newRunner(t, settings(t, PermissionRequest, group{hooks: []hookSpec{cmdHook("exit 0")}}))
	d := r.Prompter(nil)(context.Background(), bashReq(r, "make"))
	if d.Allow || !strings.Contains(d.Reason, "approval required") {
		t.Errorf("decision = %+v", d)
	}
}

func TestPrompterPassesTheRequestToTheHook(t *testing.T) {
	r := newRunner(t, settings(t, PermissionRequest, group{matcher: "Bash", hooks: []hookSpec{cmdHook("cat > payload.json")}}))
	req := bashReq(r, "make deploy")
	req.Paths = []string{filepath.Join(r.Dir, "a.txt")}
	r.Prompter(func(context.Context, perm.Request) perm.Decision { return perm.Decision{Allow: true} })(context.Background(), req)
	b, err := os.ReadFile(filepath.Join(r.Dir, "payload.json"))
	if err != nil {
		t.Fatal("the hook was not consulted (does the matcher see the tool?)")
	}
	var p map[string]any
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	if p["hook_event_name"] != "PermissionRequest" || p["tool_name"] != "Bash" || p["command"] != "make deploy" || p["agent"] != "be-1" || p["role"] != "backend" {
		t.Errorf("payload = %s", b)
	}
	in, _ := p["tool_input"].(map[string]any)
	if in["command"] != "make deploy" {
		t.Errorf("tool_input = %v", in)
	}
}

// A Notification hook hears that a person is being asked something, beside the
// question and without being able to answer it.
func TestNotificationHookRunsWhenAPersonIsAsked(t *testing.T) {
	dir := realTemp(t)
	seen := filepath.Join(dir, "seen.json")
	r := newRunner(t, settings(t, "Notification", group{matcher: "permission_prompt", hooks: []hookSpec{cmdHook("cat > " + seen)}}))
	answered := make(chan struct{})
	human := func(context.Context, perm.Request) perm.Decision {
		close(answered)
		return perm.Decision{Allow: true, Reason: "the human said yes"}
	}
	d := r.Prompter(human)(context.Background(), bashReq(r, "make deploy"))
	if !d.Allow || d.Reason != "the human said yes" {
		t.Fatalf("the human's answer must be the decision: %+v", d)
	}
	<-answered
	var payload map[string]any
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(seen); err == nil && json.Unmarshal(b, &payload) == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if payload["hook_event_name"] != "Notification" || payload["notification_type"] != "permission_prompt" || !strings.Contains(payload["message"].(string), "make deploy") {
		t.Errorf("payload: %v", payload)
	}

	// No question for a person, no notification: a hook that decided has nothing to announce.
	r2 := newRunner(t, map[string]json.RawMessage{
		"PermissionRequest": settings(t, "PermissionRequest", group{hooks: []hookSpec{cmdHook(heredoc(`{"hookSpecificOutput":{"decision":{"behavior":"allow"}}}`))}})["PermissionRequest"],
		"Notification":      settings(t, "Notification", group{hooks: []hookSpec{cmdHook("touch " + filepath.Join(dir, "unexpected"))}})["Notification"],
	})
	if d := r2.Prompter(nil)(context.Background(), bashReq(r2, "make deploy")); !d.Allow {
		t.Fatalf("the hook allowed it: %+v", d)
	}
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(dir, "unexpected")); err == nil {
		t.Error("a Notification hook ran although nobody was asked")
	}
}
