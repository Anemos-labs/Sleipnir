package gitx

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// validateRev refuses revision arguments that git could take for something else.
// A leading '-' would be parsed as an option (--output=<file> writes files, -c and
// --upload-pack run programs on some subcommands); a newline or NUL cannot occur
// in a ref we would ever create and is how batch-mode inputs get smuggled.
func validateRev(op, rev string) error {
	switch {
	case rev == "":
		return newErr(KindInvalid, op, "empty revision")
	case strings.HasPrefix(rev, "-"):
		return newErr(KindInvalid, op, "revision %q looks like an option", rev)
	case strings.ContainsAny(rev, "\x00\n\r"):
		return newErr(KindInvalid, op, "revision contains a control character")
	case len(rev) > 4096:
		return newErr(KindInvalid, op, "revision is too long")
	}
	return nil
}

// cleanRelPath validates a repository-relative path handed to git commands that
// take "<rev>:<path>" and returns its slash-separated clean form.
func cleanRelPath(op, p string) (string, error) {
	// A newline is fine in an argument (only NUL cannot be passed at all); every
	// list we read back from git is NUL-separated, so names with newlines survive.
	if p == "" || strings.ContainsRune(p, 0) {
		return "", newErr(KindInvalid, op, "invalid path %q", p)
	}
	// Git paths always use '/'. ToSlash is the identity on Unix, where a backslash
	// is an ordinary file name character.
	p = filepath.ToSlash(p)
	c := path.Clean(p)
	if path.IsAbs(c) || c == ".." || strings.HasPrefix(c, "../") || c == "." {
		return "", newErr(KindInvalid, op, "path %q is outside the repository", p)
	}
	return c, nil
}

func notFound(op, what string) error {
	return &Error{Kind: KindNotFound, Op: op, ExitCode: 1, Detail: what}
}

// ResolveRef resolves a revision (branch, tag, sha, HEAD~2, ...) to the full
// object name of the commit it points at.
func (r *Repo) ResolveRef(ctx context.Context, ref string) (string, error) {
	if err := validateRev("rev-parse", ref); err != nil {
		return "", err
	}
	out, err := r.run(ctx, call{
		args:   []string{"rev-parse", "--verify", "--quiet", "--end-of-options", ref + "^{commit}"},
		okExit: []int{1},
	})
	if err != nil {
		return "", err
	}
	if out.exit != 0 {
		return "", notFound("rev-parse", fmt.Sprintf("no commit named %q", ref))
	}
	return out.trimmed(), nil
}

// ResolveTree resolves a revision to the tree object it names.
func (r *Repo) ResolveTree(ctx context.Context, rev string) (string, error) {
	if err := validateRev("rev-parse", rev); err != nil {
		return "", err
	}
	out, err := r.run(ctx, call{
		args:   []string{"rev-parse", "--verify", "--quiet", "--end-of-options", rev + "^{tree}"},
		okExit: []int{1},
	})
	if err != nil {
		return "", err
	}
	if out.exit != 0 {
		return "", notFound("rev-parse", fmt.Sprintf("no tree named %q", rev))
	}
	return out.trimmed(), nil
}

// Head returns the commit HEAD points at. A repository without commits yields
// ErrNotFound.
func (r *Repo) Head(ctx context.Context) (string, error) {
	sha, err := r.ResolveRef(ctx, "HEAD")
	if err != nil && KindOf(err) == KindNotFound {
		return "", notFound("rev-parse", "HEAD does not point to a commit (the repository has no commits yet)")
	}
	return sha, err
}

// Branch returns the short name of the checked-out branch, or "" when HEAD is
// detached.
func (r *Repo) Branch(ctx context.Context) (string, error) {
	out, err := r.run(ctx, call{args: []string{"symbolic-ref", "--quiet", "--short", "HEAD"}, okExit: []int{1}})
	if err != nil {
		return "", err
	}
	if out.exit != 0 {
		return "", nil
	}
	return out.trimmed(), nil
}

