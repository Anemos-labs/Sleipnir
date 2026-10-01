package harden

import (
	"bytes"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Credential-shaped values are assembled from fragments: no literal in this file
// looks like a real token to a secret scanner.
var rep = strings.Repeat

func getenv(name string) string { return os.Getenv(name) }

func TestLooksSecret(t *testing.T) {
	secret := []string{
		"OPENAI_API_KEY=x", "anthropic_api-key=x", "MY_SECRET=x", "GITHUB_TOKEN=x", "DB_PASSWORD=x", "DB_PASSWD=x",
		"AWS_SESSION_TOKEN=x", "STRIPE_KEY=x", "HEIMDALL_KEY=x", "KEY=x", "PRIVATE_KEY=x", "MYSQL_PWD=x", "GH_PAT=x",
		"SENTRY_DSN=x", "AUTHORIZATION=x", "COOKIE=x", "SSH_AUTH_SOCK=/tmp/agent.1",
		"DATABASE_URL=postgres://app:hunter2@db:5432/prod", "REDIS_URL=redis://:hunter2@cache",
		"X=" + "sk" + "-ant-api03-" + rep("a", 22),
		"X=" + "gh" + "p_" + rep("a", 36),
		"X=" + "-----BEGIN " + "RSA PRIVATE" + " KEY-----",
	}
	plain := []string{
		"PATH=/usr/bin:/bin", "HOME=/home/u", "PWD=/work", "SHELL=/bin/bash", "LANG=C.UTF-8", "TERM=xterm",
		"MONKEY=banana", "KEYBOARD=us", "PASSENGERS=3", "GOPATH=/go", "CI=true", "EMPTY=", "SK=sk-short",
		"HTTPS_PROXY=http://user:pw@proxy.corp:3128", "http_proxy=http://user:pw@proxy:3128",
	}
	for _, kv := range secret {
		name, val, _ := strings.Cut(kv, "=")
		if !LooksSecret(name, val) {
			t.Errorf("%s should look secret", name)
		}
	}
	for _, kv := range plain {
		name, val, _ := strings.Cut(kv, "=")
		if LooksSecret(name, val) {
			t.Errorf("%s should pass", kv)
		}
	}
}

// The erasure net is wider than the scrub: it costs nothing to blank a variable in /proc.
func TestShouldErase(t *testing.T) {
	for _, kv := range []string{
		"HEIMDALL_API_KEY=x", "OPENROUTER_API_KEY=x", "GITHUB_TOKEN=x", "AWS_ACCESS_KEY_ID=" + "AK" + "IA" + rep("x", 16), "AWS_SECRET_ACCESS_KEY=x",
		"SSH_AUTH_SOCK=/tmp/a", "NPM_CONFIG__AUTH=x", "MY_SERVICE_PASSWORD=x", "SIGNATURE=x", "DATABASE_URL=postgres://u:p@h/d",
		"UNRELATED=" + "gh" + "p_" + rep("b", 36),
	} {
		name, val, _ := strings.Cut(kv, "=")
		if !shouldErase(name, val) {
			t.Errorf("%s should be erased", name)
		}
	}
	for _, kv := range []string{
		"PATH=/usr/bin", "HOME=/root", "PWD=/work", "LANG=C", "TERM=xterm", "SHELL=/bin/sh", "USER=me", "LOGNAME=me", "TMPDIR=/tmp",
		"EDITOR=vim", "GOFLAGS=-mod=mod", "HTTPS_PROXY=http://proxy:3128", "SLEIPNIR_DUMPABLE=1", "SLEIPNIR_HOME=/x",
	} {
		name, val, _ := strings.Cut(kv, "=")
		if shouldErase(name, val) {
			t.Errorf("%s should be left alone", name)
		}
	}
}

func block(entries ...string) []byte {
	var b bytes.Buffer
	for _, e := range entries {
		b.WriteString(e)
		b.WriteByte(0)
	}
	return b.Bytes()
}

// erased applies secretSpans the way eraseEnviron does and returns the block.
func erased(b []byte, erase func(name, value string) bool) ([]byte, int) {
	out := append([]byte(nil), b...)
	spans := secretSpans(b, erase)
	for _, s := range spans {
		clear(out[s.off : s.off+s.n])
	}
	return out, len(spans)
}

func TestSecretSpansErasesOnlyTheValuesOfSecretEntries(t *testing.T) {
	isKey := func(name, _ string) bool { return strings.HasSuffix(name, "_KEY") }
	tests := []struct {
		name string
		in   []byte
		want []byte
		n    int
	}{
		// the value's seven bytes become NULs (the entry's own terminator makes eight); the layout does not move
		{"one secret between plain entries", block("A=1", "X_KEY=hunter2", "B=2"), []byte("A=1\x00X_KEY=\x00\x00\x00\x00\x00\x00\x00\x00B=2\x00"), 1},
		{"nothing to erase", block("A=1", "B=2"), block("A=1", "B=2"), 0},
		{"empty block", nil, nil, 0},
		{"entry without equals", block("NOEQUALS", "X_KEY=v"), block("NOEQUALS", "X_KEY=\x00"), 1},
		{"empty name is not an entry", block("=X_KEY", "X_KEY=v"), block("=X_KEY", "X_KEY=\x00"), 1},
		{"empty value stays", block("X_KEY=", "A=b"), block("X_KEY=", "A=b"), 0},
		{"unterminated last entry", []byte("A=1\x00X_KEY=abc"), []byte("A=1\x00X_KEY=\x00\x00\x00"), 1},
		{"value containing equals signs", block("X_KEY=a=b=c"), block("X_KEY=\x00\x00\x00\x00\x00"), 1},
		{"non-utf8 value", block("X_KEY=\xff\xfe\xfd"), block("X_KEY=\x00\x00\x00"), 1},
		{"consecutive NULs (already erased)", []byte("X_KEY=\x00\x00\x00\x00A=1\x00"), []byte("X_KEY=\x00\x00\x00\x00A=1\x00"), 0},
		{"same name twice", block("X_KEY=1", "X_KEY=22"), block("X_KEY=\x00", "X_KEY=\x00\x00"), 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, n := erased(tt.in, isKey)
			if n != tt.n || !bytes.Equal(got, tt.want) {
				t.Fatalf("erased %d entries, block = %q\nwant %d, %q", n, got, tt.n, tt.want)
			}
			if len(got) != len(tt.in) {
				t.Fatalf("the block changed size: %d -> %d", len(tt.in), len(got))
			}
		})
	}
}

