package app

import (
	"encoding/xml"
	"io"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/tui/state/statetest"
)

func TestRecordIsADeterministicAnimation(t *testing.T) {
	path := statetest.DemoLogFile(t)
	o := RecordOptions{Speed: 0.25, FPS: 5}
	a, err := Record(path, o)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Record(path, o)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("the same log and options must give the same bytes")
	}
	d := xml.NewDecoder(strings.NewReader(a))
	for {
		if _, err := d.Token(); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("the recording is not well-formed XML: %v", err)
		}
	}
	for _, want := range []string{"@keyframes", ">SLEIPNIR</text>", ">riders</text>", ">gantt</text>"} {
		if !strings.Contains(a, want) {
			t.Errorf("the recording lacks %q", want)
		}
	}
	frames, err := Frames(path, o)
	if err != nil || len(frames) < 10 {
		t.Fatalf("%d frames, err %v: a 0.7 s session at a quarter of its speed is a few seconds of frames", len(frames), err)
	}
	for i := 1; i < len(frames); i++ {
		if frames[i].At <= frames[i-1].At {
			t.Fatalf("frame %d is at %v, not after frame %d (%v)", i, frames[i].At, i-1, frames[i-1].At)
		}
	}
}

// Stopping at an event shows the session as it was then: fewer agents, less spent.
func TestRecordUntilAnEvent(t *testing.T) {
	path := statetest.DemoLogFile(t)
	full, err := Frames(path, RecordOptions{Speed: 0.25})
	if err != nil {
		t.Fatal(err)
	}
	part, err := Frames(path, RecordOptions{Speed: 0.25, Until: 120})
	if err != nil {
		t.Fatal(err)
	}
	if len(part) >= len(full) {
		t.Errorf("%d frames until event 120, %d for the whole log", len(part), len(full))
	}
}

func TestRecordOfAMissingLog(t *testing.T) {
	if _, err := Record("/no/such/events.jsonl", RecordOptions{}); err == nil {
		t.Fatal("a log that is not there is an error")
	}
}
