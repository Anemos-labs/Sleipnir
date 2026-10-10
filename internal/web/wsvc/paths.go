package wsvc

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/checkpoint"
	"github.com/anemos-labs/sleipnir/internal/gitx"
	"github.com/anemos-labs/sleipnir/internal/harden"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

const (
	// maxText is the text of a file the page is sent; past it the text is cut.
	maxText = 2 << 20
	// maxTree bounds the files the index lists.
	maxTree = 20000
	// maxPathBytes bounds a path parameter.
	maxPathBytes = 4096
)

var (
	errBadPath = werr(http.StatusBadRequest, "bad_path", "that is not a path inside the project (use a project-relative path with /)")
	errNoFile  = werr(http.StatusNotFound, "no_file", "there is no such file at that point")
)

// cleanRel validates a project-relative path the page sent and returns its clean form.
func cleanRel(p string) (string, error) {
	if p == "" || len(p) > maxPathBytes || !utf8.ValidString(p) || strings.ContainsAny(p, "\x00\\") || strings.HasPrefix(p, "/") {
		return "", errBadPath
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return "", errBadPath
		}
	}
	c := path.Clean(p)
	if c == "." || c == ".." || strings.HasPrefix(c, "../") || !filepath.IsLocal(filepath.FromSlash(c)) {
		return "", errBadPath
	}
	return c, nil
}

// hiddenPath reports whether a project path is never served whatever the rules say:
// git's own directory (of the project or of a nested repository), the state directory
// and the session's directory.
func hiddenPath(sess *session.Session, rel string) bool {
	for _, seg := range strings.Split(rel, "/") {
		if seg == ".git" {
			return true
		}
	}
	abs := filepath.Join(sess.Root(), filepath.FromSlash(rel))
	for _, dir := range []string{sess.Dir, session.StateRoot(sess.Options().Home)} {
		if dir == "" {
			continue
		}
		if r, err := filepath.Rel(dir, abs); err == nil && (r == "." || filepath.IsLocal(r)) {
			return true
		}
	}
	return false
}

// readable checks that a path may be shown at all: inside the project, not hidden, not
// refused to agents for reading. It returns the clean path or the refusal.
func (s *service) readable(sess *session.Session, p string) (string, error) {
	rel, err := cleanRel(p)
	if err != nil {
		return "", err
	}
	if hiddenPath(sess, rel) {
		return "", werr(http.StatusForbidden, "denied", "this path is not shown: it belongs to git or to the harness")
	}
	if leavesRoot(sess.Root(), rel) {
		return "", errBadPath
	}
	if c := s.classOf(sess, rel); c.protected != nil {
		return "", &wire.Error{Status: http.StatusForbidden, Code: "denied",
			Msg:    "agents may not read this path, so it is not shown either",
			Detail: c.protected}
	}
	return rel, nil
}

// leavesRoot reports whether a project path is a symlink (or passes through one) that
// leads outside the project root: such a path is refused outright, though the reads
// would not follow it anyway.
func leavesRoot(root, rel string) bool {
	abs := filepath.Join(root, filepath.FromSlash(rel))
	fi, err := os.Lstat(abs)
	if err != nil {
		parent := filepath.Dir(abs)
		if parent == abs || parent == root {
			return false
		}
		r, perr := filepath.Rel(root, parent)
		return perr == nil && filepath.IsLocal(r) && leavesRoot(root, filepath.ToSlash(r))
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		dir := filepath.Dir(abs)
		real, err := filepath.EvalSymlinks(dir)
		if err != nil {
			return false
		}
		r, err := filepath.Rel(resolved(root), real)
		return err != nil || !(r == "." || filepath.IsLocal(r))
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return true // dangling or a loop: nothing to show
	}
	r, err := filepath.Rel(resolved(root), real)
	return err != nil || !(r == "." || filepath.IsLocal(r))
}

