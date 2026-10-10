package approvals

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/testutil"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// Every question waits in a goroutine of its own; a test that leaves one waiting fails the package.
func TestMain(m *testing.M) { os.Exit(testutil.CheckLeaks(m)) }

// clock is a settable time.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// rig is a bridge with a fake clock that records what it reports.
type rig struct {
	t       *testing.T
	c       *clock
	b       *Bridge
	mu      sync.Mutex
	asked   chan wire.Question
	answers []wire.Answer
	tabOf   map[string]string
}

func newRig(t *testing.T, cfg Config) *rig {
	r := &rig{t: t, c: &clock{t: time.Unix(1_800_000_000, 0)}, asked: make(chan wire.Question, 256), tabOf: map[string]string{}}
	cfg.Now = r.c.now
	cfg.OnAsk = func(tab string, q wire.Question) {
		r.mu.Lock()
		r.tabOf[q.ID] = tab
		r.mu.Unlock()
		r.asked <- q
	}
	cfg.OnAnswer = func(tab string, a wire.Answer) {
		r.mu.Lock()
		r.answers = append(r.answers, a)
		r.mu.Unlock()
	}
	r.b = New(cfg)
	t.Cleanup(r.b.Close)
	return r
}

// pending is a question being asked: its decision arrives on d.
type pending struct {
	q wire.Question
	d chan perm.Decision
}

// ask puts a request to the tab's prompter in a goroutine and waits until the bridge has reported the question.
func (r *rig) ask(ctx context.Context, tab string, req perm.Request) pending {
	r.t.Helper()
	p := r.b.Prompter(tab, "/proj", func(agent string) (string, string) {
		if agent == "fe-1" {
			return "T6", "web/**"
		}
		return "", ""
	}, func() bool { return false })
	d := make(chan perm.Decision, 1)
	go func() { d <- p(ctx, req) }()
	select {
	case q := <-r.asked:
		return pending{q: q, d: d}
	case <-time.After(10 * time.Second):
		r.t.Fatal("the question was not asked")
		return pending{}
	}
}

// decided waits for the decision of a pending question.
func (p pending) decided(t *testing.T) perm.Decision {
	t.Helper()
	select {
	case d := <-p.d:
		return d
	case <-time.After(10 * time.Second):
		t.Fatal("the question was not decided")
		return perm.Decision{}
	}
}

func (r *rig) answersCopy() []wire.Answer {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]wire.Answer(nil), r.answers...)
}

// code is the wire code of an error, or "" for another error or none.
func code(err error) string {
	var we *wire.Error
	if errors.As(err, &we) {
		return we.Code
	}
	return ""
}

var bash = perm.Request{Agent: "fe-1", Tool: "bash", Command: "npm install --save-dev vitest", Cwd: "/proj/web",
	Summary: "npm install --save-dev vitest [installs a package from the network]"}

func TestQuestionIDs(t *testing.T) {
	re := regexp.MustCompile(`^q_[a-z2-7]{26}$`)
	seen := map[string]bool{}
	for i := 0; i < 10000; i++ {
		id, err := newID()
		if err != nil {
			t.Fatal(err)
		}
		if !re.MatchString(id) || seen[id] {
			t.Fatalf("id %q: malformed or repeated", id)
		}
		seen[id] = true
	}
}

