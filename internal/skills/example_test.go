package skills_test

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/anemos-labs/sleipnir/internal/skills"
)

// A catalog puts one line per skill in the shared prompt layer and loads the
// body only when the model asks for it.
func Example() {
	root, _ := os.MkdirTemp("", "skills-example")
	defer func() { _ = os.RemoveAll(root) }()
	dir := filepath.Join(root, ".claude", "skills", "deploy")
	_ = os.MkdirAll(filepath.Join(dir, "scripts"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: deploy\ndescription: Deploy the app to an environment\nallowed-tools: Bash(make deploy:*)\n---\nRun `make deploy ENV=$1`, then check $ARGUMENTS.\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "scripts", "check.sh"), []byte("#!/bin/sh\n"), 0o755)

	cat, warnings := skills.Discover(skills.Opts{Root: root, Home: filepath.Join(root, "home"), TrustProject: true})
	fmt.Println("warnings:", len(warnings))
	fmt.Println(cat.Listing(0, nil))

	loaded, err := cat.LoadForModel("deploy", "staging")
	fmt.Println(err)
	fmt.Println(loaded.Body)
	fmt.Println(loaded.Files, loaded.AllowedTools)
	// Output:
	// warnings: 0
	// deploy: Deploy the app to an environment
	// <nil>
	// Run `make deploy ENV=staging`, then check staging.
	// [scripts/check.sh] [Bash(make deploy:*)]
}
