package gitx

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// BlameLine is the commit and author of one line (1-based Line, in the blamed
// content). Author is the author's name, Email their address without the angle
// brackets, Summary the commit's subject. A line no commit introduced (content given
// to BlameContents that differs from rev) has the all-zero commit.
type BlameLine struct {
	Line           int
	Commit, Author string
	Email, Summary string
}

// ZeroCommit is the commit name git blame gives a line that no commit introduced.
const ZeroCommit = "0000000000000000000000000000000000000000"

// maxBlameOutput bounds the porcelain output read from one blame.
const maxBlameOutput = 64 << 20

// Blame runs git blame on path at rev ("blame" is in the allowlist of Repo.Git):
// who introduced each line of the file as rev has it.
func (r *Repo) Blame(ctx context.Context, rev, path string) ([]BlameLine, error) {
	return r.blame(ctx, rev, path, nil)
}

// BlameContents is Blame of contents (the file as it is in a work tree, say) against
// the history of rev: lines that rev's history introduced are attributed to their
// commits, the others to ZeroCommit. The contents go to git on its standard input,
// never through a file.
func (r *Repo) BlameContents(ctx context.Context, rev, path string, contents []byte) ([]BlameLine, error) {
	if contents == nil {
		contents = []byte{}
	}
	return r.blame(ctx, rev, path, contents)
}

// blame runs git blame --porcelain and parses it.
func (r *Repo) blame(ctx context.Context, rev, path string, contents []byte) ([]BlameLine, error) {
	if err := validateRev("blame", rev); err != nil {
		return nil, err
	}
	p, err := cleanRelPath("blame", path)
	if err != nil {
		return nil, err
	}
	args := []string{"blame", "--porcelain", "--no-progress"}
	c := call{maxOut: maxBlameOutput}
	if contents != nil {
		args = append(args, "--contents", "-")
		c.stdin = bytes.NewReader(contents)
	}
	c.args = append(args, rev, "--", p)
	out, err := r.run(ctx, c)
	if err != nil {
		return nil, err
	}
	if out.truncated {
		return nil, &Error{Kind: KindTooLarge, Op: "blame", ExitCode: -1, Detail: "blame output exceeds the size limit"}
	}
	return parsePorcelain(out.stdout), nil
}

// parsePorcelain reads git blame --porcelain: a header line "<sha> <orig> <final>
// [<count>]" per line, the commit's details the first time it appears, then the line
// itself after a tab.
func parsePorcelain(b []byte) []BlameLine {
	type info struct{ author, email, summary string }
	commits := map[string]*info{}
	var out []BlameLine
	var cur *BlameLine
	for _, raw := range bytes.Split(b, []byte("\n")) {
		line := string(raw)
		if strings.HasPrefix(line, "\t") {
			if cur != nil {
				if in := commits[cur.Commit]; in != nil {
					cur.Author, cur.Email, cur.Summary = in.author, in.email, in.summary
				}
				out = append(out, *cur)
				cur = nil
			}
			continue
		}
		if cur == nil {
			f := strings.Fields(line)
			if len(f) >= 3 && len(f[0]) >= 40 && isHex(f[0]) {
				n, err := strconv.Atoi(f[2])
				if err == nil {
					cur = &BlameLine{Commit: f[0], Line: n}
					if commits[f[0]] == nil {
						commits[f[0]] = &info{}
					}
				}
			}
			continue
		}
		in := commits[cur.Commit]
		key, val, _ := strings.Cut(line, " ")
		switch key {
		case "author":
			in.author = val
		case "author-mail":
			in.email = strings.TrimSuffix(strings.TrimPrefix(val, "<"), ">")
		case "summary":
			in.summary = val
		}
	}
	return out
}