// An answer before the floor is refused with how long to wait; at the floor it is taken; a second answer is refused; and the floor
// starts again from the tab's last answer for the next question.
func TestAnswerOnceAndFloor(t *testing.T) {
	r := newRig(t, Config{})
	p := r.ask(context.Background(), "shop", bash)
	r.c.add(100 * time.Millisecond)
	_, err := r.b.Answer(context.Background(), p.q.ID, wire.AnswerRequest{Choice: 1})
	var we *wire.Error
	if !errors.As(err, &we) || we.Code != "too_soon" || we.Status != 409 {
		t.Fatalf("an answer at +100ms: %v", err)
	}
	if d, _ := we.Detail.(map[string]int64); d["retryAfterMs"] != 250 {
		t.Errorf("retryAfterMs = %v, want 250", we.Detail)
	}
	r.c.add(260 * time.Millisecond)
	res, err := r.b.Answer(context.Background(), p.q.ID, wire.AnswerRequest{Choice: 1})
	if err != nil || !res.OK {
		t.Fatalf("an answer at +360ms: %v %+v", err, res)
	}
	if d := p.decided(t); !d.Allow || d.Reason != "allowed by user" {
		t.Errorf("decision %+v", d)
	}
	if _, err := r.b.Answer(context.Background(), p.q.ID, wire.AnswerRequest{Choice: 1}); code(err) != "answered" {
		t.Errorf("a second answer: %v", err)
	}
	if _, err := r.b.Answer(context.Background(), "q_aaaaaaaaaaaaaaaaaaaaaaaaaa", wire.AnswerRequest{Choice: 1}); code(err) != "no_question" {
		t.Errorf("an unknown id: %v", err)
	}
	// The next question of the tab was asked long ago, but the tab's answer was just now: the floor counts from that.
	r.c.add(-10 * time.Second) // the question is created "before" the last answer
	p2 := r.ask(context.Background(), "shop", bash)
	r.c.add(10*time.Second + 100*time.Millisecond)
	if _, err := r.b.Answer(context.Background(), p2.q.ID, wire.AnswerRequest{Choice: 1}); code(err) != "too_soon" {
		t.Errorf("an answer 100ms after the tab's previous one: %v", err)
	}
	// Another tab's floor is its own.
	p3 := r.ask(context.Background(), "docs", bash)
	r.c.add(400 * time.Millisecond)
	if _, err := r.b.Answer(context.Background(), p3.q.ID, wire.AnswerRequest{Choice: 3}); err != nil {
		t.Errorf("another tab: %v", err)
	}
	if _, err := r.b.Answer(context.Background(), p2.q.ID, wire.AnswerRequest{Choice: 3}); err != nil {
		t.Errorf("after the floor: %v", err)
	}
	p2.decided(t)
	p3.decided(t)
}

// The answers mean what the terminal dialog's do; the page cannot choose a scope.
func TestDecisionTable(t *testing.T) {
	for _, tc := range []struct {
		name   string
		req    perm.Request
		answer wire.AnswerRequest
		want   perm.Decision
		code   string
	}{
		{"yes", bash, wire.AnswerRequest{Choice: 1}, perm.Decision{Allow: true, Reason: "allowed by user"}, ""},
		{"yes for the session", bash, wire.AnswerRequest{Choice: 2}, perm.Decision{Allow: true, Reason: "allowed by user for the session", Remember: perm.ScopeSession}, ""},
		{"no", bash, wire.AnswerRequest{Choice: 3}, perm.Decision{Reason: "denied by user"}, ""},
		{"no with a note", bash, wire.AnswerRequest{Choice: 3, Note: "use the existing test runner"}, perm.Decision{Reason: perm.DeclinedWith("use the existing test runner")}, ""},
		{"tool server for the project", perm.Request{Tool: perm.ToolMCPServer, Summary: "start x"}, wire.AnswerRequest{Choice: 2}, perm.Decision{Allow: true, Reason: "approved by user for this project", Remember: perm.ScopeProject}, ""},
		{"project files until they change", perm.Request{Tool: perm.ToolProjectTrust, Summary: "use them?"}, wire.AnswerRequest{Choice: 2}, perm.Decision{Allow: true, Reason: "trusted by user until the files change", Remember: perm.ScopeProject}, ""},
		{"the tests preset", perm.Request{Agent: "be-1", Tool: "bash", Command: "go test ./...", OffersTests: true}, wire.AnswerRequest{Choice: 4}, perm.Decision{Allow: true, Reason: "builds and tests allowed by user for the session", Remember: perm.ScopeSession, Preset: perm.PresetTests}, ""},
		{"the tests preset not offered", bash, wire.AnswerRequest{Choice: 4}, perm.Decision{}, "bad_choice"},
		{"no such choice", bash, wire.AnswerRequest{Choice: 5}, perm.Decision{}, "bad_choice"},
		{"a note with a yes", bash, wire.AnswerRequest{Choice: 1, Note: "x"}, perm.Decision{}, "bad_choice"},
		{"a note too long", bash, wire.AnswerRequest{Choice: 3, Note: strings.Repeat("é", MaxNote+1)}, perm.Decision{}, "bad_choice"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, Config{})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			p := r.ask(ctx, "shop", tc.req)
			r.c.add(time.Second)
			_, err := r.b.Answer(context.Background(), p.q.ID, tc.answer)
			if tc.code != "" {
				if code(err) != tc.code {
					t.Fatalf("error %v, want %s", err, tc.code)
				}
				cancel()
				p.decided(t)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := p.decided(t); got != tc.want {
				t.Errorf("decision %+v, want %+v", got, tc.want)
			}
		})
	}
}

