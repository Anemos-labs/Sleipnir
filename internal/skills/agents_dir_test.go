package skills

import (
	"strings"
	"testing"
)

// The shared .agents/skills directory (the Agent Skills convention other
// harnesses read) is found in the project and the home directory, after the
// harness-named directories, and only with the project trusted.
func TestDiscoverSharedAgentsDirectory(t *testing.T) {
	w := newWorld(t)
	w.proj(".agents", "shared", skillText("shared", "project agents"))
	w.proj(".agents", "x", skillText("x", "project agents x"))
	w.proj(".claude", "x", skillText("x", "project claude x"))
	w.user(".agents", "mine", skillText("mine", "user agents"))

	c, warns := w.discover(true)
	if got := names(c); got != "mine,shared,x" {
		t.Fatalf("skills = %s", got)
	}
	if s, _ := c.Get("shared"); s.Path != ".agents/skills/shared/SKILL.md" || s.Scope != ScopeProject {
		t.Errorf("shared = %+v", s)
	}
	if s, _ := c.Get("mine"); s.Path != "~/.agents/skills/mine/SKILL.md" || s.Scope != ScopeUser {
		t.Errorf("mine = %+v", s)
	}
	if s, _ := c.Get("x"); s.Description != "project claude x" {
		t.Errorf(".claude must win over .agents: %+v", s)
	}
	if len(warns) != 1 || !strings.HasPrefix(warns[0].Path, ".agents/skills/x") || !strings.Contains(warns[0].Msg, "shadowed by") {
		t.Fatalf("warnings:\n%s", warnText(warns))
	}

	c, warns = w.discover(false)
	if got := names(c); got != "mine" {
		t.Fatalf("untrusted skills = %s", got)
	}
	if strings.Contains(warnText(warns), "shared") {
		t.Errorf("an untrusted project's skill names must not be read: %s", warnText(warns))
	}
}
