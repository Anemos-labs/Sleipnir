package session_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// A PreToolUse hook that answers "ask" puts the call to the person even when the rules allow it, and a refusal stops it.
func TestPreToolUseAskIsPutToThePerson(t *testing.T) {
	for _, approve := range []bool{false, true} {
		repo := newRepo(t)
		client, model := startMock(t, func(c *mock.Call) mock.Reply {
			if assistantTurns(c) == 0 {
				return mock.Reply{Text: "making it", ToolCalls: []mock.ToolCall{call("c1", "bash", map[string]any{"command": "touch made"})}}
			}
			return mock.Reply{Text: "finished"}
		})
		o := opts(t, repo, client, model)
		o.Mode = perm.ModeAcceptEdits
		o.Allow = []string{"Bash(touch:*)"}
		o.Config = hookConfig(t, map[string]string{
			"PreToolUse": `cat >/dev/null; echo '{"hookSpecificOutput":{"permissionDecision":"ask","permissionDecisionReason":"touching is reviewed"}}'`,
		})
		var asked []string
		o.Prompter = func(_ context.Context, r perm.Request) perm.Decision {
			asked = append(asked, r.Summary)
			return perm.Decision{Allow: approve, Reason: "the person said so"}
		}
		s, err := session.New(context.Background(), o)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Run(context.Background(), "make it"); err != nil {
			t.Fatal(err)
		}
		s.Close()
		if len(asked) != 1 || !strings.Contains(asked[0], "touching is reviewed") {
			t.Errorf("approve=%v: asked %q", approve, asked)
		}
		_, err = os.Stat(filepath.Join(repo, "made"))
		if made := err == nil; made != approve {
			t.Errorf("approve=%v: the file was made = %v", approve, made)
		}
	}
}
