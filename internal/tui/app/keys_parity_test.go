package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	"github.com/anemos-labs/sleipnir/internal/tui/input"
	"github.com/anemos-labs/sleipnir/internal/tui/state"
	"github.com/anemos-labs/sleipnir/internal/tui/state/statetest"
)

// Which keys do something in the terminal interface, found by pressing every candidate key in a set of neutral states of the chat
// program and of the watch and replay program, and compared with the file internal/parity/testdata/keys-terminal.json. The keys are
// handled by code (a switch in the chat model, the editor's table, the watch program's switch), not by a table, so a key added there
// is found only by pressing it: the page's counterpart is internal/web/uidev/test/keys-parity.test.mjs, and
// internal/parity/keys_test.go reads both files and the contract (internal/parity/contract/keys.json) and fails when one interface
// has a key the other lacks and the contract does not say why.
//
// A key "does something" in a state when, after it, the screen or the cursor is not what it was, the session was told something
// (a goal, a command, a mode, an answer, a cancellation), the program wrote bytes (a redraw), or the program ended. A character
// that only goes into the prompt does not count: typing is not a binding, so for a character the prompt's own row and the cursor
// are left out of the comparison, and the character is taken out again. The watch and replay program is compared with a twin that
// is pressed nothing: the frame the key made, and the frame of a tick after it.
//
// Nothing waits for time: the rig of the chat program (chat_rig_test.go) moves the clock and the keys. A program that a key did
// nothing to is the program it was, so the next key is pressed in it; one that a key did something to is thrown away. (A state that
// a sequence of keys primed has a program for every key, and a replay is played on only after the keys; see
// TestKeysSweepDoesNotDependOnTheOrder.) Rewrite the file after a deliberate change to the keys:
//
//	go test ./internal/tui/app -run TestKeysParityTerminal -update
//
// and read the diff of internal/parity/testdata/keys-terminal.json.

// ---- the candidate keys ----

const keySymbols = "!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~"

// keyNamed are the named keys, each with the modifiers it is pressed with on purpose (shift+tab; the three newlines; shift+left and
// shift+right, a minute of a replay). An Esc with ctrl held or a Tab with alt belongs to the system, and ctrl+enter is the byte of
// ctrl+j in a terminal. The page's test builds the same list, and keys_test.go fails when the two lists differ.
var keyNamed = []struct {
	name string
	mods []string
}{
	{"tab", []string{"", "shift+"}}, {"esc", []string{""}}, {"enter", []string{"", "shift+", "alt+"}}, {"backspace", []string{""}},
	{"delete", []string{""}}, {"insert", []string{""}}, {"up", []string{""}}, {"down", []string{""}}, {"left", []string{"", "shift+"}},
	{"right", []string{"", "shift+"}}, {"home", []string{""}}, {"end", []string{""}}, {"pgup", []string{""}}, {"pgdn", []string{""}},
}

// keyCandidates is every key that is pressed, spelled lowercase with the modifiers in the order ctrl+ alt+ shift+.
func keyCandidates() []string {
	var out []string
	const lower = "abcdefghijklmnopqrstuvwxyz"
	for _, c := range lower {
		out = append(out, string(c))
	}
	for _, c := range "0123456789" {
		out = append(out, string(c))
	}
	out = append(out, "space")
	for _, c := range keySymbols {
		out = append(out, string(c))
	}
	for _, c := range lower {
		out = append(out, "shift+"+string(c))
	}
	for _, c := range lower {
		out = append(out, "ctrl+"+string(c))
	}
	for _, c := range []string{"space", "\\", "]", "^", "_"} {
		out = append(out, "ctrl+"+c)
	}
	for _, c := range lower {
		out = append(out, "alt+"+string(c))
	}
	for _, c := range "0123456789" {
		out = append(out, "alt+"+string(c))
	}
	for _, c := range keySymbols {
		out = append(out, "alt+"+string(c))
	}
	for i := 1; i <= 12; i++ {
		out = append(out, fmt.Sprintf("f%d", i))
	}
	for _, n := range keyNamed {
		for _, m := range n.mods {
			out = append(out, m+n.name)
		}
	}
	return out
}

// candidatesDigest identifies a list of candidates whatever its order.
func candidatesDigest(list []string) string {
	s := append([]string(nil), list...)
	sort.Strings(s)
	sum := sha256.Sum256([]byte(strings.Join(s, "\n")))
	return hex.EncodeToString(sum[:])[:16]
}

