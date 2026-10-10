package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/provider/openaichat"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// testFloor is the answer floor of the host's tests: long enough that an answer sent the moment the question is seen is too soon on
// any machine.
const testFloor = 2 * time.Second

// webGuard is the most a host test waits for something that has to happen (a hang guard, not a timing).
const webGuard = 45 * time.Second

// webModel is a scripted mock provider for the host's tests: script answers a call; hold makes a call that names a word wait.
type webModel struct {
	mu     sync.Mutex
	script func(c *mock.Call) mock.Reply
	seen   []string
	holds  map[string]chan struct{}
	ts     interface{ Close() }
}

// newWebModel starts the provider and returns the base options of a session that uses it.
func newWebModel(t *testing.T, script func(c *mock.Call) mock.Reply) (*webModel, session.Options) {
	t.Helper()
	m := &webModel{script: script, holds: map[string]chan struct{}{}}
	srv := mock.New(mock.Config{Engine: mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64}}, m.respond)
	ts := srv.Start()
	t.Cleanup(ts.Close)
	t.Cleanup(m.releaseAll)
	prof := openaichat.DefaultProfile("mock", ts.URL)
	client := openaichat.New(openaichat.Config{Name: "mock", BaseURL: ts.URL, Profile: &prof})
	model := cost.Model{ID: "mock-1", ContextTokens: 1_000_000, MaxOutput: 4096, Cache: cost.OpenAICacheModel(),
		Price: cost.Price{InputPerM: 1, OutputPerM: 2, CacheReadPerM: 0.25, CacheWrite5mPerM: 1, CacheWrite1hPerM: 1}}
	cfg := config.Defaults()
	cfg.Providers = map[string]config.Provider{"mock": {BaseURL: ts.URL + "/v1"}} // what a model change resolves mock/mock-1 to
	return m, session.Options{Provider: client, ModelInfo: &model, Model: model.ID, Config: cfg, NoWeb: true,
		Offline: true, NoMCP: true, TrustProject: true}
}

// hold makes the calls whose last message contains word wait until release.
func (m *webModel) hold(word string) {
	m.mu.Lock()
	m.holds[word] = make(chan struct{})
	m.mu.Unlock()
}

// release lets the held calls of word answer.
func (m *webModel) release(word string) {
	m.mu.Lock()
	ch := m.holds[word]
	delete(m.holds, word)
	m.mu.Unlock()
	if ch != nil {
		close(ch)
	}
}

// releaseAll lets every held call answer.
func (m *webModel) releaseAll() {
	m.mu.Lock()
	words := make([]string, 0, len(m.holds))
	for w := range m.holds {
		words = append(words, w)
	}
	m.mu.Unlock()
	for _, w := range words {
		m.release(w)
	}
}

// respond records the person's message of the call and answers it with the script, after its hold.
func (m *webModel) respond(c *mock.Call) mock.Reply {
	last := c.LastUser()
	m.mu.Lock()
	if c.Messages[len(c.Messages)-1].Role == "user" && !strings.Contains(c.System, "You judge whether") {
		m.seen = append(m.seen, last)
	}
	var wait chan struct{}
	for w, ch := range m.holds {
		if strings.Contains(last, w) && c.Messages[len(c.Messages)-1].Role == "user" {
			wait = ch
		}
	}
	m.mu.Unlock()
	if wait != nil {
		<-wait
	}
	return m.script(c)
}

// asked reports whether a message containing word reached the model, and its position among the messages.
func (m *webModel) asked(word string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, s := range m.seen {
		if strings.Contains(s, word) {
			return i
		}
	}
	return -1
}

// frame is one message of the page's stream.
type frame struct {
	id    string
	event string
	data  json.RawMessage
}

// ev decodes the event of an "ev" frame (nil for another frame).
func (f frame) ev() map[string]any {
	if f.event != "ev" {
		return nil
	}
	var e struct {
		Tab string         `json:"tab"`
		Ev  map[string]any `json:"ev"`
	}
	if json.Unmarshal(f.data, &e) != nil {
		return nil
	}
	e.Ev["_tab"] = e.Tab
	return e.Ev
}

// webRig is a host behind a real server on a loopback port, with a reader of its stream.
type webRig struct {
	t      *testing.T
	h      *webHostImpl
	srv    *web.Server
	base   string
	token  string
	cancel context.CancelFunc
	done   chan error
	*frameLog
}

// frameLog is what a page's stream delivered, read in the background.
type frameLog struct {
	t      *testing.T
	mu     sync.Mutex
	frames []frame
	notify chan struct{}
}

// readFrames reads the frames of a stream's body in the background.
func readFrames(t *testing.T, body io.ReadCloser) *frameLog {
	l := &frameLog{t: t, notify: make(chan struct{}, 1)}
	go func() {
		defer body.Close()
		sc := bufio.NewScanner(body)
		sc.Buffer(make([]byte, 0, 1<<20), 4<<20)
		var f frame
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "id: "):
				f.id = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "event: "):
				f.event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				f.data = json.RawMessage(strings.TrimPrefix(line, "data: "))
			case line == "" && f.event != "":
				l.mu.Lock()
				l.frames = append(l.frames, f)
				l.mu.Unlock()
				select {
				case l.notify <- struct{}{}:
				default:
				}
				f = frame{}
			}
		}
	}()
	return l
}

