// Temporary development server (not part of the deliverable).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/reee344/sleipnir/internal/inspect"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8791", "")
	token := flag.String("token", "", "")
	flag.Parse()
	srv, err := inspect.NewServer(inspect.Config{Root: flag.Arg(0), Addr: *addr, Token: *token, Logf: func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) }})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ln, err := inspect.Listen(*addr, *token)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	fmt.Println("listening", ln.Addr())
	_ = srv.Serve(ctx, ln)
}
