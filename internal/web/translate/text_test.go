package translate

import (
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/tools"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// secret is a provider key of the shape the masker knows.
const secret = "sk-ant-api03-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789abcdefABCDEF"

// Untrusted text leaves as plain text: escape sequences, control, bidirectional and invisible characters are removed, secret-shaped
// substrings masked, lengths capped; markup stays text (the JSON escapes < and >, and the page escapes what it renders).
func TestNoUntrustedMarkup(t *testing.T) {
	h := newHarness(t, Config{Root: "/work", StartedAt: t0})
	s := h.tr.Sink()
	hostile := "<script>window.__pwn=1</script>\x1b[2J\x1b]52;c;ZXZpbA==\x07 ‮gnp.exe key=" + secret + "​"
	c := call("c1", "bash", map[string]any{"command": "echo " + hostile})
	s.ToolStart("be-1", c)
	s.ToolEnd("be-1", c, &tools.Result{Text: hostile + "\nline 2"}, time.Millisecond)
	s.Text("main", "here is a key "+secret+" and <img src=x onerror=alert(1)> ")
	s.Response("main", nil, 0)
	s.Notice("be-1", "warn", hostile)
	h.drain()
	b := newLog(t0)
	h.feed(b.add(time.Second, "be-1", "mail.send", map[string]any{"id": "m1", "from": "be-1", "to": "fe-1", "text": hostile}))
	h.set(t0.Add(2 * time.Second))
	h.tr.Question(wire.Question{ID: "q_1", Agent: "be-1", Cmd: hostile, Why: hostile, Kind: "command"})
	h.tr.Emit(&wire.Sys{Ch: "mgr", Glyph: "◇", Text: hostile})
	raw := string(lines(h.raws()))
	for _, bad := range []string{"\x1b", "\u001b", "‮", "​", "\x07", secret, "<script>", "<img"} {
		if strings.Contains(raw, bad) {
			t.Errorf("the output carries %q", bad)
		}
	}
	if !strings.Contains(raw, "⟦redacted:") || !strings.Contains(raw, "\\u003cscript\\u003e") {
		t.Errorf("the secret was not masked or the markup not kept as text:\n%s", raw)
	}
	for _, e := range h.decoded() {
		if e["k"] == "tool" {
			if n := len([]rune(e["arg"].(string))); n > capArg {
				t.Errorf("arg of %d runes", n)
			}
		}
	}
}

// t is session time in seconds, never negative and never decreasing in seq order: an event stamped before the run's start is at 0,
// and a log event older than an event already sent takes that event's t.
func TestTimeAndOrdering(t *testing.T) {
	h := newHarness(t, Config{Root: "/work", StartedAt: t0.Add(10 * time.Second)})
	b := newLog(t0)
	h.feed(b.add(time.Second, "be-1", "agent.spawn", map[string]any{"id": "be-1"})) // before the start
	h.set(t0.Add(20 * time.Second))
	h.tr.Emit(&wire.Say{Who: "you", Text: "hi"})
	h.feed(b.add(5*time.Second, "be-1", "mail.send", map[string]any{"id": "m1", "from": "be-1", "to": "mgr", "text": "late"})) // at 16 s, after a 20 s event
	h.feed(b.add(30*time.Second, "be-1", "mail.send", map[string]any{"id": "m2", "from": "be-1", "to": "mgr", "text": "later"}))
	evs := h.decoded()
	if evs[0]["t"] != 0.0 {
		t.Fatalf("an event before the start: %v", evs[0])
	}
	mails := ofKind(evs, "mail")
	if mails[0]["t"] != 10.0 || mails[1]["t"] != 26.0 {
		t.Fatalf("mail times: %v", mails)
	}
	checkStream(t, h.raws())
	if got := h.tr.Now(); got != 26 {
		t.Fatalf("Now %v at 36 s, 10 s after the start", got)
	}
}

// A response at a price nobody knows makes the agent's saving a lower bound (PARITY A16).
func TestHonestPrices(t *testing.T) {
	h := newHarness(t, Config{Root: "/work", StartedAt: t0})
	b := newLog(t0)
	h.feed(
		b.add(time.Second, "be-1", "model.request", map[string]any{"req": "r1", "kind": "main", "model": "no-such-model-xyz"}),
		b.add(time.Second, "be-1", "model.response", map[string]any{"req": "r1", "model": "no-such-model-xyz", "usage": map[string]any{"input_tokens": 100, "cache_read_tokens": 900}}),
	)
	u := ofKind(h.decoded(), "use")
	if len(u) != 1 || u[0]["savedPartial"] != true || u[0]["unpriced"] != 900.0 || u[0]["saved"] != 0.0 {
		t.Fatalf("use: %v", u)
	}
}

// A message is cut at 64 KiB: past it, end is sent and the rest is dropped; no event carries more than 16 KiB of text.
func TestMessagesAreBounded(t *testing.T) {
	h := newHarness(t, Config{StartedAt: t0})
	s := h.tr.Sink()
	chunk := strings.Repeat("abcdefgh ", 1000) // 9 KB
	for i := 0; i < 12; i++ {
		h.set(h.now().Add(200 * time.Millisecond))
		s.Text("main", chunk)
		h.drain()
		h.advance(0)
	}
	s.Response("main", nil, 0)
	h.drain()
	total, ends := 0, 0
	for _, e := range h.decoded() {
		if txt, ok := e["text"].(string); ok {
			if len(txt) > capEvent {
				t.Fatalf("an event of %d bytes", len(txt))
			}
			total += len(txt)
		}
		if e["end"] == true {
			ends++
		}
	}
	if total > capMessage || total < capMessage-capEvent || ends != 1 {
		t.Fatalf("%d bytes sent, %d ends", total, ends)
	}
}
