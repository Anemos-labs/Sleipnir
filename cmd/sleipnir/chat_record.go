package main

// `sleipnir chat-record` makes the transcript that the README's chat recording is played from (docs/media/chat/transcript.jsonl; see
// internal/tui/app/chat_transcript.go for what a transcript is and docs/media/README.md for how the picture is made from it). It is
// not in the list of commands: it is a tool of scripts/record-demo.sh, which is the one place it is run from.
//
// It does what `sleipnir chat` does around the program and puts a recorder where the program would be. A real session is made, the way
// chat_tty.go makes one (the same options, the same host, the same subscription to the log), against the mock endpoint with a script for
// a model (internal/demo/chat.go), in a project of its own with a private home. A scripted person types a goal; at the permission
// question presses y (which does not answer it: it lands in the input box), deletes it and answers with the key 1; then types another
// goal and presses Ctrl-C. Everything the program would have been given is written down in the order it was given: the keys, what the
// session says through its sink and its prompter, the events of its log, how each turn ended.
//
// The session runs as fast as the machine does; the time of each record is designed afterwards (chat_pace.go), so that what is
// committed does not depend on how busy the machine was. The tools are the real ones, the go command included: the project's tests run
// and fail, and run and pass.
//
// Nothing here draws. A transcript is played into the real program on a virtual clock by PlayChat, and that is the picture.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/demo"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/tools"
	"github.com/anemos-labs/sleipnir/internal/tui/app"
	"github.com/anemos-labs/sleipnir/internal/tui/input"
)

func init() { extraCommands["chat-record"] = cmdChatRecord }

const (
	// chatRecordVersion is the version the recording says in its banner. The binary's own is whatever git describes, which a picture
	// should not depend on.
	chatRecordVersion = "0.1.0"
	// chatRecordCwd is the directory the recording says the project is in; the real one is a temporary directory.
	chatRecordCwd = "/work/orders-api"

	// The pauses of the person's script.
	personSettle = 700 * time.Millisecond  // the banner is read before the first key
	personRead   = 1300 * time.Millisecond // the question is read before the first key at it
	personNotice = 900 * time.Millisecond  // the letter has landed in the input box and answered nothing: it is seen, and deleted
	personDecide = 600 * time.Millisecond  // and the answer is pressed (a question takes no key until the keyboard has been quiet for a moment)
	personAfter  = 2200 * time.Millisecond // the answer is read before the next goal is typed
	personReact  = 150 * time.Millisecond  // from the words that decide it to the Ctrl-C

	// compactionGuard is how long the recorder waits for a compaction's model call, a hang guard and not a timing.
	compactionGuard = 2 * time.Minute
)

const chatRecordUsage = `usage: sleipnir chat-record --out FILE [--dir DIR]

Records one chat session for docs/media: a scripted person and a scripted model at a real session, and writes everything the chat
program would have been given, in order and with the times of a session at the speed of the script, as a transcript. It runs the
project's tests for real and so needs the go command. scripts/record-demo.sh --new-chat is how it is run.

flags:
`

