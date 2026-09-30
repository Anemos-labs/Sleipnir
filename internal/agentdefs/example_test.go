package agentdefs_test

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/reee344/sleipnir/internal/agentdefs"
)

// A markdown definition becomes a swarm role plus a permission profile.
func Example() {
	root, _ := os.MkdirTemp("", "agentdefs-example")
	defer func() { _ = os.RemoveAll(root) }()
	_ = os.MkdirAll(filepath.Join(root, ".claude", "agents"), 0o755)
	_ = os.WriteFile(filepath.Join(root, ".claude", "agents", "code-reviewer.md"), []byte("---\nname: code-reviewer\ndescription: Reviews diffs\ntools: Read, Grep, Glob\n---\nYou review diffs. You never edit files.\n"), 0o644)

	defs, warnings := agentdefs.Load(agentdefs.Opts{Root: root, Home: filepath.Join(root, "home"), TrustProject: true, ReservedShorts: []string{"mgr", "rv"}})
	fmt.Println("warnings:", len(warnings))
	d := defs[0]
	r := d.ToRole()
	fmt.Printf("%s (%s) readonly=%v steps=%d\n", r.Name, r.Short, r.ReadOnly, r.MaxSteps)
	fmt.Println(d.Profile().Mode, d.Profile().Deny)
	// Output:
	// warnings: 0
	// code-reviewer (cr) readonly=true steps=150
	// plan [Bash Edit WebFetch WebSearch mcp__*]
}
