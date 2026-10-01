package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/tui/app"
)

// The time of a recorded chat is designed (chat_pace.go), so what it comes to is a function of the records and can be said exactly. Nothing
// here reads a clock or runs a session.

var testPace = chatPace{
	Attach: 100 * time.Millisecond, Key: 10 * time.Millisecond,
	FirstToken: 500 * time.Millisecond, PrefillPer: 100 * time.Microsecond, Decode: 40 * time.Millisecond, Compactor: time.Second,
	ToolMin: time.Millisecond, ToolMax: 2 * time.Second,
}

func evRec(typ, data string) rawRec {
	return rawRec{rec: app.ChatRecord{Kind: app.RecEvent, Event: &events.Event{Agent: "main", Type: typ, Data: json.RawMessage(data)}}}
}

func recRec(r app.ChatRecord) rawRec { return rawRec{rec: r} }

func pauseRec(d time.Duration) rawRec { return rawRec{pause: d} }

func textRec(s string) rawRec {
	return recRec(app.ChatRecord{Kind: app.RecText, Agent: "main", Text: s})
}

func keyRec(k string) rawRec { return recRec(app.ChatRecord{Kind: app.RecKey, Key: k}) }

func requestRec(req string) rawRec {
	return evRec("model.request", `{"kind":"main","req":"`+req+`"}`)
}

