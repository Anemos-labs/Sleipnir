package swarm

import (
	"context"
	"testing"
)

// The ids an earlier process used (read back from its log on a resume) are not
// handed out again: a worker spawned after the resume gets the next free
// number, so two agents never share an id, or request ids, in one log. Ids of
// unknown roles or without a number are ignored.
func TestReservedIDsAreNotReused(t *testing.T) {
	r := newRVRig(t, Config{}, func(_ context.Context, c *rvCall) rvReply { return rvReply{Text: "done"} })
	r.sw.ReserveIDs(map[string]string{"be-3": "backend", "zz-9": "nosuchrole", "be-x": "backend"})
	id, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "after the resume", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	if id != "be-4" {
		t.Errorf("the first worker after the resume is %s, want be-4", id)
	}
}
