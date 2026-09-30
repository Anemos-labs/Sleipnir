package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reee344/sleipnir/internal/agent"
	"github.com/reee344/sleipnir/internal/events"
)

// ResolveResume finds the session directory a resume request names: "latest" is
// the newest session of this project that has something to resume, anything with
// a path separator is a directory, and anything else is a session id under the
// state directory.
func ResolveResume(home, root, spec string) (string, error) {
	spec = strings.TrimSpace(spec)
	sessions := filepath.Join(stateRoot(home), "sessions")
	switch {
	case spec == "" || spec == "latest":
		ents, err := os.ReadDir(sessions)
		if err != nil {
			return "", fmt.Errorf("no sessions to resume (%s)", sessions)
		}
		names := make([]string, 0, len(ents))
		for _, e := range ents {
			if e.IsDir() {
				names = append(names, e.Name())
			}
		}
		sort.Sort(sort.Reverse(sort.StringSlice(names))) // ids start with a timestamp
		for _, n := range names {
			d := filepath.Join(sessions, n)
			if sessionRoot(d) == root && hasSnapshot(d) {
				return d, nil
			}
		}
		return "", fmt.Errorf("no session of %s has a finished turn to resume from", root)
	case strings.ContainsAny(spec, `/\`):
		if !hasLog(spec) {
			return "", fmt.Errorf("%s is not a session directory (no events.jsonl)", spec)
		}
		return spec, nil
	default:
		d := filepath.Join(sessions, spec)
		if !hasLog(d) {
			return "", fmt.Errorf("no session %q (looked in %s)", spec, sessions)
		}
		return d, nil
	}
}

func hasLog(dir string) bool {
	st, err := os.Stat(filepath.Join(dir, "events.jsonl"))
	return err == nil && !st.IsDir()
}

// scanHead calls fn for the events of a log until it returns false.
func scanHead(dir string, fn func(events.Event) bool) {
	f, err := os.Open(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 32<<20)
	for sc.Scan() {
		var e events.Event
		if json.Unmarshal(sc.Bytes(), &e) == nil && !fn(e) {
			return
		}
	}
}

// sessionRoot is the project root a session was started in ("" when unknown).
func sessionRoot(dir string) string {
	root := ""
	scanHead(dir, func(e events.Event) bool {
		if e.Type != events.TypeSessionStart {
			return true
		}
		var d struct {
			Root string `json:"root"`
		}
		_ = json.Unmarshal(e.Data, &d)
		root = d.Root
		return false
	})
	return root
}

func hasSnapshot(dir string) bool {
	found := false
	scanHead(dir, func(e events.Event) bool {
		if e.Type == events.TypeAgentSnapshot {
			found = true
			return false
		}
		return true
	})
	return found
}

// restore brings the single agent back from the session's newest snapshot.
func (s *Session) restore() error {
	if s.Swarm != nil {
		return errors.New("resuming a swarm session is not supported yet: start a new one (its log and checkpoints stay)")
	}
	snap, err := agent.LatestSnapshot(s.Dir, s.Agent.ID())
	if err != nil {
		return err
	}
	return s.Agent.Restore(*snap)
}