func TestSecretSpansCoverExactlyTheValues(t *testing.T) {
	b := block("HOME=/root", "API_KEY=abcdef", "PATH=/bin", "TOKEN=x")
	all := func(name, _ string) bool { return name == "API_KEY" || name == "TOKEN" }
	spans := secretSpans(b, all)
	var got []string
	for _, s := range spans {
		got = append(got, string(b[s.off:s.off+s.n]))
	}
	if want := []string{"abcdef", "x"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("spans cover %q, want %q", got, want)
	}
}

// A large environment is scanned in linear time (a real one is a few kilobytes; the kernel allows
// megabytes). The block is 100 KB, about 4,000 entries: enough to show an accidental rescan from the
// start of the block, small enough to stay fast under the race detector.
//
// The bound catches a scan that runs away, not a slow one: the regular expressions cost a few
// seconds under the race detector on a machine that is doing other work (three suites at once took
// sixteen), against a tenth of a second in a normal run.
func TestSecretSpansOnALargeBlock(t *testing.T) {
	var b bytes.Buffer
	want := 0
	for i := 0; b.Len() < 100<<10; i++ {
		fmt.Fprintf(&b, "VAR_%d=value-%d\x00", i, i)
		if i%50 == 0 {
			fmt.Fprintf(&b, "SERVICE_%d_TOKEN=abc%d\x00", i, i)
			want++
		}
	}
	start := time.Now()
	spans := secretSpans(b.Bytes(), shouldErase)
	if d := time.Since(start); d > 2*time.Minute {
		t.Fatalf("scanning %d bytes took %v", b.Len(), d)
	}
	if len(spans) != want {
		t.Fatalf("%d spans, want %d", len(spans), want)
	}
}

