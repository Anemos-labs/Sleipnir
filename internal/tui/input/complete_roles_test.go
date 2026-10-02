package input

import (
	"reflect"
	"testing"
)

var (
	testRoles = []Choice{
		{Text: "manager", Detail: "heimdall/qwen/qwen3.8-27b · the session's model"},
		{Text: "worker", Detail: "heimdall/qwen/qwen3.8-27b · the session's model"},
		{Text: "compactor", Detail: "(each agent's own) · no compactor model is set"},
	}
	testModels = []Choice{{Text: "heimdall/qwen/qwen3.8-27b"}, {Text: "heimdall/deepseek/deepseek-v4.1-flash"}, {Text: "ollama/qwen3:8b"}}
)

func roleModels() Completer {
	return RoleModels("roles", func() []Choice { return testRoles }, func() []Choice { return testModels })
}

// A role is completed with its "=", and what follows the "=" is a model: the second word of one command takes the same two steps.
func TestRoleModelsCompleteTheRoleThenItsModel(t *testing.T) {
	c := roleModels()
	complete := func(line string) (int, []string) {
		from, cs := c.Complete(line, len(line))
		var out []string
		for _, x := range cs {
			out = append(out, x.Text)
		}
		return from, out
	}
	if from, got := complete("/roles "); from != 7 || !reflect.DeepEqual(got, []string{"manager=", "worker=", "compactor="}) {
		t.Errorf("nothing typed: the roles, in the order given: %d %q", from, got)
	}
	if from, got := complete("/roles comp"); from != 7 || !reflect.DeepEqual(got, []string{"compactor="}) {
		t.Errorf("a role is matched like a model: %d %q", from, got)
	}
	if from, got := complete("/roles manager="); from != 15 || len(got) != 3 || got[0] != "heimdall/qwen/qwen3.8-27b " {
		t.Errorf("after the =, every model, replacing what follows it: %d %q", from, got)
	}
	if from, got := complete("/roles manager=dsk"); from != 15 || !reflect.DeepEqual(got, []string{"heimdall/deepseek/deepseek-v4.1-flash "}) {
		t.Errorf("a model is matched by the letters typed: %d %q", from, got)
	}
	line := "/roles manager=dsk compactor=oll"
	if from, got := complete(line); from != len(line)-len("oll") || !reflect.DeepEqual(got, []string{"ollama/qwen3:8b "}) {
		t.Errorf("the second pair is completed on its own: %d %q", from, got)
	}
	if _, got := complete("/roles bogus="); got != nil {
		t.Errorf("a word that is not a role has no models: %q", got)
	}
	if _, got := complete("/role manager="); got != nil {
		t.Errorf("another command gets nothing: %q", got)
	}
	if _, got := complete("/roles"); got != nil {
		t.Errorf("the command's own name is the slash commands' to complete: %q", got)
	}
}

// Choosing a role in the menu opens the menu of models at once, and choosing a model ends the pair, with a space for the next one.
func TestRoleMenuContinuesWithTheModelsOfTheRoleChosen(t *testing.T) {
	r := newRig(t, Options{Completer: roleModels()})
	r.send("/roles ")
	if m := r.ed.Completion(); !m.Open || len(m.Candidates) != 3 || m.From != 7 {
		t.Fatalf("the space after the command opens the roles: %+v", m)
	}
	r.send(kTab)
	m := r.ed.Completion()
	if r.state() != "/roles manager=|" || !m.Open || m.From != 15 || len(m.Candidates) != 3 {
		t.Fatalf("a role is chosen with its =, and its models are offered: %q %+v", r.state(), m)
	}
	r.send("dsk")
	r.send(kTab)
	if r.state() != "/roles manager=heimdall/deepseek/deepseek-v4.1-flash |" || r.ed.Completion().Open {
		t.Errorf("a model ends the pair: %q", r.state())
	}
}