func cmdChatRecord(ctx context.Context, args []string) error {
	fs := newFlagSet("chat-record", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		printHelp(os.Stderr, chatRecordUsage)
		printFlags(fs)
	}
	out := fs.String("out", "", "write the transcript to this file (required)")
	dir := fs.String("dir", "", "where to make the project and the session (default: a temporary directory, removed afterwards)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *out == "" {
		return usageError(fs, "chat-record: --out FILE is required and nothing else is taken")
	}
	root := *dir
	if root == "" {
		d, err := os.MkdirTemp("", "sleipnir-chat-record-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(d)
		root = d
	}
	var buf bytes.Buffer
	stats, err := recordChat(ctx, &buf, root)
	if err != nil {
		return fmt.Errorf("chat-record: %w", err)
	}
	if err := os.WriteFile(*out, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("chat-record: %w", err)
	}
	fmt.Fprintf(os.Stderr, "sleipnir chat-record: wrote %s (%d KB: %d records, %d events, %s of session)\n", *out, (buf.Len()+1023)/1024, stats.Records, stats.Events, stats.Length.Round(100*time.Millisecond))
	return nil
}

// chatRecordStats is what a recording came to.
type chatRecordStats struct {
	Records, Events int
	Length          time.Duration
}

// recordChat runs the scenario in a directory of its own (root, which is not removed) and writes its transcript to w, with the paths of
// this machine replaced by the ones the recording names.
func recordChat(ctx context.Context, w io.Writer, root string) (chatRecordStats, error) {
	var stats chatRecordStats
	if _, err := exec.LookPath("go"); err != nil {
		return stats, errors.New("the go command is not on the path: the recorded session runs the project's tests")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return stats, err
	}
	ws, home := filepath.Join(root, "orders-api"), filepath.Join(root, "home")
	for name, body := range demo.ChatFiles() {
		p := filepath.Join(ws, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return stats, err
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			return stats, err
		}
	}
	sc := demo.NewChatScenario()
	url, stop := sc.Start()
	defer stop()
	if err := os.MkdirAll(filepath.Join(home, ".sleipnir"), 0o755); err != nil {
		return stats, err
	}
	cfg, err := json.Marshal(sc.UserConfig(url))
	if err != nil {
		return stats, err
	}
	if err := os.WriteFile(filepath.Join(home, ".sleipnir", "config.json"), cfg, 0o600); err != nil {
		return stats, err
	}
	// The first run of the go command in a project builds what it needs. What the recording shows of it is its time on a warm cache
	// (and the time is held to bounds in any case), so it is run once before the session starts.
	warm := exec.CommandContext(ctx, "go", "test", "-count=1", "./orders/...")
	warm.Dir = ws
	_ = warm.Run() // the project's tests fail, which is the point of the scenario

	rec := &chatRecorder{t0: time.Now()}
	session.Version = chatRecordVersion
	so := chatOptions(session.Options{
		Cwd: ws, Root: ws, Home: home, Dir: filepath.Join(root, "session"), ID: demo.ChatSessionID,
		Allow:        expandAllow([]string{testsPreset}),
		TrustProject: true, Offline: true, NoMCP: true,
		Sink: rec, Prompter: rec.prompt, Now: rec.clock,
	})
	s, err := session.New(ctx, so)
	if err != nil {
		return stats, err
	}
	defer func() {
		s.SetEndReason(session.EndExit)
		s.Close()
	}()
	s.Log.SetClock(rec.clock)
	rec.sub, _ = s.Log.Subscribe(4096) // as the chat does, once the session is made
	rec.host = &sessionHost{s: s}
	rec.put(app.ChatRecord{Kind: app.RecAttach, Attach: &app.ChatAttachRecord{Version: chatRecordVersion, Model: s.Model.ID, Cwd: chatRecordCwd,
		Session: s.ID, Mode: string(s.Perm.Mode())}})

	if err := rec.person(ctx); err != nil {
		return stats, err
	}
	raw := rec.finish()
	if err := checkStory(raw); err != nil {
		return stats, fmt.Errorf("the session did not go as the script says: %w", err)
	}
	recs, err := paceChat(raw, defaultChatPace)
	if err != nil {
		return stats, err
	}
	// An event says when it happened by the time of its record, and not by the clock of the machine: the played program is given the
	// time of the record in any case, and a file that read two times for one moment would be a puzzle.
	for i := range recs {
		if e := recs[i].Event; e != nil {
			ev := *e
			ev.TS = demo.ChatEpoch.Add(time.Duration(recs[i].T) * time.Millisecond)
			recs[i].Event = &ev
		}
	}
	var buf bytes.Buffer
	tw, err := app.NewChatTranscriptWriter(&buf, demo.ChatEpoch, "a real session of the harness against the mock endpoint, with a script for a model and a script for the person, at the speed the pace in cmd/sleipnir/chat_pace.go says; made by `sleipnir chat-record`")
	if err != nil {
		return stats, err
	}
	for _, r := range recs {
		if err := tw.Write(r); err != nil {
			return stats, err
		}
		stats.Records++
		if r.Kind == app.RecEvent {
			stats.Events++
		}
	}
	stats.Length = time.Duration(recs[len(recs)-1].T) * time.Millisecond
	data, err := portable(buf.Bytes(), ws, root)
	if err != nil {
		return stats, err
	}
	_, err = w.Write(data)
	return stats, err
}

// checkStory says whether a recorded session has what the script is for: the tests failing and then passing, a question that the
// person answered with the key 1 after a letter had not, a compaction, a cache break, and a turn that the person cancelled. A harness
// that no longer compacts at the point the script was written for, say, would make a recording that tells another story, and that is
// found here.
func checkStory(raw []rawRec) error {
	var asks, answers, commits, anomalies, turnEnds, cancels int
	var atQuestion []string // the keys pressed while the question was up
	asked := false
	failed, passed := false, false
	for _, r := range raw {
		rec := r.rec
		switch rec.Kind {
		case app.RecAsk:
			asks++
			asked = true
		case app.RecKey:
			if asked {
				atQuestion = append(atQuestion, rec.Key)
			}
			if rec.Key == "1" { // the goals have no digit 1 in them
				answers++
				asked = false
			}
		case app.RecToolEnd:
			if code, ok := exitCode(rec.Result.Meta); ok && rec.Call.Name == "bash" {
				failed = failed || code != 0
				passed = passed || (code == 0 && failed)
			}
		case app.RecEvent:
			switch rec.Event.Type {
			case "compact.commit":
				commits++
			case "cache.anomaly":
				anomalies++
			}
		case app.RecTurnEnd:
			turnEnds++
			switch rec.End.Err {
			case "":
			case app.ErrTranscriptCanceled:
				cancels++
			default:
				return fmt.Errorf("a turn failed: %s", rec.End.Err)
			}
		}
	}
	switch {
	case !failed || !passed:
		return errors.New("the tests did not fail and then pass")
	case asks != 1 || answers != 1:
		return fmt.Errorf("%d questions and %d answers, want one of each", asks, answers)
	case strings.Join(atQuestion, " ") != "y backspace 1":
		return fmt.Errorf("the keys at the question were %q, want y (a letter, which does not answer), backspace and 1 (the number that does)", atQuestion)
	case commits != 1:
		return fmt.Errorf("%d compactions, want one", commits)
	case anomalies != 1:
		return fmt.Errorf("%d cache breaks, want one", anomalies)
	case turnEnds != 2 || cancels != 1:
		return fmt.Errorf("%d turns, %d of them cancelled, want two and one", turnEnds, cancels)
	}
	return nil
}

// exitCode is the exit status a command's result carries: a number, whatever it went through (an int in the harness, a float64 once
// it has been through JSON).
func exitCode(meta map[string]any) (int, bool) {
	switch v := meta["exit_code"].(type) {
	case int:
		return v, true
	case float64:
		return int(v), true
	}
	return 0, false
}

// portable replaces the paths of this machine in a transcript by the ones the recording names, and refuses a transcript that still
// names one: a committed file must not say where a machine keeps its temporary files.
func portable(data []byte, ws, root string) ([]byte, error) {
	for _, set := range []struct {
		path, as string
	}{{ws, chatRecordCwd}, {root, "/work/chat"}} {
		names := []string{set.path}
		if real, err := filepath.EvalSymlinks(set.path); err == nil && real != set.path {
			names = append(names, real)
		}
		data = replacePaths(data, names, set.as)
	}
	if m := machinePath.Find(data); m != nil {
		return nil, fmt.Errorf("the transcript still names a path of this machine (%q); not keeping it", m)
	}
	return data, nil
}

// replacePaths replaces each of names in data by as, the longest first. A temporary directory can be named two ways (macOS: /var/folders/...
// is /private/var/folders/...), and the shorter name is the end of the longer: replaced first it leaves "/private" and the new name, a path
// that exists on no machine and that the check for what is left of this machine's paths then refuses (the first macOS run of CI did).
func replacePaths(data []byte, names []string, as string) []byte {
	sorted := append([]string(nil), names...)
	sort.SliceStable(sorted, func(i, j int) bool { return len(sorted[i]) > len(sorted[j]) })
	for _, n := range sorted {
		data = bytes.ReplaceAll(data, []byte(n), []byte(as))
	}
	return data
}

// machinePath is a path under the places a machine keeps what is its own: temporary files, homes, user directories.
var machinePath = regexp.MustCompile(`/(tmp|var|private|Users|home|root)/[^"\s\\]*`)

// ---- the recorder ----

// chatRecorder writes down what the chat program would have been given. It is the sink and the prompter of the session, owns its log
// subscription, and is the person: it has no program to take its keys, so it types them into the transcript.
type chatRecorder struct {
	t0   time.Time
	host *sessionHost

	mu         sync.Mutex // everything below
	raw        []rawRec
	sub        <-chan events.Event
	planned    int // compactions that were planned,
	settled    int // and that have answered
	pieces     int // pieces of the answer to the goal that is being waited for
	cancelAt   int // pieces of it after which the person presses Ctrl-C (zero: never)
	cancelled  bool
	cancelTurn context.CancelFunc
}

var (
	_ agent.Sink     = (*chatRecorder)(nil)
	_ agent.Resetter = (*chatRecorder)(nil)
)

// clock is the session's time: the epoch of the recording and what has really passed since the session began. Nothing in the
// transcript is read from it (the records are paced afterwards, and a played event has the time of its record); it only keeps the
// dates of the real day out of what the session writes.
func (r *chatRecorder) clock() time.Time { return demo.ChatEpoch.Add(time.Since(r.t0)) }

// put writes a record. The events the log has emitted so far are written first: an event that was emitted before the call that is
// being recorded happened before it, and whoever plays the transcript in order must see them in that order. (The log hands its
// events to a channel at the moment they are emitted, so whatever is in it was emitted before this call.)
func (r *chatRecorder) put(rec app.ChatRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.drain()
	if r.cancelled && sinkRecord(rec.Kind) {
		return // after the Ctrl-C the person's program has nothing more to say about the turn
	}
	r.raw = append(r.raw, rawRec{rec: rec})
}

func sinkRecord(kind string) bool {
	switch kind {
	case app.RecText, app.RecReset, app.RecToolStart, app.RecToolEnd, app.RecResponse, app.RecNotice:
		return true
	}
	return false
}

// pause is the person's script waiting, or the world.
func (r *chatRecorder) pause(d time.Duration) {
	r.mu.Lock()
	r.raw = append(r.raw, rawRec{pause: d})
	r.mu.Unlock()
}

// drain writes the events that are waiting. The caller holds the lock.
func (r *chatRecorder) drain() {
	for r.sub != nil {
		select {
		case e, ok := <-r.sub:
			if !ok {
				r.sub = nil
				return
			}
			rec := app.ChatRecord{Kind: app.RecEvent, Event: &e}
			switch in := infoOf(&rec); {
			case e.Type == "compact.plan" && in.Decision == "start":
				r.planned++
			case e.Type == "compact.patch" && in.Stage == "ready", e.Type == "compact.reject":
				r.settled++
			}
			r.raw = append(r.raw, rawRec{rec: rec})
		default:
			return
		}
	}
}

// finish writes what the log still holds and returns the records.
func (r *chatRecorder) finish() []rawRec {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.drain()
	return r.raw
}

// awaitCompaction waits until the compaction that was planned has had its answer from the model, which it asks for beside the agent. The
// person reads a question for a moment, and that is when it answers; a recorded session in which it had not yet would commit it a
// request later, and tell another story.
func (r *chatRecorder) awaitCompaction(ctx context.Context) error {
	deadline := time.Now().Add(compactionGuard)
	for {
		r.mu.Lock()
		r.drain()
		pending := r.planned > r.settled
		r.mu.Unlock()
		if !pending {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("the compaction's model call did not answer")
		}
		select {
		case <-time.After(2 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// The sink of the session, as the chat's own (app.ChatSink) is: what it is told is what the program is told.

func (r *chatRecorder) Text(a, d string) {
	r.put(app.ChatRecord{Kind: app.RecText, Agent: a, Text: d})
	r.mu.Lock()
	r.pieces++
	press := r.cancelAt > 0 && r.pieces == r.cancelAt && !r.cancelled
	cancel := r.cancelTurn
	if press { // the person has seen enough: Ctrl-C
		r.cancelled = true
		r.raw = append(r.raw, rawRec{pause: personReact}, rawRec{rec: app.ChatRecord{Kind: app.RecKey, Key: app.FormatKey(input.RuneKey('c', input.Ctrl))}})
	}
	r.mu.Unlock()
	if press && cancel != nil {
		cancel()
	}
}

func (r *chatRecorder) Thinking(string, string) {}
func (r *chatRecorder) Reset(a string)          { r.put(app.ChatRecord{Kind: app.RecReset, Agent: a}) }

func (r *chatRecorder) ToolStart(a string, call core.Block) {
	r.put(app.ChatRecord{Kind: app.RecToolStart, Agent: a, Call: callOfBlock(call)})
}

func (r *chatRecorder) ToolEnd(a string, call core.Block, res *tools.Result, took time.Duration) {
	cr := &app.ChatResult{}
	if res != nil {
		cr = &app.ChatResult{Text: res.Text, IsError: res.IsError, Truncated: res.Truncated, Handle: res.Handle, Meta: res.Meta}
	}
	r.put(app.ChatRecord{Kind: app.RecToolEnd, Agent: a, Call: callOfBlock(call), Result: cr, TookMS: took.Milliseconds()})
}

func (r *chatRecorder) Response(a string, _ *provider.Response, hit float64) {
	r.put(app.ChatRecord{Kind: app.RecResponse, Agent: a, Hit: hit})
}

func (r *chatRecorder) Notice(a, level, msg string) {
	r.put(app.ChatRecord{Kind: app.RecNotice, Agent: a, Level: level, Text: msg})
}

func callOfBlock(b core.Block) *app.ChatCall {
	return &app.ChatCall{ID: b.ToolID, Name: b.ToolName, Input: b.Input}
}

// prompt is the person answering a question. They read it, and their hand does what it does at the y/a/n prompt of other tools: it
// presses y. A letter does not answer this question (that is what the recording is to show): it lands in the input box like any letter
// typed ahead, and the question stays. The person sees that, deletes the letter, and presses 1, which the dialog turns into the decision
// AnswerFor gives. Nothing here decides: what each key does is the program's, when the transcript is played, and the player checks it.
func (r *chatRecorder) prompt(ctx context.Context, req perm.Request) perm.Decision {
	one, habit := input.RuneKey('1', 0), input.RuneKey('y', 0)
	dec, ok := app.AnswerFor(req, one)
	if _, answers := app.AnswerFor(req, habit); !ok || answers {
		return perm.Decision{Reason: "no answer"} // the script is written for a dialog that takes 1 and does not take y
	}
	r.put(app.ChatRecord{Kind: app.RecAsk, Ask: &app.ChatAsk{Request: req, Allow: dec.Allow, Remember: dec.Remember}})
	if err := r.awaitCompaction(ctx); err != nil {
		return perm.Decision{Reason: "no answer"}
	}
	r.pause(personRead)
	r.put(app.ChatRecord{Kind: app.RecKey, Key: app.FormatKey(habit)})
	r.pause(personNotice)
	r.put(app.ChatRecord{Kind: app.RecKey, Key: app.FormatKey(input.SpecialKey(input.Backspace, 0))})
	r.pause(personDecide)
	r.put(app.ChatRecord{Kind: app.RecKey, Key: app.FormatKey(one)})
	return dec
}

// ---- the person ----

// typeLine types a line and presses enter, and runs the turn that the line starts: the session is given the goal as the program would
// give it, on a goroutine, and its end is waited for. cancelAfter is how many pieces of the answer the person lets by before they press
// Ctrl-C (zero: they wait for the end).
func (r *chatRecorder) typeLine(ctx context.Context, line string, cancelAfter int) error {
	for _, c := range line {
		r.put(app.ChatRecord{Kind: app.RecKey, Key: app.FormatKey(input.RuneKey(c, 0))})
	}
	r.put(app.ChatRecord{Kind: app.RecKey, Key: app.FormatKey(input.SpecialKey(input.Enter, 0))})
	r.put(app.ChatRecord{Kind: app.RecTurn, Text: line})
	tctx, cancel := context.WithCancel(ctx)
	defer cancel()
	r.mu.Lock()
	r.pieces, r.cancelAt, r.cancelled, r.cancelTurn = 0, cancelAfter, false, cancel
	r.mu.Unlock()
	done := make(chan app.TurnResult, 1)
	go func() { done <- r.host.Turn(tctx, line) }()
	var res app.TurnResult
	select {
	case res = <-done:
	case <-ctx.Done():
		cancel()
		<-done
		return ctx.Err()
	}
	end := &app.ChatTurnEnd{Steps: res.Steps, CostUSD: res.CostUSD, HitRatio: res.HitRatio, Message: res.Message}
	switch {
	case res.Err == nil:
	case errors.Is(res.Err, context.Canceled):
		end.Err = app.ErrTranscriptCanceled
	default:
		end.Err = res.Err.Error()
	}
	if cancelAfter > 0 && end.Err != app.ErrTranscriptCanceled {
		return errors.New("the answer ended before the person pressed Ctrl-C")
	}
	r.mu.Lock()
	r.cancelled = false
	r.mu.Unlock()
	r.put(app.ChatRecord{Kind: app.RecTurnEnd, End: end})
	return nil
}

// person is what the person does: reads the banner, asks for the fix and answers the question that comes (the prompter), reads the
// answer, asks for one more thing and presses Ctrl-C before the model has answered.
func (r *chatRecorder) person(ctx context.Context) error {
	r.pause(personSettle)
	if err := r.typeLine(ctx, demo.ChatGoal1, 0); err != nil {
		return err
	}
	r.pause(personAfter)
	return r.typeLine(ctx, demo.ChatGoal2, demo.ChatInterruptAfter)
}
