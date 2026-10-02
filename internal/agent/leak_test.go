package agent_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
)

func leakEvents(r *rig) int {
	n := 0
	for _, e := range r.log.OfType(events.TypeAgentStuck) {
		var p struct{ Phase string }
		_ = json.Unmarshal(e.Data, &p)
		if p.Phase == "leak" {
			n++
		}
	}
	return n
}

// A model served through a gateway that does not parse its chat format writes its tool call as text: gpt-oss-20b's answer, once, was
// "to=functions.read <|constrain|>json<|message|>{...}<|call|>". No tool ran, and the run took that for the end of the work (exit 0).
// The model is told once that it was not an answer, and the work goes on.
func TestAToolCallWrittenAsTextIsNotTheAnswer(t *testing.T) {
	r := newRig(t, rigOpts{noCompact: true, steps: 20}, func(c *mock.Call) mock.Reply {
		if assistantTurns(c) == 0 {
			return mock.Reply{Text: "Scrolling? to=functions.read <|constrain|>json<|message|>{\"path\":\"c/c.go\"}<|call|>"}
		}
		return mock.Reply{Text: "done: nothing to change"}
	})
	res, err := r.agent.Run(context.Background(), "fix it")
	if err != nil {
		t.Fatal(err)
	}
	if res.Steps != 2 || res.Text != "done: nothing to change" {
		t.Fatalf("steps %d, answer %q: the leaked call must be sent back, and the next answer taken", res.Steps, res.Text)
	}
	if n := leakEvents(r); n != 1 {
		t.Fatalf("%d agent.stuck events of the leak kind, want 1", n)
	}
	noted := false
	for _, turn := range r.agent.Thread().Snapshot().Turns {
		for _, b := range turn.Blocks {
			if b.Kind == core.BlockText && strings.HasPrefix(b.Text, "[harness] Your last message was not an answer") && strings.Contains(b.Text, "(to=functions.read)") {
				noted = true
			}
		}
	}
	if !noted {
		t.Fatal("the note that names the leaked markup is not in the thread")
	}
}

// A model that cannot stop doing it is not asked for ever: twice, then its text is the answer, as it was.
func TestALeakedCallIsSentBackTwiceAtMost(t *testing.T) {
	r := newRig(t, rigOpts{noCompact: true, steps: 20}, func(c *mock.Call) mock.Reply {
		return mock.Reply{Text: "<|start|>assistant<|channel|>commentary to=functions.read<|call|>"}
	})
	res, err := r.agent.Run(context.Background(), "fix it")
	if err != nil {
		t.Fatal(err)
	}
	if res.Steps != 3 || !strings.Contains(res.Text, "<|call|>") {
		t.Fatalf("steps %d, answer %q: two sends back, then the answer", res.Steps, res.Text)
	}
	if n := leakEvents(r); n != 2 {
		t.Fatalf("%d leak notes, want 2", n)
	}
}

// Ordinary answers, code and angle brackets are not markup of a chat format.
func TestPlainAnswersAreNotTakenForLeakedCalls(t *testing.T) {
	for _, text := range []string{
		"Fixed: the comparison used <= where < was meant, and a || b became a && b.",
		"```go\nvar ch chan<- int = make(chan int)\nif a <-b { }\n```",
		"The HTML is <div class=\"x\">hello</div> and the tag <br> is gone.",
		"A pipe | and a bar || are not markup, nor is <|> alone.",
	} {
		r := newRig(t, rigOpts{noCompact: true, steps: 5}, func(*mock.Call) mock.Reply { return mock.Reply{Text: text} })
		res, err := r.agent.Run(context.Background(), "go")
		if err != nil || res.Steps != 1 || res.Text != text || leakEvents(r) != 0 {
			t.Errorf("%q: steps %d, text %q, err %v, %d leak notes", text, res.Steps, res.Text, err, leakEvents(r))
		}
	}
}
