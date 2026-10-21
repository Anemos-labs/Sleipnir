package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/kv"
)

// maxPastArchives bounds how many earlier sessions' archives recall keeps open at once.
const maxPastArchives = 4

// pastArchives is recall's cache of archives of earlier sessions, by session directory and agent.
type pastArchives struct {
	mu    sync.Mutex
	order []string
	byKey map[string]*kv.Archive
}

// readThrough is a blob store that reads an earlier session's blobs and keeps what is written in memory, so that reading
// that session never changes its directory.
type readThrough struct {
	mem  *events.MemBlobs
	past events.Blobs
}

func (r readThrough) Put(data []byte) (core.Hash, error) { return r.mem.Put(data) }
func (r readThrough) Has(h core.Hash) bool               { return r.mem.Has(h) || r.past != nil && r.past.Has(h) }
func (r readThrough) Get(h core.Hash) ([]byte, error) {
	if b, err := r.mem.Get(h); err == nil {
		return b, nil
	}
	if r.past == nil {
		return nil, events.ErrBlobNotFound
	}
	return r.past.Get(h)
}

// pastArchive is recall's Past: the archived turns of an agent of an earlier session of this project. spec is "last" (or
// "latest") or a session id; only a session of the same project, other than this one, can be read. who "" is the agent the
// person talked to (the manager, or the single agent).
func (s *Session) pastArchive(spec, who string) (*kv.Archive, string, error) {
	spec = strings.TrimSpace(spec)
	var dir string
	switch {
	case spec == "last" || spec == "latest":
		if ds := s.EarlierSessions(1); len(ds) > 0 {
			dir = ds[0]
		} else {
			return nil, "", fmt.Errorf("no earlier session of this project to read")
		}
	case spec == "" || strings.ContainsAny(spec, `/\`) || !filepath.IsLocal(spec):
		return nil, "", fmt.Errorf("session must be last or a session id (sleipnir sessions lists them)")
	default:
		dir = filepath.Join(stateRoot(s.opts.Home), "sessions", spec)
		if spec == s.ID {
			return nil, "", fmt.Errorf("%s is this session: leave session out to read it", spec)
		}
		if !hasLog(dir) || inspectLog(dir).root != s.opts.Root {
			return nil, "", fmt.Errorf("no session %q of this project", spec)
		}
	}
	candidates := []string{strings.TrimSpace(who)}
	if candidates[0] == "" {
		candidates = []string{managerAgent, soloAgent}
	}
	for _, id := range candidates {
		ar, err := s.past.open(dir, id)
		if err != nil {
			return nil, "", err
		}
		if ar != nil {
			return ar, id, nil
		}
	}
	return nil, "", fmt.Errorf("no turns of %s in session %s (its agents: %s)", strings.Join(candidates, " or "), filepath.Base(dir), strings.Join(pastAgents(dir), ", "))
}

// open is the archive of agent id in the session at dir, from the cache or rebuilt from its log; nil when the agent has no
// turns there.
func (p *pastArchives) open(dir, id string) (*kv.Archive, error) {
	key := dir + "\x00" + id
	p.mu.Lock()
	defer p.mu.Unlock()
	if ar, ok := p.byKey[key]; ok {
		return ar, nil
	}
	var past events.Blobs
	if _, err := os.Stat(filepath.Join(dir, "blobs")); err == nil {
		if b, err := events.NewDirBlobs(filepath.Join(dir, "blobs")); err == nil {
			past = b
		}
	}
	ar := kv.NewArchive(readThrough{mem: events.NewMemBlobs(), past: past})
	n, err := agent.RebuildArchiveFrom(dir, id, id, ar)
	if err != nil {
		return nil, fmt.Errorf("session %s could not be read: %v", filepath.Base(dir), err)
	}
	if n == 0 {
		return nil, nil
	}
	if p.byKey == nil {
		p.byKey = map[string]*kv.Archive{}
	}
	if len(p.order) >= maxPastArchives {
		old := p.order[0]
		p.order = p.order[1:]
		delete(p.byKey, old) // not closed: a recall that already holds it may still read it
	}
	p.order = append(p.order, key)
	p.byKey[key] = ar
	return ar, nil
}

// pastAgents lists the agents that have turns in the session at dir, sorted.
func pastAgents(dir string) []string {
	seen := map[string]bool{}
	err := events.Scan(filepath.Join(dir, "events.jsonl"), func(e events.Event) error {
		if e.Type == events.TypeTurnAppend && e.Agent != "" {
			seen[e.Agent] = true
		}
		return nil
	})
	var ce *events.CorruptError
	if err != nil && !errors.As(err, &ce) {
		return nil
	}
	out := make([]string, 0, len(seen))
	for a := range seen {
		out = append(out, a)
	}
	sort.Strings(out)
	if len(out) == 0 {
		return []string{"none"}
	}
	return out
}