// typesText reports whether a candidate is a character that goes into the prompt when nothing else takes it.
func typesText(spec string) bool {
	if spec == "space" || len([]rune(spec)) == 1 {
		return true
	}
	return len(spec) == len("shift+a") && strings.HasPrefix(spec, "shift+")
}

// nonText reports whether a candidate is not a character: the keys of a state in which typing would only edit a query.
func nonText(spec string) bool { return !typesText(spec) }

// splitMods takes the modifiers off the front of a candidate.
func splitMods(spec string) (rest string, ctrl, alt, shift bool) {
	rest = spec
	for {
		switch {
		case strings.HasPrefix(rest, "ctrl+") && len(rest) > len("ctrl+"):
			ctrl, rest = true, rest[len("ctrl+"):]
		case strings.HasPrefix(rest, "alt+") && len(rest) > len("alt+"):
			alt, rest = true, rest[len("alt+"):]
		case strings.HasPrefix(rest, "shift+") && len(rest) > len("shift+"):
			shift, rest = true, rest[len("shift+"):]
		default:
			return
		}
	}
}

// keyBytes is what a terminal sends for a candidate: the bytes the decoder turns into the key the program gets.
func keyBytes(spec string) []byte {
	rest, ctrl, alt, shift := splitMods(spec)
	csi := func(final string) string { // an arrow, Home or End, with shift as xterm reports it
		if shift {
			return "\x1b[1;2" + final
		}
		return "\x1b[" + final
	}
	tilde := map[string]string{"insert": "2", "delete": "3", "pgup": "5", "pgdn": "6"}
	fkeys := map[string]string{"f1": "\x1bOP", "f2": "\x1bOQ", "f3": "\x1bOR", "f4": "\x1bOS", "f5": "\x1b[15~", "f6": "\x1b[17~", "f7": "\x1b[18~", "f8": "\x1b[19~",
		"f9": "\x1b[20~", "f10": "\x1b[21~", "f11": "\x1b[23~", "f12": "\x1b[24~"}
	switch {
	case rest == "tab" && shift:
		return []byte("\x1b[Z")
	case rest == "tab":
		return []byte("\t")
	case rest == "esc":
		return []byte("\x1b")
	case rest == "enter" && shift:
		return []byte("\x1b[13;2u")
	case rest == "enter" && alt:
		return []byte("\x1b\r")
	case rest == "enter":
		return []byte("\r")
	case rest == "backspace":
		return []byte("\x7f")
	case rest == "up", rest == "down", rest == "right", rest == "left":
		return []byte(csi(map[string]string{"up": "A", "down": "B", "right": "C", "left": "D"}[rest]))
	case rest == "home":
		return []byte(csi("H"))
	case rest == "end":
		return []byte(csi("F"))
	case tilde[rest] != "":
		return []byte("\x1b[" + tilde[rest] + "~")
	case fkeys[rest] != "":
		return []byte(fkeys[rest])
	case ctrl && rest == "space":
		return []byte{0}
	case ctrl && len(rest) == 1 && rest[0] >= 'a' && rest[0] <= 'z':
		return []byte{rest[0] - 'a' + 1}
	case ctrl && len(rest) == 1: // \ ] ^ _
		return []byte{rest[0] - 0x40}
	}
	text := rest
	if rest == "space" {
		text = " "
	}
	if shift && len(rest) == 1 {
		text = strings.ToUpper(rest)
	}
	if alt {
		return append([]byte{0x1b}, text...)
	}
	return []byte(text)
}

// canonKey spells a key the way the candidates are spelled.
func canonKey(k input.Key) string {
	mod, r := k.Mod, k.R
	if k.Kind == input.KindRune && unicode.IsUpper(r) {
		mod |= input.Shift
		r = unicode.ToLower(r)
	}
	var b strings.Builder
	for _, m := range []struct {
		bit  input.Mod
		name string
	}{{input.Ctrl, "ctrl+"}, {input.Alt, "alt+"}, {input.Shift, "shift+"}} {
		if mod&m.bit != 0 {
			b.WriteString(m.name)
		}
	}
	switch {
	case k.Kind == input.KindSpecial:
		b.WriteString(k.Code.String())
	case r == ' ':
		b.WriteString("space")
	default:
		b.WriteRune(r)
	}
	return b.String()
}

// keyAliases are candidates a terminal sends as another key's bytes, spelled as the decoder spells that key: LF is ctrl+j.
var keyAliases = map[string]string{"ctrl+j": "ctrl+enter"}

