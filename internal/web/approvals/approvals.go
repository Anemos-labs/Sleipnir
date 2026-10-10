// Package approvals is the approvals bridge of `sleipnir web`: the perm.Prompter of each hosted session, which puts the
// harness's questions to the page and waits for a person's answer.
//
// A question is never dropped: it waits until it is answered (POST /api/questions/{qid}/answer, through Bridge.Answer), or until
// it ends for a reason the person can see in the answer it gets: the context that asked ended (an interrupt or a restart:
// by "canceled"; the engine's --ask-timeout: by "timeout"), its tab was closed (by "closed"), or no page has had the stream open
// for the grace period (by "nobody"). Every question has a random id of 130 bits, is answered once, and takes no answer earlier
// than the floor (350 ms) after it was asked and after the previous answer of its tab: a script on the page that answers in the
// instant the question appears is refused with too_soon, whatever the page itself enforces.
//
// The bridge does not choose what an answer means: choice 1 is yes, 2 yes and remember for the session (for a project's tool
// server or its own files: for the project), 3 no with an optional instruction that the model reads with the refusal, and 4 (only
// when the question offers it) yes and allow the builds and tests of most projects for the session; the page cannot pick a scope.
// Questions about the project's own files and its tool servers are refused without being asked while the session is starting:
// the New session dialog decides those before the start.
package approvals

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"net/http"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/rl/redact"
	"github.com/anemos-labs/sleipnir/internal/tools"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// Defaults of a Config.
const (
	// DefaultFloor is the least time between a question appearing (or the tab's previous answer) and an answer to it: the terminal
	// chat's pause before a question takes a key.
	DefaultFloor = 350 * time.Millisecond
	// DefaultMaxPerTab bounds the questions of one tab that are open at once; more wait to be asked.
	DefaultMaxPerTab = 64
	// MaxNote bounds the instruction that goes with a "no", in characters.
	MaxNote = 2000
	// maxText bounds a field of a question shown to the page, in bytes.
	maxText = 16 << 10
	// maxChange bounds the change a question shows, in bytes.
	maxChange = 256 << 10
	// maxRemembered is how many answered ids are kept to tell "answered" from "no such question".
	maxRemembered = 4096
)

// Who resolved a question other than a person (wire.Answer.By).
const (
	ByYou      = "you"
	ByTimeout  = "timeout"
	ByCanceled = "canceled"
	ByClosed   = "closed"
	ByNobody   = "nobody"
)

// noAnswer is the decision of a question that ended without an answer: a refusal that the engine words for the model (it turns it
// into its "nobody answered in time" refusal when the ask timeout passed).
var noAnswer = perm.Decision{Allow: false, Reason: "no answer"}

// Config configures a Bridge.
type Config struct {
	// Floor is the least time between a question becoming answerable and an answer (DefaultFloor when zero).
	Floor time.Duration
	// Grace is how long the open questions wait when no page has a stream open; they are refused after it (by "nobody"). Zero
	// never refuses for that.
	Grace time.Duration
	// Now is the clock (time.Now when nil).
	Now func() time.Time
	// OnAsk reports a question that was asked, OnAnswer one that was resolved (answered, refused, cancelled). They are called
	// outside the bridge's lock and must not block for long.
	OnAsk    func(tab string, q wire.Question)
	OnAnswer func(tab string, a wire.Answer)
	// SessionTime gives the session time in seconds of a moment of a tab (the t0 of an open question); nil gives 0.
	SessionTime func(tab string, at time.Time) float64
	// Change gives the change an edit of a tab asks to make (a path relative to the project and a unified diff), for the
	// question's body; nil shows the path only.
	Change func(tab string, r perm.Request) (path, change string)
	// MaxPerTab bounds the open questions of a tab (DefaultMaxPerTab when zero).
	MaxPerTab int
}

// question is one open question.
type question struct {
	id      string
	tab     string
	agent   string // as the harness names it
	q       wire.Question
	tests   bool // the fourth answer is offered
	project bool // a question about the project's files or a tool server: "remember" is for the project
	created time.Time
	ans     chan perm.Decision // buffered 1; written once, under the lock
	done    bool
}

