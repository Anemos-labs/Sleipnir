package gitx

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Author is the identity recorded on commits we create. Commits never depend on
// the user's user.name/user.email (which may be unset in a container, or be the
// human's, which agent commits must not impersonate).
type Author struct {
	Name  string
	Email string
	// When is the commit time; zero means now (the repository's clock).
	When time.Time
}

// DefaultAuthor identifies harness commits that name no agent.
var DefaultAuthor = Author{Name: "Sleipnir", Email: "sleipnir@localhost"}

// env renders the identity as the GIT_AUTHOR_* / GIT_COMMITTER_* variables.
// Author and committer are the same on purpose: the committer would otherwise
// fall back to configuration.
func (a Author) env(now func() time.Time) ([]string, error) {
	name, email := strings.TrimSpace(a.Name), strings.TrimSpace(a.Email)
	if name == "" {
		name = DefaultAuthor.Name
	}
	if email == "" {
		email = DefaultAuthor.Email
	}
	for _, s := range []string{name, email} {
		if strings.ContainsAny(s, "\x00\n\r<>") {
			return nil, newErr(KindInvalid, "commit", "identity %q contains a forbidden character", s)
		}
	}
	when := a.When
	if when.IsZero() {
		when = now()
	}
	date := strconv.FormatInt(when.Unix(), 10) + " +0000"
	return []string{
		"GIT_AUTHOR_NAME=" + name, "GIT_AUTHOR_EMAIL=" + email, "GIT_AUTHOR_DATE=" + date,
		"GIT_COMMITTER_NAME=" + name, "GIT_COMMITTER_EMAIL=" + email, "GIT_COMMITTER_DATE=" + date,
	}, nil
}

// Unmerged is one path with conflict stages in the index.
type Unmerged struct {
	Path string
	// Stages holds the blob ids and modes for stage 1 (base), 2 (ours) and 3
	// (theirs); an empty ID means that stage is absent.
	Base, Ours, Theirs UnmergedStage
}

// UnmergedStage is one side of a conflict.
type UnmergedStage struct {
	Mode string
	ID   string
}

func (s UnmergedStage) present() bool { return s.ID != "" }

// Kind names the shape of the conflict from which sides exist.
func (u Unmerged) Kind() string {
	switch {
	case u.Base.present() && u.Ours.present() && u.Theirs.present():
		return "content"
	case !u.Base.present() && u.Ours.present() && u.Theirs.present():
		return "add/add"
	case u.Base.present() && u.Ours.present() && !u.Theirs.present():
		return "deleted-by-them"
	case u.Base.present() && !u.Ours.present() && u.Theirs.present():
		return "deleted-by-us"
	case u.Base.present() && !u.Ours.present() && !u.Theirs.present():
		return "both-deleted"
	case !u.Base.present() && u.Ours.present():
		return "added-by-us"
	case !u.Base.present() && u.Theirs.present():
		return "added-by-them"
	}
	return "unknown"
}

// Unmerged lists paths with unresolved conflicts, sorted.
func (r *Repo) Unmerged(ctx context.Context) ([]Unmerged, error) {
	out, err := r.run(ctx, call{args: []string{"ls-files", "-u", "-z"}})
	if err != nil {
		return nil, err
	}
	byPath := map[string]*Unmerged{}
	for _, rec := range strings.Split(out.text(), "\x00") {
		if rec == "" {
			continue
		}
		meta, path, ok := strings.Cut(rec, "\t")
		if !ok {
			continue
		}
		f := strings.Fields(meta)
		if len(f) != 3 {
			continue
		}
		u := byPath[path]
		if u == nil {
			u = &Unmerged{Path: path}
			byPath[path] = u
		}
		st := UnmergedStage{Mode: f[0], ID: f[1]}
		switch f[2] {
		case "1":
			u.Base = st
		case "2":
			u.Ours = st
		case "3":
			u.Theirs = st
		}
	}
	res := make([]Unmerged, 0, len(byPath))
	for _, u := range byPath {
		res = append(res, *u)
	}
	sort.Slice(res, func(i, j int) bool { return res[i].Path < res[j].Path })
	return res, nil
}

// InProgress names an interrupted multi-step operation in this work tree:
// "merge", "rebase", "cherry-pick", "revert", or "" when none is.
func (r *Repo) InProgress() string {
	for _, c := range []struct{ file, name string }{
		{"MERGE_HEAD", "merge"}, {"rebase-merge", "rebase"}, {"rebase-apply", "rebase"},
		{"CHERRY_PICK_HEAD", "cherry-pick"}, {"REVERT_HEAD", "revert"},
	} {
		if _, err := os.Lstat(filepath.Join(r.gitDir, c.file)); err == nil {
			return c.name
		}
	}
	return ""
}

