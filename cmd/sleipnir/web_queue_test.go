package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// A line that waits for the turn can be sent now (it reaches the running turn as a steer), taken back (its whole text comes back for
// the message box) or dropped, and /queue lists, drops and clears the lines as the terminal's chat does. A slash line cannot steer;
// a line that is gone is 404.
func TestWebHostQueuedLinesSendNowEditAndDrop(t *testing.T) {
	project := webWorld(t)
	m, base := newWebModel(t, sayScript("ok"))
	r := newWebRig(t, soloDefaults(project), base)
	tab := r.ready("")
	q := "/api/sessions/" + tab.ID + "/queue/"
	m.hold("slow")
	r.json("POST", "/api/sessions/"+tab.ID+"/messages", wire.MessageRequest{Text: "slow one"}, 200, nil)
	r.waitEv(tab.ID, "turn", map[string]any{"s": "start"})
	var a, b, c wire.SendResult
	r.json("POST", "/api/sessions/"+tab.ID+"/messages", wire.MessageRequest{Text: "second line\nwith more"}, 200, &a)
	r.json("POST", "/api/sessions/"+tab.ID+"/messages", wire.MessageRequest{Text: "third"}, 200, &b)
	r.json("POST", "/api/sessions/"+tab.ID+"/messages", wire.MessageRequest{Text: "fourth"}, 200, &c)
	var cmd wire.CommandResult
	r.json("POST", "/api/sessions/"+tab.ID+"/command", wire.CommandRequest{Line: "/budget 3"}, 200, &cmd)
	if !a.Queued || !b.Queued || !c.Queued || !strings.HasPrefix(cmd.Output, "queued") {
		t.Fatalf("queued %+v %+v %+v %+v", a, b, c, cmd)
	}
	var kinds []string
	r.waitFor("meta with four queued lines and their kinds", func(f frame) bool {
		var mf wire.MetaFrame
		if f.event != "meta" || json.Unmarshal(f.data, &mf) != nil || mf.Patch.Queued == nil || len(*mf.Patch.Queued) != 4 {
			return false
		}
		kinds = nil
		for _, l := range *mf.Patch.Queued {
			kinds = append(kinds, l.Kind)
		}
		return true
	})
	if strings.Join(kinds, ",") != "message,message,message,command" {
		t.Errorf("kinds %v", kinds)
	}

	var list wire.CommandResult
	r.json("POST", "/api/sessions/"+tab.ID+"/command", wire.CommandRequest{Line: "/queue"}, 200, &list)
	for _, want := range []string{"queued (4)", "1. second line", "3. fourth", "4. /budget 3"} {
		if !strings.Contains(list.Output, want) {
			t.Errorf("/queue lacks %q:\n%s", want, list.Output)
		}
	}

	// send now: the turn is told at once, as /steer tells it
	var now wire.QueuedDrop
	r.json("POST", q+b.ID+"/now", nil, 200, &now)
	if !now.Steered || now.Text != "third" || now.Kind != "message" {
		t.Errorf("send now %+v", now)
	}
	r.waitEv(tab.ID, "steer", map[string]any{"text": "third"})

	// the command cannot steer; the edit takes the whole line back; a line taken is gone
	var cmdID string
	for _, l := range *r.h.tab(tab.ID).meta().Queued {
		if l.Kind == "command" {
			cmdID = l.ID
		}
	}
	if code, body := r.do("POST", q+cmdID+"/now", nil); code != 409 || !strings.Contains(string(body), "not_message") {
		t.Errorf("a command sent now = %d %s", code, body)
	}
	var edit wire.QueuedDrop
	r.json("DELETE", q+a.ID, nil, 200, &edit)
	if edit.Text != "second line\nwith more" {
		t.Errorf("the edit took back %q", edit.Text)
	}
	if code, body := r.do("DELETE", q+a.ID, nil); code != 404 || !strings.Contains(string(body), "not_queued") {
		t.Errorf("a line taken twice = %d %s", code, body)
	}
	if code, _ := r.do("DELETE", q+"nope", nil); code != 400 {
		t.Errorf("a bad id = %d", code)
	}

	// /queue drop and clear, numbered as /queue lists them
	r.json("POST", "/api/sessions/"+tab.ID+"/command", wire.CommandRequest{Line: "/queue drop 2"}, 200, &list)
	if !strings.Contains(list.Output, "dropped 2: /budget 3") {
		t.Errorf("/queue drop 2: %s", list.Output)
	}
	r.json("POST", "/api/sessions/"+tab.ID+"/command", wire.CommandRequest{Line: "/queue clear"}, 200, &list)
	if !strings.Contains(list.Output, "cleared the queue (1 line)") {
		t.Errorf("/queue clear: %s", list.Output)
	}
	if n := len(*r.h.tab(tab.ID).meta().Queued); n != 0 {
		t.Errorf("%d lines still queued", n)
	}

	m.release("slow")
	r.waitEv(tab.ID, "turn", map[string]any{"s": "end"})
	time.Sleep(100 * time.Millisecond)
	if m.asked("second line") >= 0 || m.asked("fourth") >= 0 {
		t.Errorf("a line taken out of the queue reached the model: %q", m.seen)
	}
}
