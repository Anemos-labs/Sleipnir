package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/perm"
	"github.com/reee344/sleipnir/internal/tools"
	"github.com/reee344/sleipnir/internal/tui/input"
)

// A chat transcript is the recording of one chat session that the README's chat picture is made from (docs/media/chat). The swarm's
// pictures are functions of an event log; the chat is not, because most of what it shows never reaches the log: the words of the
// answer as they stream, what each tool printed, the questions, the keys. So the transcript holds everything the chat program was
// given while it ran, in the order it got it and with the time it arrived: the keys (what the person typed and pressed), what the
// session said through its sink and its prompter, the events of its log, and how each turn ended.
//
// It is made once, from a real session (the harness, its tools and its permission engine, against the mock endpoint, with a script for
// a model; `sleipnir chat-record`, cmd/sleipnir/chat_record.go) and played back as often as needed (PlayChat): the program, the renderer
// and the terminal emulator are the real ones and the clock is virtual, so the same transcript gives the same screens on every machine and
// every run, and a change to the program that has not been recorded again is a difference that a test finds.
//
// The file is JSON lines: a header, then one record per line. Times are milliseconds from the start of the session and never go
// backwards. An event is played with the time of its record, whatever its own ts says.

// The kinds of record.
const (
	RecHeader    = "header"
	RecAttach    = "attach"     // the session is made: what the banner and the footer say about it
	RecKey       = "key"        // a key the person pressed
	RecTurn      = "turn"       // the program sent a goal to the session (checked, not played: the program does it itself on enter)
	RecEvent     = "event"      // an event of the session's log
	RecText      = "text"       // a piece of the agent's words
	RecReset     = "reset"      // the response being shown starts over
	RecToolStart = "tool_start" // a tool call begins
	RecToolEnd   = "tool_end"   // it ends
	RecResponse  = "response"   // a model response is complete
	RecNotice    = "notice"     // a notice of the harness
	RecAsk       = "ask"        // a permission question
	RecTurnEnd   = "turn_end"   // the turn ended
)

// chatTranscriptFormat is the version of the file's layout.
const chatTranscriptFormat = 1

// ChatRecord is one line of a transcript. Which fields are set depends on Kind; a record carries nothing else.
type ChatRecord struct {
	// T is when it happened, in milliseconds from the start of the session.
	T    int64  `json:"t"`
	Kind string `json:"kind"`

	// header
	Format int    `json:"format,omitempty"`
	Epoch  string `json:"epoch,omitempty"` // the time of T = 0, RFC 3339: the clock of the playback starts here
	Note   string `json:"note,omitempty"`

	// attach
	Attach *ChatAttachRecord `json:"attach,omitempty"`

	// key: "a", "enter", "ctrl+c", "esc", "space" (see parseKey). turn: the goal the program sent (Text). event.
	Key   string        `json:"key,omitempty"`
	Event *events.Event `json:"event,omitempty"`

	// text, reset, tool_start, tool_end, response, notice: the agent that said it.
	Agent  string      `json:"agent,omitempty"`
	Text   string      `json:"text,omitempty"` // a piece of words, the notice's message, the goal
	Level  string      `json:"level,omitempty"`
	Call   *ChatCall   `json:"call,omitempty"`
	Result *ChatResult `json:"result,omitempty"`
	TookMS int64       `json:"took_ms,omitempty"`
	Hit    float64     `json:"hit,omitempty"`

	// ask: the question, and what the person's keys must answer to it.
	Ask *ChatAsk `json:"ask,omitempty"`

	// turn_end
	End *ChatTurnEnd `json:"end,omitempty"`
}

// ChatAttachRecord is what the banner and the footer say about the session.
type ChatAttachRecord struct {
	Version string `json:"version"`
	Model   string `json:"model"`
	Cwd     string `json:"cwd"`
	Session string `json:"session"`
	// Mode is the permission mode the session started in ("default" when empty).
	Mode string `json:"mode,omitempty"`
}

// ChatCall is a tool call as the agent made it.
type ChatCall struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input,omitempty"`
}

func (c ChatCall) block() core.Block {
	return core.Block{ToolID: c.ID, ToolName: c.Name, Input: c.Input}
}

// ChatResult is what a tool answered: the parts of a tools.Result that the chat reads.
type ChatResult struct {
	Text      string         `json:"text,omitempty"`
	IsError   bool           `json:"is_error,omitempty"`
	Truncated bool           `json:"truncated,omitempty"`
	Handle    string         `json:"handle,omitempty"`
	Meta      map[string]any `json:"meta,omitempty"`
}

