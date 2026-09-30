package hooks

import (
	"reflect"
	"strings"
	"testing"
)

func TestScrubEnv(t *testing.T) {
	// The URL with a password in it is assembled here so that no credential-shaped
	// literal sits in the source.
	urlWithPassword := "postgres://" + "app" + ":" + "pw" + "@db.internal/app"
	base := []string{
		"PATH=/usr/bin", "HOME=/home/u", "USER=u", "LANG=C.UTF-8", "TERM=xterm", "PWD=/work", "SHELL=/bin/sh", "TMPDIR=/tmp",
		"GOPATH=/go", "PYTHONPATH=/py", "MANPATH=/man", "GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=a@example.com", "SLEIPNIR_MODEL=x/y",
		"EDITOR=vi", "LC_ALL=C", "KEYBOARD_LAYOUT=us", "AUTHOR=ada", "SESSION_MANAGER=x",

		"HEIMDALL_API_KEY=canary-not-a-secret", "OPENAI_API_KEY=canary", "ANTHROPIC_AUTH_TOKEN=canary", "api-key=canary",
		"AWS_ACCESS_KEY_ID=canary", "AWS_SECRET_ACCESS_KEY=canary", "AWS_SESSION_TOKEN=canary", "GITHUB_TOKEN=canary", "GH_PAT=canary",
		"NPM_TOKEN=canary", "MY_PASSWORD=canary", "DB_PASSWD=canary", "MYSQL_PWD=canary", "PGPASSWORD=canary", "CLIENT_SECRET=canary",
		"GOOGLE_APPLICATION_CREDENTIALS=/creds.json", "SIGNING_KEY=canary", "STRIPE_KEY=canary", "KEY=canary", "SENTRY_DSN=canary",
		"SSH_AUTH_SOCK=/tmp/agent.sock", "HTTP_COOKIE=canary", "AUTHORIZATION=canary", "BEARER_VALUE=canary",
		"DATABASE_URL=postgres://db.internal/app", "REDIS_URL=redis://cache", "MONGODB_URI=mongodb://x", "AMQP_URL=amqp://x",
		"AZURE_STORAGE_CONNECTION_STRING=canary", "PRIVATE_KEY=canary", "MYAPP_URL=" + urlWithPassword,

		"noequalsign", "=novalue", "BAD\x00NAME=x",
	}
	got := scrubEnv(base, nil, nil)
	kept := map[string]bool{}
	for _, kv := range got {
		name, _, _ := strings.Cut(kv, "=")
		kept[name] = true
	}
	for _, name := range []string{"PATH", "HOME", "USER", "LANG", "TERM", "PWD", "SHELL", "TMPDIR", "GOPATH", "PYTHONPATH", "MANPATH",
		"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "SLEIPNIR_MODEL", "EDITOR", "LC_ALL", "KEYBOARD_LAYOUT", "AUTHOR", "SESSION_MANAGER"} {
		if !kept[name] {
			t.Errorf("%s was scrubbed but is harmless", name)
		}
	}
	for _, name := range []string{"HEIMDALL_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_AUTH_TOKEN", "api-key", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY",
		"AWS_SESSION_TOKEN", "GITHUB_TOKEN", "GH_PAT", "NPM_TOKEN", "MY_PASSWORD", "DB_PASSWD", "MYSQL_PWD", "PGPASSWORD", "CLIENT_SECRET",
		"GOOGLE_APPLICATION_CREDENTIALS", "SIGNING_KEY", "STRIPE_KEY", "KEY", "SENTRY_DSN", "SSH_AUTH_SOCK", "HTTP_COOKIE", "AUTHORIZATION",
		"BEARER_VALUE", "DATABASE_URL", "REDIS_URL", "MONGODB_URI", "AMQP_URL", "AZURE_STORAGE_CONNECTION_STRING", "PRIVATE_KEY", "MYAPP_URL"} {
		if kept[name] {
			t.Errorf("%s reached the hook", name)
		}
	}
	for _, bad := range []string{"noequalsign", "", "BAD\x00NAME"} {
		if kept[bad] {
			t.Errorf("malformed entry %q survived", bad)
		}
	}
}

func TestScrubEnvPassAndDeny(t *testing.T) {
	base := []string{"GITHUB_TOKEN=canary", "GITHUB_ENTERPRISE_TOKEN=canary", "OPENAI_API_KEY=canary", "PATH=/bin", "CUSTOM_BACKEND_VAR=x", "custom_backend_thing=y"}
	got := scrubEnv(base, []string{"github_token", "GITHUB_ENT*"}, []string{"custom_backend_*", "path"})
	want := []string{"GITHUB_TOKEN=canary", "GITHUB_ENTERPRISE_TOKEN=canary"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	// Deny wins over pass.
	if got := scrubEnv([]string{"X_TOKEN=1"}, []string{"X_TOKEN"}, []string{"X_TOKEN"}); len(got) != 0 {
		t.Errorf("deny must win over pass: %v", got)
	}
	// Blank entries in the lists are ignored, not treated as "match everything".
	if got := scrubEnv([]string{"A_TOKEN=1", "B=2"}, []string{"", "  "}, []string{""}); !reflect.DeepEqual(got, []string{"B=2"}) {
		t.Errorf("got %v", got)
	}
}

func TestHookEnvForcesTheHarnessVariables(t *testing.T) {
	r := &Runner{
		Dir: "/project", SessionID: "sess-9",
		Env: []string{"PATH=/bin", "SLEIPNIR_AGENT=stale", "SLEIPNIR_ROLE=stale", "CLAUDE_PROJECT_DIR=/elsewhere", "no_color=0", "SLEIPNIR_SESSION_ID=stale", "OPENAI_API_KEY=canary"},
	}
	env := r.hookEnv(PreToolUse, Event{Agent: "be-1", Role: "backend"})
	m := map[string]string{}
	count := map[string]int{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		m[strings.ToUpper(k)] = v
		count[strings.ToUpper(k)]++
	}
	for k, want := range map[string]string{
		"PATH": "/bin", "SLEIPNIR_PROJECT_DIR": "/project", "CLAUDE_PROJECT_DIR": "/project", "SLEIPNIR_HOOK_EVENT": PreToolUse,
		"SLEIPNIR_SESSION_ID": "sess-9", "SLEIPNIR_AGENT": "be-1", "SLEIPNIR_ROLE": "backend", "NO_COLOR": "1", "GIT_TERMINAL_PROMPT": "0",
	} {
		if m[k] != want || count[k] != 1 {
			t.Errorf("%s = %q (x%d), want %q once", k, m[k], count[k], want)
		}
	}
	if _, leaked := m["OPENAI_API_KEY"]; leaked {
		t.Error("the provider key reached the hook")
	}
	// Without an agent, a stale agent name must not survive either.
	env = r.hookEnv(Stop, Event{})
	for _, kv := range env {
		if strings.HasPrefix(kv, "SLEIPNIR_AGENT=") || strings.HasPrefix(kv, "SLEIPNIR_ROLE=") {
			t.Errorf("stale %s survived", kv)
		}
	}
}