// content reads a file at a point. exists is false when it did not exist then.
func content(sess *session.Session, p point, rel string) (data []byte, exists bool, err error) {
	switch {
	case p.live:
		data, exists, err = sess.Ckpt.CurrentContent(rel)
	case p.base:
		list := sess.Ckpt.Infos()
		if len(list) == 0 {
			data, exists, err = sess.Ckpt.CurrentContent(rel)
		} else {
			data, exists, err = sess.Ckpt.ContentAt(list[0].ID, rel)
		}
	default:
		data, exists, err = sess.Ckpt.ContentAt(p.cp, rel)
	}
	if errors.Is(err, checkpoint.ErrUnknownCheckpoint) {
		return nil, false, werr(http.StatusNotFound, "no_checkpoint", "there is no such checkpoint")
	}
	if errors.Is(err, checkpoint.ErrOutsideRoot) {
		return nil, false, errBadPath
	}
	return data, exists, err
}

// ---- secret carriers --------------------------------------------------------------------

// secretCarrier reports whether a file is one whose secret-shaped values are masked when
// it is served (CONTRACT.md 2.3): .env files, PEM files and SSH keys.
func secretCarrier(rel string) bool {
	base := strings.ToLower(path.Base(rel))
	return strings.HasPrefix(base, ".env") || strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key") ||
		strings.HasPrefix(base, "id_") || strings.HasSuffix(base, ".p12") || strings.HasSuffix(base, ".pfx")
}

// maskSecrets hides the values of a secret carrier that look like credentials: the body
// of a PEM block, and the value of a NAME=VALUE (or NAME: VALUE) line whose name or value
// looks secret.
func maskSecrets(text string) string {
	lines := strings.SplitAfter(text, "\n")
	inPEM := false
	for i, l := range lines {
		body := strings.TrimRight(l, "\r\n")
		end := l[len(body):]
		switch {
		case strings.HasPrefix(strings.TrimSpace(body), "-----BEGIN"):
			inPEM = true
			continue
		case strings.HasPrefix(strings.TrimSpace(body), "-----END"):
			inPEM = false
			continue
		case inPEM:
			if strings.TrimSpace(body) != "" {
				lines[i] = "(secret not shown)" + end
			}
			continue
		}
		sep := strings.IndexAny(body, "=:")
		if sep <= 0 {
			continue
		}
		name := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(body[:sep]), "export "))
		value := strings.Trim(strings.TrimSpace(body[sep+1:]), `"'`)
		if value != "" && harden.LooksSecret(name, value) {
			lines[i] = body[:sep+1] + " (set, not shown)" + end
		}
	}
	return strings.Join(lines, "")
}

// ---- the tree -----------------------------------------------------------------------------

