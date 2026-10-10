// Package clispec holds the CLI spec of `sleipnir web`: every `sleipnir` command with its usage, summary, positionals and typed
// flags, the mode the page's runner runs it in (CONTRACT.md 18.2), the chat's slash commands and the exit codes. The page builds
// the runner's forms, the Tools catalogue and the palette's program entries from it (GET /api/cli).
//
// clispec.json is generated and checked in: `go generate ./internal/web/clispec` rebuilds it from docs/CLI.md (whose flag blocks are
// the binary's own -h output) and the chat's command tables (package specgen), and a test fails when the file differs from what the
// generator makes now, or when a command that `sleipnir --help` lists has no entry. scripts/gen-clispec.sh --check is the same test
// for scripts and CI.
package clispec

import (
	_ "embed"
	"encoding/json"
	"strings"
	"sync"

	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

//go:generate go run ./gen -o clispec.json

// raw is the generated spec, as served.
//
//go:embed clispec.json
var raw []byte

// JSON returns the spec as it is served: the generated file's bytes. The slice is shared: do not modify it.
func JSON() []byte { return raw }

var (
	parsed     wire.CLISpec
	parseErr   error
	parseOnce  sync.Once
	byPath     map[string]*wire.CLICommand
	byPathOnce sync.Once
)

// Spec returns the spec, decoded. The value is shared: do not modify it.
func Spec() (*wire.CLISpec, error) {
	parseOnce.Do(func() { parseErr = json.Unmarshal(raw, &parsed) })
	return &parsed, parseErr
}

// Lookup finds the command of a path (["sessions", "prune"]).
func Lookup(path []string) (*wire.CLICommand, bool) {
	byPathOnce.Do(func() {
		byPath = map[string]*wire.CLICommand{}
		s, err := Spec()
		if err != nil {
			return
		}
		for i := range s.Commands {
			byPath[strings.Join(s.Commands[i].Path, " ")] = &s.Commands[i]
		}
	})
	c, ok := byPath[strings.Join(path, " ")]
	return c, ok
}
