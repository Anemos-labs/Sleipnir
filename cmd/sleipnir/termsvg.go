package main

// `sleipnir term-svg` turns a real terminal recording into an animated SVG for docs/media: the output log and the timing file of
// util-linux `script` (script --log-out F --log-timing T --logging-format advanced -c COMMAND) are played into the repository's own
// terminal emulator, a frame is taken as the screen changes, and the frames are drawn by the same code that draws the replay's pictures.
// Nothing is scripted but the keys the recorder pressed: the model, the tools and the timing are those of the real session, except that a wait
// longer than --max-gap is shortened to it (a model that thinks for twenty seconds would otherwise be twenty seconds of a still picture), which
// is said under the picture. It is a tool of scripts/record-real.sh and is not in the list of commands.

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/tui/svg"
	"github.com/anemos-labs/sleipnir/internal/tui/vt"
)

func init() { extraCommands["term-svg"] = cmdTermSVG }

func cmdTermSVG(_ context.Context, args []string) error {
	fs := newFlagSet("term-svg", flag.ContinueOnError)
	logf := fs.String("log", "", "the output log of script (--log-out)")
	timing := fs.String("timing", "", "the timing file of script (--log-timing, --logging-format advanced)")
	out := fs.String("out", "", "write the SVG here")
	cols := fs.Int("cols", 110, "columns of the recorded terminal")
	rows := fs.Int("rows", 32, "rows of the recorded terminal")
	maxGap := fs.Duration("max-gap", 1500*time.Millisecond, "shorten any wait longer than this to it")
	minStep := fs.Duration("min-step", 90*time.Millisecond, "take a frame at most this often")
	hold := fs.Duration("hold", 4*time.Second, "how long the last frame stays before the loop starts again")
	title := fs.String("title", "", "a title for the window bar")
	still := fs.String("still", "", "also write one moment as a static SVG here")
	stillAt := fs.Duration("still-at", -1, "with --still: the moment (from the start of the recording after waits are shortened); default the last frame")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *logf == "" || *timing == "" || *out == "" {
		return errors.New("term-svg: --log, --timing and --out are required")
	}
	data, err := os.ReadFile(*logf)
	if err != nil {
		return err
	}
	if i := strings.IndexByte(string(data), '\n'); i >= 0 {
		data = data[i+1:] // script's own "Script started on ..." line
	}
	tf, err := os.Open(*timing)
	if err != nil {
		return err
	}
	defer tf.Close()

	frames, at, err := recordedFrames(data, tf, *cols, *rows, *maxGap, *minStep)
	if err != nil {
		return err
	}
	th := svg.DefaultTheme()
	th.Title = *title
	if err := os.WriteFile(*out, []byte(svg.Animated(frames, th, svg.Options{Hold: *hold})), 0o644); err != nil {
		return err
	}
	if *still != "" {
		pick := frames[len(frames)-1]
		if *stillAt >= 0 {
			for _, f := range frames {
				if f.At <= *stillAt {
					pick = f
				}
			}
		}
		pick.CursorOn = false
		if err := os.WriteFile(*still, []byte(svg.Static(pick, th)), 0o644); err != nil {
			return err
		}
	}
	fmt.Fprintf(os.Stderr, "term-svg: wrote %s (%d frames, %s)\n", *out, len(frames), (at + *hold).Round(100*time.Millisecond))
	return nil
}

// recordedFrames plays what script wrote (data, with timing, its --log-timing) into a terminal emulator and takes a frame of it at most
// every minStep, a wait longer than maxGap shortened to it. It returns the frames and the length of the recording. Output that comes
// within a step of the last frame is on the screen at the next frame; when nothing comes for a step after it, it has a frame of its own at
// the moment it ended, because the screen stood still in that state (a picture taken in a pause must show the screen as it was).
func recordedFrames(data []byte, timing io.Reader, cols, rows int, maxGap, minStep time.Duration) ([]svg.Frame, time.Duration, error) {
	term := vt.New(cols, rows)
	var frames []svg.Frame
	var at, last time.Duration
	dirty := false // output written since the last frame
	off := 0
	sc := bufio.NewScanner(timing)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 3 || f[0] != "O" {
			continue // headers, input and signals
		}
		delay, err1 := strconv.ParseFloat(f[1], 64)
		n, err2 := strconv.Atoi(f[2])
		if err1 != nil || err2 != nil || n < 0 || off+n > len(data) {
			return nil, 0, fmt.Errorf("term-svg: the timing file does not match the log at %q", sc.Text())
		}
		d := time.Duration(delay * float64(time.Second))
		if d > maxGap {
			d = maxGap
		}
		if dirty && d >= minStep {
			frames = append(frames, svg.Capture(term, at)) // the screen stood still from the last output until this one
			last, dirty = at, false
		}
		at += d
		term.Write(data[off : off+n])
		off += n
		dirty = true
		if len(frames) == 0 || at-last >= minStep {
			frames = append(frames, svg.Capture(term, at))
			last, dirty = at, false
		}
	}
	if dirty { // the last change is always on screen
		frames = append(frames, svg.Capture(term, at))
	}
	if len(frames) == 0 {
		return nil, 0, errors.New("term-svg: nothing was recorded")
	}
	return frames, at, nil
}
