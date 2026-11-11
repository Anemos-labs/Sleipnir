package swarm

import (
	"encoding/json"
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
		{"pool", `{"action":"propose","id":"T2"}`, "pool:propose:T2", true},
		{"wait", `{}`, "", false},
	} {
		key, _, ok := unsentKey(tc.tool, json.RawMessage(tc.in))
		if key != tc.key || ok != tc.ok {
			t.Errorf("%s %s: key %q ok %v", tc.tool, tc.in, key, ok)
		}
	}
}
