package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/trust"
)

// init registers project trust management with the CLI dispatcher.
func init() { extraCommands["trust"] = cmdTrust }

const trustUsage = `usage: sleipnir trust [command] [flags]

A project can say things to the harness of its own: instruction files (AGENTS.md), settings, tool
servers, skills, commands and agents. None of it is used until you trust the project, for one run
with --trust-project or, with this command, until any of those files changes: the answer is kept
for exactly the files you saw (a hash of them), in your own state directory, never in the project.
A chat asks at its start. A run, a swarm and a rollout have no one to ask, and use the answer.

commands:
  (none)             what in this directory's project trust would unlock, and whether you said yes to exactly that
  add [--yes]        say yes to those files as they are now (shows them and asks first)
  forget [--all]     forget the answer for this directory, or for every project
  list               the directories you said yes to, and whether the files in each still are the ones you saw

flags:
  --cwd DIR          the directory to look at (default: the current one)
  --yes              add: do not ask (a script: you have read what it shows)
  --all              forget: every project

What the answer covers is what the harness itself reads as text and settings; the repository's code is
not covered, and running it (go test, make, a hook's script) is what every approval is about.
`

// cmdTrust shows and keeps the answers to "may this project's own files be used": internal/trust.
func cmdTrust(ctx context.Context, args []string) error {
	sub, rest := "", args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, rest = args[0], args[1:]
	}
	if sub == "help" {
		printHelp(os.Stderr, trustUsage)
		return nil
	}
	fs := newFlagSet("trust", flag.ContinueOnError)
	fs.Usage = func() { printHelp(os.Stderr, trustUsage) }
	cwd := fs.String("cwd", "", "the directory to look at (default: the current one)")
	yes := fs.Bool("yes", false, "add: do not ask")
	all := fs.Bool("all", false, "forget: every project")
	extra, err := parseInterspersed(fs, rest)
	if err != nil {
		return err
	}
	if len(extra) > 0 {
		return fmt.Errorf("trust: unexpected argument %q", extra[0])
	}
	dir := *cwd
	if dir == "" {
		if dir, err = os.Getwd(); err != nil {
			return err
		}
	}
	if dir, err = filepath.Abs(dir); err != nil {
		return err
	}
	home, _ := os.UserHomeDir()
	ledger := trust.OpenLedger(session.TrustLedgerPath(home))
	switch sub {
	case "":
		return trustShow(os.Stdout, ledger, home, dir)
	case "add":
		return trustAdd(os.Stdin, os.Stdout, ledger, home, dir, *yes)
	case "forget":
		return trustForget(os.Stdout, ledger, dir, *all)
	case "list":
		return trustList(os.Stdout, ledger, home)
	}
	printHelp(os.Stderr, trustUsage)
	return fmt.Errorf("trust: unknown command %q", sub)
}

// projectFootprint reads what the project around dir would let the harness use.
func projectFootprint(home, dir string) (*trust.Footprint, error) {
	root, _ := config.FindRoot(dir)
	if root == "" {
		root = dir
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	return trust.Scan(root, dir, home)
}

// shortDigest removes an optional sha256: prefix and retains at most the first 12 bytes for
// display.
func shortDigest(d string) string {
	d = strings.TrimPrefix(d, "sha256:")
	if len(d) > 12 {
		d = d[:12]
	}
	return d
}

func trustShow(w io.Writer, ledger *trust.Ledger, home, dir string) error {
	fp, err := projectFootprint(home, dir)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "%s\n", trust.Show(dir))
	if fp.Empty() && !fp.Partial {
		fmt.Fprintln(w, "  nothing here that trust would unlock: no instruction files, settings, tool servers, skills, commands or agents")
		return nil
	}
	for _, l := range strings.Split(fp.Describe(), "\n") {
		fmt.Fprintln(w, "  "+l)
	}
	fmt.Fprintf(w, "  digest %s\n", shortDigest(fp.Digest))
	state, entry := ledger.Check(dir, fp)
	switch state {
	case trust.Trusted:
		fmt.Fprintln(w, "\n"+wrapFor(w, fmt.Sprintf("trusted since %s, for exactly these files: a session started here uses them without asking (`sleipnir trust forget` ends it)", entry.Saved)))
	case trust.Changed:
		fmt.Fprintln(w, "\n"+wrapFor(w, fmt.Sprintf("you trusted this directory on %s, and these files are not the ones you saw: %s", entry.Saved, trust.DescribeChanges(trust.Changes(entry, fp)))))
		fmt.Fprintln(w, wrapFor(w, "They are not used until you say yes again (`sleipnir trust add`, or the question at the start of a chat)."))
	default:
		fmt.Fprintln(w, "\n"+wrapFor(w, "not trusted: a chat asks about these at its start, and a run leaves them out unless you pass --trust-project or run `sleipnir trust add`"))
	}
	return nil
}

func trustAdd(in io.Reader, w io.Writer, ledger *trust.Ledger, home, dir string, yes bool) error {
	fp, err := projectFootprint(home, dir)
	if err != nil {
		return err
	}
	if fp.Empty() && !fp.Partial {
		fmt.Fprintf(w, "%s\n  nothing here that trust would unlock\n", trust.Show(dir))
		return nil
	}
	fmt.Fprintf(w, "%s\n", trust.Show(dir))
	for _, l := range strings.Split(fp.Describe(), "\n") {
		fmt.Fprintln(w, "  "+l)
	}
	if fp.Partial {
		return fmt.Errorf("trust: %w", trust.ErrNotRememberable)
	}
	if !yes {
		fmt.Fprint(w, "use exactly these files, in a session started here, until any of them changes? [y/N] ")
		line, _ := bufio.NewReader(in).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			return errors.New("not trusted")
		}
	}
	if err := ledger.Remember(dir, fp, time.Now()); err != nil {
		return err
	}
	fmt.Fprintf(w, "trusted (digest %s): the answer ends when any of these files changes\n", shortDigest(fp.Digest))
	return nil
}

// trustForget removes one or all saved project approvals and reports the change, propagating
// ledger failures.
func trustForget(w io.Writer, ledger *trust.Ledger, dir string, all bool) error {
	if all {
		n, err := ledger.ForgetAll()
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "forgot %d %s\n", n, plural(n, "project", "projects"))
		return nil
	}
	had, err := ledger.Forget(dir)
	if err != nil {
		return err
	}
	if !had {
		fmt.Fprintf(w, "nothing was remembered for %s\n", trust.Show(dir))
		return nil
	}
	fmt.Fprintf(w, "forgot %s: its files are not used until you say yes again\n", trust.Show(dir))
	return nil
}

func trustList(w io.Writer, ledger *trust.Ledger, home string) error {
	all := ledger.All()
	if len(all) == 0 {
		fmt.Fprintln(w, "no project is trusted (`sleipnir trust add` in a project, or the question at the start of a chat)")
		return nil
	}
	dirs := make([]string, 0, len(all))
	for d := range all {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "DIRECTORY\tSAVED\tFILES\tNOW")
	for _, d := range dirs {
		e := all[d]
		now := "unchanged"
		switch fi, err := os.Stat(d); {
		case err != nil || !fi.IsDir():
			now = "directory is gone"
		default:
			fp, err := projectFootprint(home, d)
			switch {
			case err != nil:
				now = "cannot be read: " + err.Error()
			default:
				if st, _ := ledger.Check(d, fp); st != trust.Trusted {
					now = "changed: " + trust.DescribeChanges(trust.Changes(e, fp))
				}
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n", trust.Show(d), e.Saved, len(e.Files), now)
	}
	return tw.Flush()
}

// plural selects the singular form only for a count of one.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
