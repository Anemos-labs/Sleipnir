// Package recall generates memory tasks: an agent reads a small fact early, does
// a long stretch of unrelated reading, and must then state the fact exactly.
//
// They train the two skills the layered cache design depends on. A compactor must
// keep what the agent will need again (the reward's fidelity probes judge that
// from the log), and an agent must know where its own history went: re-read the
// file, or page the turn back in with the recall tool. The answer is checked by
// exact match on the final message, so nothing about the task needs a model to
// grade it, and the answer can be recovered either way; the cost components of the
// reward decide which way pays.
//
// Generation is deterministic and needs no model and no run: the fact is a
// distinctive constant that appears in exactly one file of the repository at the
// chosen commit, the filler is a list of other files, and the context window
// written into the budget is small enough that the reading overflows it.
package recall

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/rl"
	"github.com/reee344/sleipnir/internal/rl/env"
)

// Options configures Generate.
type Options struct {
	// Git is the hardened git runner (env.NewGit or Workspaces.Git()). Required.
	Git *env.Git
	// Rev is the commit to build tasks from (default HEAD).
	Rev string
	// Max is the number of tasks wanted (default 10).
	Max int
	// Files is how many other files the agent must read between learning the fact
	// and being asked for it (default 12, at least 3).
	Files int
	// Window is the context window written into each task's budget, in tokens.
	// It is deliberately small so the reading overflows it and the agent's early
	// history is compacted (default 12000).
	Window int
	// Seed selects which facts and fillers are used; the result is deterministic.
	Seed int64
	// Languages restricts to "go" and/or "python" (default both).
	Languages []string
	// IDPrefix prefixes task ids (default "recall").
	IDPrefix string
	// Tags are added to every task.
	Tags []string
}

// fact is a distinctive constant found in one file.
type fact struct {
	path, name, value string
	lang              string
}

// Meta is recorded in Task.Meta.
type Meta struct {
	Generator string   `json:"generator"`
	Commit    string   `json:"commit"`
	Landmark  string   `json:"landmark"`
	Name      string   `json:"name"`
	Fillers   []string `json:"fillers"`
	Lang      string   `json:"lang"`
}

const (
	maxFileBytes = 24 << 10
	minValueLen  = 3
)

var (
	goConst = regexp.MustCompile(`(?m)^[ \t]*(?:const[ \t]+|var[ \t]+)?([A-Z][A-Za-z0-9_]*)[ \t]*(?:[A-Za-z0-9_.\[\]*]+[ \t]*)?=[ \t]*("[^"\\\n]{3,}"|-?[0-9]{3,}|0x[0-9a-fA-F]{3,})[ \t]*(?://.*)?$`)
	pyConst = regexp.MustCompile(`(?m)^([A-Z][A-Z0-9_]*)[ \t]*=[ \t]*("[^"\\\n]{3,}"|'[^'\\\n]{3,}'|-?[0-9]{3,})[ \t]*(?:#.*)?$`)
)

// Generate builds recall tasks from the repository at repoPath.
func Generate(ctx context.Context, repoPath string, o Options) ([]rl.Task, error) {
	if o.Git == nil {
		return nil, errors.New("recall: Options.Git is required")
	}
	if o.Max <= 0 {
		o.Max = 10
	}
	if o.Files < 3 {
		o.Files = max(o.Files, 12)
	}
	if o.Window <= 0 {
		o.Window = 12000
	}
	if o.Rev == "" {
		o.Rev = "HEAD"
	}
	if o.IDPrefix == "" {
		o.IDPrefix = "recall"
	}
	gitDir, ok := o.Git.GitDirOf(ctx, repoPath)
	if !ok {
		return nil, fmt.Errorf("recall: %s is not the root of a git repository", repoPath)
	}
	commit, err := o.Git.ResolveCommit(ctx, gitDir, o.Rev)
	if err != nil {
		return nil, err
	}
	names, err := o.Git.Run(ctx, "", nil, "--git-dir="+gitDir, "ls-tree", "-r", "-z", "--name-only", "--end-of-options", commit)
	if err != nil {
		return nil, err
	}
	langs := map[string]bool{"go": true, "python": true}
	if len(o.Languages) > 0 {
		langs = map[string]bool{}
		for _, l := range o.Languages {
			langs[strings.ToLower(strings.TrimSpace(l))] = true
		}
	}

	type file struct {
		path, lang, text string
	}
	var files []file
	for _, p := range strings.Split(string(names), "\x00") {
		lang := langOf(p)
		if lang == "" || !langs[lang] || skipPath(p) {
			continue
		}
		body, err := o.Git.Run(ctx, "", nil, "--git-dir="+gitDir, "show", "--end-of-options", commit+":"+p)
		if err != nil || len(body) > maxFileBytes || !validText(body) {
			continue
		}
		files = append(files, file{p, lang, string(body)})
	}
	if len(files) < o.Files+1 {
		return nil, fmt.Errorf("recall: only %d suitable source files at %s; need more than --files=%d", len(files), o.Rev, o.Files)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })

	// Facts: constants whose literal appears in exactly one file, once.
	var all strings.Builder
	for _, f := range files {
		all.WriteString(f.text)
		all.WriteByte('\n')
	}
	corpus := all.String()
	var facts []fact
	for _, f := range files {
		re := goConst
		if f.lang == "python" {
			re = pyConst
		}
		for _, m := range re.FindAllStringSubmatch(f.text, -1) {
			val := unquote(m[2])
			if len(val) < minValueLen || strings.Count(corpus, val) != 1 {
				continue
			}
			facts = append(facts, fact{path: f.path, name: m[1], value: val, lang: f.lang})
		}
	}
	if len(facts) == 0 {
		return nil, errors.New("recall: no distinctive constants found to ask about")
	}
	sort.Slice(facts, func(i, j int) bool {
		return keyed(o.Seed, "fact", facts[i].path, facts[i].name) < keyed(o.Seed, "fact", facts[j].path, facts[j].name)
	})

	var out []rl.Task
	used := map[string]bool{}
	for _, ft := range facts {
		if len(out) >= o.Max {
			break
		}
		if used[ft.path] { // one task per landmark file: they would share a fact otherwise
			continue
		}
		used[ft.path] = true
		var pool []file
		for _, f := range files {
			if f.path != ft.path && f.lang == ft.lang {
				pool = append(pool, f)
			}
		}
		if len(pool) < o.Files {
			continue
		}
		sort.Slice(pool, func(i, j int) bool {
			return keyed(o.Seed, "fill", ft.path, pool[i].path) < keyed(o.Seed, "fill", ft.path, pool[j].path)
		})
		pool = pool[:o.Files]
		sort.Slice(pool, func(i, j int) bool { return pool[i].path < pool[j].path })
		fillers := make([]string, len(pool))
		for i, f := range pool {
			fillers[i] = f.path
		}
		out = append(out, task(ft, fillers, commit, repoPath, len(out)+1, o))
	}
	if len(out) == 0 {
		return nil, errors.New("recall: no fact had enough other files of its language to read")
	}
	if err := env.ValidateTasks(out); err != nil {
		return nil, fmt.Errorf("recall: generated an invalid task: %w", err)
	}
	return out, nil
}

