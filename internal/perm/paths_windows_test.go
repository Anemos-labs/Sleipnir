package perm

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestWindowsWorkspaceGoPackagePatterns keeps package analysis in required
// Windows CI alongside the native path protections it must preserve.
func TestWindowsWorkspaceGoPackagePatterns(t *testing.T) {
	testGoPackagePermissions(t)
	f := newFixture(t)
	var cases []tc
	for _, command := range []string{
		"go build -o ./... ./src/...", "go test -o=./... ./src/...",
		"go list -modfile ./... ./src/...", "go list -overlay=./... ./src/...",
		"go test ./src/... -args ./...", "go test -- ./...",
		"go test -custom ./...", "go test -custom=value ./...",
		"go test -f ./...", "go test -m ./...",
		"go test -vettool tool ./...",
		"go test -test.race ./...", "go test ./src -v ./...",
		"go test ./src -run TestExample ./...", "cat ./...",
		"go list ./.git./...", "go list ./NUL/...", "go list C:relative/...", "go list C:relative...",
	} {
		cases = append(cases, tc{name: command, mode: ModeBypass, req: bash(command), want: "deny"})
	}
	cases = append(cases,
		tc{name: "literal read", mode: ModeBypass, req: read("./..."), want: "deny"},
		tc{name: "literal write", mode: ModeBypass, req: write("./..."), want: "deny"},
		tc{name: "native package", req: bash(`go list '.\src\...'`), want: "allow"},
		tc{name: "absolute package", req: bash("go list '" + filepath.ToSlash(f.root) + "/src/...'"), want: "allow"},
		tc{name: "test prefix value", mode: ModeAcceptEdits, req: bash("go test -test.run TestExample ./..."), want: "allow"},
		tc{name: "test regex is not a file", mode: ModeAcceptEdits, req: bash("go test -run ./... ./src/..."), want: "allow"},
		tc{name: "buildvcs boolean", req: bash("go list -buildvcs ./..."), want: "allow"},
		tc{name: "list terminator", req: bash("go list -- ./..."), want: "allow"},
	)
	runCases(t, f, cases)
	e := f.engine(t, Config{Mode: ModeAcceptEdits})
	if d := e.Check(context.Background(), f.request(bash("go test $(echo ./...)"))); d.Allow {
		t.Fatalf("command substitution was allowed: %+v", d)
	}
}

