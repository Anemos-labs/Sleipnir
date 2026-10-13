package translate

import (
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// managerDecideLog is a team whose manager answered two of a worker's questions (a yes and a no with an instruction) and passed a
// third on to the person, who said yes.
func managerDecideLog() []events.Event {
	b := newLog(t0)
	const gap = 20 * time.Millisecond
	perm := func(cmd string) map[string]any {
		return map[string]any{"tool": "bash", "command": cmd, "reason": "./bin/app: not on the read-only allowlist", "role": "backend"}
	}
	decide := func(cmd string, allow bool, by, reason string) map[string]any {
		d := perm(cmd)
		d["allow"], d["by"], d["reason"] = allow, by, reason
		return d
	}
	b.add(0, "", "session.start", map[string]any{"swarm": true, "model": "m1", "root": "/work"})
	b.add(gap, "swarm", "agent.spawn", map[string]any{"id": "mgr", "role": "manager", "model": "m1"})
	b.add(gap, "swarm", "agent.spawn", map[string]any{"id": "be-1", "role": "backend", "model": "m1", "by": "mgr", "parent": "mgr"})
	b.add(gap, "be-1", "perm.ask", perm("PORT=18098 ./bin/app &"))
	b.add(gap, "be-1", "perm.decide", decide("PORT=18098 ./bin/app &", true, "manager", "approved by the manager"))
	b.add(gap, "be-1", "perm.ask", perm("./bin/seed --reset"))
	b.add(gap, "be-1", "perm.decide", decide("./bin/seed --reset", false, "manager",
		"denied by the manager (do not make it another way, with another tool or command; do what the manager says, or ask it) The manager says: use the test fixture instead"))
	b.add(gap, "be-1", "perm.ask", perm("./bin/migrate"))
	b.add(gap, "be-1", "perm.decide", decide("./bin/migrate", true, "user", "allowed by user"))
	b.add(gap, "", "session.end", map[string]any{"reason": "other"})
	return b.evs
}

// A question the manager settled reaches the page as an ask and its answer by "manager", sent together; one a person answered sends
// none from the log (its card is the approvals bridge's).
func TestTheManagersAnswersReachThePage(t *testing.T) {
	evs := translateLog(t, managerDecideLog(), "/work", true).decoded()
	asks, answers := ofKind(evs, "ask"), ofKind(evs, "answer")
	if len(asks) != 2 || len(answers) != 2 {
		t.Fatalf("%d asks and %d answers, want 2 and 2", len(asks), len(answers))
	}
	for i, a := range answers {
		q := asks[i]["q"].(map[string]any)
		if a["qid"] != q["id"] || a["by"] != "manager" || asks[i]["t"] != a["t"] {
			t.Errorf("pair %d: %v / %v", i, asks[i], a)
		}
	}
	if q := asks[0]["q"].(map[string]any); q["agent"] != "be-1" || q["cmd"] != "PORT=18098 ./bin/app &" || answers[0]["choice"] != 1.0 {
		t.Errorf("the yes: %v / %v", asks[0], answers[0])
	}
	if answers[1]["choice"] != 3.0 || answers[1]["note"] != "use the test fixture instead" {
		t.Errorf("the no: %v", answers[1])
	}
}
