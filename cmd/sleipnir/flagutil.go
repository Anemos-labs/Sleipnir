package main

import (
	"flag"
	"fmt"
	"strings"
)

// parseInterspersed parses args like the go flag package but lets flags follow
// positional arguments, the way git and docker do: `sleipnir swarm 8 "goal"
// --verify "make test"` must not swallow --verify into the goal. A lone "--"
// ends flag parsing; everything after it is positional. It returns the
// positional arguments in order.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return pos, nil
		}
		// fs.Parse stopped at the first non-flag argument, or consumed a "--".
		// Distinguish the two: after "--" the flag package drops the marker, so
		// look at what was actually given.
		consumed := len(args) - len(rest)
		if consumed > 0 && args[consumed-1] == "--" {
			return append(pos, rest...), nil
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

// oneOf returns an error unless v is one of the allowed values.
func oneOf(flagName, v string, allowed ...string) error {
	for _, a := range allowed {
		if v == a {
			return nil
		}
	}
	return fmt.Errorf("--%s must be one of %s, got %q", flagName, strings.Join(allowed, ", "), v)
}