// decodeCandidate is the key the program gets when a terminal sends the candidate's bytes. ok is false when a terminal cannot tell
// the candidate from another key (ctrl+h is backspace, ctrl+i is tab, ctrl+m is enter): such a candidate is not pressed, and the file
// says which key it is.
func decodeCandidate(spec string) (k input.Key, other string, ok bool) {
	var d input.Decoder
	keys := append(d.Feed(keyBytes(spec)), d.Flush()...)
	if len(keys) != 1 {
		return input.Key{}, fmt.Sprintf("%d keys", len(keys)), false
	}
	got := canonKey(keys[0])
	if got == spec || got == keyAliases[spec] {
		return keys[0], "", true
	}
	return input.Key{}, got, false
}

// ---- the probes: one program in a state, and the keys pressed in it ----

// keyRig is a program in a state, and a way to press a key in it. A program that a key did nothing to is the program it was, so the
// next key is pressed in it; one that a key did something to is thrown away.
type keyRig interface {
	// try presses k and reports whether anything came of it; typed leaves the prompt's own row and the cursor out of the comparison.
	try(k input.Key, typed bool) (changed, quit bool)
	// restore takes back a character that try typed and found nothing else in, and reports whether the program is as it was.
	restore() bool
	// stop ends the program and waits for it.
	stop()
}

// maskPrompt takes the text out of the last row that is a prompt (a box row that starts with the prompt mark), where a typed character
// goes.
func maskPrompt(screen string) string {
	rows := strings.Split(screen, "\n")
	for i := len(rows) - 1; i >= 0; i-- {
		if strings.HasPrefix(rows[i], "│ ❯ ") {
			rows[i] = "│ ❯ "
			break
		}
	}
	return strings.Join(rows, "\n")
}

// chatProbe is the chat program on the rig.
type chatProbe struct {
	r *chatRig
	// extra is what the session saw of the key that the screen does not show at once (the turn was cancelled, a question was answered),
	// read in the same instant as the screen.
	extra func() string
	typed string // how the program looked before a character was typed
}

func (p *chatProbe) look(typed bool) string {
	s := p.r.screen()
	if typed {
		return maskPrompt(s)
	}
	x, y, vis := p.r.bridge.v.Cursor()
	p.r.host.mu.Lock()
	host := fmt.Sprint(p.r.host.mode, len(p.r.host.goals), p.r.host.commands)
	p.r.host.mu.Unlock()
	extra := ""
	if p.extra != nil {
		extra = p.extra()
	}
	return fmt.Sprintf("%s\n--\ncursor %d,%d,%v alt %v host %s extra %s bytes %d", s, x, y, vis, p.r.altScreen(), host, extra, len(p.r.written()))
}

// barrier returns when the program has handled the key it was given, or reports that it ended.
func (p *chatProbe) barrier() (ended bool) {
	select {
	case p.r.keys <- ping:
		return false
	case e := <-p.r.done:
		p.r.done <- e
		return true
	case <-time.After(time.Minute):
		p.r.t.Fatalf("the program took neither the key nor an end (a hang guard); the screen:\n%s", p.r.screen())
		return false
	}
}

func (p *chatProbe) try(k input.Key, typed bool) (changed, quit bool) {
	before := p.look(typed)
	if typed {
		p.typed = p.look(false)
	}
	p.r.send(k)
	if p.barrier() {
		return true, true
	}
	return p.look(typed) != before, false
}

func (p *chatProbe) restore() bool {
	p.r.send(input.SpecialKey(input.Backspace, 0))
	return !p.barrier() && p.look(false) == p.typed
}

func (p *chatProbe) stop() {
	p.r.cancel()
	select {
	case e := <-p.r.done:
		p.r.done <- e
	case <-time.After(time.Minute):
		p.r.t.Error("the program did not end (a hang guard)")
	}
}

// watchPair is the watch and replay program twice, in step: one is pressed the key, the other a key that does nothing, and each draws
// the same ticks. They show the same frames as long as the key did nothing, and the ticks are what shows a key that only changes how
// the screen moves (the animation off, a replay's speed).
type watchPair struct{ keyed, plain *rig }

// frame presses k and returns the frame it made; quit says the program ended instead.
func frameAfter(r *rig, k input.Key) (frame string, quit bool) {
	r.keys <- k
	select {
	case f := <-r.scr.frames:
		return f, false
	case <-r.done:
		return "", true
	case <-time.After(time.Minute):
		r.t.Fatal("the program drew no frame and did not end (a hang guard)")
		return "", false
	}
}

