package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// What the chat program is given of a session (chat_tty.go): the commands it completes, the host that runs its turns and commands,
// and what it says of the session in its banner.

// The palette of the program and the help of the line chat are two lists of the same commands, kept together by this test.
func TestChatHelpListsEveryCommandOfThePalette(t *testing.T) {
	for _, c := range chatCommands {
		if !strings.Contains(chatHelp, "/"+c.name) {
			t.Errorf("/%s is in the palette and not in /help", c.name)
		}
	}
	for _, line := range strings.Split(chatHelp, "\n") {
		if !strings.HasPrefix(line, "/") {
			continue
		}
		name := strings.TrimPrefix(strings.Fields(line)[0], "/")
		found := false
		for _, c := range chatCommands {
			found = found || c.name == name
		}
		if !found {
			t.Errorf("/%s is in /help and not in the palette", name)
		}
	}
	seen := map[string]bool{}
	for _, c := range chatCommands {
		if seen[c.name] || c.name == "" || c.desc == "" {
			t.Errorf("the palette entry %+v is empty or repeated", c)
		}
		seen[c.name] = true
	}
}

// /help is for a terminal of 80 columns, the size most windows open at: a line that is longer is broken with its rest at the left edge, under
// the names, and a description that starts a column early is the one that was typed wrong.
func TestChatHelpFitsEightyColumnsAndLinesUp(t *testing.T) {
	for _, line := range strings.Split(chatHelp, "\n") {
		if n := utf8.RuneCountInString(line); n > 79 {
			t.Errorf("%d characters: %q", n, line)
		}
		if strings.HasPrefix(line, "/") && len(line) > 19 && (line[18] != ' ' || line[19] == ' ') {
			t.Errorf("the description does not start in column 19: %q", line)
		}
	}
}

// Every command of the palette is one the chat answers, and none of them is "unknown".
func TestEveryCommandOfThePaletteIsAnswered(t *testing.T) {
	s := chatSession(t, false, nil)
	for _, c := range chatCommands {
		if c.name == "exit" || c.name == "compact" { // /exit ends the chat; /compact needs a thread
			continue
		}
		var out strings.Builder
		if _, ok := (&sessionHost{s: s}).programCommand("/"+c.name, &out); ok {
			continue // a command of the terminal program, answered by the host (restart.go): /swarm alone says its usage
		}
		slashTo(context.Background(), s, "/"+c.name, &out, &out)
		if strings.Contains(out.String(), "unknown command") {
			t.Errorf("/%s is in the palette and the chat does not know it: %q", c.name, out.String())
		}
	}
}

func TestChatSlashCommandsAreTheChatsOwnThenTheCustomOnesAndSkills(t *testing.T) {
	s := chatSession(t, true, map[string]string{
		".sleipnir/commands/fix.md":          "---\ndescription: fix an issue\nargument-hint: <n>\n---\nFix issue $1.\n",
		"~/.sleipnir/skills/deploy/SKILL.md": "---\nname: deploy\ndescription: deploy the service\ndisable-model-invocation: true\n---\nRun the checklist.\n",
	})
	got := chatSlashCommands(s)
	if len(got) < len(chatCommands)+2 {
		t.Fatalf("%d commands: %+v", len(got), got)
	}
	for i, c := range chatCommands {
		if got[i].Name != c.name || got[i].Description != c.desc || got[i].Args != c.args {
			t.Errorf("command %d is %+v, want the chat's own %+v first", i, got[i], c)
		}
	}
	byName := map[string]string{}
	for _, c := range got {
		byName[c.Name] = c.Description
		if c.Name == "" || strings.HasPrefix(c.Name, "/") {
			t.Errorf("a command is named without its slash: %q", c.Name)
		}
	}
	if byName["fix"] != "fix an issue" || byName["deploy"] != "deploy the service" {
		t.Errorf("the custom command and the skill are in the list with their descriptions: %v", byName)
	}
	seen := map[string]bool{}
	for _, c := range got {
		if seen[c.Name] {
			t.Errorf("/%s is listed twice", c.Name)
		}
		seen[c.Name] = true
	}
}

// What a command says, on either stream, comes back in one writer and in the order it was said: the program prints it, and nothing
// else may write to the terminal.
func TestASessionHostRunsACommandIntoOneWriter(t *testing.T) {
	s := chatSession(t, false, nil)
	h := &sessionHost{s: s}
	var out strings.Builder
	if res := h.Command(context.Background(), "/mode plan", &out); res.Quit || res.Send != "" {
		t.Errorf("a mode is not a quit nor a prompt: %+v", res)
	}
	if h.Mode() != "plan" || !strings.Contains(out.String(), "mode: plan") {
		t.Errorf("the mode is %q, said %q", h.Mode(), out.String())
	}
	if got := h.SetMode("accept-edits"); got != "accept-edits" || h.Mode() != "accept-edits" {
		t.Errorf("SetMode reports the mode in force: %q", got)
	}
	out.Reset()
	h.Command(context.Background(), "/diff", &out) // usage goes to stderr
	h.Command(context.Background(), "/rewind", &out)
	h.Command(context.Background(), "/nothing", &out)
	for _, want := range []string{"usage: /diff <checkpoint id>", "no checkpoints yet", "unknown command /nothing; try /help"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the output lacks %q:\n%s", want, out.String())
		}
	}
	if !strings.HasPrefix(out.String(), "usage: /diff") {
		t.Errorf("what was said first comes first:\n%s", out.String())
	}
	if res := h.Command(context.Background(), "/exit", &out); !res.Quit {
		t.Errorf("/exit quits: %+v", res)
	}
	if res := h.Command(context.Background(), "/quit", &out); !res.Quit {
		t.Errorf("/quit quits: %+v", res)
	}
}

