package gitx

import (
	"context"
	"os"
	"strings"
)

// maxPatchBytes bounds a patch handed to git apply.
const maxPatchBytes = 64 << 20

// ApplyOptions tunes ApplyWith.
type ApplyOptions struct {
	// Check verifies the patch would apply without touching anything.
	Check bool
	// ThreeWay falls back to a three-way merge using the blobs named in the
	// patch's index lines (needs a patch made with --full-index, as ours are).
	// Conflicts are left as markers in the work tree.
	ThreeWay bool
	// Index also updates the index.
	Index bool
	// Reverse applies the patch backwards.
	Reverse bool
}

// Apply applies a patch (as produced by Diff) to the work tree; with check it
// only verifies that it would apply. A patch that does not apply yields an error
// of kind KindConflict and changes nothing (git apply is all-or-nothing).
//
// git apply refuses paths that leave the work tree and writes that would go
// through a symbolic link, which is what makes handing it a model-authored patch
// tolerable.
func (r *Repo) Apply(ctx context.Context, patch string, check bool) error {
	return r.ApplyWith(ctx, patch, ApplyOptions{Check: check})
}

// ApplyWith is Apply with options.
func (r *Repo) ApplyWith(ctx context.Context, patch string, opts ApplyOptions) error {
	if r.bare {
		return &Error{Kind: KindNotARepo, Op: "apply", ExitCode: -1, Detail: "bare repository has no work tree"}
	}
	if strings.TrimSpace(patch) == "" {
		return newErr(KindInvalid, "apply", "empty patch")
	}
	if len(patch) > maxPatchBytes {
		return &Error{Kind: KindTooLarge, Op: "apply", ExitCode: -1, Detail: "patch exceeds the size limit"}
	}
	if strings.ContainsRune(patch, 0) {
		// Binary content is base85 text in our patches; a raw NUL means the input is
		// not a patch.
		return newErr(KindInvalid, "apply", "patch contains a NUL byte")
	}
	args := []string{"apply", "--whitespace=nowarn"}
	if opts.Check {
		args = append(args, "--check")
	}
	if opts.ThreeWay {
		args = append(args, "--3way")
	}
	if opts.Index {
		args = append(args, "--index")
	}
	if opts.Reverse {
		args = append(args, "--reverse")
	}
	args = append(args, "-")
	_, err := r.run(ctx, call{args: args, stdin: strings.NewReader(patch), mutating: !opts.Check})
	return err
}

// PatchedTree computes the tree produced by applying patch to base. It uses a
// temporary index and never changes the working tree, real index, or references.
func (r *Repo) PatchedTree(ctx context.Context, base, patch string) (string, error) {
	if err := validateRev("patched tree", base); err != nil {
		return "", err
	}
	if strings.TrimSpace(patch) == "" || len(patch) > maxPatchBytes || strings.ContainsRune(patch, 0) {
		return "", newErr(KindInvalid, "patched tree", "invalid or oversized patch")
	}
	f, err := os.CreateTemp("", "sleipnir-apply-index-*")
	if err != nil {
		return "", err
	}
	name := f.Name()
	if err := f.Close(); err != nil {
		os.Remove(name)
		return "", err
	}
	defer os.Remove(name)
	defer os.Remove(name + ".lock")
	env := []string{"GIT_INDEX_FILE=" + name}
	if _, err := r.run(ctx, call{args: []string{"read-tree", base}, env: env}); err != nil {
		return "", err
	}
	if _, err := r.run(ctx, call{args: []string{"apply", "--cached", "--whitespace=nowarn", "-"}, env: env, stdin: strings.NewReader(patch)}); err != nil {
		return "", err
	}
	out, err := r.run(ctx, call{args: []string{"write-tree"}, env: env})
	if err != nil {
		return "", err
	}
	return out.trimmed(), nil
}
