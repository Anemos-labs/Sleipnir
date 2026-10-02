package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// /restart ends the chat program and starts the chat again in a child that has the terminal. Whatever the first one read the keyboard
// with must be gone by then: a read that is left waiting takes the first thing typed for the new chat, and at the prompt of /login it
// takes the whole line, the key.
func TestE2EChatKeysTypedAfterARestartReachTheNewChat(t *testing.T) {
	m := startModel(t)
	m.on("@hello", say("hi there"))
	w := newWorld(t, m.url())
	u := startUI(t, w)

	u.send("/restart")
	u.wait("the chat again", func(_, all string) bool {
		i := strings.LastIndex(all, "restarting:")
		return i >= 0 && strings.Count(all[i:], "Type a goal") >= 2
	})
	u.typeText("@hello")
	u.expectVisible("| > @hello")
	u.enter()
	u.expect("hi there")
	u.ready()
	u.ctrlD()
	u.exited(0)
	u.goals(m, "@hello")
}

// /login leaves the chat for the terminal's own prompt, takes the key typed at it whole (a read left behind by the chat would have taken
// the line), keeps it, and the chat comes back and works.
func TestE2EChatLoginTakesTheKeyAndTheChatComesBack(t *testing.T) {
	m := startModel(t)
	m.on("@hello", say("hi there"))
	m.on("ping", say("pong")) // the key is tried with one small request when it has been typed
	w := newWorld(t, "")
	w.writeUserConfig(map[string]any{
		"providers": map[string]any{"mock": map[string]any{"base_url": m.url(), "api_key_env": "SLEIPNIR_MOCK_KEY"}},
		"models":    map[string]any{"default": "mock/mock-1"},
	})
	w.extra = append(w.extra, "SLEIPNIR_MOCK_KEY=the-key-of-the-environment")
	u := startUI(t, w)

	u.send("/login mock")
	u.expect("leaving the chat to sign in", "Paste your mock key (hidden")
	u.typeText("the-typed-key")
	u.enter()
	u.wait("the chat again", func(_, all string) bool {
		i := strings.LastIndex(all, "Saved.")
		return i >= 0 && strings.Count(all[i:], "Type a goal") >= 2
	})
	if b, err := os.ReadFile(filepath.Join(w.home, ".sleipnir", "auth.json")); err != nil || !strings.Contains(string(b), "the-typed-key") {
		t.Errorf("the key that was typed is not in auth.json: %q, %v", b, err)
	}
	u.send("@hello")
	u.expect("hi there")
	u.ready()
	u.ctrlD()
	u.exited(0)
	u.goals(m, "ping", "@hello")
}

// The chat on a terminal is a team by default, so it has to be resumable: --continue brings the manager's conversation back (it was refused:
// "resuming into a swarm is not supported yet"), and a goal after it works.
func TestE2EChatTeamResumesWithContinue(t *testing.T) {
	m := startModel(t)
	m.on("@hello", say("hi there"))
	m.on("@again", say("and again"))
	w := newWorld(t, m.url())

	u := startUI(t, w, "--swarm", "4")
	u.send("@hello")
	u.expect("hi there")
	u.ready()
	u.ctrlD()
	u.exited(0)

	u = startUI(t, w, "--swarm", "4", "--continue")
	u.expect("resumed: the manager's 2 turns and the board are back")
	u.send("/agents") // the page of the team knows the manager is there (it said the team starts with the first goal)
	u.expect("1 of 4 agents started", "mgr")
	u.send("@again")
	u.expect("and again")
	u.ready()
	u.ctrlD()
	u.exited(0)
	u.goals(m, "@hello", "@again")
}

// /model in a team starts it again on the other model, and the manager's conversation goes with it (it did not: "its conversation does not
// carry over").
func TestE2EChatTeamModelSwitchKeepsTheConversation(t *testing.T) {
	m := startModel(t)
	m.on("@hello", say("hi there"))
	m.on("@again", say("and again"))
	w := newWorld(t, m.url())
	u := startUI(t, w, "--swarm", "4")
	u.send("@hello")
	u.expect("hi there")
	u.ready()
	u.send("/model mock/mock-1")
	u.wait("the team again, resumed", func(_, all string) bool {
		i := strings.LastIndex(all, "restarting:")
		return i >= 0 && strings.Contains(all[i:], "resumed: the manager's 2 turns")
	})
	u.send("@again")
	u.expect("and again")
	u.ready()
	u.ctrlD()
	u.exited(0)
	u.goals(m, "@hello", "@again")
}

// /roles opens a menu of the roles that can run on a model of their own, with what each runs on, and choosing one goes on to the menu of its
// models: a role's model is chosen in the chat, and not by writing a flag.
func TestE2EChatRolesMenu(t *testing.T) {
	m := startModel(t)
	w := newWorld(t, m.url())
	u := startUI(t, w, "--swarm", "4")
	u.ready()

	u.typeText("/roles ")
	u.expectVisible("manager", "backend", "the session's model")
	u.typeText("comp") // the menu shows eight rows: the compactor is found by typing
	u.expectVisible("compactor", "no compactor model is set")
	u.typeText(keyTab)
	u.expectVisible("> /roles compactor=")
	u.ctrlC() // clears the line
	u.ready()
	u.ctrlD()
	u.exited(0)
}

// `/resume ` offers the earlier sessions of this project to choose from, as `/model ` offers models: when it was, what it cost, what was asked.
func TestE2EChatResumeCompletesTheEarlierSessions(t *testing.T) {
	m := startModel(t)
	m.on("@hello", say("hi there"))
	w := newWorld(t, m.url())
	u := startUI(t, w, "--swarm", "4")
	u.send("@hello")
	u.expect("hi there")
	u.ready()
	u.ctrlD()
	u.exited(0)

	u = startUI(t, w, "--swarm", "4")
	u.ready()
	u.typeText("/resume ")
	u.expect("just now", "@hello")
	u.typeText("\x15") // ctrl+u: Ctrl-D quits only from an empty prompt
	u.ctrlD()
	u.exited(0)
}
