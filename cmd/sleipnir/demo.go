package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/anemos-labs/sleipnir/internal/demo"
	"github.com/anemos-labs/sleipnir/internal/tui/app"
)

func init() { extraCommands["demo"] = cmdDemo }

// demoTrailer is the last thing the demo says, after its report.
const demoTrailer = "\nTry it on a real model: sleipnir init --user && sleipnir swarm 6 \"<goal>\" --verify \"<your tests>\"\n"

// cmdDemo runs a scripted team through the real harness against the built-in mock endpoint: no API key, no network. On a terminal
// the team is watched in the live cockpit and the report follows when the person leaves it; anywhere else the report is all there is.
func cmdDemo(ctx context.Context, args []string) error {
	fs := newFlagSet("demo", flag.ExitOnError)
	scenario := fs.String("scenario", "", "what the team does: handbook (survey a handbook and summarise it, a second or two) | shop (build a small shop in git worktrees: mail, a cache that goes cold, a stuck agent, a cache break, a merge that is sent back; about twenty seconds; needs git and sh) (default: shop with the live cockpit on a terminal, handbook otherwise)")
	scale := fs.Float64("scale", 1, "shop: stretch (above 1) or squeeze (below 1) the time it takes, and the lifetime of its cache with it")
	topics := fs.Int("topics", 8, "handbook: documents to survey and summarise (an even number, 2-32)")
	dir := fs.String("dir", "", "where to put the workspace and the recorded session (default: a temporary directory)")
	plain := fs.Bool("plain", false, "no cockpit, even on a terminal: print the report as text when the team is done")
	noAnim := fs.Bool("no-anim", false, "with the cockpit: no animation, the horse stands")
	view := fs.String("view", "cockpit", "with the cockpit: the screen to start on: cockpit, cache, mail or board (the keys o c m b change it)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *topics < 2 || *topics > 32 {
		return fmt.Errorf("demo: --topics must be between 2 and 32")
	}
	if *scenario != "" {
		if err := oneOf("scenario", *scenario, "handbook", "shop"); err != nil {
			return fmt.Errorf("demo: %w", err)
		}
	}
	if *scale <= 0 || *scale > 20 {
		return fmt.Errorf("demo: --scale must be above 0 and at most 20")
	}
	v, err := app.ParseView(*view)
	if err != nil {
		return fmt.Errorf("demo: %w", err)
	}
	cockpit := !*plain && app.HaveTerminal(app.TTYOptions{})
	o := demo.Options{Scenario: pickScenario(*scenario, cockpit, haveCommands("git", "sh")), Scale: *scale, Topics: *topics, Dir: *dir}
	if !cockpit {
		o.Out = os.Stdout
		if _, err := demo.Run(ctx, o); err != nil {
			return err
		}
		fmt.Print(demoTrailer)
		return nil
	}
	return demoInCockpit(ctx, o, v, *noAnim, os.Stdout, os.Stderr)
}

// pickScenario is the scenario to run: the one that was asked for; else the shop where it is watched in the cockpit and the tools it
// needs are there (the handbook is over in a second or two, too short to watch); else the handbook.
func pickScenario(asked string, cockpit, tools bool) string {
	switch {
	case asked != "":
		return asked
	case cockpit && tools:
		return "shop"
	}
	return "handbook"
}

func haveCommands(names ...string) bool {
	for _, n := range names {
		if _, err := exec.LookPath(n); err != nil {
			return false
		}
	}
	return true
}

// demoInCockpit runs the team while the person watches it: the cockpit follows the session's event log as the harness writes it,
// keeps the last screen when the team is done (the other screens can be looked at, q leaves), and the report is printed after the
// screen has been left. A q before the end stops the team. An error of the run leaves the screen at once: there is nothing to hold
// it for, and the error is what there is to say.
func demoInCockpit(ctx context.Context, o demo.Options, view app.View, noAnim bool, stdout, stderr io.Writer) error {
	if o.Dir == "" {
		d, err := os.MkdirTemp("", "sleipnir-demo-")
		if err != nil {
			return err
		}
		o.Dir = d
	}
	var report bytes.Buffer
	o.Out = &report

	runCtx, stopRun := context.WithCancel(ctx)
	defer stopRun()
	screenCtx, leave := context.WithCancel(ctx)
	defer leave()
	type outcome struct {
		rep *demo.Report
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		rep, err := demo.Run(runCtx, o)
		if err != nil {
			leave()
		}
		done <- outcome{rep, err}
	}()

	src := app.OpenLive(screenCtx, filepath.Join(o.Dir, "session", "events.jsonl"), app.LiveOptions{WaitForFile: true})
	err := app.RunTTY(screenCtx, src, app.TTYOptions{View: view, NoAnim: noAnim, QuitLive: "stop the demo", QuitEnded: "leave, then the report"})
	src.Close()
	quit := err == nil // the person pressed q (or Ctrl-C), as against the screen ending for another reason
	stopRun()          // a team that is still at work is stopped with the screen
	res := <-done

	session := filepath.Join(o.Dir, "session")
	switch {
	case ctx.Err() != nil:
		return ctx.Err() // the process was told to stop
	case err != nil && !errors.Is(err, context.Canceled):
		return err // the screen failed
	case res.err != nil && quit && errors.Is(res.err, context.Canceled):
		fmt.Fprintf(stderr, "sleipnir demo: stopped before the team was done; what it did is in %s (sleipnir replay %s)\n", session, session)
		return nil
	case res.err != nil:
		return res.err
	}
	if _, werr := stdout.Write(report.Bytes()); werr != nil {
		return werr
	}
	_, werr := io.WriteString(stdout, demoTrailer)
	return werr
}
