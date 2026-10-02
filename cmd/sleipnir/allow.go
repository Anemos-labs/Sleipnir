package main

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/shellparse"
)

// allowFlag is --allow: rules that need no question in this run. A run that nobody is there to answer (a script, CI, a cron job)
// refuses everything that would have asked, and this is how its owner says in advance what may go ahead.
type allowFlag []string

func (a *allowFlag) String() string { return strings.Join(*a, ", ") }

func (a *allowFlag) Set(v string) error {
	if strings.TrimSpace(v) == "" {
		return fmt.Errorf("--allow wants a rule such as 'Bash(go test:*)', or the name of a set of them (tests)")
	}
	*a = append(*a, v)
	return nil
}

// allowFlags registers --allow on fs.
func allowFlags(fs *flag.FlagSet) *allowFlag {
	a := &allowFlag{}
	fs.Var(a, "allow", "a permission rule that needs no question in this run, repeatable: 'Bash(go test:*)', 'Edit(docs/**)'; the name tests stands for the build and test commands of most projects ("+testsPresetSummary+"); more in docs/CONFIGURATION.md")
	return a
}

const testsPreset = "tests"

const testsPresetSummary = "go, cargo, npm, pnpm, yarn, pytest, unittest, mvn, gradle, dotnet and make: test, build, check, lint and vet, and go mod init and tidy, never install or run"

// testsAllow is the preset --allow tests: the commands that build and test a project (the list is in package perm, where the third answer of a
// question about such a command uses it too).
var testsAllow = perm.TestsAllow

// expandAllow turns the names of sets of rules into the rules; anything else is a rule as it is written.
func expandAllow(in []string) []string {
	var out []string
	for _, r := range in {
		if strings.TrimSpace(r) == testsPreset {
			out = append(out, testsAllow...)
			continue
		}
		out = append(out, r)
	}
	return out
}

// subcommandTools are the programs whose first argument says what they do, so that a rule for them names it: Bash(go test:*) and not
// Bash(go:*), which would let go run anything.
var subcommandTools = map[string]bool{
	"go": true, "cargo": true, "npm": true, "pnpm": true, "yarn": true, "git": true, "make": true, "dotnet": true,
	"mvn": true, "gradle": true, "pip": true, "pip3": true, "docker": true, "kubectl": true, "gh": true,
}

// harmless are programs that no rule needs to name: the engine lets them through, or they only move about and print.
var harmless = map[string]bool{
	"cd": true, "echo": true, "printf": true, "true": true, "false": true, "exit": true, "test": true, "[": true, "pwd": true,
	"cat": true, "ls": true, "head": true, "tail": true, "wc": true, "grep": true, "rg": true, "sort": true, "uniq": true,
	"diff": true, "cut": true, "tr": true, "which": true, "date": true, "basename": true, "dirname": true,
}

// interpreters run any program they are given, and neverSuggested are the programs whose blanket rule would be as bad (or a rule for
// which is a decision to make by hand): suggestAllow names no rule for them, but for an interpreter run on a script, that script.
var interpreters = map[string]bool{"python": true, "python3": true, "node": true, "ruby": true, "perl": true, "php": true, "bash": true, "sh": true, "zsh": true}
var neverSuggested = map[string]bool{"sudo": true, "doas": true, "rm": true, "curl": true, "wget": true, "ssh": true, "scp": true, "xargs": true, "eval": true, "dd": true, "chmod": true, "chown": true}

// suggestAllow is the rules that would let a refused command run: one for each program in it that is not harmless, naming the
// subcommand of the tools that have them (go test, cargo build). A command that cannot be read gets none.
func suggestAllow(command string) []string {
	an := shellparse.Parse(command)
	var out []string
	seen := map[string]bool{}
	for _, c := range an.Commands {
		prog := shellparse.ProgramName(c.Program)
		if prog == "" || harmless[prog] {
			continue
		}
		pattern := prog
		if interpreters[prog] {
			// a rule for python alone is a rule to run anything: only a script that is a plain file name, as the rule for that script
			if len(c.Args) == 0 || strings.HasPrefix(c.Args[0], "-") || strings.ContainsAny(c.Args[0], "$`\\\"' ") {
				continue
			}
			pattern += " " + c.Args[0]
		} else if neverSuggested[prog] {
			continue
		} else if subcommandTools[prog] && len(c.Args) > 0 && !strings.HasPrefix(c.Args[0], "-") {
			pattern += " " + c.Args[0]
		}
		rule := "Bash(" + pattern + ":*)"
		if !seen[rule] {
			seen[rule] = true
			out = append(out, rule)
		}
	}
	return out
}

// noOneToAskNote is what a run says first when nobody is there to answer and nothing lets the usual work through: in the default mode every edit
// and every command that is not read-only is refused, and the model found that out by being refused, a step at a time, with the way out at the end.
func noOneToAskNote(mode perm.Mode, allow []string) string {
	if len(allow) > 0 {
		return ""
	}
	switch mode {
	case perm.ModeDefault, "":
		return "no terminal to ask on: edits and commands that need an answer are refused. --mode accept-edits --allow " + testsPreset + " lets the usual ones through."
	case perm.ModeAcceptEdits:
		return "no terminal to ask on: commands that need an answer are refused. --allow " + testsPreset + " lets the build and test commands through."
	}
	return ""
}

// printRefusals says, at the end of a run that had nobody to ask, which commands and edits were refused for it and how to let them through:
// the mode that accepts edits for an edit, a rule for a command. Paths are shown as they are from cwd.
func printRefusals(w io.Writer, refused []session.RefusedCommand, cwd string) {
	if len(refused) == 0 {
		return
	}
	fmt.Fprintln(w, "refused, because this run had no one to ask:")
	var rules []string
	seen := map[string]bool{}
	edits := false
	for _, r := range refused {
		what := ""
		if r.Path != "" {
			edits = true
			what = "edit " + relativeTo(r.Path, cwd)
		} else {
			what = strings.Join(strings.Fields(r.Command), " ")
			if len(what) > 100 {
				what = what[:99] + "…"
			}
			for _, rule := range suggestAllow(r.Command) {
				if !seen[rule] {
					seen[rule] = true
					rules = append(rules, rule)
				}
			}
		}
		times := ""
		if r.Times > 1 {
			times = fmt.Sprintf(" (%d times)", r.Times)
		}
		fmt.Fprintf(w, "  %s%s\n", what, times)
	}
	var flags []string
	if edits {
		flags = append(flags, "--mode accept-edits")
	}
	for _, rule := range rules[:min(len(rules), 3)] {
		flags = append(flags, "--allow '"+rule+"'")
	}
	if len(flags) > 0 {
		tail := ""
		if len(rules) > 0 {
			tail = fmt.Sprintf(" (or --allow %s for the build and test commands of most projects)", testsPreset)
		}
		fmt.Fprintln(w, wrapFor(w, "to let them through next time: "+strings.Join(flags, " ")+tail))
	}
}

// relativeTo is path as it reads from dir when it lies under it, and with ~ for the home directory otherwise.
func relativeTo(path, dir string) string {
	if dir != "" {
		if rel, err := filepath.Rel(dir, path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return rel
		}
	}
	return tildePath(path)
}