// isHex reports whether s is lowercase hexadecimal.
func isHex(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// CommitPaths commits only paths (a path-limited commit), refusing conflict markers as
// CommitAll does: changes to other files, staged or not, stay as they are. Paths are
// relative to the repository root, taken literally (no pathspec magic); a path that
// neither exists nor is tracked is skipped. It returns the new commit, or "" with a nil
// error when the paths hold nothing to commit. It refuses to run during a merge or
// another multi-step operation (git cannot commit part of a merge).
func (r *Repo) CommitPaths(ctx context.Context, msg string, author Author, paths []string) (string, error) {
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
	if op := r.InProgress(); op != "" {
		return "", &Error{Kind: KindConflict, Op: "commit", ExitCode: -1, Detail: "a " + op + " is in progress; finish or abort it first"}
	}
	idEnv, err := author.env(r.s.now)
	if err != nil {
		return "", err
	}
	var clean []string
	seen := map[string]bool{}
	for _, p := range paths {
		c, err := cleanRelPath("commit", p)
		if err != nil {
			return "", err
		}
		if !seen[c] {
			seen[c] = true
			clean = append(clean, c)
		}
	}
	if len(clean) == 0 {
		return "", nil
	}
	b, err := r.begin(ctx)
	if err != nil {
		return "", err
	}
	if err := r.checkNoMarkers(ctx); err != nil {
		return "", err
	}
	literal := []string{"GIT_LITERAL_PATHSPECS=1"}
	// Keep the paths that exist or that git knows: `add` refuses a pathspec that
	// matches nothing.
	tracked := map[string]bool{}
	lsArgs := append([]string{"ls-files", "-z", "--"}, clean...)
	if out, err := b.run(ctx, call{args: lsArgs, env: literal}); err == nil {
		for _, f := range strings.Split(out.text(), "\x00") {
			if f != "" {
				tracked[f] = true
			}
		}
	} else {
		return "", err
	}
	var keep []string
	for _, p := range clean {
		full, err := safeJoin(r.root, p)
		if err != nil {
			return "", err
		}
		if _, err := os.Lstat(full); err == nil || tracked[p] || hasTrackedUnder(tracked, p) {
			keep = append(keep, filepath.ToSlash(p))
		}
	}
	if len(keep) == 0 {
		return "", nil
	}
	addArgs := append([]string{"add", "-A", "--"}, keep...)
	if _, err := b.run(ctx, call{args: addArgs, env: literal, mutating: true, timeout: 5 * r.s.timeout}); err != nil {
		return "", err
	}
	// exit 0: those paths in the index equal HEAD (nothing to commit); 1: there is.
	diffArgs := append([]string{"diff", "--cached", "--quiet", "--no-ext-diff", "--ignore-submodules=dirty", "--"}, keep...)
	out, err := b.run(ctx, call{args: diffArgs, env: literal, okExit: []int{1}})
	if err != nil {
		return "", err
	}
	if out.exit == 0 {
		return "", nil
	}
	commitArgs := append([]string{"commit", "--no-verify", "--no-gpg-sign", "--cleanup=whitespace", "-F", "-", "--only", "--"}, keep...)
	if _, err := b.run(ctx, call{args: commitArgs, stdin: strings.NewReader(msg + "\n"), env: append(idEnv, literal...), mutating: true}); err != nil {
		return "", err
	}
	return r.Head(ctx)
}

// hasTrackedUnder reports whether a tracked path lies under directory p.
func hasTrackedUnder(tracked map[string]bool, p string) bool {
	for f := range tracked {
		if strings.HasPrefix(f, p+"/") {
			return true
		}
	}
	return false
}

// ApplyHunk applies one hunk of one file, forward or reversed (Reverse), to the work
// tree: it checks first that the hunk applies (git apply --check) and changes nothing
// when it does not (KindConflict). hunk is the hunk's text from its "@@" header on,
// with every line terminated; path is the file's path relative to the repository
// root. The patch git sees names that one file only and is restricted to it
// (--include), so a crafted hunk cannot reach another path.
func (r *Repo) ApplyHunk(ctx context.Context, path, hunk string, reverse bool) error {
	p, err := cleanRelPath("apply", path)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(hunk, "@@ ") || strings.Contains(hunk, "\n--- ") || strings.Contains(hunk, "\n+++ ") || strings.Contains(hunk, "\ndiff ") {
		return newErr(KindInvalid, "apply", "not a single hunk")
	}
	if !strings.HasSuffix(hunk, "\n") {
		hunk += "\n"
	}
	patch := "--- a/" + p + "\n+++ b/" + p + "\n" + hunk
	opts := ApplyOptions{Reverse: reverse, Include: []string{p}}
	opts.Check = true
	if err := r.ApplyWith(ctx, patch, opts); err != nil {
		return err
	}
	opts.Check = false
	return r.ApplyWith(ctx, patch, opts)
}
