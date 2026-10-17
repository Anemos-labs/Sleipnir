package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// --compact-at is a share of each model's context window from 10 to 90, or 0 for the configuration's: the chat's parser refuses
// anything else with the bounds in its message, and the New session dialog's request becomes the flag and the session's option.
func TestCompactAtIsAShareFrom10To90(t *testing.T) {
	for _, ok := range []string{"10", "25", "90", "0"} {
		f, err := parseChatFlags([]string{"--compact-at", ok})
		if err != nil {
			t.Errorf("--compact-at %s: %v", ok, err)
			continue
		}
		if want := map[string]int{"10": 10, "25": 25, "90": 90, "0": 0}[ok]; f.options().CompactAtPercent != want {
			t.Errorf("--compact-at %s gives the session %d", ok, f.options().CompactAtPercent)
		}
	}
	for _, bad := range []string{"9", "91", "-5", "100"} {
		if _, err := parseChatFlags([]string{"--compact-at", bad}); err == nil || !strings.Contains(err.Error(), "--compact-at must be from 10 to 90") {
			t.Errorf("--compact-at %s: %v", bad, err)
		}
	}
	if _, err := parseChatFlags([]string{"--compact-at", "a third"}); err == nil {
		t.Error("--compact-at takes a whole number")
	}

	forty, zero := 40, 0
	args := argsFor(wire.NewSessionRequest{Cwd: "/p", CompactAt: &forty})
	if got := strings.Join(args, " "); got != "--cwd /p --compact-at 40" {
		t.Errorf("argsFor: %s", got)
	}
	f, err := parseChatFlags(args)
	if err != nil || f.options().CompactAtPercent != 40 {
		t.Errorf("the request's share does not reach the session: %v %+v", err, f)
	}
	for _, none := range []*int{nil, &zero} {
		if args := argsFor(wire.NewSessionRequest{Cwd: "/p", CompactAt: none}); slices.Contains(args, "--compact-at") {
			t.Errorf("no share in the request is the configuration's, not a flag: %v", args)
		}
	}
	if got := chatArgsOf(webDefaults{Cwd: "/p", CompactAtPercent: 30}, ""); !slices.Contains(got, "--compact-at") || got[slices.Index(got, "--compact-at")+1] != "30" {
		t.Errorf("sleipnir web's --compact-at is not its first tab's: %v", got)
	}
}

// A session restarted (/restart, /swarm, /model, /new) keeps the share it was started with: it is one of the flags of its shape.
func TestARestartKeepsTheCompactionShare(t *testing.T) {
	s := chatSessionWith(t, false, nil, func(o *session.Options) { o.CompactAtPercent = 40 }, nil)
	args, err := restartArgs(s, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if i := slices.Index(args, "--compact-at"); i < 0 || args[i+1] != "40" {
		t.Errorf("the restart drops --compact-at 40: %v", args)
	}
	if args, _ := restartArgs(s, []string{"--compact-at", "60"}, true); count(args, "--compact-at") != 1 || args[slices.Index(args, "--compact-at")+1] != "60" {
		t.Errorf("the share typed after /restart is the one: %v", args)
	}
	plain := chatSession(t, false, nil)
	if args, _ := restartArgs(plain, nil, false); contains(args, "--compact-at") {
		t.Errorf("a session that took the configuration's share keeps taking it: %v", args)
	}
}

// The New session dialog of the page: its defaults name sleipnir web's --compact-at and the share a session takes without the flag
// (the configuration's, 25 built in, 0 where the configuration's token limits apply); a request's share is the session's and survives
// a restart of the tab; a share outside 10 to 90 is refused before a tab opens.
func TestTheCompactionShareOfASessionStartedInThePage(t *testing.T) {
	project := webWorld(t)
	_, base := newWebModel(t, sayScript("ok"))
	d := soloDefaults(project)
	d.CompactAtPercent = 40
	r := newWebRig(t, d, base)
	first := r.ready("")
	if got := r.h.tab(first.ID).session().Options().CompactAtPercent; got != 40 {
		t.Errorf("the first tab's share is %d, want sleipnir web's --compact-at 40", got)
	}
	var hello helloBody
	r.json("GET", "/api/hello", nil, 200, &hello)
	if hello.Defaults == nil || hello.Defaults.CompactAt == nil || *hello.Defaults.CompactAt != 40 || hello.Defaults.CompactAtDefault != 25 {
		t.Fatalf("the dialog's defaults: %+v", hello.Defaults)
	}

	zero, sixty := 0, 60
	var created struct{ Tab wire.TabSummary }
	r.json("POST", "/api/sessions", wire.NewSessionRequest{Cwd: project, Swarm: &zero, CompactAt: &sixty}, 201, &created)
	tab := r.ready(created.Tab.ID)
	if got := r.h.tab(tab.ID).session().Options().CompactAtPercent; got != 60 {
		t.Fatalf("the request's share 60 became %d", got)
	}
	r.json("POST", "/api/sessions/"+tab.ID+"/restart", wire.RestartRequest{Kind: "restart"}, 202, nil)
	r.waitForSID(tab.ID, 2)
	if got := r.h.tab(tab.ID).session().Options().CompactAtPercent; got != 60 {
		t.Errorf("after a restart the share is %d, want 60", got)
	}
	r.json("POST", "/api/sessions", wire.NewSessionRequest{Cwd: project, Swarm: &zero}, 201, &created)
	if got := r.h.tab(r.ready(created.Tab.ID).ID).session().Options().CompactAtPercent; got != 0 {
		t.Errorf("a request with no share gives the session %d, not the configuration's", got)
	}
	for _, bad := range []int{5, 95, -1} {
		if code, body := r.do("POST", "/api/sessions", wire.NewSessionRequest{Cwd: project, Swarm: &zero, CompactAt: &bad}); code != 400 || !strings.Contains(string(body), "--compact-at must be from 10 to 90") {
			t.Errorf("a share of %d = %d %s", bad, code, body)
		}
	}

	home, _ := os.UserHomeDir()
	file := filepath.Join(home, ".sleipnir", "config.json")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	for conf, want := range map[string]int{
		`{"cache":{"compact_at_percent":35}}`:                                  35,
		`{"cache":{"thread_soft_limit_tokens":30000}}`:                         0,
		`{"cache":{"compact_at_percent":30,"thread_soft_limit_tokens":30000}}`: 30,
	} {
		if err := os.WriteFile(file, []byte(conf), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := r.h.defaults().CompactAtDefault; got != want {
			t.Errorf("%s: the share a session takes without the flag is %d, want %d", conf, got, want)
		}
	}
}
