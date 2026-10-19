package session_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// writeUserConfig writes home's ~/.sleipnir/config.json.
func writeUserConfig(t *testing.T, home, body string) {
	t.Helper()
	p := filepath.Join(home, ".sleipnir", "config.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// specNames lists the tool names a session sends.
func specNames(s *session.Session) []string {
	var out []string
	for _, sp := range s.Specs {
		out = append(out, sp.Name)
	}
	return out
}

func TestToolPluginsThatAreOffSendNoTools(t *testing.T) {
	repo := newRepo(t)
	client, model := startMock(t, func(c *mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	o := opts(t, repo, client, model)
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	all := specNames(s)
	s.Close()
	for _, want := range []string{"read", "bash", "recall", "skill", "memory", "plan"} {
		if !slices.Contains(all, want) {
			t.Fatalf("standard profile lacks %s: %v", want, all)
		}
	}

	o = opts(t, repo, client, model)
	writeUserConfig(t, o.Home, `{"plugins":{"enabled":{"shell":false,"recall":false,"memory":false,"plan":false}}}`)
	s, err = session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got := specNames(s)
	for _, gone := range []string{"bash", "bash_output", "recall", "memory", "plan"} {
		if slices.Contains(got, gone) {
			t.Errorf("%s is still sent with its plugin off: %v", gone, got)
		}
	}
	if !slices.Contains(got, "read") {
		t.Errorf("files stay on: %v", got)
	}
	if s.Plugins().On("shell") {
		t.Error("shell should be off")
	}
}

func TestMinimalProfileIsASingleAgentWithoutSkillsOrInstructions(t *testing.T) {
	repo := newRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("Never touch vendor/.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	client, model := startMock(t, func(c *mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	o := opts(t, repo, client, model)
	o.Swarm, o.Workers, o.DefaultTeam = true, 8, true
	writeUserConfig(t, o.Home, `{"plugins":{"profile":"minimal"}}`)
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Swarm != nil {
		t.Error("the team plugin is off in minimal: a single agent")
	}
	if strings.Contains(s.Shared.Text(), "Never touch vendor") {
		t.Error("instruction files are a plugin that minimal leaves off")
	}
	if got := specNames(s); slices.Contains(got, "skill") || slices.Contains(got, "web_fetch") {
		t.Errorf("minimal sends %v", got)
	}
	if flags := strings.Join(s.StartFlags(), " "); !strings.Contains(flags, "--swarm 8") {
		t.Errorf("a restart still asks for the team: %s", flags)
	}
	if msg, off := s.SlashOff("/goal"); !off || !strings.Contains(msg, "plugins enable goal") {
		t.Errorf("/goal off = %v %q", off, msg)
	}
}

func TestRolePluginOffLeavesTheRoleOutOfTheTeam(t *testing.T) {
	repo := newRepo(t)
	client, model := startMock(t, func(c *mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	o := opts(t, repo, client, model)
	o.Swarm, o.Workers, o.DefaultTeam = true, 8, true
	writeUserConfig(t, o.Home, `{"plugins":{"enabled":{"role:devops":false,"auto-docs":false}}}`)
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, slot := range s.Team() {
		if slot.Role == "devops" {
			t.Fatalf("devops is in the team: %v", s.Team())
		}
	}
	if len(s.Team()) == 0 {
		t.Fatal("the other roles stay")
	}
}

func TestExternalPluginAddsSkillsCommandsAndHooksOnlyWhenOn(t *testing.T) {
	repo := newRepo(t)
	client, model := startMock(t, func(c *mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	o := opts(t, repo, client, model)
	dir := filepath.Join(o.Home, ".sleipnir", "plugins", "acme")
	files := map[string]string{
		"plugin.json":            `{"id":"acme","title":"Acme","description":"acme tools","version":"1.0.0"}`,
		"skills/deploy/SKILL.md": "---\nname: deploy\ndescription: deploy the acme app\n---\nRun make deploy.\n",
		"commands/ship.md":       "Ship it: $ARGUMENTS\n",
		"hooks.json":             `{"SessionStart":[{"hooks":[{"type":"command","command":"echo ${SLEIPNIR_PLUGIN_ROOT}"}]}]}`,
		".claude-plugin/ignored": "x",
	}
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(s.Shared.Text(), "deploy the acme app") {
		t.Error("an external plugin is off until it is turned on")
	}
	if st, ok := s.Plugins().Get("acme"); !ok || st.State != "off" {
		t.Errorf("acme = %+v", st)
	}
	s.Close()

	o = opts(t, repo, client, model)
	o.Home = filepath.Dir(filepath.Dir(filepath.Dir(dir)))
	writeUserConfig(t, o.Home, `{"plugins":{"enabled":{"acme":true}}}`)
	s, err = session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !strings.Contains(s.Shared.Text(), "deploy the acme app") {
		t.Error("the plugin's skill is listed once it is on")
	}
}