func TestParseEnvRange(t *testing.T) {
	// proc_pid_stat(5): env_start is field 50 and env_end field 51; the fields after the command name start at 3.
	line := func(comm string, start, end string) []byte {
		f := make([]string, 0, 60)
		for i := 3; i <= 52; i++ {
			switch i {
			case 50:
				f = append(f, start)
			case 51:
				f = append(f, end)
			default:
				f = append(f, "0")
			}
		}
		return []byte("1234 (" + comm + ") " + strings.Join(f, " ") + "\n")
	}
	tests := []struct {
		name       string
		in         []byte
		start, end uint64
		bad        bool
	}{
		{"plain", line("harden.test", "140721126367606", "140721126375377"), 140721126367606, 140721126375377, false},
		{"command name with spaces", line("my prog", "100", "200"), 100, 200, false},
		{"command name with a closing parenthesis", line("a) S 1 2 3 (b", "100", "200"), 100, 200, false},
		{"command name that is only parentheses", line(")))(((", "100", "200"), 100, 200, false},
		{"no range reported", line("x", "0", "0"), 0, 0, true},
		{"end before start", line("x", "200", "100"), 0, 0, true},
		{"empty range: a process started with no environment", line("x", "100", "100"), 100, 100, false},
		{"not a number", line("x", "abc", "200"), 0, 0, true},
		{"negative", line("x", "-5", "200"), 0, 0, true},
		{"absurdly large block", line("x", "100", "999999999999"), 0, 0, true},
		{"outside user space", line("x", "9223372036854775800", "9223372036854775900"), 0, 0, true},
		{"too few fields", []byte("1 (x) S 1 2 3"), 0, 0, true},
		{"no parenthesis", []byte("garbage without a command name"), 0, 0, true},
		{"empty", nil, 0, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, e, err := parseEnvRange(tt.in)
			if tt.bad {
				if err == nil {
					t.Fatalf("accepted %q as [%d, %d)", tt.in, s, e)
				}
				return
			}
			if err != nil || s != tt.start || e != tt.end {
				t.Fatalf("parseEnvRange = [%d, %d), %v; want [%d, %d)", s, e, err, tt.start, tt.end)
			}
		})
	}
}

// ---- the vault (in-process: applyForTest never touches the OS-level state) ----------------

