package env

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
)

// NetIsolation describes how the local sandbox denies network access to a
// command: a fresh network namespace (`unshare -n`), which leaves the command
// with a loopback interface only. Loopback is raised explicitly because tests
// routinely start servers on 127.0.0.1 and a down loopback would turn every
// such test into a false failure.
type NetIsolation struct {
	Available bool
	// Prefix creates the namespace and ends just before the wrapped command, for
	// example ["/usr/bin/unshare", "-n", "--"].
	Prefix []string
	// Loopback is the argv that raises lo inside the namespace.
	Loopback []string
	// Reason says why isolation is unavailable.
	Reason string
}

// wrap returns argv running inner inside the namespace with loopback up.
func (n NetIsolation) wrap(shell string, inner []string) []string {
	script := shellJoin(n.Loopback) + ` >/dev/null 2>&1; exec "$@"`
	out := append([]string{}, n.Prefix...)
	out = append(out, shell, "-c", script, "sleipnir-netns")
	return append(out, inner...)
}

// shellJoin quotes each argument independently before joining them into a shell command line.
func shellJoin(argv []string) string {
	q := make([]string, len(argv))
	for i, a := range argv {
		q[i] = shellQuote(a)
	}
	return strings.Join(q, " ")
}

// shellQuote quotes s for a POSIX shell.
func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		safe := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_-./=:@%+,", r)
		return !safe
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// detectNetIsolation probes the host. Unprivileged users get a user namespace
// (`unshare -r -n`) when the kernel allows it. Everything that goes wrong
// becomes a Reason, never an error: isolation is optional hardening and the
// caller records the fallback in the run manifest.
func detectNetIsolation(look func(string) (string, error), probe func(ctx context.Context, name string, args ...string) error) NetIsolation {
	if runtime.GOOS != "linux" {
		return NetIsolation{Reason: "only implemented on Linux, not " + runtime.GOOS}
	}
	unshare, err := lookAny(look, "unshare")
	if err != nil {
		return NetIsolation{Reason: "unshare not found"}
	}
	var lo []string
	if ip, err := lookAny(look, "ip", "/sbin/ip", "/usr/sbin/ip", "/bin/ip", "/usr/bin/ip"); err == nil {
		lo = []string{ip, "link", "set", "lo", "up"}
	} else if ifc, err := lookAny(look, "ifconfig", "/sbin/ifconfig", "/usr/sbin/ifconfig"); err == nil {
		lo = []string{ifc, "lo", "up"}
	} else {
		return NetIsolation{Reason: "neither ip nor ifconfig is installed to bring up the loopback interface (tests that bind 127.0.0.1 would fail)"}
	}
	var lastErr error
	for _, flags := range [][]string{{"-n"}, {"-r", "-n"}} {
		iso := NetIsolation{Available: true, Prefix: append(append([]string{unshare}, flags...), "--"), Loopback: lo}
		argv := iso.wrap("/bin/sh", []string{"/bin/sh", "-c", "exit 0"})
		if err := probe(context.Background(), argv[0], argv[1:]...); err != nil {
			lastErr = err
			continue
		}
		return iso
	}
	return NetIsolation{Reason: fmt.Sprintf("unshare cannot create a network namespace here: %v", lastErr)}
}

// lookAny returns the first candidate that resolves: bare names go through look,
// absolute paths are checked directly.
func lookAny(look func(string) (string, error), names ...string) (string, error) {
	var last error
	for _, n := range names {
		if strings.HasPrefix(n, "/") {
			if fi, err := os.Stat(n); err == nil && !fi.IsDir() {
				return n, nil
			}
			continue
		}
		p, err := look(n)
		if err == nil {
			return p, nil
		}
		last = err
	}
	if last == nil {
		last = os.ErrNotExist
	}
	return "", last
}