// MergeBase returns the best common ancestor of two revisions, or ErrNotFound
// when their histories are unrelated.
func (r *Repo) MergeBase(ctx context.Context, a, b string) (string, error) {
	if err := validateRev("merge-base", a); err != nil {
		return "", err
	}
	if err := validateRev("merge-base", b); err != nil {
		return "", err
	}
	out, err := r.run(ctx, call{args: []string{"merge-base", a, b}, okExit: []int{1}})
	if err != nil {
		return "", err
	}
	if out.exit != 0 {
		return "", notFound("merge-base", fmt.Sprintf("%q and %q have no common ancestor", a, b))
	}
	return strings.TrimSpace(strings.SplitN(out.text(), "\n", 2)[0]), nil
}

// IsAncestor reports whether a is an ancestor of (or equal to) b.
func (r *Repo) IsAncestor(ctx context.Context, a, b string) (bool, error) {
	if err := validateRev("merge-base", a); err != nil {
		return false, err
	}
	if err := validateRev("merge-base", b); err != nil {
		return false, err
	}
	out, err := r.run(ctx, call{args: []string{"merge-base", "--is-ancestor", a, b}, okExit: []int{1}})
	if err != nil {
		return false, err
	}
	return out.exit == 0, nil
}

// Show returns the content of path at rev (binary-safe, capped at the runner's
// output limit). With an empty path it returns git-show style text for the
// commit: header, message, diffstat and patch.
func (r *Repo) Show(ctx context.Context, rev, filePath string) ([]byte, error) {
	sha, err := r.ResolveRef(ctx, rev)
	if err != nil {
		return nil, err
	}
	if filePath == "" {
		out, err := r.run(ctx, call{
			args: []string{"show", "--no-color", "--no-ext-diff", "--no-textconv", "--stat", "--patch",
				"--format=medium", "--end-of-options", sha},
			maxOut: 1 << 20, killOnCap: true,
		})
		if err != nil {
			return nil, err
		}
		return out.stdout, nil
	}
	p, err := cleanRelPath("cat-file", filePath)
	if err != nil {
		return nil, err
	}
	out, err := r.run(ctx, call{args: []string{"cat-file", "blob", sha + ":" + p}})
	if err != nil {
		if KindOf(err) == KindOther {
			// "fatal: Not a valid object name" and "not a blob" both mean the path is
			// not a file at that revision.
			return nil, &Error{Kind: KindNotFound, Op: "cat-file", ExitCode: 128, Detail: fmt.Sprintf("%s is not a file at %s", p, rev), Err: err}
		}
		return nil, err
	}
	return out.stdout, nil
}

// Person is an author or committer identity with its timestamp.
type Person struct {
	Name  string
	Email string
	When  time.Time
}

// Commit is one commit as stored, parsed from the raw object.
type Commit struct {
	SHA       string
	Tree      string
	Parents   []string
	Author    Person
	Committer Person
	// Subject is the first line of the message; Body is the rest, trimmed.
	Subject string
	Body    string
}

// LogOptions selects commits.
type LogOptions struct {
	// Rev is the starting point (default "HEAD"). Range, when set, is used
	// instead ("a..b").
	Rev   string
	Range string
	// Max bounds the result (default 100, at most 10000).
	Max         int
	Paths       []string
	NoMerges    bool
	FirstParent bool
}

// Log lists commits newest first. Commits are read as raw objects rather than
// through --format, so no message, name or path can break the parse.
func (r *Repo) Log(ctx context.Context, opts LogOptions) ([]Commit, error) {
	spec := opts.Range
	if spec == "" {
		spec = opts.Rev
	}
	if spec == "" {
		spec = "HEAD"
	}
	if err := validateRev("log", spec); err != nil {
		return nil, err
	}
	max := opts.Max
	if max <= 0 {
		max = 100
	}
	if max > 10000 {
		max = 10000
	}
	args := []string{"rev-list", "--max-count=" + strconv.Itoa(max)}
	if opts.NoMerges {
		args = append(args, "--no-merges")
	}
	if opts.FirstParent {
		args = append(args, "--first-parent")
	}
	args = append(args, "--end-of-options", spec)
	if len(opts.Paths) > 0 {
		args = append(args, "--")
		for _, p := range opts.Paths {
			c, err := cleanRelPath("log", p)
			if err != nil {
				return nil, err
			}
			args = append(args, c)
		}
	}
	out, err := r.run(ctx, call{args: args})
	if err != nil {
		return nil, err
	}
	shas := strings.Fields(out.text())
	if len(shas) == 0 {
		return nil, nil
	}
	return r.readCommits(ctx, shas)
}

