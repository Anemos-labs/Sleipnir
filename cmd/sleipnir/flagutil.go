package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
)

// trustProjectHelp is the help text of --trust-project on the commands that start a
// session: what the flag lets a repository do is the same for all of them.
const trustProjectHelp = "trust this project: apply its security-sensitive config (hooks, allow rules, providers, MCP servers) and read its AGENTS.md, skills, commands and agent definitions; only for repositories you trust"

// newFlagSet is flag.NewFlagSet whose help is the flag package's own, with the descriptions fitted to the terminal (printFlags).
func newFlagSet(name string, h flag.ErrorHandling) *flag.FlagSet {
	fs := flag.NewFlagSet(name, h)
	fs.Usage = func() {
		// The flag package calls this after it has printed what is wrong (a flag that does not exist) and for -h alike. Forty lines of flags
		// under a typo hide the one line that says so: they are for the person who asked.
		if !helpAsked(os.Args) {
			fmt.Fprintf(fs.Output(), "(sleipnir %s -h lists the flags)\n", name)
			return
		}
		fmt.Fprintf(fs.Output(), "Usage of %s:\n", name)
		printFlags(fs)
	}
	return fs
}

// usageError is the error for a command line that makes no sense: what is wrong, and where the flags are listed. The flags themselves are
// not printed with it (forty lines above the one that says what is wrong hid it).
func usageError(fs *flag.FlagSet, msg string) error {
	return fmt.Errorf("%s (sleipnir %s -h lists the flags)", msg, fs.Name())
}

// printFlags is fs.PrintDefaults with each description broken at spaces to the terminal's width when the output is a terminal (a flag's
// description is one line, up to 370 characters). Anywhere else, a pipe or the generator of docs/CLI.md, it is PrintDefaults as it was.
func printFlags(fs *flag.FlagSet) {
	out := fs.Output()
	if termWidth(out) <= 20 {
		fs.PrintDefaults()
		return
	}
	var b strings.Builder
	fs.SetOutput(&b)
	fs.PrintDefaults()
	fs.SetOutput(out)
	printHelp(out, strings.ReplaceAll(b.String(), "\n    \t", "\n        "))
}

// resumeFlags registers --resume and --continue on fs. The returned function
// gives the resume request for session.Options.Resume: "" for a new session, a
// session id or directory, or "latest" (the newest session of this project).
func resumeFlags(fs *flag.FlagSet) func() (string, error) {
	spec := fs.String("resume", "", "continue an earlier session, of one agent or of a team (its manager and its board): its id, its directory, or 'latest' (this project's newest)")
	cont := fs.Bool("continue", false, "continue this project's newest session (same as --resume latest)")
	return func() (string, error) {
		switch {
		case *spec != "" && *cont:
			return "", errors.New("--resume and --continue are alternatives; give one")
		case *cont:
			return "latest", nil
		}
		return *spec, nil
	}
}

// parseInterspersed parses args like the go flag package but lets flags follow
// positional arguments, the way git and docker do: `sleipnir swarm 8 "goal"
// --verify "make test"` must not swallow --verify into the goal. A lone "--"
// ends flag parsing; everything after it is positional. It returns the
// positional arguments in order.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return pos, nil
		}
		// fs.Parse stopped at the first non-flag argument, or consumed a "--".
		// Distinguish the two: after "--" the flag package drops the marker, so
		// look at what was actually given.
		consumed := len(args) - len(rest)
		if consumed > 0 && args[consumed-1] == "--" {
			return append(pos, rest...), nil
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

// oneOf returns an error unless v is one of the allowed values.
func oneOf(flagName, v string, allowed ...string) error {
	for _, a := range allowed {
		if v == a {
			return nil
		}
	}
	return fmt.Errorf("--%s must be one of %s, got %q", flagName, strings.Join(allowed, ", "), v)
}

// helpAsked reports whether the command line asks for help: -h, -help or --help as a word of its own.
func helpAsked(args []string) bool {
	for _, a := range args {
		if a == "-h" || a == "-help" || a == "--h" || a == "--help" {
			return true
		}
	}
	return false
}
