package taskgen

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/rl/env"
)

// repo wraps the hardened git runner around one repository's git directory.
type repo struct {
	g      *env.Git
	gitDir string
}

func (r *repo) git(ctx context.Context, args ...string) ([]byte, error) {
	return r.g.Run(ctx, "", nil, append([]string{"--git-dir=" + r.gitDir}, args...)...)
}

func (r *repo) gitIn(ctx context.Context, stdin []byte, extraEnv []string, args ...string) ([]byte, error) {
	var in *bytes.Reader
	if stdin != nil {
		in = bytes.NewReader(stdin)
	}
	if in == nil {
		return r.g.RunEnv(ctx, "", extraEnv, nil, 1<<30, append([]string{"--git-dir=" + r.gitDir}, args...)...)
	}
	return r.g.RunEnv(ctx, "", extraEnv, in, 1<<30, append([]string{"--git-dir=" + r.gitDir}, args...)...)
}

// show returns the content of path at rev.
func (r *repo) show(ctx context.Context, rev, path string) ([]byte, error) {
	return r.git(ctx, "show", "--end-of-options", rev+":"+path)
}

// exists reports whether path exists at rev.
func (r *repo) exists(ctx context.Context, rev, path string) bool {
	_, err := r.git(ctx, "cat-file", "-e", rev+":"+path)
	return err == nil
}

func (r *repo) resolve(ctx context.Context, rev string) (string, error) {
	return r.g.ResolveCommit(ctx, r.gitDir, rev)
}

// fileChange is one changed path of a commit.
type fileChange struct {
	Status  string // A, M, D, T
	Path    string
	Added   int
	Deleted int
	Binary  bool
}

// changes lists what commit changed relative to parent, without rename
// detection: every path stands alone.
func (r *repo) changes(ctx context.Context, parent, commit string) ([]fileChange, error) {
	raw, err := r.git(ctx, "diff-tree", "-r", "-z", "--no-renames", "--raw", parent, commit)
	if err != nil {
		return nil, err
	}
	var out []fileChange
	idx := map[string]int{}
	toks := bytes.Split(raw, []byte{0})
	for i := 0; i+1 < len(toks); i += 2 {
		meta := string(toks[i])
		if !strings.HasPrefix(meta, ":") {
			return nil, fmt.Errorf("unexpected diff-tree record %q", meta)
		}
		f := strings.Fields(meta)
		if len(f) < 5 {
			return nil, fmt.Errorf("unexpected diff-tree record %q", meta)
		}
		p := string(toks[i+1])
		idx[p] = len(out)
		out = append(out, fileChange{Status: f[4][:1], Path: p})
	}
	num, err := r.git(ctx, "diff-tree", "-r", "-z", "--no-renames", "--numstat", parent, commit)
	if err != nil {
		return nil, err
	}
	for _, rec := range bytes.Split(num, []byte{0}) {
		f := strings.SplitN(string(rec), "\t", 3)
		if len(f) != 3 {
			continue
		}
		i, ok := idx[f[2]]
		if !ok {
			continue
		}
		if f[0] == "-" {
			out[i].Binary = true
			continue
		}
		out[i].Added, _ = strconv.Atoi(f[0])
		out[i].Deleted, _ = strconv.Atoi(f[1])
	}
	return out, nil
}