func (p *watchPair) try(k input.Key, _ bool) (changed, quit bool) {
	f, quit := frameAfter(p.keyed, k)
	g, _ := frameAfter(p.plain, ping)
	if quit || f != g || len(p.keyed.scr.resizes) != len(p.plain.scr.resizes) {
		return true, quit
	}
	return p.keyed.step(0) != p.plain.step(0), false // a tick that moves the animation and not the recording: no key sees how long the others took
}

func (p *watchPair) restore() bool { return true }

func (p *watchPair) stop() { p.keyed.cancel(); p.plain.cancel() }

// ---- the states ----

// keyState is a program in a state that the candidates are pressed in.
type keyState struct {
	name, about string
	// keys limits the candidates (nil: all): a state whose widget takes only some keys, or in which a character edits a query, would
	// otherwise show only that the widget closes.
	keys func(spec string) bool
	// open brings up a program in the state, for the lifetime of t.
	open func(t *testing.T) keyRig
	// fresh says that every key is pressed in a program of its own: the state is one a sequence of keys has primed (the last key was a
	// yank, a character was just undone), and any other key between would undo that.
	fresh bool
	// late finds, among the candidates that changed nothing at once, those that change how the program moves on (nil: none can).
	late func(t *testing.T, quiet []keyCand) []string
}

// chatState is a state of the chat program: opts makes the options of each program (a history is written to, so each has its own),
// and setup brings the rig to the state and returns what the session saw (or nil).
func chatState(name, about string, opts func() rigOpts, keys func(string) bool, setup func(r *chatRig) func() string) keyState {
	return keyState{name: name, about: about, keys: keys, open: func(t *testing.T) keyRig {
		r := startChat(t, opts())
		p := &chatProbe{r: r}
		if setup != nil {
			p.extra = setup(r)
		}
		return p
	}}
}

// primed marks a state that the keys before it have set up for the next one.
func primed(st keyState) keyState {
	st.fresh = true
	return st
}

// frozenSource is a session under way whose log is read: the screen of `sleipnir watch` for a session that does not move.
type frozenSource struct {
	st  *state.State
	now time.Time
}

func (f *frozenSource) State() *state.State   { return f.st }
func (f *frozenSource) Now() time.Time        { return f.now }
func (f *frozenSource) Advance(time.Duration) {}
func (f *frozenSource) Err() error            { return nil }
func (f *frozenSource) Close() error          { return nil }

// watchState is a state of the watch and replay program: the keys of setup are pressed first.
func watchState(name, about string, view View, replay bool, setup ...input.Key) keyState {
	open := func(t *testing.T) *rig {
		var src Source
		if replay {
			src = demoReplay(t, 1)
		} else {
			st := state.New()
			st.ApplyAll(statetest.DemoEvents())
			src = &frozenSource{st: st, now: st.Snapshot().Clock}
		}
		r := startRig(t, src, view, 80, 24)
		r.frame()
		for _, k := range setup {
			r.press(k)
		}
		r.step(0) // the first tick of a program only starts its clock
		if replay {
			r.step(maxStep) // and a replay is then a quarter of a second into its recording, where it can be sought both ways
		}
		return r
	}
	st := keyState{name: name, about: about, open: func(t *testing.T) keyRig { return &watchPair{keyed: open(t), plain: open(t)} }}
	st.late = func(t *testing.T, quiet []keyCand) []string { return lateEffects(t, st, quiet) }
	return st
}

// lateEffects finds the keys whose effect is not on the frame they make but on the frames that follow as the recording plays (the
// animation off, which only shows as things arrive). Time passes between keys, and a key would see how long the others took, so the
// keys are pressed together, in a program of their own, before the recording is played on; when the program then differs from one
// that was pressed nothing, the keys are halved until one is left.
func lateEffects(t *testing.T, st keyState, quiet []keyCand) []string {
	if len(quiet) == 0 {
		return nil
	}
	differs := false
	t.Run(fmt.Sprintf("later %d keys from %s", len(quiet), quiet[0].spec), func(t *testing.T) {
		p := st.open(t).(*watchPair)
		defer p.stop()
		for _, c := range quiet {
			frameAfter(p.keyed, c.key)
			frameAfter(p.plain, ping)
		}
		for range 2 {
			if p.keyed.step(maxStep) != p.plain.step(maxStep) {
				differs = true
				return
			}
		}
	})
	switch {
	case !differs:
		return nil
	case len(quiet) == 1:
		return []string{quiet[0].spec}
	}
	mid := len(quiet) / 2
	return append(lateEffects(t, st, quiet[:mid]), lateEffects(t, st, quiet[mid:])...)
}

