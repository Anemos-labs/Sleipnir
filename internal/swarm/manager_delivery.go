package swarm

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/skills/mdfile"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// deliver commits only the verified integration snapshot into the person's
// checkout. Workers retain their confinement and explicit permission rules apply
// to both the affected files and the Git operation before the checkout changes.
func (t *taskTool) deliver(ctx context.Context, c *tools.Call, in taskIn) *tools.Result {
	s := t.s
	if c.Env.Role != "manager" || c.Env.Agent != s.ManagerID() {
		return tools.Errorf("only the manager can deliver verified work")
	}
	if !s.isolated() {
		return tools.Errorf("this session has no isolated integration; use bash to commit in your checkout")
	}
	m := s.get(s.ManagerID())
	if m == nil {
		return tools.Errorf("manager is not running")
	}
	if err := s.flushManager(ctx, m); err != nil {
		return tools.Errorf("delivery needs repair: %v", err)
	}
	preview, err := s.Accept(ctx, AcceptOptions{Commits: true, DryRun: true})
	if err != nil {
		return tools.Errorf("delivery preview: %v", err)
	}
	if !preview.CanCommit {
		return tools.Errorf("delivery cannot commit: %v", preview.CommitBlocked)
	}
	if !preview.Waiting {
		return text("verified work is already committed; use bash for push and PR commands")
	}
	root := s.deps.Isolation.Manager.Repository().Root()
	paths := make([]string, 0, len(preview.Files))
	for _, p := range preview.Files {
		paths = append(paths, filepath.Join(root, p))
	}
	message := strings.TrimSpace(in.Text)
	if message == "" {
		message = "Apply verified team work"
	}
	req := perm.Request{Agent: c.Env.Agent, Role: c.Env.Role, Tool: "task", Input: c.Input,
		Cwd: root, Paths: paths, Writes: true, VerifiedIntegration: true,
		Summary: "commit verified integration to " + root}
	if c.Env.Perm == nil {
		return tools.Errorf("delivery permission engine is unavailable")
	}
	if d := c.Env.Perm.Check(ctx, req); !d.Allow {
		return tools.Errorf("delivery denied: %s", d.Reason)
	}
	// Mirror the Git argument vectors, keeping the message on stdin. Path and
	// task rules were checked above; command rules see the actual operation.
	commands := [][]string{{"git", "merge", "--no-edit", "--no-verify", "--no-autostash", "--no-stat", "--no-log", "--no-squash", "--no-gpg-sign", "-m", "Merge " + preview.Tip, "--ff-only", "--end-of-options", preview.Tip}}
	if preview.CommitMode == "commit" {
		commands = [][]string{
			append([]string{"git", "add", "-A", "--"}, preview.Files...),
			append([]string{"git", "diff", "--cached", "--quiet", "--no-ext-diff", "--ignore-submodules=dirty", "--"}, preview.Files...),
			append([]string{"git", "commit", "--no-verify", "--no-gpg-sign", "--cleanup=whitespace", "-F", "-", "--only", "--"}, preview.Files...),
		}
	}
	req.Tool = "bash"
	for _, command := range commands {
		quoted := make([]string, len(command))
		for i, arg := range command {
			quoted[i] = mdfile.ShellQuote(arg)
		}
		req.Command = strings.Join(quoted, " ")
		if d := c.Env.Perm.Check(ctx, req); !d.Allow {
			return tools.Errorf("delivery Git operation denied: %s", d.Reason)
		}
	}
	report, err := s.Accept(ctx, AcceptOptions{Commits: true, Message: message, ExpectTip: preview.Tip, ExpectFrom: preview.From, ExpectCommitMode: preview.CommitMode})
	if err != nil {
		return tools.Errorf("delivery needs repair: %v", err)
	}
	b, err := json.Marshal(report)
	if err != nil {
		return tools.Errorf("delivery report: %v", err)
	}
	return c.Env.Finish(string(b), false)
}
