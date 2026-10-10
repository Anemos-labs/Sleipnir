package settings

import (
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/trust"
	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// trustCovers says what a yes covers (as `sleipnir trust` says it).
const trustCovers = "what the harness itself reads as text and settings: instruction files, .sleipnir/config.json and .mcp.json, skills, commands and agents; the repository's code is not covered, and running it (go test, make, a hook's script) is what every approval is about"

// trustOptions are the answers of the question a chat asks at its start about a project's files (the terminal's trust dialog).
var trustOptions = []string{"Yes, use them this time", "Yes, and remember them until they change", "No, leave them out (esc)"}

// challengeTTL bounds how long a trust challenge is kept: the confirmation it carries lives a minute.
const challengeTTL = 2 * time.Minute

// maxChallenges bounds the challenges kept at once.
const maxChallenges = 64

// challenge is a trust challenge that was issued: the directory and the digest of the files the person was shown, the scope of its
// confirmation, and when it was issued.
type challenge struct {
	dir, digest, scope string
	at                 time.Time
}

// challenges remembers the trust challenges by the confirmation id they carry, so that POST /api/trust knows which files the
// person said yes to and can answer 409 changed when they are not the files now. The ids are never logged or returned again.
type challenges struct {
	mu sync.Mutex
	m  map[string]challenge
}

// newChallenges returns an empty store.
func newChallenges() *challenges { return &challenges{m: map[string]challenge{}} }

// add records a challenge, dropping expired ones and, past the bound, the oldest.
func (c *challenges) add(id string, ch challenge) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, v := range c.m {
		if ch.at.Sub(v.at) > challengeTTL {
			delete(c.m, k)
		}
	}
	for len(c.m) >= maxChallenges {
		oldest := ""
		for k, v := range c.m {
			if oldest == "" || v.at.Before(c.m[oldest].at) {
				oldest = k
			}
		}
		delete(c.m, oldest)
	}
	c.m[id] = ch
}

// take removes and returns the challenge of id.
func (c *challenges) take(id string) (challenge, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch, ok := c.m[id]
	delete(c.m, id)
	return ch, ok
}

// peek returns the challenge of id without removing it.
func (c *challenges) peek(id string) (challenge, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch, ok := c.m[id]
	return ch, ok
}

