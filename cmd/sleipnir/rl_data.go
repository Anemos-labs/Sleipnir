package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/rl"
	"github.com/anemos-labs/sleipnir/internal/rl/reward"
	"github.com/anemos-labs/sleipnir/internal/rl/traj"
)

// A rollout run directory holds <task>/<sample>/{episode.json, events.jsonl,
// blobs/, task.json, ...} under a manifest.json and summary.json
// (internal/rl/env). These helpers read one back.

// sample is one recorded rollout on disk.
type sample struct {
	Dir     string
	Episode *rl.Episode
}

// findSamples resolves each path to the rollouts under it: a sample directory
// (it holds episode.json), a run directory (its <task>/<sample> children), or a
// directory of run directories. The result is sorted by path, so exports are
// deterministic whatever order the filesystem lists things in.
func findSamples(paths []string) ([]sample, error) {
	seen := map[string]bool{}
	var dirs []string
	add := func(d string) {
		if !seen[d] {
			seen[d] = true
			dirs = append(dirs, d)
		}
	}
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if !st.IsDir() {
			return nil, fmt.Errorf("%s is not a directory: pass a run directory (the --out of rl rollout) or a sample directory", p)
		}
		if fileExists(filepath.Join(p, "episode.json")) {
			add(p)
			continue
		}
		// <run>/<task>/<sample> and <runs>/<run>/<task>/<sample>
		err = filepath.WalkDir(p, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				return nil
			}
			if d.Name() == "blobs" || d.Name() == "checkpoints" {
				return filepath.SkipDir
			}
			if fileExists(filepath.Join(path, "episode.json")) {
				add(path)
				return filepath.SkipDir
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(dirs)
	out := make([]sample, 0, len(dirs))
	for _, d := range dirs {
		ep, err := readEpisode(d)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", d, err)
		}
		out = append(out, sample{Dir: d, Episode: ep})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no rollouts found under %v (looking for <task>/<sample>/episode.json)", paths)
	}
	return out, nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func readEpisode(dir string) (*rl.Episode, error) {
	b, err := os.ReadFile(filepath.Join(dir, "episode.json"))
	if err != nil {
		return nil, err
	}
	var ep rl.Episode
	if err := json.Unmarshal(b, &ep); err != nil {
		return nil, fmt.Errorf("episode.json: %w", err)
	}
	return &ep, nil
}

func readTask(dir string) (*rl.Task, error) {
	b, err := os.ReadFile(filepath.Join(dir, "task.json"))
	if err != nil {
		return nil, err
	}
	var t rl.Task
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, fmt.Errorf("task.json: %w", err)
	}
	return &t, nil
}

// writeJSONFile replaces path atomically.
func writeJSONFile(path string, v any) error {
	b, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, path)
}

// runCache opens rollout logs on demand and keeps a few, so exporting thousands
// of rollouts does not hold all their events in memory: the exporter walks
// episodes one after another, and each episode's steps are resolved together.
type runCache struct {
	mu    sync.Mutex
	max   int
	runs  map[string]*traj.Run
	order []string
}

func newRunCache(max int) *runCache {
	return &runCache{max: max, runs: map[string]*traj.Run{}}
}

func (c *runCache) get(dir string) (*traj.Run, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if r, ok := c.runs[dir]; ok {
		return r, nil
	}
	r, err := traj.Open(dir)
	if err != nil {
		return nil, err
	}
	c.runs[dir] = r
	c.order = append(c.order, dir)
	for len(c.order) > c.max {
		delete(c.runs, c.order[0])
		c.order = c.order[1:]
	}
	return r, nil
}

// prompts resolves the exact prompts of one rollout's steps (export.PromptResolver).
type prompts struct {
	dir   string
	cache *runCache
}

func (p prompts) Prompt(ep *rl.Episode, st *rl.Step) (*core.Prompt, error) {
	run, err := p.cache.get(p.dir)
	if err != nil {
		return nil, err
	}
	return traj.Resolver{Run: run}.Prompt(ep, st)
}

// promptText adapts a rollout's log to the reward package's fidelity probes.
func promptText(cache *runCache, dir string) reward.PromptText {
	return func(ep *rl.Episode, st *rl.Step) (string, error) {
		p, err := prompts{dir: dir, cache: cache}.Prompt(ep, st)
		if err != nil {
			return "", err
		}
		s := *st
		s.Inline = p
		return reward.InlinePromptText(ep, &s)
	}
}

// blobsOf opens a rollout's blob store.
func blobsOf(dir string) (events.Blobs, error) {
	return events.NewDirBlobs(filepath.Join(dir, "blobs"))
}