// newWebRig starts a host whose first tab starts in d.Cwd with the base options.
func newWebRig(t *testing.T, d webDefaults, base session.Options) *webRig {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h, err := newWebHost(ctx, d, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	h.base = base
	h.bridge = h.newBridge(testFloor, defaultGrace) // a floor that a loaded machine cannot outrun between seeing a question and answering it
	srv, err := web.New(web.Config{Addr: "127.0.0.1:0", Routes: h.register, Hub: webHubConfig})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := web.Listen("127.0.0.1:0", false)
	if err != nil {
		t.Fatal(err)
	}
	r := &webRig{t: t, h: h, srv: srv, base: "http://" + ln.Addr().String(), token: srv.Token(), cancel: cancel, done: make(chan error, 1)}
	go func() { r.done <- srv.Serve(ctx, ln) }()
	t.Cleanup(r.stop)
	r.stream()
	return r
}

// stop ends the server and then the host, as the command does.
func (r *webRig) stop() {
	r.cancel()
	select {
	case <-r.done:
	case <-time.After(webGuard):
		r.t.Error("the server did not stop")
	}
	finished := make(chan struct{})
	go func() {
		r.h.close()
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(webGuard):
		r.t.Error("the host did not close")
	}
}

// do sends a request with the token and, for writes, the header and the JSON content type; it returns the status and the body.
func (r *webRig) do(method, path string, body any, hdr ...string) (int, []byte) {
	r.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, r.base+path, rd)
	if err != nil {
		r.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	if method != http.MethodGet {
		req.Header.Set(web.RequestHeader, "1")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := (&http.Client{Timeout: webGuard}).Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// json sends a request and decodes the answer into v, failing unless the status is want.
func (r *webRig) json(method, path string, body any, want int, v any, hdr ...string) {
	r.t.Helper()
	code, b := r.do(method, path, body, hdr...)
	if code != want {
		r.t.Fatalf("%s %s = %d %s, want %d", method, path, code, b, want)
	}
	if v != nil {
		if err := json.Unmarshal(b, v); err != nil {
			r.t.Fatalf("%s %s: %v: %s", method, path, err, b)
		}
	}
}

// stream reads the page's stream in the background.
func (r *webRig) stream() {
	req, _ := http.NewRequest(http.MethodGet, r.base+"/api/stream?after=0", nil)
	req.Header.Set("Authorization", "Bearer "+r.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		r.t.Fatalf("stream = %d", resp.StatusCode)
	}
	r.frameLog = readFrames(r.t, resp.Body)
}

// waitFor waits until a frame the predicate accepts has arrived and returns it.
func (r *frameLog) waitFor(what string, pred func(frame) bool) frame {
	r.t.Helper()
	deadline := time.After(webGuard)
	seen := 0
	for {
		r.mu.Lock()
		for ; seen < len(r.frames); seen++ {
			if pred(r.frames[seen]) {
				f := r.frames[seen]
				r.mu.Unlock()
				return f
			}
		}
		r.mu.Unlock()
		select {
		case <-r.notify:
		case <-time.After(50 * time.Millisecond):
		case <-deadline:
			r.t.Fatalf("no frame %s within %v; the stream had %d frames:\n%s", what, webGuard, len(r.frames), r.dump())
			return frame{}
		}
	}
}

// dump is the stream so far, for a failure.
func (r *frameLog) dump() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var b strings.Builder
	for _, f := range r.frames {
		fmt.Fprintf(&b, "%s %s\n", f.event, oneLineCLI(string(f.data), 300))
	}
	return b.String()
}

// waitEv waits for an event of a kind of a tab whose fields include want.
func (r *frameLog) waitEv(tab, kind string, want map[string]any) map[string]any {
	r.t.Helper()
	var got map[string]any
	r.waitFor(fmt.Sprintf("ev %s %v of %s", kind, want, tab), func(f frame) bool {
		e := f.ev()
		if e == nil || e["k"] != kind || (tab != "" && e["_tab"] != tab) {
			return false
		}
		for k, v := range want {
			if fmt.Sprint(e[k]) != fmt.Sprint(v) {
				return false
			}
		}
		got = e
		return true
	})
	return got
}

// ready waits until a tab's session has started and returns the tab.
func (r *webRig) ready(id string) wire.TabSummary {
	r.t.Helper()
	deadline := time.Now().Add(webGuard)
	for time.Now().Before(deadline) {
		var body struct{ Tabs []wire.TabSummary }
		r.json("GET", "/api/sessions", nil, 200, &body)
		for _, tb := range body.Tabs {
			if (id == "" || tb.ID == id) && tb.SID != "" {
				if t := r.h.tab(tb.ID); t != nil && t.session() != nil {
					return tb
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	r.t.Fatalf("tab %q did not start:\n%s", id, r.dump())
	return wire.TabSummary{}
}

// webWorld is a private HOME and state directory and a project for a host test.
func webWorld(t *testing.T) (project string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("SLEIPNIR_HOME", filepath.Join(root, "state"))
	project = filepath.Join(root, "project")
	for _, d := range []string{filepath.Join(root, "home"), filepath.Join(root, "state"), filepath.Join(project, ".sleipnir")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return project
}

// say is a script that answers every call with text, and the judge with done.
func sayScript(text string) func(c *mock.Call) mock.Reply {
	return func(c *mock.Call) mock.Reply {
		if strings.Contains(c.System, "You judge whether") {
			return mock.Reply{Text: `{"verdict":"done","reason":"it is done","left":[]}`}
		}
		return mock.Reply{Text: text}
	}
}

// soloDefaults are the server's defaults for a single agent in project.
func soloDefaults(project string) webDefaults {
	return webDefaults{Cwd: project, Workers: 0, WorkersGiven: true, Mode: "accept-edits"}
}

// A message is echoed when it is delivered, runs a turn whose answer streams to the page, and ends with final; the snapshot has it.
func TestWebHostRunsATurnAndStreamsIt(t *testing.T) {
	project := webWorld(t)
	_, base := newWebModel(t, sayScript("hello from the model"))
	r := newWebRig(t, soloDefaults(project), base)
	tab := r.ready("")
	var hello helloBody
	r.json("GET", "/api/hello", nil, 200, &hello)
	if len(hello.Tabs) != 1 || hello.Active != tab.ID || hello.Boot == "" || hello.Defaults == nil || hello.Defaults.Cwd != project {
		t.Fatalf("hello %+v", hello)
	}
	var res wire.SendResult
	r.json("POST", "/api/sessions/"+tab.ID+"/messages", wire.MessageRequest{Text: "hi there", Display: "hi"}, 200, &res)
	if res.Queued || res.ID == "" {
		t.Errorf("send %+v", res)
	}
	r.waitEv(tab.ID, "say", map[string]any{"who": "you", "text": "hi"})
	r.waitEv(tab.ID, "turn", map[string]any{"s": "start"})
	r.waitEv(tab.ID, "say", map[string]any{"who": "mgr"})
	r.waitEv(tab.ID, "final", nil)
	r.waitEv(tab.ID, "turn", map[string]any{"s": "end"})
	var snap wire.TabSnapshot
	r.json("GET", "/api/sessions/"+tab.ID+"/snapshot", nil, 200, &snap)
	if snap.Seq == 0 || len(snap.Events) == 0 || len(snap.Hist) != 1 || snap.Hist[0] != "hi" || snap.Meta.Mode == nil || *snap.Meta.Mode != "accept-edits" {
		t.Errorf("snapshot seq %d, %d events, hist %q, mode %v", snap.Seq, len(snap.Events), snap.Hist, snap.Meta.Mode)
	}
	if !strings.Contains(string(bytes.Join(rawsToBytes(snap.Events), nil)), `"who":"mgr"`) {
		t.Error("the snapshot lacks the answer")
	}
	if code, _ := r.do("POST", "/api/sessions/"+tab.ID+"/messages", wire.MessageRequest{Text: "  "}); code != 400 {
		t.Errorf("an empty message = %d", code)
	}
}

// rawsToBytes is the events of a snapshot as byte slices.
func rawsToBytes(rs []wire.Raw) [][]byte {
	out := make([][]byte, len(rs))
	for i, r := range rs {
		out[i] = r
	}
	return out
}

// Lines sent while a turn runs are queued in order (meta.queued), a look command answers at once beside the turn, and the lines
// are delivered one after the other when the turn is over.
func TestWebHostQueuesLinesBehindTheTurn(t *testing.T) {
	project := webWorld(t)
	m, base := newWebModel(t, sayScript("ok"))
	r := newWebRig(t, soloDefaults(project), base)
	tab := r.ready("")
	m.hold("slow")
	r.json("POST", "/api/sessions/"+tab.ID+"/messages", wire.MessageRequest{Text: "slow one"}, 200, nil)
	r.waitEv(tab.ID, "turn", map[string]any{"s": "start"})
	var a, b wire.SendResult
	r.json("POST", "/api/sessions/"+tab.ID+"/messages", wire.MessageRequest{Text: "second"}, 200, &a)
	r.json("POST", "/api/sessions/"+tab.ID+"/messages", wire.MessageRequest{Text: "third"}, 200, &b)
	if !a.Queued || a.Position != 1 || !b.Queued || b.Position != 2 {
		t.Fatalf("queued %+v %+v", a, b)
	}
	r.waitFor("meta with two queued lines", func(f frame) bool {
		var mf wire.MetaFrame
		return f.event == "meta" && json.Unmarshal(f.data, &mf) == nil && mf.Patch.Queued != nil && len(*mf.Patch.Queued) == 2
	})
	var out wire.CommandResult
	r.json("POST", "/api/sessions/"+tab.ID+"/command", wire.CommandRequest{Line: "/cost"}, 200, &out)
	if !strings.Contains(out.Output, "hit") {
		t.Errorf("/cost beside the turn: %+v", out)
	}
	if code, body := r.do("POST", "/api/sessions/"+tab.ID+"/command", wire.CommandRequest{Line: "/nosuch"}); code != 404 || !strings.Contains(string(body), "unknown_command") {
		t.Errorf("an unknown command = %d %s", code, body)
	}
	m.release("slow")
	r.waitEv(tab.ID, "say", map[string]any{"who": "you", "text": "third"})
	deadline := time.Now().Add(webGuard)
	for m.asked("third") < 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if s, th := m.asked("second"), m.asked("third"); s < 0 || th <= s {
		t.Errorf("the queued lines reached the model as %d and %d: %q", s, th, m.seen)
	}
}

// A standing goal is judged after each turn: the verdict and the goal's state reach the page, a continuation is sent while it is not
// met, and the verdicts are in the session's log (goal.judge).
func TestWebHostGoalLoopJudgesAndContinues(t *testing.T) {
	project := webWorld(t)
	var mu sync.Mutex
	judged := 0
	_, base := newWebModel(t, func(c *mock.Call) mock.Reply {
		if strings.Contains(c.System, "You judge whether") {
			mu.Lock()
			defer mu.Unlock()
			if judged++; judged == 1 {
				return mock.Reply{Text: `{"verdict":"continue","reason":"no build ran","left":["run the build"]}`}
			}
			return mock.Reply{Text: `{"verdict":"done","reason":"the build ran","left":[]}`}
		}
		return mock.Reply{Text: "working on it"}
	})
	r := newWebRig(t, soloDefaults(project), base)
	tab := r.ready("")
	var res struct {
		OK    bool
		State string
	}
	r.json("POST", "/api/sessions/"+tab.ID+"/goal", wire.GoalRequest{Action: "set", Text: "make it build"}, 200, &res)
	r.waitEv(tab.ID, "say", map[string]any{"who": "you", "text": "/goal make it build"})
	r.waitEv(tab.ID, "goal", map[string]any{"s": "active", "objective": "make it build"})
	r.waitEv(tab.ID, "goal", map[string]any{"s": "met"})
	r.waitEv(tab.ID, "final", nil)
	// Verdicts are coalescable on the stream (a newer one replaces an older one a slow page has not read yet); the journal has
	// every one, in order.
	var snap wire.TabSnapshot
	r.json("GET", "/api/sessions/"+tab.ID+"/snapshot", nil, 200, &snap)
	var verdicts []string
	for _, raw := range snap.Events {
		var e struct{ K, Kind, Text string }
		if json.Unmarshal(raw, &e) == nil && e.K == "verdict" {
			verdicts = append(verdicts, e.Kind+": "+e.Text)
		}
	}
	want := []string{"checking: checking: the judge reads the evidence of this turn", "continue: not yet: no build ran",
		"checking: checking: the judge reads the evidence of this turn", "done: met: the build ran"}
	if strings.Join(verdicts, "\n") != strings.Join(want, "\n") {
		t.Errorf("verdicts in the journal:\n%s", strings.Join(verdicts, "\n"))
	}
	r.json("POST", "/api/sessions/"+tab.ID+"/goal", wire.GoalRequest{Action: "pause"}, 409, nil)
	s := r.h.tab(tab.ID).session()
	_ = s.Log.Flush()
	n := 0
	_ = events.Scan(filepath.Join(s.Dir, "events.jsonl"), func(e events.Event) error {
		if e.Type == "goal.judge" {
			n++
		}
		return nil
	})
	if n != 2 {
		t.Errorf("%d goal.judge events, want 2", n)
	}
}

// A question goes to the page with an unguessable id; an answer in the same instant is refused (too_soon), one after the floor is
// taken, a second one is refused, and the tool goes ahead.
func TestWebHostAsksThePageAndTakesItsAnswer(t *testing.T) {
	project := webWorld(t)
	_, base := newWebModel(t, func(c *mock.Call) mock.Reply {
		if c.Messages[len(c.Messages)-1].Role == "tool" {
			return mock.Reply{Text: "written"}
		}
		return mock.Reply{ToolCalls: []mock.ToolCall{{ID: "w1", Name: "write", Args: `{"path":"notes.txt","content":"hello\n"}`}}}
	})
	d := soloDefaults(project)
	d.Mode = "default"
	r := newWebRig(t, d, base)
	tab := r.ready("")
	r.json("POST", "/api/sessions/"+tab.ID+"/messages", wire.MessageRequest{Text: "write the notes"}, 200, nil)
	ask := r.waitEv(tab.ID, "ask", nil)
	q, _ := ask["q"].(map[string]any)
	qid, _ := q["id"].(string)
	if !qidRE.MatchString(qid) || q["kind"] != "edit" || q["agent"] != "mgr" || q["cwd"] != "." || q["path"] != "notes.txt" || !strings.Contains(fmt.Sprint(q["change"]), "+hello") {
		t.Fatalf("ask %v: want the change the write asks to make, its path, and the directory relative to the project", ask)
	}
	var open struct{ Questions []wire.OpenQuestion }
	r.json("GET", "/api/questions", nil, 200, &open)
	if len(open.Questions) != 1 || open.Questions[0].Tab != tab.ID {
		t.Fatalf("questions %+v", open)
	}
	code, body := r.do("POST", "/api/questions/"+qid+"/answer", wire.AnswerRequest{Choice: 1})
	var tooSoon struct {
		Code   string
		Detail struct{ RetryAfterMs int64 }
	}
	if code != 409 || json.Unmarshal(body, &tooSoon) != nil || tooSoon.Code != "too_soon" || tooSoon.Detail.RetryAfterMs <= 0 || tooSoon.Detail.RetryAfterMs > testFloor.Milliseconds() {
		t.Fatalf("an answer at once = %d %s", code, body)
	}
	time.Sleep(time.Duration(tooSoon.Detail.RetryAfterMs+50) * time.Millisecond)
	r.json("POST", "/api/questions/"+qid+"/answer", wire.AnswerRequest{Choice: 1}, 200, nil)
	r.waitEv(tab.ID, "answer", map[string]any{"qid": qid, "choice": 1, "by": "you"})
	if code, _ := r.do("POST", "/api/questions/"+qid+"/answer", wire.AnswerRequest{Choice: 1}); code != 409 {
		t.Errorf("a second answer = %d", code)
	}
	if code, _ := r.do("POST", "/api/questions/q_nope/answer", wire.AnswerRequest{Choice: 1}); code != 400 {
		t.Errorf("a malformed id = %d", code)
	}
	r.waitEv(tab.ID, "final", nil)
	if _, err := os.Stat(filepath.Join(project, "notes.txt")); err != nil {
		t.Errorf("the write did not happen: %v", err)
	}
}

// An interrupt cancels the turn, refuses the question of the agent the person talks to, and pauses the goal; with nothing running it
// is refused.
func TestWebHostInterruptRefusesTheQuestionAndPausesTheGoal(t *testing.T) {
	project := webWorld(t)
	_, base := newWebModel(t, func(c *mock.Call) mock.Reply {
		if strings.Contains(c.System, "You judge whether") {
			return mock.Reply{Text: `{"verdict":"continue","reason":"x","left":[]}`}
		}
		if c.Messages[len(c.Messages)-1].Role == "tool" {
			return mock.Reply{Text: "done"}
		}
		return mock.Reply{ToolCalls: []mock.ToolCall{{ID: "b1", Name: "bash", Args: `{"command":"rm -rf build"}`}}}
	})
	d := soloDefaults(project)
	d.Mode = "default"
	r := newWebRig(t, d, base)
	tab := r.ready("")
	r.json("POST", "/api/sessions/"+tab.ID+"/goal", wire.GoalRequest{Action: "set", Text: "clean up"}, 200, nil)
	ask := r.waitEv(tab.ID, "ask", nil)
	qid := ask["q"].(map[string]any)["id"]
	r.json("POST", "/api/sessions/"+tab.ID+"/interrupt", map[string]string{"target": "turn"}, 200, nil)
	r.waitEv(tab.ID, "answer", map[string]any{"qid": qid, "by": "canceled", "choice": 3})
	r.waitEv(tab.ID, "interrupt", map[string]any{"id": "turn"})
	r.waitEv(tab.ID, "goal", map[string]any{"s": "paused", "paused": "you interrupted it"})
	r.waitEv(tab.ID, "turn", map[string]any{"s": "end"})
	if code, body := r.do("POST", "/api/sessions/"+tab.ID+"/interrupt", map[string]string{"target": "turn"}); code != 409 || !strings.Contains(string(body), "idle") {
		t.Errorf("an interrupt with nothing running = %d %s", code, body)
	}
	if code, _ := r.do("POST", "/api/sessions/"+tab.ID+"/stop", nil); code != 409 {
		t.Errorf("stop with nothing running = %d", code)
	}
	if code, _ := r.do("POST", "/api/sessions/"+tab.ID+"/steer", map[string]string{"text": "x"}); code != 409 {
		t.Errorf("a steer with nothing running = %d", code)
	}
}

// The restart family runs in process: /new starts a new session in the tab (a new generation and sid, an empty journal); /restart
// carries the conversation (the same sid); a single agent's model change does not restart.
func TestWebHostRestartKinds(t *testing.T) {
	project := webWorld(t)
	_, base := newWebModel(t, sayScript("ok"))
	r := newWebRig(t, soloDefaults(project), base)
	tab := r.ready("")
	r.json("POST", "/api/sessions/"+tab.ID+"/messages", wire.MessageRequest{Text: "first"}, 200, nil)
	r.waitEv(tab.ID, "final", nil)

	var gen struct{ Gen uint64 }
	r.json("POST", "/api/sessions/"+tab.ID+"/restart", wire.RestartRequest{Kind: "restart"}, 202, &gen)
	if gen.Gen != 2 {
		t.Errorf("gen after restart = %d", gen.Gen)
	}
	r.waitFor("reset to gen 2", func(f frame) bool { return f.event == "reset" && strings.Contains(string(f.data), `"gen":2`) })
	carried := r.waitForSID(tab.ID, 2)
	if carried != tab.SID {
		t.Errorf("a restart that carries the conversation has sid %s, want %s", carried, tab.SID)
	}
	r.json("POST", "/api/sessions/"+tab.ID+"/restart", wire.RestartRequest{Kind: "new", Fresh: true}, 202, &gen)
	fresh := r.waitForSID(tab.ID, 3)
	if fresh == tab.SID || fresh == "" {
		t.Errorf("/new kept the sid %s", fresh)
	}
	var snap wire.TabSnapshot
	r.json("GET", "/api/sessions/"+tab.ID+"/snapshot", nil, 200, &snap)
	if snap.Gen != 3 || strings.Contains(string(bytes.Join(rawsToBytes(snap.Events), nil)), `"text":"first"`) {
		t.Errorf("after /new: gen %d events %d", snap.Gen, len(snap.Events))
	}
	var model struct{ Restarted bool }
	r.json("POST", "/api/sessions/"+tab.ID+"/model", wire.ModelRequest{Ref: "mock/mock-1"}, 200, &model)
	if model.Restarted {
		t.Error("a single agent's model change restarted the session")
	}
	r.waitEv(tab.ID, "sys", map[string]any{"text": "model: mock/mock-1 (the conversation carries over; the prompt cache starts over)"})
	if got := r.h.tab(tab.ID).Summary(); got.Gen != 3 || got.SID != fresh {
		t.Errorf("after a model change: gen %d sid %s", got.Gen, got.SID)
	}
	r.json("POST", "/api/sessions/"+tab.ID+"/model", wire.ModelRequest{Ref: " "}, 422, nil)
	if code, _ := r.do("POST", "/api/sessions/"+tab.ID+"/restart", wire.RestartRequest{Kind: "sideways"}); code != 400 {
		t.Errorf("an unknown kind = %d", code)
	}
}

// waitForSID waits until the tab is at gen with a started session and returns its sid.
func (r *webRig) waitForSID(id string, gen uint64) string {
	r.t.Helper()
	deadline := time.Now().Add(webGuard)
	for time.Now().Before(deadline) {
		if t := r.h.tab(id); t != nil {
			t.mu.Lock()
			done := t.gen == gen && !t.restarting && !t.starting && t.s != nil
			t.mu.Unlock()
			if sum := t.Summary(); done && sum.SID != "" {
				return sum.SID
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	r.t.Fatalf("tab %s did not reach gen %d:\n%s", id, gen, r.dump())
	return ""
}

// Two sessions live in one process: a second tab starts in another project; the last tab cannot be closed; a closed session can be
// resumed in a new tab (its lock was released), and a session that a tab hosts is not opened twice.
func TestWebHostHostsSeveralSessions(t *testing.T) {
	project := webWorld(t)
	other := filepath.Join(filepath.Dir(project), "other")
	if err := os.MkdirAll(filepath.Join(other, ".sleipnir"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, base := newWebModel(t, sayScript("ok"))
	d := soloDefaults(project)
	d.Projects = []string{other}
	r := newWebRig(t, d, base)
	first := r.ready("")
	r.json("POST", "/api/sessions/"+first.ID+"/messages", wire.MessageRequest{Text: "one"}, 200, nil)
	r.waitEv(first.ID, "final", nil)

	zero := 0
	var created struct{ Tab wire.TabSummary }
	r.json("POST", "/api/sessions", newSessionRequest{NewSessionRequest: wire.NewSessionRequest{Cwd: other, Swarm: &zero, Name: "other"}, ClientID: "c1"}, 201, &created)
	var again struct{ Tab wire.TabSummary }
	r.json("POST", "/api/sessions", newSessionRequest{NewSessionRequest: wire.NewSessionRequest{Cwd: other, Swarm: &zero, Name: "other"}, ClientID: "c1"}, 201, &again)
	if again.Tab.ID != created.Tab.ID {
		t.Errorf("a repeated client id made another tab: %s and %s", created.Tab.ID, again.Tab.ID)
	}
	second := r.ready(created.Tab.ID)
	if second.Cwd != other || second.Name != "other" {
		t.Errorf("second tab %+v", second)
	}
	r.json("POST", "/api/sessions/"+second.ID+"/messages", wire.MessageRequest{Text: "two"}, 200, nil)
	r.waitEv(second.ID, "final", nil)

	r.json("POST", "/api/sessions", wire.NewSessionRequest{Cwd: t.TempDir()}, 403, nil)
	r.json("DELETE", "/api/sessions/"+first.ID, nil, 200, nil)
	r.waitFor("tab remove", func(f frame) bool { return f.event == "tab" && strings.Contains(string(f.data), `"op":"remove"`) })
	if code, body := r.do("DELETE", "/api/sessions/"+second.ID, nil); code != 409 || !strings.Contains(string(body), "last") {
		t.Errorf("closing the last tab = %d %s", code, body)
	}
	var resumed struct{ Tab wire.TabSummary }
	r.json("POST", "/api/sessions/resume", wire.ResumeRequest{From: first.SID}, 201, &resumed)
	got := r.ready(resumed.Tab.ID)
	if got.SID != first.SID {
		t.Errorf("the resumed tab has sid %s, want %s", got.SID, first.SID)
	}
	if code, body := r.do("POST", "/api/sessions/resume", wire.ResumeRequest{From: first.SID}); code != 409 || !strings.Contains(string(body), "hosted") {
		t.Errorf("resuming a hosted session = %d %s", code, body)
	}
	if code, _ := r.do("POST", "/api/sessions/resume", wire.ResumeRequest{From: "20200101-000000-abcdef"}); code != 404 {
		t.Errorf("resuming nothing = %d", code)
	}
}

// The session settings act on the session and are acknowledged: the mode (bypass only with a confirmation), the effort, the
// budget, the rules with their origins, the staged launch flags.
func TestWebHostSessionSettings(t *testing.T) {
	project := webWorld(t)
	_, base := newWebModel(t, sayScript("ok"))
	r := newWebRig(t, soloDefaults(project), base)
	tab := r.ready("")
	path := "/api/sessions/" + tab.ID
	r.json("POST", path+"/mode", wire.ModeRequest{Mode: "plan"}, 200, nil)
	r.waitEv(tab.ID, "sys", map[string]any{"text": "mode: plan (read-only)"})
	if code, _ := r.do("POST", path+"/mode", wire.ModeRequest{Mode: "bypass"}); code != 428 {
		t.Errorf("bypass without a confirmation = %d", code)
	}
	var c wire.ConfirmID
	r.json("POST", "/api/confirm", wire.ConfirmRequest{Scope: "mode:bypass:" + tab.ID}, 200, &c)
	if code, body := r.do("POST", path+"/mode", wire.ModeRequest{Mode: "bypass"}, web.ConfirmHeader, c.ID); code != 200 {
		t.Errorf("bypass with its confirmation = %d %s", code, body)
	}
	r.json("POST", path+"/mode", wire.ModeRequest{Mode: "sideways"}, 400, nil)
	var eff wire.EffortResult
	r.json("POST", path+"/effort", wire.EffortRequest{Level: "high"}, 200, &eff)
	if eff.Requested != "high" {
		t.Errorf("effort %+v", eff)
	}
	r.json("POST", path+"/effort", wire.EffortRequest{Level: "extreme"}, 400, nil)
	r.json("POST", path+"/budget", wire.BudgetRequest{USD: 5}, 200, nil)
	r.waitEv(tab.ID, "sys", map[string]any{"glyph": "◇"})
	r.json("POST", path+"/budget", wire.BudgetRequest{USD: -1}, 400, nil)
	var added wire.RuleResult
	scope := r.needsConfirm("POST", path+"/rules", wire.RuleRequest{Effect: "allow", Rule: "tests"})
	r.json("POST", path+"/rules", wire.RuleRequest{Effect: "allow", Rule: "tests"}, 200, &added, web.ConfirmHeader, r.confirmFor(scope))
	if added.Added < 10 {
		t.Errorf("the tests preset added %d rules", added.Added)
	}
	r.json("POST", path+"/rules", wire.RuleRequest{Effect: "deny", Rule: "Bash(rm:*)"}, 200, nil)
	r.json("POST", path+"/rules", wire.RuleRequest{Effect: "allow", Rule: ""}, 400, nil)
	var rules struct{ Rules []wire.Rule }
	r.json("GET", path+"/rules", nil, 200, &rules)
	found := map[string]string{}
	for _, rl := range rules.Rules {
		found[rl.Effect+" "+rl.Rule] = rl.Origin
	}
	if found["allow Bash(go test:*)"] != "the tests preset" || found["deny Bash(rm:*)"] != "this session" {
		t.Errorf("rules %v", found)
	}
	iso := "worktree"
	r.json("PATCH", path+"/launch", wire.LaunchPatch{Isolation: &iso}, 200, nil)
	bad := "docker"
	r.json("PATCH", path+"/launch", wire.LaunchPatch{Isolation: &bad}, 400, nil)
	var snap wire.TabSnapshot
	r.json("GET", path+"/snapshot", nil, 200, &snap)
	if snap.Meta.Isolation == nil || *snap.Meta.Isolation != "worktree" || snap.Meta.Launch == nil || !strings.Contains(*snap.Meta.Launch, "--isolation worktree") {
		t.Errorf("staged isolation in meta: %v %v", snap.Meta.Isolation, snap.Meta.Launch)
	}
	var slash struct{ Slash []wire.SlashEntry }
	r.json("GET", path+"/slash", nil, 200, &slash)
	if len(slash.Slash) < 30 || slash.Slash[0].Group == "" {
		t.Errorf("slash list: %d entries", len(slash.Slash))
	}
	r.json("PATCH", path, map[string]string{"name": "renamed"}, 200, nil)
	r.json("PATCH", path, map[string]string{"name": strings.Repeat("x", 61)}, 400, nil)
}

// Every route of the host is behind the envelope: no credential is 401, another origin is 403, a write without the custom header is
// 403, a bad id is 400 before any lookup, an unknown tab is 404, an oversized message is 413, an unknown field is 400.
func TestWebHostRoutesAreBehindTheEnvelope(t *testing.T) {
	project := webWorld(t)
	_, base := newWebModel(t, sayScript("ok"))
	r := newWebRig(t, soloDefaults(project), base)
	tab := r.ready("")
	raw := func(method, path, body string, hdr map[string]string) int {
		req, _ := http.NewRequest(method, r.base+path, strings.NewReader(body))
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	bearer := "Bearer " + r.token
	routes := []struct{ method, path, body string }{
		{"GET", "/api/hello", ""}, {"GET", "/api/sessions", ""}, {"GET", "/api/projects", ""}, {"GET", "/api/questions", ""},
		{"GET", "/api/snapshot", ""}, {"GET", "/api/sessions/" + tab.ID + "/snapshot", ""},
		{"POST", "/api/sessions", `{"cwd":"` + project + `"}`}, {"POST", "/api/sessions/" + tab.ID + "/messages", `{"text":"x"}`},
		{"POST", "/api/sessions/" + tab.ID + "/mode", `{"mode":"plan"}`}, {"POST", "/api/sessions/" + tab.ID + "/rules", `{"rule":"tests"}`},
		{"DELETE", "/api/sessions/" + tab.ID, ""}, {"POST", "/api/questions/q_aaaaaaaaaaaaaaaaaaaaaaaaaa/answer", `{"choice":1}`},
	}
	for _, rt := range routes {
		if code := raw(rt.method, rt.path, rt.body, map[string]string{"Content-Type": "application/json", web.RequestHeader: "1"}); code != 401 {
			t.Errorf("%s %s without a credential = %d", rt.method, rt.path, code)
		}
		if rt.method == "GET" {
			continue
		}
		if code := raw(rt.method, rt.path, rt.body, map[string]string{"Authorization": bearer, "Content-Type": "application/json", "Origin": "http://evil.example", web.RequestHeader: "1"}); code != 403 {
			t.Errorf("%s %s from another origin = %d", rt.method, rt.path, code)
		}
		if code := raw(rt.method, rt.path, rt.body, map[string]string{"Authorization": bearer, "Content-Type": "application/json"}); code != 403 {
			t.Errorf("%s %s without the custom header = %d", rt.method, rt.path, code)
		}
		if rt.body != "" {
			if code := raw(rt.method, rt.path, rt.body, map[string]string{"Authorization": bearer, "Content-Type": "text/plain", web.RequestHeader: "1"}); code != 415 {
				t.Errorf("%s %s as text = %d", rt.method, rt.path, code)
			}
		}
	}
	if code, _ := r.do("GET", "/api/sessions/BAD_ID/snapshot", nil); code != 400 {
		t.Errorf("a bad tab id = %d", code)
	}
	if code, _ := r.do("GET", "/api/sessions/nosuch/snapshot", nil); code != 404 {
		t.Errorf("an unknown tab = %d", code)
	}
	if code, _ := r.do("POST", "/api/sessions/"+tab.ID+"/messages", wire.MessageRequest{Text: strings.Repeat("x", maxMessage+1)}); code != 413 {
		t.Errorf("an oversized message = %d", code)
	}
	if code, _ := r.do("POST", "/api/sessions/"+tab.ID+"/mode", map[string]any{"mode": "plan", "extra": 1}); code != 400 {
		t.Errorf("an unknown field = %d", code)
	}
	if code, _ := r.do("POST", "/api/sessions/"+tab.ID+"/restart", wire.RestartRequest{Kind: "restart", Flags: []string{"--mode", "yolo"}}); code != 428 {
		t.Errorf("a restart into yolo without a confirmation = %d", code)
	}
}

// A new session that asks for the project's files is answered with the trust challenge first; the repeat with its confirmation
// records the trust and starts; files that changed in between are a new challenge.
func TestWebHostTrustStep(t *testing.T) {
	project := webWorld(t)
	other := filepath.Join(filepath.Dir(project), "trusty")
	if err := os.MkdirAll(filepath.Join(other, ".sleipnir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "AGENTS.md"), []byte("be nice\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, base := newWebModel(t, sayScript("ok"))
	d := soloDefaults(project)
	d.Projects = []string{other}
	r := newWebRig(t, d, base)
	r.ready("")
	zero := 0
	req := wire.NewSessionRequest{Cwd: other, Swarm: &zero, TrustProject: true}
	code, body := r.do("POST", "/api/sessions", req)
	if code != 409 || !strings.Contains(string(body), "trust_required") {
		t.Fatalf("an untrusted project = %d %s", code, body)
	}
	var e struct{ Detail wire.TrustChallenge }
	_ = json.Unmarshal(body, &e)
	if e.Detail.Confirm == "" || len(e.Detail.Files) == 0 || e.Detail.Scope == "" {
		t.Fatalf("challenge %+v", e.Detail)
	}
	if err := os.WriteFile(filepath.Join(other, "AGENTS.md"), []byte("be nicer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, body := r.do("POST", "/api/sessions", req, web.ConfirmHeader, e.Detail.Confirm); code != 409 || !strings.Contains(string(body), "trust_required") {
		t.Fatalf("changed files = %d %s", code, body)
	}
	code, body = r.do("POST", "/api/sessions", req)
	_ = json.Unmarshal(body, &e)
	if code, body := r.do("POST", "/api/sessions", req, web.ConfirmHeader, e.Detail.Confirm); code != 201 {
		t.Fatalf("the repeat with its confirmation = %d %s", code, body)
	}
	var projects struct{ Projects []wire.Project }
	r.json("GET", "/api/projects", nil, 200, &projects)
	for _, p := range projects.Projects {
		if p.Dir == other && p.Trust != "trusted" {
			t.Errorf("the project after the trust step: %+v", p)
		}
	}
}

// The chat's flags are parsed by one parser for the chat, the restarts and the New session dialog: the dialog's request becomes the
// arguments the dialog's command line shows, and they give the options the request asked for.
func TestChatFlagsAreSharedWithTheNewSessionDialog(t *testing.T) {
	three, five := 3, 5.0
	req := wire.NewSessionRequest{Cwd: "/p", Model: "mock/mock-1", Mode: "bypass", Swarm: &three, Isolation: "worktree", Verify: "go test {dirs}",
		Commit: true, Mailman: true, Budget: &five, Rules: []string{"tests", "Bash(make:*)"}, TrustProject: true, NoMcp: true,
		RoleModels: map[string]string{"scout": "a/b", "backend": "c/d"}}
	args := argsFor(req)
	want := "--cwd /p --model mock/mock-1 --mode bypass --swarm 3 --isolation worktree --verify go test {dirs} --commit --mailman --budget-usd 5 --allow tests --allow Bash(make:*) --trust-project --no-mcp --role-model backend=c/d --role-model scout=a/b"
	if got := strings.Join(args, " "); got != want {
		t.Errorf("argsFor:\n got %s\nwant %s", got, want)
	}
	f, err := parseChatFlags(args)
	if err != nil {
		t.Fatal(err)
	}
	o := f.options()
	if !o.Interactive || o.Cwd != "/p" || o.Model != "mock/mock-1" || string(o.Mode) != "bypass" || !o.Swarm || o.Workers != 3 || o.Isolation != "worktree" ||
		o.Verify != "go test {dirs}" || !o.Commit || o.Mailman == nil || !*o.Mailman || o.BudgetUSD != 5 || !o.TrustProject || !o.NoMCP ||
		o.RoleModels["scout"] != "a/b" || len(o.Allow) < 10 {
		t.Errorf("options %+v", o)
	}
	for _, bad := range [][]string{{"--mode", "sideways"}, {"--isolation", "docker"}, {"--swarm", "-1"}, {"--budget-usd", "-2"}, {"positional"}, {"--nope"}, {"--resume", "x", "--continue"}} {
		if _, err := parseChatFlags(bad); err == nil {
			t.Errorf("parseChatFlags(%q) accepted it", bad)
		}
	}
	if got := commandLine([]string{"--verify", "go test {dirs}", "--cwd", "/a b"}); got != "--verify 'go test {dirs}' --cwd '/a b'" {
		t.Errorf("commandLine = %s", got)
	}
}

// The meta patch carries what changed and nothing else.
func TestMetaDiffSendsWhatChanged(t *testing.T) {
	a := wire.MetaPatch{Mode: ptr("default"), Running: ptr(false), Rules: &[]wire.Rule{}}
	b := wire.MetaPatch{Mode: ptr("plan"), Running: ptr(false), Rules: &[]wire.Rule{}}
	p, changed := metaDiff(a, b)
	if !changed || p.Mode == nil || *p.Mode != "plan" || p.Running != nil || p.Rules != nil {
		t.Errorf("patch %+v changed %v", p, changed)
	}
	if _, changed := metaDiff(b, b); changed {
		t.Error("an unchanged meta was sent")
	}
}

// The slash list has every command of the chat's /help, grouped as /help groups them, with the palette's words.
func TestBuiltinSlashFollowsTheChatsHelp(t *testing.T) {
	got := map[string]bool{}
	for _, e := range builtinSlash() {
		if e.Group == "" || e.Desc == "" {
			t.Errorf("entry %+v", e)
		}
		got[strings.TrimPrefix(e.Cmd, "/")] = true
	}
	for _, c := range chatCommands {
		if !got[c.name] {
			t.Errorf("/%s is missing", c.name)
		}
	}
}

// keep exec imported for git availability checks in the helpers above
var _ = exec.LookPath