// listFiles lists the project's files: in a git work tree the tracked and untracked files
// that are not ignored (git ls-files), else a walk of the root that never follows a
// symlink and skips .git. At most maxTree are listed; the second result says whether the
// list was cut.
func listFiles(ctx context.Context, root string) ([]string, bool) {
	if repo, err := gitx.Open(root); err == nil && !repo.IsBare() {
		if files, cut, ok := gitFiles(ctx, repo, root); ok {
			return files, cut
		}
	}
	var out []string
	cut := false
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			if d.Name() == ".git" && p != root {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		if len(out) >= maxTree {
			cut = true
			return filepath.SkipAll
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	sort.Strings(out)
	return out, cut
}

// gitFiles is listFiles for a git work tree; ok is false when git could not list them.
func gitFiles(ctx context.Context, repo *gitx.Repo, root string) ([]string, bool, bool) {
	sub, err := filepath.Rel(repo.Root(), resolved(root))
	if err != nil || !(sub == "." || filepath.IsLocal(sub)) {
		return nil, false, false
	}
	args := []string{"ls-files", "-z", "-c", "-o", "--exclude-standard", "--"}
	if sub != "." {
		args = append(args, ":(literal)"+filepath.ToSlash(sub)+"/")
	}
	res, err := repo.Git(ctx, args...)
	if err != nil || res == nil {
		return nil, false, false
	}
	prefix := ""
	if sub != "." {
		prefix = filepath.ToSlash(sub) + "/"
	}
	seen := map[string]bool{}
	var out []string
	cut := res.Truncated
	for _, f := range strings.Split(res.Stdout, "\x00") {
		if f == "" || !strings.HasPrefix(f, prefix) {
			continue
		}
		f = strings.TrimPrefix(f, prefix)
		if seen[f] {
			continue
		}
		if len(out) >= maxTree {
			cut = true
			break
		}
		seen[f] = true
		out = append(out, f)
	}
	sort.Strings(out)
	return out, cut, true
}

// resolved is a directory with its symlinks resolved (itself when that fails).
func resolved(dir string) string {
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		return r
	}
	return dir
}

// kindKey identifies a version of a file for the cache of kinds.
type kindKey struct {
	path  string
	size  int64
	mtime int64
}

// fileKind says what the tree shows a file as: "text", or "other" (binary content, a
// special file, a link), with its size; exists is false for a path with nothing there
// now. Whether a regular file is binary is read from its first 8000 bytes (git's
// heuristic), opened without following a symlink and without waiting on a FIFO, and
// remembered while its size and modification time stay the same.
func (s *service) fileKind(root, rel string) (kind string, size int64, exists bool) {
	abs := filepath.Join(root, filepath.FromSlash(rel))
	fi, err := os.Lstat(abs)
	if err != nil {
		return "text", 0, false
	}
	if !fi.Mode().IsRegular() {
		return "other", 0, true
	}
	key := kindKey{abs, fi.Size(), fi.ModTime().UnixNano()}
	s.mu.Lock()
	k, ok := s.kinds[key]
	s.mu.Unlock()
	if ok {
		return k, fi.Size(), true
	}
	k = "text"
	if f, err := openNoFollow(abs); err == nil {
		buf := make([]byte, 8000)
		n, _ := io.ReadFull(f, buf)
		f.Close()
		if checkpoint.IsBinary(buf[:n]) {
			k = "other"
		}
	}
	s.mu.Lock()
	if len(s.kinds) >= 4*maxTree {
		s.kinds = map[kindKey]string{}
	}
	s.kinds[key] = k
	s.mu.Unlock()
	return k, fi.Size(), true
}

// ---- permission classes -----------------------------------------------------------------

// class is what the permission engine says of a path, for the tree's markers.
type class struct {
	protected *wire.WsProtect
	ask       *wire.WsAsk
}

// classCache keeps the classes of paths while the session's rules and mode stay the same.
type classCache struct {
	key   string
	paths map[string]class
}

// rulesKey fingerprints what the classes of paths depend on.
func rulesKey(sess *session.Session) string {
	var b strings.Builder
	if sess.Perm != nil {
		b.WriteString(string(sess.Perm.Mode()))
		for _, r := range sess.Perm.RuleInfos() {
			b.WriteString("\x00" + string(r.Action) + r.Rule)
		}
	}
	return b.String()
}

// classOf classifies a project path for reading (a refusal is the protected marker) and
// for writing (a rule or protection that asks is the ask marker).
func (s *service) classOf(sess *session.Session, rel string) class {
	key := rulesKey(sess)
	s.mu.Lock()
	cc := s.classes[sess.Dir]
	if cc == nil || cc.key != key {
		cc = &classCache{key: key, paths: map[string]class{}}
		s.classes[sess.Dir] = cc
	}
	if c, ok := cc.paths[rel]; ok {
		s.mu.Unlock()
		return c
	}
	s.mu.Unlock()
	var c class
	if rc, err := sess.Classify("Read", rel); err == nil && rc.Verdict == perm.Deny {
		c.protected = &wire.WsProtect{Rule: rc.Rule, Origin: rc.Origin, Tier: tierName(rc), Why: rc.Why}
	}
	if wc, err := sess.Classify("Edit", rel); err == nil && (wc.Verdict == perm.Ask || wc.NoOneToAsk) && (wc.Rule != "" || wc.Origin == perm.OriginBuiltIn) {
		c.ask = &wire.WsAsk{Rule: wc.Rule, Why: wc.Why}
	}
	s.mu.Lock()
	if len(cc.paths) >= 4*maxTree {
		cc.paths = map[string]class{}
	}
	cc.paths[rel] = c
	s.mu.Unlock()
	return c
}

// tierName is the tier of a refusal as the page names it: "hard" and "guarded" for the
// built-in protections, "deny" for a deny rule.
func tierName(c perm.Classification) string {
	if c.Tier == perm.TierRule {
		return "deny"
	}
	return c.Tier
}