// Bridge holds the open questions of every tab. Its methods are safe for concurrent use.
type Bridge struct {
	cfg  Config
	mask *redact.Redactor

	mu          sync.Mutex
	open        map[string]*question
	order       []*question // asked order, oldest first; resolved ones are removed
	lastAnswer  map[string]time.Time
	answered    map[string]bool
	answeredQ   []string
	slots       map[string]chan struct{}
	pages       int
	lonelySince time.Time // when the last page left (or the bridge was made); zero while a page is connected
	closed      bool
}

// New returns a bridge.
func New(cfg Config) *Bridge {
	if cfg.Floor <= 0 {
		cfg.Floor = DefaultFloor
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.MaxPerTab <= 0 {
		cfg.MaxPerTab = DefaultMaxPerTab
	}
	return &Bridge{
		cfg:  cfg,
		mask: redact.New(redact.Config{Kinds: []string{redact.GroupTokens, redact.KindJWT, redact.KindPrivateKey, redact.KindURLCred, redact.KindBearer, redact.KindSecret}}),
		open: map[string]*question{}, lastAnswer: map[string]time.Time{}, answered: map[string]bool{},
		slots: map[string]chan struct{}{}, lonelySince: cfg.Now(),
	}
}

// newID returns "q_" and 26 lowercase base32 characters of crypto/rand (130 bits).
func newID() (string, error) {
	var b [17]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	s := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:]))
	return "q_" + s[:26], nil
}

// slot returns the semaphore that bounds the open questions of a tab.
func (b *Bridge) slot(tab string) chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.slots[tab]
	if s == nil {
		s = make(chan struct{}, b.cfg.MaxPerTab)
		b.slots[tab] = s
	}
	return s
}

// Prompter is the perm.Prompter of one tab. root is the project root (question paths are shown relative to it); describe gives an
// agent's task and the globs of its scope ("" when it has none); starting reports whether the tab's session is still being made,
// when questions about the project's own files and its tool servers are refused without being asked.
func (b *Bridge) Prompter(tab, root string, describe func(agent string) (task, scope string), starting func() bool) perm.Prompter {
	return func(ctx context.Context, r perm.Request) perm.Decision {
		if (r.Tool == perm.ToolProjectTrust || r.Tool == perm.ToolMCPServer) && starting != nil && starting() {
			return perm.Decision{Allow: false, Reason: "not asked while the session starts: the New session dialog decides this"}
		}
		slot := b.slot(tab)
		select {
		case slot <- struct{}{}:
		case <-ctx.Done():
			return noAnswer
		}
		defer func() { <-slot }()

		id, err := newID()
		if err != nil {
			return perm.Decision{Allow: false, Reason: "no answer: the question could not be given an id"}
		}
		var task, scope string
		if describe != nil {
			task, scope = describe(r.Agent)
		}
		q := &question{id: id, tab: tab, agent: r.Agent, tests: r.OffersTests,
			project: r.Tool == perm.ToolProjectTrust || r.Tool == perm.ToolMCPServer,
			ans:     make(chan perm.Decision, 1)}
		q.q = b.shape(tab, id, root, task, scope, r)

		b.mu.Lock()
		if b.closed {
			b.mu.Unlock()
			return noAnswer
		}
		q.created = b.cfg.Now()
		b.open[id] = q
		b.order = append(b.order, q)
		b.mu.Unlock()
		if b.cfg.OnAsk != nil {
			b.cfg.OnAsk(tab, q.q)
		}

		select {
		case d := <-q.ans:
			return d
		case <-ctx.Done():
			by := ByCanceled
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				by = ByTimeout
			}
			if b.resolve(q, noAnswer, wire.Answer{QID: id, Choice: 3, By: by}) {
				return noAnswer
			}
			return <-q.ans // answered in the same instant: the answer stands (the engine discards it if the turn is over)
		}
	}
}

// resolve settles q with d and reports a's answer event, once; it reports whether this call settled it.
func (b *Bridge) resolve(q *question, d perm.Decision, a wire.Answer) bool {
	b.mu.Lock()
	if q.done {
		b.mu.Unlock()
		return false
	}
	b.settleLocked(q, d)
	b.mu.Unlock()
	if b.cfg.OnAnswer != nil {
		b.cfg.OnAnswer(q.tab, a)
	}
	return true
}

