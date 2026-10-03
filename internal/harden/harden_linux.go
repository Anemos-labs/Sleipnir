//go:build linux

package harden

import (
	"bytes"
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// platformHarden erases selected Linux environment bytes before disabling dumpability, recording
// failures and respecting the dumpability opt-out.
func platformHarden(optOut bool) Status {
	var st Status
	// Erase first: opening /proc/self/mem needs the process to be dumpable, which
	// the next step ends (for everyone but root, which can read it anyway).
	n, err := eraseEnviron(shouldErase)
	st.EnvErased = n
	if err != nil {
		st.Notes = append(st.Notes, "environment not erased in memory: "+err.Error())
	}
	if optOut {
		st.OptedOut = true
		return st
	}
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		st.Notes = append(st.Notes, "prctl(PR_SET_DUMPABLE): "+err.Error())
	} else {
		st.Protected = true
	}
	return st
}

// eraseEnviron overwrites, in this process's initial environment block, the value
// of every entry for which erase says so, and reports how many it erased. It
// writes through /proc/self/mem (an error there is an error, not a crash) and only
// after checking that the bytes it is about to change are exactly what the kernel
// serves as /proc/self/environ; afterwards it reads them back.
func eraseEnviron(erase func(name, value string) bool) (int, error) {
	stat, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0, err
	}
	start, end, err := parseEnvRange(stat)
	if err != nil {
		return 0, err
	}
	if end == start { // started with an empty environment
		return 0, nil
	}
	served, err := os.ReadFile("/proc/self/environ")
	if err != nil {
		return 0, err
	}
	mem, err := os.OpenFile("/proc/self/mem", os.O_RDWR, 0) // O_CLOEXEC: no child inherits it
	if err != nil {
		return 0, err
	}
	defer mem.Close()
	block := make([]byte, end-start)
	if _, err := mem.ReadAt(block, int64(start)); err != nil {
		return 0, fmt.Errorf("reading the environment block: %w", err)
	}
	if !bytes.Equal(block, served) {
		return 0, errors.New("the environment block in memory is not what /proc/self/environ serves")
	}
	spans := secretSpans(block, erase)
	if len(spans) == 0 {
		return 0, nil
	}
	for _, s := range spans {
		clear(block[s.off : s.off+s.n])
		if _, err := mem.WriteAt(block[s.off:s.off+s.n], int64(start)+int64(s.off)); err != nil {
			return 0, fmt.Errorf("writing the environment block: %w", err)
		}
	}
	after, err := os.ReadFile("/proc/self/environ")
	if err != nil || !bytes.Equal(after, block) {
		return 0, errors.New("the erasure could not be confirmed")
	}
	return len(spans), nil
}
