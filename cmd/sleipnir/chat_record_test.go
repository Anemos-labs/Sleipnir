package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/demo"
	"github.com/reee344/sleipnir/internal/tui/app"
	"github.com/reee344/sleipnir/internal/tui/svg"
)

// ---- the story the script is for ----

func askRec() rawRec {
	return recRec(app.ChatRecord{Kind: app.RecAsk, Ask: &app.ChatAsk{Allow: true}})
}

func bashEndRec(id string, code int) rawRec {
	r := toolEndRec(id, `{}`, 1)
	r.rec.Result.Meta = map[string]any{"exit_code": code}
	return r
}

func turnEndRec(err string) rawRec {
	return recRec(app.ChatRecord{Kind: app.RecTurnEnd, End: &app.ChatTurnEnd{Err: err}})
}

func goodStory() []rawRec {
	return []rawRec{
		bashEndRec("c1", 1), askRec(), keyRec("y"), keyRec("backspace"), keyRec("1"), bashEndRec("c2", 0),
		evRec("compact.commit", `{}`), evRec("cache.anomaly", `{}`),
		turnEndRec(""), turnEndRec(app.ErrTranscriptCanceled),
	}
}

func TestCheckStoryKnowsTheStoryTheScriptIsFor(t *testing.T) {
	if err := checkStory(goodStory()); err != nil {
		t.Fatalf("the story the script tells: %v", err)
	}
	drop := func(raw []rawRec, f func(rawRec) bool) []rawRec {
		var out []rawRec
		for _, r := range raw {
			if !f(r) {
				out = append(out, r)
			}
		}
		return out
	}
	call := func(id string) func(rawRec) bool {
		return func(r rawRec) bool { return r.rec.Call != nil && r.rec.Call.ID == id }
	}
	event := func(typ string) func(rawRec) bool {
		return func(r rawRec) bool { return r.rec.Event != nil && r.rec.Event.Type == typ }
	}
	kind := func(k string) func(rawRec) bool { return func(r rawRec) bool { return r.rec.Kind == k } }
	key := func(k string) func(rawRec) bool {
		return func(r rawRec) bool { return r.rec.Kind == app.RecKey && r.rec.Key == k }
	}
	for name, tc := range map[string]struct {
		raw  []rawRec
		want string
	}{
		"tests that never pass": {drop(goodStory(), call("c2")), "fail and then pass"},
		"tests that never fail": {append([]rawRec{bashEndRec("c0", 0)}, drop(goodStory(), call("c1"))...), "fail and then pass"},
		"no question":           {drop(goodStory(), kind(app.RecAsk)), "0 questions and 1 answers"},
		"no answer":             {drop(goodStory(), kind(app.RecKey)), "1 questions and 0 answers"},
		"no letter first":       {drop(goodStory(), key("y")), `the keys at the question were ["backspace" "1"]`},
		"a letter left in":      {drop(goodStory(), key("backspace")), `the keys at the question were ["y" "1"]`},
		"no compaction":         {drop(goodStory(), event("compact.commit")), "0 compactions"},
		"two compactions":       {append(goodStory(), evRec("compact.commit", `{}`)), "2 compactions"},
		"no cache break":        {drop(goodStory(), event("cache.anomaly")), "0 cache breaks"},
		"nothing cancelled": {append(drop(goodStory(), func(r rawRec) bool { return r.rec.End != nil && r.rec.End.Err != "" }), turnEndRec("")),
			"0 of them cancelled"},
		"a turn that failed": {append(goodStory(), turnEndRec("the endpoint is down")), "a turn failed: the endpoint is down"},
	} {
		t.Run(name, func(t *testing.T) {
			err := checkStory(tc.raw)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %v, want one that says %q", err, tc.want)
			}
		})
	}
}

func TestExitCodeIsANumberWhateverItWentThrough(t *testing.T) {
	for _, meta := range []map[string]any{{"exit_code": 2}, {"exit_code": float64(2)}} {
		if n, ok := exitCode(meta); !ok || n != 2 {
			t.Errorf("%#v: %d %v", meta, n, ok)
		}
	}
	for _, meta := range []map[string]any{nil, {}, {"exit_code": "2"}} {
		if n, ok := exitCode(meta); ok {
			t.Errorf("%#v is not an exit status and was read as %d", meta, n)
		}
	}
}

