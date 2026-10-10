// Command gen writes the CLI spec of `sleipnir web` (internal/web/clispec/clispec.json) from docs/CLI.md and the command tables of
// cmd/sleipnir; `go generate ./internal/web/clispec` runs it.
//
//	go run ./internal/web/clispec/gen [-repo DIR] [-o FILE] [-check]
//
// -check writes nothing and exits 1 when FILE is not what the generator makes now (scripts/gen-clispec.sh --check).
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/anemos-labs/sleipnir/internal/web/clispec/specgen"
)

// main parses the flags and runs the generator.
func main() {
	repo := flag.String("repo", "", "the root of the checkout (default: found from the working directory)")
	out := flag.String("o", "", "the file to write (default: internal/web/clispec/clispec.json of the checkout)")
	check := flag.Bool("check", false, "write nothing; exit 1 when the file is out of date")
	flag.Parse()
	if err := run(*repo, *out, *check); err != nil {
		fmt.Fprintln(os.Stderr, "clispec:", err)
		os.Exit(1)
	}
}

// run generates the spec and writes it, or compares it with the file.
func run(repo, out string, check bool) error {
	if repo == "" {
		var err error
		if repo, err = findRepo(); err != nil {
			return err
		}
	}
	if out == "" {
		out = filepath.Join(repo, "internal", "web", "clispec", "clispec.json")
	}
	in, err := specgen.ReadInputs(repo)
	if err != nil {
		return err
	}
	spec, err := specgen.Generate(in)
	if err != nil {
		return err
	}
	if err := specgen.Check(spec, in.Sources); err != nil {
		return err
	}
	b, err := specgen.Encode(spec)
	if err != nil {
		return err
	}
	if check {
		have, err := os.ReadFile(out)
		if err != nil {
			return err
		}
		if !bytes.Equal(have, b) {
			return fmt.Errorf("%s is out of date: run go generate ./internal/web/clispec", out)
		}
		return nil
	}
	return os.WriteFile(out, b, 0o644)
}

// findRepo walks up from the working directory to the directory that holds go.mod.
func findRepo() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod above the working directory")
		}
		dir = parent
	}
}
