package traj

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/reee344/sleipnir/internal/rl"
)

func TestScratchFixture(t *testing.T) {
	r, err := Open("testdata/agent_run")
	if err != nil {
		t.Fatal(err)
	}
	ep, err := r.Episode(Options{TaskID: "fx", Policy: rl.PolicyRef{Model: "mock-1"}})
	if err != nil {
		t.Fatal(err)
	}
	a := ep.Agents[0]
	fmt.Println("agent", a.ID, a.Role, a.Model, a.Status, len(a.Steps))
	for _, s := range a.Segments {
		fmt.Printf("seg %+v\n", s)
	}
	for i, s := range a.Steps {
		fmt.Printf("%2d %-10s %-9s seg=%d ep=%d train=%v tok=%v obs=%d in=%d shared=%s/%d lat=%d stop=%s hit=%.2f\n", i, s.ID, s.Kind, s.Segment, s.Epoch, s.Trainable, s.Tokens != nil, len(s.Observations), s.Prompt.Tokens, s.Prompt.SharedPrefix, s.Prompt.SharedMessages, s.LatencyMs, s.Completion.Stop, s.Cache.HitRatio)
	}
	for _, e := range ep.Edges {
		fmt.Printf("edge %+v\n", e)
	}
	b, _ := json.MarshalIndent(ep.Signals, "", " ")
	fmt.Println(string(b))
	fmt.Println("flags", ep.Flags, "outcome", ep.Outcome, "cost", ep.Cost, "prov", ep.Provenance, "harness", ep.Harness, "group", ep.Group, ep.ID)
	fmt.Println(ep.StartedAt, ep.EndedAt)
	if _, err := json.Marshal(ep); err != nil {
		t.Fatal(err)
	}
}
