package main

import (
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/tui/svg"
)

func firstRow(f svg.Frame) string {
	var b strings.Builder
	for _, c := range f.Rows[0] {
		b.WriteString(c.Text)
	}
	return strings.TrimSpace(b.String())
}

// A burst of output is a frame of its own when the screen then stands still, even when it ended within a step of the last frame: a picture
// taken in a pause shows the screen as it was in that pause, not as it was before the burst was over (a menu's last row was missing from
// the picture of the first run, and the chat that came back after /login from the picture of that).
func TestARecordingHasAFrameOfAScreenThatStoodStill(t *testing.T) {
	timing := "H 0.000000 START_TIME\nO 0.100000 3\nO 0.020000 3\nO 5.000000 1\n"
	frames, total, err := recordedFrames([]byte("abcdefg"), strings.NewReader(timing), 20, 3, time.Hour, 90*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	var at []time.Duration
	for _, f := range frames {
		got = append(got, firstRow(f))
		at = append(at, f.At)
	}
	if strings.Join(got, ",") != "abc,abcdef,abcdefg" || at[1] != 120*time.Millisecond || total != 5120*time.Millisecond {
		t.Errorf("frames %q at %v, the recording %v; want abc, abcdef (at 120ms, where the screen stopped), abcdefg", got, at, total)
	}
	// output that keeps coming is not a frame a step: the frames are a step apart at least
	timing = "O 0.100000 1\nO 0.020000 1\nO 0.020000 1\nO 0.100000 1\n"
	frames, _, err = recordedFrames([]byte("wxyz"), strings.NewReader(timing), 20, 3, time.Hour, 90*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	got = nil
	for _, f := range frames {
		got = append(got, firstRow(f))
	}
	if strings.Join(got, ",") != "w,wxy,wxyz" {
		t.Errorf("frames %q; want w, wxy (the pause after y), wxyz", got)
	}
	// a wait that is longer than the gap is shortened to it
	frames, total, err = recordedFrames([]byte("ab"), strings.NewReader("O 0.1 1\nO 30.0 1\n"), 20, 3, 2*time.Second, 90*time.Millisecond)
	if err != nil || len(frames) != 2 || total != 2100*time.Millisecond {
		t.Errorf("a long wait: %d frames, %v, %v", len(frames), total, err)
	}
	if _, _, err = recordedFrames([]byte("a"), strings.NewReader("O 0.1 5\n"), 20, 3, time.Hour, time.Second); err == nil {
		t.Error("a timing file that names more bytes than the log has was accepted")
	}
	if _, _, err = recordedFrames(nil, strings.NewReader("H 0.0 START_TIME\n"), 20, 3, time.Hour, time.Second); err == nil {
		t.Error("a recording with no output was accepted")
	}
}