// An interrupt refuses the questions of the agent the person talks to and keeps the workers'.
func TestInterruptRefusesMainQuestionsOnly(t *testing.T) {
	r := newRig(t, Config{})
	main := r.ask(context.Background(), "shop", perm.Request{Agent: "mgr", Tool: "bash", Command: "rm -rf build"})
	worker := r.ask(context.Background(), "shop", bash)
	other := r.ask(context.Background(), "docs", perm.Request{Agent: "mgr", Tool: "bash", Command: "make docs"})
	r.b.CancelAgent("shop", "mgr", ByCanceled)
	if d := main.decided(t); d.Allow || d.Reason != "no answer" {
		t.Errorf("the manager's question: %+v", d)
	}
	open := r.b.Open()
	if len(open) != 2 || open[0].Q.ID != worker.q.ID || open[1].Q.ID != other.q.ID {
		t.Fatalf("open after the interrupt: %+v", open)
	}
	a := r.answersCopy()
	if len(a) != 1 || a[0].QID != main.q.ID || a[0].By != ByCanceled || a[0].Choice != 3 {
		t.Errorf("answers %+v", a)
	}
	r.b.CancelTab("shop", ByClosed)
	worker.decided(t)
	if open := r.b.OpenFor("docs"); len(open) != 1 {
		t.Errorf("closing one tab touched another: %+v", open)
	}
	r.b.CancelTab("docs", ByClosed)
	other.decided(t)
}

// With no page connected for the grace period, the open questions are refused by "nobody"; a page that connects in time stops the clock.
func TestGraceRefusal(t *testing.T) {
	r := newRig(t, Config{Grace: time.Minute})
	p := r.ask(context.Background(), "shop", bash)
	r.b.Connected(1, r.c.now())
	r.c.add(2 * time.Minute)
	r.b.Connected(1, r.c.now())
	if len(r.b.Open()) != 1 {
		t.Fatal("refused while a page was connected")
	}
	r.b.Connected(0, r.c.now())
	r.c.add(59 * time.Second)
	r.b.Connected(0, r.c.now())
	if len(r.b.Open()) != 1 {
		t.Fatal("refused before the grace passed")
	}
	r.c.add(time.Second)
	r.b.Connected(0, r.c.now())
	if d := p.decided(t); d.Allow {
		t.Errorf("decision %+v", d)
	}
	if a := r.answersCopy(); len(a) != 1 || a[0].By != ByNobody {
		t.Errorf("answers %+v", a)
	}
}

// While the session starts, the questions about the project's files and its tool servers are refused without being asked.
func TestStartRefusesTrustAndMCPQuestions(t *testing.T) {
	r := newRig(t, Config{})
	starting := true
	p := r.b.Prompter("shop", "/proj", nil, func() bool { return starting })
	for _, tool := range []string{perm.ToolProjectTrust, perm.ToolMCPServer} {
		if d := p(context.Background(), perm.Request{Tool: tool, Summary: "?"}); d.Allow {
			t.Errorf("%s while starting: %+v", tool, d)
		}
	}
	select {
	case q := <-r.asked:
		t.Errorf("a question was asked while starting: %+v", q)
	default:
	}
	starting = false
	q := r.ask(context.Background(), "shop", perm.Request{Tool: perm.ToolMCPServer, Summary: "start the MCP server x"})
	if q.q.Kind != "mcp" || q.q.Agent != "mgr" {
		t.Errorf("a later tool server question: %+v", q.q)
	}
	r.b.CancelTab("shop", ByClosed)
	q.decided(t)
}