func (r ChatResult) result() *tools.Result {
	return &tools.Result{Text: r.Text, IsError: r.IsError, Truncated: r.Truncated, Handle: r.Handle, Meta: r.Meta}
}

// ChatAsk is a permission question as the session put it, and the answer the person's keys gave it: what the transcript's keys must
// make the dialog decide. Only the verdict is kept; the words of its reason are the dialog's.
type ChatAsk struct {
	Request  perm.Request `json:"request"`
	Allow    bool         `json:"allow"`
	Remember perm.Scope   `json:"remember,omitempty"`
}

// ChatTurnEnd is how a turn ended. Err is "canceled" for a turn that was cancelled, else what went wrong (empty: it finished).
type ChatTurnEnd struct {
	Steps    int     `json:"steps,omitempty"`
	CostUSD  float64 `json:"cost_usd,omitempty"`
	HitRatio float64 `json:"hit_ratio,omitempty"`
	Err      string  `json:"err,omitempty"`
	Message  string  `json:"message,omitempty"`
}

// ErrTranscriptCanceled is ChatTurnEnd.Err of a cancelled turn.
const ErrTranscriptCanceled = "canceled"

// ChatTranscript is a parsed transcript.
type ChatTranscript struct {
	// Epoch is the time of T = 0. The program's clock, and the time of every event it is given, are the epoch plus the record's T.
	Epoch time.Time
	Note  string
	// Records are the records after the header, in order.
	Records []ChatRecord
}

// Limits on what a transcript may hold: a recording is read by the tests of every checkout, and a damaged one must be an error, not a
// memory problem.
const (
	maxTranscriptRecords = 200_000
	maxTranscriptBytes   = 64 << 20
)

