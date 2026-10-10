package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/anemos-labs/sleipnir/internal/update"
)

// init registers the binary updater with the CLI dispatcher.
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

// cmdUpdate parses check-only mode, resolves the running executable through symlinks when
// possible, and invokes release update handling.
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

// runUpdate is `sleipnir update` (update.Run).
func runUpdate(ctx context.Context, out io.Writer, o update.Options, version, commit, exe string, checkOnly bool) error {
	return update.Run(ctx, out, o, version, commit, exe, checkOnly)
}
