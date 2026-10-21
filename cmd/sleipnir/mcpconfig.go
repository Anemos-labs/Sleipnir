package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/mcp"
)

// mcpConfigFlag is --mcp-config: files that name MCP servers for one run. A program that drives the harness (an editor, a
// coding app running one session per conversation) hands a session its tool servers this way, without writing them into the
// person's configuration, where every other session would start them too.
type mcpConfigFlag []string

// String formats the named files as a comma-separated list for flag help.
func (m *mcpConfigFlag) String() string { return strings.Join(*m, ", ") }

// Set appends a nonblank path; the file is read after flag parsing.
func (m *mcpConfigFlag) Set(v string) error {
	if strings.TrimSpace(v) == "" {
		return fmt.Errorf(`--mcp-config wants the path of a JSON file such as {"mcpServers": {"name": {"command": "…"}}}`)
	}
	*m = append(*m, v)
	return nil
}

// mcpConfigFlags registers --mcp-config on fs.
func mcpConfigFlags(fs *flag.FlagSet) *mcpConfigFlag {
	m := &mcpConfigFlag{}
	fs.Var(m, "mcp-config", `a JSON file of MCP servers this run starts beside the configured ones, repeatable: {"mcpServers": {"name": {...}}} as .mcp.json writes it, or the configuration's mcp section; trusted like your user configuration, and an entry replaces a configured server of the same name`)
	return m
}

// servers reads the named files in order into server entries keyed by name; a later file's entry replaces an earlier one of the
// same name. A file that cannot be read, is not a JSON object or holds an entry the configuration would refuse is an error, so a
// run whose caller meant to give it a tool server never starts without that server.
func (m mcpConfigFlag) servers() (map[string]json.RawMessage, error) {
	if len(m) == 0 {
		return nil, nil
	}
	out := map[string]json.RawMessage{}
	for _, path := range m {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("--mcp-config: %w", err)
		}
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(b, &doc); err != nil {
			return nil, fmt.Errorf("--mcp-config %s: not a JSON object: %w", path, err)
		}
		_, issues := mcp.ParseWith(doc, mcp.ParseOptions{Scope: mcp.ScopeUser})
		for _, is := range issues {
			if is.Fatal {
				return nil, fmt.Errorf("--mcp-config %s: %w", path, is)
			}
		}
		entries := doc
		if inner, ok := doc["mcpServers"]; ok {
			entries = map[string]json.RawMessage{}
			if err := json.Unmarshal(inner, &entries); err != nil {
				return nil, fmt.Errorf("--mcp-config %s: mcpServers must be an object mapping server names to definitions", path)
			}
		}
		for name, raw := range entries {
			out[name] = raw
		}
	}
	return out, nil
}
