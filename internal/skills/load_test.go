package skills

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestLoadSubstitutesArguments(t *testing.T) {
	w := newWorld(t)
	dir := w.proj(".claude", "fix", "---\nname: fix\ndescription: d\nallowed-tools: Read, Bash(git diff:*)\nmodel: opus\ncontext: fork\nagent: reviewer\n---\nFix issue $1 (all: $ARGUMENTS, second: $2, index: $ARGUMENTS[0]). Dir ${CLAUDE_SKILL_DIR} / ${SLEIPNIR_SKILL_DIR}. Literal $$1.\n")
	put(t, filepath.Join(dir, "scripts", "check.sh"), "echo")
	c, _ := w.discover(true)
	l, err := c.Load("fix", `42 "in the db"`)
	if err != nil {
		t.Fatal(err)
	}
	wantBody := "Fix issue 42 (all: 42 \"in the db\", second: in the db, index: 42). Dir " + dir + " / " + dir + ". Literal $1."
	if l.Body != wantBody {
		t.Errorf("body:\n%q\nwant:\n%q", l.Body, wantBody)
	}
	if l.Header != "Base directory for this skill: "+dir || l.Dir != dir {
		t.Errorf("header = %q", l.Header)
	}
	if strings.Join(l.Files, ",") != "scripts/check.sh" {
		t.Errorf("files = %v", l.Files)
	}
	if strings.Join(l.AllowedTools, "|") != "Read|Bash(git diff:*)" || l.Model != "opus" || l.Context != "fork" || l.Agent != "reviewer" || l.Hash == "" || l.Scope != ScopeProject {
		t.Errorf("metadata: %+v", l)
	}
	text := l.Text()
	if !strings.HasPrefix(text, l.Header+"\n\nFix issue 42") || !strings.Contains(text, "Supporting files") || !strings.HasSuffix(text, "- scripts/check.sh") {
		t.Errorf("text:\n%s", text)
	}
	// The caller's copy of the tool list cannot alter the catalog.
	l.AllowedTools[0] = "Write"
	if l2, _ := c.Load("fix", ""); l2.AllowedTools[0] != "Read" {
		t.Error("Loaded shares the catalog's tool list")
	}
}

func TestLoadAppendsArgumentsWhenThereIsNoPlaceholder(t *testing.T) {
	w := newWorld(t)
	w.proj(".claude", "plain", skillText("plain", "d"))
	c, _ := w.discover(true)
	l, _ := c.Load("plain", "some request")
	if l.Body != "Body of plain.\n\nARGUMENTS: some request" {
		t.Errorf("body = %q", l.Body)
	}
	l, _ = c.Load("plain", "")
	if l.Body != "Body of plain." || strings.Contains(l.Body, "ARGUMENTS") {
		t.Errorf("body = %q", l.Body)
	}
}

// Arguments come from a model or a user and are data: they are never expanded.
func TestLoadArgumentsAreNotReinterpreted(t *testing.T) {
	w := newWorld(t)
	dir := w.proj(".claude", "echo", "---\nname: echo\ndescription: d\n---\n[$ARGUMENTS] [$1]")
	c, _ := w.discover(true)
	l, _ := c.Load("echo", "${CLAUDE_SKILL_DIR} $ARGUMENTS $1")
	if strings.Contains(l.Body, dir) {
		t.Errorf("an argument was expanded into the base directory: %q", l.Body)
	}
	if l.Body != "[${CLAUDE_SKILL_DIR} $ARGUMENTS $1] [${CLAUDE_SKILL_DIR}]" {
		t.Errorf("body = %q", l.Body)
	}
}

func TestLoadCleansArguments(t *testing.T) {
	w := newWorld(t)
	w.proj(".claude", "echo", "---\nname: echo\ndescription: d\n---\n<$ARGUMENTS>")
	c, _ := w.discover(true)
	l, err := c.Load("echo", "a\u202Eb\x00c")
	if err != nil || l.Body != "<abc>" {
		t.Fatalf("body = %q, err = %v", l.Body, err)
	}
	if _, err := c.Load("echo", strings.Repeat("x", 100000)); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("err = %v", err)
	}
}

func TestInvocationPolicies(t *testing.T) {
	w := newWorld(t)
	w.proj(".claude", "open", skillText("open", "both"))
	w.proj(".claude", "userdo", "---\nname: userdo\ndescription: d\ndisable-model-invocation: true\n---\nu")
	w.proj(".claude", "modeldo", "---\nname: modeldo\ndescription: d\nuser-invocable: false\n---\nm")
	c, _ := w.discover(true)

	if _, err := c.LoadForModel("userdo", ""); err == nil || !strings.Contains(err.Error(), "unknown skill") {
		t.Errorf("the model loaded a disable-model-invocation skill: %v", err)
	} else if strings.Contains(err.Error(), "userdo") && strings.Contains(err.Error(), "available skills: modeldo, open, userdo") {
		t.Errorf("the error lists the hidden skill: %v", err)
	} else if !strings.Contains(err.Error(), "available skills: modeldo, open") {
		t.Errorf("the error should list what the model can use: %v", err)
	}
	if _, err := c.LoadForModel("modeldo", ""); err != nil {
		t.Errorf("model-only skill: %v", err)
	}
	if _, err := c.LoadForUser("modeldo", ""); err == nil || !strings.Contains(err.Error(), "only be used by the model") {
		t.Errorf("the user loaded a user-invocable:false skill: %v", err)
	}
	if _, err := c.LoadForUser("userdo", ""); err != nil {
		t.Errorf("user-only skill: %v", err)
	}
	if _, err := c.Load("userdo", ""); err != nil {
		t.Errorf("unrestricted Load: %v", err)
	}
	if _, err := c.Load("modeldo", ""); err != nil {
		t.Errorf("unrestricted Load: %v", err)
	}
}

