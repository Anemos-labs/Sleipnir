package main

// The session host of `sleipnir web`: several harness sessions in this process, one per tab, behind the session, stream and
// question routes, and the registration of the other route packages (the Workspace, the settings, the tools and the
// runner). web.go calls webHost before the server exists; the host registers its routes when the server is made (web.Config.Routes)
// and closes every tab after the server has stopped.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/term"

	"github.com/anemos-labs/sleipnir/internal/checkpoint"
	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/swarm"
	"github.com/anemos-labs/sleipnir/internal/trust"
	"github.com/anemos-labs/sleipnir/internal/update"
	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/approvals"
	"github.com/anemos-labs/sleipnir/internal/web/seam"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// init attaches the session host to `sleipnir web`.
func init() { webHost = startWebHost }

// Bounds of the host: tabs, message and project sizes, the idempotency window, the stream ping and the snapshot write.
const (
	maxTabs       = 16
	maxMessage    = 256 << 10
	maxProjects   = 200
	idemWindow    = 60 * time.Second
	pingEvery     = 15 * time.Second
	defaultGrace  = 60 * time.Second
	snapshotWrite = 60 * time.Second
)

// webRouteEnv is what the route packages are configured with: the user's home, the program's version, the executable and the
// environment of the commands that call a provider.
type webRouteEnv struct {
	Home, Version, Self string
	// Cwd is the directory of a run that names no tab (--cwd).
	Cwd string
	Env func() []string
}

// webRoutePackage is a route package the command links in (web_routes_*.go add theirs to webRoutePackages in an init function, so
// that the command builds with any set of them): its name, its place in the order of registration, its registration function, and
// what stops what it started when the server stops (nil: nothing).
type webRoutePackage struct {
	name     string
	order    int
	register func(srv *web.Server, h seam.Host, env webRouteEnv)
	shutdown func(ctx context.Context, srv *web.Server)
}

// routeShutdownLimit bounds how long the route packages take to stop what they started (runs, a daemon) when the server stops.
const routeShutdownLimit = 15 * time.Second

// webRoutePackages are the route packages linked into this binary.
var webRoutePackages []webRoutePackage

// webHostImpl is the session host: the tabs, the approvals bridge and the routes.
type webHostImpl struct {
	ctx    context.Context // ends when the host closes
	cancel context.CancelFunc
	cmdCtx context.Context // the command's: it ends on Ctrl-C, before the server stops
	d      webDefaults
	logf   func(string, ...any)
	bridge *approvals.Bridge
	boot   string

	askTimeout time.Duration
	base       session.Options // the base of every tab's options (the fixture backend injects its provider here)
	fixture    *webFixture

	srv *web.Server
	hub *web.Hub

	mu       sync.Mutex
	tabs     []*webTab
	used     map[string]bool
	active   string
	nextOrd  int
	idem     map[string]*idemEntry
	qagent   map[string]string // open question -> the agent that asked
	closing  map[*webTab]bool  // tabs removed from the strip whose close has not ended
	trustIDs map[string]trustIssued
	wg       sync.WaitGroup
}

// idemEntry is a request with a client id: claimed by the first request, which answers it (done is closed then); ok says that it
// succeeded, and status and body are its answer.
type idemEntry struct {
	at     time.Time
	done   chan struct{}
	ok     bool
	status int
	body   any
}

// trustIssued is a trust challenge that was issued: the scope its confirmation id is for (it binds the directory, the footprint and
// whatever else the session raises).
type trustIssued struct {
	scope string
	at    time.Time
}

// startWebHost is webHost: it builds the host and returns the function that registers its routes, the one that starts its first
// session (once the address is bound) and the one that closes it.
func startWebHost(ctx context.Context, d webDefaults, logf func(string, ...any)) (func(*web.Server), func(), func(), error) {
	h, err := newWebHost(ctx, d, logf)
	if err != nil {
		return nil, nil, nil, err
	}
	return h.register, h.start, h.close, nil
}

// newWebHost makes the host. When the server has no model to start sessions with and its terminal can ask, the chat's first-run
// setup runs here, before the address is printed.
func newWebHost(ctx context.Context, d webDefaults, logf func(string, ...any)) (*webHostImpl, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	hctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	h := &webHostImpl{ctx: hctx, cancel: cancel, cmdCtx: ctx, d: d, logf: logf, boot: hex.EncodeToString(b), askTimeout: d.AskTimeout,
		used: map[string]bool{}, idem: map[string]*idemEntry{}, qagent: map[string]string{}, trustIDs: map[string]trustIssued{},
		closing: map[*webTab]bool{}}
	grace := d.AskGrace
	if grace == 0 {
		grace = defaultGrace
	}
	if grace < 0 {
		grace = 0
	}
	h.bridge = h.newBridge(0, grace)
	session.Version = version
	if d.Fixture != "" {
		fx, err := newWebFixture(hctx, d.Fixture)
		if err != nil {
			cancel()
			return nil, err
		}
		h.fixture = fx
		if len(fx.scenarios) > 0 {
			// sessions started from the page run on the fixture's model too
			b := fx.scenarios[0].base
			b.Root, b.Cwd, b.AskTimeout = "", "", 0
			b.TrustProject = false // trust in a session started from the page comes from its own request and gate, never from the fixture
			h.base = b
		}
		return h, nil
	}
	if d.Model == "" && term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stderr.Fd())) {
		asking := bufio.NewReader(os.Stdin)
		if err := ensureModel(ctx, &h.d.Model, asking, os.Stderr, func() (string, error) { return readSecret(ctx, asking) }, true); err != nil {
			cancel()
			return nil, err
		}
	}
	return h, nil
}

