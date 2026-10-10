//go:build unix

package sched

import (
	"io"
	"os"
	"os/exec"
	"strconv"
	"time"
)

// keysWriteTimeout bounds the parent's write of the keys: a child that does not read them (it died at its start) does not hold a
// goroutine of the parent.
const keysWriteTimeout = 10 * time.Second

// passKeys hands payload to the child over an inherited pipe (Unix): the read end becomes one of the child's descriptors, named by
// KeysFDEnv, and the write end, kept by the parent, is written and closed right after the start.
func passKeys(cmd *exec.Cmd, payload []byte) func(error) {
	r, w, err := os.Pipe()
	if err != nil {
		// no pipe: the child starts without keys rather than with them in its environment
		return func(error) {}
	}
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	cmd.ExtraFiles = append(cmd.ExtraFiles, r)
	fd := 2 + len(cmd.ExtraFiles) // ExtraFiles[i] is descriptor 3+i in the child
	cmd.Env = append(cmd.Env, KeysFDEnv+"="+strconv.Itoa(fd))
	return func(startErr error) {
		_ = r.Close()
		if startErr != nil {
			_ = w.Close()
			return
		}
		go func() {
			defer w.Close()
			_ = w.SetWriteDeadline(time.Now().Add(keysWriteTimeout))
			_, _ = w.Write(payload)
		}()
	}
}

// readKeysFD reads the key pipe a parent passed as descriptor v (3 and above), and closes it.
func readKeysFD(v string) []byte {
	fd, err := strconv.Atoi(v)
	if err != nil || fd < 3 || fd > 1024 {
		return nil
	}
	f := os.NewFile(uintptr(fd), "sleipnir-keys")
	if f == nil {
		return nil
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, maxKeysPayload))
	return b
}
