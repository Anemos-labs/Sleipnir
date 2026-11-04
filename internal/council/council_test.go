package council

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/provider"
)

// fake answers every request with scores: cand for the subject whose images are the candidate's (found by its bytes), ref for
// the others. It records the prompts it was sent.
type fake struct {
	mu        sync.Mutex
	cand, ref float64
	reply     func(letters map[string]bool, candLetter string) string
	prompts   []*core.Prompt
}

func (f *fake) Profile() provider.Profile { return provider.Profile{Name: "fake"} }

func (f *fake) Do(_ context.Context, req *provider.Request, _ func(provider.Event)) (*provider.Response, error) {
	f.mu.Lock()
	f.prompts = append(f.prompts, req.Prompt)
	f.mu.Unlock()
	letters, cand, cur := map[string]bool{}, "", ""
	for _, b := range req.Prompt.Messages[0].Blocks {
		if b.Kind == core.BlockText && strings.HasPrefix(b.Text, "Product ") {
			cur = b.Text[8:9]
			letters[cur] = true
		}
		if b.Kind == core.BlockImage && strings.HasSuffix(b.MediaRef, "Y2FuZA==") { // base64 of "cand"
			cand = cur
		}
	}
	text := ""
	if f.reply != nil {
		text = f.reply(letters, cand)
	} else {
		scores := map[string]map[string]float64{}
		for l := range letters {
			v := f.ref
			if l == cand {
				v = f.cand
			}
			scores[l] = map[string]float64{}
			for _, c := range []string{"hierarchy", "color and contrast"} {
				scores[l][c] = v
			}
		}
		b, _ := json.Marshal(map[string]any{"scores": scores, "findings": []Finding{{Category: "hierarchy", Problem: "the title is lost", Fix: "make it 20px bold"}}})
		text = "here you go: " + string(b)
	}
	return &provider.Response{Turn: core.Turn{Blocks: []core.Block{core.Text(text)}}}, nil
}

func brief() Brief {
	return Brief{
		Product:    "a diagram editor",
		Categories: []string{"Hierarchy", "color and contrast"},
		Candidate:  Subject{Images: []Image{{Path: "a.png", MediaType: "image/png", Data: []byte("cand")}}},
		References: []Subject{{Name: "rival", Images: []Image{{Path: "r.png", MediaType: "image/png", Data: []byte("rival")}}}},
	}
}

func member(name string, f *fake) Member {
	return Member{Name: name, Provider: f, Model: cost.Model{ID: name + "-model"}}
}

func TestRoundPassesOnlyWhenEveryMemberRatesEveryCategoryAtTheTarget(t *testing.T) {
	hi, lo := &fake{cand: 9.2, ref: 8}, &fake{cand: 8.4, ref: 8}
	r, err := Run(context.Background(), brief(), []Member{member("a", hi), member("b", hi)}, Options{Target: 9, Round: 1})
	if err != nil || !r.Pass {
		t.Fatalf("pass expected: %+v %v", r, err)
	}
	r, err = Run(context.Background(), brief(), []Member{member("a", hi), member("b", lo)}, Options{Target: 9, Round: 1})
	if err != nil || r.Pass || len(r.Short) != 1 || !strings.Contains(r.Short[0], "b: below the target in hierarchy 8.4") {
		t.Fatalf("one low member must hold the round: %+v %v", r.Short, err)
	}
	if got := r.Reviews[0].Findings; len(got) != 1 || got[0].Fix != "make it 20px bold" {
		t.Fatalf("findings: %+v", got)
	}
}

func TestBeatReferences(t *testing.T) {
	f := &fake{cand: 9.2, ref: 9.5}
	r, _ := Run(context.Background(), brief(), []Member{member("a", f)}, Options{Target: 9, BeatReferences: true, Round: 1})
	if r.Pass || !strings.Contains(strings.Join(r.Short, ""), "does not beat rival") {
		t.Fatalf("a candidate scored under a reference passed: %+v", r.Short)
	}
}

// Staged: a slow member is asked only once the one before it reached the target.
func TestStagedWaitsForTheEarlierMember(t *testing.T) {
	fast, slow := &fake{cand: 7, ref: 8}, &fake{cand: 9.5, ref: 8}
	r, _ := Run(context.Background(), brief(), []Member{member("fast", fast), member("slow", slow)}, Options{Target: 9, Staged: true, Round: 1})
	if len(slow.prompts) != 0 || r.Reviews[1].Skipped == "" || r.Pass {
		t.Fatalf("the slow member was asked before the fast one reached the target: %+v", r.Reviews)
	}
	fast.cand = 9.1
	r, _ = Run(context.Background(), brief(), []Member{member("fast", fast), member("slow", slow)}, Options{Target: 9, Staged: true, Round: 2})
	if len(slow.prompts) != 1 || !r.Pass {
		t.Fatalf("the slow member was not asked once the fast one passed: %+v", r)
	}
}

