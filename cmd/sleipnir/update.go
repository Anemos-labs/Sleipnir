package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/update"
)

func init() { extraCommands["update"] = cmdUpdate }

// updateOptions say where the last check of the releases is kept.
func updateOptions() update.Options {
	return update.Options{CachePath: filepath.Join(userHome(), ".sleipnir", "update.json"), Agent: "sleipnir/" + version}
}

// updateChecks reports whether this build looks for newer releases by itself: not one built from source, and not when
// SLEIPNIR_NO_UPDATE_CHECK is set (the only thing it sends is the request for the latest release of the repository to api.github.com).
func updateChecks() bool {
	return version != "dev" && os.Getenv("SLEIPNIR_NO_UPDATE_CHECK") == ""
}

// updateNotice is the line the chat says at its start when a newer release is out, from the last check and nothing else: it never waits.
func updateNotice() string {
	if !updateChecks() {
		return ""
	}
	st, ok := update.Cached(updateOptions())
	if !ok {
		return ""
	}
	return update.Notice(version, st)
}

// checkForUpdateInBackground looks for a newer release when the last look is more than a day old, for the start of the next chat to say.
func checkForUpdateInBackground() {
	if !updateChecks() {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		update.Refresh(ctx, updateOptions(), version, commit)
	}()
}

const updateHelp = `usage: sleipnir update [--check]

Installs the latest release over this binary: downloads the archive for this machine from the
GitHub release, checks it against the release's checksums.txt, and replaces the program. A chat
that is open keeps running the old one until it is restarted. --check only says whether a newer
release is out.

The chat says, at its start, when a newer release is out (it looks once a day, in the
background; SLEIPNIR_NO_UPDATE_CHECK=1 turns that off). A build from source is told how to
update itself instead.
`

func cmdUpdate(ctx context.Context, args []string) error {
	fs := newFlagSet("update", flag.ExitOnError)
	fs.Usage = func() { printHelp(os.Stdout, updateHelp) }
	check := fs.Bool("check", false, "only say whether a newer release is out")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return errors.New("update: takes no argument (--check only looks)")
	}
	exe, _ := os.Executable()
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	return runUpdate(ctx, os.Stdout, updateOptions(), version, commit, exe, *check)
}

// runUpdate is `sleipnir update`.
func runUpdate(ctx context.Context, out io.Writer, o update.Options, version, commit, exe string, checkOnly bool) error {
	update.CleanUp(exe)
	if version == "dev" || strings.Contains(filepath.ToSlash(exe), "/go-build") {
		fmt.Fprintln(out, "This build is from source, not from a release: update it with `go install github.com/anemos-labs/sleipnir/cmd/sleipnir@latest`, or `git pull` and `make build`.")
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	rel, st, err := update.Fetch(cctx, o, version, commit)
	if err != nil {
		return fmt.Errorf("update: could not look for the latest release: %w", err)
	}
	update.Remember(o, st) // what the chat says at its start is what this found
	if !update.Newer(version, rel.Tag) {
		fmt.Fprintf(out, "sleipnir %s is up to date (the latest release is %s).\n", version, rel.Tag)
		return nil
	}
	ahead := ""
	if st.Behind > 0 {
		ahead = fmt.Sprintf(", %d commits ahead", st.Behind)
	}
	fmt.Fprintf(out, "sleipnir %s -> %s%s\n", version, rel.Tag, ahead)
	if checkOnly {
		fmt.Fprintln(out, "Run `sleipnir update` to install it.")
		return nil
	}
	dctx, dcancel := context.WithTimeout(ctx, 5*time.Minute)
	defer dcancel()
	if err := update.Install(dctx, o, rel, exe); err != nil {
		return fmt.Errorf("update: %w", err)
	}
	fmt.Fprintf(out, "Updated %s to %s. A chat that is open keeps the old version until it is restarted. Release notes: %s\n", exe, rel.Tag, rel.URL)
	return nil
}