// holdTurn is a turn that runs until it is cancelled, and tells whether it was.
func holdTurn(r *chatRig) func() string {
	var mu sync.Mutex
	var ctx context.Context
	started := make(chan struct{})
	r.host.turn = func(c context.Context, goal string) TurnResult {
		mu.Lock()
		ctx = c
		mu.Unlock()
		close(started)
		<-c.Done()
		return TurnResult{Err: c.Err()}
	}
	r.submit("go")
	select {
	case <-started:
	case <-time.After(time.Minute):
		r.t.Fatal("the turn did not start (a hang guard)")
	}
	r.shows("esc to interrupt")
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		return fmt.Sprint("cancelled ", ctx.Err() != nil)
	}
}

// keyStates are the neutral states. A person is in one of them when a key arrives.
func keyStates() []keyState {
	all := func(string) bool { return true }
	letters := func(s string) bool { return strings.HasPrefix(s, "ctrl+") || strings.HasPrefix(s, "alt+") }
	stats := func(s string) bool { return strings.Contains(" up down pgup pgdn esc ctrl+t ", " "+s+" ") }
	search := func(s string) bool { return strings.Contains(" ctrl+r ctrl+g ctrl+c esc enter backspace ", " "+s+" ") }
	rw := func(f func()) func() string { f(); return nil }
	opts := func(o rigOpts) func() rigOpts { return func() rigOpts { return o } }
	team := opts(rigOpts{cols: 110, rows: 34, team: 8, mainAgent: "mgr"})
	withHistory := func() rigOpts {
		h := input.NewHistory()
		_ = h.Add("fix the build")
		_ = h.Add("run the tests")
		return rigOpts{history: h}
	}
	// idle is a session with a project to complete paths from: the rig's own attach has none.
	idle := func() rigOpts {
		o := withHistory()
		o.noAttach = true
		return o
	}
	plain := opts(rigOpts{})
	return []keyState{
		chatState("chat-idle", "the prompt is empty; nothing runs; earlier lines are in the history; the project has a file", idle, all, func(r *chatRig) func() string {
			dir := r.t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o600); err != nil {
				r.t.Fatal(err)
			}
			r.attach <- ChatAttach{Host: r.host, Events: r.log, Root: dir, Info: ChatInfo{Version: "0.1.0", Model: "mock/mock-1", Cwd: dir, SessionID: "20260102-030405-abcdef"},
				Commands: []input.Command{{Name: "help", Description: "this text"}, {Name: "cost", Description: "tokens, cost and cache hit ratio so far"},
					{Name: "compact", Args: "[focus]", Description: "fold the older thread now"}, {Name: "exit", Description: "quit"}}}
			r.shows("◆ sleipnir", "default")
			return nil
		}),
		chatState("chat-text", "the prompt holds text, the cursor in the middle", plain, all, func(r *chatRig) func() string {
			return rw(func() {
				r.typeText("hello world")
				for range 5 {
					r.special(input.Left)
				}
			})
		}),
		chatState("chat-menu", "the prompt holds a slash and the command menu is open", plain, nonText, func(r *chatRig) func() string {
			return rw(func() { r.typeText("/"); r.shows("/compact") })
		}),
		chatState("chat-search", "the history search is open", withHistory, search, func(r *chatRig) func() string {
			return rw(func() { r.ctrl('r') })
		}),
		primed(chatState("chat-yanked", "text was killed, typed over and killed again, then the newest kill was yanked", plain, letters, func(r *chatRig) func() string {
			return rw(func() {
				r.typeText("one two three")
				r.ctrl('w')
				r.typeText("x")
				r.ctrl('w')
				r.ctrl('y')
			})
		})),
		primed(chatState("chat-undone", "typed text was undone", plain, letters, func(r *chatRig) func() string {
			return rw(func() { r.typeText("hello"); r.ctrl('z') })
		})),
		chatState("chat-running", "a turn is running", plain, all, holdTurn),
		chatState("chat-question", "a permission question is open and takes its answer", plain, all, func(r *chatRig) func() string {
			ans := r.ask(bashRequest("rm x"))
			r.shows("1. Yes")
			return func() string { return fmt.Sprint("answered ", len(ans)) }
		}),
		chatState("chat-cockpit", "the team's cockpit is open", team, all, func(r *chatRig) func() string {
			return rw(func() {
				b := statetest.NewBuilder()
				log := sessionLog(b, 2, 0)
				log = append(log, b.Spawn("be-1", "backend", "T1", "mgr"), b.Spawn("fe-1", "frontend", "T2", "mgr"))
				r.at(b.Now().Add(2 * time.Second))
				r.emit(log...)
				r.ctrl('g')
				r.shows("ctrl+g back to the chat")
			})
		}),
		chatState("chat-stats", "the statistics page is open and longer than the screen", opts(rigOpts{cols: 100, rows: 10}), stats, func(r *chatRig) func() string {
			return rw(func() {
				r.emit(sessionLog(statetest.NewBuilder(), 4, 0)...)
				r.ctrl('t')
				r.shows("estimated prompt distribution")
			})
		}),
		watchState("watch-live", "sleipnir watch follows a session under way, on the cache view, an agent chosen", ViewCache, false, input.RuneKey('j', 0)),
		watchState("watch-replay", "sleipnir replay is part of the way through a recording, on the cockpit", ViewCockpit, true),
		watchState("watch-replay-cache", "sleipnir replay is part of the way through a recording, on the cache view, an agent chosen", ViewCache, true, input.RuneKey('j', 0)),
	}
}

