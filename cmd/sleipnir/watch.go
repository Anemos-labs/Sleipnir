package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/reee344/sleipnir/internal/tui/app"
	"github.com/reee344/sleipnir/internal/tui/state"
	"github.com/reee344/sleipnir/internal/tui/term"
	"github.com/reee344/sleipnir/internal/tui/widget"
)

func init() {
	extraCommands["watch"] = func(ctx context.Context, args []string) error { return cmdWatch(ctx, args, os.Stdout, os.Stderr) }
	extraCommands["replay"] = func(ctx context.Context, args []string) error { return cmdReplay(ctx, args, os.Stdout, os.Stderr) }
}

const (
	watchUsage  = "watch [flags] [SESSION]"
	replayUsage = "replay [flags] [SESSION]"

	// defaultCols and defaultRows are the size of the screen that is drawn when there is no terminal to ask (a recording, a text
	// screen written to a pipe).
	defaultCols, defaultRows = 100, 36
)

// sessionsRoot is the directory the sessions are in: <state>/sessions, <state> being $SLEIPNIR_HOME or ~/.sleipnir.
func sessionsRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(stateDir(home), "sessions")
}

// sessionLog finds the event log a SESSION argument names: a log file, a directory that holds one (a session's own, or the
// session directory that `sleipnir demo --dir` writes), the id of a session in the state directory or the start of one, or
// "latest" (the default), the session written most recently.
func sessionLog(arg string) (string, error) {
	arg = strings.TrimSpace(arg)
	root := sessionsRoot()
	if arg == "" || arg == "latest" {
		return latestSessionLog(root)
	}
	if fi, err := os.Stat(arg); err == nil {
		if !fi.IsDir() {
			return arg, nil
		}
		for _, rel := range []string{"events.jsonl", filepath.Join("session", "events.jsonl")} {
			if p := filepath.Join(arg, rel); isRegular(p) {
				return p, nil
			}
		}
		return "", fmt.Errorf("%s holds no events.jsonl (for a directory of sessions, name one of them)", arg)
	}
	if !strings.ContainsAny(arg, `/\`) { // an id, or the start of one
		if p := filepath.Join(root, arg, "events.jsonl"); isRegular(p) {
			return p, nil
		}
		ents, err := os.ReadDir(root)
		if err == nil {
			var match []string
			for _, e := range ents {
				if e.IsDir() && strings.HasPrefix(e.Name(), arg) && isRegular(filepath.Join(root, e.Name(), "events.jsonl")) {
					match = append(match, e.Name())
				}
			}
			switch len(match) {
			case 1:
				return filepath.Join(root, match[0], "events.jsonl"), nil
			case 0:
			default:
				sort.Strings(match)
				if len(match) > 4 {
					match = append(match[:4], "…")
				}
				return "", fmt.Errorf("%q is the start of several sessions: %s", arg, strings.Join(match, ", "))
			}
		}
	}
	return "", fmt.Errorf("no session %q (`sleipnir sessions` lists them; a path to a session directory or an events.jsonl works too)", arg)
}

func isRegular(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// latestSessionLog is the log of the session written most recently.
func latestSessionLog(root string) (string, error) {
	ents, err := os.ReadDir(root)
	if err != nil {
		return "", errors.New("no sessions yet: run something first (`sleipnir demo` needs no key)")
	}
	var best string
	var bestT time.Time
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(root, e.Name(), "events.jsonl")
		fi, err := os.Stat(p)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		if best == "" || fi.ModTime().After(bestT) {
			best, bestT = p, fi.ModTime()
		}
	}
	if best == "" {
		return "", errors.New("no sessions yet: run something first (`sleipnir demo` needs no key)")
	}
	return best, nil
}

// screenFlags are the flags that say what the screen shows; watch and replay both have them.
type screenFlags struct {
	view   string
	agent  string
	noAnim bool
	cols   int
	rows   int
}

func (s *screenFlags) register(fs *flag.FlagSet, withSize bool) {
	fs.StringVar(&s.view, "view", "cockpit", "the screen to start on: cockpit, cache, mail or board (the keys o c m b change it)")
	fs.StringVar(&s.agent, "agent", "", "the agent the cache view is about (default: the one that was answered last)")
	fs.BoolVar(&s.noAnim, "no-anim", false, "no animation: the horse stands and nothing moves")
	if withSize {
		fs.IntVar(&s.cols, "cols", 0, "width of the screen in cells (default: the terminal's, else 100)")
		fs.IntVar(&s.rows, "rows", 0, "height of the screen in lines (default: the terminal's, else 36)")
	}
}

// size is the size of the screen to draw without a terminal: what was asked for, else the terminal's, else the default.
func (s *screenFlags) size(out io.Writer) (cols, rows int) {
	cols, rows = s.cols, s.rows
	if f, ok := out.(*os.File); ok && (cols <= 0 || rows <= 0) {
		if sz, err := term.GetSize(f); err == nil {
			if cols <= 0 {
				cols = sz.Width
			}
			if rows <= 0 {
				rows = sz.Height
			}
		}
	}
	if cols <= 0 {
		cols = defaultCols
	}
	if rows <= 0 {
		rows = defaultRows
	}
	return cols, rows
}

// cmdReplay plays a recorded session back on a full screen, writes it as an animated SVG, or prints its last screen.
func cmdReplay(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlags("replay", stderr, replayUsage)
	var sf screenFlags
	sf.register(fs, true)
	record := fs.String("record", "", "write an animated SVG of the replay to FILE instead of showing it (needs no terminal)")
	final := fs.Bool("final", false, "print the last screen of the session as text and exit (needs no terminal)")
	speed := fs.Float64("speed", 1, "seconds of the session that pass in a second of the replay (a recording of a session that took a second is slowed down to be seen)")
	until := fs.Uint64("until", 0, "stop after the event with this seq (0: the whole log)")
	from := fs.Duration("from", 0, "with --record: start this far into the session (what happened before is on the screen at the first frame)")
	length := fs.Duration("length", 0, "with --record: record this much of the session from --from (default: to its end)")
	fps := fs.Int("fps", 5, "with --record: frames of the recording a second, at most 15")
	hold := fs.Duration("hold", 4*time.Second, "with --record: how long the last frame stays before the loop starts again")
	gallery := fs.String("gallery", "", "draw every recording listed in this manifest (docs/media/gallery.json) into --out, from the session or, for the chat's, from the transcript the manifest names; the other flags that choose a screen are then the manifest's")
	outDir := fs.String("out", ".", "with --gallery: the directory the recordings are written to")
	fs.Usage = func() {
		fmt.Fprint(stderr, `usage: sleipnir replay [flags] [SESSION]

Plays a recorded session back the way the cockpit showed it: the swarm, its cache
and its mail, drawn from the session's event log and nothing else. SESSION is a
session id (or the start of one), a session directory, an events.jsonl, or latest
(the default).

On a terminal it is a program: space pauses, the arrows seek, + and - change the
speed, o c m b choose the screen, ? lists the keys. --record writes the same thing
as an animated SVG that plays in a README, and --final prints the last screen as
text; neither needs a terminal.

flags:
`)
		fs.PrintDefaults()
	}
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err // -h is flag.ErrHelp, which main takes for success
	}
	if len(pos) > 1 {
		fs.Usage()
		return errors.New("replay: at most one session")
	}
	if (*record != "" && *final) || (*gallery != "" && (*record != "" || *final)) {
		return errors.New("replay: --record, --final and --gallery are alternatives")
	}
	view, err := app.ParseView(sf.view)
	if err != nil {
		return fmt.Errorf("replay: %w", err)
	}
	arg := ""
	if len(pos) == 1 {
		arg = pos[0]
	}
	path, err := sessionLog(arg)
	if err != nil {
		return fmt.Errorf("replay: %w", err)
	}
	switch {
	case *gallery != "":
		return renderGallery(stdout, stderr, path, *gallery, *outDir)
	case *record != "":
		cols, rows := sf.size(nil)
		o := app.RecordOptions{Cols: cols, Rows: rows, FPS: *fps, Speed: *speed, Until: *until, From: *from, Length: *length, Hold: *hold, NoAnim: sf.noAnim,
			View: view, Agent: sf.agent, Follow: sf.agent == ""}
		svg, err := app.Record(path, o)
		if err != nil {
			return fmt.Errorf("replay: %w", err)
		}
		if err := os.WriteFile(*record, []byte(svg), 0o644); err != nil {
			return fmt.Errorf("replay: %w", err)
		}
		fmt.Fprintf(stderr, "sleipnir replay: wrote %s (%d KB, %dx%d cells)\n", *record, (len(svg)+1023)/1024, cols, rows)
		return nil
	case *final:
		cols, rows := sf.size(stdout)
		return printFinal(stdout, path, view, sf.agent, cols, rows, *until, sf.noAnim)
	}
	src, err := app.OpenReplay(path, *speed, *until)
	if err != nil {
		return fmt.Errorf("replay: %w", err)
	}
	defer src.Close()
	err = app.RunTTY(ctx, src, app.TTYOptions{View: view, Agent: sf.agent, NoAnim: sf.noAnim})
	if errors.Is(err, app.ErrNoTerminal) {
		return errors.New("replay: a full-screen replay needs a terminal; --final prints the last screen as text and --record FILE.svg writes an animation")
	}
	return err
}

// renderGallery draws the recordings of a manifest into dir: the cockpit's from the session log, the chat's from the transcript each
// of them names. It prints, on stdout, one line "still SVG PNG SECONDS" for every still picture a recording wants (the picture PNG.png
// is taken from SVG.svg at that second), which is what scripts/record-demo.sh takes the stills from.
func renderGallery(stdout, stderr io.Writer, path, manifest, dir string) error {
	recs, err := app.LoadGallery(manifest)
	if err != nil {
		return fmt.Errorf("replay: %w", err)
	}
	docs, err := app.RenderGallery(path, recs)
	if err != nil {
		return fmt.Errorf("replay: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("replay: %w", err)
	}
	for _, r := range recs {
		name := r.Name + ".svg"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(docs[name]), 0o644); err != nil {
			return fmt.Errorf("replay: %w", err)
		}
		fmt.Fprintf(stderr, "sleipnir replay: wrote %s (%d KB)\n", filepath.Join(dir, name), (len(docs[name])+1023)/1024)
		for _, s := range r.StillList() {
			fmt.Fprintf(stdout, "still %s %s %g\n", r.Name, s.Name, s.At)
		}
	}
	return nil
}

// printFinal writes the last screen of a session as text: the log folded to its end (or to until), the screen drawn once at that
// size, no colours and no escape sequences.
func printFinal(w io.Writer, path string, view app.View, agent string, cols, rows int, until uint64, noAnim bool) error {
	st, err := state.FoldUntil(path, until)
	if err != nil {
		return fmt.Errorf("replay: %w", err)
	}
	sn := st.Snapshot()
	mem := app.NewMemory()
	mem.Seed(sn)
	if agent == "" {
		agent = app.Newest(sn)
	}
	lines := app.Draw(app.Scene{Snap: sn, View: view, Agent: agent, Frame: 0, Cols: cols, Rows: rows, Pal: widget.MonoPalette(), Mem: mem,
		NoAnim: true, Mode: app.Mode{Final: true}})
	for _, l := range lines {
		if _, err := fmt.Fprintln(w, strings.TrimRight(l.Plain(), " ")); err != nil {
			return err
		}
	}
	return nil
}

// cmdWatch follows a session that is being written, on a full screen.
func cmdWatch(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlags("watch", stderr, watchUsage)
	var sf screenFlags
	sf.register(fs, false)
	poll := fs.Duration("poll", state.DefaultPoll, "how often the log is looked at for new events")
	fs.Usage = func() {
		fmt.Fprint(stderr, `usage: sleipnir watch [flags] [SESSION]

Shows a session as it is written: the swarm cockpit, the cache of each agent, the
mail and the task board, drawn from the session's event log and nothing else, so it
can be opened in a second terminal beside a run (or tmux pane) and closed again
without touching it. SESSION is a session id (or the start of one), a session
directory, an events.jsonl, or latest (the default).

Space pauses the screen, o c m b choose the view, the arrows choose the agent, ?
lists the keys and q leaves. It needs a terminal; `+"`sleipnir replay SESSION --final`"+`
prints a text screen and `+"`sleipnir inspect`"+` serves a web page instead.

flags:
`)
		fs.PrintDefaults()
	}
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err // -h is flag.ErrHelp, which main takes for success
	}
	if len(pos) > 1 {
		fs.Usage()
		return errors.New("watch: at most one session")
	}
	view, err := app.ParseView(sf.view)
	if err != nil {
		return fmt.Errorf("watch: %w", err)
	}
	arg := ""
	if len(pos) == 1 {
		arg = pos[0]
	}
	path, err := sessionLog(arg)
	if err != nil {
		return fmt.Errorf("watch: %w", err)
	}
	var tick <-chan time.Time
	if *poll > 0 {
		t := time.NewTicker(*poll)
		defer t.Stop()
		tick = t.C
	}
	src := app.OpenLive(ctx, path, app.LiveOptions{Tick: tick})
	defer src.Close()
	err = app.RunTTY(ctx, src, app.TTYOptions{View: view, Agent: sf.agent, NoAnim: sf.noAnim})
	if errors.Is(err, app.ErrNoTerminal) {
		return errors.New("watch: a full-screen view needs a terminal; `sleipnir replay " + path + " --final` prints the screen as text")
	}
	return err
}
