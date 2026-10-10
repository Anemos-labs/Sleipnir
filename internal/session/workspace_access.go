package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/checkpoint"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/swarm"
	"github.com/anemos-labs/sleipnir/internal/workspace"
)

// What the web workspace (`sleipnir web`) reads of a session beside its exported fields:
// the isolated team's merge queue and trees, who last wrote a file, the change a pending
// file tool call asks for, what the permission engine would do with a request without
// asking, and the manual application of the team's verified work.

// MergeQueue is the isolated team's merge queue (nil without isolation).
func (s *Session) MergeQueue() *workspace.Queue {
	if s.Swarm == nil {
		return nil
	}
	_, q := s.Swarm.Workspace()
	return q
}

// Worktrees is the isolated team's worktree manager (nil without isolation).
func (s *Session) Worktrees() *workspace.Manager {
	if s.Swarm == nil {
		return nil
	}
	m, _ := s.Swarm.Workspace()
	return m
}

// LastWriter names the agent that last wrote path through a file tool in this session:
// the newest entry of the checkpoint store's write journal (it survives a resume), else
// the team's file tracker. path is relative to the project root or absolute.
func (s *Session) LastWriter(path string) (agent string, ok bool) {
	if s.Ckpt != nil {
		if a, _, ok := s.Ckpt.LastWriter(path); ok {
			return a, true
		}
	}
	if s.Swarm != nil {
		if f := s.Swarm.Files(); f != nil {
			abs := path
			if !filepath.IsAbs(abs) {
				abs = filepath.Join(s.opts.Root, filepath.FromSlash(path))
			}
			return f.LastWriter(filepath.Clean(abs))
		}
	}
	return "", false
}

// EnableWriteJournal turns on the checkpoint store's write journal for this session (checkpoint.Store.EnableJournal, with its
// default budget): the web workspace's per-line authorship needs it. A host that shows the session calls it when it starts the
// session; the terminal chat, run and swarm never do, and keep only the pre-images. Writes made before the call are reported
// with approximate authorship.
func (s *Session) EnableWriteJournal() {
	if s.Ckpt != nil {
		s.Ckpt.EnableJournal(0)
	}
}

// ErrNotIsolated is returned for an operation of an isolated team asked of a session
// whose workers share the checkout (or that has no team).
var ErrNotIsolated = swarm.ErrNotIsolated

// AcceptVerified commits the verified, merged result onto the person's branch now (a
// clean checkout and an unmoved branch are required), deferring the automatic apply at
// close for what it committed. With a clean checkout the branch moves to the integration
// tip (the agents' commits); when the checkout holds the result applied as uncommitted
// edits already, those files alone are committed as one commit with msg. Errors:
// ErrNotIsolated, swarm.ErrNothingToAccept, swarm.ErrCheckoutDirty, swarm.ErrBranchMoved,
// or a failure of the application itself.
func (s *Session) AcceptVerified(ctx context.Context, msg string) (commit string, files []string, err error) {
	rep, err := s.ApplyVerified(ctx, swarm.AcceptOptions{Commits: true, Message: msg})
	if rep != nil {
		files = rep.Files
		commit = rep.Commit
	}
	return commit, files, err
}

// ApplyVerified applies the isolated team's verified work to the person's checkout now
// (swarm.Swarm.Accept): as uncommitted edits, or as commits; DryRun reports what it would
// do and whether commits are possible.
func (s *Session) ApplyVerified(ctx context.Context, o swarm.AcceptOptions) (*swarm.AcceptReport, error) {
	if s.Swarm == nil || s.iso == nil {
		return nil, ErrNotIsolated
	}
	return s.Swarm.Accept(ctx, o)
}

// PendingChange is the change a file tool call that waits for approval would make:
// Path is the file it names (relative to the project root when inside it), Change the
// unified diff (at most 256 KiB, "[diff truncated]" past it), Added and Removed its lines.
type PendingChange struct {
	Path           string
	Change         string
	Added, Removed int
}

