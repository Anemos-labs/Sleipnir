package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// The frames of the page's stream have the hub classes of wire.Frame: what the page must not miss is critical, a value that a newer
// one replaces is coalescable under its key, and a message's continuation is ordinary.
func TestFrameClassesAreTheHubClasses(t *testing.T) {
	for _, tc := range []struct {
		ev        wire.Event
		first     bool
		crit, coa bool
		key       string
	}{
		{&wire.Ask{}, false, true, false, ""},
		{&wire.State{ID: "be-1"}, false, true, false, ""},
		{&wire.Use{ID: "be-1"}, false, false, true, "use/shop/be-1"},
		{&wire.Layers{ID: "mgr"}, false, false, true, "layers/shop/mgr"},
		{&wire.Gov{}, false, false, true, "gov/shop"},
		{&wire.Verdict{}, false, false, true, "verdict/shop"},
		{&wire.Diff{File: "a.go"}, false, false, true, "diff/shop/a.go"},
		{&wire.Ckpt{CID: "c01"}, true, true, false, ""},
		{&wire.Ckpt{CID: "c01"}, false, false, true, "ckpt/shop/c01"},
		{&wire.More{}, false, false, false, ""},
	} {
		crit, coa, key := frameClass("shop", tc.ev, tc.first)
		if crit != tc.crit || coa != tc.coa || key != tc.key {
			t.Errorf("%s: %v %v %q, want %v %v %q", wire.KindOf(tc.ev), crit, coa, key, tc.crit, tc.coa, tc.key)
		}
	}
}

// The stand-in translator streams the main agent's answer as say and more, shows tool calls as states, keeps what it emitted in its
// journal with increasing seq, cleans terminal controls, and does nothing once closed.
func TestStubTranslatorJournalsWhatItEmits(t *testing.T) {
	var mu sync.Mutex
	var frames []wire.Frame
	tr := newStubTranslator(translatorConfig{Tab: "shop", Gen: 1, StartedAt: time.Now(), Publish: func(f wire.Frame) {
		mu.Lock()
		frames = append(frames, f)
		mu.Unlock()
	}})
	sink := tr.Sink()
	sink.Text("main", "hello \x1b[31mworld")
	sink.Text("main", " again")
	sink.Text("be-1", "worker prose is not shown")
	sink.ToolStart("main", core.Block{ToolName: "bash", Input: json.RawMessage(`{"command":"ls"}`)})
	sink.Notice("be-1", "warn", "lease conflict")
	tr.Emit(&wire.Turn{S: "end"})
	_, evs, seq, _ := tr.Journal()
	if seq != uint64(len(evs)) || len(evs) != 6 {
		t.Fatalf("seq %d, %d events", seq, len(evs))
	}
	kinds := []string{}
	for _, raw := range evs {
		var e map[string]any
		_ = json.Unmarshal(raw, &e)
		kinds = append(kinds, e["k"].(string))
	}
	if got := strings.Join(kinds, " "); got != "say more more state sys turn" {
		t.Errorf("kinds %s", got)
	}
	if strings.Contains(string(evs[0]), "\x1b") || strings.Contains(string(evs[0]), `\u001b`) {
		t.Errorf("a terminal control reached the page: %s", evs[0])
	}
	tr.Close()
	tr.Emit(&wire.Turn{S: "start"})
	if _, evs, _, _ := tr.Journal(); len(evs) != 6 {
		t.Error("a closed translator emitted")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(frames) != 6 || frames[0].Type != "ev" || !frames[0].Critical {
		t.Errorf("%d frames, first %+v", len(frames), frames[0])
	}
}

// A hint row names the Settings page that fixes a missing model or key.
func TestHintsNameTheSettingsPage(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{session.ErrNoModel, "models"},
		{fmt.Errorf("start: %w", session.ErrNoModel), "models"},
		{errors.New(`provider "x" has no key: /login x, or set X_KEY`), "providers"},
		{errors.New("disk full"), ""},
	} {
		if got := hintRow("x", tc.err).Open; got != tc.want {
			t.Errorf("%v: %q, want %q", tc.err, got, tc.want)
		}
	}
}
