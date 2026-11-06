package swarm

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// A message longer than its kind's inline limit is not refused and not cut: its opening is
// delivered with the harness's note of how much follows, and its sender and recipient (and
// no one else) read the whole, line breaks kept, with mail read.
func TestLongMailIsDeliveredInPartAndReadWhole(t *testing.T) {
	r, got := newRouter(t)
	body := "Plan for the API:\n" + strings.Repeat("- endpoint detail line\n", 200) + "END"
	m, err := r.Send("be-1", "fe-1", "info", body)
	if err != nil {
		t.Fatalf("a %d-character info was refused: %v", utf8.RuneCountInString(body), err)
	}
	if len(*got) != 1 {
		t.Fatalf("delivered %d", len(*got))
	}
	d := (*got)[0]
	if n := utf8.RuneCountInString(d.Text); n > 1200 {
		t.Fatalf("the inline part is %d characters, over the 1,200 limit", n)
	}
	frame := d.Frame()
	if !strings.Contains(frame, "continues") || !strings.Contains(frame, "mail read="+m.ID) {
		t.Fatalf("the delivery does not say how to read the rest: %q", frame[len(frame)-200:])
	}
	for _, who := range []string{"fe-1", "be-1"} {
		w, err := r.Read(who, m.ID)
		if err != nil || !strings.HasSuffix(w.Full, "END") || !strings.Contains(w.Full, "\n- endpoint detail line\n") {
			t.Fatalf("%s reads %v %q", who, err, w.Full[:40])
		}
	}
	if _, err := r.Read("mgr", m.ID); err == nil {
		t.Fatal("a third agent read the mail")
	}
	if _, err := r.Read("fe-1", "m999"); err == nil || !strings.Contains(err.Error(), "not kept") {
		t.Fatalf("an unknown id: %v", err)
	}
	// A short message stays as it was: whole, with no note.
	if s, _ := r.Send("be-1", "mgr", "", "short"); s.Full != "" || strings.Contains(s.Frame(), "continues") {
		t.Fatalf("short mail: %+v", s)
	}
}

// The router keeps long messages within its byte budget, forgetting the oldest first.
func TestKeptMailIsBounded(t *testing.T) {
	cfg := DefaultRouterConfig()
	cfg.MaxKeptBytes = 5000
	cfg.MaxPerMinute, cfg.MaxPerPairPerMin = 100, 100
	r, _ := newRouterCfg(t, cfg)
	var ids []string
	for i := 0; i < 4; i++ {
		m, err := r.Send("be-1", "fe-1", "info", strings.Repeat(string(rune('a'+i)), 2000))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, m.ID)
	}
	if _, err := r.Read("fe-1", ids[0]); err == nil {
		t.Fatal("the oldest long message is still kept over the budget")
	}
	if _, err := r.Read("fe-1", ids[3]); err != nil {
		t.Fatalf("the newest: %v", err)
	}
}

// Mail coalesced into an inbox digest is kept whole, and the digest names it.
func TestInboxDigestNamesMessagesToReadWhole(t *testing.T) {
	r, _ := newRouter(t)
	var o overflow
	o.init()
	m := Message{ID: "m7", From: "be-1", To: "fe-1", Text: strings.Repeat("detail ", 100)}
	r.keepWhole(m)
	o.add(m, true)
	d := o.digest("m8")
	if !strings.Contains(d, "whole: m7") || !strings.Contains(d, "mail read=") {
		t.Fatalf("digest: %q", d)
	}
	if w, err := r.Read("fe-1", "m7"); err != nil || w.Full != m.Text {
		t.Fatalf("read: %v", err)
	}
}

// A description is kept whole, every line, up to its limit, and refused (not cut) beyond it; a title too long for the board
// opens the description instead of being lost.
func TestTaskDescriptionAndLongTitleAreKept(t *testing.T) {
	b := NewBoard(nil)
	desc := strings.Repeat("step\n", 120) + "LAST"
	long := "Build the export pipeline " + strings.Repeat("with every format and option ", 10)
	task, err := b.CreateTask("mgr", TaskSpec{Title: long, Desc: desc})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(task.Desc, "LAST") || strings.Count(task.Desc, "step") != 120 {
		t.Fatalf("description lost lines: %d", strings.Count(task.Desc, "step"))
	}
	if !strings.HasPrefix(task.Desc, strings.Join(strings.Fields(long), " ")) {
		t.Fatalf("the long title does not open the description: %q", task.Desc[:80])
	}
	if _, err := b.CreateTask("mgr", TaskSpec{Title: "too much", Desc: strings.Repeat("x", maxDescRunes+1)}); err == nil || !strings.Contains(err.Error(), "description too long") {
		t.Fatalf("an over-long description: %v", err)
	}
}

// A note over its limit is refused with its size, not cut into a wrong fact.
func TestLongNoteIsRefusedNotCut(t *testing.T) {
	b := NewBoard(nil)
	if _, err := b.AddNote("be-1", "shared", "be", strings.Repeat("n", maxNoteRunes+5)); err == nil || !strings.Contains(err.Error(), "5 over") {
		t.Fatalf("long note: %v", err)
	}
}

// A notice that may stop being true is kept whole when the inbox overflows, not folded into the digest, so that the agent still
// asks it when it takes the mail; the table keeps a bounded number of them.
func TestAConditionalNoticeIsNotFoldedIntoTheDigest(t *testing.T) {
	var o overflow
	o.init()
	still := func() bool { return true }
	o.add(Message{ID: "m1", From: "be-1", Kind: "info", Text: "plain"}, true)
	for i := 0; i < maxConditional+2; i++ {
		o.add(Message{ID: fmt.Sprintf("h%d", i), From: harnessSender, Kind: "request", Text: "Stall check", still: still}, false)
	}
	if len(o.conditional) != maxConditional || o.conditional[0].ID != "h2" {
		t.Fatalf("kept %d conditional notices, the first %s", len(o.conditional), o.conditional[0].ID)
	}
	if d := o.digest("m9"); strings.Contains(d, "Stall check") || !strings.Contains(d, "1 more messages") {
		t.Fatalf("the digest folded a conditional notice: %q", d)
	}
	var only overflow
	only.init()
	only.add(Message{ID: "h1", From: harnessSender, still: still}, false)
	if only.empty() {
		t.Fatal("an overflow that holds only a conditional notice reads as empty, so the notice would never be pumped")
	}
}