// A committed transcript must not say where the machine that made it keeps its files.
func TestPortableNamesNoPathOfThisMachine(t *testing.T) {
	ws, root := "/tmp/sleipnir-chat-record-123/orders-api", "/tmp/sleipnir-chat-record-123"
	in := `{"text":"` + ws + `/orders/list.go and ` + root + `/session and ` + ws + `"}`
	got, err := portable([]byte(in), ws, root)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"text":"/work/orders-api/orders/list.go and /work/chat/session and /work/orders-api"}`; string(got) != want {
		t.Errorf("got %s\nwant %s", got, want)
	}
	for _, leak := range []string{
		`{"text":"/home/alice/go/pkg/mod/x"}`, `{"text":"/Users/bob/src"}`, `{"text":"/var/folders/zz/T/x"}`, `{"text":"/root/.cache/go-build"}`,
		`{"text":"/tmp/another-dir/x"}`, `{"text":"/private/var/x"}`,
	} {
		if _, err := portable([]byte(leak), ws, root); err == nil || !strings.Contains(err.Error(), "path of this machine") {
			t.Errorf("%s was kept (%v)", leak, err)
		}
	}
}

// ---- the recording of the chat, end to end ----

func frameText(f svg.Frame) string {
	var rows []string
	for _, row := range f.Rows {
		var b strings.Builder
		for _, c := range row {
			if c.Text == "" {
				b.WriteByte(' ')
			} else {
				b.WriteString(c.Text)
			}
		}
		rows = append(rows, strings.TrimRight(b.String(), " "))
	}
	return strings.Join(rows, "\n")
}

// The command that makes the transcript is run for real: a session of the harness against the mock endpoint with the scripted model, in a
// project of its own, with the go command running the project's tests. What it writes is a transcript that the reader accepts, that names
// nothing of this machine, and that the chat program plays into the moments the README's recording is made of. The session is the harness's
// (the order of what it says is its own), so a change to the harness that changes this story is found here and not in a picture.
func TestChatRecordMakesATranscriptThatTheChatProgramPlays(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end: it runs a session and the go command")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("the recorded session runs the project's tests with the go command")
	}
	// a hang guard, not a timing: the session itself waits on barriers
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	root := t.TempDir()
	var buf bytes.Buffer
	stats, err := recordChat(ctx, &buf, root)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Records < 100 || stats.Events < 30 {
		t.Errorf("a session of %d records and %d events", stats.Records, stats.Events)
	}
	if stats.Length < 15*time.Second || stats.Length > 40*time.Second {
		t.Errorf("a session of %v: the recording is meant to be about twenty to thirty seconds", stats.Length)
	}
	if bytes.Contains(buf.Bytes(), []byte(root)) || machinePath.Match(buf.Bytes()) {
		t.Errorf("the transcript names a path of the machine it was made on: %s", machinePath.Find(buf.Bytes()))
	}
	// the project's tests were run, for real, in the project the recording names
	if !bytes.Contains(buf.Bytes(), []byte("--- FAIL: TestListFirstPage")) || !bytes.Contains(buf.Bytes(), []byte(`ok  \texample.com/orders-api/orders`)) {
		t.Error("the transcript does not have the project's tests failing and then passing")
	}
	// the edit was made by the real edit tool, to a real file of the project
	if b, err := os.ReadFile(filepath.Join(root, "orders-api", "orders", "list.go")); err != nil || !bytes.Contains(b, []byte("offset := (page - 1) * size")) {
		t.Errorf("the project's file after the session: %v\n%s", err, b)
	}

	tr, err := app.ReadChatTranscript(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("the reader refuses what the command wrote: %v", err)
	}
	if !tr.Epoch.Equal(demo.ChatEpoch) {
		t.Errorf("the epoch is %v", tr.Epoch)
	}
	frames, err := app.PlayChat(tr, app.ChatPlayOptions{Cols: 100, Rows: 36, FPS: 5})
	if err != nil {
		t.Fatalf("the chat program cannot play what the command wrote: %v", err)
	}
	var seen strings.Builder
	letterAtQuestion := false
	for _, f := range frames {
		text := frameText(f)
		seen.WriteString(text)
		seen.WriteByte('\n')
		// the person's y lands in the input box, and the question is still up
		letterAtQuestion = letterAtQuestion || (strings.Contains(text, "1 2 3 answers") && strings.Contains(text, "│ ❯ y "))
	}
	for _, want := range []string{
		"◆ sleipnir", "/work/orders-api", // the banner
		"● Read orders/list.go", "● Bash go test ./orders/...", "exit 1", "--- FAIL: TestListFirstPage", // the tools and the failing tests
		"Edit a file", "1 2 3 answers", "● Edit orders/list.go", "+1 −1", // the question and the diff
		"◆ compacting", "◆ compacted", "⚠ cache break", // the compaction and the break
		"7 steps", "saved ≈", "(cancelled)", // the end of the first turn and the second turn's cancel
	} {
		if !strings.Contains(seen.String(), want) {
			t.Errorf("no screen of the recording shows %q", want)
		}
	}
	if !letterAtQuestion {
		t.Error("no screen of the recording shows a letter in the input box while the question is up: the person's y should land there and answer nothing")
	}
	last := frameText(frames[len(frames)-1])
	if !strings.Contains(last, "Type a goal") || strings.Contains(last, "esc to interrupt") {
		t.Errorf("the session does not end at the prompt, waiting:\n%s", last)
	}
}

// storyShape is what a transcript says happened, without how long anything took or what the machine reported of it: the keys and the
// words, the tools, the questions, the kinds of event (in order) and how each turn ended.
func storyShape(tr *app.ChatTranscript) []string {
	var out []string
	for _, r := range tr.Records {
		s := r.Kind
		switch r.Kind {
		case app.RecKey:
			s += " " + r.Key
		case app.RecText, app.RecTurn:
			s += " " + r.Text
		case app.RecNotice:
			s += " " + r.Level // its words say how many tokens, which follows from what the tools printed
		case app.RecToolStart, app.RecToolEnd:
			s += " " + r.Call.Name
		case app.RecAsk:
			s += " " + r.Ask.Request.Tool
		case app.RecEvent:
			in := infoOf(&r)
			s += " " + r.Event.Type + " " + in.Kind + in.Stage + in.Decision
		case app.RecTurnEnd:
			s += " " + r.End.Err
		}
		out = append(out, s)
	}
	return out
}

// The same script against the same harness is the same story, whatever the machine was doing: how long the go command took is real and
// differs from one recording to the next, and what happened, in what order, must not. (A recording made again on a busy machine must not
// tell another story, or the README would be a different picture every time.)
func TestChatRecordTellsTheSameStoryEveryTime(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end: it runs a session and the go command twice")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("the recorded session runs the project's tests with the go command")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute) // a hang guard, not a timing
	defer cancel()
	var shapes [2][]string
	for i := range shapes {
		var buf bytes.Buffer
		if _, err := recordChat(ctx, &buf, t.TempDir()); err != nil {
			t.Fatal(err)
		}
		tr, err := app.ReadChatTranscript(&buf)
		if err != nil {
			t.Fatal(err)
		}
		shapes[i] = storyShape(tr)
	}
	if !reflect.DeepEqual(shapes[0], shapes[1]) {
		for i := 0; i < min(len(shapes[0]), len(shapes[1])); i++ {
			if shapes[0][i] != shapes[1][i] {
				t.Fatalf("two recordings of the same session differ at record %d: %q and %q", i, shapes[0][i], shapes[1][i])
			}
		}
		t.Fatalf("two recordings of the same session have %d and %d records", len(shapes[0]), len(shapes[1]))
	}
}

// macOS names a temporary directory two ways, and the short one is the end of the long one: replacing it first left "/private/work/orders-api".
func TestPortableReplacesTheLongNameOfADirectoryBeforeTheShortOne(t *testing.T) {
	short, long := "/var/folders/36/xyz/T/TestX/001/orders-api", "/private/var/folders/36/xyz/T/TestX/001/orders-api"
	data := []byte(`{"cwd":"` + long + `","files":["` + short + `/a.go","` + long + `/b.go"]}`)
	for _, order := range [][]string{{short, long}, {long, short}} {
		got := string(replacePaths(data, order, "/work/orders-api"))
		want := `{"cwd":"/work/orders-api","files":["/work/orders-api/a.go","/work/orders-api/b.go"]}`
		if got != want {
			t.Errorf("names in the order %v: %s, want %s", order, got, want)
		}
		if machinePath.MatchString(got) {
			t.Errorf("a path of this machine is left in %s", got)
		}
	}
}
