package fs

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/tools"
)

// TestStressMixedToolsFromManyAgents hammers a small tree with every tool at
// once. Writers append unique tokens through edit (with a read first, retrying
// on staleness) and through apply_patch (which needs no read); readers run
// read/glob/grep/ls the whole time. Nothing may be lost, torn or left behind,
// and -race must stay quiet.
func TestStressMixedToolsFromManyAgents(t *testing.T) {
	if testing.Short() {
		t.Skip("large input; skipped in -short mode")
	}
	base := testEnv(t)
	const files = 4
	for i := 0; i < files; i++ {
		writeFile(t, filepath.Join(base.Cwd, fmt.Sprintf("d/f%d.txt", i)), "HEAD\n")
	}
	deadline := time.Now().Add(3 * time.Second)
	var wg sync.WaitGroup
	var mu sync.Mutex
	expected := make([][]string, files)
	var ops, rejected int64

	record := func(file int, token string) {
		mu.Lock()
		expected[file] = append(expected[file], token)
		mu.Unlock()
	}
	do := func(tool tools.Tool, env *tools.Env, in map[string]any) *tools.Result {
		b, _ := json.Marshal(in)
		res, err := tool.Run(context.Background(), &tools.Call{Input: b, Env: env})
		if err != nil || res == nil {
			t.Errorf("%s: %v", tool.Spec().Name, err)
			return &tools.Result{IsError: true}
		}
		atomic.AddInt64(&ops, 1)
		return res
	}

	for w := 0; w < 6; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(w)))
			env := agentEnv(base, fmt.Sprintf("writer%d", w))
			for n := 0; time.Now().Before(deadline); n++ {
				file := rng.Intn(files)
				path := fmt.Sprintf("d/f%d.txt", file)
				token := fmt.Sprintf("w%d-n%d", w, n)
				if rng.Intn(2) == 0 {
					// edit: read, then insert after HEAD
					do(Read{}, env, map[string]any{"path": path})
					res := do(Edit{}, env, map[string]any{"path": path, "old_string": "HEAD\n", "new_string": "HEAD\n" + token + "\n"})
					if res.IsError {
						if !strings.Contains(res.Text, "changed since you last read it") {
							t.Errorf("edit failed unexpectedly: %s", res.Text)
						}
						atomic.AddInt64(&rejected, 1)
						continue
					}
				} else {
					// patch with a fresh agent id: no prior read to go stale
					penv := agentEnv(base, fmt.Sprintf("patcher%d-%d", w, n))
					res := do(ApplyPatch{}, penv, map[string]any{"patch": patchText("*** Update File: "+path, "@@", " HEAD", "+"+token)})
					if res.IsError {
						t.Errorf("patch failed: %s", res.Text)
						continue
					}
				}
				record(file, token)
			}
		}(w)
	}
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func(r int) {
			defer wg.Done()
			env := agentEnv(base, fmt.Sprintf("reader%d", r))
			for n := 0; time.Now().Before(deadline); n++ {
				switch n % 5 {
				case 0:
					res := do(Read{}, env, map[string]any{"path": fmt.Sprintf("d/f%d.txt", n%files)})
					if !res.IsError && !strings.Contains(res.Text, "HEAD") {
						t.Errorf("read saw a torn file: %q", res.Text)
					}
				case 1:
					do(Glob{}, env, map[string]any{"pattern": "**/*.txt"})
				case 2:
					do(Grep{}, env, map[string]any{"pattern": "HEAD", "output_mode": "count"})
				case 3:
					do(LS{}, env, map[string]any{})
				default:
					do(Grep{DisableRipgrep: true}, env, map[string]any{"pattern": "w[0-9]+-n", "-C": 1})
				}
			}
		}(r)
	}
	wg.Wait()

	for i := 0; i < files; i++ {
		got := readFileT(t, filepath.Join(base.Cwd, fmt.Sprintf("d/f%d.txt", i)))
		lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
		if lines[0] != "HEAD" {
			t.Errorf("f%d: first line = %q", i, lines[0])
		}
		if len(lines)-1 != len(expected[i]) {
			t.Errorf("f%d: %d tokens on disk, %d successful writes", i, len(lines)-1, len(expected[i]))
		}
		seen := map[string]int{}
		for _, l := range lines[1:] {
			seen[l]++
		}
		for _, tok := range expected[i] {
			if seen[tok] != 1 {
				t.Errorf("f%d: token %s appears %d times", i, tok, seen[tok])
			}
		}
	}
	entries, _ := os.ReadDir(filepath.Join(base.Cwd, "d"))
	if len(entries) != files {
		t.Errorf("leftover files: %v", entries)
	}
	t.Logf("%d tool calls, %d edits rejected as stale", atomic.LoadInt64(&ops), atomic.LoadInt64(&rejected))
}