// ---- the sweep ----

// keyCand is a candidate and the key a terminal sends for it.
type keyCand struct {
	spec string
	key  input.Key
}

// pressAll presses the candidates in the program of a state, and returns the ones that did something. Candidates that are not
// characters come first, in one program for as long as nothing happens; then the characters, each taken back out again.
func pressAll(t *testing.T, st keyState, cands []keyCand) []string {
	var hits []string
	var run func(typed bool)
	run = func(typed bool) {
		var todo []keyCand
		for _, c := range cands {
			if (st.keys == nil || st.keys(c.spec)) && typesText(c.spec) == typed {
				todo = append(todo, c)
			}
		}
		for len(todo) > 0 {
			t.Run("from "+todo[0].spec, func(t *testing.T) { // a program lasts as long as this function: the keys it does nothing to
				p := st.open(t)
				defer p.stop()
				for len(todo) > 0 {
					c := todo[0]
					todo = todo[1:]
					if changed, _ := p.try(c.key, typed); changed {
						hits = append(hits, c.spec)
						return
					}
					if st.fresh || (typed && !p.restore()) {
						return
					}
				}
			})
		}
	}
	run(false)
	run(true)
	if st.late != nil {
		var quiet []keyCand
		for _, c := range cands {
			if (st.keys == nil || st.keys(c.spec)) && !slices.Contains(hits, c.spec) {
				quiet = append(quiet, c)
			}
		}
		hits = append(hits, st.late(t, quiet)...)
	}
	return hits
}

// keySweep is what pressing every candidate everywhere found.
type keySweep struct {
	keys    map[string][]string // key -> the states in which it did something
	skipped map[string]string   // candidate -> the key a terminal sends instead
	states  []keyState
}

// candidatesOfATerminal are the candidates that a terminal can send as themselves, and the ones it cannot.
func candidatesOfATerminal() (cands []keyCand, skipped map[string]string) {
	skipped = map[string]string{}
	for _, spec := range keyCandidates() {
		k, other, ok := decodeCandidate(spec)
		if !ok {
			skipped[spec] = other
			continue
		}
		cands = append(cands, keyCand{spec, k})
	}
	return
}

// runKeySweep presses the candidates (only in the named states when only is not empty).
func runKeySweep(t *testing.T, only ...string) *keySweep {
	t.Helper()
	cands, skipped := candidatesOfATerminal()
	sw := &keySweep{keys: map[string][]string{}, skipped: skipped, states: keyStates()}
	var mu sync.Mutex
	t.Run("sweep", func(t *testing.T) {
		for _, st := range sw.states {
			if len(only) > 0 && !hasWord(strings.Join(only, " "), st.name) {
				continue
			}
			t.Run(st.name, func(t *testing.T) {
				t.Parallel()
				hits := pressAll(t, st, cands)
				mu.Lock()
				defer mu.Unlock()
				for _, spec := range hits {
					sw.keys[spec] = append(sw.keys[spec], st.name)
				}
			})
		}
	})
	return sw
}

// keysFile is keys-terminal.json.
type keysFile struct {
	Dimension         string              `json:"dimension"`
	Interface         string              `json:"interface"`
	About             string              `json:"about"`
	Candidates        int                 `json:"candidates"`
	CandidatesSHA256  string              `json:"candidates_sha256"`
	Indistinguishable map[string]string   `json:"indistinguishable"`
	States            []string            `json:"states"`
	Keys              map[string][]string `json:"keys"`
}

