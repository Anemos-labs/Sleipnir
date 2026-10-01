package app

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
	"unicode"

	"github.com/anemos-labs/sleipnir/internal/tui/input"
)

// The chat recording of the README (docs/media/chat.svg) is the chat program played from a transcript of a real session
// (docs/media/chat/transcript.jsonl, made by `sleipnir chat-record`). TestTheCommittedGalleryIsWhatTheCodeDraws says that the SVG is what
// the code draws from the transcript; these say that the transcript has the session the README tells, and that the program shows it.

const chatTranscriptFile = "docs/media/chat/transcript.jsonl"

func committedChat(t *testing.T) (*ChatTranscript, Recording) {
	t.Helper()
	tr, err := loadChatTranscript(repoFile(t, chatTranscriptFile))
	if err != nil {
		t.Fatalf("%v (scripts/record-demo.sh --new-chat makes it)", err)
	}
	recs, err := LoadGallery(repoFile(t, galleryManifest))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if r.isChat() {
			return tr, r
		}
	}
	t.Fatal("the gallery lists no recording of the chat")
	return nil, Recording{}
}

func TestTheCommittedChatTranscriptHasTheStoryTheRecordingTells(t *testing.T) {
	tr, rec := committedChat(t)
	if rec.sourcePath() != repoFile(t, chatTranscriptFile) {
		t.Errorf("the manifest takes the chat from %s", rec.sourcePath())
	}

	var typed strings.Builder
	var goals, tools, atQuestion []string
	var exits []float64
	var asks, answered, turnEnds int
	var commits, anomalies, mainReplies int
	var anomaly struct{ Expected, Actual int }
	var firstEnd, secondEnd *ChatTurnEnd
	for i, r := range tr.Records {
		switch r.Kind {
		case RecAttach:
			if i != 0 || r.Attach.Version == "" || r.Attach.Model == "" || r.Attach.Cwd != "/work/orders-api" || r.Attach.Session != "20260102-030405-5eed01" || r.Attach.Mode != "default" {
				t.Errorf("record %d: the session as the banner says it: %+v", i, r.Attach)
			}
		case RecKey:
			k, err := parseKey(r.Key)
			if err != nil {
				t.Fatal(err)
			}
			if asks == 1 && answered == 0 {
				atQuestion = append(atQuestion, r.Key)
			}
			switch {
			case k.IsRune('1', 0):
				if asks != 1 || answered != 0 {
					t.Errorf("record %d: the key 1 is pressed with %d questions asked and %d answered", i, asks, answered)
				}
				answered++
			case k.Is(input.Backspace, 0):
				kept := []rune(typed.String())
				typed.Reset()
				if len(kept) > 0 {
					typed.WriteString(string(kept[:len(kept)-1]))
				}
			case k.Kind == input.KindRune && k.Mod == 0: // a character: typed
				typed.WriteRune(k.R)
			}
		case RecTurn:
			goals = append(goals, r.Text)
			if typed.String() != r.Text {
				t.Errorf("record %d: the goal sent is %q, the keys typed %q", i, r.Text, typed.String())
			}
			typed.Reset()
		case RecToolEnd:
			tools = append(tools, r.Call.Name)
			if r.Call.Name == "bash" {
				code, _ := r.Result.Meta["exit_code"].(float64)
				exits = append(exits, code)
				if r.TookMS <= 0 {
					t.Errorf("record %d: a command that took no time", i)
				}
			}
			if r.Call.Name == "edit" && !strings.Contains(r.Result.Meta["diff"].(string), "-\toffset := page * size") {
				t.Errorf("record %d: the edit's diff is %v", i, r.Result.Meta["diff"])
			}
		case RecAsk:
			asks++
			if r.Ask.Request.Tool != "edit" || !r.Ask.Allow || r.Ask.Remember != "" {
				t.Errorf("record %d: the question and its answer: %+v", i, r.Ask)
			}
		case RecTurnEnd:
			turnEnds++
			if turnEnds == 1 {
				firstEnd = r.End
			} else {
				secondEnd = r.End
			}
		case RecEvent:
			var d struct {
				Kind     string `json:"kind"`
				Expected int    `json:"expected_read"`
				Actual   int    `json:"actual_read"`
			}
			_ = json.Unmarshal(r.Event.Data, &d)
			switch r.Event.Type {
			case "compact.commit":
				commits++
			case "cache.anomaly":
				anomalies++
				anomaly.Expected, anomaly.Actual = d.Expected, d.Actual
			case "model.response":
				mainReplies++
			}
		}
	}
	if len(goals) != 2 || !strings.Contains(goals[0], "pagination test") || !strings.Contains(goals[1], "negative size") {
		t.Errorf("the goals: %q", goals)
	}
	if want := "read bash read edit bash grep"; strings.Join(tools, " ") != want {
		t.Errorf("the tools that ran: %v, want %s", tools, want)
	}
	if len(exits) != 2 || exits[0] == 0 || exits[1] != 0 {
		t.Errorf("the tests must fail and then pass: exit statuses %v", exits)
	}
	if asks != 1 || answered != 1 {
		t.Errorf("%d questions and %d answers", asks, answered)
	}
	// the person's hand goes to y, as it does at a y/a/n prompt: it answers nothing and is taken back; then the number answers
	if got := strings.Join(atQuestion, " "); got != "y backspace 1" {
		t.Errorf("the keys pressed at the question: %s, want y backspace 1", got)
	}
	if commits != 1 {
		t.Errorf("%d compactions", commits)
	}
	if anomalies != 1 || anomaly.Expected < 2000 || anomaly.Actual != 0 {
		t.Errorf("the provider's cache break: %d of them, %+v: it expected a cached prefix and read none of it", anomalies, anomaly)
	}
	if mainReplies < 7 {
		t.Errorf("%d model responses", mainReplies)
	}
	if turnEnds != 2 || firstEnd == nil || firstEnd.Err != "" || firstEnd.Steps != 7 || secondEnd == nil || secondEnd.Err != ErrTranscriptCanceled {
		t.Errorf("the first turn ends in seven steps and the second is cancelled: %+v, %+v", firstEnd, secondEnd)
	}
	// about twenty to thirty seconds with the hold at the end
	if end := tr.Records[len(tr.Records)-1].T; end < 15_000 || end > 30_000 {
		t.Errorf("the session lasts %d ms", end)
	}

	// nothing of the machine that recorded it
	raw, err := os.ReadFile(repoFile(t, chatTranscriptFile))
	if err != nil {
		t.Fatal(err)
	}
	if m := regexp.MustCompile(`/(tmp|var|private|Users|home|root)/[^"\s\\]*`).Find(raw); m != nil {
		t.Errorf("the transcript names a path of the machine that made it: %s", m)
	}
}

