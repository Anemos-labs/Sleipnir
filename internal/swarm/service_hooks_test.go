package swarm

import (
	"context"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/agent"
)

// The mailman's one tool is mail, whether or not the tool it calls asks the permission requester.
func TestServiceHooksRefuseEveryToolButMail(t *testing.T) {
	h := &serviceHooks{only: map[string]bool{"mail": true}, role: "mailman"}
	if o := h.BeforeTool(context.Background(), agent.ToolHookCall{Tool: "research"}); !o.Veto {
		t.Fatal("research was let through")
	}
	if o := h.BeforeTool(context.Background(), agent.ToolHookCall{Tool: "mail"}); o.Veto {
		t.Fatalf("mail was refused: %+v", o)
	}
}