func responseRec(req string, inputTokens int) rawRec {
	return evRec("model.response", `{"req":"`+req+`","usage":{"input_tokens":`+itoa(inputTokens)+`}}`)
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func toolStartRec(id, input string) rawRec {
	return recRec(app.ChatRecord{Kind: app.RecToolStart, Agent: "main", Call: &app.ChatCall{ID: id, Name: "bash", Input: json.RawMessage(input)}})
}

func toolEndRec(id, input string, tookMS int64) rawRec {
	return recRec(app.ChatRecord{Kind: app.RecToolEnd, Agent: "main", Call: &app.ChatCall{ID: id, Name: "bash", Input: json.RawMessage(input)},
		Result: &app.ChatResult{Text: "ok"}, TookMS: tookMS})
}

func attachRec() rawRec {
	return recRec(app.ChatRecord{Kind: app.RecAttach, Attach: &app.ChatAttachRecord{Version: "0.1.0", Model: "m", Cwd: "/w", Session: "s", Mode: "default"}})
}

func times(recs []app.ChatRecord) []int64 {
	out := make([]int64, len(recs))
	for i, r := range recs {
		out[i] = r.T
	}
	return out
}

// A model answers after its first-token time and, for every token of the prompt that the provider did not have cached, a little more; then
// a piece of the words comes every decode time. The first answer of a session is slower than the one after it, as it is.
func TestPaceChatAnswersAsSlowlyAsThePromptWasUncached(t *testing.T) {
	raw := []rawRec{
		attachRec(),
		keyRec("a"),
		requestRec("main.1"),
		textRec("hello "), textRec("world"),
		responseRec("main.1", 10_000), // 10k tokens the cache did not hold: 1 s more
		recRec(app.ChatRecord{Kind: app.RecResponse, Agent: "main"}),
		keyRec("b"),
		requestRec("main.2"),
		textRec("again"),
		responseRec("main.2", 100), // 10 ms more
		recRec(app.ChatRecord{Kind: app.RecTurnEnd, End: &app.ChatTurnEnd{Steps: 2}}),
	}
	recs, err := paceChat(raw, testPace)
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{
		100,  // attach
		110,  // a key
		110,  // the request
		1610, // the first piece: the request + 500 ms + 1 s
		1650, // the next: a decode later
		1690, // the end of the answer: one more decode
		1690, // what the sink says of the end
		1700, // a key
		1700, // the request
		2210, // the first piece: 500 ms + 10 ms
		2250, // the end: one more decode
		2250, // the end of the turn
	}
	if got := times(recs); !reflect.DeepEqual(got, want) {
		t.Errorf("the times are %v, want %v", got, want)
	}
}

// The calls of an answer come after its words: each call and each 16 characters of its arguments is a chunk of its own.
func TestPaceChatTheCallsOfAnAnswerTakeTheTimeOfTheirChunks(t *testing.T) {
	args := `{"command":"go test ./orders/..."}` // 34 runes: three pieces
	raw := []rawRec{
		attachRec(),
		requestRec("main.1"),
		textRec("Let me run it."),
		responseRec("main.1", 0),
		toolStartRec("c1", args),
		toolEndRec("c1", args, 30),
	}
	recs, err := paceChat(raw, testPace)
	if err != nil {
		t.Fatal(err)
	}
	// request at 100; the words at 100+500 = 600; then the end of the answer: (1 + 1 call + 3 pieces) chunks of 40 ms = 200 ms
	if got, want := times(recs), []int64{100, 100, 600, 800, 800, 830}; !reflect.DeepEqual(got, want) {
		t.Errorf("the times are %v, want %v", got, want)
	}
	// the call took what the real tool took (30 ms), and that is what the line says
	if end := recs[len(recs)-1]; end.Kind != app.RecToolEnd || end.TookMS != 30 {
		t.Errorf("the tool's end: %+v", end)
	}
}

// A tool takes the time the real one took, held between bounds: no command takes no time, and a machine that stalled is not the session.
// What the line says is the time the transcript spans, not the one that was measured.
func TestPaceChatHoldsToolTimesBetweenBounds(t *testing.T) {
	for name, tc := range map[string]struct{ took, want int64 }{
		"no time":     {0, 1},
		"a moment":    {300, 300},
		"a long time": {600_000, 2000},
	} {
		t.Run(name, func(t *testing.T) {
			raw := []rawRec{attachRec(), toolStartRec("c1", `{}`), toolEndRec("c1", `{}`, tc.took)}
			recs, err := paceChat(raw, testPace)
			if err != nil {
				t.Fatal(err)
			}
			end := recs[len(recs)-1]
			if end.TookMS != tc.want || end.T != 100+tc.want {
				t.Errorf("a tool that took %d ms is shown taking %d ms and ends at %d ms, want %d and %d", tc.took, end.TookMS, end.T, tc.want, 100+tc.want)
			}
		})
	}
}

// The model call of a compaction runs beside the agent and answers when it answers: its events come after the plan by the time the call
// takes, not at the place in the session where the harness happened to write them down, and nothing else is moved by it.
func TestPaceChatTheCompactionsCallAnswersWhenItAnswers(t *testing.T) {
	raw := []rawRec{
		attachRec(),
		requestRec("main.1"),
		textRec("one"),
		responseRec("main.1", 0), // the end of the answer, 40 ms after the word
		evRec("compact.plan", `{"decision":"start"}`),
		evRec("model.request", `{"kind":"compactor","req":"c.1"}`),
		evRec("model.response", `{"req":"c.1","usage":{"input_tokens":5}}`),
		evRec("compact.patch", `{"stage":"ready"}`),
		keyRec("x"),
		pauseRec(5 * time.Second), // the person goes on for a while, past the compaction's answer
		keyRec("y"),
	}
	recs, err := paceChat(raw, testPace)
	if err != nil {
		t.Fatal(err)
	}
	var plan, ready int64 = -1, -1
	for i, r := range recs {
		if i > 0 && r.T < recs[i-1].T {
			t.Errorf("record %d is at %d ms, after one at %d ms: times never go back", i, r.T, recs[i-1].T)
		}
		if r.Kind != app.RecEvent {
			continue
		}
		switch r.Event.Type {
		case "compact.plan":
			plan = r.T
		case "compact.patch":
			ready = r.T
		}
	}
	if plan < 0 || ready != plan+1000 {
		t.Errorf("the plan is at %d ms and the patch at %d ms, want the patch a compactor-time (1000 ms) after the plan", plan, ready)
	}
	// the key before the pause comes before the answer of the compaction, the key after it comes after
	order := func(kind, key string) int {
		for i, r := range recs {
			if r.Kind == kind && (key == "" || r.Key == key) {
				return i
			}
		}
		return -1
	}
	patch := -1
	for i, r := range recs {
		if r.Kind == app.RecEvent && r.Event.Type == "compact.patch" {
			patch = i
		}
	}
	if !(order(app.RecKey, "x") < patch && patch < order(app.RecKey, "y")) {
		t.Errorf("the patch (record %d) is not between the keys x (%d) and y (%d)", patch, order(app.RecKey, "x"), order(app.RecKey, "y"))
	}
}

// A cancelled turn is a request that was never answered: it has the first piece of the words and no more, and the Ctrl-C comes after the
// pause of the person's script.
func TestPaceChatARequestThatIsNeverAnsweredStillHasItsFirstToken(t *testing.T) {
	raw := []rawRec{
		attachRec(),
		requestRec("main.1"),
		textRec("a"), textRec("b"),
		pauseRec(150 * time.Millisecond),
		keyRec("ctrl+c"),
		recRec(app.ChatRecord{Kind: app.RecTurnEnd, End: &app.ChatTurnEnd{Err: app.ErrTranscriptCanceled}}),
	}
	recs, err := paceChat(raw, testPace)
	if err != nil {
		t.Fatal(err)
	}
	// request 100; first word 600; second 640; the key 150 ms and one key time later
	if got, want := times(recs), []int64{100, 100, 600, 640, 800, 800}; !reflect.DeepEqual(got, want) {
		t.Errorf("the times are %v, want %v", got, want)
	}
}

func TestPaceChatIsAFunctionOfTheRecords(t *testing.T) {
	raw := []rawRec{
		attachRec(), pauseRec(700 * time.Millisecond), keyRec("a"), requestRec("main.1"), textRec("x"), responseRec("main.1", 777),
		toolStartRec("c1", `{"command":"ls"}`), toolEndRec("c1", `{"command":"ls"}`, 5),
	}
	a, err := paceChat(raw, testPace)
	if err != nil {
		t.Fatal(err)
	}
	b, err := paceChat(raw, testPace)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Error("the same records were given different times")
	}
	// and the records that went in are not changed: they are the session's
	if raw[len(raw)-1].rec.TookMS != 5 {
		t.Errorf("the pace changed the record it was given: %+v", raw[len(raw)-1].rec)
	}
}

