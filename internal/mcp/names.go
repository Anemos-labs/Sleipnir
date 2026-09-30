package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Exposed names.
//
// Every MCP tool becomes "mcp__<server>__<tool>". Providers accept tool names
// matching ^[a-zA-Z0-9_-]{1,64}$, permission rules are written against these
// names ("mcp__github__*", "mcp__github__create_issue"), and the name is part
// of the frozen tool list, so the mapping must be:
//
//   - a pure function of (server, tool): the same pair always yields the same
//     name, whatever else is configured, so adding a server never renames
//     another server's tools;
//   - unambiguous: the server segment never contains "__" and never ends in
//     "_", so splitting at the first "__" after the prefix recovers the server,
//     and "mcp__srv__*" cannot match another server's tools (that would let a
//     permission rule for one server approve calls to another);
//   - within 64 bytes.
//
// Names that are already clean pass through untouched. A server name that has
// to be altered (spaces, dots, "__", length) gets a short hash of the original
// appended, so two different names cannot collapse into one.
const (
	toolPrefix     = "mcp__"
	maxToolNameLen = 64
	maxServerSeg   = 24
	hashLen        = 8
)

func isNameChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
}

func isAlnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// hashOf is a short, stable digest of the original names, used to keep altered
// or shortened names distinct.
func hashOf(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:hashLen]
}

// cleanServerSegment reports whether s is usable as a server segment as is.
func cleanServerSegment(s string) bool {
	if s == "" || len(s) > maxServerSeg || strings.Contains(s, "__") {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isNameChar(s[i]) {
			return false
		}
	}
	return isAlnum(s[0]) && isAlnum(s[len(s)-1])
}

// serverSegment maps a configured server name to its segment of exposed names.
func serverSegment(server string) string {
	if cleanServerSegment(server) {
		return server
	}
	var b strings.Builder
	prevUnderscore := false
	for _, r := range server {
		c := byte('_')
		if r < 0x80 && isNameChar(byte(r)) {
			c = byte(r)
		}
		if c == '_' {
			if prevUnderscore {
				continue // no "__" inside a segment
			}
			prevUnderscore = true
		} else {
			prevUnderscore = false
		}
		b.WriteByte(c)
	}
	seg := strings.Trim(b.String(), "_-")
	if seg == "" {
		seg = "srv"
	}
	if room := maxServerSeg - 1 - 6; len(seg) > room {
		seg = strings.Trim(seg[:room], "_-")
	}
	return seg + "-" + hashOf(server)[:6]
}

// toolPart makes a tool name safe for the name charset. Characters outside it
// become "_" (one per rune).
func toolPart(tool string) string {
	var b strings.Builder
	for _, r := range tool {
		if r < 0x80 && isNameChar(byte(r)) {
			b.WriteByte(byte(r))
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// exposedName is the model-visible name of a server's tool (or prompt).
func exposedName(server, tool string) string {
	seg, t := serverSegment(server), toolPart(tool)
	name := toolPrefix + seg + "__" + t
	if len(name) <= maxToolNameLen {
		return name
	}
	return hashedName(server, tool)
}

// hashedName is the collision-proof, length-limited form: the tool part is
// cut to fit and a hash of the original pair is appended.
func hashedName(server, tool string) string {
	seg, t := serverSegment(server), toolPart(tool)
	room := maxToolNameLen - len(toolPrefix) - len(seg) - 2 - 1 - hashLen
	if room < 1 {
		room = 1
	}
	if len(t) > room {
		t = t[:room]
	}
	return toolPrefix + seg + "__" + t + "_" + hashOf(server, tool)
}

// ParseName splits an exposed name into its server segment and tool part. The
// segment is the sanitised form, not necessarily the configured server name.
func ParseName(name string) (server, tool string, ok bool) {
	rest, found := strings.CutPrefix(name, toolPrefix)
	if !found {
		return "", "", false
	}
	server, tool, found = strings.Cut(rest, "__")
	if !found || server == "" || tool == "" {
		return "", "", false
	}
	return server, tool, true
}
