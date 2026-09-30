package taskgen

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/rl"
	"github.com/reee344/sleipnir/internal/rl/env"
)

// compositeScriptPath is where the generated verifier script is written in the
// clean checkout (as a hidden file, so the agent never sees or edits it).
const compositeScriptPath = ".sleipnir/composite.sh"

type compositeConfig struct{ max int }

// CompositeOption tunes Composite.
type CompositeOption func(*compositeConfig)

// WithMax limits how many composite tasks are produced.
func WithMax(n int) CompositeOption { return func(c *compositeConfig) { c.max = n } }

// component is a task prepared for combination.
type component struct {
	t     rl.Task
	meta  Meta
	files map[string]bool
	key   uint64
}

// Composite combines k independent tasks of one repository into swarm tasks:
// one repository state with k problems in it, which a manager can only solve
// quickly by dispatching workers, and whose verifier passes only when every
// component verifier does (its score is the fraction that pass). This is the
// natural training ground for the manager's dispatch and the workers'
// coordination.
//
// Tasks are combinable when they start from the same repository and commit,
// have the same working directory and network setting, use the plain exit-code
// pass mode, and their changed files (Meta.files, recorded by FromGit and
// Mutate) are disjoint, so the k problems cannot interfere. Mutate produces
// exactly such families (many independent bugs injected into one commit);
// FromGit tasks share a starting commit only by coincidence. Setup commands are
// merged as the longest common prefix followed by each task's own remainder, so
// that k mutations are applied one after the other after a shared dependency
// download.
//
// Grouping is greedy over tasks ordered by a hash of (seed, id): deterministic,
// and independent of the input order. Tasks that fit no group are left out.
func Composite(tasks []rl.Task, k int, seed int64, opts ...CompositeOption) ([]rl.Task, error) {
	if k < 2 {
		return nil, fmt.Errorf("composite size must be at least 2, got %d", k)
	}
	cfg := compositeConfig{}
	for _, o := range opts {
		o(&cfg)
	}
	var comps []component
	for _, t := range tasks {
		c, ok := prepareComponent(t, seed)
		if ok {
			comps = append(comps, c)
		}
	}
	sort.Slice(comps, func(a, b int) bool {
		if comps[a].key != comps[b].key {
			return comps[a].key < comps[b].key
		}
		return comps[a].t.ID < comps[b].t.ID
	})
	used := make([]bool, len(comps))
	var out []rl.Task
	for i := range comps {
		if used[i] {
			continue
		}
		group := []int{i}
		union := copySet(comps[i].files)
		for j := i + 1; j < len(comps) && len(group) < k; j++ {
			if used[j] || !sameBase(comps[i].t, comps[j].t) || overlaps(union, comps[j].files) || hiddenConflict(comps, group, j) {
				continue
			}
			group = append(group, j)
			for f := range comps[j].files {
				union[f] = true
			}
		}
		if len(group) < k {
			continue
		}
		for _, g := range group {
			used[g] = true
		}
		sel := make([]component, len(group))
		for n, g := range group {
			sel[n] = comps[g]
		}
		ct, err := buildComposite(sel, seed)
		if err != nil {
			return nil, err
		}
		out = append(out, ct)
		if cfg.max > 0 && len(out) >= cfg.max {
			break
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	if err := env.ValidateTasks(out); err != nil {
		return nil, fmt.Errorf("internal error: generated invalid composite tasks: %w", err)
	}
	return out, nil
}

func prepareComponent(t rl.Task, seed int64) (component, bool) {
	if t.Kind == rl.TaskSwarm || t.Kind == rl.TaskRecall || env.HasExpect(t.Verifier) {
		return component{}, false
	}
	if t.Verifier.Pass != "" && t.Verifier.Pass != "exit0" {
		return component{}, false
	}
	if strings.TrimSpace(t.Verifier.Cmd) == "" {
		return component{}, false
	}
	var m Meta
	if len(t.Meta) == 0 || json.Unmarshal(t.Meta, &m) != nil || len(m.Files) == 0 {
		return component{}, false // without file metadata independence cannot be shown
	}
	files := map[string]bool{}
	for _, f := range m.Files {
		files[f] = true
	}
	h := sha256.Sum256([]byte(fmt.Sprintf("%d\x00composite\x00%s", seed, t.ID)))
	key := uint64(h[0])<<56 | uint64(h[1])<<48 | uint64(h[2])<<40 | uint64(h[3])<<32 | uint64(h[4])<<24 | uint64(h[5])<<16 | uint64(h[6])<<8 | uint64(h[7])
	return component{t: t, meta: m, files: files, key: key}, true
}

func sameBase(a, b rl.Task) bool {
	return env.RepoKey(a) == env.RepoKey(b) && a.Repo.Commit == b.Repo.Commit && a.Repo.Subdir == b.Repo.Subdir && a.Network == b.Network
}

func copySet(s map[string]bool) map[string]bool {
	out := make(map[string]bool, len(s))
	for k := range s {
		out[k] = true
	}
	return out
}

func overlaps(union, files map[string]bool) bool {
	for f := range files {
		if union[f] {
			return true
		}
	}
	return false
}

// hiddenConflict reports whether adding component j would put two different
// contents at one hidden path.
func hiddenConflict(comps []component, group []int, j int) bool {
	for _, g := range group {
		for p, ref := range comps[g].t.Verifier.Hidden {
			if other, ok := comps[j].t.Verifier.Hidden[p]; ok && other != ref {
				return true
			}
		}
	}
	return false
}

func buildComposite(sel []component, seed int64) (rl.Task, error) {
	sort.Slice(sel, func(a, b int) bool { return sel[a].t.ID < sel[b].t.ID })
	k := len(sel)
	ids := make([]string, k)
	for i, c := range sel {
		ids[i] = c.t.ID
	}
	sum := sha256.Sum256([]byte(strings.Join(ids, "\x00") + fmt.Sprint(seed)))
	first := sel[0].t

	// Setup: common prefix once, then each component's remainder in id order.
	prefix := sel[0].t.Setup
	for _, c := range sel[1:] {
		n := 0
		for n < len(prefix) && n < len(c.t.Setup) && prefix[n] == c.t.Setup[n] {
			n++
		}
		prefix = prefix[:n]
	}
	setup := append([]string(nil), prefix...)
	for _, c := range sel {
		setup = append(setup, c.t.Setup[len(prefix):]...)
	}

	// Verifier: one script that runs every component command.
	cmds := make([]string, k)
	hidden := map[string]string{}
	var protected []string
	timeout := 60
	for i, c := range sel {
		cmds[i] = c.t.Verifier.Cmd
		for p, ref := range c.t.Verifier.Hidden {
			hidden[p] = ref
		}
		protected = append(protected, c.t.Verifier.Protected...)
		if c.t.Verifier.TimeoutS > 0 {
			timeout += c.t.Verifier.TimeoutS
		} else {
			timeout += 600
		}
	}
	hidden[compositeScriptPath] = "text:" + compositeScript(cmds)
	protected = append(dedupe(protected), ".sleipnir/**")

	// Prompt.
	var pb strings.Builder
	fmt.Fprintf(&pb, "This repository has %d independent problems. Fix all of them: each has its own tests, and the work is done only when every one of them passes.\n", k)
	for i, c := range sel {
		fmt.Fprintf(&pb, "\nProblem %d:\n%s\n", i+1, strings.TrimSpace(c.t.Prompt))
	}

	// Budget: a swarm shares the total work, with headroom for coordination; wall
	// time lies between fully parallel and fully serial.
	var b rl.Budget
	var wallMax, wallSum int
	for _, c := range sel {
		b.Steps += c.t.Budget.Steps
		b.Requests += c.t.Budget.Requests
		b.ITE += c.t.Budget.ITE
		wallSum += c.t.Budget.WallS
		wallMax = max(wallMax, c.t.Budget.WallS)
		if w := c.t.Budget.ContextWindow; w > 0 && (b.ContextWindow == 0 || w < b.ContextWindow) {
			b.ContextWindow = w
		}
	}
	b.Steps = b.Steps * 5 / 4
	b.Requests = b.Requests * 5 / 4
	b.ITE *= 1.25
	if wallSum > 0 {
		b.WallS = wallMax + (wallSum-wallMax)/2
	}

	var files []string
	seenFile := map[string]bool{}
	tags := []string{"composite", "swarm", "k" + strconv.Itoa(k)}
	var latest time.Time
	var golds []string
	allGold := true
	for _, c := range sel {
		for _, f := range c.meta.Files {
			if !seenFile[f] {
				seenFile[f] = true
				files = append(files, f)
			}
		}
		tags = append(tags, c.t.Tags...)
		if d, err := time.Parse(time.RFC3339, c.meta.AuthorDate); err == nil && d.After(latest) {
			latest = d
		}
		if c.meta.GoldBlob == "" {
			allGold = false
		} else {
			golds = append(golds, c.meta.GoldBlob)
		}
	}
	sort.Strings(files)
	meta := Meta{Commit: first.Repo.Commit, Base: first.Repo.Commit, Generator: "taskgen/composite", Files: files, Components: ids}
	if !latest.IsZero() {
		meta.AuthorDate = latest.UTC().Format(time.RFC3339)
	}
	if allGold {
		meta.GoldBlobs = golds
	}
	metaRaw, _ := json.Marshal(meta)

	return rl.Task{
		ID:     fmt.Sprintf("composite-%d-%s", k, hex.EncodeToString(sum[:5])),
		Kind:   rl.TaskSwarm,
		Repo:   first.Repo,
		Setup:  setup,
		Prompt: strings.TrimSpace(pb.String()),
		Team:   rl.Team{Mode: "swarm", Agents: k},
		Verifier: rl.Verifier{
			Cmd: "sh " + compositeScriptPath, TimeoutS: timeout, Pass: "json-score", Hidden: hidden, Protected: protected,
		},
		Budget:  b,
		Tags:    dedupe(tags),
		Network: first.Network,
		Meta:    metaRaw,
	}, nil
}

// compositeScript is the generated verifier. Each component command runs in a
// subshell with its stdout sent to stderr and stdin closed, so nothing a
// component (or the agent's code inside it) prints can look like the final
// score line, which only this script writes.
func compositeScript(cmds []string) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n# Generated by sleipnir taskgen composite: runs every component verifier and\n# prints the fraction that passed as the last line of stdout.\npass=0\n")
	b.WriteString("run() { ( eval \"$1\" ) </dev/null >&2 && pass=$((pass+1)); }\n")
	for _, c := range cmds {
		fmt.Fprintf(&b, "run %s\n", shq(c))
	}
	total := len(cmds)
	b.WriteString("case $pass in\n")
	for i := 0; i <= total; i++ {
		fmt.Fprintf(&b, "%d) s=%s ;;\n", i, strconv.FormatFloat(float64(i)/float64(total), 'f', 6, 64))
	}
	b.WriteString("esac\nprintf '{\"score\": %s}\\n' \"$s\"\n")
	return b.String()
}

// ValidateComposite proves a composite task sound the same way FromGit does for
// a single one: its verifier fails on the starting state and passes with the
// components' reference solutions applied. The solutions are fetched from gold
// (the store the generators wrote Meta.gold_blob to) and are concatenated, which
// is valid because the components touch disjoint files.
func ValidateComposite(ctx context.Context, comp rl.Task, gold events.Blobs, opts env.VerifyOptions) (env.CheckReport, error) {
	var m Meta
	if err := json.Unmarshal(comp.Meta, &m); err != nil || len(m.GoldBlobs) == 0 {
		return env.CheckReport{}, errors.New("the composite has no recorded reference solutions (generate its components with a gold blob store)")
	}
	if gold == nil {
		return env.CheckReport{}, errors.New("a gold blob store is required")
	}
	var patch []byte
	for _, h := range m.GoldBlobs {
		b, err := gold.Get(core.Hash(h))
		if err != nil {
			return env.CheckReport{}, fmt.Errorf("reference solution %s: %w", core.Hash(h).Short(), err)
		}
		patch = append(patch, b...)
		if len(b) > 0 && b[len(b)-1] != '\n' {
			patch = append(patch, '\n')
		}
	}
	return env.CheckTask(ctx, comp, patch, opts)
}
