package swarm

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestUnsentRefusalsStayInTheViewUntilRetriedOrOld(t *testing.T) {
	now := time.Unix(1000, 0)
	b := unsentBook{now: func() time.Time { return now }}
	b.record("be-1", "mail:fe-1", "your mail to fe-1", "message too long: 30000 characters")
	b.record("be-1", "task:create", "your task create", "description too long")
	lines := b.lines("be-1")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "NOT SENT") || !strings.Contains(lines[1], "fe-1") || !strings.Contains(lines[2], "description too long") {
		t.Fatalf("view: %q", lines)
	}
	if len(b.lines("be-2")) != 0 {
		t.Fatal("another agent's refusals are not shown")
	}
	b.clear("be-1", "mail:fe-1")
	if lines := b.lines("be-1"); len(lines) != 2 || strings.Contains(strings.Join(lines, "\n"), "fe-1") {
		t.Fatalf("a successful mail to the same recipient clears its refusal: %q", lines)
	}
	now = now.Add(unsentTTL + time.Second)
	if len(b.lines("be-1")) != 0 {
		t.Fatal("an old refusal leaves the view")
	}
	for i := range 10 {
		b.record("mgr", "mail:"+string(rune('a'+i)), "mail", "x")
	}
	if got := len(b.lines("mgr")); got != maxUnsent+1 {
		t.Fatalf("%d lines, want the newest %d and the header", got, maxUnsent)
	}
}

func TestUnsentKeyNamesDeliveriesOnly(t *testing.T) {
	for _, tc := range []struct {
		tool, in, key string
		ok            bool
	}{
		{"mail", `{"to":"be-2","text":"x"}`, "mail:be-2", true},
		{"mail", `{"read":"m3"}`, "", false},
		{"task", `{"action":"create","title":"x"}`, "task:create", true},
		{"task", `{"action":"done","id":"T4"}`, "task:done:T4", true},
		{"task", `{"action":"list"}`, "", false},
		{"note", `{"withdraw":3}`, "", false},
		{"note", `{"read":3}`, "", false},
		{"note", `{"text":"x"}`, "note", true},
		{"pool", `{"action":"propose","id":"T2"}`, "pool:propose:T2", true},
		{"wait", `{}`, "", false},
	} {
		key, _, ok := unsentKey(tc.tool, json.RawMessage(tc.in))
		if key != tc.key || ok != tc.ok {
			t.Errorf("%s %s: key %q ok %v", tc.tool, tc.in, key, ok)
		}
	}
}

// A long refusal is whole in the tool's answer; the line the live view repeats every turn shows its opening.
func TestALongRefusalIsShownInPartInTheView(t *testing.T) {
	b := unsentBook{now: time.Now}
	b.record("be-1", "task:done:T1", "your task done of T1", strings.Repeat("verify failed ", 2000)+"END")
	lines := b.lines("be-1")
	if len(lines) != 2 || strings.Contains(lines[1], "END") || len([]rune(lines[1])) > listedRunes+200 || !strings.Contains(lines[1], "more characters") {
		t.Fatalf("view: %.600q", lines)
	}
}

// A note read through the wrapped tool leaves an earlier refused note in the view.
func TestANoteReadKeepsAnEarlierRefusedNote(t *testing.T) {
	sw := New(Config{SessionID: "t"}, Deps{}, nil)
	id, err := sw.Board.AddNote("w-1", "shared", "backend", "a fact")
	if err != nil {
		t.Fatal(err)
	}
	sw.unsent.record("w-2", "note", "your note", "refused for a reason")
	res := secRevCall(t, unsentTool{inner: &noteTool{s: sw}, s: sw}, "w-2", "backend", map[string]any{"read": id})
	if res.IsError {
		t.Fatalf("note read: %s", res.Text)
	}
	if lines := sw.unsent.lines("w-2"); len(lines) != 2 || !strings.Contains(lines[1], "refused for a reason") {
		t.Fatalf("view after a note read: %q", lines)
	}
}

// However many long refusals an agent has, what the view adds to every turn is bounded: the newest maxUnsent, each shown in part.
// The view is added after the hot block's budget on purpose (a refusal must not be the part the budget drops), so the bound is
// what keeps it small.
func TestLongRefusalsAddABoundedViewToEveryTurn(t *testing.T) {
	b := unsentBook{now: time.Now}
	for i := range 10 {
		b.record("be-1", fmt.Sprintf("task:done:T%d", i), fmt.Sprintf("your task done of T%d", i), strings.Repeat("verify failed ", 2000))
	}
	lines := b.lines("be-1")
	total := 0
	for _, l := range lines {
		total += len([]rune(l))
	}
	if len(lines) != 1+maxUnsent || total > 300+maxUnsent*(listedRunes+200) {
		t.Fatalf("the view is %d lines, %d characters", len(lines), total)
	}
}