// markerLine matches the start of a conflict marker line. Seven is git's default
// marker size; larger sizes (a conflict-marker-size attribute) still begin with
// seven of the same character.
func hasConflictMarkers(data []byte) bool {
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		switch {
		case bytes.HasPrefix(line, []byte("<<<<<<< ")), bytes.Equal(line, []byte("<<<<<<<")),
			bytes.HasPrefix(line, []byte(">>>>>>> ")), bytes.Equal(line, []byte(">>>>>>>")):
			return true
		}
	}
	return false
}

// CommitAll stages every change (`add -A`: modifications, deletions, new
// non-ignored files) and commits it with the given message and identity. It
// returns the new commit, or "" with a nil error when there was nothing to
// commit. Hooks, signing and editors are disabled (see the package doc).
//
// It refuses to commit while unmerged paths still contain conflict markers:
// `add -A` would happily record them as the resolution.
func (r *Repo) CommitAll(ctx context.Context, msg string, author Author) (string, error) {
	if r.bare {
		return "", &Error{Kind: KindNotARepo, Op: "commit", ExitCode: -1, Detail: "bare repository has no work tree"}
	}
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return "", newErr(KindInvalid, "commit", "empty commit message")
	}
	if strings.ContainsRune(msg, 0) {
		return "", newErr(KindInvalid, "commit", "commit message contains a NUL byte")
	}
	idEnv, err := author.env(r.s.now)
	if err != nil {
		return "", err
	}
	b, err := r.begin(ctx)
	if err != nil {
		return "", err
	}
	if err := r.checkNoMarkers(ctx); err != nil {
		return "", err
	}
	if _, err := b.run(ctx, call{args: []string{"add", "-A"}, mutating: true, timeout: 5 * r.s.timeout}); err != nil {
		return "", err
	}
	merging := r.InProgress() == "merge"
	if !merging {
		// exit 0: index equals HEAD; 1: there is something to commit.
		out, err := b.run(ctx, call{args: []string{"diff", "--cached", "--quiet", "--no-ext-diff", "--ignore-submodules=dirty"}, okExit: []int{1}})
		if err != nil {
			return "", err
		}
		if out.exit == 0 {
			return "", nil
		}
	}
	args := []string{"commit", "--no-verify", "--no-gpg-sign", "--cleanup=whitespace", "-F", "-"}
	if merging {
		args = append(args, "--allow-empty")
	}
	if _, err := b.run(ctx, call{args: args, stdin: strings.NewReader(msg + "\n"), env: idEnv, mutating: true}); err != nil {
		return "", err
	}
	return r.Head(ctx)
}

// checkNoMarkers fails when an unmerged path still holds conflict markers.
func (r *Repo) checkNoMarkers(ctx context.Context) error {
	un, err := r.Unmerged(ctx)
	if err != nil || len(un) == 0 {
		return err
	}
	var bad []string
	for _, u := range un {
		p, err := safeJoin(r.root, u.Path)
		if err != nil {
			continue
		}
		f, err := os.Open(p)
		if err != nil {
			continue // deleted: a valid resolution
		}
		data, _ := io.ReadAll(io.LimitReader(f, 8<<20))
		f.Close()
		if hasConflictMarkers(data) {
			bad = append(bad, u.Path)
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return &Error{Kind: KindConflict, Op: "commit", ExitCode: -1,
			Detail: fmt.Sprintf("unresolved conflict markers in %d file(s): %s", len(bad), strings.Join(bad, ", "))}
	}
	return nil
}

// safeJoin joins a repository-relative path onto root and refuses results that
// leave it.
func safeJoin(root, rel string) (string, error) {
	p := filepath.Join(root, filepath.FromSlash(rel))
	r, err := filepath.Rel(root, p)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", newErr(KindInvalid, "path", "%q escapes the work tree", rel)
	}
	return p, nil
}

// ResetHard moves HEAD (and the current branch) to rev and makes the index and
// work tree match it, discarding local changes and any merge or rebase in
// progress.
func (r *Repo) ResetHard(ctx context.Context, rev string) error {
	if err := validateRev("reset", rev); err != nil {
		return err
	}
	// Populating a new worktree is this command too, and on a repository with a
	// million files it is not a two-minute job.
	_, err := r.run(ctx, call{args: []string{"reset", "--hard", "--quiet", rev, "--"}, mutating: true, timeout: 10 * r.s.timeout})
	return err
}

// CleanUntracked removes untracked files and directories that are not ignored.
// Ignored files (build caches, node_modules) are kept on purpose: they are
// expensive to recreate and never part of a change.
func (r *Repo) CleanUntracked(ctx context.Context) error {
	_, err := r.run(ctx, call{args: []string{"clean", "-f", "-d", "--quiet"}, mutating: true})
	return err
}