func TestTheCommittedChatPlaysIntoTheScreensTheRecordingShows(t *testing.T) {
	tr, rec := committedChat(t)
	o, err := rec.ChatOptions()
	if err != nil {
		t.Fatal(err)
	}
	frames, err := PlayChat(tr, o)
	if err != nil {
		t.Fatal(err)
	}
	scr := screens(frames)
	// The steps are in the order the recording shows them, except for the ones marked alongside: the fold's record appears when the
	// fold's animation has run (a second and a half after the commit), the passing tests when the real go command came back, and the
	// break with the answer to the sixth request, which are three things going on at once, and which comes first follows how long
	// the machine that made the session took. Each must come after the step before them and before the step after.
	steps := []struct {
		what      string
		words     []string
		alongside bool
	}{
		{"the program starting", []string{"$ sleipnir chat", "Starting"}, false},
		{"the banner", []string{"◆ sleipnir", "/work/orders-api", "Type a goal"}, false},
		{"the goal being typed", []string{"│ ❯ the pagination"}, false},
		{"the goal sent", []string{"❯ the pagination test in ./orders is failing, fix it", "esc to interrupt"}, false},
		{"the stack bar before the first answer", []string{"prompt ", "G1"}, false},
		{"a tool line", []string{"● Read orders/list.go", "89 lines"}, false},
		{"the stack bar, cached, and the clock", []string{"% cached", "warm "}, false},
		{"the failing tests", []string{"● Bash go test ./orders/...", "✗", "exit 1", "--- FAIL: TestListFirstPage"}, false},
		{"the question", []string{"Edit a file", "orders/list.go", "(page - 1) * size", "1 2 3 answers"}, false},
		{"a letter at the question lands in the input box, and the question stays", []string{"Edit a file", "1 2 3 answers", "│ ❯ y "}, false},
		{"the edit as a diff with line numbers", []string{"● Edit orders/list.go", "+1 −1", "58 +"}, false},
		{"the thread folding", []string{"◆ compacting"}, false},
		{"the passing tests", []string{"● Bash go test ./orders/...", "ok  "}, true},
		{"the fold's record", []string{"◆ compacted", "→", "spine +1 resume"}, true},
		{"the cache break", []string{"⚠ cache break", "read 0 of"}, true},
		{"the answer as markdown", []string{"● Fixed", "• the offset is", "│ go"}, false},
		{"the end of the turn", []string{"── ", "7 steps", "saved ≈"}, false},
		{"the hit ratio with its marks", []string{"hit ratio per request", "7 requests"}, false},
		{"the second goal", []string{"❯ yes, add a test for page 0"}, false},
		{"the cancel", []string{"(cancelled)"}, false},
	}
	at, alongsideAt := -1, -1 // the last ordered step's screen, and the last of the steps that are alongside
	for _, s := range steps {
		i := firstShowing(t, scr, s.words...)
		t.Logf("%-42s first on screen %d", s.what, i)
		if i < at {
			t.Errorf("%s is first on screen %d, before the step before it (screen %d)", s.what, i, at)
		}
		if s.alongside {
			alongsideAt = max(alongsideAt, i)
			continue
		}
		if i < alongsideAt {
			t.Errorf("%s is first on screen %d, before steps that come before it (screen %d)", s.what, i, alongsideAt)
		}
		at = max(at, i, alongsideAt)
	}
	// the sparkline's marks are over the bars of the requests that had them: a compaction and a break
	if i := firstShowing(t, scr, "hit ratio per request", "7 requests"); i >= 0 {
		var marks string
		for _, line := range strings.Split(scr[i], "\n") {
			if strings.Contains(line, "◆") && strings.Contains(line, "⚠") && !strings.Contains(line, "cache break") && !strings.Contains(line, "compacted") {
				marks = line
			}
		}
		if marks == "" {
			t.Errorf("no line of the sparkline marks both a compaction (◆) and a break (⚠):\n%s", scr[i])
		}
	}
	// the footer (mode, model, session) is on every screen from the banner on, and the last screen is the prompt waiting
	for i := firstShowing(t, scr, "◆ sleipnir"); i < len(scr); i++ {
		if !strings.Contains(scr[i], "default · ") || !strings.Contains(scr[i], "mock-1 · 20260102-030405-5eed01") {
			t.Errorf("screen %d has no footer:\n%s", i, scr[i])
			break
		}
	}
	if last := scr[len(scr)-1]; strings.Contains(last, "esc to interrupt") || !strings.Contains(last, "Type a goal") {
		t.Errorf("the last screen is the prompt, waiting:\n%s", last)
	}
	// What the pictures must never be: a status line that begins with a blank where its spinner should be, a row wider than the screen,
	// a control character, a question that is on screen with the keys it takes not said.
	for i, s := range scr {
		for _, line := range strings.Split(s, "\n") {
			if strings.Contains(line, "esc to interrupt") && (line == "" || unicode.IsSpace([]rune(line)[0])) {
				t.Errorf("screen %d: a status line without its spinner: %q", i, line)
			}
			if n := len([]rune(line)); n > o.Cols {
				t.Errorf("screen %d: a row of %d cells on a screen of %d: %q", i, n, o.Cols, line)
			}
			for _, r := range line {
				if unicode.IsControl(r) {
					t.Errorf("screen %d: a control character in %q", i, line)
				}
			}
		}
		if strings.Contains(s, "Edit a file") && !strings.Contains(s, "1 2 3 answers") && !strings.Contains(s, "typing goes to the prompt") {
			t.Errorf("screen %d: a question, and nothing about the keys that answer it:\n%s", i, s)
		}
	}
}

// The recording is made again from the transcript by anyone who has the repository, and it is the same file.
func TestTheChatRecordingIsDrawnTheSameTwice(t *testing.T) {
	_, rec := committedChat(t)
	o, err := rec.ChatOptions()
	if err != nil {
		t.Fatal(err)
	}
	a, err := recordChat(rec.sourcePath(), o)
	if err != nil {
		t.Fatal(err)
	}
	b, err := recordChat(rec.sourcePath(), o)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("the same transcript and options must give the same bytes")
	}
	if len(a) > 1<<20 {
		t.Errorf("the recording is %d KB: a README should not carry more than a megabyte of picture", len(a)/1024)
	}
}