// maxPendingCurrent bounds the current text read to show a pending change.
const maxPendingCurrent = 1 << 20

// PendingChange renders what a write, edit or apply_patch call (tool and its JSON input,
// as the tool start carries them) would change in the project, against the file as it is
// now. It reads only regular text files inside the project root, without following a
// symlink; a file that cannot be read so is shown as new. ok is false for other tools and
// for an input it cannot read.
func (s *Session) PendingChange(tool string, input json.RawMessage) (PendingChange, bool) {
	root := s.opts.Root
	rel := func(p string) string {
		if filepath.IsAbs(p) {
			if r, err := filepath.Rel(root, p); err == nil && filepath.IsLocal(r) {
				return filepath.ToSlash(r)
			}
			return p
		}
		return filepath.ToSlash(filepath.Clean(p))
	}
	current := func(p string) (string, bool) {
		if s.Ckpt == nil {
			return "", false
		}
		b, ok, err := s.Ckpt.CurrentContent(p)
		if err != nil || !ok || len(b) > maxPendingCurrent || !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0 {
			return "", false
		}
		return string(b), true
	}
	c, ok := checkpoint.Propose(tool, input, current)
	if !ok {
		return PendingChange{}, false
	}
	return PendingChange{Path: rel(c.Path), Change: c.Unified, Added: c.Added, Removed: c.Removed}, true
}

// errBadTool is returned by Classify for a tool the tester does not know.
var errBadTool = errors.New("the tool is Bash, Edit or Read")

// IsBadTool reports whether err is Classify's refusal of an unknown tool.
func IsBadTool(err error) bool { return errors.Is(err, errBadTool) }

// Classify says what the permission engine would do, without asking anyone, if an agent
// of the session ran a command (tool "Bash", arg the command line, run in the project
// root), edited a file ("Edit") or read one ("Read"; arg a path relative to the project
// root or absolute). The origin of a configuration rule is refined to the flag or the
// built-in list it came from.
func (s *Session) Classify(tool, arg string) (perm.Classification, error) {
	if s.Perm == nil {
		return perm.Classification{}, errors.New("the session has no permission engine")
	}
	r := perm.Request{Summary: strings.TrimSpace(tool + " " + arg)}
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "bash":
		r.Tool, r.Command, r.Cwd, r.Writes = "bash", arg, s.opts.Root, true
	case "edit", "write":
		r.Tool, r.Writes, r.Paths = "edit", true, []string{s.absPath(arg)}
	case "read":
		r.Tool, r.Paths = "read", []string{s.absPath(arg)}
	default:
		return perm.Classification{}, errBadTool
	}
	c := s.Perm.Classify(r)
	c.Origin = s.ruleOrigin(c.Rule, c.Origin)
	return c, nil
}

// absPath makes a path the person typed absolute against the project root.
func (s *Session) absPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return s.opts.Root
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(s.opts.Root, filepath.FromSlash(p))
}

// RuleOrigin names where a rule of the session's engine came from, as the Permissions
// page shows it: "--allow flag", "built-in protection" (the configuration directories
// whose writes always ask), "configuration" (a rule of a configuration file, perm.OriginConfig),
// or the origin the engine gives a rule added while the session ran.
func (s *Session) RuleOrigin(rule string) string {
	if s.Perm == nil {
		return ""
	}
	for _, ri := range s.Perm.RuleInfos() {
		if ri.Rule == rule {
			return s.ruleOrigin(rule, ri.Origin)
		}
	}
	return ""
}

// ruleOrigin refines the engine's origin of a configuration rule.
func (s *Session) ruleOrigin(rule, origin string) string {
	if origin != perm.OriginConfig || rule == "" {
		return origin
	}
	canon := func(list []string) bool {
		for _, x := range list {
			if r, err := perm.ParseRule(perm.Allow, x); err == nil && r.String() == rule {
				return true
			}
		}
		return false
	}
	switch {
	case canon(s.opts.Allow):
		return "--allow flag"
	case canon(protectedConfigDirs):
		return perm.OriginBuiltIn
	}
	return origin
}