func TestWindowsWorkspacePermissions(t *testing.T) {
	f := newFixture(t)
	cases := []tc{
		{name: "native read", req: read(filepath.Join(f.root, "src", "a.go")), want: "allow"},
		{name: "slash read", req: read(filepath.ToSlash(filepath.Join(f.root, "src", "a.go"))), want: "allow"},
		{name: "relative read", req: read(`src\a.go`), want: "allow"},
		{name: "native edit", mode: ModeAcceptEdits, req: edit(filepath.Join(f.root, "src", "a.go")), want: "allow"},
		{name: "new nested file", mode: ModeAcceptEdits, req: write(filepath.Join(f.root, "new", "nested", "a.go")), want: "allow"},
		{name: "case alias", req: read(strings.ToUpper(filepath.Join(f.root, "src", "a.go"))), want: "allow"},
		{name: "sibling prefix", req: read(f.root + `-other\a.go`), want: "ask"},
		{name: "outside", mode: ModeAcceptEdits, req: write(filepath.Join(f.outside, "new.txt")), want: "ask"},
		{name: "parent escape", req: read(f.root + `\..\notes.txt`), want: "ask"},
		{name: "dotenv", mode: ModeBypass, req: read(filepath.Join(f.root, ".env")), want: "deny"},
		{name: "git metadata", mode: ModeBypass, req: write(filepath.Join(f.root, ".git", "HEAD")), want: "deny"},
		{name: "private key", mode: ModeBypass, req: read(filepath.Join(f.home, ".ssh", "id_rsa")), want: "deny"},
		{name: "absolute deny", deny: []string{"Read(" + filepath.ToSlash(f.root) + "/src/**)"}, req: read(filepath.Join(f.root, "src", "a.go")), want: "deny"},
		{name: "lowercase drive wildcard deny", deny: []string{"Read(" + strings.ToLower(filepath.VolumeName(f.root)) + "/**)"}, req: read(filepath.Join(f.root, "src", "a.go")), want: "deny"},
		{name: "relative deny", deny: []string{"Read(src/**)"}, req: read(filepath.Join(f.root, "src", "a.go")), want: "deny"},
		{name: "absolute outside allow", allow: []string{"Read(" + filepath.ToSlash(f.outside) + "/secret.txt)"}, req: read(filepath.Join(f.outside, "secret.txt")), want: "allow"},
		{name: "explicit env allow", allow: []string{"Read(" + filepath.ToSlash(f.root) + "/.env)"}, req: read(filepath.Join(f.root, ".env")), want: "allow"},
		{name: "internal link", req: read(filepath.Join(f.root, "link-in")), want: "allow"},
		{name: "escaping link", req: read(filepath.Join(f.root, "link-out")), want: "ask"},
		{name: "relative escaping link", req: read(filepath.Join(f.root, "rel-out")), want: "ask"},
		{name: "credential link", mode: ModeBypass, req: read(filepath.Join(f.root, "link-ssh", "id_rsa")), want: "deny"},
		{name: "dangling credential link", mode: ModeBypass, req: write(filepath.Join(f.root, "dangling")), want: "deny"},
	}
	for _, p := range []string{
		`C:relative.txt`, `\rooted.txt`, `\\?\` + f.root + `\.env`, `\\.\NUL`,
		f.root + `\.env:stream`, f.root + `\.git.\HEAD`, f.root + `\.env `, f.root + `\NUL`,
	} {
		cases = append(cases, tc{name: "unsupported " + p, mode: ModeBypass, req: read(p), want: "deny"})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := f.engine(t, Config{Mode: c.mode, Allow: c.allow, Deny: c.deny})
			d := e.Check(context.Background(), f.request(c.req))
			if got := outcome(d); got != c.want {
				t.Fatalf("outcome = %s, want %s: %s", got, c.want, d.Reason)
			}
		})
	}
	t.Run("state edits require approval", func(t *testing.T) {
		dir := filepath.Join(f.root, "state[1]")
		e := f.engine(t, Config{Mode: ModeBypass, StateDir: dir})
		if d := e.Check(context.Background(), f.request(write(filepath.Join(dir, "sessions", "state.json")))); outcome(d) != "ask" {
			t.Fatalf("state edit = %+v", d)
		}
	})
	t.Run("absolute patterns retain glob escaping", func(t *testing.T) {
		p := filepath.Join(f.outside, "literal[1].txt")
		e := f.engine(t, Config{Allow: []string{"Read(" + escapeGlob(filepath.ToSlash(p)) + ")"}})
		for _, c := range []struct {
			path  string
			allow bool
		}{{p, true}, {filepath.Join(f.outside, "literal1.txt"), false}} {
			if d := e.Check(context.Background(), f.request(read(c.path))); d.Allow != c.allow {
				t.Errorf("escaped rule %s = %+v", c.path, d)
			}
		}
		if _, err := NewEngine(Config{Root: f.root, Deny: []string{"Read(" + f.root + `\src\**)`}}); err == nil {
			t.Fatal("native separators in an absolute rule must report the forward-slash requirement")
		}
	})
	t.Run("confinement", func(t *testing.T) {
		e := f.engine(t, Config{Mode: ModeBypass})
		e.Confine("a1", filepath.Join(f.root, "src"))
		for _, c := range []struct {
			path  string
			allow bool
		}{
			{filepath.Join(f.root, "src", "a.go"), true},
			{filepath.Join(f.root, "main.go"), false},
			{filepath.Join(f.root, "docs", "README.md"), false},
		} {
			if d := e.Check(context.Background(), f.request(read(c.path))); d.Allow != c.allow {
				t.Errorf("confinement %s = %+v", c.path, d)
			}
		}
	})
}

func TestWindowsJunctionEscape(t *testing.T) {
	f := newFixture(t)
	link := filepath.Join(f.root, "junction")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, f.outside).CombinedOutput(); err != nil {
		t.Fatalf("create junction: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = os.Remove(link) })
	e := f.engine(t, Config{Mode: ModeAcceptEdits})
	if d := e.Check(context.Background(), f.request(write(filepath.Join(link, "new.txt")))); outcome(d) != "ask" {
		t.Fatalf("write through junction = %+v", d)
	}
}

func TestWindowsWorkspaceRootRelativeLinkEscape(t *testing.T) {
	f := newFixture(t)
	outside := filepath.Join(f.outside, "secret.txt")
	link := filepath.Join(f.root, "root-relative-link")
	if err := os.Symlink(strings.TrimPrefix(outside, filepath.VolumeName(outside)), link); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(link); err != nil || string(b) != "outside" {
		t.Fatalf("root-relative link fixture: %q, %v", b, err)
	}
	e := f.engine(t, Config{Mode: ModeAcceptEdits})
	if d := e.Check(context.Background(), f.request(read(link))); outcome(d) != "ask" {
		t.Fatalf("read through root-relative link = %+v", d)
	}
}

func TestWindowsWorkspaceDeviceLink(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t, Config{Mode: ModeBypass})
	link := filepath.Join(f.root, "device-link")
	if err := os.Symlink("NUL", link); err != nil {
		t.Fatal(err)
	}
	if d := e.Check(context.Background(), f.request(read(link))); outcome(d) != "deny" {
		t.Fatalf("read through device link = %+v", d)
	}
}

func TestWindowsWorkspaceLongLinkChain(t *testing.T) {
	f := newFixture(t)
	target := filepath.Join(f.home, ".ssh", "id_rsa")
	for i := 0; i < 42; i++ {
		link := filepath.Join(f.root, strings.Repeat("x", i+1))
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		target = link
	}
	// Windows can follow chains beyond the resolver's work bound. Refusing only
	// those rejected by the OS would leave a credential alias permitted here.
	if b, err := os.ReadFile(target); err != nil || string(b) != "SECRET-PRIVATE-KEY" {
		t.Fatalf("long-chain fixture: %q, %v", b, err)
	}
	e := f.engine(t, Config{Mode: ModeBypass})
	if d := e.Check(context.Background(), f.request(read(target))); outcome(d) != "deny" {
		t.Fatalf("long link chain = %+v", d)
	}
	// An unresolved absolute Allow pattern must not become the empty glob,
	// which represents the entire filesystem.
	e = f.engine(t, Config{Allow: []string{"Read(" + filepath.ToSlash(target) + ")"}})
	if d := e.Check(context.Background(), f.request(read(filepath.Join(f.outside, "secret.txt")))); outcome(d) != "ask" {
		t.Fatalf("unresolved rule allowed an unrelated outside file: %+v", d)
	}
}

func TestWindowsGlobVolumeAndBoundaries(t *testing.T) {
	f := newFixture(t)
	budget := 1000
	paths, ok := expandGlob(filepath.ToSlash(f.root)+"/src/*.go", &budget)
	if !ok || len(paths) != 2 {
		t.Fatalf("glob = %v, %v", paths, ok)
	}
	volume := filepath.ToSlash(filepath.VolumeName(f.root)) + "/"
	if got := realPath(strings.ToLower(volume)); got != volume {
		t.Errorf("drive root = %q, want %q", got, volume)
	}
	if !inside(volume, filepath.ToSlash(f.root)) || inside("C:/repo", "C:/repo-other/a") || inside("//server/share", "//server/share-other/a") {
		t.Fatal("volume/path boundaries are wrong")
	}
	for _, c := range []struct{ path, root, tail string }{
		{`C:\repo\file`, "C:/", "repo/file"},
		{`\\server\share\repo\file`, "//server/share/", "repo/file"},
	} {
		root, tail := volumeRoot(c.path)
		if root != c.root || tail != c.tail {
			t.Errorf("volumeRoot(%q) = %q, %q", c.path, root, tail)
		}
	}
}