// settleLocked marks q answered with d and forgets it as open.
func (b *Bridge) settleLocked(q *question, d perm.Decision) {
	q.done = true
	q.ans <- d
	delete(b.open, q.id)
	for i, x := range b.order {
		if x == q {
			b.order = append(b.order[:i], b.order[i+1:]...)
			break
		}
	}
	b.answered[q.id] = true
	b.answeredQ = append(b.answeredQ, q.id)
	if len(b.answeredQ) > maxRemembered {
		delete(b.answered, b.answeredQ[0])
		b.answeredQ = b.answeredQ[1:]
	}
}

// Open lists the open questions of every tab, oldest first.
func (b *Bridge) Open() []wire.OpenQuestion {
	b.mu.Lock()
	qs := append([]*question(nil), b.order...)
	b.mu.Unlock()
	out := make([]wire.OpenQuestion, 0, len(qs))
	for _, q := range qs {
		oq := wire.OpenQuestion{Tab: q.tab, Q: q.q}
		if b.cfg.SessionTime != nil {
			oq.T0 = b.cfg.SessionTime(q.tab, q.created)
		}
		out = append(out, oq)
	}
	return out
}

// OpenFor lists the open questions of one tab, oldest first.
func (b *Bridge) OpenFor(tab string) []wire.Question {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := []wire.Question{}
	for _, q := range b.order {
		if q.tab == tab {
			out = append(out, q.q)
		}
	}
	return out
}

// TabOf names the tab a question belongs to ("" when it is not open).
func (b *Bridge) TabOf(qid string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if q := b.open[qid]; q != nil {
		return q.tab
	}
	return ""
}

// errorf is a *wire.Error.
func errorf(status int, code, msg string, detail any) *wire.Error {
	return &wire.Error{Status: status, Code: code, Msg: msg, Detail: detail}
}

// decision is what an answer means (the terminal dialog's table, internal/tui/app/chat_dialog.go).
func decision(q *question, choice int, note string) perm.Decision {
	switch {
	case choice == 1:
		return perm.Decision{Allow: true, Reason: "allowed by user"}
	case choice == 2 && q.q.Kind == "mcp":
		return perm.Decision{Allow: true, Reason: "approved by user for this project", Remember: perm.ScopeProject}
	case choice == 2 && q.q.Kind == "trust":
		return perm.Decision{Allow: true, Reason: "trusted by user until the files change", Remember: perm.ScopeProject}
	case choice == 2:
		return perm.Decision{Allow: true, Reason: "allowed by user for the session", Remember: perm.ScopeSession}
	case choice == 4:
		return perm.Decision{Allow: true, Reason: "builds and tests allowed by user for the session", Remember: perm.ScopeSession, Preset: perm.PresetTests}
	}
	return perm.Decision{Allow: false, Reason: perm.DeclinedWith(note)}
}

