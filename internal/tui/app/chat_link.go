package app

import (
	"context"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// The chat program is the only writer to the terminal and the only owner of its input (see RunChat). The session does not know
// that: it talks to an agent.Sink and a perm.Prompter, from whichever goroutines its agents run on. A ChatLink is the pair of
// thin adapters that turn those calls into messages for the program and nothing else: they draw nothing, read nothing and decide
// nothing, so that Ctrl-C, an approval and a line typed ahead are all settled in one goroutine and cannot race.

type msgKind uint8

const (
	mText         msgKind = iota // a piece of an agent's words
	mReset                       // the response in progress is being retried
	mToolStart                   // a tool call begins
	mToolEnd                     // it ends
	mResponse                    // a model response is complete
	mNotice                      // a notice
	mQuestion                    // a permission question that waits for its answer
	mQuestionGone                // the question was cancelled by the turn that asked it
)

// chatMsg is what the adapters send. Which fields are set depends on the kind.
type chatMsg struct {
	kind  msgKind
	agent string
	text  string // the piece of words; the notice's message
	level string // the notice's level
	call  core.Block
	res   toolResult
	took  time.Duration
	hit   float64
	q     *question
}

// toolResult is what the program keeps of a tools.Result. It is copied when the tool ends, in the agent's goroutine, so that
// nothing the agent does with its result afterwards (it is still the agent's) can be seen, or raced with, by the program.
type toolResult struct {
	Text      string
	IsError   bool
	Failed    bool   // the call did not do what it was asked: a tool error, or a command with a status other than 0
	Outcome   string // "exit 1", "timed out": how a failed command ended
	Truncated bool
	Handle    string
	Meta      map[string]any
}

func snapshotResult(res *tools.Result) toolResult {
	if res == nil {
		return toolResult{}
	}
	r := toolResult{Text: res.Text, IsError: res.IsError, Failed: res.Failed(), Outcome: res.Outcome(), Truncated: res.Truncated, Handle: res.Handle}
	if len(res.Meta) > 0 {
		r.Meta = make(map[string]any, len(res.Meta))
		for k, v := range res.Meta {
			r.Meta[k] = v
		}
	}
	return r
}

// question is a permission question on its way to a person, and the way back: the answer is sent on ans, which has room for it
// (the program never waits for whoever asked).
type question struct {
	req perm.Request
	ans chan perm.Decision
}

// ChatLink connects a session to the chat program: its Sink and Prompter forward to the program, and the program reads what they
// forward. One link serves one program; the program closes it when it ends, after which the adapters drop what they are given
// and refuse every question, so that an agent that outlives the terminal never blocks on it.
type ChatLink struct {
	msgs chan chatMsg
	done chan struct{}
	once sync.Once
}

// linkBuffer is how many messages wait for the program before an agent that sends one waits too: the program takes them at the
// speed of a frame, and a model streams a few hundred pieces a second at most.
const linkBuffer = 4096

// NewChatLink makes a link.
func NewChatLink() *ChatLink {
	return &ChatLink{msgs: make(chan chatMsg, linkBuffer), done: make(chan struct{})}
}

// Close ends the link: nothing is forwarded after it and a question is refused at once. It is safe to call more than once.
func (l *ChatLink) Close() { l.once.Do(func() { close(l.done) }) }

// Done is closed when the link has ended.
func (l *ChatLink) Done() <-chan struct{} { return l.done }

// send hands a message to the program, waiting for room; it gives up (false) when the link has ended or ctx is done.
func (l *ChatLink) send(ctx context.Context, m chatMsg) bool {
	select {
	case l.msgs <- m:
		return true
	case <-l.done:
		return false
	case <-ctx.Done():
		return false
	}
}

// ChatSink is the agent.Sink of a chat on a terminal. All methods are safe for concurrent use.
type ChatSink struct{ l *ChatLink }

// Sink is the agent.Sink of the link.
func (l *ChatLink) Sink() *ChatSink { return &ChatSink{l: l} }

var (
	_ agent.Sink     = (*ChatSink)(nil)
	_ agent.Resetter = (*ChatSink)(nil)
)

func (s *ChatSink) send(m chatMsg) { s.l.send(context.Background(), m) }

// Text forwards a piece of an agent's words.
func (s *ChatSink) Text(a, d string) { s.send(chatMsg{kind: mText, agent: a, text: d}) }

// Thinking drops the model's reasoning: the status line says that the agent is thinking, and the words are the model's own.
func (s *ChatSink) Thinking(string, string) {}

// ToolStart forwards the beginning of a tool call.
func (s *ChatSink) ToolStart(a string, call core.Block) {
	s.send(chatMsg{kind: mToolStart, agent: a, call: call})
}

// ToolEnd forwards the end of a tool call with what it did.
func (s *ChatSink) ToolEnd(a string, call core.Block, res *tools.Result, took time.Duration) {
	s.send(chatMsg{kind: mToolEnd, agent: a, call: call, res: snapshotResult(res), took: took})
}

// Reset forwards that the response being shown starts over.
func (s *ChatSink) Reset(a string) { s.send(chatMsg{kind: mReset, agent: a}) }

// Response forwards the end of a model response. The figures of it (tokens, cost, what the cache did) reach the program through
// the log (state.State), as they reach every other reader of it, and are not repeated here.
func (s *ChatSink) Response(a string, _ *provider.Response, hit float64) {
	s.send(chatMsg{kind: mResponse, agent: a, hit: hit})
}

// Notice forwards a notice.
func (s *ChatSink) Notice(a, level, msg string) {
	s.send(chatMsg{kind: mNotice, agent: a, level: level, text: msg})
}

// noAnswer is what a question that nobody answered is: a refusal, for the reason the line prompter gives.
var noAnswer = perm.Decision{Allow: false, Reason: "no answer"}

// Prompter is the perm.Prompter of the link: it puts the question to the program and waits for the person's answer. It returns a
// refusal when ctx is cancelled (the turn that asked was cancelled, and takes no input: the program is told, and closes the
// question) and when the link has ended.
func (l *ChatLink) Prompter() perm.Prompter {
	return func(ctx context.Context, r perm.Request) perm.Decision {
		q := &question{req: r, ans: make(chan perm.Decision, 1)}
		if !l.send(ctx, chatMsg{kind: mQuestion, q: q}) {
			return noAnswer
		}
		select {
		case d := <-q.ans:
			return d
		case <-ctx.Done():
			// A question that was answered in the same instant is still the answer, but the turn is cancelled and the engine
			// discards it: the refusal is the same either way.
			l.send(context.WithoutCancel(ctx), chatMsg{kind: mQuestionGone, q: q})
			return noAnswer
		case <-l.done:
			return noAnswer
		}
	}
}
