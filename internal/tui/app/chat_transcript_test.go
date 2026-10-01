package app

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/perm"
	"github.com/reee344/sleipnir/internal/tui/input"
)

// A chat transcript is read from a file that is committed and played by every checkout, so what reads it is strict (a record it does
// not understand is an error that names its line) and what writes it cannot make a file that the reader refuses.

func TestKeysRoundTripThroughTheirNames(t *testing.T) {
	for _, k := range []input.Key{
		input.RuneKey('a', 0), input.RuneKey('Z', 0), input.RuneKey('1', 0), input.RuneKey(' ', 0), input.RuneKey('+', 0), input.RuneKey('é', 0),
		input.RuneKey('世', 0), input.RuneKey('c', input.Ctrl), input.RuneKey('+', input.Ctrl), input.RuneKey('d', input.Ctrl|input.Alt),
		input.RuneKey(' ', input.Ctrl), input.SpecialKey(input.Enter, 0), input.SpecialKey(input.Esc, 0), input.SpecialKey(input.Tab, input.Shift),
		input.SpecialKey(input.Enter, input.Alt), input.SpecialKey(input.F5, 0), input.SpecialKey(input.PgDn, 0), input.SpecialKey(input.Up, input.Ctrl),
	} {
		name := FormatKey(k)
		got, err := parseKey(name)
		if err != nil || got != k {
			t.Errorf("%+v is written %q and read back as %+v (%v)", k, name, got, err)
		}
	}
	for _, bad := range []string{"", "ctrl+", "enterr", "ab", "ctrl+ab", "shift+shift+", "\xff", "paste"} {
		if k, err := parseKey(bad); err == nil {
			t.Errorf("%q is not a key and was read as %+v", bad, k)
		}
	}
}

func transcriptLines(recs ...string) string {
	return `{"t":0,"kind":"header","format":1,"epoch":"2026-01-02T03:04:05Z"}` + "\n" + strings.Join(recs, "\n") + "\n"
}

func TestAWrittenTranscriptIsReadBackAsItWas(t *testing.T) {
	var buf bytes.Buffer
	epoch := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	tw, err := NewChatTranscriptWriter(&buf, epoch, "a note")
	if err != nil {
		t.Fatal(err)
	}
	ev := events.Event{Seq: 7, Session: "s", Agent: "main", Type: "user.input", Data: json.RawMessage(`{"text":"hi"}`)}
	want := []ChatRecord{
		{T: 5, Kind: RecAttach, Attach: &ChatAttachRecord{Version: "0.1.0", Model: "m", Cwd: "/w", Session: "id", Mode: "plan"}},
		{T: 10, Kind: RecKey, Key: "ctrl+c"},
		{T: 10, Kind: RecTurn, Text: "do it"},
		{T: 11, Kind: RecEvent, Event: &ev},
		{T: 12, Kind: RecText, Agent: "main", Text: "a \"piece\"\nof words"},
		{T: 13, Kind: RecReset, Agent: "main"},
		{T: 14, Kind: RecToolStart, Agent: "main", Call: &ChatCall{ID: "c1", Name: "bash", Input: json.RawMessage(`{"command":"ls"}`)}},
		{T: 20, Kind: RecToolEnd, Agent: "main", Call: &ChatCall{ID: "c1", Name: "bash", Input: json.RawMessage(`{"command":"ls"}`)},
			Result: &ChatResult{Text: "a\tb", IsError: true, Truncated: true, Handle: "h1", Meta: map[string]any{"exit_code": float64(1)}}, TookMS: 6},
		{T: 21, Kind: RecResponse, Agent: "main", Hit: 0.5},
		{T: 22, Kind: RecNotice, Agent: "main", Level: "warn", Text: "slow down"},
		{T: 23, Kind: RecAsk, Ask: &ChatAsk{Request: perm.Request{Agent: "main", Tool: "bash", Command: "ls", Summary: "run ls"}, Allow: true, Remember: perm.ScopeSession}},
		{T: 30, Kind: RecTurnEnd, End: &ChatTurnEnd{Steps: 2, CostUSD: 0.5, HitRatio: 0.25, Err: ErrTranscriptCanceled, Message: "m"}},
	}
	for _, r := range want {
		if err := tw.Write(r); err != nil {
			t.Fatalf("%+v: %v", r, err)
		}
	}
	tr, err := ReadChatTranscript(&buf)
	if err != nil {
		t.Fatalf("%v\n%s", err, buf.String())
	}
	if !tr.Epoch.Equal(epoch) || tr.Note != "a note" {
		t.Errorf("the header came back as %v, %q", tr.Epoch, tr.Note)
	}
	if !reflect.DeepEqual(tr.Records, want) {
		t.Errorf("the records came back as\n%+v\nwant\n%+v", tr.Records, want)
	}
}

