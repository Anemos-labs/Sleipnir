package perm

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Confine binds an agent to its own worktree: inside the workspace it may touch its
// tree and nothing else, for the file tools and for shell commands alike, and other
// agents are not affected.
func TestConfineKeepsAnAgentInsideItsOwnTree(t *testing.T) {
	f := newFixture(t)
	trees := filepath.Join(f.base, "trees")
	mine, other := filepath.Join(trees, "be-1"), filepath.Join(trees, "be-2")
	for _, p := range []string{filepath.Join(mine, "src", "a.go"), filepath.Join(other, "src", "a.go")} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("package src"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	e, err := NewEngine(Config{Root: f.root, Home: f.home, ExtraRoots: []string{trees}, Mode: ModeAcceptEdits})
	if err != nil {
		t.Fatal(err)
	}
	e.Confine("be-1", mine)

	as := func(agent string, q rq, cwd string) Decision {
		r := f.request(q)
		r.Agent, r.Cwd = agent, cwd
		return e.Check(bg, r)
	}
	inMine := func(rel string) string { return filepath.Join(mine, rel) }
	cases := []struct {
		name  string
		agent string
		q     rq
		cwd   string
		want  string
	}{
		{"read in its own tree", "be-1", read(inMine("src/a.go")), mine, "allow"},
		{"write in its own tree", "be-1", write(inMine("src/new.go")), mine, "allow"},
		{"shell write in its own tree", "be-1", bash("echo x > src/new.go"), mine, "allow"},
		{"read of the shared checkout", "be-1", read("{root}/main.go"), mine, "deny"},
		{"write to the shared checkout", "be-1", write("{root}/main.go"), mine, "deny"},
		{"edit through an absolute path into the checkout", "be-1", edit("{root}/src/a.go"), mine, "deny"},
		{"read of another agent's tree", "be-1", read(filepath.Join(other, "src", "a.go")), mine, "deny"},
		{"write to another agent's tree", "be-1", write(filepath.Join(other, "src", "a.go")), mine, "deny"},
		{"shell read of another agent's tree", "be-1", bash("cat " + filepath.Join(other, "src", "a.go")), mine, "deny"},
		{"shell read up and over", "be-1", bash("cat ../be-2/src/a.go"), mine, "deny"},
		{"shell write into the checkout", "be-1", bash("echo x > {root}/pwned.txt"), mine, "deny"},
		{"listing the trees directory", "be-1", read(trees), mine, "deny"},
		{"a link in its tree that leads to the checkout", "be-1", read(inMine("to-main")), mine, "deny"},
		{"outside every workspace root is the ordinary rules' business", "be-1", read("{out}/secret.txt"), mine, "ask"},
		{"credentials stay denied", "be-1", read("{home}/.ssh/id_rsa"), mine, "deny"},
		{"another agent is not confined by be-1's tree", "be-2", read("{root}/main.go"), other, "allow"},
		{"an unconfined agent may write the checkout", "mgr", write("{root}/main.go"), f.root, "allow"},
	}
	if err := os.Symlink(f.root, inMine("to-main")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		d := as(tc.agent, tc.q, tc.cwd)
		got := "deny"
		switch {
		case d.Allow:
			got = "allow"
		case strings.Contains(d.Reason, "approval required"):
			got = "ask"
		}
		if got != tc.want {
			t.Errorf("%s: %s (%s), want %s", tc.name, got, d.Reason, tc.want)
		}
		if got == "deny" && strings.Contains(tc.name, "tree") && !d.Allow && !strings.Contains(d.Reason, "isolation:") && !strings.Contains(tc.name, "credentials") {
			t.Errorf("%s: the refusal should say why: %q", tc.name, d.Reason)
		}
	}
	// A rule cannot lift it: the check is a hard deny like the built-in protections.
	e.AddRule(ScopeSession, Rule{Action: Allow, Tool: "Edit", Pattern: "**"})
	if d := as("be-1", write("{root}/main.go"), mine); d.Allow {
		t.Error("an allow rule lifted the confinement")
	}
	// Bypass mode does not lift it either.
	e.SetMode(ModeBypass)
	if d := as("be-1", write("{root}/main.go"), mine); d.Allow {
		t.Error("bypass mode lifted the confinement")
	}
	// Unconfine ends it.
	e.Unconfine("be-1")
	if d := as("be-1", read("{root}/main.go"), mine); !d.Allow {
		t.Errorf("Unconfine did not lift the confinement: %s", d.Reason)
	}
}

// The rules that are relative to the workspace hold inside every agent's tree too: a
// project's own protections (a denied directory, the ask rule on the configuration
// directory, a floating allow) are not lost by working in a copy of the project, whose
// work reaches the checkout by a merge.
func TestRelativeRulesApplyInsideEveryTree(t *testing.T) {
	for _, name := range []string{"trees", "we[ir]d*trees?"} { // the second has glob characters in its name
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			trees := filepath.Join(f.base, name)
			mine, other := filepath.Join(trees, "be-1"), filepath.Join(trees, "be-2")
			for _, dir := range []string{mine, other} {
				for _, sub := range []string{"src", "migrations", "secrets", ".sleipnir"} {
					if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
						t.Fatal(err)
					}
				}
			}
			e, err := NewEngine(Config{
				Root: f.root, Home: f.home, TreeParents: []string{trees}, Mode: ModeBypass,
				Deny:  []string{"Edit(./migrations/**)", "Read(./secrets/**)"},
				Ask:   []string{"Edit(./.sleipnir/**)"},
				Allow: []string{"Edit(src/**)"},
			})
			if err != nil {
				t.Fatal(err)
			}
			e.Confine("be-1", mine)
			as := func(q rq, cwd string) string {
				r := f.request(q)
				r.Agent, r.Cwd = "be-1", cwd
				d := e.Check(bg, r)
				switch {
				case d.Allow:
					return "allow"
				case strings.Contains(d.Reason, "approval required"):
					return "ask"
				}
				return "deny: " + d.Reason
			}
			cases := []struct {
				name string
				q    rq
				want string
			}{
				{"an ordinary write in its tree", write(filepath.Join(mine, "docs.md")), "allow"},
				{"a write under a denied directory", write(filepath.Join(mine, "migrations", "001.sql")), "deny: denied by rule Edit(./migrations/**)"},
				{"a shell write under a denied directory", bash("echo x > migrations/002.sql"), "deny: denied by rule Edit(./migrations/**)"},
				{"a read of a denied directory", read(filepath.Join(mine, "secrets", "key.txt")), "deny: denied by rule Read(./secrets/**)"},
				{"the configuration directory asks", write(filepath.Join(mine, ".sleipnir", "config.json")), "ask"},
				{"a shell write to the configuration directory asks", bash("echo {} > .sleipnir/config.json"), "ask"},
				{"a floating rule reaches the tree", edit(filepath.Join(mine, "src", "x.go")), "allow"},
			}
			for _, tc := range cases {
				if got := as(tc.q, mine); !strings.HasPrefix(got, tc.want) {
					t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
				}
			}
			// The same rules still guard the checkout itself, for an agent that is not confined.
			r := f.request(write("{root}/migrations/x.sql"))
			r.Agent = "mgr"
			if d := e.Check(bg, r); d.Allow {
				t.Error("the deny rule no longer guards the checkout")
			}
			// And they do not spill over to what is not a tree: a directory of the same shape
			// elsewhere (the parent's own siblings) is judged by the ordinary rules only.
			elsewhere := filepath.Join(f.outside, "migrations", "x.sql")
			e.Unconfine("be-1")
			r = f.request(write(elsewhere))
			r.Agent = "be-1"
			if d := e.Check(bg, r); strings.Contains(d.Reason, "Edit(./migrations/**)") {
				t.Errorf("a relative rule matched outside the workspace: %s", d.Reason)
			}
		})
	}
}

// Confine and Unconfine are safe under concurrent checks (a swarm confines workers
// while others are running).
func TestConfineIsSafeForConcurrentUse(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t, Config{Mode: ModeAcceptEdits})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := "w-" + string(rune('a'+i))
			for j := 0; j < 50; j++ {
				e.Confine(id, f.root)
				r := f.request(read("{root}/main.go"))
				r.Agent = id
				_ = e.Check(bg, r)
				e.Unconfine(id)
			}
		}(i)
	}
	wg.Wait()
}
