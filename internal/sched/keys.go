package sched

import (
	"bytes"
	"os"
	"os/exec"
	"regexp"
	"sort"

	"github.com/anemos-labs/sleipnir/internal/harden"
)

// Keys for a child Sleipnir. A scheduled job, and a command the web runner starts, is Sleipnir itself, and it needs the provider keys
// the parent holds (harden.MoveKeys took them out of the parent's environment). They never travel in the child's environment: on
// Linux a same-user process reads /proc/<pid>/environ of any dumpable process, and a child is dumpable until its own hardening runs.
// Where pipes can be inherited (Unix), PassKeys hands them over an inherited pipe (KeysFDEnv names its descriptor) that the parent
// writes and closes right after the start, and the child's ReceiveKeys reads them first thing in main, before anything reads the
// environment. Elsewhere (Windows, which has no /proc) they are put in the child's environment, as JobEnv does.

// KeysFDEnv is the variable that tells a child Sleipnir which inherited descriptor carries its keys. It holds a number, never a key.
const KeysFDEnv = "SLEIPNIR_KEYS_FD"

// maxKeysPayload bounds what a child reads from the key pipe.
const maxKeysPayload = 1 << 20

// keyNameRE is what a variable name of a passed key may be.
var keyNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// heldPayload is the held keys as lines NAME=value, sorted (a value with a NUL or a line break cannot be passed and is left out).
func heldPayload() []byte {
	names := harden.Held()
	sort.Strings(names)
	var b bytes.Buffer
	for _, name := range names {
		v, ok := harden.LookupSecret(name)
		if !ok || v == "" || !keyNameRE.MatchString(name) || bytes.ContainsAny([]byte(v), "\x00\r\n") {
			continue
		}
		b.WriteString(name + "=" + v + "\n")
	}
	return b.Bytes()
}

// parsePayload reads the lines NAME=value of a key pipe.
func parsePayload(b []byte) [][2]string {
	var out [][2]string
	for _, line := range bytes.Split(b, []byte("\n")) {
		name, value, ok := bytes.Cut(line, []byte("="))
		if !ok || len(value) == 0 || !keyNameRE.Match(name) {
			continue
		}
		out = append(out, [2]string{string(name), string(value)})
	}
	return out
}

// ReceiveKeys takes the keys a parent Sleipnir passed (PassKeys) and puts them in this process's environment, where
// harden.MoveKeys expects them: the process is then in the state it would be in had it been started with them, except that they were
// never in the environment block the kernel shows. It unsets KeysFDEnv and closes the descriptor, and returns the names, for
// harden.MoveKeys to move whatever they look like. Call it first thing in main, before harden.Process; without KeysFDEnv it does
// nothing.
func ReceiveKeys() []string {
	v, ok := os.LookupEnv(KeysFDEnv)
	if !ok {
		return nil
	}
	_ = os.Unsetenv(KeysFDEnv)
	b := readKeysFD(v)
	var names []string
	for _, kv := range parsePayload(b) {
		if err := os.Setenv(kv[0], kv[1]); err == nil {
			names = append(names, kv[0])
		}
	}
	return names
}

// PassKeys arranges for the child Sleipnir cmd starts to receive the held provider keys without its environment. Call it before
// cmd.Start, and call the function it returns right after, with Start's error: it closes the parent's copy of the child's end and
// writes the keys (in the background, given up after a few seconds) on a pipe the child reads at its start. With no key held it
// changes nothing. cmd.Env nil means the parent's environment, as for exec.
func PassKeys(cmd *exec.Cmd) (started func(startErr error)) {
	payload := heldPayload()
	if len(payload) == 0 {
		return func(error) {}
	}
	return passKeys(cmd, payload)
}