func task(f fact, fillers []string, commit, repoPath string, n int, o Options) rl.Task {
	unit := "package"
	if f.lang == "python" {
		unit = "module docstring or, failing that, its first function"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "This is a memory exercise on the repository. Follow the steps in order and do not modify any file.\n\n")
	fmt.Fprintf(&b, "1. Read `%s` and note the value of `%s` exactly as it is written in the source.\n", f.path, f.name)
	fmt.Fprintf(&b, "2. Then open each of these files, one at a time, and after each one write a single line naming the file and its %s:\n", unit)
	for _, p := range fillers {
		fmt.Fprintf(&b, "   - %s\n", p)
	}
	fmt.Fprintf(&b, "3. When you have covered every file, finish with a line of its own: `ANSWER: <the value of %s from %s>`. Give the value only, without quotes.\n", f.name, f.path)
	expect, _ := json.Marshal(map[string]any{"contains": []string{"ANSWER: " + f.value}})
	meta, _ := json.Marshal(Meta{Generator: "recall/repo", Commit: commit, Landmark: f.path, Name: f.name, Fillers: fillers, Lang: f.lang})
	tags := append([]string{"recall", f.lang}, o.Tags...)
	return rl.Task{
		ID:       fmt.Sprintf("%s-%03d-%s", o.IDPrefix, n, shortHash(f.path+f.name+commit)),
		Kind:     rl.TaskRecall,
		Repo:     rl.RepoSpec{Path: repoPath, Commit: commit},
		Prompt:   b.String(),
		Team:     rl.Team{Mode: "single"},
		Verifier: rl.Verifier{Expect: expect},
		Budget:   rl.Budget{Steps: 3*len(fillers) + 12, Requests: 6*len(fillers) + 30, WallS: 900, ContextWindow: o.Window},
		Tags:     tags,
		Meta:     meta,
	}
}

func langOf(p string) string {
	switch path.Ext(p) {
	case ".go":
		return "go"
	case ".py":
		return "python"
	}
	return ""
}

// skipPath leaves out tests, vendored and generated code: their constants are
// noise, and reading them teaches nothing about the project.
func skipPath(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		switch seg {
		case "vendor", "node_modules", "testdata", "third_party", "generated", ".git":
			return true
		}
	}
	base := path.Base(p)
	return strings.HasSuffix(base, "_test.go") || strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py") ||
		strings.HasSuffix(base, ".pb.go") || strings.Contains(base, "generated") || base == "__init__.py" || base == "conftest.py"
}

func validText(b []byte) bool {
	return !bytes.Contains(b, []byte{0}) && utf8.Valid(b)
}

func unquote(lit string) string {
	if len(lit) >= 2 && (lit[0] == '"' || lit[0] == '\'') {
		return lit[1 : len(lit)-1]
	}
	return lit // numbers are asked as written, hexadecimal included
}

func keyed(seed int64, parts ...string) string {
	h := core.HashString(strconv.FormatInt(seed, 10) + "\x00" + strings.Join(parts, "\x00"))
	return string(h)
}

func shortHash(s string) string { return core.HashString(s).Short() }