// Answer resolves a question with a person's answer: choice 1, 2, 3 (no, with an optional note of at most MaxNote characters) or 4
// (only when the question offers the tests preset). It refuses an answer earlier than the floor after the question was asked and
// after the tab's previous answer (409 too_soon, with retryAfterMs), a second answer (409 answered) and an unknown id (404
// no_question). Errors are *wire.Error.
func (b *Bridge) Answer(ctx context.Context, qid string, req wire.AnswerRequest) (wire.AnswerResult, error) {
	note := strings.TrimSpace(req.Note)
	switch {
	case req.Choice < 1 || req.Choice > 4:
		return wire.AnswerResult{}, errorf(http.StatusBadRequest, "bad_choice", "the answer is 1, 2, 3 or 4", nil)
	case note != "" && req.Choice != 3:
		return wire.AnswerResult{}, errorf(http.StatusBadRequest, "bad_choice", "a note goes with answer 3 (no) only", nil)
	case utf8.RuneCountInString(note) > MaxNote:
		return wire.AnswerResult{}, errorf(http.StatusBadRequest, "bad_choice", "the note is longer than 2,000 characters", nil)
	}
	note = tools.SanitizeForTerminal(note)
	b.mu.Lock()
	q := b.open[qid]
	if q == nil {
		answered := b.answered[qid]
		b.mu.Unlock()
		if answered {
			return wire.AnswerResult{}, errorf(http.StatusConflict, "answered", "this question was already answered", nil)
		}
		return wire.AnswerResult{}, errorf(http.StatusNotFound, "no_question", "there is no such question", nil)
	}
	if req.Choice == 4 && !q.tests {
		b.mu.Unlock()
		return wire.AnswerResult{}, errorf(http.StatusBadRequest, "bad_choice", "this question does not offer the builds and tests answer", nil)
	}
	now := b.cfg.Now()
	armAt := q.created
	if last := b.lastAnswer[q.tab]; last.After(armAt) {
		armAt = last
	}
	armAt = armAt.Add(b.cfg.Floor)
	if now.Before(armAt) {
		b.mu.Unlock()
		wait := armAt.Sub(now)
		ms := wait.Milliseconds()
		if time.Duration(ms)*time.Millisecond < wait {
			ms++
		}
		return wire.AnswerResult{}, errorf(http.StatusConflict, "too_soon", "the question takes an answer a moment after it appears; answer again", map[string]int64{"retryAfterMs": ms})
	}
	d := decision(q, req.Choice, note)
	b.lastAnswer[q.tab] = now
	b.settleLocked(q, d)
	b.mu.Unlock()
	rule := ""
	if req.Choice == 2 {
		rule = q.q.Rule
	}
	if b.cfg.OnAnswer != nil {
		b.cfg.OnAnswer(q.tab, wire.Answer{QID: qid, Choice: req.Choice, Note: note, By: ByYou, Rule: rule})
	}
	return wire.AnswerResult{OK: true, Rule: rule}, nil
}

// cancel refuses the open questions that match, as by.
func (b *Bridge) cancel(match func(*question) bool, by string) {
	b.mu.Lock()
	var hit []*question
	for _, q := range b.order {
		if match(q) {
			hit = append(hit, q)
		}
	}
	b.mu.Unlock()
	for _, q := range hit {
		b.resolve(q, noAnswer, wire.Answer{QID: q.id, Choice: 3, By: by})
	}
}

// CancelTab refuses every open question of a tab (by: "closed" or "canceled").
func (b *Bridge) CancelTab(tab, by string) {
	b.cancel(func(q *question) bool { return q.tab == tab }, by)
}

// CancelAgent refuses the open questions of one agent of a tab (an interrupt refuses those of the agent the person talks to). The
// agent is named as the harness names it; "" and "main" are the questions of the single agent and of the session itself.
func (b *Bridge) CancelAgent(tab, agent, by string) {
	b.cancel(func(q *question) bool {
		if q.tab != tab {
			return false
		}
		if agent == "main" || agent == "" {
			return q.agent == "main" || q.agent == ""
		}
		return q.agent == agent
	}, by)
}

// Connected tells the bridge how many pages have a stream open now; the host calls it periodically. When none has been connected
// for Config.Grace, every open question is refused (by "nobody").
func (b *Bridge) Connected(n int, at time.Time) {
	b.mu.Lock()
	switch {
	case n > 0:
		b.pages, b.lonelySince = n, time.Time{}
	case b.pages > 0 || b.lonelySince.IsZero():
		b.pages, b.lonelySince = 0, at
	}
	refuse := b.cfg.Grace > 0 && b.pages == 0 && !b.lonelySince.IsZero() && at.Sub(b.lonelySince) >= b.cfg.Grace && len(b.order) > 0
	b.mu.Unlock()
	if refuse {
		b.cancel(func(*question) bool { return true }, ByNobody)
	}
}

// Close refuses every open question (by "closed") and every question asked afterwards.
func (b *Bridge) Close() {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	b.cancel(func(*question) bool { return true }, ByClosed)
}

// ---- shaping a question for the page ----

// agentID is the agent as the page names it: the single agent is the manager's place.
func agentID(a string) string {
	if a == "" || a == "main" {
		return "mgr"
	}
	return a
}

