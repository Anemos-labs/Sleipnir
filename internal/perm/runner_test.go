package perm

import "testing"

// A prefix rule names a build or test runner: Bash(go test:*) allows the project's tests, not a program the runner is told to run
// in their place. go test -exec PROG, cargo --config (a runner, a compiler wrapper), npm --script-shell, node --require, make SHELL=,
// gradle --init-script and go's -ldflags=-extld take the command off the rule, whoever wrote it (the tests preset, a read-only
// role's profile, the person); the command is then judged as if no rule existed. A read-only reviewer used to be able to run
// go test -exec='rm -rf x' ./... under its Bash(go test:*).
func TestARunnerRuleDoesNotCoverAProgramTheRunnerIsToldToRun(t *testing.T) {
	f := newFixture(t)
	roles := map[string]RoleProfile{"reviewer": {Mode: ModePlan, Allow: []string{"Bash(go test:*)"}}}
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
		{name: "node --test --require", mode: ModeDefault, allow: []string{"Bash(node --test:*)"}, req: bash("node --test --require ./x.js"), want: "ask"},
		{name: "node --test", mode: ModeDefault, allow: []string{"Bash(node --test:*)"}, req: bash("node --test test/"), want: "allow"},
		{name: "node --test with a built-in reporter", mode: ModeDefault, allow: []string{"Bash(node --test:*)"}, req: bash("node --test --test-reporter=spec test/"), want: "allow"},
		{name: "node --test with a reporter module", mode: ModeDefault, allow: []string{"Bash(node --test:*)"}, req: bash("node --test --test-reporter ./mine.js test/"), want: "ask"},
		{name: "make test SHELL=", mode: ModeDefault, allow: []string{"Bash(make test:*)"}, req: bash("make test SHELL=x"), want: "ask"},
		{name: "make test V=1", mode: ModeDefault, allow: []string{"Bash(make test:*)"}, req: bash("make test V=1"), want: "allow"},
		{name: "the tests preset in accept-edits", mode: ModeAcceptEdits, req: bash("go test -exec x ./..."), want: "ask"},
		{name: "the tests preset in accept-edits, plain", mode: ModeAcceptEdits, req: bash("go test ./..."), want: "allow"},
		{name: "a read-only reviewer", mode: ModeBypass, roles: roles, req: as(bash("go test -exec='rm -rf x' ./...")), want: "deny"},
		{name: "a read-only reviewer, plain", mode: ModeBypass, roles: roles, req: as(bash("go test ./...")), want: "allow"},
	}
	runCases(t, f, cases)
}
