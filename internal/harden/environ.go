package harden

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// The kernel serves /proc/<pid>/environ from a fixed range of the process's own
// memory: the strings execve copied onto the initial stack ("NAME=value\0" one
// after another). The Go runtime copies them into its own environment at start-up
// and never looks at them again, so os.Setenv and os.Unsetenv leave the range
// as it was, and a same-user reader (or root, which dumpable=0 does not stop) sees
// every variable the harness was started with. The functions here find that range
// and the values worth erasing; the Linux file writes the zeros.

// maxEnvBlock bounds the environment block we are willing to touch: far above the
// kernel's own limit (a quarter of the stack rlimit, 2 MiB by default), so
// anything bigger is not what we think it is.
const maxEnvBlock = 32 << 20

// /proc/<pid>/stat field numbers (proc_pid_stat(5)); the list after the command
// name starts at field 3.
const (
	statEnvStartField = 50
	statEnvEndField   = 51
)

// parseEnvRange returns the [start, end) address range of a process's initial
// environment from the contents of its /proc/<pid>/stat. A process started with
// an empty environment has an empty range (start == end), which is not an error.
func parseEnvRange(stat []byte) (start, end uint64, err error) {
	// The command name (field 2) is in parentheses and may itself hold spaces and
	// parentheses, so everything after the LAST ')' is the space-separated rest.
	i := bytes.LastIndexByte(stat, ')')
	if i < 0 {
		return 0, 0, errors.New("unexpected /proc/self/stat format")
	}
	f := strings.Fields(string(stat[i+1:]))
	if len(f) < statEnvEndField-2 {
		return 0, 0, fmt.Errorf("/proc/self/stat has %d fields after the command name, want at least %d", len(f), statEnvEndField-2)
	}
	start, err1 := strconv.ParseUint(f[statEnvStartField-3], 10, 64)
	end, err2 := strconv.ParseUint(f[statEnvEndField-3], 10, 64)
	switch {
	case err1 != nil || err2 != nil:
		return 0, 0, errors.New("unexpected env_start/env_end in /proc/self/stat")
	case start == 0 || end < start:
		return 0, 0, errors.New("the kernel reports no environment range (env_start/env_end)")
	case end-start > maxEnvBlock:
		return 0, 0, fmt.Errorf("the environment range is %d bytes; refusing to touch it", end-start)
	case end > 1<<62:
		return 0, 0, errors.New("the environment range is outside user space")
	}
	return start, end, nil
}

// span is a run of bytes of the environment block.
type span struct{ off, n int }

// secretSpans returns the value bytes of every "NAME=value" entry of block (NUL
// separated) for which erase(NAME, value) says the value is a credential. Names
// stay: a reader sees that NAME was set, not what it was set to. Entries without
// "=", with an empty name or an empty value are left alone.
func secretSpans(block []byte, erase func(name, value string) bool) []span {
	var out []span
	for off := 0; off < len(block); {
		n := bytes.IndexByte(block[off:], 0)
		if n < 0 {
			n = len(block) - off // an unterminated last entry
		}
		entry := block[off : off+n]
		if eq := bytes.IndexByte(entry, '='); eq > 0 && eq+1 < len(entry) {
			if erase(string(entry[:eq]), string(entry[eq+1:])) {
				out = append(out, span{off: off + eq + 1, n: len(entry) - eq - 1})
			}
		}
		off += n + 1
	}
	return out
}