// When a compaction starts, its goroutine and the main thread both emit events, and which gets into the log first is a race that a busy
// machine can make a wide one (the compaction's request can land after the main thread has begun to stream its answer). The
// transcript must not carry it: the same session twice is the same story, whichever way the race went.
func TestPaceChatDoesNotCarryTheRaceAtACompactionsStart(t *testing.T) {
	plan := evRec("compact.plan", `{"decision":"start"}`)
	compactorRequest := evRec("model.request", `{"kind":"compactor","req":"c.1"}`)
	patchRequest := evRec("compact.patch", `{"stage":"request"}`)
	mainRequest := requestRec("main.2")
	words, answered := textRec("y"), responseRec("main.2", 0)
	pace := func(after ...rawRec) []app.ChatRecord {
		raw := []rawRec{attachRec(), keyRec("a"), requestRec("main.1"), textRec("x"), responseRec("main.1", 0), evRec("turn.append", `{}`)}
		recs, err := paceChat(append(raw, after...), testPace)
		if err != nil {
			t.Fatal(err)
		}
		return recs
	}
	first := pace(plan, compactorRequest, patchRequest, mainRequest, words, answered)
	for name, after := range map[string][]rawRec{
		"the main thread first":      {plan, mainRequest, compactorRequest, patchRequest, words, answered},
		"the main thread in between": {plan, compactorRequest, mainRequest, patchRequest, words, answered},
		"the main thread last":       {plan, compactorRequest, patchRequest, mainRequest, words, answered},
		"the compaction late":        {plan, mainRequest, words, compactorRequest, patchRequest, answered},
		"the compaction very late":   {plan, mainRequest, words, answered, compactorRequest, patchRequest},
	} {
		if got := pace(after...); !reflect.DeepEqual(got, first) {
			t.Errorf("%s: the records are not the same as when the compaction's events come first", name)
		}
	}
	var got []string
	for _, r := range first {
		if r.Kind == app.RecEvent {
			in := infoOf(&r)
			got = append(got, strings.TrimSpace(r.Event.Type+" "+in.Kind+in.Stage))
		}
	}
	want := []string{"model.request main", "model.response", "turn.append", "compact.plan", "model.request compactor", "compact.patch request",
		"model.request main", "model.response"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the events are %q, want %q (what came before the plan stays before it)", got, want)
	}
}

func TestPaceChatRefusesARecordItCannotPlace(t *testing.T) {
	_, err := paceChat([]rawRec{recRec(app.ChatRecord{Kind: "dance"})}, testPace)
	if err == nil || !strings.Contains(err.Error(), `"dance"`) {
		t.Errorf("error %v, want one that names the kind", err)
	}
}