// loadChatTranscript reads the transcript at path.
func loadChatTranscript(path string) (*ChatTranscript, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tr, err := ReadChatTranscript(io.LimitReader(f, maxTranscriptBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return tr, nil
}

// ReadChatTranscript parses a transcript and checks it: a header first, known kinds with the fields they need, times that do not go
// backwards. A line that is not a record, and a record with a field this reader does not know, are errors that name the line.
func ReadChatTranscript(r io.Reader) (*ChatTranscript, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if len(data) > maxTranscriptBytes {
		return nil, fmt.Errorf("the transcript is larger than %d bytes", maxTranscriptBytes)
	}
	tr := &ChatTranscript{}
	var last int64
	n := 0
	for ln, line := range bytes.Split(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var rec ChatRecord
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&rec); err != nil {
			return nil, fmt.Errorf("line %d: %w", ln+1, err)
		}
		if n++; n > maxTranscriptRecords {
			return nil, fmt.Errorf("more than %d records", maxTranscriptRecords)
		}
		if n == 1 {
			if rec.Kind != RecHeader {
				return nil, fmt.Errorf("line %d: a transcript starts with its header, not a %q", ln+1, rec.Kind)
			}
			if rec.Format != chatTranscriptFormat {
				return nil, fmt.Errorf("line %d: format %d, this reader takes %d", ln+1, rec.Format, chatTranscriptFormat)
			}
			ep, err := time.Parse(time.RFC3339Nano, rec.Epoch)
			if err != nil {
				return nil, fmt.Errorf("line %d: the epoch: %w", ln+1, err)
			}
			tr.Epoch, tr.Note = ep.UTC(), rec.Note
			continue
		}
		if err := rec.check(); err != nil {
			return nil, fmt.Errorf("line %d: %w", ln+1, err)
		}
		if rec.T < last {
			return nil, fmt.Errorf("line %d: a %s at %d ms comes after a record at %d ms", ln+1, rec.Kind, rec.T, last)
		}
		last = rec.T
		tr.Records = append(tr.Records, rec)
	}
	if n == 0 {
		return nil, errors.New("the transcript is empty")
	}
	return tr, nil
}

// check says what is wrong with a record, if anything.
func (r *ChatRecord) check() error {
	if r.T < 0 {
		return fmt.Errorf("a %s at %d ms", r.Kind, r.T)
	}
	need := func(ok bool, what string) error {
		if !ok {
			return fmt.Errorf("a %s record needs %s", r.Kind, what)
		}
		return nil
	}
	switch r.Kind {
	case RecAttach:
		return need(r.Attach != nil, "attach")
	case RecKey:
		if _, err := parseKey(r.Key); err != nil {
			return err
		}
	case RecTurn:
		return need(r.Text != "", "the goal in text")
	case RecEvent:
		return need(r.Event != nil, "event")
	case RecText:
		return need(r.Text != "", "text")
	case RecReset:
	case RecToolStart:
		return need(r.Call != nil, "call")
	case RecToolEnd:
		return need(r.Call != nil && r.Result != nil, "call and result")
	case RecResponse:
	case RecNotice:
		return need(r.Text != "", "text")
	case RecAsk:
		return need(r.Ask != nil, "ask")
	case RecTurnEnd:
		return need(r.End != nil, "end")
	case RecHeader:
		return errors.New("a second header")
	default:
		return fmt.Errorf("unknown kind %q", r.Kind)
	}
	return nil
}

// ChatTranscriptWriter writes a transcript, one record per line. Records must be given in order; the writer does not sort them. It
// is not safe for concurrent use: the recorder that owns it serialises its callers.
type ChatTranscriptWriter struct {
	w    io.Writer
	last int64
	err  error
}

// NewChatTranscriptWriter starts a transcript on w: the header says when T = 0 was and how the transcript was made.
func NewChatTranscriptWriter(w io.Writer, epoch time.Time, note string) (*ChatTranscriptWriter, error) {
	tw := &ChatTranscriptWriter{w: w}
	tw.put(ChatRecord{Kind: RecHeader, Format: chatTranscriptFormat, Epoch: epoch.UTC().Format(time.RFC3339Nano), Note: note})
	return tw, tw.err
}

// Write appends a record.
func (tw *ChatTranscriptWriter) Write(rec ChatRecord) error {
	if tw.err != nil {
		return tw.err
	}
	if err := rec.check(); err != nil {
		tw.err = err
		return err
	}
	if rec.T < tw.last {
		tw.err = fmt.Errorf("a %s at %d ms after a record at %d ms", rec.Kind, rec.T, tw.last)
		return tw.err
	}
	tw.last = rec.T
	tw.put(rec)
	return tw.err
}

func (tw *ChatTranscriptWriter) put(rec ChatRecord) {
	b, err := json.Marshal(rec)
	if err != nil {
		tw.err = err
		return
	}
	if _, err := tw.w.Write(append(b, '\n')); err != nil {
		tw.err = err
	}
}

// ---- keys ----

var specialByName = func() map[string]input.Special {
	m := map[string]input.Special{}
	for s := input.NoSpecial + 1; s <= input.F12; s++ {
		m[s.String()] = s
	}
	return m
}()

// FormatKey is a key as a transcript writes it: the modifiers (ctrl+, alt+, shift+) and then a character, or the name of a key
// (enter, esc, up, space).
func FormatKey(k input.Key) string {
	var b strings.Builder
	if k.Mod&input.Ctrl != 0 {
		b.WriteString("ctrl+")
	}
	if k.Mod&input.Alt != 0 {
		b.WriteString("alt+")
	}
	if k.Mod&input.Shift != 0 {
		b.WriteString("shift+")
	}
	switch {
	case k.Kind == input.KindSpecial:
		b.WriteString(k.Code.String())
	case k.R == ' ':
		b.WriteString("space")
	default:
		b.WriteRune(k.R)
	}
	return b.String()
}

// parseKey reads a key the way FormatKey writes it. A paste is not a key a transcript holds.
func parseKey(s string) (input.Key, error) {
	var mod input.Mod
	rest := s
	for {
		switch {
		case strings.HasPrefix(rest, "ctrl+") && utf8.RuneCountInString(rest) > len("ctrl+"):
			mod |= input.Ctrl
			rest = rest[len("ctrl+"):]
			continue
		case strings.HasPrefix(rest, "alt+") && utf8.RuneCountInString(rest) > len("alt+"):
			mod |= input.Alt
			rest = rest[len("alt+"):]
			continue
		case strings.HasPrefix(rest, "shift+") && utf8.RuneCountInString(rest) > len("shift+"):
			mod |= input.Shift
			rest = rest[len("shift+"):]
			continue
		}
		break
	}
	if sp, ok := specialByName[rest]; ok {
		return input.SpecialKey(sp, mod), nil
	}
	if rest == "space" {
		return input.RuneKey(' ', mod), nil
	}
	if r, n := utf8.DecodeRuneInString(rest); n == len(rest) && n > 0 && r != utf8.RuneError {
		return input.RuneKey(r, mod), nil
	}
	return input.Key{}, fmt.Errorf("%q is not a key (a character, or a name such as enter, esc or space, with ctrl+, alt+ or shift+ in front)", s)
}
