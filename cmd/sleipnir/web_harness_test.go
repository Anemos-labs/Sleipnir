package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/goal"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// A frame whose coalescing key is longer than the hub takes (the diff of a file under a long path, such as a session's scratch
// directory) is sent, not dropped as an invalid event, and two such keys stay apart.
func TestAFrameWithALongKeyIsSent(t *testing.T) {
	hub := web.NewHub(web.HubConfig{})
	defer hub.Close()
	long := "diff/t1/" + strings.Repeat("deep/", 40)
	for _, file := range []string{"plugin.json", "hooks.json"} {
		f := wire.Frame{Type: "ev", Data: map[string]string{"file": file}, Coalescable: true, Key: long + file}
		if err := publishFrame(hub, f); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
	}
	a, b := frameKey(long+"plugin.json"), frameKey(long+"hooks.json")
	if len(a) > maxFrameKey || a == b || !strings.HasPrefix(a, "diff/t1/") {
		t.Errorf("keys %q and %q", a, b)
	}
	if k := "diff/t1/a.go"; frameKey(k) != k {
		t.Errorf("a short key changed: %q", frameKey(k))
	}
	// A key of the bound's length that reads as another key's short form is shortened too: the two frames do not replace each other.
	if len(a) != maxFrameKey || frameKey(a) == a {
		t.Errorf("the key %q that is a long key's short form was sent as it is", a)
	}
	if k := strings.Repeat("k", maxFrameKey); frameKey(k) != k {
		t.Errorf("a key of the bound's length changed: %q", frameKey(k))
	}
}

// fakeLink records the lines the sleipnir tool queues in the terminal chat.
type fakeLink struct{ lines []string }

func (l *fakeLink) Queue(line string) bool { l.lines = append(l.lines, line); return true }

// A reload the agent asks for in the terminal chat restarts the chat, and the chat that starts in its place sends the agent on with
// what the reload applied, as the web page does; a chat that starts for any other reason does not.
func TestTheTerminalChatSendsTheAgentOnAfterItsReload(t *testing.T) {
	t.Setenv(reloadEnv, "")
	os.Unsetenv(reloadEnv)
	link := &fakeLink{}
	if _, err := (ttyHarnessHost{link: link}).Reload("the standup plugin"); err != nil {
		t.Fatal(err)
	}
	if len(link.lines) != 1 || link.lines[0] != "/restart" || os.Getenv(reloadEnv) != "the standup plugin" {
		t.Fatalf("queued %v, %s=%q", link.lines, reloadEnv, os.Getenv(reloadEnv))
	}

	first := chatSessionWith(t, false, nil, func(o *session.Options) { o.Dir = "" }, nil)
	if _, err := first.Run(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	id, home, cwd := first.ID, first.Home(), first.Cwd()
	first.Close()
	again := chatSessionWith(t, false, nil, func(o *session.Options) { o.Resume, o.Home, o.Cwd, o.Root, o.Dir = id, home, cwd, cwd, "" }, nil)
	link = &fakeLink{}
	if !carryReload(again, link.Queue) || len(link.lines) != 1 || !strings.Contains(link.lines[0], "What changed: the standup plugin.") {
		t.Fatalf("the agent is not sent on: %v", link.lines)
	}
	if _, set := os.LookupEnv(reloadEnv); set {
		t.Error("the reload is carried once")
	}
	if carryReload(again, link.Queue) {
		t.Error("a restart without a reload sends nothing")
	}
}

// A reload the agent asks for while a goal is active resumes the goal, and the agent is told why it is back and which plugin the
// person turned on did not start (research, as web is off here), even when the goal ends as it resumes.
func TestAReloadWithAnActiveGoalTellsTheAgentWhy(t *testing.T) {
	project := webWorld(t)
	var mu sync.Mutex
	var told string
	_, base := newWebModel(t, func(c *mock.Call) mock.Reply {
		if u := c.LastUser(); strings.Contains(u, "sleipnir reload") {
			mu.Lock()
			told = u
			mu.Unlock()
		}
		return sayScript("ok")(c)
	})
	r := newWebRig(t, soloDefaults(project), base)
	tab := r.ready("")
	wt := r.h.tab(tab.ID)
	s := wt.session()
	r.json("POST", "/api/sessions/"+tab.ID+"/messages", wire.MessageRequest{Text: "first"}, 200, nil)
	r.waitEv(tab.ID, "final", nil) // a session with a turn can be resumed

	wt.goals.set(s, goal.New("ship the shop"))
	if _, err := (webHarnessHost{wt}).Reload("the standup plugin"); err != nil {
		t.Fatal(err)
	}
	r.waitForSID(tab.ID, 2)
	var said string
	r.waitFor("the goal goes on", func(f frame) bool {
		e := f.ev()
		if e == nil || e["k"] != "sys" || e["_tab"] != tab.ID || !strings.HasPrefix(fmt.Sprint(e["text"]), "the goal goes on") {
			return false
		}
		said = fmt.Sprint(e["text"])
		return true
	})
	if !strings.Contains(said, "the standup plugin") || !strings.Contains(said, "did not start: ") {
		t.Errorf("the person reads %q", said)
	}
	deadline := time.Now().Add(webGuard)
	for {
		mu.Lock()
		got := told
		mu.Unlock()
		if strings.Contains(got, "What changed: the standup plugin") && strings.Contains(got, "Did not start: ") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the goal's turn did not tell the agent about its reload: %q", got)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
