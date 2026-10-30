package agent

import (
	"errors"

	"github.com/anemos-labs/sleipnir/internal/provider"
)

// ErrRepeatedCalls is a response that was stopped while it streamed because it wrote the same tool call over and over: a
// degenerate generation. One such response of a real model held 72 calls, among them one command 21 times and one edit 10
// times, and took ten minutes to reach the output limit; stopped at the repeatedCallLimit-th copy, it costs seconds.
var ErrRepeatedCalls = errors.New("the response repeated the same tool call")

// repeatedCallLimit is how many copies of one call (the same tool and the same arguments) a response may hold before it is
// stopped. A model that means to run a command twice in one turn does not write it four times.
const repeatedCallLimit = 4

// repeatWatch counts the tool calls of one streaming response by tool and arguments.
type repeatWatch struct {
	index   int // the call being streamed (meaningful once seen is set)
	name    string
	args    []byte
	seen    map[string]int
	tripped bool
}

// observe takes one streaming event and reports whether the response has just reached repeatedCallLimit copies of a call.
func (w *repeatWatch) observe(e provider.Event) bool {
	if w.tripped {
		return false
	}
	switch e.Kind {
	case provider.EvReset:
		*w = repeatWatch{}
	case provider.EvToolStart:
		if w.seen != nil && e.Index == w.index {
			return false // an endpoint that names the call again in a later chunk of it
		}
		if w.done() {
			return true
		}
		w.index, w.name, w.args = e.Index, e.ToolName, w.args[:0]
		if w.seen == nil {
			w.seen = map[string]int{}
		}
	case provider.EvToolDelta:
		if w.seen != nil && e.Index == w.index {
			w.args = append(w.args, e.Text...)
		}
	}
	return false
}

// done counts the call streamed so far, which the start of the next one has completed, and reports whether it trips the limit.
func (w *repeatWatch) done() bool {
	if w.seen == nil {
		return false
	}
	key := w.name + "\x00" + string(w.args)
	w.seen[key]++
	if w.seen[key] >= repeatedCallLimit {
		w.tripped = true
	}
	return w.tripped
}