func TestLoadNameResolution(t *testing.T) {
	w := newWorld(t)
	w.proj(".claude", "deploy", skillText("deploy", "d"))
	c, _ := w.discover(true)
	for _, name := range []string{"deploy", "Deploy", "/deploy", "  deploy  ", "/DEPLOY"} {
		if l, err := c.Load(name, ""); err != nil || l.Name != "deploy" {
			t.Errorf("Load(%q) = %v, %v", name, l.Name, err)
		}
	}
	for _, name := range []string{"", "/", "deplo", "deploy2", "../deploy", "deploy/../deploy", "dep*", "<b>bold</b>"} {
		_, err := c.Load(name, "")
		if err == nil || !strings.Contains(err.Error(), "unknown skill") || !strings.Contains(err.Error(), "available skills: deploy") {
			t.Errorf("Load(%q) err = %v", name, err)
		}
		if err != nil && (strings.Contains(err.Error(), "<b>") || strings.Contains(err.Error(), "\n")) {
			t.Errorf("the error echoes markup or line breaks: %q", err)
		}
	}
}

func TestUnknownSkillErrorIsBounded(t *testing.T) {
	w := newWorld(t)
	for i := 0; i < 60; i++ {
		n := "s" + strings.Repeat("x", i%5) + string(rune('a'+i%26)) + string(rune('a'+i/26))
		w.proj(".claude", n, skillText(n, "d"))
	}
	c, _ := w.discover(true)
	_, err := c.Load("nope", "")
	if err == nil || !strings.Contains(err.Error(), "and 30 more") || len(err.Error()) > 600 {
		t.Fatalf("err (%d bytes) = %v", len(err.Error()), err)
	}
}

func TestSupportingFileListIsBounded(t *testing.T) {
	w := newWorld(t)
	dir := w.proj(".claude", "many", skillText("many", "d"))
	for i := 0; i < maxListedFiles+50; i++ {
		put(t, filepath.Join(dir, "f", strings.Repeat("0", 3)+string(rune('a'+i%26))+string(rune('a'+(i/26)%26))+string(rune('a'+i/676))+".txt"), "x")
	}
	put(t, filepath.Join(dir, "a", "b", "c", "d", "e", "too-deep.txt"), "x")
	c, _ := w.discover(true)
	l, _ := c.Load("many", "")
	if len(l.Files) != maxListedFiles || !l.FilesTruncated || !strings.Contains(l.Text(), "and more") {
		t.Fatalf("%d files, truncated=%v", len(l.Files), l.FilesTruncated)
	}
	for _, f := range l.Files {
		if strings.Contains(f, "too-deep") {
			t.Error("a file deeper than the limit was listed")
		}
	}
}

func TestSnapshotSemantics(t *testing.T) {
	w := newWorld(t)
	dir := w.proj(".claude", "snap", "---\nname: snap\ndescription: original\nallowed-tools: Read\n---\noriginal body")
	c, _ := w.discover(true)
	// A compromised agent rewrites the skill mid-session: nothing changes until rediscovery.
	put(t, filepath.Join(dir, "SKILL.md"), "---\nname: snap\ndescription: hijacked\nallowed-tools: Bash(*)\n---\nhijacked body")
	l, err := c.Load("snap", "")
	if err != nil || l.Body != "original body" || strings.Join(l.AllowedTools, ",") != "Read" {
		t.Fatalf("loaded %+v (%v): the catalog must be a snapshot", l, err)
	}
	if c.Listing(0, nil) != "snap: original" {
		t.Fatalf("listing = %q", c.Listing(0, nil))
	}
	c2, _ := w.discover(true)
	if l2, _ := c2.Load("snap", ""); l2.Body != "hijacked body" || l2.Hash == l.Hash {
		t.Fatalf("rediscovery should see the change: %+v", l2)
	}
}

func TestConcurrentUse(t *testing.T) {
	w := newWorld(t)
	for _, n := range []string{"a", "b", "c"} {
		dir := w.proj(".claude", n, skillText(n, "d"))
		put(t, filepath.Join(dir, "x.txt"), n)
	}
	c, _ := w.discover(true)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			n := string(rune('a' + i%3))
			for j := 0; j < 30; j++ {
				if l, err := c.LoadForModel(n, "arg"); err != nil || !strings.Contains(l.Body, "Body of "+n) {
					t.Errorf("load %s: %v", n, err)
				}
				if f, err := c.ReadFile(n, "x.txt"); err != nil || f.Text != n {
					t.Errorf("read %s: %v", n, err)
				}
				_ = c.Listing(0, nil)
				_ = c.Skills()
			}
		}(i)
	}
	wg.Wait()
}
