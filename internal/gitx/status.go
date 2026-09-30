package gitx

import (
	"context"
	"strconv"
	"strings"
)

// Change is one path in the index or work tree that differs from what it is
// compared with.
type Change struct {
	Path string
	// OrigPath is the source of a rename or copy.
	OrigPath string
	// Code is git's one-letter state: M modified, A added, D deleted, R renamed,
	// C copied, T type changed.
	Code byte
	// Submodule marks a gitlink entry.
	Submodule bool
}

// Conflicted is an unmerged path; XY is git's two-letter code (UU both modified,
// AA both added, DU deleted by us, UD deleted by them ...).
type Conflicted struct {
	Path string
	XY   string
}

// Status is `git status --porcelain=v2 --branch` parsed.
type Status struct {
	// Head is the commit HEAD points at ("" before the first commit).
	Head string
	// Branch is the checked-out branch ("" when detached).
	Branch      string
	Detached    bool
	Upstream    string
	HasUpstream bool
	Ahead       int
	Behind      int
	// Staged are index-vs-HEAD changes, Unstaged are work-tree-vs-index changes.
	// A path modified in both appears in both.
	Staged     []Change
	Unstaged   []Change
	Untracked  []string
	Conflicted []Conflicted
	// Truncated is true when the output cap was hit and the lists are incomplete.
	Truncated bool
}

// Clean reports whether nothing is staged, modified, untracked or conflicted.
func (s *Status) Clean() bool {
	return len(s.Staged) == 0 && len(s.Unstaged) == 0 && len(s.Untracked) == 0 && len(s.Conflicted) == 0 && !s.Truncated
}

// StatusOptions tunes Status.
type StatusOptions struct {
	// Untracked is "normal" (default; untracked directories collapse to "dir/"),
	// "all" (every file) or "no".
	Untracked string
	// MaxBytes caps the parsed output (default 8 MiB); beyond it Truncated is set.
	MaxBytes int64
}

// Status reports the state of the work tree.
func (r *Repo) Status(ctx context.Context) (*Status, error) {
	return r.StatusWith(ctx, StatusOptions{})
}

// StatusWith is Status with options.
func (r *Repo) StatusWith(ctx context.Context, opts StatusOptions) (*Status, error) {
	if r.bare {
		return nil, &Error{Kind: KindNotARepo, Op: "status", ExitCode: -1, Detail: "bare repository has no work tree"}
	}
	u := opts.Untracked
	switch u {
	case "":
		u = "normal"
	case "normal", "all", "no":
	default:
		return nil, newErr(KindInvalid, "status", "untracked mode %q", u)
	}
	max := opts.MaxBytes
	if max <= 0 {
		max = 8 << 20
	}
	out, err := r.run(ctx, call{
		// dirty: report a submodule whose commit moved but never descend into its
		// work tree (a second repository with its own untrusted configuration).
		// --renames because diff.renames=false is part of the hardening (it also
		// governs status), and a rename is worth reporting as one.
		args:      []string{"status", "--porcelain=v2", "--branch", "-z", "--renames", "--untracked-files=" + u, "--ignore-submodules=dirty"},
		maxOut:    max,
		killOnCap: true,
	})
	if err != nil {
		return nil, err
	}
	st := parseStatus(out.stdout)
	st.Truncated = out.truncated
	return st, nil
}

// IsClean reports whether the work tree has no changes at all (untracked files
// included, ignored files excluded).
func (r *Repo) IsClean(ctx context.Context) (bool, error) {
	st, err := r.Status(ctx)
	if err != nil {
		return false, err
	}
	return st.Clean(), nil
}

func parseStatus(data []byte) *Status {
	st := &Status{}
	toks := strings.Split(string(data), "\x00")
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if t == "" {
			continue
		}
		switch t[0] {
		case '#':
			parseHeader(st, t)
		case '1':
			// 1 XY sub mH mI mW hH hI path
			f := strings.SplitN(t, " ", 9)
			if len(f) < 9 {
				continue
			}
			addChange(st, f[1], f[2], f[8], "")
		case '2':
			// 2 XY sub mH mI mW hH hI Xscore path <NUL> origPath
			f := strings.SplitN(t, " ", 10)
			if len(f) < 10 {
				continue
			}
			orig := ""
			if i+1 < len(toks) {
				orig = toks[i+1]
				i++
			}
			addChange(st, f[1], f[2], f[9], orig)
		case 'u':
			// u XY sub m1 m2 m3 mW h1 h2 h3 path
			f := strings.SplitN(t, " ", 11)
			if len(f) < 11 {
				continue
			}
			st.Conflicted = append(st.Conflicted, Conflicted{Path: f[10], XY: f[1]})
		case '?':
			if len(t) > 2 {
				st.Untracked = append(st.Untracked, t[2:])
			}
		}
	}
	return st
}

func parseHeader(st *Status, line string) {
	f := strings.Fields(line)
	if len(f) < 3 {
		return
	}
	switch f[1] {
	case "branch.oid":
		if f[2] != "(initial)" {
			st.Head = f[2]
		}
	case "branch.head":
		// A branch name can contain spaces? No: git forbids them in refs.
		if f[2] == "(detached)" {
			st.Detached = true
		} else {
			st.Branch = f[2]
		}
	case "branch.upstream":
		st.Upstream, st.HasUpstream = f[2], true
	case "branch.ab":
		if len(f) >= 4 {
			st.Ahead, _ = strconv.Atoi(strings.TrimPrefix(f[2], "+"))
			st.Behind, _ = strconv.Atoi(strings.TrimPrefix(f[3], "-"))
		}
	}
}

func addChange(st *Status, xy, sub, path, orig string) {
	if len(xy) != 2 {
		return
	}
	isSub := strings.HasPrefix(sub, "S")
	if xy[0] != '.' {
		st.Staged = append(st.Staged, Change{Path: path, OrigPath: orig, Code: xy[0], Submodule: isSub})
	}
	if xy[1] != '.' {
		st.Unstaged = append(st.Unstaged, Change{Path: path, OrigPath: orig, Code: xy[1], Submodule: isSub})
	}
}
