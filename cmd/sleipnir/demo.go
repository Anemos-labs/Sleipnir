package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/reee344/sleipnir/internal/demo"
)

func init() { extraCommands["demo"] = cmdDemo }

// cmdDemo runs a scripted team through the real harness against the built-in
// mock endpoint: no API key, no network, a few seconds.
func cmdDemo(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("demo", flag.ExitOnError)
	topics := fs.Int("topics", 8, "documents to survey and summarise (an even number, 2-32)")
	dir := fs.String("dir", "", "where to put the workspace and the recorded session (default: a temporary directory)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *topics < 2 || *topics > 32 {
		return fmt.Errorf("demo: --topics must be between 2 and 32")
	}
	rep, err := demo.Run(ctx, demo.Options{Topics: *topics, Dir: *dir, Out: os.Stdout})
	if err != nil {
		return err
	}
	fmt.Printf("\nTry it on a real model: sleipnir init --user && sleipnir swarm 6 \"<goal>\" --verify \"<your tests>\"\n")
	_ = rep
	return nil
}