// newBridge makes the approvals bridge of the host: floor is the least time before an answer (0: the default 350 ms), grace how long
// questions wait with no page connected (0: forever).
func (h *webHostImpl) newBridge(floor, grace time.Duration) *approvals.Bridge {
	return approvals.New(approvals.Config{Floor: floor, Grace: grace, OnAsk: h.onAsk, OnAnswer: h.onAnswer, Change: h.pendingChange,
		Where: h.where, OnRefused: h.refused, MaxEncoded: webHubConfig.MaxEventBytes - 16<<10, // the room an event leaves for its question
		SessionTime: func(tab string, at time.Time) float64 {
			if t := h.tab(tab); t != nil {
				return t.sessionTime(at)
			}
			return 0
		}})
}

// register adds the host's routes and the linked route packages to the server and starts the host's clock; it starts no session
// (start does, once the server's address is bound). It is web.Config.Routes.
func (h *webHostImpl) register(srv *web.Server) {
	h.srv, h.hub = srv, srv.Hub()
	h.routes(srv)
	pkgs := append([]webRoutePackage(nil), webRoutePackages...)
	sort.SliceStable(pkgs, func(i, j int) bool { return pkgs[i].order < pkgs[j].order })
	self, _ := os.Executable()
	home, _ := os.UserHomeDir()
	env := webRouteEnv{Home: home, Version: version, Self: self, Cwd: h.d.Cwd, Env: os.Environ}
	for _, p := range pkgs {
		p.register(srv, h, env)
	}
	h.track(h.clock)
	// the last frame every page gets: the server delivers it before it ends the streams
	srv.OnShutdown(func() {
		h.Publish(wire.Frame{Type: "bye", Data: wire.ReasonFrame{Reason: "the server is shutting down"}, Critical: true})
	})
}

// start starts the first tab (or the fixture's tabs). The command calls it once the server's address is bound: a run that cannot
// listen starts no session and leaves nothing behind.
func (h *webHostImpl) start() {
	if h.fixture != nil {
		h.fixture.start(h)
		return
	}
	h.startFirst()
}

// startFirst starts the first tab in --cwd with the server's defaults, as `sleipnir chat` would start (--resume and --continue
// included). When it cannot start the tab is removed and the reason is on the terminal: the page opens with no tab.
func (h *webHostImpl) startFirst() {
	args := chatArgsOf(h.d, h.d.Resume)
	t, err := h.addTab("", h.d.Cwd, h.base)
	if err != nil {
		return
	}
	h.track(func() {
		if err := t.startGen(startSpec{args: args, first: true}); err != nil && !errors.Is(err, context.Canceled) {
			h.logf("the first session did not start: %s", clip(err.Error(), 400))
			h.removeTab(t, approvals.ByClosed)
		}
	})
}

// track runs fn on a goroutine that close waits for.
func (h *webHostImpl) track(fn func()) {
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		fn()
	}()
}

// clock tells the bridge whether a page is connected (every second), sends each tab's meta and roster when they changed, and the
// pages a ping with every tab's session time (every 15 seconds).
func (h *webHostImpl) clock() {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	n := 0
	for {
		select {
		case <-h.ctx.Done():
			return
		case now := <-tick.C:
			h.bridge.Connected(h.hub.Streams(), now)
			tabs := h.tabList()
			for _, t := range tabs {
				t.publishRoster()
				t.publishMeta()
			}
			if n++; n%int(pingEvery/time.Second) == 0 {
				p := wire.Ping{Now: map[string]float64{}}
				for _, t := range tabs {
					if tr := t.translator(); tr != nil {
						p.Now[t.id] = tr.Now()
					}
				}
				h.Publish(wire.Frame{Type: "ping", Data: p, Coalescable: true, Key: "ping"})
			}
		}
	}
}

