package perm

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// The harness's own directory holds what the person trusted, their settings and skills and every session's record. It is ~/.sleipnir
// unless SLEIPNIR_HOME says otherwise, and the session lists the first of those with the other places that steer a later session
// (Edit(~/.sleipnir/**)); Config.StateDir is the second, so that moving the directory does not move it out of the rule.
func TestAWriteToTheStateDirectoryAsksWhereverItIs(t *testing.T) {
	f := newFixture(t)
	state := filepath.Join(f.base, "elsewhere", "sleipnir-home")
	for _, c := range []tc{
		{name: "the ledger of trust", mode: ModeBypass, req: write(state + "/trust.json"), want: "ask", why: "Edit("},
		{name: "the settings", mode: ModeAcceptEdits, req: edit(state + "/config.json"), want: "ask"},
		{name: "a skill of the user's", mode: ModeBypass, req: write(state + "/skills/x/SKILL.md"), want: "ask"},
		{name: "a session's record", mode: ModeBypass, req: write(state + "/sessions/s1/events.jsonl"), want: "ask"},
		{name: "a shell redirection into it", mode: ModeBypass, req: bash("echo '{}' > " + state + "/trust.json"), want: "ask"},
		{name: "reading it is reading", req: read(state + "/trust.json"), want: "ask", why: "outside the workspace"},
		{name: "a neighbour with a similar name is not it", mode: ModeBypass, req: write(state + "-backup/x"), want: "allow"},
	} {
		c := c
		t.Run(c.name, func(t *testing.T) {
			e := f.engine(t, Config{Mode: c.mode, StateDir: state})
			d := e.Check(context.Background(), f.request(c.req))
			if got := outcome(d); got != c.want {
				t.Fatalf("%s %q %v: got %s (%q), want %s", c.req.tool, c.req.cmd, c.req.paths, got, d.Reason, c.want)
			}
			if c.why != "" && !strings.Contains(d.Reason, c.why) {
				t.Errorf("reason %q does not contain %q", d.Reason, c.why)
			}
		})
	}
	// without the setting the directory is any other directory outside the workspace
	e := f.engine(t, Config{Mode: ModeBypass})
	if d := e.Check(context.Background(), f.request(write(state+"/trust.json"))); !d.Allow {
		t.Errorf("a write outside the workspace in bypass mode, with no state directory named: %+v", d)
	}
}

func TestTheStateDirectoryWithGlobCharactersIsStillThatDirectory(t *testing.T) {
	f := newFixture(t)
	state := filepath.Join(f.base, "weird [dir] *name*")
	e := f.engine(t, Config{Mode: ModeBypass, StateDir: state})
	if d := e.Check(context.Background(), f.request(write(state+"/trust.json"))); outcome(d) != "ask" {
		t.Errorf("a state directory whose name has glob characters is not protected: %+v", d)
	}
	other := filepath.Join(f.base, "weird x dirx xnamex")
	if d := e.Check(context.Background(), f.request(write(other+"/x"))); !d.Allow {
		t.Errorf("a directory that the glob characters would match, if they were not escaped: %+v", d)
	}
}
