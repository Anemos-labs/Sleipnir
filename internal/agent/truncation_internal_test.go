package agent

import (
	"strings"
	"sync"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/events"
)

type noticeRecorder struct {
	NopSink
	mu    sync.Mutex
	texts []string
}

func (r *noticeRecorder) Notice(_, _, text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.texts = append(r.texts, text)
}

// A server whose token counts wobbled once (the prompt grew 6% and the count by 344 tokens, at 36,000) looked like one that cuts the prompt
// off at 36,352 tokens. It then read 48,000 tokens of a later prompt, and the agent went on compacting against the guessed window: 344
// compactions in an afternoon. A prompt read past the guess withdraws it, says so once, and the window it replaced is back.
func TestAWindowGuessThatTheServerReadsPastIsWithdrawn(t *testing.T) {
	sink := &noticeRecorder{}
	a := &Agent{cfg: Config{ID: "be-1", Model: cost.Model{ContextTokens: 200_000}, Sink: sink, Events: events.Discard{}}}
	a.noteTruncation(100_000, 35_999)
	a.noteTruncation(106_000, 36_343)
	if got := a.cfg.Model.ContextTokens; got != 36_352 {
		t.Fatalf("after the wobble the window is %d, want the 36352 guessed", got)
	}
	a.noteTruncation(110_000, 36_300) // within the guess: nothing proves it wrong
	if got := a.cfg.Model.ContextTokens; got != 36_352 {
		t.Fatalf("a prompt inside the guess changed the window to %d", got)
	}
	a.noteTruncation(140_000, 47_987)
	if got := a.cfg.Model.ContextTokens; got != 200_000 {
		t.Fatalf("after a prompt read past the guess the window is %d, want the 200000 it replaced", got)
	}
	// It is not guessed again, however the counts wobble later.
	a.noteTruncation(150_000, 48_000)
	a.noteTruncation(200_000, 48_100)
	a.noteTruncation(260_000, 60_000)
	if got := a.cfg.Model.ContextTokens; got != 200_000 {
		t.Fatalf("the window was guessed again: %d", got)
	}
	if len(sink.texts) != 2 || !strings.Contains(sink.texts[0], "read only about 36343 tokens") ||
		!strings.Contains(sink.texts[1], "read 47987 tokens") || !strings.Contains(sink.texts[1], "withdrawn") {
		t.Fatalf("notices = %q, want the guess and its withdrawal", sink.texts)
	}
}

// A window that was never lowered by the guess (the model's own was smaller already) is not "withdrawn" when a prompt passes the guess.
func TestAGuessThatChangedNothingIsNotWithdrawn(t *testing.T) {
	sink := &noticeRecorder{}
	a := &Agent{cfg: Config{ID: "be-1", Model: cost.Model{ContextTokens: 8_192}, Sink: sink, Events: events.Discard{}}}
	a.noteTruncation(100_000, 35_999)
	a.noteTruncation(106_000, 36_343)
	a.noteTruncation(140_000, 47_987)
	if got := a.cfg.Model.ContextTokens; got != 8_192 {
		t.Fatalf("window = %d, want the model's 8192", got)
	}
	if len(sink.texts) != 1 {
		t.Fatalf("notices = %q, want only the guess", sink.texts)
	}
}
