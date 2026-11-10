package perm

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// A built-in skill's script is extracted under the state directory, whose write rule asks in every mode. An unlisted program's
// operand counts as a possible write, so `node <skill>/vulns.mjs` met that rule and a run with nobody to ask refused every built-in
// skill's script, even with Bash(node:*) allowed. Running or reading what the harness itself ships is no write to the state
// directory; a write to it, and every other word of the command, is still judged.
func TestAScriptTheHarnessShipsRunsWithoutTheStateDirectoryAsking(t *testing.T) {
	f := newFixture(t)
	ship := filepath.Join(f.home, ".sleipnir", "cache", "skills", "abc", "builtin")
	other := filepath.Join(f.home, ".sleipnir", "cache", "skills", "other.mjs")
	if err := os.MkdirAll(filepath.Join(ship, "vuln-check"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(ship, "vuln-check", "vulns.mjs")
	for _, p := range []string{script, other} {
		if err := os.WriteFile(p, []byte("// code\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(ship, "vuln-check", "link.mjs")
	if err := os.Symlink(filepath.Join(f.outside, "evil.mjs"), link); err != nil {
		t.Fatal(err)
	}
	guard := []string{"Edit(./.sleipnir/**)", "Edit(~/.sleipnir/**)"}
	base := Config{Mode: ModeAcceptEdits, Ask: guard, Allow: []string{"Bash(node:*)"}, ShippedDirs: []string{ship}}
	with := func(c Config, change func(*Config)) Config { change(&c); return c }
	bypass := with(base, func(c *Config) { c.Mode, c.Allow = ModeBypass, nil })
	noRule := with(base, func(c *Config) { c.Allow = nil })
	denied := with(base, func(c *Config) { c.Deny = []string{"Read(~/.sleipnir/**)"} })
	unlisted := with(base, func(c *Config) { c.ShippedDirs = nil })
	named := with(unlisted, func(c *Config) { c.Allow = []string{"Bash(node " + script + ":*)"} })

	for _, c := range []struct {
		name string
		cfg  Config
		q    rq
		want string
	}{
		{"its script, with an allow rule", base, rq{tool: "Bash", cmd: "node " + script + " id CVE-2021-44228"}, "allow"},
		{"its script, in bypass mode", bypass, rq{tool: "Bash", cmd: "node " + script + " id CVE-2021-44228"}, "allow"},
		{"another word of the command stays judged", base, rq{tool: "Bash", cmd: "node " + script + " deps .sleipnir/config.json"}, "ask"},
		{"without an allow rule the command itself still asks", noRule, rq{tool: "Bash", cmd: "node " + script + " id CVE-1"}, "ask"},
		{"a script beside the shipped tree", base, rq{tool: "Bash", cmd: "node " + other}, "ask"},
		{"a path that climbs out of the tree", base, rq{tool: "Bash", cmd: "node " + ship + "/../../other.mjs"}, "ask"},
		{"a link in the tree that leads out of it", base, rq{tool: "Bash", cmd: "node " + link}, "ask"},
		{"an inline program given the script as its argument", base, rq{tool: "Bash", cmd: "node -e 'x' " + script}, "ask"},
		{"an option before the script", base, rq{tool: "Bash", cmd: "node --require ./x.js " + script}, "ask"},
		{"a redirection onto the script", base, rq{tool: "Bash", cmd: "node " + script + " > " + script}, "ask"},
		{"copying over the script", base, rq{tool: "Bash", cmd: "cp a.txt " + script}, "ask"},
		{"editing the script with the file tool", base, rq{tool: "Edit", paths: []string{script}, writes: true}, "ask"},
		{"reading the script with the file tool", base, rq{tool: "Read", paths: []string{script}}, "allow"},
		{"searching the shipped tree", base, rq{tool: "Bash", cmd: "grep -n CWE " + script}, "allow"},
		{"a deny rule still wins", denied, rq{tool: "Bash", cmd: "node " + script + " id CVE-1"}, "deny"},
		{"a directory that is not declared shipped", unlisted, rq{tool: "Bash", cmd: "node " + script + " id CVE-1"}, "ask"},
		// An ask rule is judged before allow rules, so naming the script in one does not lift the state directory's question.
		{"an allow rule naming a script that is not shipped", named, rq{tool: "Bash", cmd: "node " + script + " id CVE-1"}, "ask"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := f.engine(t, c.cfg)
			d := e.Check(context.Background(), f.request(c.q))
			if got := outcome(d); got != c.want {
				t.Errorf("%s %s = %s (%s), want %s", c.q.tool, c.q.cmd, got, d.Reason, c.want)
			}
		})
	}
}
