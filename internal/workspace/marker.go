package workspace

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// markerFile lives in a tree's private git directory (.git/worktrees/<id>/): the
// one place that exists exactly as long as the worktree does (git deletes it with
// the entry), is invisible to the agent's tree, and is not part of any commit.
// It is how Remove and Prune know that a worktree is ours; a branch name alone is
// not proof, because anyone can create a branch called sleipnir/x.
const markerFile = "sleipnir-workspace.json"

// marker is the ownership record.
type marker struct {
	V      int    `json:"v"`
	Prefix string `json:"prefix"` // the creating manager's branch prefix
	Agent  string `json:"agent"`
	Path   string `json:"path"`
	Branch string `json:"branch,omitempty"`
	Base   string `json:"base"`
	Mode   string `json:"mode"`
	// Integration marks the merge queue's tree; Ref is the branch it publishes to.
	Integration bool   `json:"integration,omitempty"`
	Ref         string `json:"ref,omitempty"`
	// Owner identifies the creating process so a later run can tell a crashed
	// session from a live one.
	PID    int    `json:"pid"`
	Start  int64  `json:"start,omitempty"`
	BootID string `json:"boot,omitempty"`
	// PIDNS names the process-id namespace PID was valid in (Linux). Containers on one
	// machine share a boot id but not their process ids: a pid recorded in another
	// namespace says nothing about whether that process is running.
	PIDNS   string    `json:"pidns,omitempty"`
	Created time.Time `json:"created"`
}

const markerVersion = 1

func writeMarker(adminDir string, mk marker) error {
	mk.V = markerVersion
	data, err := json.MarshalIndent(mk, "", "  ")
	if err != nil {
		return err
	}
	// Atomic: a crash must never leave a torn marker that makes a tree look
	// foreign (and therefore permanently un-prunable) or, worse, half-valid.
	tmp, err := os.CreateTemp(adminDir, ".marker-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, filepath.Join(adminDir, markerFile)); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

var errNoMarker = errors.New("workspace: no marker")

func readMarker(adminDir string) (*marker, error) {
	data, err := os.ReadFile(filepath.Join(adminDir, markerFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errNoMarker
		}
		return nil, err
	}
	var mk marker
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&mk); err != nil {
		return nil, fmt.Errorf("workspace: corrupt marker: %w", err)
	}
	if mk.V != markerVersion || mk.Agent == "" || mk.Path == "" || mk.Prefix == "" {
		return nil, errors.New("workspace: unrecognised marker")
	}
	return &mk, nil
}

// self identifies this process for markers.
type self struct {
	pid    int
	start  int64
	bootID string
	pidns  string
}

// currentSelf captures process identity and available start-time, boot, and PID-namespace markers.
func currentSelf() self {
	pid := os.Getpid()
	return self{pid: pid, start: procStart(pid), bootID: bootID(), pidns: pidNamespace()}
}

// pidNamespace identifies the process-id namespace of this process ("" where the
// platform has none to tell).
func pidNamespace() string {
	ns, err := os.Readlink("/proc/self/ns/pid")
	if err != nil {
		return ""
	}
	return ns
}

// bootID identifies the machine boot on Linux (process ids mean nothing across
// reboots); elsewhere the hostname stands in.
func bootID() string {
	if b, err := os.ReadFile("/proc/sys/kernel/random/boot_id"); err == nil {
		return strings.TrimSpace(string(b))
	}
	h, _ := os.Hostname()
	return "host:" + h
}

// ownerState says what a marker's owner is doing.
type ownerState int

const (
	ownerAlive   ownerState = iota // the process exists
	ownerDead                      // it does not (or the pid now belongs to another process)
	ownerUnknown                   // recorded on another machine or boot: cannot tell
)

func (mk *marker) owner() ownerState {
	if mk.PID <= 0 {
		return ownerDead
	}
	if mk.BootID != "" && mk.BootID != bootID() {
		return ownerUnknown
	}
	if mk.PIDNS != "" && mk.PIDNS != pidNamespace() {
		return ownerUnknown // another container: its process ids are not ours to look up
	}
	if !pidExists(mk.PID) {
		return ownerDead
	}
	if mk.Start != 0 {
		if cur := procStart(mk.PID); cur != 0 && cur != mk.Start {
			return ownerDead // the pid was reused by a different process
		}
	}
	return ownerAlive
}

// atoi64 parses decimal signed integers while discarding parse errors; invalid syntax yields zero
// and overflow saturates.
func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}
