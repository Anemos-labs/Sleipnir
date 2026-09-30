//go:build unix

package hooks_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/reee344/sleipnir/internal/hooks"
)

// A settings file's "hooks" object becomes a Set; a Runner runs the hooks that
// match an event and folds their answers into one Result.
func Example() {
	settings := map[string]json.RawMessage{
		"PreToolUse": json.RawMessage(`[{"matcher": "Bash", "hooks": [
			{"type": "command", "command": "echo 'no force pushes' >&2; exit 2", "if": "Bash(git push --force:*)"}]}]`),
	}
	set, err := hooks.ParseAs(hooks.OriginUser, "settings.json", settings)
	if err != nil {
		fmt.Println(err)
		return
	}
	dir, _ := os.MkdirTemp("", "hooks-example")
	defer func() { _ = os.RemoveAll(dir) }()
	runner := &hooks.Runner{Set: set, Dir: dir, Trusted: true}

	for _, cmd := range []string{"git push --force origin main", "git status"} {
		input, _ := json.Marshal(map[string]string{"command": cmd})
		res, err := runner.Run(context.Background(), hooks.Event{Name: hooks.PreToolUse, Tool: "bash", Input: input, Cwd: dir})
		fmt.Printf("%-30s blocked=%-5v decision=%q reason=%q err=%v\n", cmd, res.Blocked, res.Decision, res.Reason, err)
	}
	// Output:
	// git push --force origin main   blocked=true  decision="deny" reason="no force pushes" err=<nil>
	// git status                     blocked=false decision="" reason="" err=<nil>
}
