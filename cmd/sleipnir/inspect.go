package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/reee344/sleipnir/internal/inspect"
)

func init() { extraCommands["inspect"] = cmdInspect }

// cmdInspect serves the embedded cache inspector: a read-only dashboard over a
// session's event log (live or after the fact), or over a directory of sessions
// such as `sleipnir rl rollout` output.
func cmdInspect(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:8787", "listen address; anything but a loopback address requires --token")
	token := fs.String("token", os.Getenv("SLEIPNIR_INSPECT_TOKEN"), "access token (also read from $SLEIPNIR_INSPECT_TOKEN, which keeps it out of process listings); required for a non-loopback --addr")
	open := fs.Bool("open", false, "open the dashboard in the default browser")
	asJSON := fs.Bool("json", false, "print the summary as JSON and exit (for scripts and CI)")
	session := fs.String("session", "", "with --json on a directory of sessions: the session to summarise (its id from the list)")
	interval := fs.Duration("interval", time.Second, "how often live logs are polled")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: sleipnir inspect [flags] DIR

Serves a read-only web dashboard for the cache engine and the swarm, built from
the session's event log: hit ratios, prompt layers per request, compactions,
cache anomalies, swarm coordination and cost against a no-cache and a naive
baseline. DIR is a session directory (events.jsonl and blobs/), or a directory of
sessions to browse, such as the output of `+"`sleipnir rl rollout`"+`. While the session
is still writing, the page follows the log live.

The server answers GET only, binds to loopback by default, and loads nothing from
the network.

flags:
`)
		fs.PrintDefaults()
	}
	// Flags may follow the directory (`inspect DIR --json`): parse, take one
	// positional, and parse the rest.
	var dirs []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		if fs.NArg() == 0 {
			break
		}
		dirs = append(dirs, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	if len(dirs) != 1 {
		fs.Usage()
		return errors.New("inspect: exactly one directory is required")
	}
	root := dirs[0]

	if *asJSON {
		return inspectJSON(root, *session)
	}

	srv, err := inspect.NewServer(inspect.Config{
		Root: root, Addr: *addr, Token: *token, Interval: *interval,
		Logf: func(f string, a ...any) { fmt.Fprintf(os.Stderr, "sleipnir inspect: "+f+"\n", a...) },
	})
	if err != nil {
		return err
	}
	ln, err := inspect.Listen(*addr, *token)
	if err != nil {
		return err
	}
	u := inspectURL(ln.Addr(), *token)
	fmt.Fprintf(os.Stderr, "sleipnir inspect: serving %s\n", root)
	fmt.Println(u)
	if *token != "" {
		fmt.Fprintln(os.Stderr, "sleipnir inspect: the URL carries the access token; it is stored in a cookie and removed from the address bar on first load")
	}
	if ta, ok := ln.Addr().(*net.TCPAddr); ok && !ta.IP.IsLoopback() {
		fmt.Fprintf(os.Stderr, "sleipnir inspect: listening on %s, not only on loopback\n", ta)
	}
	if *open {
		if err := openBrowser(u); err != nil {
			fmt.Fprintf(os.Stderr, "sleipnir inspect: could not open a browser (%v); open the URL above\n", err)
		}
	}
	fmt.Fprintln(os.Stderr, "sleipnir inspect: Ctrl-C to stop")
	return srv.Serve(ctx, ln)
}

// inspectJSON prints a session's summary (or, for a directory of sessions, the
// list with digests) and returns.
func inspectJSON(root, id string) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	list, err := inspect.Sessions(root, inspect.Options{})
	if err != nil {
		return err
	}
	if list.Mode == "multi" && id == "" {
		return enc.Encode(list)
	}
	sess, err := inspect.OpenSession(root, id, inspect.Options{})
	if err != nil {
		return err
	}
	return enc.Encode(sess.Summary())
}

// inspectURL is the address to open: loopback names for wildcard binds, and the
// token as a query parameter (the server turns it into a cookie and redirects).
func inspectURL(a net.Addr, token string) string {
	host, port := "127.0.0.1", ""
	if ta, ok := a.(*net.TCPAddr); ok {
		port = fmt.Sprint(ta.Port)
		switch {
		case ta.IP.IsUnspecified():
			host = "localhost"
		case strings.Contains(ta.IP.String(), ":"):
			host = "[" + ta.IP.String() + "]"
		default:
			host = ta.IP.String()
		}
	}
	u := "http://" + host + ":" + port + "/"
	if token != "" {
		u += "?token=" + url.QueryEscape(token)
	}
	return u
}

func openBrowser(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
