package session

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/reee344/sleipnir/internal/agent"
	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/perm"
	"github.com/reee344/sleipnir/internal/provider"
	"github.com/reee344/sleipnir/internal/tools"
)

// TextSink renders progress for a human. The main agent's words go to out;
// everything else (tool activity, worker chatter, notices) goes to log, so
// `sleipnir run ... > answer.txt` captures just the answer.
type TextSink struct {
	out, log io.Writer
	// Main is the agent whose text is the answer ("" means every agent).
	Main    string
	Verbose bool

	mu       sync.Mutex
	midLine  bool // out has an unterminated line
	lastTool map[string]string

	// said and lead track, per agent, how the message in progress began. Some models open a turn
	// that is all tool calls with a newline or two, and each of those left an empty line on the answer
	// stream: thirty of them before the final answer of a small swarm. What a message has said while
	// it is only white space is held in lead, written without its blank lines when a word arrives
	// (said is then true) and dropped when the message ends without one.
	said map[string]bool
	lead map[string]string
}

// NewTextSink builds a sink writing answers to out and progress to log. Both are
// cleaned for a terminal: what the model says, what a tool ran and what came back
// can carry escape sequences (a page or a file that talks the model into printing
// an OSC 52 clipboard write, a title change, a cursor jump), and none of it may
// reach the terminal as commands.
func NewTextSink(out, log io.Writer, main string, verbose bool) *TextSink {
	return &TextSink{out: termSafe{out}, log: termSafe{log}, Main: main, Verbose: verbose,
		lastTool: map[string]string{}, said: map[string]bool{}, lead: map[string]string{}}
}

// termSafe writes what it is given after tools.SanitizeForTerminal has removed
// escape sequences and control characters (newline and tab stay).
type termSafe struct{ w io.Writer }

func (t termSafe) Write(p []byte) (int, error) {
	if _, err := io.WriteString(t.w, tools.SanitizeForTerminal(string(p))); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (s *TextSink) isMain(a string) bool { return s.Main == "" || a == s.Main }

func (s *TextSink) Text(a, d string) {
	if !s.isMain(a) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.said[a] {
		s.lead[a] += d
		if strings.TrimSpace(s.lead[a]) == "" {
			return
		}
		d = trimBlankLines(s.lead[a])
		delete(s.lead, a)
		s.said[a] = true
	}
	fmt.Fprint(s.out, d)
	s.midLine = !strings.HasSuffix(d, "\n")
}

// trimBlankLines drops the lines at the start of s that hold nothing but white space, and keeps the
// indentation of the first line that does.
func trimBlankLines(s string) string {
	for {
		i := strings.IndexByte(s, '\n')
		if i < 0 || strings.TrimSpace(s[:i]) != "" {
			return s
		}
		s = s[i+1:]
	}
}

func (s *TextSink) Thinking(string, string) {}

func (s *TextSink) endLine() {
	if s.midLine {
		fmt.Fprintln(s.out)
		s.midLine = false
	}
}

func (s *TextSink) ToolStart(a string, call core.Block) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastTool[a] = toolSummary(call)
}

func (s *TextSink) ToolEnd(a string, call core.Block, res *tools.Result, took time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.isMain(a) {
		s.endLine()
	}
	mark := "✓"
	if res != nil && res.IsError {
		mark = "✗"
	}
	who := ""
	if s.Main == "" || a != s.Main {
		who = "[" + a + "] "
	}
	fmt.Fprintf(s.log, "%s%s %s (%s)\n", who, mark, toolSummary(call), took.Round(time.Millisecond))
	if res != nil && res.IsError && s.Verbose {
		fmt.Fprintf(s.log, "    %s\n", firstLine(res.Text, 200))
	}
}

// Response ends the agent's message: the next one starts afresh, and white space that was all a message
// said is dropped.
func (s *TextSink) Response(a string, _ *provider.Response, _ float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.said, a)
	delete(s.lead, a)
}

func (s *TextSink) Notice(a, level, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if level == "info" && !s.Verbose {
		return
	}
	fmt.Fprintf(s.log, "[%s] %s: %s\n", a, level, msg)
}

func toolSummary(call core.Block) string {
	var in map[string]any
	_ = json.Unmarshal(call.Input, &in)
	for _, k := range []string{"command", "path", "file_path", "pattern", "url", "action", "title"} {
		if v, ok := in[k].(string); ok && v != "" {
			return call.ToolName + " " + firstLine(v, 100)
		}
	}
	return call.ToolName
}

func firstLine(s string, n int) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i] + " …"
	}
	if len(s) > n {
		s = s[:n] + "…"
	}
	return s
}

// JSONSink writes one JSON object per event (stream-json), for scripts and for
// other programs that drive Sleipnir.
type JSONSink struct {
	mu  sync.Mutex
	enc *json.Encoder
}

// NewJSONSink writes NDJSON to w.
func NewJSONSink(w io.Writer) *JSONSink { return &JSONSink{enc: json.NewEncoder(w)} }

func (s *JSONSink) emit(v map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.enc.Encode(v)
}

