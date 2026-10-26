package mock

import "strings"

// EarlyReview is how a manager's wait says it returned before every awaited task settled: one of them reached review while a
// worker sat free. A scripted manager has no verdict to give early; Rewait lets it wait again for the rest.
const EarlyReview = "reached review while"

// Rewait is the reply of a scripted manager whose last wait returned early (EarlyReview): the same wait again. ok is false when the
// newest tool results hold no early return, and the script answers as it would have.
func Rewait(c *Call) (r Reply, ok bool) {
	early := false
	for i := len(c.Messages) - 1; i >= 0; i-- {
		m := c.Messages[i]
		switch {
		case m.Role == "tool" && strings.Contains(m.Content, EarlyReview):
			early = true
		case m.Role == "assistant":
			if !early {
				return Reply{}, false
			}
			for j := len(m.ToolCalls) - 1; j >= 0; j-- {
				if tc := m.ToolCalls[j]; tc.Name == "wait" {
					return Reply{Text: "Waiting for the rest.", ToolCalls: []ToolCall{{ID: tc.ID + "-again", Name: "wait", Args: tc.Args}}}, true
				}
			}
			return Reply{}, false
		}
	}
	return Reply{}, false
}

// Rewaits counts the early waits a scripted manager answered with Rewait, for a script that counts its steps by its turns.
func Rewaits(c *Call) int {
	n := 0
	for i, m := range c.Messages {
		if m.Role == "assistant" && i > 0 && len(m.ToolCalls) == 1 && strings.HasSuffix(m.ToolCalls[0].ID, "-again") {
			n++
		}
	}
	return n
}