// kindOf is the question's kind for its header (FEATURES.md D-09).
func kindOf(r perm.Request) string {
	switch strings.ToLower(r.Tool) {
	case perm.ToolProjectTrust:
		return "trust"
	case perm.ToolMCPServer:
		return "mcp"
	case "bash", "bash_output", "bash_kill":
		return "command"
	case "write", "edit", "apply_patch":
		return "edit"
	case "read", "glob", "grep", "ls":
		return "read"
	case "web_fetch", "web_search":
		return "web"
	}
	return "other"
}

// setField sets a string field of a question by name when the wire type has it (change and path of PARITY A1).
func setField(q *wire.Question, name, value string) {
	if value == "" {
		return
	}
	if f := reflect.ValueOf(q).Elem().FieldByName(name); f.IsValid() && f.CanSet() && f.Kind() == reflect.String {
		f.SetString(value)
	}
}

// splitWhy separates the engine's reason from a summary: the engine appends it as a last " [reason]".
func splitWhy(summary string) (text, why string) {
	s := strings.TrimRight(summary, " ")
	if strings.HasSuffix(s, "]") {
		if i := strings.LastIndex(s, " ["); i >= 0 {
			return s[:i], s[i+2 : len(s)-1]
		}
	}
	return summary, ""
}

// relDir is a directory relative to the project root with a trailing slash, "." for the root itself, or the directory as it is
// when it is outside the project.
func relDir(root, dir string) string {
	if dir == "" || root == "" {
		return "."
	}
	rel, err := filepath.Rel(root, dir)
	switch {
	case err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)):
		return filepath.ToSlash(dir)
	case rel == ".":
		return "."
	}
	return filepath.ToSlash(rel) + "/"
}

// clean is untrusted text as the page may show it: terminal controls removed, secret-shaped values masked, bounded.
func (b *Bridge) clean(s string, n int) string {
	s = b.mask.String(tools.SanitizeForTerminal(s))
	if len(s) > n {
		s = strings.ToValidUTF8(s[:n], "") + "…"
	}
	return s
}

// shape builds the question the page shows (VOCAB.md 9).
func (b *Bridge) shape(tab, id, root, task, scope string, r perm.Request) wire.Question {
	kind := kindOf(r)
	summary, why := splitWhy(r.Summary)
	if r.Why != "" {
		why = r.Why
	}
	cmd := r.Command
	if cmd == "" || (kind != "command") {
		cmd = summary
	}
	cwd := relDir(root, r.Cwd)
	scopeText := "cwd " + cwd
	if scope != "" {
		scopeText += " (inside " + agentID(r.Agent) + "'s lease " + scope + ")"
	}
	what := r.Remembers
	if what == "" {
		switch kind {
		case "command":
			what = "this command"
		case "edit":
			what = "this change"
		default:
			what = "this request"
		}
	}
	rule := ""
	if len(r.RememberRules) > 0 {
		rule = r.RememberRules[0]
	} else if r.Remembers == "" && kind == "command" && r.Command != "" && !strings.ContainsAny(r.Command, "\n") {
		rule = "Bash(" + r.Command + ")"
	}
	q := wire.Question{
		ID: id, Agent: agentID(r.Agent), Task: b.clean(task, 64), Cmd: b.clean(cmd, maxText), Cwd: b.clean(cwd, 1024),
		Why: b.clean(why, 600), Scope: b.clean(scopeText, 1024), What: b.clean(what, 300), Rule: b.clean(rule, 600),
		Kind: kind, Tool: b.clean(r.Tool, 64), OffersTests: r.OffersTests,
	}
	if kind == "edit" {
		path, change := "", ""
		if b.cfg.Change != nil {
			path, change = b.cfg.Change(tab, r)
		}
		if path == "" && len(r.Paths) > 0 {
			paths := append([]string(nil), r.Paths...)
			sort.Strings(paths)
			path = strings.TrimSuffix(relDir(root, paths[0]), "/")
		}
		setField(&q, "Path", b.clean(path, 4096))
		setField(&q, "Change", b.clean(change, maxChange))
	}
	return q
}