const keysAbout = "Written by internal/tui/app/keys_parity_test.go (go test ./internal/tui/app -run TestKeysParityTerminal -update): every candidate key pressed in each state of the chat program and of the watch and replay program. A key is listed with the states in which it did something (changed the screen or the cursor, told the session something, redrew, or ended the program); a character that only goes into the prompt is not a key. The candidates a terminal cannot tell from another key are under indistinguishable. The page's counterpart is keys-web.json."

// render is the text of the file: a key to a line, so that a change reads as a diff.
func (sw *keySweep) render() string {
	all := keyCandidates()
	var b strings.Builder
	b.WriteString("{\n")
	put := func(name string, v any, last bool) {
		j, _ := marshal(v)
		fmt.Fprintf(&b, "  %q: %s", name, j)
		if !last {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	put("dimension", "keys", false)
	put("interface", "terminal", false)
	put("about", keysAbout, false)
	put("candidates", len(all), false)
	put("candidates_sha256", candidatesDigest(all), false)
	skipped := map[string]string{}
	for k, v := range sw.skipped {
		skipped[k] = v
	}
	put("indistinguishable", skipped, false)
	var names []string
	for _, st := range sw.states {
		names = append(names, st.name)
	}
	put("states", names, false)
	b.WriteString("  \"keys\": {\n")
	var keys []string
	for k := range sw.keys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	order := map[string]int{}
	for i, st := range sw.states {
		order[st.name] = i
	}
	for i, k := range keys {
		sort.Slice(sw.keys[k], func(a, b int) bool { return order[sw.keys[k][a]] < order[sw.keys[k][b]] })
		j, _ := marshal(sw.keys[k])
		kj, _ := marshal(k)
		fmt.Fprintf(&b, "    %s: %s", kj, j)
		if i < len(keys)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("  }\n}\n")
	return b.String()
}

// marshal is json.Marshal without the escaping of < and > (a key may be one).
func marshal(v any) ([]byte, error) {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return []byte(strings.TrimSuffix(b.String(), "\n")), nil
}

// keysTerminalPath is the file the sweep is recorded in.
func keysTerminalPath() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "parity", "testdata", "keys-terminal.json")
}

func hasWord(s, word string) bool {
	for _, w := range strings.Fields(s) {
		if w == word {
			return true
		}
	}
	return false
}

// The keys that do something in the terminal interface are the ones in internal/parity/testdata/keys-terminal.json. A key that was
// bound or unbound since fails with the names of the keys, and with what to do about the other interface.
func TestKeysParityTerminal(t *testing.T) {
	sw := runKeySweep(t)
	if t.Failed() {
		return
	}
	got := sw.render()
	path := keysTerminalPath()
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err == nil && string(want) == got {
		return
	}
	var old, now keysFile
	if err == nil {
		_ = json.Unmarshal(want, &old)
	}
	_ = json.Unmarshal([]byte(got), &now)
	var added, gone, moved []string
	for k, v := range now.Keys {
		switch o, had := old.Keys[k]; {
		case !had:
			added = append(added, k)
		case strings.Join(o, ",") != strings.Join(v, ","):
			moved = append(moved, k)
		}
	}
	for k := range old.Keys {
		if _, has := now.Keys[k]; !has {
			gone = append(gone, k)
		}
	}
	sort.Strings(added)
	sort.Strings(gone)
	sort.Strings(moved)
	state := "out of date"
	if err != nil {
		state = "missing"
	}
	msg := "internal/parity/testdata/keys-terminal.json is " + state + ": the terminal's keys changed.\n"
	if len(added) > 0 {
		msg += "  keys the terminal now handles: " + strings.Join(added, "  ") + "\n"
	}
	if len(gone) > 0 {
		msg += "  keys the terminal no longer handles: " + strings.Join(gone, "  ") + "\n"
	}
	if len(moved) > 0 {
		msg += "  keys that now do something in other states: " + strings.Join(moved, "  ") + "\n"
	}
	t.Error(msg + "If this is intended: go test ./internal/tui/app -run TestKeysParityTerminal -update, read the diff of the file, then keep the page level with it: build the key there too, or list it in internal/parity/contract/keys.json with the reason it differs.")
}

// ---- the tests of the test ----

// The probe finds a key the program binds, does not find one it ignores, and does not take a typed character for a binding.
func TestKeysSweepFindsWhatTheProgramBinds(t *testing.T) {
	for _, c := range []struct {
		state, key string
		want       bool
	}{
		{"chat-idle", "ctrl+t", true},    // the statistics page
		{"chat-idle", "shift+tab", true}, // the permission mode
		{"chat-idle", "f9", false},       // nothing is bound to it
		{"chat-idle", "a", false},        // typing is not a binding
		{"chat-idle", "/", true},         // it opens the command menu
		{"chat-idle", "@", true},         // it opens the menu of the project's paths
		{"chat-idle", "ctrl+d", true},    // it ends the program on an empty prompt
		{"chat-idle", "up", true},        // the line sent before
		{"chat-text", "ctrl+e", true},    // the cursor moves to the end
		{"chat-text", "x", false},
		{"chat-running", "esc", true}, // the turn is cancelled
		{"chat-running", "f9", false},
		{"chat-question", "1", true}, // it answers
		{"chat-question", "q", false},
		{"watch-live", "q", true}, // it ends the program
		{"watch-live", "f9", false},
		{"watch-replay-cache", "a", true}, // the animation only shows as things arrive
		{"watch-live", "f", true},         // follow the agent that was answered, after an agent was chosen
		{"watch-replay", "right", true},
		{"watch-replay", "left", true},
		{"watch-replay", "end", true},
	} {
		var st keyState
		for _, s := range keyStates() {
			if s.name == c.state {
				st = s
			}
		}
		k, _, ok := decodeCandidate(c.key)
		if !ok {
			t.Fatalf("%s is not a key a terminal sends", c.key)
		}
		t.Run(c.state+"/"+c.key, func(t *testing.T) {
			if got := len(pressAll(t, st, []keyCand{{c.key, k}})) == 1; got != c.want {
				t.Errorf("%s: %s did something = %v, want %v", c.state, c.key, got, c.want)
			}
		})
	}
}

// A key that did nothing leaves the program as it was, so the keys can be pressed in one program one after the other: the same keys
// in the opposite order find the same bindings. (A state that a sequence primed, an undo or a yank, has a program for each key; a
// replay is played on only after the keys, so that no key sees how long the others took.) The replay is checked with the keys its
// bindings are, the named keys and a few more, because it is slow to draw.
func TestKeysSweepDoesNotDependOnTheOrder(t *testing.T) {
	cands, _ := candidatesOfATerminal()
	named := map[string]bool{"space": true, "f1": true}
	for _, n := range keyNamed {
		for _, m := range n.mods {
			named[m+n.name] = true
		}
	}
	var plain []keyCand
	for _, c := range cands {
		if r := []rune(c.spec); named[c.spec] || (len(r) == 1 && (strings.ContainsRune("abjpqx1+-", r[0]))) {
			plain = append(plain, c)
		}
	}
	for _, st := range keyStates() {
		use := cands
		switch st.name {
		case "chat-idle", "chat-text", "chat-yanked":
		case "watch-replay":
			use = plain
		default:
			continue
		}
		t.Run(st.name, func(t *testing.T) {
			t.Parallel()
			backwards := slices.Clone(use)
			slices.Reverse(backwards)
			a, b := pressAll(t, st, use), pressAll(t, st, backwards)
			slices.Sort(a)
			slices.Sort(b)
			if !slices.Equal(a, b) {
				t.Errorf("the keys found depend on the order they were pressed in:\n forwards  %v\n backwards %v", a, b)
			}
		})
	}
}

// Every candidate is a key a terminal can send, or is listed as indistinguishable from another; the bytes of each decode to the key
// that its name says.
func TestKeyCandidatesDecodeToTheirNames(t *testing.T) {
	seen := map[string]bool{}
	for _, spec := range keyCandidates() {
		if seen[spec] {
			t.Errorf("%s is listed twice", spec)
		}
		seen[spec] = true
		if _, other, ok := decodeCandidate(spec); !ok {
			switch spec {
			case "ctrl+h", "ctrl+i", "ctrl+m":
				if want := map[string]string{"ctrl+h": "backspace", "ctrl+i": "tab", "ctrl+m": "enter"}[spec]; other != want {
					t.Errorf("%s is sent as %s, want %s", spec, other, want)
				}
			default:
				t.Errorf("%s decodes to %q, which is not itself", spec, other)
			}
		}
	}
	for _, must := range []string{"ctrl+k", "alt+t", "shift+tab", "esc", "enter", "space", "f12", "alt+9", "?", "/", "@", "+", "-", ",", ".", "pgup", "pgdn", "home", "end", "shift+left", "shift+h", "alt+enter"} {
		if !seen[must] {
			t.Errorf("%s is not a candidate", must)
		}
	}
}
