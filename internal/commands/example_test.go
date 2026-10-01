package commands_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/anemos-labs/sleipnir/internal/commands"
)

// Expansion substitutes arguments, includes project files, and runs shell
// commands only through the executor the caller supplies.
func Example() {
	root, _ := os.MkdirTemp("", "commands-example")
	defer func() { _ = os.RemoveAll(root) }()
	_ = os.MkdirAll(filepath.Join(root, ".claude", "commands"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "notes.txt"), []byte("remember the milk"), 0o644)
	_ = os.WriteFile(filepath.Join(root, ".claude", "commands", "todo.md"), []byte("---\ndescription: Plan a task\n---\nPlan $ARGUMENTS using @notes.txt. Branch: !`git branch --show-current`\n"), 0o644)

	reg, _ := commands.Load(commands.Opts{
		Root: root, Home: filepath.Join(root, "home"), TrustProject: true,
		// The caller decides whether and how a command may run, typically through its permission engine.
		Exec: func(_ context.Context, cmd string) (string, error) { return "main (from " + cmd + ")", nil },
	})
	for _, c := range reg.List() {
		fmt.Printf("/%s: %s\n", c.Name, c.Description)
	}
	e, err := reg.Expand(context.Background(), "todo", "the release")
	fmt.Println(err)
	fmt.Println(e.Prompt)
	// Output:
	// /todo: Plan a task
	// <nil>
	// Plan the release using @notes.txt. Branch: main (from git branch --show-current)
	//
	// Referenced files (contents inserted by the harness):
	//
	// Contents of notes.txt:
	// ```
	// remember the milk
	// ```
}
