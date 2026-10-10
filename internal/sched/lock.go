package sched

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Two kinds of lock keep the schedule consistent between processes (a daemon in a terminal, `sleipnir web`, a `sleipnir schedule`
// command), each an advisory lock on a file that the operating system drops when its holder dies, so a crash never leaves one
// behind:
//
//   - the daemon lock (Lock, schedule.lock beside the job file) is held for as long as a daemon runs: a second daemon cannot take
//     it, and so two daemons never start the same job twice. The holder writes its process id into the file, for the one that was
//     refused to name.
//   - the write lock (schedule.json.lock) is held for the moment of a read-modify-write of the job file (Add, Remove, Update, the
//     daemon's records), so that two writers cannot lose each other's change.
//
// On platforms without advisory file locks both always succeed and the protection is what it was before: none.

// ErrLocked is the error of Lock when another process holds the daemon lock.
var ErrLocked = errors.New("another daemon holds the schedule")

// lockTries and lockPause bound how long Lock waits for a lock that is held for a moment (a probe by another process).
const (
	lockTries = 6
	lockPause = 50 * time.Millisecond
)

// Lock takes the daemon lock at path (Store.LockPath) and writes this process's id into it. unlock gives it back (it may be called
// more than once). When another process holds it, the error is ErrLocked and pid is the holder's process id as it wrote it (0 when
// unknown). A lock held only for a moment is waited for, briefly.
func Lock(path string) (unlock func(), pid int, err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, 0, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, 0, err
	}
	for i := 0; ; i++ {
		err = tryLockFile(f)
		if err == nil || !errors.Is(err, errHeld) || i == lockTries-1 {
			break
		}
		time.Sleep(lockPause)
	}
	if err != nil {
		pid := readPID(f)
		f.Close()
		if errors.Is(err, errHeld) {
			return nil, pid, ErrLocked
		}
		return nil, 0, err
	}
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = f.Truncate(0)
			_ = unlockFile(f)
			_ = f.Close()
		})
	}, 0, nil
}

// Probe reports whether a process holds the daemon lock at path, and its process id, without keeping the lock: it takes it and gives
// it back at once, and never creates the file. A daemon that starts at that moment waits for it (Lock retries briefly).
func Probe(path string) (pid int, held bool) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	if err := tryLockFile(f); err != nil {
		return readPID(f), errors.Is(err, errHeld)
	}
	_ = unlockFile(f)
	return 0, false
}

// readPID reads the process id a lock holder wrote at the start of the file.
func readPID(f *os.File) int {
	b := make([]byte, 32)
	n, err := f.ReadAt(b, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b[:n])))
	return pid
}

// writeTimeout bounds the wait for the write lock: a writer holds it for the moment of a small file write.
const writeTimeout = 5 * time.Second

// writeLocks serialise the writers of one job file in this process (the file lock then only waits on other processes).
var writeLocks sync.Map // path -> *sync.Mutex

// writeLock takes the write lock of the job file, in this process and between processes; the returned function gives it back.
func (s Store) writeLock() (func(), error) {
	m, _ := writeLocks.LoadOrStore(filepath.Clean(s.Path), &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		mu.Unlock()
		return nil, err
	}
	f, err := os.OpenFile(s.Path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		mu.Unlock()
		return nil, err
	}
	deadline := time.Now().Add(writeTimeout)
	for {
		err = tryLockFile(f)
		if err == nil || !errors.Is(err, errHeld) || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		f.Close()
		mu.Unlock()
		if errors.Is(err, errHeld) {
			return nil, fmt.Errorf("%s is being written by another process; try again", filepath.Base(s.Path))
		}
		return nil, err
	}
	return func() {
		_ = unlockFile(f)
		_ = f.Close()
		mu.Unlock()
	}, nil
}
