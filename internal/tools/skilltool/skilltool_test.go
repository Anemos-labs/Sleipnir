package skilltool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/skills"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

func catalog(t *testing.T) *skills.Catalog {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".sleipnir", "skills", "release")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("SKILL.md", "---\nname: release\ndescription: cut a release of this project\n---\nTag the commit, then run $ARGUMENTS.\n")
	write("checklist.md", "- changelog\n- tag\n")
	hidden := filepath.Join(home, ".sleipnir", "skills", "internal")
	if err := os.MkdirAll(hidden, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hidden, "SKILL.md"), []byte("---\nname: internal\ndescription: only the user runs this\ndisable-model-invocation: true\n---\nsecret procedure\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, warns := skills.Discover(skills.Opts{Home: home})
	if len(warns) > 0 {
		t.Logf("warnings: %v", warns)
	}
	return cat
}

func run(t *testing.T, tool *Tool, in any) *tools.Result {
	t.Helper()
	b, _ := json.Marshal(in)
	res, err := tool.Run(context.Background(), &tools.Call{ID: "1", Name: "skill", Input: b, Env: &tools.Env{}})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestLoadsASkillWithArgumentsAndSupportingFiles(t *testing.T) {
	res := run(t, New(catalog(t)), map[string]any{"name": "release", "args": "make dist"})
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Text)
	}
	for _, want := range []string{"Tag the commit, then run make dist.", "checklist.md"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("result lacks %q:\n%s", want, res.Text)
		}
	}
}

func TestTheModelCannotLoadWhatIsReservedForTheUser(t *testing.T) {
	res := run(t, New(catalog(t)), map[string]any{"name": "internal"})
	if !res.IsError || strings.Contains(res.Text, "secret procedure") {
		t.Fatalf("a disable-model-invocation skill must not load for the model: %+v", res)
	}
}

func TestUnknownSkillNamesTheChoices(t *testing.T) {
	res := run(t, New(catalog(t)), map[string]any{"name": "releas"})
	if !res.IsError || !strings.Contains(res.Text, "release") {
		t.Fatalf("an unknown name should suggest the close one: %+v", res)
	}
}

func TestNoCatalogIsNotAnError(t *testing.T) {
	for _, tool := range []*Tool{New(nil), New(&skills.Catalog{})} {
		res := run(t, tool, map[string]any{"name": "x"})
		if !res.IsError || !strings.Contains(res.Text, "no skills") {
			t.Errorf("got %+v", res)
		}
	}
}

func TestSchemaIsConstantAcrossProjects(t *testing.T) {
	a, b := New(nil).Spec(), New(catalog(t)).Spec()
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(ja) != string(jb) {
		t.Fatal("the tool's bytes must not depend on the catalog: they are part of every agent's cached prefix")
	}
	if !a.ReadOnly {
		t.Error("loading a skill reads text; it must be a read-only tool")
	}
}

func TestBadInput(t *testing.T) {
	res, err := New(nil).Run(context.Background(), &tools.Call{ID: "1", Name: "skill", Input: json.RawMessage(`{"name":`), Env: &tools.Env{}})
	if err != nil || !res.IsError {
		t.Fatalf("%+v %v", res, err)
	}
	if res := run(t, New(catalog(t)), map[string]any{"name": "  "}); !res.IsError {
		t.Fatal("an empty name must be an error")
	}
}