// close ends the host after the server has stopped: what the route packages started is stopped, then every tab is closed (an isolated
// run is finished first), at once for all tabs.
func (h *webHostImpl) close() {
	if h.srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), routeShutdownLimit)
		for _, p := range webRoutePackages {
			if p.shutdown != nil {
				p.shutdown(ctx, h.srv)
			}
		}
		cancel()
	}
	// Every tab, and every tab whose close a request started (DELETE, a first start that failed), is closed before the process may end:
	// an isolated team's verified work is being applied to the checkout. The wait is bounded as finishing a run is.
	reason := session.EndExit
	if h.cmdCtx != nil && h.cmdCtx.Err() != nil {
		reason = session.EndInterrupted // Ctrl-C or SIGTERM, as the terminal chat records it
	}
	h.mu.Lock()
	tabs := append(append([]*webTab(nil), h.tabs...), slices.Collect(maps.Keys(h.closing))...)
	h.mu.Unlock()
	var wg sync.WaitGroup
	for _, t := range tabs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			done := make(chan *swarm.IntegrationReport, 1)
			go func() { done <- t.shutdown(approvals.ByClosed, reason) }()
			select {
			case rep := <-done:
				if rep != nil {
					h.logf("%s: %s", t.id, integrationText(rep))
				}
			case <-time.After(closeLimit + time.Minute):
				h.logf("%s: the session did not close within %v", t.id, closeLimit+time.Minute)
			}
		}()
	}
	wg.Wait()
	h.bridge.Close()
	h.cancel()
	h.wg.Wait()
	if h.fixture != nil {
		h.fixture.close()
	}
}

// ---- seam.Host ---------------------------------------------------------------------------------------------------------------

// publishFrame hands a wire.Frame to the hub on the page topic: the data encoded as JSON, the frame's class carried over (Critical,
// Coalescable and Key; wire.Frame). It is the implementation of seam.Host.Publish.
func publishFrame(hub *web.Hub, f wire.Frame) error {
	if hub == nil {
		return errors.New("no hub")
	}
	data, err := json.Marshal(f.Data)
	if err != nil {
		return err
	}
	_, err = hub.Publish(seam.Topic, web.Event{Type: f.Type, Data: data, Critical: f.Critical, Coalescable: f.Coalescable && !f.Critical, Key: f.Key})
	return err
}

// Publish sends a frame to every page. A question whose ask could not be sent (too large for the stream, which the bridge checks
// before asking) is refused: nobody could see it, so it must not stay open.
func (h *webHostImpl) Publish(f wire.Frame) {
	if h.hub == nil {
		return
	}
	err := publishFrame(h.hub, f)
	if err == nil || errors.Is(err, web.ErrClosed) {
		return
	}
	h.logf("a %s frame was not sent: %v", f.Type, err)
	if qid := askID(f); qid != "" && h.bridge != nil {
		// From a goroutine of its own: the translator that publishes holds its lock, and the refusal's answer event goes through it.
		go h.bridge.Refuse(qid, "its question could not be sent to the page ("+err.Error()+")")
	}
}

// askID is the id of the question of an ask event's frame, "" for any other frame.
func askID(f wire.Frame) string {
	ev, ok := f.Data.(wire.EvFrame)
	if f.Type != "ev" || !ok {
		return ""
	}
	var a struct {
		K string `json:"k"`
		Q struct {
			ID string `json:"id"`
		} `json:"q"`
	}
	if json.Unmarshal(ev.Ev, &a) != nil || a.K != "ask" {
		return ""
	}
	return a.Q.ID
}

// tabList is the tabs in strip order.
func (h *webHostImpl) tabList() []*webTab {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]*webTab(nil), h.tabs...)
}

// tab finds a tab by id.
func (h *webHostImpl) tab(id string) *webTab {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, t := range h.tabs {
		if t.id == id {
			return t
		}
	}
	return nil
}

// Tabs lists the live tabs in strip order.
func (h *webHostImpl) Tabs() []wire.TabSummary {
	out := []wire.TabSummary{}
	for _, t := range h.tabList() {
		out = append(out, t.Summary())
	}
	return out
}

// HostedSessions maps the session ids the tabs hold, or are about to hold while they start or restart, to their tab (webTab.sessionIDs):
// a recorded-session delete or prune of the page leaves them alone (internal/web/tools).
func (h *webHostImpl) HostedSessions() map[string]string {
	out := map[string]string{}
	for _, t := range h.tabList() {
		for _, sid := range t.sessionIDs() {
			out[sid] = t.id
		}
	}
	return out
}

// Tab finds a live tab by its id.
func (h *webHostImpl) Tab(id string) (seam.Tab, bool) {
	t := h.tab(id)
	if t == nil {
		return nil, false
	}
	return t, true
}

// Active is the tab the page should show first.
func (h *webHostImpl) Active() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.active != "" {
		return h.active
	}
	if len(h.tabs) > 0 {
		return h.tabs[0].id
	}
	return ""
}

// Questions lists the open questions of every tab, oldest first.
func (h *webHostImpl) Questions() []wire.OpenQuestion { return h.bridge.Open() }

// pendingChange is the change an edit of a tab asks to make: its path and a unified diff against the file the write will
// change (a link is followed, as the write follows it); a file that is there and cannot be shown as text is said to be so, never
// drawn as a new file. ok is false when nothing can be shown (the question is then not asked).
func (h *webHostImpl) pendingChange(tab string, r perm.Request) (path, change string, ok bool) {
	t := h.tab(tab)
	if t == nil {
		return "", "", false
	}
	s := t.session()
	if s == nil {
		return "", "", false
	}
	input := r.Input
	if len(input) == 0 {
		input, _ = t.calls.input(r.Agent, r.Tool) // the request names the call; the call's input came through the sink
	}
	path, change, ok = proposedChange(r, input, s.Cwd())
	if ok && filepath.IsAbs(path) {
		path = strings.TrimSuffix(h.where(tab, path), "/")
	}
	return path, change, ok
}

