package swarm

import (
	"context"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/cost"
)

// With reviewer slots on two different models, a merge counts as reviewed only once reviewers on both models covered it; a second
// review by the same model does not count twice. One model's reviewers keep the single review of before.
func TestTwoReviewerModelsMustBothCoverAMerge(t *testing.T) {
	cfg := Config{MaxWorkers: 8, Team: []string{"backend", "reviewer", "reviewer"}}
	r := newRVRigWith(t, cfg, func(context.Context, *rvCall) rvReply { return rvReply{Text: "noted"} }, func(d *Deps) {
		d.SlotModels = []RoleModel{{}, {}, {Provider: d.Provider, Model: cost.Model{ID: "other-reviewer-model", ContextTokens: 1_000_000, Cache: cost.OpenAICacheModel()}}}
	})
	s := r.sw
	if q := s.reviewQuorum(); q != 2 {
		t.Fatalf("quorum = %d, want 2", q)
	}
	rv1, err := s.Spawn(SpawnReq{Role: ReviewerRole, Title: "review one", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	rv2, err := s.Spawn(SpawnReq{Role: ReviewerRole, Title: "review two", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	if s.memberModel(rv1) == s.memberModel(rv2) {
		t.Fatalf("both reviewers run on %s", s.memberModel(rv1))
	}
	s.noteAccepted(Task{ID: "T1", Role: "backend"})
	review := func(id, owner string) {
		t.Helper()
		task := Task{ID: id, Role: ReviewerRole, Owner: owner}
		if err := s.reviewSubmission(task)(nil, task); err != nil {
			t.Fatal(err)
		}
		s.noteAccepted(task)
	}
	review("R1", rv1)
	if got := s.reviewPending(); got != "T1" {
		t.Fatalf("one model's review cleared the gate: %q", got)
	}
	if line := s.reviewLine("T1"); !strings.Contains(line, "two reviewers on different models") {
		t.Errorf("review line: %s", line)
	}
	review("R2", rv1)
	if got := s.reviewPending(); got != "T1" {
		t.Fatalf("a second review by the same model counted: %q", got)
	}
	review("R3", rv2)
	if got := s.reviewPending(); got != "" {
		t.Fatalf("reviews by both models did not clear the gate: %q", got)
	}
	// a later merge needs both again
	s.noteAccepted(Task{ID: "T2", Role: "backend"})
	review("R4", rv2)
	if got := s.reviewPending(); got != "T2" {
		t.Fatalf("after a new merge, one review cleared the gate: %q", got)
	}
}

func TestOneReviewerModelKeepsTheSingleReview(t *testing.T) {
	r := newRVRigWith(t, Config{MaxWorkers: 8, Team: []string{"backend", "reviewer", "reviewer"}}, func(context.Context, *rvCall) rvReply { return rvReply{Text: "noted"} }, nil)
	if q := r.sw.reviewQuorum(); q != 1 {
		t.Fatalf("quorum = %d, want 1", q)
	}
	if !strings.Contains(r.sw.reviewLine("T1"), "create a task for the reviewer") {
		t.Error("single-model review line changed")
	}
}

func TestQuorumCovered(t *testing.T) {
	for _, tc := range []struct {
		by     map[string]int
		q, out int
	}{
		{map[string]int{"a": 3}, 1, 3},
		{map[string]int{"a": 3}, 2, 0},
		{map[string]int{"a": 3, "b": 1}, 2, 1},
		{map[string]int{"a": 3, "b": 5, "c": 4}, 2, 4},
	} {
		if got := quorumCovered(tc.by, tc.q); got != tc.out {
			t.Errorf("quorumCovered(%v, %d) = %d, want %d", tc.by, tc.q, got, tc.out)
		}
	}
}

// A team whose reviewers share the writers' model while another model is on the team is told, once, that a reviewer on another model
// would help; a team on one model is not (not everyone has two), nor one whose reviewers already run on a model no writer uses.
func TestAReviewerOnTheWritersModelIsGivenAHintOnlyWhenAnotherModelIsThere(t *testing.T) {
	other := func(d *Deps, id string) RoleModel {
		return RoleModel{Provider: d.Provider, Model: cost.Model{ID: id, ContextTokens: 1_000_000, Cache: cost.OpenAICacheModel()}}
	}
	reply := func(context.Context, *rvCall) rvReply { return rvReply{Text: "noted"} }
	for _, c := range []struct {
		name  string
		team  []string
		slots func(d *Deps) []RoleModel
		want  string
	}{
		{name: "one model", team: []string{"backend", "reviewer"}, want: ""},
		{name: "writers on two models, the reviewer on one of theirs", team: []string{"backend", "frontend", "reviewer"},
			slots: func(d *Deps) []RoleModel { return []RoleModel{{}, other(d, "front-model"), {}} }, want: "reviewer=front-model"},
		{name: "the reviewer on a model no writer uses", team: []string{"backend", "reviewer"},
			slots: func(d *Deps) []RoleModel { return []RoleModel{{}, other(d, "review-model")} }, want: ""},
		{name: "reviewers on two models", team: []string{"backend", "frontend", "reviewer", "reviewer"},
			slots: func(d *Deps) []RoleModel {
				return []RoleModel{{}, other(d, "front-model"), {}, other(d, "front-model")}
			}, want: ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRVRigWith(t, Config{MaxWorkers: 8, Team: c.team}, reply, func(d *Deps) {
				if c.slots != nil {
					d.SlotModels = c.slots(d)
				}
			})
			got := r.sw.reviewerModelHint()
			if c.want == "" && got != "" || c.want != "" && !strings.Contains(got, c.want) {
				t.Errorf("hint = %q, want %q", got, c.want)
			}
		})
	}
}
