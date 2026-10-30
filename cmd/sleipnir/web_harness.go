package main

import (
	"context"
	"errors"
	"fmt"
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

// Reload queues /restart, which starts the chat again with its flags and this conversation. The terminal's chat does not send the
// agent on after a restart: the person does, with their next message.
func (h ttyHarnessHost) Reload(string) (string, error) {
	if !h.link.Queue("/restart") {
		return "", errors.New("the chat has ended")
	}
	return "the chat starts again when this turn ends, with this conversation and the changes. End your turn now: tell the person what changed and that their next message goes on with it.", nil
}
