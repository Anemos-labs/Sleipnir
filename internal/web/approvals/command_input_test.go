package approvals

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/perm"
)

// A shell request that comes without its command (one a hook asks about) shows the command its input runs, or is refused: a card
// that names no command is never asked.
func TestAShellRequestWithoutItsCommandShowsTheInputsOrIsRefused(t *testing.T) {
	r := newRig(t, Config{})
	in, _ := json.Marshal(map[string]string{"command": "rm -rf build"})
	p := r.ask(context.Background(), "shop", perm.Request{Agent: "fe-1", Tool: "bash", Input: in, Summary: "run a command"})
	if p.q.Cmd != "rm -rf build" || p.q.What != "this command" {
		t.Errorf("question %+v", p.q)
	}
	r.b.CancelTab("shop", ByClosed)
	p.decided(t)

	rr, refused := refusedRig(t, Config{})
	if d := rr.b.Prompter("shop", "/proj", nil, nil)(context.Background(), perm.Request{Agent: "be-1", Tool: "bash", Summary: "run a command"}); d.Allow ||
		!strings.Contains(d.Reason, "command to run cannot be read") {
		t.Errorf("%+v", d)
	}
	if len(*refused) != 1 {
		t.Errorf("refused %q", *refused)
	}
}

// The command is read as the shell reads its input: the key "command" exactly, so an input that also holds a "Command" shows the
// command that runs, and one that holds only a "Command" names none and is refused.
func TestAShellRequestShowsTheCommandKeyTheShellReads(t *testing.T) {
	r := newRig(t, Config{})
	p := r.ask(context.Background(), "shop", perm.Request{Agent: "fe-1", Tool: "bash", Input: json.RawMessage(`{"command":"rm -rf build","Command":"ls"}`), Summary: "run a command"})
	if p.q.Cmd != "rm -rf build" {
		t.Errorf("the card shows %q, the shell runs rm -rf build", p.q.Cmd)
	}
	r.b.CancelTab("shop", ByClosed)
	p.decided(t)

	rr, _ := refusedRig(t, Config{})
	if d := rr.b.Prompter("shop", "/proj", nil, nil)(context.Background(), perm.Request{Agent: "be-1", Tool: "bash", Input: json.RawMessage(`{"Command":"ls"}`), Summary: "run a command"}); d.Allow ||
		!strings.Contains(d.Reason, "command to run cannot be read") {
		t.Errorf("an input with only a Command key: %+v", d)
	}
}