// proposedChange is the change a file tool's request (r, with the call's input) asks to make, drawn against what the files it
// names hold now: its path (absolute when the request names one file) and a unified diff (checkpoint.ProposeFrom; a patch is read
// as the apply_patch tool reads it). cwd resolves a relative path that the request does not name. ok is false for another tool or
// an input that cannot be read.
func proposedChange(r perm.Request, input []byte, cwd string) (path, change string, ok bool) {
	pc, ok := checkpoint.ProposeFrom(r.Tool, input, func(p string) checkpoint.Current { return currentOf(p, r.Paths, cwd) })
	if !ok {
		return "", "", false
	}
	path = pc.Path
	if len(r.Paths) == 1 {
		path = r.Paths[0]
	}
	return path, pc.Unified, true
}

// currentOf is what the file p of a call holds now. paths are the files the tool resolved and asks about: the only one when there is
// one, else the one that p names (a patch names several, relative to the agent's directory); cwd resolves p when the request names
// none. A file that cannot be told apart among them is said to be unshown.
func currentOf(p string, paths []string, cwd string) checkpoint.Current {
	switch {
	case len(paths) == 1:
		return currentContent(paths[0])
	case len(paths) == 0 && filepath.IsAbs(p):
		return currentContent(filepath.Clean(p))
	case len(paths) == 0:
		return currentContent(filepath.Join(cwd, p))
	}
	clean := filepath.Clean(p)
	var match []string
	for _, abs := range paths {
		if abs == clean || (!filepath.IsAbs(clean) && strings.HasSuffix(abs, string(filepath.Separator)+clean)) {
			match = append(match, abs)
		}
	}
	if len(match) != 1 {
		return checkpoint.Current{Exists: true, Unshown: "it cannot be told apart among the files the request names"}
	}
	return currentContent(match[0])
}

// maxCurrentContent is the largest current file a pending change is drawn against.
const maxCurrentContent = 1 << 20

// currentContent is what the file at abs holds now, following a link as a write through it does: its text, or why it cannot be
// shown (not a regular file, larger than 1 MiB, not text).
func currentContent(abs string) checkpoint.Current {
	target, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if _, lerr := os.Lstat(abs); lerr == nil {
			return checkpoint.Current{Exists: true, Unshown: "a link whose target cannot be read"}
		}
		return checkpoint.Current{}
	}
	fi, err := os.Stat(target)
	switch {
	case err != nil:
		return checkpoint.Current{Exists: true, Unshown: "it cannot be read"}
	case !fi.Mode().IsRegular():
		return checkpoint.Current{Exists: true, Unshown: "it is not a regular file"}
	case fi.Size() > maxCurrentContent:
		return checkpoint.Current{Exists: true, Unshown: fmt.Sprintf("it is %d bytes, larger than a change is drawn against (%d)", fi.Size(), maxCurrentContent)}
	}
	b, err := os.ReadFile(target)
	switch {
	case err != nil:
		return checkpoint.Current{Exists: true, Unshown: "it cannot be read"}
	case len(b) > maxCurrentContent:
		return checkpoint.Current{Exists: true, Unshown: "it grew past the size a change is drawn against"}
	case !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0:
		return checkpoint.Current{Exists: true, Unshown: "it is not text"}
	}
	return checkpoint.Current{Text: string(b), Exists: true}
}

// where renders a directory of a tab for a question: relative to the project root ("." for the root, "web/" below it); inside an
// isolated worker's tree, "worktree <tree>/<path>"; elsewhere as it is.
func (h *webHostImpl) where(tab, dir string) string {
	if dir == "" {
		return "."
	}
	t := h.tab(tab)
	var s *session.Session
	if t != nil {
		s = t.session()
	}
	if s == nil {
		return filepath.ToSlash(dir)
	}
	if m := s.Worktrees(); m != nil && m.TreesDir() != "" {
		if rel, err := filepath.Rel(m.TreesDir(), dir); err == nil && filepath.IsLocal(rel) {
			return "worktree " + filepath.ToSlash(rel) + "/"
		}
	}
	rel, err := filepath.Rel(s.Root(), dir)
	switch {
	case err != nil || !filepath.IsLocal(rel) && rel != ".":
		return filepath.ToSlash(dir)
	case rel == ".":
		return "."
	}
	return filepath.ToSlash(rel) + "/"
}

// refused reports, in the session's log (and so as a row of the tab), a request that was refused without being asked because its
// question could not be shown whole.
func (h *webHostImpl) refused(tab, agent, why string) {
	t := h.tab(tab)
	if t == nil {
		return
	}
	if s := t.session(); s != nil && s.Log != nil {
		_, _ = s.Log.Emit("", "notice", map[string]any{"level": "warn", "msg": "a request of " + uiAgent(agent) + " was refused without being asked: " + why})
	}
}

