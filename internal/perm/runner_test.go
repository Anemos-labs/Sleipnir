package perm

import (
	"context"
	"strings"
	"testing"
)

// A prefix rule names a build or test runner: Bash(go test:*) allows the project's tests, not a program the runner is told to run
// in their place. go test -exec PROG, cargo --config (a runner, a compiler wrapper), npm --script-shell, node --require, make SHELL=,
// gradle --init-script and go's -ldflags=-extld take the command off the rule, whoever wrote it (the tests preset, a read-only
// role's profile, the person); the command is then judged as if no rule existed. A read-only reviewer used to be able to run
// go test -exec='rm -rf x' ./... under its Bash(go test:*).
func TestARunnerRuleDoesNotCoverAProgramTheRunnerIsToldToRun(t *testing.T) {
	f := newFixture(t)
	roles := map[string]RoleProfile{"reviewer": {Mode: ModePlan, Allow: []string{"Bash(go test:*)", "Bash(make test)", "Bash(make test:*)"}}}
	as := func(q rq) rq { q.role = "reviewer"; return q }
	cases := []tc{
		{name: "go test", mode: ModeDefault, allow: []string{"Bash(go test:*)"}, req: bash("go test ./..."), want: "allow"},
		{name: "go test -exec", mode: ModeDefault, allow: []string{"Bash(go test:*)"}, req: bash("go test -exec='rm -rf x' ./..."), want: "ask"},
		{name: "go test -exec as a separate word", mode: ModeDefault, allow: []string{"Bash(go test:*)"}, req: bash("go test -exec env ./..."), want: "ask"},
		{name: "go test --toolexec", mode: ModeDefault, allow: []string{"Bash(go test:*)"}, req: bash("go test --toolexec=x ./..."), want: "ask"},
		{name: "go build -ldflags -X", mode: ModeDefault, allow: []string{"Bash(go build:*)"}, req: bash(`go build -ldflags "-s -w -X main.version=1" ./...`), want: "allow"},
		{name: "go build -ldflags -extld", mode: ModeDefault, allow: []string{"Bash(go build:*)"}, req: bash("go build -ldflags=-extld=x ./..."), want: "ask"},
		{name: "cargo test", mode: ModeDefault, allow: []string{"Bash(cargo test:*)"}, req: bash("cargo test --all"), want: "allow"},
		{name: "cargo test --config runner", mode: ModeDefault, allow: []string{"Bash(cargo test:*)"}, req: bash(`cargo test --config 'target.x.runner="sh"'`), want: "ask"},
		{name: "npm test --script-shell", mode: ModeDefault, allow: []string{"Bash(npm test:*)"}, req: bash("npm test --script-shell=x"), want: "ask"},
		// a configuration file sets script-shell and node-options as well as the options do; /dev/stdin is a file
		{name: "npm test --userconfig", mode: ModeDefault, allow: []string{"Bash(npm test:*)"}, req: bash("npm test --userconfig=/dev/stdin"), want: "ask"},
		{name: "npm test --userconfig apart", mode: ModeDefault, allow: []string{"Bash(npm test:*)"}, req: bash("npm test --userconfig ./x.npmrc"), want: "ask"},
		{name: "npm run test --globalconfig", mode: ModeDefault, allow: []string{"Bash(npm run test:*)"}, req: bash("npm run test --globalconfig=./x"), want: "ask"},
		{name: "pnpm test --userconfig", mode: ModeDefault, allow: []string{"Bash(pnpm test:*)"}, req: bash("pnpm test --userconfig=./x"), want: "ask"},
		{name: "yarn test --globalconfig", mode: ModeDefault, allow: []string{"Bash(yarn test:*)"}, req: bash("yarn test --globalconfig ./x"), want: "ask"},
		{name: "npm test with its own options", mode: ModeDefault, allow: []string{"Bash(npm test:*)"}, req: bash("npm test -- --watch=false"), want: "allow"},
		{name: "node --test --require", mode: ModeDefault, allow: []string{"Bash(node --test:*)"}, req: bash("node --test --require ./x.js"), want: "ask"},
		{name: "node --test", mode: ModeDefault, allow: []string{"Bash(node --test:*)"}, req: bash("node --test test/"), want: "allow"},
		{name: "node --test with a built-in reporter", mode: ModeDefault, allow: []string{"Bash(node --test:*)"}, req: bash("node --test --test-reporter=spec test/"), want: "allow"},
		{name: "node --test with a reporter module", mode: ModeDefault, allow: []string{"Bash(node --test:*)"}, req: bash("node --test --test-reporter ./mine.js test/"), want: "ask"},
		{name: "make test SHELL=", mode: ModeDefault, allow: []string{"Bash(make test:*)"}, req: bash("make test SHELL=x"), want: "ask"},
		{name: "make test V=1", mode: ModeDefault, allow: []string{"Bash(make test:*)"}, req: bash("make test V=1"), want: "allow"},
		{name: "make test -j4", mode: ModeDefault, allow: []string{"Bash(make test:*)"}, req: bash("make test -j4"), want: "allow"},
		{name: "make test SHELL:=", mode: ModeDefault, allow: []string{"Bash(make test:*)"}, req: bash("make test SHELL:=/tmp/x"), want: "ask"},
		{name: "make test SHELL?=", mode: ModeDefault, allow: []string{"Bash(make test:*)"}, req: bash("make test SHELL?=x"), want: "ask"},
		{name: "make test .SHELLFLAGS+=", mode: ModeDefault, allow: []string{"Bash(make test:*)"}, req: bash("make test .SHELLFLAGS+=-c"), want: "ask"},
		{name: "make test SHELLCHECK=1", mode: ModeDefault, allow: []string{"Bash(make test:*)"}, req: bash("make test SHELLCHECK=1"), want: "allow"},
		{name: "make test -f attached", mode: ModeDefault, allow: []string{"Bash(make test:*)"}, req: bash("make test -fother.mk"), want: "ask"},
		{name: "make test -f in a cluster", mode: ModeDefault, allow: []string{"Bash(make test:*)"}, req: bash("make test -sf other.mk"), want: "ask"},
		{name: "make test -E attached", mode: ModeDefault, allow: []string{"Bash(make test:*)"}, req: bash("make test -E'x:=1'"), want: "ask"},
		{name: "cargo test -Z attached", mode: ModeDefault, allow: []string{"Bash(cargo test:*)"}, req: bash("cargo test -Zunstable-options"), want: "ask"},
		{name: "the tests preset, make -f attached", mode: ModeAcceptEdits, req: bash("make test -fother.mk"), want: "ask"},
		{name: "a read-only reviewer, make -f attached", mode: ModeBypass, roles: roles, req: as(bash("make test -fother.mk")), want: "deny"},
		{name: "the tests preset in accept-edits", mode: ModeAcceptEdits, req: bash("go test -exec x ./..."), want: "ask"},
		{name: "the tests preset in accept-edits, plain", mode: ModeAcceptEdits, req: bash("go test ./..."), want: "allow"},
		{name: "a read-only reviewer", mode: ModeBypass, roles: roles, req: as(bash("go test -exec='rm -rf x' ./...")), want: "deny"},
		{name: "a read-only reviewer, plain", mode: ModeBypass, roles: roles, req: as(bash("go test ./...")), want: "allow"},
	}
	runCases(t, f, cases)
}

// What a refused runner option is told matches the decision: the tests preset does not allow go test -exec PROG, so the question does
// not say that it would.
func TestARunnerOptionIsNotSaidToBeAllowedByTheTestsPreset(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t, Config{Mode: ModeDefault})
	d := e.Check(context.Background(), f.request(bash("go test -exec x ./...")))
	if d.Allow || strings.Contains(d.Reason, "tests preset") {
		t.Errorf("go test -exec x: %+v", d)
	}
	if d := e.Check(context.Background(), f.request(bash("go test ./..."))); d.Allow || !strings.Contains(d.Reason, "tests preset") {
		t.Errorf("go test ./... is no longer said to be allowed by the tests preset: %+v", d)
	}
}
