package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/anemos-labs/sleipnir/internal/acpserve"
	"github.com/anemos-labs/sleipnir/internal/sched"
)

const acpServeUsage = `usage: sleipnir acp serve [flags]

Serves Sleipnir as an ACP agent on stdin and stdout. One ACP session is one
conversation; each prompt starts a child 'sleipnir run --json --delegated'.
Progress, tools, usage and the final answer are sent as session/update
notifications. Sessions are kept under ~/.sleipnir/acp-sessions so a client can
list, load and resume them.

A session's folder must be inside one of the roots: the --root flags, or the
--cwd folder alone when only --cwd is given, or else your home directory. One
entry in a client's configuration therefore serves every project under home.

flags:
`

// cmdACPServe runs the ACP server until its client disconnects.
func cmdACPServe(ctx context.Context, args []string) error {
	fs := newFlagSet("acp serve", flag.ContinueOnError)
	fs.Usage = func() {
		printHelp(os.Stderr, acpServeUsage)
		fs.PrintDefaults()
	}
	cwd := fs.String("cwd", "", "the directory a session uses when the client names none, and the base of a relative cwd; given alone, also the only root")
	var roots stringList
	fs.Var(&roots, "root", "a directory a session's cwd must be inside, repeatable (default: the --cwd directory if given, else your home directory)")
	mode := fs.String("mode", acpserve.DefaultMode, "the most permissive mode a turn may use: plan | default | accept-edits | bypass | yolo; a client may ask for a stricter one")
	allow := allowFlags(fs)
	trust := fs.Bool("trust-project", false, "pass --trust-project to every turn: apply the project's security-sensitive config and read its AGENTS.md, skills, commands and agent definitions; only for repositories you trust")
	maxJobs := fs.Int("max-jobs", acpserve.DefaultMaxJobs, "turns running at once; a start past it is refused")
	model := fs.String("model", "", "model advertised and used when the client does not choose one (default: the configured model)")
	noMCP := fs.Bool("no-mcp", false, "ignore the MCP servers a client sends in session setup")
	rest, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return fmt.Errorf("acp serve: unexpected argument %q", rest[0])
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("acp serve: cannot find the sleipnir binary: %w", err)
	}
	here, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	rootDirs, dir := serveDirs(roots, *cwd, here, home)
	if len(rootDirs) == 0 {
		return fmt.Errorf("acp serve: no home directory to default to: pass --root DIR")
	}
	srv, err := acpserve.New(acpserve.Config{
		Exe: exe, Roots: rootDirs, Dir: dir, Mode: *mode, Allow: *allow,
		TrustProject: *trust, MaxJobs: *maxJobs, Version: version,
		Prepare: sched.PassKeys, Home: home, DefaultModel: *model, NoMCP: *noMCP,
	})
	if err != nil {
		return err
	}
	return srv.Serve(ctx, os.Stdin, os.Stdout)
}