// onAsk hands a question to its tab's translator.
func (h *webHostImpl) onAsk(tab string, q wire.Question) {
	h.mu.Lock()
	h.qagent[q.ID] = q.Agent
	h.mu.Unlock()
	if t := h.tab(tab); t != nil {
		if tr := t.translator(); tr != nil {
			tr.Question(q)
		}
	}
}

// onAnswer hands an answer to its tab's translator; a "no" with an instruction is also the person's row and a quiet steer of the
// agent that asked (the instruction reaches it with the refusal).
func (h *webHostImpl) onAnswer(tab string, a wire.Answer) {
	h.mu.Lock()
	agentID := h.qagent[a.QID]
	delete(h.qagent, a.QID)
	h.mu.Unlock()
	t := h.tab(tab)
	if t == nil {
		return
	}
	if tr := t.translator(); tr != nil {
		tr.Answered(a)
		if a.By == approvals.ByYou && a.Choice == 3 && a.Note != "" {
			tr.Emit(&wire.Say{Who: "you", Text: clip(a.Note, approvals.MaxNote)}, &wire.Steer{To: firstNonEmpty(agentID, "mgr"), Text: clip(a.Note, approvals.MaxNote), Quiet: true})
		}
	}
	if a.By == approvals.ByYou && (a.Choice == 2 || a.Choice == 4) {
		t.rulesSoon()
	}
}

// rulesSoon publishes the tab's rules once the engine has taken in a "don't ask again" (it does so when the question returns).
func (t *webTab) rulesSoon() {
	t.h.track(func() {
		for _, d := range []time.Duration{50 * time.Millisecond, 300 * time.Millisecond, time.Second} {
			select {
			case <-time.After(d):
			case <-t.h.ctx.Done():
				return
			}
			t.publishMeta()
		}
	})
}

// firstNonEmpty is the first argument that is not empty.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// ---- tabs --------------------------------------------------------------------------------------------------------------------

// slugRE matches what a tab id may not hold.
var slugRE = regexp.MustCompile(`[^a-z0-9]+`)

// newTabID makes a tab id from a name or a directory: lower case letters, digits and dashes, unique in this server's life.
func (h *webHostImpl) newTabIDLocked(from string) string {
	base := strings.Trim(slugRE.ReplaceAllString(strings.ToLower(from), "-"), "-")
	if len(base) > 30 {
		base = strings.Trim(base[:30], "-")
	}
	if base == "" {
		base = "session"
	}
	id := base
	for n := 2; h.used[id]; n++ {
		id = base + "-" + strconv.Itoa(n)
	}
	h.used[id] = true
	return id
}

// addTab makes a tab (its first generation is started by the caller) and announces it.
func (h *webHostImpl) addTab(name, cwd string, base session.Options) (*webTab, error) {
	h.mu.Lock()
	if len(h.tabs) >= maxTabs {
		h.mu.Unlock()
		return nil, werr(http.StatusConflict, "limit", "16 sessions are open: close one first")
	}
	from := name
	if from == "" {
		from = filepath.Base(rootOf(cwd))
	}
	id := h.newTabIDLocked(from)
	if name == "" {
		name = id
	}
	t := newTab(h, id, name, base)
	t.order = h.nextOrd
	t.gen = 1
	h.nextOrd++
	h.tabs = append(h.tabs, t)
	h.mu.Unlock()
	h.Publish(wire.Frame{Type: "tab", Data: wire.TabFrame{Op: "add", Tab: t.Summary()}, Critical: true})
	return t, nil
}

// removeTab ends a tab and takes it off the strip; it returns how an isolated run ended (nil when it was not one).
func (h *webHostImpl) removeTab(t *webTab, by string) (rep *swarm.IntegrationReport) {
	h.mu.Lock()
	i := slices.Index(h.tabs, t)
	if i >= 0 {
		h.tabs = append(h.tabs[:i], h.tabs[i+1:]...)
	}
	if h.active == t.id {
		h.active = ""
	}
	h.closing[t] = true // until its close has ended, the host's close waits for it
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.closing, t)
		h.mu.Unlock()
	}()
	sum := t.Summary()
	h.Publish(wire.Frame{Type: "tab", Data: wire.TabFrame{Op: "remove", Tab: sum}, Critical: true})
	r := t.shutdown(by, session.EndExit)
	h.Publish(wire.Frame{Type: "recorded", Data: map[string]any{}})
	return r
}

// ---- projects ------------------------------------------------------------------------------------------------------------------

