package statetest

import (
	"bytes"
	_ "embed" // the recording of the demo session
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/reee344/sleipnir/internal/events"
)

// demo4 is the event log of the repository's own demo (internal/demo: a manager, scouts, writers and a reviewer, run through the real
// harness against the built-in cache-faithful mock endpoint), 4 topics, 312 events, 8 agents, 30 model requests. It is a recording:
// nothing in it is made at test time, so a test that folds it gets the same State on every machine and every run. The only edit
// is the workspace path, which was made into /work/handbook. To record it again (after the demo's script or an event's payload
// changed, say), and then to read the diff of what the golden tests say about it:
//
//	D=$(mktemp -d) && go run ./cmd/sleipnir demo --topics 4 --dir "$D"
//	sed "s#$D/handbook#/work/handbook#g" "$D/session/events.jsonl" > internal/tui/state/statetest/demo-4.events.jsonl
//	go test ./internal/tui/state -run Golden -update
//
//go:embed demo-4.events.jsonl
var demo4 []byte

// DemoLog returns the recorded log of the demo session as the bytes of an events.jsonl: one event per line, seq 1 to 312, in the
// order the harness wrote them. The caller may change the bytes; each call returns its own copy.
func DemoLog() []byte { return bytes.Clone(demo4) }

// DemoLogFile writes the recording to an events.jsonl in a directory of the test's own (removed with the test) and returns its
// path: for code that reads a log from disk, such as state.Fold, state.OpenReplay and state.Tail, and the commands built on them.
func DemoLogFile(tb testing.TB) string {
	tb.Helper()
	p := filepath.Join(tb.TempDir(), "events.jsonl")
	if err := os.WriteFile(p, demo4, 0o600); err != nil {
		tb.Fatalf("statetest: %v", err)
	}
	return p
}

// DemoEvents returns the events of DemoLog, in order. It panics if the embedded log cannot be read, which the tests of this package
// make a failure of the build rather than of a user.
func DemoEvents() []events.Event {
	evs, err := Parse(demo4)
	if err != nil {
		panic("statetest: the embedded demo log is damaged: " + err.Error())
	}
	return evs
}

// Parse reads the events of a log held in memory, one JSON object per line. Blank lines are skipped; a line that is not an event
// (not JSON, or with no seq) is an error naming the line: a recording that cannot be read is a broken fixture, and a test of
// tolerance builds its damaged logs by hand.
func Parse(log []byte) ([]events.Event, error) {
	var out []events.Event
	for n, line := range bytes.Split(log, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var e events.Event
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("line %d: %w", n+1, err)
		}
		if e.Seq == 0 {
			return nil, fmt.Errorf("line %d: no seq", n+1)
		}
		out = append(out, e)
	}
	return out, nil
}
