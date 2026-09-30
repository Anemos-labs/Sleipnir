package traj

import (
	"fmt"
	"testing"
)

func TestScratchFixture(t *testing.T) {
	r, err := Open("testdata/agent_run")
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("events", len(r.evs), "reqs", len(r.reqOrder), "resps", len(r.respOrder), "torn", r.torn)
	if ms := r.Verify(); len(ms) != 0 {
		t.Fatalf("verify: %v", ms)
	}
	for _, q := range r.reqOrder {
		p, err := r.Prompt(q.id)
		if err != nil {
			t.Fatal(err)
		}
		msgs, _ := r.msgHashes(q.id)
		fmt.Println(q.id, q.kind, len(p.Messages), len(msgs), len(p.Tools), len(p.System))
	}
}