// Projects lists the directories a new session may start in: --cwd, each --project, the directories of the live tabs, of the
// recorded sessions, and those of the trust ledger that exist.
func (h *webHostImpl) Projects(ctx context.Context) []wire.Project {
	var dirs []string
	seen := map[string]bool{}
	add := func(d string) {
		if d == "" || len(dirs) >= maxProjects {
			return
		}
		abs, err := filepath.Abs(d)
		if err != nil {
			return
		}
		abs = filepath.Clean(abs)
		if seen[abs] {
			return
		}
		if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
			return
		}
		seen[abs] = true
		dirs = append(dirs, abs)
	}
	add(h.d.Cwd)
	for _, p := range h.d.Projects {
		add(p)
	}
	for _, t := range h.tabList() {
		add(t.Summary().Cwd)
	}
	home, _ := os.UserHomeDir()
	if recs, err := session.ListRecorded(home, time.Now()); err == nil {
		for i, r := range recs {
			if i >= 100 || ctx.Err() != nil {
				break
			}
			add(r.Cwd)
		}
	}
	ledger := trust.OpenLedger(session.TrustLedgerPath(home)).All()
	keys := make([]string, 0, len(ledger))
	for k := range ledger {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		add(k)
	}
	out := make([]wire.Project, 0, len(dirs))
	for _, d := range dirs {
		root := rootOf(d)
		p := wire.Project{Dir: d, Root: root, Name: filepath.Base(root), Trust: "none", Default: d == filepath.Clean(h.d.Cwd)}
		switch fp, err := trust.Scan(root, d, home); {
		case err != nil:
			p.Trust = "unreadable" // never "none": a directory whose files cannot be read is not one with nothing to trust
		case fp.Partial:
			p.Files, p.Trust = len(fp.Files), "partial"
		case !fp.Empty():
			p.Files = len(fp.Files)
			switch st, _ := trust.OpenLedger(session.TrustLedgerPath(home)).Check(d, fp); st {
			case trust.Trusted:
				p.Trust = "trusted"
			case trust.Changed:
				p.Trust = "changed"
			default:
				p.Trust = "untrusted"
			}
		}
		out = append(out, p)
	}
	return out
}

// isProject reports whether dir is one of the directories a new session may start in, and returns the listed project: its Dir is
// the server's own spelling of the directory, which a caller goes on with in place of the request's.
func (h *webHostImpl) isProject(ctx context.Context, dir string) (wire.Project, bool) {
	clean := filepath.Clean(dir)
	for _, p := range h.Projects(ctx) {
		if p.Dir == clean {
			return p, true
		}
	}
	return wire.Project{}, false
}

// ---- defaults of a new session ---------------------------------------------------------------------------------

// swarmWorkerCeiling is the most workers a team may have when swarm.max_workers sets no ceiling (the swarm's own default).
const swarmWorkerCeiling = 24

// newSessionDefaults are the server's defaults as the New session dialog seeds its fields from them.
type newSessionDefaults struct {
	wire.NewSessionRequest
	MaxWorkers int `json:"maxWorkers"`
}

// defaults reads the server's flags over the configuration, as the chat's options would have them.
func (h *webHostImpl) defaults() newSessionDefaults {
	cfg, _, _ := config.Load(config.LoadOpts{Cwd: h.d.Cwd, UntrustedProject: true})
	d := newSessionDefaults{NewSessionRequest: wire.NewSessionRequest{
		Cwd: h.d.Cwd, Model: h.d.Model, Mode: h.d.Mode, Isolation: h.d.Isolation, Verify: h.d.Verify, Commit: h.d.Commit,
		TrustProject: h.d.TrustProject, NoMcp: h.d.NoMCP, Effort: "default", Rules: append([]string{}, h.d.Allow...),
		RoleModels: map[string]string{},
	}, MaxWorkers: swarmWorkerCeiling}
	workers := h.d.Workers
	d.Swarm = &workers
	for k, v := range h.d.RoleModels {
		d.RoleModels[k] = v
	}
	if h.d.BudgetUSD > 0 {
		b := h.d.BudgetUSD
		d.Budget = &b
	}
	if cfg != nil {
		if d.Model == "" {
			d.Model = cfg.Models.Default
		}
		if d.Isolation == "" {
			d.Isolation = cfg.Swarm.Isolation
		}
		if cfg.Swarm.MaxWorkers > 0 {
			d.MaxWorkers = cfg.Swarm.MaxWorkers
		}
		if d.Budget == nil && cfg.Swarm.BudgetUSD > 0 {
			b := cfg.Swarm.BudgetUSD
			d.Budget = &b
		}
		d.Mailman = cfg.Swarm.Mailman
	}
	if d.Isolation == "" {
		d.Isolation = config.IsolationNone
	}
	if h.d.Mailman != nil {
		d.Mailman = *h.d.Mailman
	}
	if d.Mode == "" {
		d.Mode = "default"
	}
	return d
}

// ---- trust step of a new session ---------------------------------------------------------------------------------------------

// d16 is the first 16 hex characters of SHA-256 over the JSON of v: the digest that the scope of a confirmation carries, which the
// page computes the same way (docs/WEB-API.md).
func d16(v any) string {
	b, _ := json.Marshal(v)
	return hex.EncodeToString(sha256Sum(b))[:16]
}