// readCommits loads and parses raw commit objects with one cat-file process.
func (r *Repo) readCommits(ctx context.Context, shas []string) ([]Commit, error) {
	in := strings.Join(shas, "\n") + "\n"
	out, err := r.run(ctx, call{args: []string{"cat-file", "--batch"}, stdin: strings.NewReader(in)})
	if err != nil {
		return nil, err
	}
	var commits []Commit
	data := out.stdout
	for len(data) > 0 {
		nl := bytes.IndexByte(data, '\n')
		if nl < 0 {
			break
		}
		header := strings.Fields(string(data[:nl]))
		data = data[nl+1:]
		if len(header) == 2 && header[1] == "missing" {
			continue
		}
		if len(header) != 3 {
			return commits, &Error{Kind: KindOther, Op: "cat-file", ExitCode: 0, Detail: "unexpected batch header: " + string(header[0])}
		}
		size, err := strconv.Atoi(header[2])
		if err != nil || size < 0 || size > len(data) {
			return commits, &Error{Kind: KindOther, Op: "cat-file", ExitCode: 0, Detail: "truncated batch output"}
		}
		body := data[:size]
		data = data[size:]
		if len(data) > 0 && data[0] == '\n' {
			data = data[1:]
		}
		if header[1] != "commit" {
			continue
		}
		commits = append(commits, parseCommit(header[0], body))
	}
	return commits, nil
}

// parseCommit decodes a raw commit object. It is lenient: a malformed header
// yields a partly filled Commit rather than an error, because a repository's
// history is data we report, not something we may refuse to read.
func parseCommit(sha string, raw []byte) Commit {
	c := Commit{SHA: sha}
	head, msg, _ := bytes.Cut(raw, []byte("\n\n"))
	for _, line := range strings.Split(string(head), "\n") {
		if strings.HasPrefix(line, " ") { // continuation of gpgsig/mergetag
			continue
		}
		key, val, _ := strings.Cut(line, " ")
		switch key {
		case "tree":
			c.Tree = val
		case "parent":
			c.Parents = append(c.Parents, val)
		case "author":
			c.Author = parsePerson(val)
		case "committer":
			c.Committer = parsePerson(val)
		}
	}
	message := strings.TrimRight(string(msg), "\n")
	subject, body, _ := strings.Cut(message, "\n")
	c.Subject = strings.TrimSpace(subject)
	c.Body = strings.TrimSpace(body)
	return c
}

func parsePerson(s string) Person {
	var p Person
	lt := strings.LastIndex(s, " <")
	gt := strings.LastIndex(s, ">")
	if lt < 0 || gt < lt {
		p.Name = s
		return p
	}
	p.Name = s[:lt]
	p.Email = s[lt+2 : gt]
	fields := strings.Fields(s[gt+1:])
	if len(fields) >= 1 {
		if sec, err := strconv.ParseInt(fields[0], 10, 64); err == nil {
			p.When = time.Unix(sec, 0).UTC()
		}
	}
	return p
}

// CommitInfo reads one commit.
func (r *Repo) CommitInfo(ctx context.Context, rev string) (*Commit, error) {
	sha, err := r.ResolveRef(ctx, rev)
	if err != nil {
		return nil, err
	}
	cs, err := r.readCommits(ctx, []string{sha})
	if err != nil {
		return nil, err
	}
	if len(cs) == 0 {
		return nil, notFound("cat-file", "commit "+sha+" is missing")
	}
	return &cs[0], nil
}

// CountCommits returns how many commits are reachable from tip but not from base
// ("base..tip").
func (r *Repo) CountCommits(ctx context.Context, base, tip string) (int, error) {
	if err := validateRev("rev-list", base); err != nil {
		return 0, err
	}
	if err := validateRev("rev-list", tip); err != nil {
		return 0, err
	}
	out, err := r.run(ctx, call{args: []string{"rev-list", "--count", "--end-of-options", base + ".." + tip}})
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(out.trimmed())
	if err != nil {
		return 0, &Error{Kind: KindOther, Op: "rev-list", ExitCode: 0, Detail: "unexpected count " + out.trimmed()}
	}
	return n, nil
}
