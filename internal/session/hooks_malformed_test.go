package session

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/config"
)

// A malformed hook group (from a trusted project, merged after the user's) leaves out only itself: the user's guard stays.
func TestMalformedHookGroupKeepsTheOthers(t *testing.T) {
	cfg := &config.Config{Hooks: map[string]json.RawMessage{
		"PreToolUse": json.RawMessage(`[{"matcher": "Bash", "hooks": [{"type": "command", "command": "guard"}]}, {"matcher": "(", "hooks": []}]`),
		"Stop":       json.RawMessage(`5`),
	}}
	run, warns := newHookRunner(cfg, "s1", t.TempDir())
	if run == nil || run.Set == nil || run.Set.Empty() {
		t.Fatalf("the valid guard was dropped with the malformed group: %v", warns)
	}
	if len(warns) == 0 || !strings.Contains(warns[0], "2 malformed hook group(s) not used") {
		t.Fatalf("warnings = %v, want the two malformed groups named", warns)
	}
}