// gateNewSession authorizes what a new session raises (p): nothing raised goes on; trusting the project's files is answered first
// with the trust challenge (409 trust_required: the files, and a confirmation id for the session's scope), whose repeat records the
// trust as `sleipnir trust add` does; anything else raised needs the confirmation of its scope (428). It reports false when it has
// answered the request; note is a row for the session (a partial footprint is trusted for this session only), and trusted the
// footprint of the files the person confirmed (nil when trust was not raised).
func (h *webHostImpl) gateNewSession(w http.ResponseWriter, r *http.Request, p privileges) (note string, trusted *trust.Footprint, ok bool) {
	reasons := p.reasons()
	if len(reasons) == 0 {
		return "", nil, true
	}
	if _, err := showReasons(reasons); err != nil {
		writeErr(w, err)
		return "", nil, false
	}
	scope := newSessionScope(p)
	home, _ := os.UserHomeDir()
	var fp *trust.Footprint
	ledger := trust.OpenLedger(session.TrustLedgerPath(home))
	if p.Trust != "" {
		f, err := trust.Scan(rootOf(p.Dir), p.Dir, home)
		if err != nil {
			writeErr(w, werr(http.StatusConflict, "trust_required", "the project's files could not be read: "+clip(err.Error(), 200)))
			return "", nil, false
		}
		fp = f
		confirm := r.Header.Get(web.ConfirmHeader)
		h.mu.Lock()
		issued, known := h.trustIDs[confirm]
		h.mu.Unlock()
		if confirm == "" || (known && issued.scope != scope) {
			h.trustChallenge(w, r, scope, p.Dir, fp, ledger, known)
			return "", nil, false
		}
	}
	if err := authorize(r.Context(), scope, reasons); err != nil {
		writeErr(w, err)
		return "", nil, false
	}
	if fp == nil {
		return "", nil, true
	}
	h.mu.Lock()
	delete(h.trustIDs, r.Header.Get(web.ConfirmHeader))
	h.mu.Unlock()
	if fp.Partial {
		return "this project has more files than can be remembered, so it is trusted for this session only", fp, true
	}
	if err := session.RememberTrust(home, p.Dir, fp, time.Now()); err != nil {
		return "trusted for this session; not remembered: " + clip(err.Error(), 200), fp, true
	}
	return "", fp, true
}

// trustChallenge answers 409 trust_required: the files of the project that would be trusted, and a confirmation id for scope that
// the repeated request sends as X-Confirm.
func (h *webHostImpl) trustChallenge(w http.ResponseWriter, r *http.Request, scope, dir string, fp *trust.Footprint, ledger *trust.Ledger, again bool) {
	id := h.srv.IssueConfirm(r, scope)
	if id == "" {
		web.Error(w, http.StatusTooManyRequests, "rate_limited", "too many confirmations are outstanding")
		return
	}
	h.mu.Lock()
	for k, v := range h.trustIDs {
		if time.Since(v.at) > 2*time.Minute {
			delete(h.trustIDs, k)
		}
	}
	h.trustIDs[id] = trustIssued{scope: scope, at: time.Now()}
	h.mu.Unlock()
	ch := wire.TrustChallenge{Dir: dir, Digest: fp.Digest, Partial: fp.Partial, Confirm: id, Scope: scope, Files: []wire.TrustFile{}}
	if state, entry := ledger.Check(dir, fp); state == trust.Changed {
		ch.Changed = trust.DescribeChanges(trust.Changes(entry, fp))
	}
	for _, f := range fp.Files {
		ch.Files = append(ch.Files, wire.TrustFile{Path: clip(f.Path, 4096), Kind: string(f.Kind), Bytes: f.Size, Hash: f.Sum})
	}
	for _, u := range fp.Unread {
		ch.Files = append(ch.Files, wire.TrustFile{Path: clip(u, 4096), Kind: "unread"}) // what the digest does not cover
	}
	msg := "trust the files of this project first"
	if fp.Partial {
		msg = "trust the files of this project first; part of them could not be read (kind \"unread\"), and trusting it applies what is there too, for this session only"
	}
	if again {
		msg = "the project's files, or what the session asks for, changed since you were shown them: look again"
	}
	web.ErrorDetail(w, http.StatusConflict, "trust_required", msg, ch)
}

// ---- idempotency ---------------------------------------------------------------------------------------------------------------

// idemKey is the key of a request's client id, or "" when it has none (or a bad one).
func idemKey(route, clientID string) string {
	if clientID == "" || len(clientID) > 64 {
		return ""
	}
	return route + "\x00" + clientID
}

// idemClaim makes a request with a client id the one that acts, or answers it with the answer of the request that did. A repeat that
// arrives while the first is still being handled waits for it (bounded by its own context); when the first did not succeed, the repeat
// acts in its place (claimed). Without a key (no client id) the request always acts. When it is not claimed, the request has been
// answered.
func (h *webHostImpl) idemClaim(w http.ResponseWriter, r *http.Request, key string) (claimed bool) {
	if key == "" {
		return true
	}
	for {
		h.mu.Lock()
		for k, e := range h.idem {
			if e.ok && time.Since(e.at) > idemWindow {
				delete(h.idem, k)
			}
		}
		e := h.idem[key]
		if e == nil {
			if len(h.idem) >= 4096 {
				h.mu.Unlock()
				return true // too many to remember: the request acts unguarded
			}
			h.idem[key] = &idemEntry{at: time.Now(), done: make(chan struct{})}
			h.mu.Unlock()
			return true
		}
		h.mu.Unlock()
		select {
		case <-e.done:
		case <-r.Context().Done():
			writeErr(w, werr(http.StatusConflict, "busy", "the same request is still being handled"))
			return false
		}
		if e.ok {
			_ = web.WriteJSON(w, e.status, e.body)
			return false
		}
	}
}

