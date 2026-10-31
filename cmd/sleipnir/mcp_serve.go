package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/anemos-labs/sleipnir/internal/mcpserve"
	"github.com/anemos-labs/sleipnir/internal/sched"
)

const mcpServeUsage = `usage: sleipnir mcp serve [flags]

Serves Sleipnir over the Model Context Protocol on stdin and stdout, so that
another harness (Claude Code, Codex, an editor) can start goals with Sleipnir's
agents and teams and follow them. Each goal runs as 'sleipnir run --json'.
The flags are the limits of every run; a caller cannot widen them.

flags:
`

// cmdMCPServe runs the MCP server until its client disconnects.
func cmdMCPServe(ctx context.Context, args []string) error {
	fs := newFlagSet("mcp serve", flag.ContinueOnError)
	fs.Usage = func() {
		printHelp(os.Stderr, mcpServeUsage)
		fs.PrintDefaults()
	}
	cwd := fs.String("cwd", "", "the directory runs happen in; a run's cwd must be inside it (default: current)")
	mode := fs.String("mode", mcpserve.DefaultMode, "the most permissive mode a run may use: plan | default | accept-edits | bypass | yolo; a caller may ask for a stricter one")
	allow := allowFlags(fs)
	trust := fs.Bool("trust-project", false, "pass --trust-project to every run: apply the project's security-sensitive config and read its AGENTS.md, skills, commands and agent definitions; only for repositories you trust")
	maxJobs := fs.Int("max-jobs", mcpserve.DefaultMaxJobs, "runs at once; a start past it is refused")
	rest, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return fmt.Errorf("mcp serve: unexpected argument %q", rest[0])
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("mcp serve: cannot find the sleipnir binary: %w", err)
	}
	root := *cwd
	if root == "" {
		if root, err = os.Getwd(); err != nil {
			return err
		}
	}
	srv, err := mcpserve.New(mcpserve.Config{
		Exe: exe, Root: root, Mode: *mode, Allow: *allow, TrustProject: *trust, MaxJobs: *maxJobs,
		Version: version, Prepare: sched.PassKeys,
	})
	if err != nil {
		return err
	}
	return srv.Serve(ctx, os.Stdin, os.Stdout)
}
