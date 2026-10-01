package statetest

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
)

func TestTheDemoRecordingIsAWholeLogOfOneSessionAndNamesNoMachine(t *testing.T) {
	evs := DemoEvents()
	if len(evs) != 312 {
		t.Fatalf("%d events", len(evs))
	}
	session := evs[0].Session
	for i, e := range evs {
		if e.Seq != uint64(i+1) || e.Session != session || e.TS.IsZero() || e.Type == "" {
			t.Fatalf("event %d is not the next of one session: %+v", i, e)
		}
		if i > 0 && e.TS.Before(evs[i-1].TS) {
			t.Fatalf("event %d is older than the one before it", i)
		}
	}
	if evs[0].Type != events.TypeLogOpen || evs[1].Type != events.TypeSessionStart || evs[len(evs)-1].Type != events.TypeSessionEnd {
		t.Errorf("a session log begins with log.open and session.start and ends with session.end: %s %s ... %s", evs[0].Type, evs[1].Type, evs[len(evs)-1].Type)
	}
	raw := DemoLog()
	for _, bad := range []string{"/tmp/", "/home/", "/root", "/Users/", "claude", "scratchpad", "@"} {
		if bytes.Contains(raw, []byte(bad)) {
			t.Errorf("the recording names the machine it was made on: %q", bad)
		}
	}
	if !bytes.Contains(raw, []byte("/work/handbook")) {
		t.Error("the workspace path is gone")
	}
	// Each call is its own copy.
	raw[0] = 'X'
	if DemoLog()[0] == 'X' {
		t.Error("DemoLog returned the embedded bytes themselves")
	}
}

func TestParseRejectsWhatIsNotAnEventAndNamesTheLine(t *testing.T) {
	good := `{"seq":1,"ts":"2026-01-02T03:04:05Z","type":"log.open"}`
	if evs, err := Parse([]byte(good + "\n\n" + good[:7] + `2` + good[8:] + "\n")); err != nil || len(evs) != 2 || evs[1].Seq != 2 {
		t.Fatalf("%v %v", evs, err)
	}
	for _, c := range []struct{ in, want string }{
		{good + "\nnot json\n", "line 2"},
		{good + "\n" + `{"type":"x"}`, "line 2: no seq"},
	} {
		if _, err := Parse([]byte(c.in)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: %v", c.in, err)
		}
	}
}

func TestBuilderMakesConsecutiveEventsWhoseTimeOnlyMovesWhenToldTo(t *testing.T) {
	b := NewBuilder()
	b.Spawn("a", "backend", "T1", "mgr")
	b.Request("a", "a.1", "m", "pk", Sec{Name: "shared", Tokens: 10}, Sec{Name: "role", Tokens: 5, BP: true})
	b.Advance(2*time.Second).Response("a", "a.1", "m", 1, 2, 3, 4, 0.5)
	b.Call("a", "c1", "bash", map[string]any{"command": "ls"})
	b.Result("a", "c1", "bash", true, 12)
	b.Raw("a", "x.y", "{not json")
	evs := b.Events()
	if len(evs) != 6 || b.Seq() != 6 {
		t.Fatalf("%d events, seq %d", len(evs), b.Seq())
	}
	for i, e := range evs {
		want := Epoch
		if i >= 2 {
			want = Epoch.Add(2 * time.Second)
		}
		if e.Seq != uint64(i+1) || !e.TS.Equal(want) || e.Session != "test" {
			t.Errorf("event %d: %+v", i, e)
		}
	}
	var req struct {
		Sections []struct {
			Name   string `json:"name"`
			Hash   string `json:"hash"`
			Tokens int    `json:"tokens"`
			BP     bool   `json:"bp"`
		} `json:"sections"`
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(evs[1].Data, &req); err != nil || req.Kind != "main" || len(req.Sections) != 2 || !req.Sections[1].BP || req.Sections[0].Hash == "" {
		t.Errorf("request payload: %v %+v", err, req)
	}
	if string(evs[5].Data) != "{not json" {
		t.Errorf("Raw must keep the payload as given: %q", evs[5].Data)
	}
	// Events is a copy: what the test does with it does not reach the builder.
	evs[0].Type = "changed"
	if b.Events()[0].Type == "changed" {
		t.Error("Events returned the builder's own slice")
	}
}
