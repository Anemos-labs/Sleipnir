package main

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/reee344/sleipnir/internal/session"
	"github.com/reee344/sleipnir/internal/shellparse"
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

const testsPresetSummary = "go, cargo, npm, pnpm, yarn, pytest, unittest, mvn, gradle, dotnet and make: test, build, check, lint and vet, never install or run"

// testsAllow is the preset --allow tests: the commands that build and test a project. They run the project's own code, as running
// its tests is meant to, and nothing else: no package installs, no downloads, no interpreter given a program of its own.
var testsAllow = []string{
	"Bash(go test:*)", "Bash(go build:*)", "Bash(go vet:*)", "Bash(gofmt:*)",
	"Bash(cargo test:*)", "Bash(cargo build:*)", "Bash(cargo check:*)", "Bash(cargo clippy:*)", "Bash(cargo fmt:*)",
	"Bash(npm test:*)", "Bash(npm run test:*)", "Bash(npm run build:*)", "Bash(npm run lint:*)", "Bash(pnpm test:*)", "Bash(yarn test:*)",
	"Bash(node --test:*)",
	"Bash(pytest:*)", "Bash(python -m pytest:*)", "Bash(python3 -m pytest:*)", "Bash(python -m unittest:*)", "Bash(python3 -m unittest:*)",
	"Bash(mvn test:*)", "Bash(mvn -q test:*)", "Bash(gradle test:*)", "Bash(./gradlew test:*)",
	"Bash(dotnet test:*)", "Bash(dotnet build:*)",
	"Bash(make test:*)", "Bash(make check:*)", "Bash(make build:*)", "Bash(make lint:*)", "Bash(ctest:*)",
}

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
		if subcommandTools[prog] && len(c.Args) > 0 && !strings.HasPrefix(c.Args[0], "-") {
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

// printRefusals says, at the end of a run that had nobody to ask, which commands were refused for it and how to let them through.
func printRefusals(w io.Writer, refused []session.RefusedCommand) {
	if len(refused) == 0 {
		return
	}
	fmt.Fprintln(w, "refused, because this run had no one to ask:")
	var rules []string
	seen := map[string]bool{}
	for _, r := range refused {
		cmd := strings.Join(strings.Fields(r.Command), " ")
		if len(cmd) > 100 {
			cmd = cmd[:99] + "…"
		}
		times := ""
		if r.Times > 1 {
			times = fmt.Sprintf(" (%d times)", r.Times)
		}
		fmt.Fprintf(w, "  %s%s\n", cmd, times)
		for _, rule := range suggestAllow(r.Command) {
			if !seen[rule] {
				seen[rule] = true
				rules = append(rules, rule)
			}
		}
	}
	if len(rules) > 0 {
		var flags []string
		for _, rule := range rules[:min(len(rules), 3)] {
			flags = append(flags, "--allow '"+rule+"'")
		}
		fmt.Fprintf(w, "to let them through next time: %s (or --allow %s for the build and test commands of most projects)\n", strings.Join(flags, " "), testsPreset)
	}
}