// handleTrustView is GET /api/sessions/{id}/trust: the tab's project (its files and their hashes, whether the person's yes holds),
// every directory of the ledger with its state now, and the options of the start-of-session question.
func (s *service) handleTrustView(w http.ResponseWriter, r *http.Request) {
	tc, err := s.tabOf(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	v := wire.TrustView{Files: []wire.TrustFile{}, Covers: trustCovers, Ledger: []wire.TrustLedgerRow{}}
	v.Question.Options = append([]string(nil), trustOptions...)
	ledger := s.ledger()
	v.Project = wire.TrustProject{Dir: s.display(tc.cwd), State: "not trusted"}
	fp, ferr := session.ProjectFootprint(s.o.Home, tc.cwd)
	if ferr == nil {
		v.Files = trustFiles(fp)
		v.Project.Digest, v.Project.Partial, v.Project.Unlocks = shortDigest(fp.Digest), fp.Partial, unlocks(fp)
		switch st, e := ledger.Check(tc.cwd, fp); {
		case fp.Empty():
			v.Project.State = "nothing to trust"
		case st == trust.Trusted:
			v.Project.State, v.Project.SavedDay = "trusted", e.Saved
		case st == trust.Changed:
			v.Project.State, v.Project.SavedDay = "changed: "+trust.DescribeChanges(trust.Changes(e, fp)), e.Saved
		}
	} else {
		v.Project.State = "cannot be read"
	}
	all := ledger.All()
	dirs := make([]string, 0, len(all))
	for d := range all {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	for _, d := range dirs {
		e := all[d]
		now, state := session.TrustNow(ledger, s.o.Home, d, e)
		v.Ledger = append(v.Ledger, wire.TrustLedgerRow{Dir: s.display(d), Saved: e.Saved, Files: len(e.Files), Now: cleanText(now), State: state})
	}
	if _, ok := all[tc.cwd]; !ok && ferr == nil && !fp.Empty() {
		v.Ledger = append(v.Ledger, wire.TrustLedgerRow{Dir: s.display(tc.cwd), Files: len(fp.Files), Now: "no yes given", State: "not trusted"})
	}
	reply(w, v, nil)
}

// trustFiles lists a footprint's files as the page shows them.
func trustFiles(fp *trust.Footprint) []wire.TrustFile {
	out := make([]wire.TrustFile, 0, len(fp.Files))
	for _, f := range fp.Files {
		h := f.Sum
		if len(h) > 16 {
			h = h[:16]
		}
		out = append(out, wire.TrustFile{Path: trust.Show(f.Path), Kind: string(f.Kind), Bytes: f.Size, Hash: h})
	}
	return out
}

// shortDigest is a footprint digest as `sleipnir trust` shows it: 12 hex characters.
func shortDigest(d string) string {
	d = strings.TrimPrefix(d, "sha256:")
	if len(d) > 12 {
		d = d[:12]
	}
	return d
}

// unlocks says in a few words what trusting the footprint lets the harness use.
func unlocks(fp *trust.Footprint) string {
	if fp.Empty() {
		return "nothing: no instruction files, settings, tool servers, skills, commands or agents"
	}
	var kinds []string
	seen := map[trust.Kind]bool{}
	for _, f := range fp.Files {
		if !seen[f.Kind] {
			seen[f.Kind] = true
			kinds = append(kinds, string(f.Kind))
		}
	}
	return "its " + strings.Join(kinds, ", ")
}

// knownDir returns the server's own spelling of dir when it may be trusted from the page, and false otherwise: the directory of a
// live tab, a project the server offers, or a directory the ledger already holds (trusting it again after its files changed). What
// it returns is the server's value, the one that matched, never the page's: a handler that goes on with it hands the file system
// only a directory the server itself named.
func (s *service) knownDir(r *http.Request, dir string) (string, bool) {
	if s.host != nil {
		for _, t := range s.host.Tabs() {
			if t.Cwd == dir {
				return t.Cwd, true
			}
		}
		for _, p := range s.host.Projects(r.Context()) {
			if d, ok := s.expand(p.Dir); ok && d == dir {
				return d, true
			}
		}
	}
	for held := range s.ledger().All() {
		if held == dir {
			return held, true
		}
	}
	return "", false
}

// challengeOf scans dir and builds its trust challenge; with issue it also issues the confirmation for the challenge's scope and
// remembers it.
func (s *service) challengeOf(r *http.Request, dir string, issue bool) (wire.TrustChallenge, error) {
	fp, err := session.ProjectFootprint(s.o.Home, dir)
	if err != nil {
		return wire.TrustChallenge{}, fail(http.StatusConflict, "nothing", "the project's files cannot be read here")
	}
	if fp.Empty() {
		return wire.TrustChallenge{}, fail(http.StatusConflict, "nothing", "nothing here that trust would unlock: no instruction files, settings, tool servers, skills, commands or agents")
	}
	ch := wire.TrustChallenge{Dir: s.display(dir), Files: trustFiles(fp), Digest: fp.Digest, Partial: fp.Partial}
	if st, e := s.ledger().Check(dir, fp); st == trust.Changed {
		ch.Changed = cleanText(trust.DescribeChanges(trust.Changes(e, fp)))
	}
	if fp.Partial || !issue {
		return ch, nil
	}
	ch.Scope = trustScope(dir, fp.Digest)
	ch.Confirm = s.srv.IssueConfirm(r, ch.Scope)
	if ch.Confirm == "" {
		return wire.TrustChallenge{}, fail(http.StatusTooManyRequests, "rate_limited", "too many confirmations are outstanding; use or wait for them")
	}
	s.challenges.add(ch.Confirm, challenge{dir: dir, digest: fp.Digest, scope: ch.Scope, at: s.now()})
	return ch, nil
}

// handleTrustChallenge is GET /api/trust/challenge?dir=: the files of dir's project, their digest, what changed since a yes, and a
// confirmation id for the scope trust:<d16 of {dir, digest}> that POST /api/trust spends. A partial footprint (more than a scan
// reads) cannot be trusted and carries no confirmation.
func (s *service) handleTrustChallenge(w http.ResponseWriter, r *http.Request) {
	asked, ok := s.expand(r.URL.Query().Get("dir"))
	if !ok {
		web.WriteError(w, fail(http.StatusBadRequest, "bad_path", "dir must be an absolute directory"))
		return
	}
	dir, ok := s.knownDir(r, asked)
	if !ok {
		web.WriteError(w, fail(http.StatusForbidden, "not_a_project", "that directory is not one of the projects of this server"))
		return
	}
	ch, err := s.challengeOf(r, dir, true)
	reply(w, ch, err)
}

// trustRequest is the body of POST /api/trust: wire.TrustRequest, and All (with On false) to forget every directory.
type trustRequest struct {
	Dir string `json:"dir"`
	On  bool   `json:"on"`
	All bool   `json:"all,omitempty"`
}

// trustReply is the answer of POST /api/trust.
type trustReply struct {
	OK     bool   `json:"ok"`
	Dir    string `json:"dir,omitempty"`
	Digest string `json:"digest,omitempty"`
	Files  int    `json:"files,omitempty"`
	Had    bool   `json:"had,omitempty"`
	Forgot int    `json:"forgot,omitempty"`
}

// handleTrust is POST /api/trust. on:true trusts dir's files and needs the X-Confirm id of the challenge for exactly the files there
// now: the files are scanned again, and when they are not the ones of the challenge the answer is 409 changed with a new challenge
// in the detail. on:false forgets dir (auth only); all:true with on:false forgets every directory.
func (s *service) handleTrust(w http.ResponseWriter, r *http.Request) {
	var req trustRequest
	if !web.DecodeJSON(w, r, &req) {
		return
	}
	ledgerPath := session.TrustLedgerPath(s.o.Home)
	if req.All {
		if req.On || req.Dir != "" {
			web.WriteError(w, fail(http.StatusBadRequest, "bad_request", "all forgets every directory: it takes no dir and on false"))
			return
		}
		unlock := config.WriteLock(ledgerPath)
		n, err := s.ledger().ForgetAll()
		unlock()
		if err != nil {
			web.Logf(r, "trust ledger not written")
			web.WriteError(w, fail(http.StatusInternalServerError, "internal", "the trust ledger could not be written"))
			return
		}
		reply(w, trustReply{OK: true, Forgot: n}, nil)
		return
	}
	asked, ok := s.expand(req.Dir)
	if !ok {
		web.WriteError(w, fail(http.StatusBadRequest, "bad_path", "dir must be an absolute directory"))
		return
	}
	if !req.On {
		unlock := config.WriteLock(ledgerPath)
		had, err := s.ledger().Forget(asked)
		unlock()
		if err != nil {
			web.Logf(r, "trust ledger not written")
			web.WriteError(w, fail(http.StatusInternalServerError, "internal", "the trust ledger could not be written"))
			return
		}
		reply(w, trustReply{OK: true, Dir: s.display(asked), Had: had}, nil)
		return
	}
	dir, ok := s.knownDir(r, asked)
	if !ok {
		web.WriteError(w, fail(http.StatusForbidden, "not_a_project", "that directory is not one of the projects of this server"))
		return
	}
	id := r.Header.Get(web.ConfirmHeader)
	ch, known := s.challenges.peek(id)
	if !known || ch.dir != dir {
		// No challenge of this server for this directory: the confirmation must be for the files as they are now.
		cur, err := s.challengeOf(r, dir, false)
		if err != nil {
			web.WriteError(w, err)
			return
		}
		ch = challenge{dir: dir, digest: cur.Digest, scope: trustScope(dir, cur.Digest)}
	}
	if !s.srv.RequireConfirm(w, r, ch.scope) {
		return
	}
	s.challenges.take(id)
	unlock := config.WriteLock(ledgerPath)
	defer unlock()
	fp, err := session.ProjectFootprint(s.o.Home, dir)
	if err != nil {
		web.WriteError(w, fail(http.StatusConflict, "nothing", "the project's files cannot be read here"))
		return
	}
	if fp.Digest != ch.digest {
		next, cerr := s.challengeOf(r, dir, true)
		if cerr != nil {
			web.WriteError(w, cerr)
			return
		}
		web.WriteError(w, failDetail(http.StatusConflict, "changed", "the project's files changed since they were shown: look at them again", next))
		return
	}
	if err := s.ledger().Remember(dir, fp, s.now()); err != nil {
		if errors.Is(err, trust.ErrNotRememberable) {
			web.WriteError(w, fail(http.StatusConflict, "conflict", cleanText(err.Error())))
			return
		}
		web.Logf(r, "trust ledger not written")
		web.WriteError(w, fail(http.StatusInternalServerError, "internal", "the trust ledger could not be written"))
		return
	}
	reply(w, trustReply{OK: true, Dir: s.display(dir), Digest: shortDigest(fp.Digest), Files: len(fp.Files)}, nil)
}