func TestASessionHostExpandsACustomCommandIntoAPrompt(t *testing.T) {
	s := chatSession(t, true, map[string]string{".sleipnir/commands/fix.md": "---\ndescription: fix an issue\n---\nFix issue $1 and add a regression test.\n"})
	h := &sessionHost{s: s}
	var out strings.Builder
	res := h.Command(context.Background(), "/fix 12", &out)
	if res.Quit || res.Send != "Fix issue 12 and add a regression test." {
		t.Errorf("%+v %q", res, out.String())
	}
}

func TestASessionHostRunsATurnAndReportsHowItWent(t *testing.T) {
	s := chatSession(t, false, nil)
	h := &sessionHost{s: s}
	res := h.Turn(context.Background(), "hello")
	if res.Err != nil || res.Steps != 1 || res.CostUSD <= 0 {
		t.Errorf("a turn of one step: %+v", res)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if res := h.Turn(cancelled, "again"); !errors.Is(res.Err, context.Canceled) {
		t.Errorf("a turn that was cancelled says so: %+v", res)
	}
}

// A turn that the budget stopped has the sentence that says how to raise it, and has been through the sanitiser.
func TestASessionHostSaysHowTheBudgetStoppedATurn(t *testing.T) {
	// the model keeps asking for a tool, so that the turn goes on to a second step, where the budget is looked at
	s := chatSessionWith(t, false, nil, func(o *session.Options) { o.BudgetUSD = 1e-9 }, func(*mock.Call) mock.Reply {
		return mock.Reply{ToolCalls: []mock.ToolCall{{ID: "c1", Name: "ls", Args: `{}`}}}
	})
	h := &sessionHost{s: s}
	res := h.Turn(context.Background(), "hello")
	if !errors.Is(res.Err, agent.ErrBudget) || !strings.Contains(res.Message, "--budget-usd") {
		t.Errorf("the budget stopped the turn: %+v", res)
	}
	if strings.ContainsAny(res.Message, "\x1b\r") {
		t.Errorf("what is said is clean: %q", res.Message)
	}
}

func TestChatInfoIsWhatTheBannerSays(t *testing.T) {
	s := chatSession(t, false, nil)
	info := chatInfo(s, "/work/proj")
	if info.Model != "mock-1" || info.SessionID != s.ID || info.Cwd != "/work/proj" || info.Swarm || info.Resumed != "" || info.Budget != "" {
		t.Errorf("%+v", info)
	}
	b := chatSessionWith(t, false, nil, func(o *session.Options) { o.BudgetUSD = 5 }, nil)
	if got := chatInfo(b, "/work/proj").Budget; !strings.Contains(got, "$5") {
		t.Errorf("a session with a budget says so: %q", got)
	}
}

func TestTildePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, tc := range []struct{ in, want string }{
		{home, "~"},
		{filepath.Join(home, "proj", "x"), filepath.Join("~", "proj", "x")},
		{home + "2", home + "2"}, // a sibling whose name starts with the home directory's
		{"/elsewhere", "/elsewhere"},
		{"", ""},
	} {
		if got := tildePath(tc.in); got != tc.want {
			t.Errorf("tildePath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if _, err := os.UserHomeDir(); err != nil {
		t.Fatal(err)
	}
}

// /status and /permissions answer what a person asks when unsure: what is this session, and what did I allow.
func TestStatusAndPermissionsCommands(t *testing.T) {
	s := chatSession(t, false, nil)
	s.Perm.AddRule(perm.ScopeSession, perm.Rule{Action: perm.Allow, Tool: "Bash", Pattern: "go test:*"})
	var out strings.Builder
	slashTo(context.Background(), s, "/status", &out, &out)
	for _, want := range []string{"model    ", "mode     " + string(s.Perm.Mode()), "session  " + s.ID, "input "} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("/status lacks %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	slashTo(context.Background(), s, "/permissions", &out, &out)
	for _, want := range []string{"mode: " + string(s.Perm.Mode()), "allow (", "Bash(go test:*)"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("/permissions lacks %q:\n%s", want, out.String())
		}
	}
}

func TestCostTextNeverShowsARealCostAsNothing(t *testing.T) {
	for usd, want := range map[float64]string{0: "$0.0000", 0.00002: "<$0.0001", 0.0001: "$0.0001", 0.0141: "$0.0141"} {
		if got := costText(usd); got != want {
			t.Errorf("costText(%v) = %q, want %q", usd, got, want)
		}
	}
}
