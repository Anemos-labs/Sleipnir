package env

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// Two processes that share a work directory (a benchmark running several models at once) may build the same snapshot at
// the same time. A setup command is tracked by a marker in its environment so that whatever it leaves behind can be swept
// once it is done; when that marker was the same in both processes, the one that finished first killed the other's setup
// (exit status 137 with no output), and its rollouts failed as infrastructure errors.
func TestTwoManagersBuildingTheSameSnapshotDoNotKillEachOthersSetup(t *testing.T) {
	r, base := mathxRepo(t)
	root := filepath.Join(t.TempDir(), "root")
	state := t.TempDir()
	task := mathxTask(r, base)
	// Both setups start before either goes on (a barrier, not a delay); the first to pass it finishes at once, the other
	// keeps running for a while, as a slow `go mod download` would, while the first one's manager cleans up after its setup.
	task.Setup = []string{fmt.Sprintf(`s=%[1]s; touch "$s/started.$$"; while [ "$(ls "$s" | grep -c '^started')" -lt 2 ]; do sleep 0.02; done; `+
		`if mkdir "$s/first" 2>/dev/null; then exit 0; fi; sleep 2; touch "$s/second-survived"`, state)}

	managers := []*Workspaces{
		newManager(t, func(o *WorkspaceOptions) { o.Root = root }),
		newManager(t, func(o *WorkspaceOptions) { o.Root = root }),
	}
	errs := make([]error, len(managers))
	var wg sync.WaitGroup
	for i, m := range managers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w, err := m.Prepare(ctxT(t), task, fmt.Sprintf("s%d", i))
			if err == nil {
				defer func() { _ = w.Cleanup() }()
			}
			errs[i] = err
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("manager %d: %v", i, err)
		}
	}
	if _, err := os.Stat(filepath.Join(state, "second-survived")); err != nil {
		t.Errorf("the slower setup was killed before it finished: %v", err)
	}
}
