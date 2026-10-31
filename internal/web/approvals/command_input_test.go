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
