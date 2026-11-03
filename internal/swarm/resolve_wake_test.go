package swarm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// A task closed as no longer needed tells the owners of tasks blocked on it: nothing will land for it, so a worker waiting
// on it would otherwise wait until someone noticed. In a team run the manager replaced the booking backend task with a new
// one, and the tour task blocked on it stayed blocked on a task that was done.
func TestResolveTellsTheTasksBlockedOnIt(t *testing.T) {
	r := newRVRig(t, Config{MaxWorkers: 2, Team: []string{"backend", "frontend"}}, idleModel)
	r.sw.StartManager()
	backend, err := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "booking backend", Role: "backend"})
	if err != nil {
		t.Fatal(err)
	}
	tour, err := r.sw.Board.CreateTask("mgr", TaskSpec{Title: "tour screenshots", Role: "frontend"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := r.sw.Spawn(SpawnReq{Role: "frontend", TaskID: tour.ID, By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	rvWait(t, id+" to own "+tour.ID, func() bool {
		c, _ := r.sw.Board.Snapshot().Task(tour.ID)
		return c.Owner == id && c.Status == StatusDoing
	})
	if err := r.sw.Board.BlockOn(id, tour.ID, backend.ID, "waits for the booking pages"); err != nil {
		t.Fatal(err)
	}
	res := r.callTool(context.Background(), "task", "mgr", "manager", map[string]any{"action": "resolve", "id": backend.ID, "text": "superseded by T9"})
	if res.IsError {
		t.Fatalf("resolve: %s", res.Text)
	}
	var told string
	for _, e := range r.log.OfType(events.TypeMailSend) {
		var m struct{ From, To, Text string }
		_ = json.Unmarshal(e.Data, &m)
		if m.From == harnessSender && m.To == id && strings.Contains(m.Text, "closed as no longer needed") {
			told = m.Text
		}
	}
	want := backend.ID + ", which " + tour.ID + " waits on, was closed as no longer needed (superseded by T9): nothing will land for it."
	if !strings.HasPrefix(told, want) {
		t.Fatalf("the blocked worker was told %q, want it to start %q", told, want)
	}
}
