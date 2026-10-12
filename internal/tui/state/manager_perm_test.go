package state

import (
	"testing"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// A worker's question the manager answered closes like a person's answer, is counted as the manager's, and its feed line says so.
func TestAManagersAnswerIsCountedAsTheManagers(t *testing.T) {
	b := newB()
	st := New()
	apply(t, st, b.Emit("", events.TypeSessionStart, startPayload(nil)))
	apply(t, st, b.Spawn("w-1", "backend", "T1", "mgr"))
	apply(t, st, b.PermAsk("w-1", "backend", "bash", "./bin/app", "not on the read-only allowlist"))
	apply(t, st, b.PermDecide("w-1", "backend", "bash", "./bin/app", "approved by the manager", true, PermByManager, ""))
	apply(t, st, b.PermAsk("w-1", "backend", "bash", "./bin/other", "not on the read-only allowlist"))
	apply(t, st, b.PermDecide("w-1", "backend", "bash", "./bin/other", "denied by the manager", false, PermByManager, ""))
	sn := st.Snapshot()
	if p := sn.Perms; len(p.Pending) != 0 || p.ByManager != 2 || p.ManagerApproved != 1 || p.ByUser != 0 {
		t.Fatalf("perms: %s", js(p))
	}
	var last FeedLine
	for _, l := range sn.Feed {
		if l.Kind == FeedPerm {
			last = l
		}
	}
	if last.Detail != "by the manager: denied by the manager" {
		t.Errorf("the feed line of the manager's no: %+v", last)
	}
}
