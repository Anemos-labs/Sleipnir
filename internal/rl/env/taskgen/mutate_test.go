package taskgen

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/rl"
	"github.com/reee344/sleipnir/internal/rl/env"
)

const geomMod = "module example.com/geom\n\ngo 1.24\n"

const shapeSrc = `package shape

import "errors"

// Rect is an axis-aligned rectangle.
type Rect struct{ W, H int }

// Area returns the area, or an error for a negative side.
func (r Rect) Area() (int, error) {
	if r.W < 0 || r.H < 0 {
		return 0, errors.New("negative side")
	}
	return r.W * r.H, nil
}

// IsSquare reports whether both sides are equal.
func (r Rect) IsSquare() bool { return r.W == r.H }

// Wider reports whether r is wider than o.
func Wider(r, o Rect) bool {
	if r.W > o.W {
		return true
	}
	return false
}

// Grow returns r with both sides one larger.
func Grow(r Rect) Rect { return Rect{W: r.W + 1, H: r.H + 1} }
`

const shapeTest = `package shape

import "testing"

func TestArea(t *testing.T) {
	if a, err := (Rect{3, 4}).Area(); err != nil || a != 12 {
		t.Fatalf("Area = %d, %v", a, err)
	}
	if _, err := (Rect{-1, 4}).Area(); err == nil {
		t.Fatal("negative side must fail")
	}
	if _, err := (Rect{4, -1}).Area(); err == nil {
		t.Fatal("negative side must fail")
	}
	if a, _ := (Rect{0, 5}).Area(); a != 0 {
		t.Fatal("zero side")
	}
}

func TestIsSquare(t *testing.T) {
	if !(Rect{2, 2}).IsSquare() || (Rect{2, 3}).IsSquare() {
		t.Fatal("IsSquare")
	}
}

func TestWider(t *testing.T) {
	if !Wider(Rect{5, 1}, Rect{3, 1}) || Wider(Rect{3, 1}, Rect{5, 1}) || Wider(Rect{3, 1}, Rect{3, 1}) {
		t.Fatal("Wider")
	}
}

func TestGrow(t *testing.T) {
	if g := Grow(Rect{2, 3}); g.W != 3 || g.H != 4 {
		t.Fatalf("Grow = %+v", g)
	}
}
`

const utilSrc = `package util

// Clamp limits v to [lo, hi].
func Clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Between reports whether lo <= v <= hi.
func Between(v, lo, hi int) bool { return v >= lo && v <= hi }

// Sign returns -1, 0 or 1.
func Sign(v int) int {
	switch {
	case v < 0:
		return -1
	case v > 0:
		return 1
	}
	return 0
}

// Even reports whether v is even.
func Even(v int) bool {
	if v%2 == 0 {
		return true
	}
	return false
}
`

const utilTest = `package util

import "testing"

func TestClamp(t *testing.T) {
	for _, c := range []struct{ v, lo, hi, want int }{{5, 0, 10, 5}, {-3, 0, 10, 0}, {12, 0, 10, 10}, {0, 0, 10, 0}, {10, 0, 10, 10}} {
		if got := Clamp(c.v, c.lo, c.hi); got != c.want {
			t.Errorf("Clamp(%d,%d,%d) = %d, want %d", c.v, c.lo, c.hi, got, c.want)
		}
	}
}

func TestBetween(t *testing.T) {
	if !Between(5, 0, 10) || !Between(0, 0, 10) || !Between(10, 0, 10) || Between(-1, 0, 10) || Between(11, 0, 10) {
		t.Fatal("Between")
	}
}

func TestSign(t *testing.T) {
	if Sign(-4) != -1 || Sign(0) != 0 || Sign(9) != 1 {
		t.Fatal("Sign")
	}
}

func TestEven(t *testing.T) {
	if !Even(4) || Even(3) || !Even(0) {
		t.Fatal("Even")
	}
}
`

