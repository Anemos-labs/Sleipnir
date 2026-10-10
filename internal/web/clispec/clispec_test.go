package clispec

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/web/clispec/specgen"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// repo is the root of the checkout, from this package's directory.
var repo = filepath.Join("..", "..", "..")

// The checked-in spec is what the generator makes from the checkout now: a flag added, renamed or retyped in a command's -h (and so
// in docs/CLI.md, which scripts/gen-cli-docs.sh --check keeps equal to -h) fails here until the spec is generated again.
func TestCLISpecDrift(t *testing.T) {
	in, err := specgen.ReadInputs(repo)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := specgen.Generate(in)
	if err != nil {
		t.Fatal(err)
	}
	want, err := specgen.Encode(spec)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(JSON(), want) {
		t.Fatalf("internal/web/clispec/clispec.json is out of date: run go generate ./internal/web/clispec")
	}
}

// Every command that `sleipnir --help` lists has an entry, and every entry has one of the modes of the runner.
func TestEveryHelpCommandHasAnEntryWithAMode(t *testing.T) {
	in, err := specgen.ReadInputs(repo)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Spec()
	if err != nil {
		t.Fatal(err)
	}
	if err := specgen.Check(s, in.Sources); err != nil {
		t.Fatal(err)
	}
	modes := map[string]bool{specgen.ModeRun: true, specgen.ModeNet: true, specgen.ModePriv: true, specgen.ModeServer: true, specgen.ModeTTYOnly: true}
	for _, c := range s.Commands {
		if !modes[c.Mode] {
			t.Errorf("%v: mode %q", c.Path, c.Mode)
		}
		if c.Mode == specgen.ModeTTYOnly && c.Why == "" {
			t.Errorf("%v: a refused command must say what to use instead", c.Path)
		}
		for _, r := range c.When {
			if !modes[r.Mode] {
				t.Errorf("%v: rule %+v", c.Path, r)
			}
			found := false
			for _, f := range c.Flags {
				found = found || f.Name == r.Flag
			}
			if !found {
				t.Errorf("%v: the rule names --%s, which the command does not have", c.Path, r.Flag)
			}
		}
	}
}

// The spec carries the mode each command runs in: terminal-only, plain, network, privileged and server.
func TestModesAreTheOnesTheSpecCarries(t *testing.T) {
	for path, want := range map[string]string{
		"chat": "tty_only", "watch": "tty_only", "login": "tty_only", "web": "tty_only", "inspect": "tty_only", "replay": "tty_only",
		"config": "run", "sessions": "run", "sessions prune": "run", "recon": "run", "sim": "run", "friction": "run", "version": "run",
		"models": "net", "doctor": "net", "run": "net", "swarm": "net", "demo": "net", "rl eval": "net", "rl rollout": "net",
		"init": "priv", "logout": "priv", "trust add": "priv", "trust forget": "priv", "mcp approve": "priv", "mcp revoke": "priv",
		"schedule add": "priv", "schedule rm": "priv", "models fav add": "priv", "models fav rm": "priv", "update": "priv",
		"mock": "server", "rl serve": "server", "daemon": "server",
	} {
		c, ok := Lookup(strings.Fields(path))
		if !ok {
			t.Errorf("%s: no entry", path)
			continue
		}
		if c.Mode != want {
			t.Errorf("%s: mode %s, want %s", path, c.Mode, want)
		}
	}
	for path, flag := range map[string]string{"update": "check", "mcp list": "cwd", "mcp test": "cwd", "sessions prune": "yes", "inspect": "json"} {
		c, _ := Lookup(strings.Fields(path))
		found := false
		for _, f := range c.Flags {
			found = found || f.Name == flag
		}
		if !found {
			t.Errorf("%s has no --%s", path, flag)
		}
	}
	if c, _ := Lookup([]string{"mcp", "test"}); len(c.Positional) != 1 || !c.Positional[0].Variadic {
		t.Errorf("mcp test takes several names: %+v", c.Positional)
	}
}

// The served file decodes into the wire type the page reads, field for field (no field of the file is unknown to it).
func TestTheSpecDecodesStrictly(t *testing.T) {
	dec := json.NewDecoder(bytes.NewReader(JSON()))
	dec.DisallowUnknownFields()
	var s wire.CLISpec
	if err := dec.Decode(&s); err != nil {
		t.Fatal(err)
	}
	if len(s.Commands) < 60 || len(s.ChatSlash) < 30 {
		t.Fatalf("%d commands, %d slash commands", len(s.Commands), len(s.ChatSlash))
	}
}

// The generator fails, rather than guesses, when the document gains a command it knows nothing about.
func TestGeneratorRefusesAnUnknownCommand(t *testing.T) {
	in, err := specgen.ReadInputs(repo)
	if err != nil {
		t.Fatal(err)
	}
	in.CLIDoc = append(append([]byte{}, in.CLIDoc...), []byte("\n### `sleipnir frobnicate`\n\nDoes things.\n\n<!-- flags: frobnicate -->\n```text\nUsage of frobnicate:\n  -x\tan x\n```\n<!-- /flags -->\n")...)
	if _, err := specgen.Generate(in); err == nil || !strings.Contains(err.Error(), "frobnicate") {
		t.Fatalf("an uncurated command: %v", err)
	}
}
