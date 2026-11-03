package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// waitTabs waits until the rig's server hosts n started tabs and returns them in strip order.
func (r *webRig) waitTabs(n int) []wire.TabSummary {
	r.t.Helper()
	deadline := time.Now().Add(webGuard)
	for time.Now().Before(deadline) {
		var body struct{ Tabs []wire.TabSummary }
		r.json("GET", "/api/sessions", nil, 200, &body)
		started := 0
		for _, tb := range body.Tabs {
			if tb.SID != "" {
				started++
			}
		}
		if len(body.Tabs) == n && started == n {
			return body.Tabs
		}
		time.Sleep(20 * time.Millisecond)
	}
	r.t.Fatalf("the server did not host %d started tabs:\n%s", n, r.dump())
	return nil
}

// A session pinned from the page is opened again, in a tab of its own and under its name, when the next server starts; unpinning
// it, or closing its tab, keeps it from coming back.
func TestAPinnedSessionReopensWhenTheServerStarts(t *testing.T) {
	project := webWorld(t)
	_, base := newWebModel(t, sayScript("ok"))
	r := newWebRig(t, soloDefaults(project), base)
	tab := r.ready("")
	path := "/api/sessions/" + tab.ID
	r.json("POST", path+"/messages", wire.MessageRequest{Text: "one"}, 200, nil)
	r.waitEv(tab.ID, "final", nil)
	r.json("PATCH", path, map[string]string{"name": "shop"}, 200, nil)

	var pinned struct{ Tab wire.TabSummary }
	r.json("PUT", path+"/pin", map[string]bool{"pinned": true}, 200, &pinned)
	if !pinned.Tab.Pinned {
		t.Fatalf("the pin route answered %+v", pinned.Tab)
	}
	r.waitFor("the tab frame that says it is pinned", func(f frame) bool {
		var d wire.TabFrame
		return f.event == "tab" && json.Unmarshal(f.data, &d) == nil && d.Op == "update" && d.Tab.ID == tab.ID && d.Tab.Pinned
	})
	pins, err := session.LoadPins("")
	if err != nil || len(pins) != 1 || pins[0].ID != tab.SID || pins[0].Name != "shop" || pins[0].Cwd != project {
		t.Fatalf("pins = %+v, %v", pins, err)
	}

	r.stop()
	r2 := newWebRig(t, soloDefaults(project), base)
	tabs := r2.waitTabs(2)
	var back wire.TabSummary
	for _, tb := range tabs {
		if tb.SID == tab.SID {
			back = tb
		}
	}
	if back.ID == "" || !back.Pinned || back.Name != "shop" {
		t.Fatalf("the pinned session did not come back pinned and named: %+v", tabs)
	}
	if s := r2.h.tab(back.ID).session(); s == nil || !s.Resumed() {
		t.Fatal("the reopened tab did not resume the pinned session")
	}

	// closing the pinned tab unpins it: the next server opens only its first tab
	r2.json("DELETE", "/api/sessions/"+back.ID, nil, 200, nil)
	if session.Pinned("", tab.SID) {
		t.Fatal("closing the pinned tab left it pinned")
	}
	r2.stop()
	r3 := newWebRig(t, soloDefaults(project), base)
	r3.ready("")
	time.Sleep(200 * time.Millisecond) // a reopen would have added its tab by now
	if tabs := r3.waitTabs(1); tabs[0].SID == tab.SID {
		t.Fatalf("an unpinned session came back: %+v", tabs)
	}
}

// A pinned session that would raise a privilege the person did not confirm when they pinned it (here: a verify command written into
// the pin list by hand differs from the one the session runs) is not reopened by itself, and stays pinned; a pin of a session that is
// no longer recorded is dropped.
func TestAPinnedSessionThatNeedsAConfirmationIsNotReopened(t *testing.T) {
	project := webWorld(t)
	if err := os.WriteFile(filepath.Join(project, "gate.sh"), []byte("exit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, base := newWebModel(t, sayScript("ok"))
	d := soloDefaults(project)
	d.Verify = "sh " + filepath.Join(project, "gate.sh")
	r := newWebRig(t, d, base)
	tab := r.ready("")
	r.json("POST", "/api/sessions/"+tab.ID+"/messages", wire.MessageRequest{Text: "one"}, 200, nil)
	r.waitEv(tab.ID, "final", nil)
	r.stop()
	gone := "20200101-000000-abcdef"
	for _, p := range []session.Pin{{ID: tab.SID, Verify: "sh other.sh"}, {ID: gone}} {
		if err := session.SetPin("", p); err != nil {
			t.Fatal(err)
		}
	}
	// the next server runs no verify command: the session's own would be raised, and the pin confirmed another
	r2 := newWebRig(t, soloDefaults(project), base)
	r2.ready("")
	deadline := time.Now().Add(webGuard)
	for session.Pinned("", gone) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if session.Pinned("", gone) {
		t.Fatal("the pin of a session that is not recorded was kept")
	}
	if tabs := r2.waitTabs(1); tabs[0].SID == tab.SID {
		t.Fatalf("a pinned session that needs a confirmation was reopened: %+v", tabs)
	}
	if !session.Pinned("", tab.SID) {
		t.Fatal("a pinned session that was not reopened lost its pin")
	}
}