func (s *JSONSink) Text(a, d string) { s.emit(map[string]any{"type": "text", "agent": a, "text": d}) }
func (s *JSONSink) Thinking(a, d string) {
	s.emit(map[string]any{"type": "thinking", "agent": a, "text": d})
}
func (s *JSONSink) ToolStart(a string, c core.Block) {
	s.emit(map[string]any{"type": "tool_start", "agent": a, "id": c.ToolID, "name": c.ToolName, "input": json.RawMessage(c.Input)})
}
func (s *JSONSink) ToolEnd(a string, c core.Block, r *tools.Result, took time.Duration) {
	m := map[string]any{"type": "tool_end", "agent": a, "id": c.ToolID, "name": c.ToolName, "ms": took.Milliseconds()}
	if r != nil {
		m["error"] = r.IsError
		m["output"] = firstLine(r.Text, 4000)
	}
	s.emit(m)
}
func (s *JSONSink) Response(a string, r *provider.Response, hit float64) {
	s.emit(map[string]any{"type": "response", "agent": a, "usage": r.Usage, "hit_ratio": hit, "stop": r.Stop})
}
func (s *JSONSink) Notice(a, level, msg string) {
	s.emit(map[string]any{"type": "notice", "agent": a, "level": level, "message": msg})
}

var _ agent.Sink = (*TextSink)(nil)
var _ agent.Sink = (*JSONSink)(nil)

// TerminalPrompter asks the user on the terminal whether an action may proceed:
// y (once), a (always for this session), n (deny). Requests are serialised so
// parallel agents never interleave questions.
//
// It reads its answers from in itself, one line per question: right for a command that
// reads nothing else from in (run, mcp test). A command that reads in for something else
// as well (chat reads its goals from it) must own the input and use LinePrompter; two
// readers of one input take each other's lines.
func TerminalPrompter(in io.Reader, out io.Writer) perm.Prompter {
	return LinePrompter(directAnswers(in), out)
}

// LinePrompter is TerminalPrompter for a caller that owns the input: answer is how it gets the
// person's reply. It is called with the question's context and with show, which puts the
// question on the screen, and it must
//
//   - start taking for itself the lines that arrive, then call show, then wait. In that order: a
//     person (or a script that waits for the question and answers at once) types as soon as the
//     question is visible, and an answer that arrives before the answer function is ready for it
//     would be taken for a line typed ahead and lost. A line that was already waiting when it
//     began is typed ahead, for whoever reads the prompt; it is not an answer;
//   - return the first line that arrives after it began;
//   - return ctx's error, having taken nothing, when ctx ends first: the turn that asked was
//     cancelled, and what the person types next is for whoever reads next;
//   - return an error when the input has ended, which is "no answer" (a refusal). An answer
//     function that fails before it shows the question shows nothing.
//
// Questions are serialised as TerminalPrompter's are.
func LinePrompter(answer func(ctx context.Context, show func()) (string, error), out io.Writer) perm.Prompter {
	var mu sync.Mutex
	return func(ctx context.Context, r perm.Request) perm.Decision {
		mu.Lock()
		defer mu.Unlock()
		who := r.Agent
		if who == "" {
			who = "agent"
		}
		show := sync.OnceFunc(func() {
			if r.Tool == "mcp-server" {
				// Starting a project's tool server: it runs code or reaches a host the
				// repository chose. The answer that remembers is per exact entry.
				fmt.Fprintf(out, "\nSleipnir wants to %s\n  start it? [y]es this time / [p]roject: remember this exact entry / [n]o: ", r.Summary)
			} else {
				fmt.Fprintf(out, "\n%s wants to: %s\n  allow? [y]es once / [a]lways this session / [n]o: ", who, r.Summary)
			}
		})
		line, err := answer(ctx, show)
		if err != nil {
			return perm.Decision{Allow: false, Reason: "no answer"}
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
			return perm.Decision{Allow: true, Reason: "allowed by user"}
		case "a", "always":
			if r.Tool != "mcp-server" {
				return perm.Decision{Allow: true, Reason: "allowed by user for the session", Remember: perm.ScopeSession}
			}
		case "p", "project":
			if r.Tool == "mcp-server" {
				return perm.Decision{Allow: true, Reason: "approved by user for this project", Remember: perm.ScopeProject}
			}
		}
		return perm.Decision{Allow: false, Reason: "denied by user"}
	}
}

// directAnswers reads one line per call from in. The read runs in a goroutine, so that a
// question that is cancelled can stop waiting, and that goroutine is not abandoned: the read
// stays in progress until it returns, and the next call waits for that same read. What is
// typed after a cancelled question is therefore the answer to the next question (or stays
// unread, if there is none), and never the prize of a goroutine nobody waits for, which is
// what it used to be: an approval cancelled by Ctrl-C took the next line the person typed.
func directAnswers(in io.Reader) func(ctx context.Context, show func()) (string, error) {
	rd, ok := in.(*bufio.Reader)
	if !ok {
		rd = bufio.NewReader(in)
	}
	type res struct {
		s   string
		err error
	}
	var (
		mu      sync.Mutex
		pending chan res // the read in progress, if any
	)
	return func(ctx context.Context, show func()) (string, error) {
		show() // nothing else reads in: there is no typed-ahead line to tell from an answer
		mu.Lock()
		if pending == nil {
			ch := make(chan res, 1)
			pending = ch
			go func() {
				s, err := rd.ReadString('\n')
				ch <- res{s, err}
			}()
		}
		ch := pending
		mu.Unlock()
		select {
		case r := <-ch:
			mu.Lock()
			if pending == ch {
				pending = nil
			}
			mu.Unlock()
			return r.s, r.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}
