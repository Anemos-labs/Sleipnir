package acpserve

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// maxACPServers bounds what a client can ask one turn to start.
const maxACPServers = 64

// serverNamePattern is the shape of a name Sleipnir accepts for a tool server.
var serverNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// acpMCPServer is one entry of ACP's mcpServers array.
type acpMCPServer struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Env     []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"env"`
	URL     string `json:"url"`
	Headers []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"headers"`
}

// writeMCPConfig turns the MCP servers of an ACP session setup into a private
// --mcp-config file for its runs. It returns "" when there are none or when
// the server was started with --no-mcp. The file holds credentials, so it is
// created with 0600, is never logged, and is removed when the session closes.
func writeMCPConfig(s *Server, raw json.RawMessage) (string, error) {
	if s.cfg.NoMCP || len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var servers []acpMCPServer
	if err := json.Unmarshal(raw, &servers); err != nil {
		return "", fmt.Errorf("expected a list of MCP servers: %w", err)
	}
	if len(servers) > maxACPServers {
		return "", fmt.Errorf("too many MCP servers (%d), at most %d", len(servers), maxACPServers)
	}
	out := map[string]any{}
	for _, srv := range servers {
		if !serverNamePattern.MatchString(srv.Name) {
			return "", fmt.Errorf("invalid MCP server name %q", srv.Name)
		}
		if _, exists := out[srv.Name]; exists {
			return "", fmt.Errorf("duplicate MCP server name %q", srv.Name)
		}
		entry, err := mcpEntry(srv)
		if err != nil {
			return "", fmt.Errorf("MCP server %s: %w", srv.Name, err)
		}
		out[srv.Name] = entry
	}
	if len(out) == 0 {
		return "", nil
	}
	b, err := json.Marshal(map[string]any{"mcpServers": out})
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp(s.stateDir, ".acp-mcp-*.json")
	if err != nil {
		return "", err
	}
	path := f.Name()
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		os.Remove(path)
		return "", err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(path)
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

// mcpEntry translates one ACP server definition into Sleipnir's config shape.
func mcpEntry(srv acpMCPServer) (map[string]any, error) {
	typ := strings.ToLower(strings.TrimSpace(srv.Type))
	if typ == "" {
		if srv.Command != "" && srv.URL == "" {
			typ = "stdio"
		} else if srv.URL != "" && srv.Command == "" {
			typ = "http"
		}
	}
	entry := map[string]any{"trust": true}
	switch typ {
	case "stdio":
		if srv.Command == "" {
			return nil, errors.New("stdio server has no command")
		}
		entry["command"] = srv.Command
		if len(srv.Args) > 0 {
			entry["args"] = srv.Args
		}
		if len(srv.Env) > 0 {
			env := make(map[string]string, len(srv.Env))
			for _, v := range srv.Env {
				if v.Name == "" {
					return nil, errors.New("empty environment variable name")
				}
				env[v.Name] = v.Value
			}
			entry["env"] = env
		}
	case "http", "sse":
		if srv.URL == "" {
			return nil, errors.New("remote server has no url")
		}
		u, err := url.Parse(srv.URL)
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("invalid url %q", srv.URL)
		}
		entry["url"] = srv.URL
		if typ == "sse" {
			entry["type"] = "sse"
		}
		if len(srv.Headers) > 0 {
			headers := make(map[string]string, len(srv.Headers))
			for _, h := range srv.Headers {
				if h.Name == "" {
					return nil, errors.New("empty header name")
				}
				headers[h.Name] = h.Value
			}
			entry["headers"] = headers
		}
		if isPrivateHost(u.Hostname()) {
			entry["allow_private"] = true
		}
	default:
		return nil, fmt.Errorf("unsupported transport %q", srv.Type)
	}
	return entry, nil
}

// isPrivateHost reports whether a URL host is loopback or private, where
// Sleipnir needs allow_private to connect.
func isPrivateHost(host string) bool {
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
	}
	return strings.EqualFold(host, "localhost")
}

// removeMCPFile unlinks a session's temporary MCP config, if it has one.
func removeMCPFile(path string) {
	if path == "" || filepath.Dir(path) == "" {
		return
	}
	_ = os.Remove(path)
}