// Members are told neither the target nor which subject is the work under review, and the order of subjects changes with the round.
func TestPromptHidesTheTargetAndTheCandidate(t *testing.T) {
	b := brief()
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for round := 1; round <= 12; round++ {
		p, letters := Prompt(b, round, "m")
		var all strings.Builder
		all.WriteString(p.System[0].Text)
		for _, blk := range p.Messages[0].Blocks {
			all.WriteString(blk.Text)
		}
		for _, leak := range []string{"candidate", "rival", "target", "9.0", "a.png"} {
			if strings.Contains(strings.ToLower(all.String()), leak) {
				t.Fatalf("round %d prompt says %q", round, leak)
			}
		}
		seen[letters["A"]] = true
	}
	if !seen[CandidateSubject] || !seen["rival"] {
		t.Fatalf("the candidate is always shown in the same place: %v", seen)
	}
}

func TestUnreadableAnswerIsNoVote(t *testing.T) {
	f := &fake{reply: func(map[string]bool, string) string { return `{"scores":{"A":{"hierarchy":9}}}` }}
	r, err := Run(context.Background(), brief(), []Member{member("a", f)}, Options{Target: 5, Round: 1})
	if err != nil || r.Pass || r.Reviews[0].Error == "" || len(f.prompts) != maxAttempts {
		t.Fatalf("an answer missing scores counted: %+v (%d asks) %v", r.Reviews[0], len(f.prompts), err)
	}
}

func TestParseRejectsOutOfRangeAndMapsLetters(t *testing.T) {
	letters := map[string]string{"A": "rival", "B": "candidate"}
	cats := []string{"hierarchy"}
	if _, _, ok := Parse(`{"scores":{"A":{"hierarchy":11},"B":{"hierarchy":5}}}`, letters, cats); ok {
		t.Fatal("a score of 11 was read")
	}
	s, _, ok := Parse("{note} then "+`{"scores":{"a":{"Hierarchy":7.26},"B":{"hierarchy":5}}}`, letters, cats)
	if !ok || s["rival"]["hierarchy"] != 7.3 || s["candidate"]["hierarchy"] != 5 {
		t.Fatalf("got %v %v", s, ok)
	}
}

func TestLedgerKeepsTheClimb(t *testing.T) {
	l := Ledger(filepath.Join(t.TempDir(), "council", "rounds.jsonl"))
	if n, err := l.Next(); n != 1 || err != nil {
		t.Fatalf("empty ledger: %d %v", n, err)
	}
	f := &fake{cand: 7, ref: 8}
	now := func() time.Time { return time.Unix(0, 0) }
	for i := 0; i < 3; i++ {
		n, _ := l.Next()
		r, err := Run(context.Background(), brief(), []Member{member("a", f)}, Options{Target: 9, Round: n, Now: now})
		if err != nil {
			t.Fatal(err)
		}
		if err := l.Append(r); err != nil {
			t.Fatal(err)
		}
		f.cand++
	}
	rs, err := l.Rounds()
	if err != nil || len(rs) != 3 || rs[2].N != 3 || !rs[2].Pass || rs[0].Pass {
		t.Fatalf("rounds: %+v %v", rs, err)
	}
	var w strings.Builder
	Report(&w, rs[:2], rs[2])
	out := w.String()
	for _, want := range []string{"round 1:  a 7.0/7.00 rival 8.00", "round 3:  a 9.0/9.00", "verdict: PASS", "fix: make it 20px bold"} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}
}

func TestValidate(t *testing.T) {
	for name, b := range map[string]Brief{
		"no candidate":   {},
		"repeated name":  {Candidate: Subject{Images: []Image{{}}}, References: []Subject{{Name: "candidate", Images: []Image{{}}}}},
		"empty ref":      {Candidate: Subject{Images: []Image{{}}}, References: []Subject{{Name: "x"}}},
		"repeated cat":   {Categories: []string{"a", "A"}, Candidate: Subject{Images: []Image{{}}}},
		"too many shots": {Candidate: Subject{Images: make([]Image, MaxImages+1)}},
	} {
		if err := b.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// silent never answers until its context ends.
type silent struct{}

func (silent) Profile() provider.Profile { return provider.Profile{Name: "silent"} }
func (silent) Do(ctx context.Context, _ *provider.Request, _ func(provider.Event)) (*provider.Response, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// A member that does not answer is a missing vote within MemberTimeout; the others' reviews stand and the round does not pass.
func TestSilentMemberTimesOut(t *testing.T) {
	f := &fake{cand: 9.5, ref: 8}
	var got []string
	var mu sync.Mutex
	r, err := Run(context.Background(), brief(), []Member{member("a", f), {Name: "mute", Provider: silent{}}}, Options{
		Target: 9, Round: 1, MemberTimeout: 50 * time.Millisecond,
		OnReview: func(rv Review) { mu.Lock(); got = append(got, rv.Member); mu.Unlock() },
	})
	if err != nil || r.Pass || !strings.Contains(r.Reviews[1].Error, "no answer within 50ms") || r.Reviews[0].Error != "" || len(got) != 2 {
		t.Fatalf("%+v %v %v", r.Reviews, got, err)
	}
}
