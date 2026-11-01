package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/mcpserve"
	"github.com/anemos-labs/sleipnir/internal/sched"
)

const mcpServeUsage = `usage: sleipnir mcp serve [flags]

Serves Sleipnir over the Model Context Protocol on stdin and stdout, so that
another harness (Claude Code, Codex, an editor) can start goals with Sleipnir's
agents and teams and follow them. Each goal runs as 'sleipnir run --json'.
The flags are the limits of every run; a caller cannot widen them.

A caller names each run's folder (sleipnir_start's cwd), and may have a new
one made. It must be inside one of the roots: the --root flags, or the --cwd
directory alone when only --cwd is given, or else your home directory. So one
entry in a client's configuration serves every project under your home.

flags:
`

// cmdMCPServe runs the MCP server until its client disconnects.
func cmdMCPServe(ctx context.Context, args []string) error {
	fs := newFlagSet("mcp serve", flag.ContinueOnError)
	fs.Usage = func() {
		printHelp(os.Stderr, mcpServeUsage)
		fs.PrintDefaults()
	}
	cwd := fs.String("cwd", "", "the directory a run uses when the caller names none, and the base of a relative one; given alone, also the only root")
	var roots stringList
	fs.Var(&roots, "root", "a directory a run's folder must be inside, repeatable (default: the --cwd directory if given, else your home directory)")
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
	here, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	rootDirs, dir := serveDirs(roots, *cwd, here, home)
	if len(rootDirs) == 0 {
		return fmt.Errorf("mcp serve: no home directory to default to: pass --root DIR")
	}
	srv, err := mcpserve.New(mcpserve.Config{
		Exe: exe, Roots: rootDirs, Dir: dir, Mode: *mode, Allow: *allow, TrustProject: *trust, MaxJobs: *maxJobs,
		Version: version, Prepare: sched.PassKeys,
	})
	if err != nil {
		return err
	}
	return srv.Serve(ctx, os.Stdin, os.Stdout)
}

// serveDirs decides the roots and the default directory of `mcp serve`. With
// no --root, --cwd alone keeps runs inside that one directory (as before
// --root existed), and with neither the roots are the home directory. Without
// --cwd, the directory the server was started in is the default only when it
// is inside a root: a client that starts servers in / or in its own install
// folder gives none, and every call then names its folder.
func serveDirs(roots []string, cwd, here, home string) ([]string, string) {
	switch {
	case len(roots) > 0:
	case cwd != "":
		roots = []string{cwd}
	case home != "":
		roots = []string{home}
	default:
		return nil, ""
	}
	if cwd != "" || here == "" {
		return roots, cwd
	}
	real := here
	if r, err := filepath.EvalSymlinks(here); err == nil {
		real = r
	}
	resolved := make([]string, 0, len(roots))
	for _, r := range roots {
		if abs, err := filepath.Abs(r); err == nil {
			r = abs
		}
		if rr, err := filepath.EvalSymlinks(r); err == nil {
			r = rr
		}
		resolved = append(resolved, r)
	}
	if mcpserve.Inside(resolved, real) {
		return roots, here
	}
	return roots, ""
}

// stringList is a repeatable string flag.
type stringList []string

func (l *stringList) String() string { return strings.Join(*l, ",") }

func (l *stringList) Set(v string) error {
	if v == "" {
		return fmt.Errorf("empty value")
	}
	*l = append(*l, v)
	return nil
}