func buildGeomRepo(t *testing.T) (*fixture, string) {
	t.Helper()
	f := newFixture(t)
	f.write("go.mod", geomMod)
	f.write("shape/shape.go", shapeSrc)
	f.write("shape/shape_test.go", shapeTest)
	f.write("util/util.go", utilSrc)
	f.write("util/util_test.go", utilTest)
	f.write("main.go", "package main\n\nfunc main() {}\n") // no tests next to it: never mutated
	return f, f.commit("geometry")
}

func idsOf(tasks []rl.Task) []string {
	out := make([]string, len(tasks))
	for i, t := range tasks {
		out[i] = t.ID
	}
	return out
}

func TestMutateGo(t *testing.T) {
	f, commit := buildGeomRepo(t)
	m := newManager(t)
	gold := events.NewMemBlobs()
	opts := MutateOptions{Workspaces: m, Max: 3, Seed: 5, Concurrency: 3, GoldBlobs: gold, Timeout: time.Minute}
	tasks, rep, err := Mutate(context.Background(), f.Dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 3 || rep.Accepted != 3 || len(rep.InfraErrors) != 0 {
		t.Fatalf("%d tasks, report %+v", len(tasks), rep)
	}
	if rep.Candidates < 20 || rep.Examined < 3 {
		t.Errorf("candidates %d examined %d", rep.Candidates, rep.Examined)
	}
	if err := env.ValidateTasks(tasks); err != nil {
		t.Fatal(err)
	}
	for _, tk := range tasks {
		var meta Meta
		if err := json.Unmarshal(tk.Meta, &meta); err != nil {
			t.Fatal(err)
		}
		if tk.Kind != rl.TaskFix || tk.Repo.Commit != commit || tk.Repo.Path != f.Dir || meta.Generator != "taskgen/mutate" ||
			meta.Mutation == nil || meta.GoldBlob == "" || meta.Base != commit || meta.AuthorDate != "2024-01-01T12:00:00Z" || len(meta.Files) != 1 {
			t.Fatalf("task %s: %+v %+v", tk.ID, tk, meta)
		}
		if !strings.HasPrefix(tk.ID, "mut-") || !contains(tk.Tags, "mutation") || !contains(tk.Tags, meta.Mutation.Op) {
			t.Errorf("id/tags: %s %v", tk.ID, tk.Tags)
		}
		if len(tk.Setup) != 2 || tk.Setup[0] != "go mod download" || !strings.HasPrefix(tk.Setup[1], "git apply --whitespace=nowarn <<'SLEIPNIR_PATCH_") {
			t.Errorf("setup: %v", tk.Setup)
		}
		if !strings.HasPrefix(tk.Verifier.Cmd, "go test -count=1 ./") || tk.Verifier.Pass != "exit0" || len(tk.Verifier.Hidden) != 0 {
			t.Errorf("verifier: %+v", tk.Verifier)
		}
		if !contains(tk.Verifier.Protected, "*_test.go") || !contains(tk.Verifier.Protected, "go.mod") {
			t.Errorf("protected: %v", tk.Verifier.Protected)
		}
		if !strings.Contains(tk.Prompt, "Test") || !strings.Contains(tk.Prompt, "fail") || strings.Contains(tk.Prompt, meta.Mutation.File) {
			t.Errorf("prompt should name failing tests but not the file:\n%s", tk.Prompt)
		}
		// The file the mutation touches has tests next to it; main.go was never picked.
		if meta.Files[0] == "main.go" {
			t.Errorf("mutated a file without tests: %s", meta.Files[0])
		}
	}

	// Deterministic for a seed; a different seed picks different mutations.
	again, _, err := Mutate(context.Background(), f.Dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(idsOf(tasks), idsOf(again)) || !reflect.DeepEqual(tasks[0].Setup, again[0].Setup) {
		t.Fatalf("Mutate is not deterministic:\n%v\n%v", idsOf(tasks), idsOf(again))
	}
	opts.Seed = 6
	other, _, err := Mutate(context.Background(), f.Dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(idsOf(tasks), idsOf(other)) {
		t.Error("the seed has no effect")
	}

	// A mutation task really starts from buggy code the tests catch, and the
	// stored reverse patch really restores it.
	tk := tasks[0]
	var meta Meta
	json.Unmarshal(tk.Meta, &meta)
	ctx := context.Background()
	w, err := m.Prepare(ctx, tk, "s0")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Cleanup()
	orig := mustRead(t, filepath.Join(f.Dir, meta.Files[0]))
	got := mustRead(t, filepath.Join(w.Root, meta.Files[0]))
	if got == orig {
		t.Fatal("the workspace does not contain the injected bug")
	}
	if !strings.Contains(got, meta.Mutation.New) && meta.Mutation.New != "" {
		t.Errorf("mutation text %q not found in the workspace file", meta.Mutation.New)
	}
	// The mutation is part of the baseline, not of the agent's diff.
	if d, err := w.Diff(ctx, 0); err != nil || len(d.Patch) != 0 {
		t.Fatalf("mutation shows up in the agent's diff: %v %s", err, d.Patch)
	}
	res, err := env.Verify(ctx, tk, w, env.VerifyOptions{})
	if err != nil || res.Pass {
		t.Fatalf("the injected bug must fail the tests: %v %v", err, res.Pass)
	}
	// An agent that restores the original file passes.
	if err := writeFile(filepath.Join(w.Root, meta.Files[0]), orig); err != nil {
		t.Fatal(err)
	}
	res, err = env.Verify(ctx, tk, w, env.VerifyOptions{})
	if err != nil || !res.Pass {
		t.Fatalf("the fix must pass: %v\n%s", err, res.Log)
	}
	// The recorded gold blob is the reverse patch.
	g, err := gold.Get(core.Hash(meta.GoldBlob))
	if err != nil || !strings.Contains(string(g), meta.Files[0]) {
		t.Fatalf("gold blob: %v", err)
	}
	if res, err := env.VerifyPatch(ctx, tk, g, env.VerifyOptions{Workspaces: m}); err != nil || !res.Pass {
		t.Fatalf("gold patch: %v", err)
	}
}

func TestMutateFilesAndErrors(t *testing.T) {
	f, _ := buildGeomRepo(t)
	m := newManager(t)
	tasks, _, err := Mutate(context.Background(), f.Dir, MutateOptions{Workspaces: m, Max: 2, Seed: 1, Files: []string{"util/*.go"}, Timeout: time.Minute})
	if err != nil || len(tasks) != 2 {
		t.Fatalf("%v %d", err, len(tasks))
	}
	for _, tk := range tasks {
		var meta Meta
		json.Unmarshal(tk.Meta, &meta)
		if meta.Files[0] != "util/util.go" || !strings.HasSuffix(tk.Verifier.Cmd, "./util") {
			t.Errorf("file filter ignored: %v %s", meta.Files, tk.Verifier.Cmd)
		}
	}
	// A language filter that leaves nothing is not an error.
	tasks, rep, err := Mutate(context.Background(), f.Dir, MutateOptions{Workspaces: m, Languages: []string{"python"}})
	if err != nil || len(tasks) != 0 || rep.Candidates != 0 {
		t.Fatalf("%v %d %+v", err, len(tasks), rep)
	}
	if _, _, err := Mutate(context.Background(), f.Dir, MutateOptions{}); err == nil {
		t.Error("missing workspaces accepted")
	}
	if _, _, err := Mutate(context.Background(), t.TempDir(), MutateOptions{Workspaces: m}); err == nil {
		t.Error("non-repository accepted")
	}
	if _, _, err := Mutate(context.Background(), f.Dir, MutateOptions{Workspaces: m, Rev: "nope"}); err == nil {
		t.Error("bad rev accepted")
	}
	// MaxAttempts bounds the work.
	_, rep, err = Mutate(context.Background(), f.Dir, MutateOptions{Workspaces: m, Max: 50, MaxAttempts: 3, Seed: 2, Concurrency: 3, Timeout: time.Minute})
	if err != nil || rep.Examined != 3 {
		t.Fatalf("examined %d, %v", rep.Examined, err)
	}
	// Cancellation returns promptly.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := Mutate(ctx, f.Dir, MutateOptions{Workspaces: m}); err == nil {
		t.Error("cancelled context ignored")
	}
}

func TestMutatePython(t *testing.T) {
	havePytest(t)
	f := newFixture(t)
	f.write("mathx.py", "def clamp(v, lo, hi):\n    if v < lo:\n        return lo\n    if v > hi:\n        return hi\n    return v\n\n\ndef is_positive(v):\n    return v > 0\n")
	f.write("test_mathx.py", "from mathx import clamp, is_positive\n\n\ndef test_clamp():\n    assert clamp(5, 0, 10) == 5\n    assert clamp(-1, 0, 10) == 0\n    assert clamp(11, 0, 10) == 10\n    assert clamp(0, 0, 10) == 0\n    assert clamp(10, 0, 10) == 10\n\n\ndef test_positive():\n    assert is_positive(1)\n    assert not is_positive(0)\n    assert not is_positive(-1)\n")
	base := f.commit("init")
	m := newManager(t)
	tasks, rep, err := Mutate(context.Background(), f.Dir, MutateOptions{Workspaces: m, Max: 2, Seed: 3, Concurrency: 2, Timeout: time.Minute})
	if err != nil || len(tasks) != 2 {
		t.Fatalf("%v %d %+v", err, len(tasks), rep)
	}
	for _, tk := range tasks {
		if tk.Repo.Commit != base || !strings.Contains(tk.Verifier.Cmd, "-m pytest") || !contains(tk.Verifier.Protected, "test_*.py") || len(tk.Setup) != 1 {
			t.Errorf("%+v", tk)
		}
	}
}

func TestCompositeFromMutations(t *testing.T) {
	f, commit := buildGeomRepo(t)
	m := newManager(t)
	gold := events.NewMemBlobs()
	ctx := context.Background()
	tasks, _, err := Mutate(ctx, f.Dir, MutateOptions{Workspaces: m, Max: 6, Seed: 9, Concurrency: 3, GoldBlobs: gold, Timeout: time.Minute})
	if err != nil || len(tasks) < 4 {
		t.Fatalf("%v %d tasks", err, len(tasks))
	}
	comps, err := Composite(tasks, 2, 4, WithMax(2))
	if err != nil {
		t.Fatal(err)
	}
	if len(comps) == 0 {
		t.Fatalf("no composites from %d mutation tasks (files: %v)", len(tasks), func() []string {
			var fs []string
			for _, tk := range tasks {
				var mm Meta
				json.Unmarshal(tk.Meta, &mm)
				fs = append(fs, mm.Files...)
			}
			return fs
		}())
	}
	c := comps[0]
	if err := env.ValidateTasks(comps); err != nil {
		t.Fatal(err)
	}
	var cm Meta
	json.Unmarshal(c.Meta, &cm)
	if c.Kind != rl.TaskSwarm || c.Team.Mode != "swarm" || c.Team.Agents != 2 || len(cm.Components) != 2 || len(cm.Files) != 2 || len(cm.GoldBlobs) != 2 ||
		cm.Commit != commit || cm.Generator != "taskgen/composite" || cm.AuthorDate != "2024-01-01T12:00:00Z" {
		t.Fatalf("composite: %+v\nmeta: %+v", c, cm)
	}
	if cm.Files[0] == cm.Files[1] {
		t.Fatalf("components touch the same file: %v", cm.Files)
	}
	if c.Verifier.Pass != "json-score" || c.Verifier.Cmd != "sh .sleipnir/composite.sh" || c.Verifier.Hidden[".sleipnir/composite.sh"] == "" || !contains(c.Verifier.Protected, ".sleipnir/**") {
		t.Errorf("verifier: %+v", c.Verifier)
	}
	if !strings.Contains(c.Prompt, "2 independent problems") || !strings.Contains(c.Prompt, "Problem 1:") || !strings.Contains(c.Prompt, "Problem 2:") {
		t.Errorf("prompt:\n%s", c.Prompt)
	}
	// setup: one dependency download, then both mutations.
	if len(c.Setup) != 3 || c.Setup[0] != "go mod download" || !strings.Contains(c.Setup[1], "git apply") || !strings.Contains(c.Setup[2], "git apply") {
		t.Errorf("setup: %v", c.Setup)
	}
	// Budget is scaled from the components' (steps sum with headroom).
	var stepsSum int
	for _, id := range cm.Components {
		for _, tk := range tasks {
			if tk.ID == id {
				stepsSum += tk.Budget.Steps
			}
		}
	}
	if c.Budget.Steps != stepsSum*5/4 || c.Budget.Requests <= c.Budget.Steps {
		t.Errorf("budget: %+v (components' steps %d)", c.Budget, stepsSum)
	}

	// The composite is proven sound the same way as any task.
	rep, err := ValidateComposite(ctx, c, gold, env.VerifyOptions{Workspaces: m})
	if err != nil {
		t.Fatalf("%v\nbaseline: %s\ngold: %s", err, rep.Baseline.Log, rep.Gold.Log)
	}

	// Run it as an agent would: fix one bug, then both. The score is the fraction.
	w, err := m.Prepare(ctx, c, "s0")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Cleanup()
	res, err := env.Verify(ctx, c, w, env.VerifyOptions{})
	if err != nil || res.Pass || res.Score != 0 {
		t.Fatalf("both bugs present: %v pass=%v score=%v\n%s", err, res.Pass, res.Score, res.Log)
	}
	restore := func(file string) {
		if err := writeFile(filepath.Join(w.Root, file), f.git("show", commit+":"+file)); err != nil {
			t.Fatal(err)
		}
	}
	restore(cm.Files[0])
	res, err = env.Verify(ctx, c, w, env.VerifyOptions{})
	if err != nil || res.Pass || res.Score != 0.5 {
		t.Fatalf("one of two fixed: %v pass=%v score=%v\n%s", err, res.Pass, res.Score, res.Log)
	}
	restore(cm.Files[1])
	res, err = env.Verify(ctx, c, w, env.VerifyOptions{})
	if err != nil || !res.Pass || res.Score != 1 {
		t.Fatalf("both fixed: %v pass=%v score=%v\n%s", err, res.Pass, res.Score, res.Log)
	}
	// The generated script is protected: an agent that overwrites it changes nothing.
	if err := writeFile(filepath.Join(w.Root, ".sleipnir", "composite.sh"), "#!/bin/sh\necho '{\"score\": 1}'\n"); err != nil {
		t.Fatal(err)
	}
	restoreBroken := func() {
		// re-break one component
		if err := writeFile(filepath.Join(w.Root, cm.Files[0]), "package x\n"); err != nil {
			t.Fatal(err)
		}
	}
	restoreBroken()
	res, err = env.Verify(ctx, c, w, env.VerifyOptions{})
	if err != nil || res.Pass {
		t.Fatalf("forged composite script accepted: %v", err)
	}
	if !contains(res.ProtectedTouched, ".sleipnir/composite.sh") {
		t.Errorf("forgery not flagged: %v", res.ProtectedTouched)
	}
}

func TestCompositeGrouping(t *testing.T) {
	mk := func(id, commit, file string, mods ...func(*rl.Task)) rl.Task {
		meta, _ := json.Marshal(Meta{Commit: commit, Files: []string{file}, AuthorDate: "2024-03-01T00:00:00Z"})
		tk := rl.Task{
			ID: id, Kind: rl.TaskFix, Repo: rl.RepoSpec{Path: "/repos/r", Commit: commit}, Prompt: "fix " + id,
			Setup:    []string{"go mod download", "apply-" + id},
			Verifier: rl.Verifier{Cmd: "go test ./" + id, TimeoutS: 100, Protected: []string{"*_test.go"}},
			Budget:   rl.Budget{Steps: 40, Requests: 100, WallS: 900, ITE: 1000, ContextWindow: 32000},
			Tags:     []string{"go", id}, Meta: meta,
		}
		for _, m := range mods {
			m(&tk)
		}
		return tk
	}
	tasks := []rl.Task{
		mk("a", "c1", "a.go"), mk("b", "c1", "b.go"), mk("c", "c1", "c.go"), mk("d", "c1", "d.go"),
		mk("clash", "c1", "a.go"),      // shares a.go with a
		mk("other-base", "c2", "e.go"), // different commit
		mk("regex", "c1", "f.go", func(t *rl.Task) { t.Verifier.Pass = "regex:ok" }), // unsupported pass mode
		mk("recall", "c1", "g.go", func(t *rl.Task) { t.Kind = rl.TaskRecall }),
		mk("nometa", "c1", "h.go", func(t *rl.Task) { t.Meta = nil }),
	}
	comps, err := Composite(tasks, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	used := map[string]int{}
	for _, c := range comps {
		var cm Meta
		json.Unmarshal(c.Meta, &cm)
		if len(cm.Components) != 2 {
			t.Fatalf("%+v", cm)
		}
		for _, id := range cm.Components {
			used[id]++
		}
		if c.Budget.ContextWindow != 32000 || c.Budget.WallS != 900+(900)/2 || c.Budget.ITE != 2500 {
			t.Errorf("budget scaling: %+v", c.Budget)
		}
		if c.Verifier.TimeoutS != 60+200 {
			t.Errorf("timeout: %d", c.Verifier.TimeoutS)
		}
	}
	for _, bad := range []string{"other-base", "regex", "recall", "nometa"} {
		if used[bad] != 0 {
			t.Errorf("%s must not be combined", bad)
		}
	}
	for id, n := range used {
		if n != 1 {
			t.Errorf("%s used %d times", id, n)
		}
	}
	if len(comps) != 2 { // a,b,c,d -> two pairs (clash conflicts with a only)
		t.Fatalf("got %d composites: %v", len(comps), idsOf(comps))
	}
	// Deterministic, independent of input order, and dependent on the seed.
	rev := append([]rl.Task(nil), tasks...)
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	comps2, _ := Composite(rev, 2, 1)
	if !reflect.DeepEqual(idsOf(comps), idsOf(comps2)) {
		t.Errorf("input order changed the result: %v vs %v", idsOf(comps), idsOf(comps2))
	}
	comps3, _ := Composite(tasks, 2, 2)
	sort.Strings(idsOf(comps3))
	// k larger than what fits yields nothing, not an error.
	if none, err := Composite(tasks, 6, 1); err != nil || len(none) != 0 {
		t.Errorf("k=6: %v %v", none, err)
	}
	if _, err := Composite(tasks, 1, 1); err == nil {
		t.Error("k=1 accepted")
	}
	// WithMax limits the output.
	if one, _ := Composite(tasks, 2, 1, WithMax(1)); len(one) != 1 {
		t.Errorf("WithMax(1): %d", len(one))
	}
	// A hidden-path conflict keeps two tasks apart.
	x := mk("x", "c1", "x.go", func(t *rl.Task) { t.Verifier.Hidden = map[string]string{"h_test.go": "text:one"} })
	y := mk("y", "c1", "y.go", func(t *rl.Task) { t.Verifier.Hidden = map[string]string{"h_test.go": "text:two"} })
	if got, _ := Composite([]rl.Task{x, y}, 2, 1); len(got) != 0 {
		t.Errorf("tasks with clashing hidden files were combined: %v", idsOf(got))
	}
	if !strings.Contains(comps[0].Prompt, "Problem 2:") {
		t.Error("prompt")
	}
}

func TestValidateCompositeNeedsGoldBlobs(t *testing.T) {
	c := rl.Task{ID: "c", Meta: json.RawMessage(`{"components":["a","b"]}`)}
	if _, err := ValidateComposite(context.Background(), c, events.NewMemBlobs(), env.VerifyOptions{}); err == nil {
		t.Error("composite without gold blobs validated")
	}
	c.Meta = json.RawMessage(`{"gold_blobs":["` + strings.Repeat("ab", 32) + `"]}`)
	if _, err := ValidateComposite(context.Background(), c, nil, env.VerifyOptions{}); err == nil {
		t.Error("nil gold store accepted")
	}
	if _, err := ValidateComposite(context.Background(), c, events.NewMemBlobs(), env.VerifyOptions{}); err == nil {
		t.Error("missing blob accepted")
	}
}
