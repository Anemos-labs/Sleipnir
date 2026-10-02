package goal

import (
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/plan"
)

func TestParseVerdictReadsWhatAJudgeWrites(t *testing.T) {
	for _, c := range []struct {
		in   string
		kind string
		ok   bool
	}{
		{`{"verdict":"done","reason":"tests pass","left":[]}`, Done, true},
		{"```json\n{\"verdict\": \"Continue\", \"reason\": \"no test was run\", \"left\": [\"run go test\"]}\n```", Continue, true},
		{`Here is my view: {"verdict":"blocked","reason":"needs the API key"} thanks`, Blocked, true},
		{`{"verdict":"maybe"}`, Continue, false},
		{`it is done, I think`, Continue, false}, // a judge that cannot be read never ends work that may not be done
		{``, Continue, false},
	} {
		if v, ok := ParseVerdict(c.in); v.Kind != c.kind || ok != c.ok {
			t.Errorf("%q: %+v ok=%v, want %s ok=%v", c.in, v, ok, c.kind, c.ok)
		}
	}
	if v, _ := ParseVerdict(`{"verdict":"continue","reason":"x","left":["1","2","3","4","5","6","7","8","9","10"]}`); len(v.Left) != 8 {
		t.Errorf("%d items kept, want 8", len(v.Left))
	}
}

// The loop ends by itself: when the goal is met, when the judge says only a person can go on, when three turns in a row bring nothing, and when
// the continuations are used up. It does not end while there is progress.
func TestAGoalStopsWhereItShould(t *testing.T) {
	g := New("make the tests pass")
	if got := g.Apply(Verdict{Kind: Continue, Reason: "the build fails"}, true); got != Resume || g.Turns != 1 {
		t.Fatalf("first continue: %v turns=%d", got, g.Turns)
	}
	if got := g.Apply(Verdict{Kind: Continue, Reason: "two tests fail"}, true); got != Resume || g.Repeats != 0 {
		t.Fatalf("progress resets the count: %v repeats=%d", got, g.Repeats)
	}
	// the same reason three times, or turns with no work, are no progress
	g.Apply(Verdict{Kind: Continue, Reason: "two tests fail"}, true)
	g.Apply(Verdict{Kind: Continue, Reason: "Two tests fail "}, true)
	if got := g.Apply(Verdict{Kind: Continue, Reason: "two tests fail"}, true); got != Pause || !strings.Contains(g.Paused, "no progress") {
		t.Errorf("the same reason again and again: %v %q", got, g.Paused)
	}
	g = New("x")
	g.Apply(Verdict{Kind: Continue, Reason: "a"}, false)
	g.Apply(Verdict{Kind: Continue, Reason: "b"}, false)
	if got := g.Apply(Verdict{Kind: Continue, Reason: "c"}, false); got != Pause {
		t.Errorf("three turns with no tool call: %v", got)
	}
	g = New("x")
	if got := g.Apply(Verdict{Kind: Blocked, Reason: "needs a key"}, true); got != Pause || !strings.Contains(g.Paused, "needs a key") {
		t.Errorf("blocked: %v %q", got, g.Paused)
	}
	g = New("x")
	if got := g.Apply(Verdict{Kind: Done, Reason: "go test passes"}, true); got != Stop {
		t.Errorf("done: %v", got)
	}
	g = New("x")
	g.Max = 2
	g.Apply(Verdict{Kind: Continue, Reason: "a"}, true)
	g.Apply(Verdict{Kind: Continue, Reason: "b"}, true)
	if got := g.Apply(Verdict{Kind: Continue, Reason: "c"}, true); got != Pause || !strings.Contains(g.Paused, "2 continuations") {
		t.Errorf("the limit: %v %q", got, g.Paused)
	}
}

// A nudge says what is missing and what is open, and asks for evidence: it is not the goal said again.
func TestAContinuationSaysWhatIsMissing(t *testing.T) {
	g := New("add paging to /items and test it")
	g.Turns = 2
	steps := []plan.Item{{Step: "write the handler", Status: plan.Done}, {Step: "write the test", Status: plan.Pending}}
	text := Continuation(g, steps, Verdict{Kind: Continue, Reason: "no test was written", Left: []string{"a test of the last page"}})
	for _, want := range []string{"continuation 2", "add paging to /items", "Not met yet: no test was written", "missing: a test of the last page", "write the test", "evidence"} {
		if !strings.Contains(text, want) {
			t.Errorf("the continuation lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "write the handler") {
		t.Errorf("a step that is done is not an open step:\n%s", text)
	}
	if p := JudgePrompt(g, steps, "I added it", []string{"bash go test → ok"}); !strings.Contains(p, "[done] write the handler") || !strings.Contains(p, "bash go test") || !strings.Contains(p, "I added it") {
		t.Errorf("the judge was not shown the plan, the answer and the evidence:\n%s", p)
	}
}