func TestATranscriptThatIsNotOneIsAnErrorThatNamesWhy(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"nothing":       {"", "empty"},
		"no header":     {`{"t":0,"kind":"key","key":"a"}` + "\n", "starts with its header"},
		"other format":  {`{"t":0,"kind":"header","format":2,"epoch":"2026-01-02T03:04:05Z"}` + "\n", "format 2"},
		"no epoch":      {`{"t":0,"kind":"header","format":1}` + "\n", "epoch"},
		"two headers":   {transcriptLines(`{"t":0,"kind":"header","format":1,"epoch":"2026-01-02T03:04:05Z"}`), "second header"},
		"not json":      {transcriptLines(`{"t":1,"kind":`), "line 2"},
		"unknown kind":  {transcriptLines(`{"t":1,"kind":"dance"}`), `unknown kind "dance"`},
		"unknown field": {transcriptLines(`{"t":1,"kind":"key","key":"a","speed":9}`), "unknown field"},
		"bad key":       {transcriptLines(`{"t":1,"kind":"key","key":"abc"}`), "not a key"},
		"time goes back": {transcriptLines(`{"t":5,"kind":"key","key":"a"}`, `{"t":4,"kind":"key","key":"b"}`),
			"comes after a record at 5 ms"},
		"negative time": {transcriptLines(`{"t":-1,"kind":"key","key":"a"}`), "-1 ms"},
		"no attach":     {transcriptLines(`{"t":1,"kind":"attach"}`), "needs attach"},
		"no event":      {transcriptLines(`{"t":1,"kind":"event"}`), "needs event"},
		"no text":       {transcriptLines(`{"t":1,"kind":"text"}`), "needs text"},
		"no call":       {transcriptLines(`{"t":1,"kind":"tool_start"}`), "needs call"},
		"no result":     {transcriptLines(`{"t":1,"kind":"tool_end","call":{"id":"c","name":"bash"}}`), "needs call and result"},
		"no ask":        {transcriptLines(`{"t":1,"kind":"ask"}`), "needs ask"},
		"no end":        {transcriptLines(`{"t":1,"kind":"turn_end"}`), "needs end"},
		"no goal":       {transcriptLines(`{"t":1,"kind":"turn"}`), "needs the goal"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ReadChatTranscript(strings.NewReader(tc.in))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %v, want one that says %q", err, tc.want)
			}
		})
	}
}

// What writes a transcript refuses what the reader would, at the record that is wrong and not when somebody tries to play the file.
func TestTheWriterRefusesWhatTheReaderWould(t *testing.T) {
	var buf bytes.Buffer
	tw, err := NewChatTranscriptWriter(&buf, time.Unix(0, 0), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := tw.Write(ChatRecord{T: 5, Kind: RecKey, Key: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Write(ChatRecord{T: 4, Kind: RecKey, Key: "b"}); err == nil {
		t.Error("a record that is earlier than the one before it was written")
	}
	if err := tw.Write(ChatRecord{T: 9, Kind: RecKey, Key: "c"}); err == nil {
		t.Error("a writer that has failed goes on writing")
	}
	var buf2 bytes.Buffer
	tw2, _ := NewChatTranscriptWriter(&buf2, time.Unix(0, 0), "")
	if err := tw2.Write(ChatRecord{T: 1, Kind: "dance"}); err == nil {
		t.Error("a record of a kind that does not exist was written")
	}
}

func TestAnswerForIsTheDialogsOwnTable(t *testing.T) {
	bash := perm.Request{Agent: "main", Tool: "bash", Command: "go test ./...", Summary: "run a command"}
	edit := perm.Request{Agent: "main", Tool: "edit", Paths: []string{"/w/a.go"}, Summary: "edit a.go"}
	server := perm.Request{Agent: "main", Tool: "mcp-server", Summary: "start a server"}
	for _, r := range []perm.Request{bash, edit} {
		for key, want := range map[rune]perm.Decision{
			'1': {Allow: true, Reason: "allowed by user"},
			'2': {Allow: true, Reason: "allowed by user for the session", Remember: perm.ScopeSession},
			'3': {Allow: false, Reason: "denied by user"},
		} {
			got, ok := AnswerFor(r, input.RuneKey(key, 0))
			if !ok || got != want {
				t.Errorf("%s: the key %c answers %+v (%v), want %+v", r.Tool, key, got, ok, want)
			}
		}
	}
	if got, ok := AnswerFor(server, input.RuneKey('2', 0)); !ok || !got.Allow || got.Remember != perm.ScopeProject {
		t.Errorf("for a tool server the second answer is kept for the project: %+v %v", got, ok)
	}
	// There is no letter that answers: not the y and n of the line prompt, not any letter, and not a number there is no option for.
	for _, k := range []input.Key{
		input.RuneKey('y', 0), input.RuneKey('Y', 0), input.RuneKey('n', 0), input.RuneKey('a', 0), input.RuneKey('o', 0), input.RuneKey('4', 0),
		input.RuneKey('0', 0), input.RuneKey('1', input.Ctrl), input.SpecialKey(input.Enter, 0), input.SpecialKey(input.Esc, 0), input.RuneKey(' ', 0),
	} {
		if d, ok := AnswerFor(bash, k); ok {
			t.Errorf("the key %s answered a question: %+v", FormatKey(k), d)
		}
	}
}
