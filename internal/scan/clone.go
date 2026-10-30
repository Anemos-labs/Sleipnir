package scan

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/gitx"
)

// cloneTimeout bounds a clone.
const cloneTimeout = 15 * time.Minute

// scpLike is git's user@host:path form of an ssh address.
var scpLike = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[A-Za-z0-9._/~-]+$`)

// CheckURL accepts the address of a repository to clone for a scan: https, ssh, or git's user@host:path. Anything else (a local
// path, file://, git's ext:: and fd:: transports, an option) is refused, so that the address cannot make git run a program or
// read the machine's own files.
func CheckURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, "-") || strings.ContainsAny(raw, " \t\r\n\x00") {
		return fmt.Errorf("%q is not a repository address", raw)
	}
	if scpLike.MatchString(raw) {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || strings.Trim(u.Path, "/") == "" {
		return fmt.Errorf("%q is not a repository address: give an https:// or ssh:// URL, or user@host:path", raw)
	}
	switch u.Scheme {
	case "https", "ssh":
		return nil
	}
	return fmt.Errorf("%q: only https and ssh repositories are cloned", raw)
}

// CloneName is the folder name a clone of the repository gets: its owner and name, as owner-name.
func CloneName(raw string) string {
	p := strings.TrimSpace(raw)
	if scpLike.MatchString(p) {
		p = p[strings.Index(p, ":")+1:]
	} else if u, err := url.Parse(p); err == nil {
		p = u.Path
	}
	p = strings.TrimSuffix(strings.Trim(p, "/"), ".git")
	name, owner := path.Base(p), path.Base(path.Dir(p))
	if owner != "." && owner != "/" && owner != "" {
		name = owner + "-" + name
	}
	clean := strings.Map(func(r rune) rune {
		if r == '-' || r == '_' || r == '.' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			return r
		}
		return '-'
	}, name)
	clean = strings.Trim(clean, ".-")
	if clean == "" {
		return "repo"
	}
	return clean
}

// cloneConfig is set for the clone and kept in the clone's configuration: no hook runs, no fsmonitor program, symbolic links
// are checked out as plain files (so that no tool on the host follows one out of the project), and git speaks only https and
// ssh, submodules included.
var cloneConfig = []string{
	"core.hooksPath=" + os.DevNull,
	"core.fsmonitor=false",
	"core.symlinks=false",
	"protocol.allow=never",
	"protocol.https.allow=always",
	"protocol.ssh.allow=always",
}

// Clone clones the repository into dir for a scan, without its submodules and with cloneConfig. A dir that exists is refused.
func Clone(ctx context.Context, raw, dir string) error {
	return CloneProgress(ctx, raw, dir, nil)
}

// CloneProgress is Clone that hands each progress line git prints ("Receiving objects:  42% (420/1000), 1.20 MiB | 2.00 MiB/s")
// to progress as it comes, so that a caller can show the person how far a long clone is. A nil progress is Clone.
func CloneProgress(ctx context.Context, raw, dir string, progress func(line string)) error {
	if err := CheckURL(raw); err != nil {
		return err
	}
	if _, err := os.Lstat(dir); err == nil {
		return fmt.Errorf("%s already exists", dir)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, cloneTimeout)
	defer cancel()
	var args []string
	for _, c := range cloneConfig {
		args = append(args, "-c", c)
	}
	args = append(args, "clone", "--no-recurse-submodules")
	if progress != nil {
		args = append(args, "--progress") // git prints its progress only to a terminal unless asked
	}
	for _, c := range cloneConfig {
		args = append(args, "--config", c)
	}
	args = append(args, "--", strings.TrimSpace(raw), dir)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if progress != nil {
		cmd.Stderr = io.MultiWriter(&out, &progressLines{fn: progress})
	}
	if err := cmd.Run(); err != nil {
		os.RemoveAll(dir)
		msg := strings.TrimSpace(out.String())
		if len(msg) > 600 {
			msg = msg[len(msg)-600:]
		}
		if ctx.Err() != nil {
			return fmt.Errorf("the clone took longer than %s", cloneTimeout)
		}
		return fmt.Errorf("git clone: %v: %s", err, msg)
	}
	return nil
}

// progressLines splits what git writes on standard error into lines (git ends a progress update with a carriage return, a
// finished step with a newline) and hands each non-empty one to fn.
type progressLines struct {
	fn  func(string)
	buf []byte
}

func (p *progressLines) Write(b []byte) (int, error) {
	p.buf = append(p.buf, b...)
	for {
		i := bytes.IndexAny(p.buf, "\r\n")
		if i < 0 {
			break
		}
		if line := strings.TrimSpace(string(p.buf[:i])); line != "" {
			p.fn(line)
		}
		p.buf = p.buf[i+1:]
	}
	if len(p.buf) > 4096 { // a line that never ends is not progress
		p.buf = p.buf[:0]
	}
	return len(b), nil
}

// Start takes the inventory of the repository at HEAD (below focus, a slash path in it, when set), creates the scan in the
// store, and writes its brief and first report.
func Start(ctx context.Context, st *Store, repo *gitx.Repo, depth, focus string) (*Run, error) {
	return start(ctx, st, repo, depth, focus, "")
}

// start is Start, for a rerun of the scan prev when prev is set.
func start(ctx context.Context, st *Store, repo *gitx.Repo, depth, focus, prev string) (*Run, error) {
	if depth == "" {
		depth = DefaultDepth
	}
	if _, ok := DepthByID(depth); !ok {
		return nil, errDepth(depth)
	}
	head, err := repo.Head(ctx)
	if err != nil {
		return nil, fmt.Errorf("the repository has no commit to scan: %w", err)
	}
	inv, err := TakeInventory(ctx, repo, head, focus)
	if err != nil {
		return nil, err
	}
	r, err := st.create(head, depth, focus, inv, "", nil, prev)
	if err != nil {
		return nil, err
	}
	if err := WriteBrief(st.Root(), r); err != nil {
		return nil, err
	}
	if err := WriteReport(st.Root(), r); err != nil {
		return nil, errors.Join(errors.New("the scan was created but its first report was not written"), err)
	}
	return r, nil
}
