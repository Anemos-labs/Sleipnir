package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/session"
)

// webHarnessHost is session.Host for a tab: what the sleipnir tool asks of the page is done as the person's own line would be.
type webHarnessHost struct{ t *webTab }

// Command runs a line that only looks, at once, as the page does for one typed while a turn runs.
func (h webHarnessHost) Command(ctx context.Context, line string) (string, error) {
	s := h.t.session()
	if s == nil {
		return "", errors.New("the session is starting again")
	}
	res, err := h.t.runCommand(ctx, s, line)
	if err != nil {
		return "", err
	}
	return res.Output, nil
}

// Queue puts the line behind the running turn, where a line the person typed then would wait; its output is a row of the
// transcript.
func (h webHarnessHost) Queue(line string) (string, error) {
	_, pos, _, err := h.t.enqueue(queued{text: oneLineCLI(line, 200), line: line})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("queued (%d): %s runs when this turn ends, and the person sees what it says. End your turn now with what you started and why.", pos, line), nil
}

// Reload queues a restart of the tab that keeps the conversation and sends the agent on once the session is back.
func (h webHarnessHost) Reload(why string) (string, error) {
	t := h.t
	t.mu.Lock()
	t.reloadWhy = why
	t.mu.Unlock()
	_, _, _, err := t.enqueue(queued{text: "reload: start again with the changes", op: func(ctx context.Context, s *session.Session) {
		if err := t.restart(ctx, "reload", nil, false); err != nil {
			t.sys("⚠", "reload: "+strings.TrimSpace(err.Error()))
		}
	}})
	if err != nil {
		return "", err
	}
	return "the session starts again when this turn ends, with this conversation and the changes; you are then told to go on. End your turn now with a line for the person on what changed.", nil
}

// ttyHarnessHost is session.Host for the chat on a terminal: a line that looks is answered as one typed beside a turn, and
// anything else waits in the program's queue as a line typed ahead.
type ttyHarnessHost struct {
	h    *sessionHost
	link interface{ Queue(string) bool }
}

// Command runs a line that only looks, at once.
func (h ttyHarnessHost) Command(ctx context.Context, line string) (string, error) {
	var out webBuffer
	h.h.Command(ctx, line, &out)
	return out.String(), nil
}

// Queue puts the line in the program's queue.
func (h ttyHarnessHost) Queue(line string) (string, error) {
	if !h.link.Queue(line) {
		return "", errors.New("the chat has ended")
	}
	return "queued: " + line + " runs when this turn ends, and the person sees what it says. End your turn now with what you started and why.", nil
}

// reloadEnv carries a reload the agent asked for from the chat that ends to the one that starts in its place (runAgain passes the
// environment on): what the reload applies, which the new chat tells the agent when it sends it on (carryReload).
const reloadEnv = "SLEIPNIR_RELOAD_WHY"

// Reload queues /restart, which starts the chat again with its flags and this conversation; the chat that starts sends the agent on
// (carryReload).
func (h ttyHarnessHost) Reload(why string) (string, error) {
	if why == "" {
		why = "changed settings"
	}
	if err := os.Setenv(reloadEnv, why); err != nil {
		return "", err
	}
	if !h.link.Queue("/restart") {
		os.Unsetenv(reloadEnv)
		return "", errors.New("the chat has ended")
	}
	return "the chat starts again when this turn ends, with this conversation and the changes; you are then told to go on. End your turn now with a line for the person on what changed.", nil
}

// carryReload sends the agent on in a chat that a reload it asked for started (reloadEnv), as the web page does; it reports whether
// it did. The variable is cleared, so a later restart does not repeat it.
func carryReload(s *session.Session, queue func(string) bool) bool {
	why, ok := os.LookupEnv(reloadEnv)
	if !ok {
		return false
	}
	os.Unsetenv(reloadEnv)
	if !s.Resumed() {
		return false
	}
	text, _ := reloadCarry(s, why)
	return queue(text)
}
