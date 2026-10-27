package perm

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// A tool request naming more paths than the engine resolves one by one is refused in every mode: the paths past the bound
// would otherwise reach the tool with no protection, rule or confinement having seen them.
func TestAToolRequestWithTooManyPathsIsRefusedInEveryMode(t *testing.T) {
	for _, m := range []Mode{ModeDefault, ModeAcceptEdits, ModePlan, ModeBypass, ModeYolo} {
		root := t.TempDir()
		e, err := NewEngine(Config{Mode: m, Root: root, Home: t.TempDir(), Deny: []string{"Edit(secret.txt)"}})
		if err != nil {
			t.Fatal(err)
		}
		paths := make([]string, 0, maxOperands+1)
		for i := 0; i < maxOperands; i++ {
			paths = append(paths, filepath.Join(root, "f"+strconv.Itoa(i)+".txt"))
		}
		paths = append(paths, filepath.Join(root, "secret.txt"))
		d := e.Check(context.Background(), Request{Tool: "apply_patch", Writes: true, Paths: paths})
		if d.Allow || !strings.Contains(d.Reason, "more than the") {
			t.Errorf("%s: %d paths: %+v", m, len(paths), d)
		}
	}
}

// The memory tool names no path, but it replaces a notes file: plan mode, which changes nothing, refuses it, and every other
// mode leaves it to the person's rules for the tool.
func TestPlanModeRefusesAMemoryChange(t *testing.T) {
	for _, tc := range []struct {
		mode  Mode
		allow bool
	}{{ModePlan, false}, {ModeDefault, true}, {ModeAcceptEdits, true}, {ModeBypass, true}} {
		e, err := NewEngine(Config{Mode: tc.mode, Root: t.TempDir(), Home: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		if d := e.Check(context.Background(), Request{Tool: ToolMemory, Summary: "remember: x"}); d.Allow != tc.allow {
			t.Errorf("%s: a memory change: %+v", tc.mode, d)
		}
	}
	// A rule the person wrote for the tool still decides in plan mode.
	e, err := NewEngine(Config{Mode: ModePlan, Root: t.TempDir(), Home: t.TempDir(), Allow: []string{"memory"}})
	if err != nil {
		t.Fatal(err)
	}
	if d := e.Check(context.Background(), Request{Tool: ToolMemory, Summary: "remember: x"}); !d.Allow {
		t.Errorf("plan mode with an allow rule for memory: %+v", d)
	}
}
