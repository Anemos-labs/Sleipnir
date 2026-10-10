package update

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
)

// Run is `sleipnir update`: it says whether a newer release than version is out and, unless checkOnly, installs it over exe, writing
// what it did to out. A build from source (version "dev", or a binary of `go run`) is told how to update itself and nothing changes.
// The answer of the look is kept (Remember) for the notice at the start of the next chat.
func Run(ctx context.Context, out io.Writer, o Options, version, commit, exe string, checkOnly bool) error {
	CleanUp(exe)
	if FromSource(version, exe) {
		fmt.Fprintln(out, "This build is from source, not from a release: update it with `go install github.com/anemos-labs/sleipnir/cmd/sleipnir@latest`, or `git pull` and `make build`.")
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	rel, st, err := Fetch(cctx, o, version, commit)
	if err != nil {
		return fmt.Errorf("update: could not look for the latest release: %w", err)
	}
	Remember(o, st) // what the chat says at its start is what this found
	if !Newer(version, rel.Tag) {
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
	if err := Install(dctx, o, rel, exe); err != nil {
		return fmt.Errorf("update: %w", err)
	}
	fmt.Fprintf(out, "Updated %s to %s. A chat that is open keeps the old version until it is restarted. Release notes: %s\n", exe, rel.Tag, rel.URL)
	return nil
}

// FromSource reports whether a build cannot update itself from a release: one built from source (version "dev") or one that `go run`
// built (its executable is in Go's build cache).
func FromSource(version, exe string) bool {
	return version == "dev" || strings.Contains(filepath.ToSlash(exe), "/go-build")
}
