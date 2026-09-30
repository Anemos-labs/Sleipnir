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
}

// NewTextSink builds a sink writing answers to out and progress to log. Both are
// cleaned for a terminal: what the model says, what a tool ran and what came back
// can carry escape sequences (a page or a file that talks the model into printing
// an OSC 52 clipboard write, a title change, a cursor jump), and none of it may
// reach the terminal as commands.
func NewTextSink(out, log io.Writer, main string, verbose bool) *TextSink {
	return &TextSink{out: termSafe{out}, log: termSafe{log}, Main: main, Verbose: verbose, lastTool: map[string]string{}}
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
	fmt.Fprint(s.out, d)
	s.midLine = !strings.HasSuffix(d, "\n")
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

func (s *TextSink) Response(string, *provider.Response, float64) {}

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
func TerminalPrompter(in io.Reader, out io.Writer) perm.Prompter {
	var mu sync.Mutex
	rd, ok := in.(*bufio.Reader)
	if !ok {
		rd = bufio.NewReader(in)
	}
	return func(ctx context.Context, r perm.Request) perm.Decision {
		mu.Lock()
		defer mu.Unlock()
		who := r.Agent
		if who == "" {
			who = "agent"
		}
		if r.Tool == "mcp-server" {
			// Starting a project's tool server: it runs code or reaches a host the
			// repository chose. The answer that remembers is per exact entry.
			fmt.Fprintf(out, "\nSleipnir wants to %s\n  start it? [y]es this time / [p]roject: remember this exact entry / [n]o: ", r.Summary)
		} else {
			fmt.Fprintf(out, "\n%s wants to: %s\n  allow? [y]es once / [a]lways this session / [n]o: ", who, r.Summary)
		}
		line, err := readLine(ctx, rd)
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

func readLine(ctx context.Context, rd *bufio.Reader) (string, error) {
	type res struct {
		s   string
		err error
	}
	ch := make(chan res, 1)
	go func() {
		s, err := rd.ReadString('\n')
		ch <- res{s, err}
	}()
	select {
	case r := <-ch:
		return r.s, r.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