func resetVault(t *testing.T) {
	t.Helper()
	reset := func() {
		mu.Lock()
		moving, held, named = false, nil, nil
		mu.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

func applyForTest(opts ...Option) {
	var s settings
	for _, o := range opts {
		o(&s)
	}
	mu.Lock()
	applyLocked(s)
	mu.Unlock()
}

func TestSecretWithoutMoveKeysIsGetenv(t *testing.T) {
	resetVault(t)
	t.Setenv("HARDEN_T_API_KEY", "v1")
	if got := Secret("HARDEN_T_API_KEY"); got != "v1" {
		t.Fatalf("Secret = %q", got)
	}
	if v, ok := LookupSecret("HARDEN_T_MISSING_API_KEY"); ok || v != "" {
		t.Fatalf("LookupSecret of an unset variable = %q, %v", v, ok)
	}
	// Reading did not move anything: os.Getenv still sees it and nothing is held.
	if got := getenv("HARDEN_T_API_KEY"); got != "v1" || len(Held()) != 0 {
		t.Fatalf("Secret without MoveKeys changed state: env=%q held=%v", got, Held())
	}
	t.Setenv("HARDEN_T_API_KEY", "v2") // no stale copy: the next read sees the new value
	if got := Secret("HARDEN_T_API_KEY"); got != "v2" {
		t.Fatalf("Secret = %q, want the new value", got)
	}
}

func TestMoveKeysTakesProviderKeysOutOfTheEnvironment(t *testing.T) {
	resetVault(t)
	t.Setenv("HARDEN_T_HEIMDALL_API_KEY", "k1")
	t.Setenv("HARDEN_T_other-api-key", "k2")
	t.Setenv("HARDEN_T_APIKEY", "k3")
	t.Setenv("HARDEN_T_PLAIN", "visible")
	t.Setenv("HARDEN_T_TOKEN", "not a provider key name")
	t.Setenv("HARDEN_T_CUSTOM", "named explicitly")

	applyForTest(MoveKeys("HARDEN_T_CUSTOM"))

	for _, name := range []string{"HARDEN_T_HEIMDALL_API_KEY", "HARDEN_T_other-api-key", "HARDEN_T_APIKEY", "HARDEN_T_CUSTOM"} {
		if v := getenv(name); v != "" {
			t.Errorf("%s is still in the environment", name)
		}
	}
	for name, want := range map[string]string{"HARDEN_T_PLAIN": "visible", "HARDEN_T_TOKEN": "not a provider key name"} {
		if v := getenv(name); v != want {
			t.Errorf("%s = %q, want it untouched (%q)", name, v, want)
		}
	}
	for name, want := range map[string]string{"HARDEN_T_HEIMDALL_API_KEY": "k1", "HARDEN_T_other-api-key": "k2", "HARDEN_T_APIKEY": "k3", "HARDEN_T_CUSTOM": "named explicitly"} {
		if got := Secret(name); got != want {
			t.Errorf("Secret(%s) = %q, want %q", name, got, want)
		}
	}
	// Held is sorted and lists names only.
	want := []string{"HARDEN_T_APIKEY", "HARDEN_T_CUSTOM", "HARDEN_T_HEIMDALL_API_KEY", "HARDEN_T_other-api-key"}
	if got := Held(); !reflect.DeepEqual(got, want) {
		t.Errorf("Held = %v, want %v", got, want)
	}
}

// After MoveKeys, reading a credential the configuration names moves it too, and it stays readable.
func TestSecretMovesWhatItReadsOnceMoving(t *testing.T) {
	resetVault(t)
	applyForTest(MoveKeys())
	t.Setenv("HARDEN_T_CUSTOM_PROVIDER_TOKEN", "tok")
	if got := Secret("HARDEN_T_CUSTOM_PROVIDER_TOKEN"); got != "tok" {
		t.Fatalf("first read = %q", got)
	}
	if v := getenv("HARDEN_T_CUSTOM_PROVIDER_TOKEN"); v != "" {
		t.Fatalf("still in the environment after the read: %q", v)
	}
	if got := Secret("HARDEN_T_CUSTOM_PROVIDER_TOKEN"); got != "tok" {
		t.Fatalf("second read = %q", got)
	}
	if v, ok := LookupSecret("HARDEN_T_NEVER_SET"); ok || v != "" {
		t.Fatalf("an unset variable = %q, %v", v, ok)
	}
	// A variable set again after the move wins over the held copy, and is moved in turn.
	t.Setenv("HARDEN_T_CUSTOM_PROVIDER_TOKEN", "rotated")
	if got := Secret("HARDEN_T_CUSTOM_PROVIDER_TOKEN"); got != "rotated" {
		t.Fatalf("after rotation Secret = %q", got)
	}
	if got := Secret("HARDEN_T_CUSTOM_PROVIDER_TOKEN"); got != "rotated" {
		t.Fatalf("after rotation, second read = %q", got)
	}
}

// A caller that passes an ordinary variable name to Secret by mistake must not empty the
// environment of it: only credentials (by name or value, or named to MoveKeys) are moved.
func TestSecretNeverMovesAnOrdinaryVariable(t *testing.T) {
	resetVault(t)
	t.Setenv("HARDEN_T_PLAIN_SETTING", "keep-me")
	t.Setenv("HARDEN_T_ODD_NAME", "declared a credential")
	t.Setenv("HARDEN_T_SNEAKY", "postgres://u:"+"pw@host/db") // a value that gives it away
	applyForTest(MoveKeys("HARDEN_T_ODD_NAME"))
	if got := Secret("HARDEN_T_PLAIN_SETTING"); got != "keep-me" || getenv("HARDEN_T_PLAIN_SETTING") != "keep-me" {
		t.Fatalf("an ordinary variable was moved: Secret=%q env=%q", got, getenv("HARDEN_T_PLAIN_SETTING"))
	}
	if got := Secret("PATH"); got != getenv("PATH") || getenv("PATH") == "" {
		t.Fatalf("PATH was touched: %q", getenv("PATH"))
	}
	if got := Secret("HARDEN_T_SNEAKY"); got == "" || getenv("HARDEN_T_SNEAKY") != "" {
		t.Errorf("a credential-shaped value should be moved on its first read: Secret=%q env=%q", got, getenv("HARDEN_T_SNEAKY"))
	}
	if got := Secret("HARDEN_T_ODD_NAME"); got != "declared a credential" {
		t.Errorf("Secret of a variable named to MoveKeys = %q", got)
	}
	if v := getenv("HARDEN_T_ODD_NAME"); v != "" {
		t.Errorf("a variable named to MoveKeys is still in the environment")
	}
	if got, want := Held(), []string{"HARDEN_T_ODD_NAME", "HARDEN_T_SNEAKY"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Held = %v, want %v", got, want)
	}
}

func TestMoveKeysIsIdempotentAndEmptyValuesMove(t *testing.T) {
	resetVault(t)
	t.Setenv("HARDEN_T_EMPTY_API_KEY", "")
	t.Setenv("HARDEN_T_X_API_KEY", "v")
	applyForTest(MoveKeys())
	applyForTest(MoveKeys())
	if v, ok := LookupSecret("HARDEN_T_EMPTY_API_KEY"); !ok || v != "" {
		t.Fatalf("an empty key = %q, %v; it was set, so it stays set", v, ok)
	}
	if got := Secret("HARDEN_T_X_API_KEY"); got != "v" {
		t.Fatalf("Secret = %q after moving twice", got)
	}
}

func TestSecretIsSafeForConcurrentUse(t *testing.T) {
	resetVault(t)
	t.Setenv("HARDEN_T_RACE_API_KEY", "v")
	applyForTest(MoveKeys())
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 200; j++ {
				if Secret("HARDEN_T_RACE_API_KEY") != "v" {
					t.Error("lost the key")
				}
				Held()
			}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
}

func TestDumpableRequested(t *testing.T) {
	for v, want := range map[string]bool{"": false, "0": false, "1": true, " 1 ": true, "true": true, "YES": true, "on": true, "no": false, "2": false, "off": false} {
		t.Setenv("SLEIPNIR_DUMPABLE", v)
		if got := dumpableRequested(); got != want {
			t.Errorf("SLEIPNIR_DUMPABLE=%q -> %v, want %v", v, got, want)
		}
	}
}

func TestProvideHoldsAStoredKeyInMemoryAndTheEnvironmentWins(t *testing.T) {
	const name = "ACME_PROVIDE_API_KEY"
	t.Setenv(name, "")
	t.Cleanup(func() { Provide(name, "") })
	Provide(name, "stored")
	if got := Secret(name); got != "stored" {
		t.Fatalf("an empty variable is not a word: %q", got)
	}
	if os.Getenv(name) != "" {
		t.Error("a stored key must not appear in the environment: commands inherit it")
	}
	t.Setenv(name, "from-env")
	if got := Secret(name); got != "from-env" {
		t.Errorf("the environment wins: %q", got)
	}
	Provide(name, "other")
	if got := Secret(name); got != "from-env" {
		t.Errorf("a set variable is not replaced: %q", got)
	}
}