// idemDone records the answer of a request that claimed key: a success is kept for the window and is the answer of every repeat; a
// failure is forgotten, so that a repeat acts again. Every claim ends with one call (a deferred idemDone(key, false, 0, nil) is a
// no-op after a success).
func (h *webHostImpl) idemDone(key string, ok bool, status int, body any) {
	if key == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	e := h.idem[key]
	if e == nil || e.isDone() {
		return
	}
	e.ok, e.status, e.body, e.at = ok, status, body, time.Now()
	if !ok {
		delete(h.idem, key)
	}
	close(e.done)
}

// isDone reports whether the entry's request has answered.
func (e *idemEntry) isDone() bool {
	select {
	case <-e.done:
		return true
	default:
		return false
	}
}

// ---- hello ---------------------------------------------------------------------------------------------------------------------

// helloUI is hello.ui: the UI build and the server's motion and bell preferences.
type helloUI struct {
	Version      string `json:"version"`
	ReduceMotion bool   `json:"reduceMotion,omitempty"`
	Bell         bool   `json:"bell"`
}

// helloUpdate is hello.update: a newer release is out.
type helloUpdate struct {
	Current string `json:"current"`
	Latest  string `json:"latest"`
	Notice  string `json:"notice"`
}

// helloBody is GET /api/hello: wire.Hello with the server's defaults for a new session, the update notice and the UI preferences.
type helloBody struct {
	wire.Hello
	UI       helloUI             `json:"ui"`
	Defaults *newSessionDefaults `json:"defaults,omitempty"`
	Update   *helloUpdate        `json:"update,omitempty"`
}

// uiVersioner is a server that can name the build of its UI.
type uiVersioner interface{ UIVersion() string }

// hello is GET /api/hello. The stream id is read first: the page opens the stream after it and misses nothing.
func (h *webHostImpl) hello(r *http.Request) helloBody {
	after := h.hub.LastID(seam.Topic)
	uiv := version
	if v, ok := any(h.srv).(uiVersioner); ok {
		uiv = v.UIVersion()
	}
	ui := helloUI{Version: uiv, Bell: os.Getenv("SLEIPNIR_BELL") != "0",
		ReduceMotion: os.Getenv("SLEIPNIR_ANIM") == "0" || os.Getenv("REDUCE_MOTION") == "1"}
	body := helloBody{Hello: wire.Hello{
		Boot: h.boot, Now: time.Now().UnixMilli(), Tabs: h.Tabs(), Active: h.Active(), Version: version, StreamAfter: after,
		Server: wire.ServerInfo{Addr: r.Host, Loopback: true, Version: version},
		Limits: wire.Limits{MaxBody: int(web.DefaultMaxBody), MaxMessage: maxMessage, MaxQuestions: approvals.DefaultMaxPerTab},
		UI:     wire.UIInfo{Version: uiv},
	}, UI: ui}
	d := h.defaults()
	body.Defaults = &d
	if n := updateNotice(); n != "" {
		st, _ := cachedUpdate()
		body.Update = &helloUpdate{Current: version, Latest: st, Notice: n}
	}
	return body
}

// ---- helpers ---------------------------------------------------------------------------------------------------------------------

// tabIDRE is the shape of a tab id in a path.
var tabIDRE = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)

// qidRE is the shape of a question id in a path.
var qidRE = regexp.MustCompile(`^q_[a-z2-7]{26}$`)

// writeErr answers a refusal: a confirmation that is required is 428 confirm_required with the scope in X-Confirm-Scope and in the
// detail ({"scope", "reasons"}); a confirmation that was refused has been answered already.
func writeErr(w http.ResponseWriter, err error) {
	var cr *confirmRequired
	switch {
	case errors.Is(err, errConfirmAnswered):
	case errors.As(err, &cr):
		w.Header().Set("X-Confirm-Scope", cr.Scope)
		web.ErrorDetail(w, http.StatusPreconditionRequired, "confirm_required", "this action needs a confirmation: obtain an id for the scope and send it as X-Confirm", cr)
	default:
		web.WriteError(w, err)
	}
}

// ok answers 200 with v.
func ok(w http.ResponseWriter, v any) { _ = web.WriteJSON(w, http.StatusOK, v) }

// okBody is the answer of an action that returns nothing.
var okBody = map[string]bool{"ok": true}

// sessionErr turns a start or resume error into a short sentence for the page.
func sessionErr(err error) string { return clip(fmt.Sprint(err), 400) }

// sha256Sum is the SHA-256 of b.
func sha256Sum(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}

// cachedUpdate is the latest release the last update check found ("" when none was kept).
func cachedUpdate() (string, bool) {
	st, ok := update.Cached(updateOptions())
	return st.Latest, ok
}