// A question whose context ends is refused and reported: by "timeout" for the ask timeout, by "canceled" for an interrupt.
func TestTimeoutAndCancelAreReported(t *testing.T) {
	r := newRig(t, Config{})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	p := r.ask(ctx, "shop", bash)
	if d := p.decided(t); d.Allow || d.Reason != "no answer" {
		t.Errorf("after the timeout: %+v", d)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	p2 := r.ask(ctx2, "shop", bash)
	cancel2()
	p2.decided(t)
	a := r.answersCopy()
	if len(a) != 2 || a[0].By != ByTimeout || a[1].By != ByCanceled {
		t.Errorf("answers %+v", a)
	}
	if len(r.b.Open()) != 0 {
		t.Error("a question is still open")
	}
}

// What the page is shown is cleaned: terminal controls are gone, secret-shaped values are masked, the directory is relative to the
// project, the engine's reason is the why, and the agent's lease is the scope.
func TestQuestionsAreShapedAndCleaned(t *testing.T) {
	r := newRig(t, Config{})
	key := "sk-ant-api03-" + strings.Repeat("A1b2C3d4", 6)
	p := r.ask(context.Background(), "shop", perm.Request{Agent: "fe-1", Tool: "bash", Cwd: "/proj/web",
		Command: "curl -H 'x-api-key: " + key + "' https://x.test \x1b[31mred\x1b[0m", Summary: "curl … [reaches the network]"})
	q := p.q
	if strings.Contains(q.Cmd, key) || strings.Contains(q.Cmd, "\x1b") {
		t.Errorf("cmd %q", q.Cmd)
	}
	if q.Cwd != "web/" || q.Why != "reaches the network" || q.Task != "T6" || q.Scope != "cwd web/ (inside fe-1's lease web/**)" || q.Kind != "command" || q.What != "this command" {
		t.Errorf("question %+v", q)
	}
	edit := r.ask(context.Background(), "shop", perm.Request{Agent: "main", Tool: "write", Paths: []string{"/proj/api/cart.go"}, Summary: "write api/cart.go"})
	if edit.q.Kind != "edit" || edit.q.Agent != "mgr" || edit.q.What != "this change" || edit.q.Cmd != "write api/cart.go" {
		t.Errorf("edit question %+v", edit.q)
	}
	r.b.CancelTab("shop", ByClosed)
	p.decided(t)
	edit.decided(t)
}

// A tab with more open questions than its bound makes the next one wait to be asked; none is dropped.
func TestQuestionsBeyondTheBoundWaitAndAreNotDropped(t *testing.T) {
	r := newRig(t, Config{MaxPerTab: 1})
	first := r.ask(context.Background(), "shop", bash)
	p := r.b.Prompter("shop", "/proj", nil, nil)
	d := make(chan perm.Decision, 1)
	go func() { d <- p(context.Background(), perm.Request{Agent: "be-1", Tool: "bash", Command: "make"}) }()
	select {
	case q := <-r.asked:
		t.Fatalf("a second question was asked over the bound: %+v", q)
	case <-time.After(100 * time.Millisecond):
	}
	r.c.add(time.Second)
	if _, err := r.b.Answer(context.Background(), first.q.ID, wire.AnswerRequest{Choice: 1}); err != nil {
		t.Fatal(err)
	}
	first.decided(t)
	var second wire.Question
	select {
	case second = <-r.asked:
	case <-time.After(10 * time.Second):
		t.Fatal("the waiting question was dropped")
	}
	r.c.add(time.Second)
	if _, err := r.b.Answer(context.Background(), second.ID, wire.AnswerRequest{Choice: 2}); err != nil {
		t.Fatal(err)
	}
	if got := <-d; !got.Allow || got.Remember != perm.ScopeSession {
		t.Errorf("decision %+v", got)
	}
}

// Closing the bridge refuses what is open and everything asked afterwards.
func TestCloseRefusesEverything(t *testing.T) {
	r := newRig(t, Config{})
	p := r.ask(context.Background(), "shop", bash)
	r.b.Close()
	if d := p.decided(t); d.Allow {
		t.Errorf("%+v", d)
	}
	pr := r.b.Prompter("shop", "/proj", nil, nil)
	if d := pr(context.Background(), bash); d.Allow {
		t.Errorf("after close: %+v", d)
	}
	if a := r.answersCopy(); len(a) != 1 || a[0].By != ByClosed {
		t.Errorf("answers %+v", a)
	}
}
