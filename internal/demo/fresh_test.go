package demo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The demo works only in directories it creates: a --dir that already holds its workspace, home or session (a tree someone
// else could have prepared, with configuration the demo would trust) is refused before anything runs.
func TestTheDemoRefusesADirectoryItDidNotMake(t *testing.T) {
	for _, sc := range []string{"", "shop"} {
		for _, sub := range []string{"handbook", "shop", "home", "session"} {
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, sub, ".sleipnir"), 0o755); err != nil {
				t.Fatal(err)
			}
			if sc == "" && sub == "shop" || sc == "shop" && sub == "handbook" {
				continue // not that scenario's directory
			}
			_, err := Run(context.Background(), Options{Scenario: sc, Topics: 2, Dir: dir})
			if err == nil || !strings.Contains(err.Error(), "exists already") {
				t.Errorf("%q with %s: %v", sc, sub, err)
			}
		}
	}
}
