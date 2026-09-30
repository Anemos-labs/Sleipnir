package perm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture is a throwaway home directory with a workspace inside it, secrets
// beside it, and symlinks that try to get out. Nothing here touches the real
// HOME.
//
//	<base>/home                    Home
//	<base>/home/.ssh, .aws, ...    credentials
//	<base>/home/proj               Root (the workspace)
//	<base>/outside                 a directory outside both
type fixture struct {
	base, home, root, outside string
}

func newFixture(t testing.TB) fixture {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{
		base: base, home: filepath.Join(base, "home"),
		root: filepath.Join(base, "home", "proj"), outside: filepath.Join(base, "outside"),
	}
	write := func(p, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	h, r := f.home, f.root
	write(filepath.Join(h, ".ssh", "id_rsa"), "SECRET-PRIVATE-KEY")
	write(filepath.Join(h, ".ssh", "id_rsa.pub"), "public")
	write(filepath.Join(h, ".ssh", "known_hosts"), "hosts")
	write(filepath.Join(h, ".ssh", "config"), "Host x")
	write(filepath.Join(h, ".aws", "credentials"), "SECRET-aws")
	write(filepath.Join(h, ".gnupg", "pubring.kbx"), "SECRET-gpg")
	write(filepath.Join(h, ".config", "gcloud", "creds.json"), "SECRET-gcloud")
	write(filepath.Join(h, ".npmrc"), "SECRET-token")
	write(filepath.Join(h, "notes.txt"), "notes")
	write(filepath.Join(r, "main.go"), "package main")
	write(filepath.Join(r, "src", "a.go"), "package src")
	write(filepath.Join(r, "src", "b.go"), "package src")
	write(filepath.Join(r, ".env"), "SECRET=1")
	write(filepath.Join(r, ".env.example"), "SECRET=")
	write(filepath.Join(r, ".git", "config"), "[core]")
	write(filepath.Join(r, ".git", "HEAD"), "ref: refs/heads/feature\n")
	write(filepath.Join(r, ".gitignore"), "bin")
	write(filepath.Join(r, "docs", "README.md"), "# hi")
	write(filepath.Join(r, "secrets", "key.txt"), "k")
	write(filepath.Join(r, "sub", "deep", "file.txt"), "x")
	write(filepath.Join(f.outside, "secret.txt"), "outside")
	write(filepath.Join(f.outside, "cert.pem"), "pem")
	link := func(target, name string) {
		t.Helper()
		if err := os.Symlink(target, filepath.Join(r, name)); err != nil {
			t.Fatal(err)
		}
	}
	link(filepath.Join(f.outside, "secret.txt"), "link-out") // escapes the workspace
	link(f.outside, "link-outdir")                           // a directory outside
	link(filepath.Join(h, ".ssh"), "link-ssh")               // into the credentials
	link(filepath.Join(r, "src", "a.go"), "link-in")         // harmless
	link(filepath.Join(h, ".ssh", "newkey"), "dangling")     // target does not exist yet
	link("../../outside/secret.txt", "rel-out")              // relative escape
	link(filepath.Join(r, "link-out"), "chain")              // link to a link that escapes
	return f
}

// engine builds an Engine for the fixture with cfg's Root and Home filled in.
func (f fixture) engine(t testing.TB, cfg Config) *Engine {
	t.Helper()
	cfg.Root, cfg.Home = f.root, f.home
	e, err := NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// expand fills the {root} {home} {out} placeholders used by the tables.
func (f fixture) expand(s string) string {
	return strings.NewReplacer("{root}", f.root, "{home}", f.home, "{out}", f.outside).Replace(s)
}

func (f fixture) expandAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = f.expand(s)
	}
	return out
}

// rq is a compact request description for the tables.
type rq struct {
	tool    string
	cmd     string
	paths   []string
	writes  bool
	network bool
	input   string
	role    string
	cwd     string
	risk    Risk
}

func (f fixture) request(q rq) Request {
	r := Request{
		Agent: "a1", Role: q.role, Tool: q.tool, Command: f.expand(q.cmd), Writes: q.writes,
		Network: q.network, Cwd: f.expand(q.cwd), Risk: q.risk, Summary: q.tool,
	}
	for _, p := range q.paths {
		r.Paths = append(r.Paths, f.expand(p))
	}
	if q.input != "" {
		r.Input = json.RawMessage(f.expand(q.input))
	}
	return r
}

func bash(cmd string) rq       { return rq{tool: "Bash", cmd: cmd} }
func read(paths ...string) rq  { return rq{tool: "Read", paths: paths} }
func write(paths ...string) rq { return rq{tool: "Write", paths: paths, writes: true} }
func edit(paths ...string) rq  { return rq{tool: "Edit", paths: paths, writes: true} }
func fetch(url string) rq {
	return rq{tool: "WebFetch", network: true, input: `{"url":"` + url + `"}`}
}

// outcome classifies a Decision made without a Prompter.
func outcome(d Decision) string {
	switch {
	case d.Allow:
		return "allow"
	case strings.HasPrefix(d.Reason, "approval required"):
		return "ask"
	}
	return "deny"
}

// tc is one table case: a configuration, a request, the expected outcome, and a
// fragment the reason must contain.
type tc struct {
	name  string
	mode  Mode
	allow []string
	ask   []string
	deny  []string
	roles map[string]RoleProfile
	req   rq
	want  string // allow | ask | deny
	why   string
}

func runCases(t *testing.T, f fixture, cases []tc) {
	t.Helper()
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			roles := map[string]RoleProfile{}
			for name, rp := range c.roles {
				rp.Allow, rp.Ask, rp.Deny = f.expandAll(rp.Allow), f.expandAll(rp.Ask), f.expandAll(rp.Deny)
				roles[name] = rp
			}
			e := f.engine(t, Config{
				Mode: c.mode, Allow: f.expandAll(c.allow), Ask: f.expandAll(c.ask), Deny: f.expandAll(c.deny), Roles: roles,
			})
			req := f.request(c.req)
			d := e.Check(context.Background(), req)
			if got := outcome(d); got != c.want {
				t.Errorf("%s %q %v: got %s (%q), want %s", req.Tool, req.Command, req.Paths, got, d.Reason, c.want)
			}
			if c.why != "" && !strings.Contains(d.Reason, c.why) {
				t.Errorf("reason %q does not contain %q", d.Reason, c.why)
			}
			if d.Reason == "" {
				t.Errorf("decision has no reason")
			}
		})
	}
}
