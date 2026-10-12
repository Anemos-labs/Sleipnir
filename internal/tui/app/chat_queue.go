package app

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/tui/input"
)

// QueueUsage is what /queue says when it is given something it does not take.
const QueueUsage = "usage: /queue lists what waits for the turn, /queue drop N removes line N, /queue clear removes them all"

// IsQueueCommand reports whether a line is /queue, with or without arguments.
func IsQueueCommand(line string) bool {
	f := strings.Fields(line)
	return len(f) > 0 && f[0] == "/queue"
}

// QueueCommand is /queue over the lines that wait (texts, in the order they run): what it says, and the indexes (0-based, ascending)
// of the lines it removes. "/queue" lists them, "/queue drop N" removes the Nth as the list numbers it, "/queue clear" removes them all.
// The terminal's chat and a tab of the web interface both answer with it, over their own queues.
func QueueCommand(line string, texts []string) (out string, drop []int) {
	f := strings.Fields(line)
	if len(f) == 0 || f[0] != "/queue" {
		return QueueUsage, nil
	}
	switch {
	case len(f) == 1:
		if len(texts) == 0 {
			return "nothing is queued", nil
		}
		var b strings.Builder
		fmt.Fprintf(&b, "queued (%d), sent in this order when the turn is over:\n", len(texts))
		for i, t := range texts {
			fmt.Fprintf(&b, "  %d. %s\n", i+1, firstLine(t, 200, "…"))
		}
		b.WriteString("/queue drop N removes one, /queue clear removes them all")
		return b.String(), nil
	case len(f) == 2 && f[1] == "clear":
		if len(texts) == 0 {
			return "nothing is queued", nil
		}
		for i := range texts {
			drop = append(drop, i)
		}
		return fmt.Sprintf("cleared the queue (%d %s)", len(texts), plural(len(texts), "line", "lines")), drop
	case len(f) == 3 && f[1] == "drop":
		n, err := strconv.Atoi(f[2])
		if err != nil || n < 1 || n > len(texts) {
			if len(texts) == 0 {
				return "nothing is queued", nil
			}
			return fmt.Sprintf("/queue drop takes a number from 1 to %d, as /queue lists them", len(texts)), nil
		}
		return fmt.Sprintf("dropped %d: %s", n, firstLine(texts[n-1], 200, "…")), []int{n - 1}
	}
	return QueueUsage, nil
}

// queueCommand answers /queue over the lines typed ahead (a Ctrl-D that waits is not a line, and stays), writing the command and its
// answer into the scrollback.
func (m *chatModel) queueCommand(line string) {
	var texts []string
	var at []int // index in m.queue of each text
	for i, q := range m.queue {
		if !q.eof {
			texts = append(texts, q.text)
			at = append(at, i)
		}
	}
	out, drop := QueueCommand(line, texts)
	if len(drop) > 0 {
		gone := map[int]bool{}
		for _, d := range drop {
			gone[at[d]] = true
		}
		keep := m.queue[:0:0]
		for i, q := range m.queue {
			if !gone[i] {
				keep = append(keep, q)
			}
		}
		m.queue = keep
	}
	m.syncStream()
	m.block(bkPrompt, m.k.promptLines(line, m.cols))
	m.asideEnded(asideEnd{out: out})
}

// pullQueued is Up on an empty prompt while lines wait: the last line typed ahead comes back into the prompt to be edited (Enter queues
// it again). It reports whether there was one.
func (m *chatModel) pullQueued() bool {
	for i := len(m.queue) - 1; i >= 0; i-- {
		if m.queue[i].eof {
			continue
		}
		text := m.queue[i].text
		m.queue = append(m.queue[:i:i], m.queue[i+1:]...)
		m.ed.SetText(text)
		m.setHint("taken back from the queue: Enter queues it again")
		return true
	}
	return false
}

// steerNow is Alt+S while a turn runs: what the prompt holds goes to the running turn as a steer (/steer), which the agent reads with
// its next step, instead of waiting in the queue. A slash command cannot steer, and with nothing running the key does nothing.
func (m *chatModel) steerNow() bool {
	text := strings.TrimSpace(m.ed.Text())
	if text == "" || m.running == nil || m.running.kind != runTurn || !m.attached {
		return false
	}
	if strings.HasPrefix(text, "/") {
		m.setHint("a command cannot steer: Enter queues it")
		return true
	}
	_ = m.ed.History().Add(text)
	m.ed.Reset()
	m.aside("/steer " + text)
	return true
}

// queueKey is a key of the queue: Up on an empty prompt pulls the last queued line back, Alt+S sends the prompt as a steer. It reports
// whether it took the key.
func (m *chatModel) queueKey(k input.Key) bool {
	switch {
	case k.Is(input.Up, 0) && m.ed.Empty() && len(m.queue) > 0 && !m.ed.Completion().Open:
		return m.pullQueued()
	case k.IsRune('s', input.Alt) && m.busy():
		return m.steerNow()
	}
	return false
}
