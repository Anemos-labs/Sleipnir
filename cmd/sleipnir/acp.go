package main

import (
	"context"
	"fmt"
	"os"
)

// init registers the ACP agent command with the CLI dispatcher.
func init() { extraCommands["acp"] = cmdACP }

const acpUsage = `usage: sleipnir acp serve [flags]

Serves Sleipnir over the Agent Client Protocol (ACP) on stdin and stdout, so
that an editor or another coding harness can run Sleipnir agents and teams as
normal ACP sessions. Each turn runs as 'sleipnir run --json --delegated'.
The flags are the limits of every turn; an ACP client cannot widen them.

The client names the folder, goal, model, permission mode and team size of a
session. Its MCP servers are handed to each turn through a private config file.

commands:
  serve [flags]       be an ACP agent on stdio (docs/ACP.md)
`

// cmdACP dispatches the acp subcommands.
func cmdACP(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		printHelp(os.Stderr, acpUsage)
		return nil
	}
	if args[0] == "serve" {
		return cmdACPServe(ctx, args[1:])
	}
	printHelp(os.Stderr, acpUsage)
	return errUnknownACPCommand(args[0])
}

// errUnknownACPCommand reports a subcommand the acp command does not have.
func errUnknownACPCommand(name string) error { return fmt.Errorf("acp: unknown command %q", name) }
