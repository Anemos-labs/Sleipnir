package perm

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestGoPackagePermissions(t *testing.T) {
	testGoPackagePermissions(t)
}

func testGoPackagePermissions(t *testing.T) {
	t.Helper()
	f := newFixture(t)
	runCases(t, f, []tc{
		{name: "test project", mode: ModeAcceptEdits, req: bash("go test ./..."), want: "allow"},
		{name: "test options", mode: ModeAcceptEdits, req: bash("go test -race -count=1 -v ./..."), want: "allow"},
		{name: "test value flags", mode: ModeAcceptEdits, req: bash("go test -run TestExample -count 1 -timeout 1m ./..."), want: "allow"},
		{name: "test subtree", mode: ModeAcceptEdits, req: bash("go test ./src/..."), want: "allow"},
		{name: "list project", req: bash("go list ./..."), want: "allow"},
		{name: "build project", req: bash("go build ./..."), want: "allow"},
		{name: "vet project", req: bash("go vet ./..."), want: "allow"},
		{name: "test still asks", req: bash("go test ./..."), want: "ask"},
		{name: "plan still denies test", mode: ModePlan, req: bash("go test ./..."), want: "deny"},
		{name: "recursive read deny", deny: []string{"Read(src/**)"}, req: bash("go list ./..."), want: "deny"},
		{name: "recursive test deny", mode: ModeAcceptEdits, deny: []string{"Read(src/**)"}, req: bash("go test ./..."), want: "deny"},
		{name: "file in package deny", deny: []string{"Read(src/a.go)"}, req: bash("go list ./src"), want: "deny"},
		{name: "output directory child deny", mode: ModeAcceptEdits, deny: []string{"Edit(out/coverage.txt)"}, req: bash("go test -outputdir out -coverprofile coverage.txt ./src/..."), want: "deny"},
		{name: "implicit package deny", deny: []string{"Read(main.go)"}, req: bash("go list"), want: "deny"},
		{name: "recursive ask beats allow", mode: ModeBypass, ask: []string{"Read(src/**)"}, allow: []string{"Bash(go list:*)"}, req: bash("go list ./..."), want: "ask"},
		{name: "unrelated read deny", deny: []string{"Read(docs/**)"}, req: bash("go list ./src/..."), want: "allow"},
		{name: "metadata output protected", mode: ModeBypass, req: bash("go test -o ./.git/test.exe ./..."), want: "deny"},
		{name: "list flag is custom in test", mode: ModeBypass, req: bash("go test -f ./.env"), want: "deny"},
		{name: "test prefixed build flag is custom", mode: ModeBypass, req: bash("go test -test.tags ./.env"), want: "deny"},
		{name: "secret protected", mode: ModeBypass, req: bash("go list ./.env/..."), want: "deny"},
		{name: "escaped subtree", req: bash("go list ../other/..."), want: "ask"},
		{name: "symlink escape", req: bash("go list ./link-outdir/..."), want: "ask"},
		{name: "symlink parent escape", req: bash("go list ./link-outdir/../..."), want: "ask"},
		{name: "named import needs location", req: bash("go list example.com/project/..."), want: "ask"},
		{name: "named import authorized test", mode: ModeAcceptEdits, req: bash("go test example.com/project/..."), want: "allow"},
		{name: "named import cannot bypass read deny", mode: ModeAcceptEdits, deny: []string{"Read(src/**)"}, req: bash("go test example.com/project/..."), want: "deny"},
		{name: "overlay asks before test preset", mode: ModeAcceptEdits, req: bash("go test -overlay overlay.json ./..."), want: "ask", why: "replacement paths"},
		{name: "overlay asks before explicit build allow", allow: []string{"Bash(go build:*)"}, req: bash("go build -overlay=overlay.json ./..."), want: "ask", why: "replacement paths"},
		{name: "overlay asks for list", req: bash("go list --overlay=overlay.json ./..."), want: "ask", why: "replacement paths"},
		{name: "overlay is not read only", mode: ModePlan, allow: []string{"Bash(go vet:*)"}, req: bash("go vet -overlay overlay.json ./..."), want: "deny", why: "replacement paths"},
		{name: "overlay after custom flag still asks", mode: ModeAcceptEdits, req: bash("go test -custom value -overlay overlay.json ./src"), want: "ask", why: "replacement paths"},
		{name: "overlay bypass is explicit", mode: ModeBypass, req: bash("go test -overlay overlay.json ./..."), want: "allow"},
	})
	t.Run("state directory is not a package output", func(t *testing.T) {
		e := f.engine(t, Config{Mode: ModeAcceptEdits, StateDir: filepath.Join(f.root, ".sleipnir")})
		for _, command := range []string{"go test ./...", "go test .", "go test", "go test -o .sleipnir/test.exe ./..."} {
			d := e.Check(context.Background(), f.request(bash(command)))
			want := "allow"
			if strings.Contains(command, "-o") {
				want = "ask"
			}
			if got := outcome(d); got != want {
				t.Errorf("%s = %s, want %s: %s", command, got, want, d.Reason)
			}
		}
	})
}
