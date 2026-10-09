package env

import (
	"bytes"
	"context"
	"errors"
	"sort"

	"github.com/anemos-labs/sleipnir/internal/rl"
)

// CheckMutants is the seeded regression check of a task's verifier: a check
// counts only if it passes on the solved code and fails once part of the
// solution is taken away. For each file the reference solution (gold) changes,
// it builds the partial solution that leaves that one file at its start
// content and runs the verifier on it. It returns, sorted, the files whose
// revert the verifier does not notice (it still passes, on every repeat): a
// task whose verifier passes a partial solution pays for unfinished work, or
// the solution changes a file the task does not need.
//
// Only files the verifier would apply count: protected and hidden paths are
// discarded at verification anyway. A solution of one such file has no
// partial solution beyond the start, which CheckTask already proves fails.
// The check is deterministic (files in path order, one verification each)
// and costs one verifier run per solution file and repeat.
func CheckMutants(ctx context.Context, task rl.Task, gold []byte, opts VerifyOptions) ([]string, error) {
	m := opts.Workspaces
	if m == nil {
		return nil, Infra("mutants", errors.New("VerifyOptions.Workspaces is required"))
	}
	if !needsCommand(task) {
		return nil, nil
	}
	files, err := m.parsePatch(ctx, gold)
	if err != nil {
		return nil, Infra("parse patch", err)
	}
	prot, err := CompileGlobs(task.Verifier.Protected)
	if err != nil {
		return nil, Infra("mutants", err)
	}
	hidden := make(map[string]bool, len(task.Verifier.Hidden))
	for p := range task.Verifier.Hidden {
		hidden[foldPath(p)] = true
	}
	applied := filterPatch(files, prot, hidden).Applied
	if len(applied) < 2 {
		return nil, nil
	}
	sort.SliceStable(applied, func(a, b int) bool { return applied[a].Path < applied[b].Path })
	if opts.Repeats > 1 {
		opts.PassPolicy = PassAll // weak only when the partial solution passes every time
	}
	var weak []string
	for i, reverted := range applied {
		var partial bytes.Buffer
		for j, f := range applied {
			if j == i {
				continue
			}
			partial.Write(f.body)
			if !bytes.HasSuffix(f.body, []byte("\n")) {
				partial.WriteByte('\n')
			}
		}
		res, err := VerifyPatch(ctx, task, partial.Bytes(), opts)
		if err != nil {
			return nil, err
		}
		if res.Pass {
			weak = append(weak, reverted.Path)
		}
	}
	return weak, nil
}
