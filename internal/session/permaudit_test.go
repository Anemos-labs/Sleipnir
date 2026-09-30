package session

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/perm"
)

func TestPermissionAuditBecomesEvents(t *testing.T) {
	dir := t.TempDir()
	log, err := events.Open(dir, "audit")
	if err != nil {
		t.Fatal(err)
	}
	s := &Session{Log: log}
	long := strings.Repeat("x", 1000)
	s.auditPermission(perm.Audit{Kind: "ask", Request: perm.Request{Agent: "a1", Tool: "bash", Command: long, Role: "backend",
		Paths: []string{"/a", "/b", "/c", "/d", "/e", "/f", "/g"}}, Reason: "reads /x outside the workspace"})
	s.auditPermission(perm.Audit{Kind: "decide", Request: perm.Request{Agent: "a1", Tool: "bash", Command: "ls"}, Reason: "approval required",
		Decision: perm.Decision{Remember: perm.ScopeSession}, By: "no one"})

	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	var ask, decide []events.Event
	if err := events.Scan(filepath.Join(dir, "events.jsonl"), func(e events.Event) error {
		switch e.Type {
		case events.TypePermAsk:
			ask = append(ask, e)
		case events.TypePermDecide:
			decide = append(decide, e)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(ask) != 1 || len(decide) != 1 || ask[0].Agent != "a1" {
		t.Fatalf("events: %d asks, %d decides", len(ask), len(decide))
	}
	var a struct {
		Tool, Command, Reason, Role string
		Paths                       []string
		Allow                       *bool
	}
	if err := json.Unmarshal(ask[0].Data, &a); err != nil {
		t.Fatal(err)
	}
	if a.Tool != "bash" || a.Role != "backend" || a.Reason == "" || len([]rune(a.Command)) != 400 || len(a.Paths) != 5 || a.Allow != nil {
		t.Errorf("perm.ask = %+v (command of %d runes)", a, len([]rune(a.Command)))
	}
	var d struct {
		Allow    bool
		By       string
		Remember string
	}
	if err := json.Unmarshal(decide[0].Data, &d); err != nil {
		t.Fatal(err)
	}
	if d.Allow || d.By != "no one" || d.Remember != "session" {
		t.Errorf("perm.decide = %+v", d)
	}
}
