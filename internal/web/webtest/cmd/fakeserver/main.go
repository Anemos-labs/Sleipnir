// Command fakeserver serves the real UI of `sleipnir web` with canned data, so that the page can be loaded, driven and photographed
// without a model, a key or the session host. See package webtest for what it answers.
//
//	go run ./internal/web/webtest/cmd/fakeserver [-addr 127.0.0.1:6969] [-ui DIR] [-speed 1]
//
// The first line on standard output is the address to open, with the run token; everything else goes to standard error. Ctrl-C stops
// it with status 0.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/anemos-labs/sleipnir/internal/web/webtest"
)

// main runs the command and exits with its status.
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "fakeserver:", err)
		os.Exit(1)
	}
}

// run parses the flags, starts the server and serves until ctx ends. It prints the address to open as the first line of stdout.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("fakeserver", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("addr", "127.0.0.1:0", "listen address (loopback only; 127.0.0.1:0 picks a free port)")
	ui := fs.String("ui", "", "serve this directory as the UI instead of the one embedded in the binary (read once, at start)")
	speed := fs.Float64("speed", 1, "play the canned continuation of the session at this multiple of real time; 0 plays nothing until POST /api/_fake/step or /api/_fake/play")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("takes no arguments, got %q", fs.Arg(0))
	}
	o := webtest.Options{Addr: *addr, Speed: *speed, Logf: func(f string, a ...any) { fmt.Fprintf(stderr, "fakeserver: "+f+"\n", a...) }}
	if *ui != "" {
		st, err := os.Stat(*ui)
		if err != nil || !st.IsDir() {
			return fmt.Errorf("-ui %s is not a directory", *ui)
		}
		o.UI = os.DirFS(*ui)
	}
	s, err := webtest.NewServer(o)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	if ta, ok := ln.Addr().(*net.TCPAddr); !ok || !ta.IP.IsLoopback() {
		ln.Close()
		return fmt.Errorf("%s is not a loopback address", ln.Addr())
	}
	fmt.Fprintln(stdout, s.Web.URL(ln.Addr()))
	fmt.Fprintln(stderr, "fakeserver: canned session \"shop\"; control routes under /api/_fake/ (step, play, reset, ask); Ctrl-C to stop")
	return s.Serve(ctx, ln)
}
