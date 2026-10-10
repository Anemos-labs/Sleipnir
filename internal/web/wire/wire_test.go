package wire

import (
	"encoding/json"
	"strings"
	"testing"
)

// allEvents has one value of every event type of the vocabulary.
func allEvents() []Event {
	return []Event{
		&Say{}, &Sys{}, &Tool{}, &Note{}, &State{}, &Task{}, &Plan{}, &Verdict{}, &Req{}, &Use{}, &Warm{}, &Gov{}, &Mail{}, &Ckpt{},
		&Ask{}, &Answer{}, &Queue{}, &Merge{}, &Break{}, &Compact{}, &Stream{}, &Diff{}, &Goal{}, &Final{}, &Steer{}, &Interrupt{},
		&Refuse{}, &More{}, &Turn{}, &Stall{}, &Handover{}, &Layers{},
	}
}

func TestEveryEventTypeHasADistinctKind(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range allEvents() {
		k := KindOf(e)
		if k == "" || seen[k] {
			t.Errorf("%T: kind %q is empty or taken", e, k)
		}
		seen[k] = true
		Stamp(e, 1.5, 7)
		if h := HeadOf(e); h.K != k || h.T != 1.5 || h.Seq != 7 {
			t.Errorf("%T: head %+v after Stamp", e, h)
		}
	}
	// 30 kinds of the reference page minus the three the server does not produce (local, reply, digest), plus the five additive ones.
	if len(seen) != 32 {
		t.Errorf("%d kinds: %v", len(seen), seen)
	}
}

func TestEventsEncodeFlatWithTheirHeadFirst(t *testing.T) {
	task := "T5"
	head := "T4"
	for _, c := range []struct {
		e    Event
		want string
	}{
		{&Say{Who: "mgr", Text: "hi", Stream: true, Rate: 240, Mid: "m12"}, `{"t":4,"k":"say","seq":12,"who":"mgr","text":"hi","stream":true,"rate":240,"mid":"m12"}`},
		{&State{ID: "be-1", S: "work", Doing: "Edit a.go", Task: &task}, `{"t":4,"k":"state","seq":12,"id":"be-1","s":"work","doing":"Edit a.go","task":"T5"}`},
		{&State{ID: "be-1", S: "idle", Doing: "waits"}, `{"t":4,"k":"state","seq":12,"id":"be-1","s":"idle","doing":"waits","task":null}`},
		{&Queue{QHead: &head, Conflicts: 1}, `{"t":4,"k":"queue","seq":12,"head":"T4","conflicts":1,"bounced":0}`},
		{&Queue{}, `{"t":4,"k":"queue","seq":12,"head":null,"conflicts":0,"bounced":0}`},
		{&Final{}, `{"t":4,"k":"final","seq":12}`},
		{&Layers{ID: "mgr", Toks: [6]int{1, 2, 3, 4, 5, 6}}, `{"t":4,"k":"layers","seq":12,"id":"mgr","toks":[1,2,3,4,5,6]}`},
		{&More{Mid: "m12", Text: "x", End: true}, `{"t":4,"k":"more","seq":12,"mid":"m12","text":"x","end":true}`},
	} {
		Stamp(c.e, 4, 12)
		b, err := json.Marshal(c.e)
		if err != nil || string(b) != c.want {
			t.Errorf("%T = %s (%v), want %s", c.e, b, err, c.want)
		}
	}
	// An event that came from history carries the real time of day.
	e := Stamp(&Say{Who: "you", Text: "x"}, 0, 3).(*Say)
	e.At = 1760050000123
	if b, _ := json.Marshal(e); !strings.Contains(string(b), `"at":1760050000123`) {
		t.Errorf("history event = %s", b)
	}
}

func TestStampOfAnUnknownTypeLeavesNoKind(t *testing.T) {
	if k := KindOf(nil); k != "" {
		t.Errorf("KindOf(nil) = %q", k)
	}
}

func TestMetaPatchSendsOnlyWhatIsSet(t *testing.T) {
	b, _ := json.Marshal(MetaPatch{})
	if string(b) != `{}` {
		t.Errorf("empty patch = %s", b)
	}
	none := []Rule{}
	zero := 0.0
	off := false
	q := []QueuedLine{}
	p := MetaPatch{Rules: &none, Budget: &zero, Mailman: &off, Queued: &q}
	b, _ = json.Marshal(p)
	if string(b) != `{"budget":0,"mailman":false,"rules":[],"queued":[]}` {
		t.Errorf("a zero is a value, not an absence: %s", b)
	}
	var back MetaPatch
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&back); err != nil || back.Rules == nil || len(*back.Rules) != 0 || back.Budget == nil || *back.Budget != 0 || back.Cwd != nil {
		t.Errorf("round trip = %+v (%v)", back, err)
	}
}

func TestErrorBodyShape(t *testing.T) {
	e := &Error{Status: 409, Msg: "trust this project first", Code: "trust_required"}
	b, _ := json.Marshal(e)
	if string(b) != `{"error":"trust this project first","code":"trust_required"}` {
		t.Errorf("without detail = %s", b)
	}
	e.Detail = map[string]int{"retryAfterMs": 350}
	b, _ = json.Marshal(e)
	if string(b) != `{"error":"trust this project first","code":"trust_required","detail":{"retryAfterMs":350}}` {
		t.Errorf("with detail = %s", b)
	}
	if e.Error() != "trust_required: trust this project first" {
		t.Errorf("Error() = %q", e.Error())
	}
	var err error = e
	if err == nil {
		t.Fatal("not an error")
	}
}

func TestSnapshotAndHelloShapes(t *testing.T) {
	s := TabSnapshot{Tab: TabSummary{ID: "shop"}, Keyframe: []json.RawMessage{json.RawMessage(`{"t":0,"k":"final"}`)}}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]json.RawMessage
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"tab", "gen", "seq", "now", "meta", "roster", "keyframe", "events", "hist", "questions"} {
		if _, ok := back[k]; !ok {
			t.Errorf("TabSnapshot lacks %q: %s", k, b)
		}
	}
	h, _ := json.Marshal(Hello{StreamAfter: 7})
	for _, k := range []string{`"boot"`, `"now"`, `"tabs"`, `"server"`, `"limits"`, `"ui"`, `"version"`, `"streamAfter":7`} {
		if !strings.Contains(string(h), k) {
			t.Errorf("Hello lacks %s: %s", k, h)
		}
	}
}
