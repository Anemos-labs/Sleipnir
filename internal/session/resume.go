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
			if li := inspectLog(d); li.root == root && li.resumable() {
				return d, nil
			}
		}
		return "", fmt.Errorf("no single-agent session of %s has a finished turn to resume from", root)
	case strings.ContainsAny(spec, `/\`):
		if !hasLog(spec) {
			return "", fmt.Errorf("%s is not a session directory (no events.jsonl)", spec)
		}
		return checkResumable(spec)
	default:
		d := filepath.Join(sessions, spec)
		if !hasLog(d) {
			return "", fmt.Errorf("no session %q (looked in %s)", spec, sessions)
		}
		return checkResumable(d)
	}
}

// checkResumable refuses a session that cannot be continued, in words that say why.
func checkResumable(dir string) (string, error) {
	li := inspectLog(dir)
	switch {
	case li.swarm:
		return "", fmt.Errorf("%s was a swarm session, and swarm sessions cannot be resumed yet (its log and checkpoints stay for inspection)", dir)
	case !li.snapshot:
		return "", fmt.Errorf("%s has no snapshot: no turn finished, so there is nothing to resume from", dir)
	}
	return dir, nil
}

// Resumable reports whether the session in dir can be continued: a single-agent
// session that finished at least one turn.
func Resumable(dir string) bool { return inspectLog(dir).resumable() }

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

// logInfo is what a resume decision needs to know about a session log.
type logInfo struct {
	root     string // project root the session was started in ("" when unknown)
	swarm    bool   // the session ran a swarm
	snapshot bool   // an agent finished a turn and saved a snapshot
}

func (l logInfo) resumable() bool { return l.snapshot && !l.swarm }

// inspectLog reads the head of a session log: session.start (the first one
// decides root and swarm) and whether any snapshot exists.
func inspectLog(dir string) logInfo {
	var li logInfo
	started := false
	scanHead(dir, func(e events.Event) bool {
		switch e.Type {
		case events.TypeSessionStart:
			if !started {
				started = true
				var d struct {
					Root  string `json:"root"`
					Swarm bool   `json:"swarm"`
				}
				_ = json.Unmarshal(e.Data, &d)
				li.root, li.swarm = d.Root, d.Swarm
			}
		case events.TypeAgentSnapshot:
			li.snapshot = true
		}
		return !(started && li.snapshot)
	})
	return li
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
	if err := s.Agent.Restore(*snap); err != nil {
		return err
	}
	// What the restored thread points at must still resolve: folded turns (recall
	// turns=...) and truncated output (recall handle=...) live in the blob store,
	// their indexes in memory.
	if _, err := agent.RebuildArchive(s.Dir, s.Agent.ID(), s.archive); err != nil {
		s.notice("", "resume: the archive could not be re-indexed, so recall of folded turns may miss: "+err.Error())
	}
	if _, err := agent.RebuildHandles(s.Dir, s.handles); err != nil {
		s.notice("", "resume: recall handles could not be restored: "+err.Error())
	}
	return nil
}
