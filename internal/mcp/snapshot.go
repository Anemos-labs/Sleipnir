package mcp

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/tools"
)

// SnapshotOptions bound what an MCP server may add to the tool list that every
// agent of a session sends. The list is part of the cached prefix (G0), so each
// byte here is paid on every request of every agent, and each change rewrites
// the cache for all of them.
type SnapshotOptions struct {
	// MaxDescriptionChars caps a tool description, marker included (default 600).
	MaxDescriptionChars int
	// MaxSchemaBytes rejects a tool whose canonical input schema is larger
	// (default 8 KiB). A schema cannot be truncated, only refused.
	MaxSchemaBytes int
	// MaxToolsPerServer and MaxTools cap the counts (defaults 128 and 256).
	MaxToolsPerServer int
	MaxTools          int
	// MaxTotalBytes is the budget for names, descriptions and schemas together
	// (default 64 KiB, roughly 16k tokens). Servers share it fairly: a server
	// that wants less than an equal share keeps everything, and the surplus
	// goes to the greedy ones.
	MaxTotalBytes int
	// AllowSuspicious keeps tools whose description or schema strings trip the
	// injection tripwire; by default they are excluded with a warning.
	AllowSuspicious bool
}

func (o *SnapshotOptions) defaults() {
	if o.MaxDescriptionChars <= 0 {
		o.MaxDescriptionChars = 600
	}
	if o.MaxSchemaBytes <= 0 {
		o.MaxSchemaBytes = 8 << 10
	}
	if o.MaxToolsPerServer <= 0 {
		o.MaxToolsPerServer = 128
	}
	if o.MaxTools <= 0 {
		o.MaxTools = 256
	}
	if o.MaxTotalBytes <= 0 {
		o.MaxTotalBytes = 64 << 10
	}
}

// Snapshot is an immutable view of the tools the connected servers offer at one
// moment, in the exact form that will be sent to the model: sorted by name,
// sanitised, size-budgeted, schemas canonical. Two snapshots of the same
// server state are equal byte for byte, and Hash says so cheaply.
//
// A snapshot never changes after it is taken. The manager keeps taking new
// ones as servers change; a session keeps using the one it froze until it
// declares an epoch.
type Snapshot struct {
	specs    []core.ToolSpec
	hash     core.Hash
	warnings []string
	bytes    int
	tools    []*mcpTool
}

// Specs returns a copy of the tool specs, sorted by name.
func (s *Snapshot) Specs() []core.ToolSpec {
	out := make([]core.ToolSpec, len(s.specs))
	for i, sp := range s.specs {
		out[i] = cloneSpec(sp)
	}
	return out
}

func cloneSpec(sp core.ToolSpec) core.ToolSpec {
	sp.InputSchema = append(json.RawMessage(nil), sp.InputSchema...)
	return sp
}

// Hash identifies the snapshot's model-visible content (names, descriptions,
// schemas): equal hashes mean an identical prefix.
func (s *Snapshot) Hash() core.Hash { return s.hash }

// Warnings lists what was dropped or altered, and why, in a stable order.
func (s *Snapshot) Warnings() []string { return append([]string(nil), s.warnings...) }

// Len is the number of tools.
func (s *Snapshot) Len() int { return len(s.specs) }

// Bytes is the size of the model-visible content.
func (s *Snapshot) Bytes() int { return s.bytes }

// Names lists the exposed tool names in order.
func (s *Snapshot) Names() []string {
	out := make([]string, len(s.specs))
	for i, sp := range s.specs {
		out[i] = sp.Name
	}
	return out
}

// Tools returns the tools of this snapshot, ready to register. Each carries
// its frozen spec and runs against the live connection of its server.
func (s *Snapshot) Tools() []tools.Tool {
	out := make([]tools.Tool, len(s.tools))
	for i, t := range s.tools {
		out[i] = t
	}
	return out
}

// Lookup resolves an exposed name to its server and the tool's own name.
func (s *Snapshot) Lookup(name string) (server, tool string, ok bool) {
	i := sort.Search(len(s.tools), func(i int) bool { return s.tools[i].spec.Name >= name })
	if i < len(s.tools) && s.tools[i].spec.Name == name {
		return s.tools[i].server, s.tools[i].tool, true
	}
	return "", "", false
}

// Register adds the snapshot's tools to a registry.
func (s *Snapshot) Register(reg *tools.Registry) {
	for _, t := range s.tools {
		reg.Register(t)
	}
}

// serverTools is one server's live tool list as input to a snapshot.
type serverTools struct {
	name   string
	cfg    ServerConfig // expanded or not: only filters and the transport type are read
	tools  []Tool
	remote bool
}

type candidate struct {
	server, tool string
	name         string
	desc         string
	schema       json.RawMessage
	readOnly     bool
	remote       bool
	size         int
}

func (c *candidate) measure() { c.size = len(c.name) + len(c.desc) + len(c.schema) }

// buildSnapshot is a pure function of its inputs: the same server tool lists and
// options always yield the same snapshot, in any input order.
func buildSnapshot(m *Manager, servers []serverTools, o SnapshotOptions) *Snapshot {
	o.defaults()
	var warns []string
	warn := func(format string, args ...any) { warns = append(warns, fmt.Sprintf(format, args...)) }

	sort.Slice(servers, func(i, j int) bool { return servers[i].name < servers[j].name })
	perServer := make([][]*candidate, len(servers))
	for si, sv := range servers {
		perServer[si] = serverCandidates(sv, o, warn)
	}

	// Names. A name is a pure function of (server, tool); if two different pairs
	// still land on one name, every one of them takes the hashed form, so the
	// outcome does not depend on order.
	byName := map[string][]*candidate{}
	for _, cs := range perServer {
		for _, c := range cs {
			c.name = exposedName(c.server, c.tool)
			byName[c.name] = append(byName[c.name], c)
		}
	}
	for name, group := range byName {
		if len(group) < 2 {
			continue
		}
		warn("name %s is produced by %d tools (%s); they were renamed with a hash suffix", name, len(group), collisionList(group))
		for _, c := range group {
			c.name = hashedName(c.server, c.tool)
		}
	}
	seen := map[string]*candidate{}
	for si, cs := range perServer {
		kept := cs[:0]
		for _, c := range cs {
			if prev, dup := seen[c.name]; dup {
				warn("tool %s of server %q dropped: its name still collides with server %q's %s", c.tool, c.server, prev.server, prev.tool)
				continue
			}
			seen[c.name] = c
			c.measure()
			kept = append(kept, c)
		}
		perServer[si] = kept
		sort.Slice(perServer[si], func(i, j int) bool { return perServer[si][i].name < perServer[si][j].name })
	}

	// Counts and bytes are budgets shared fairly between servers.
	counts := make([]int, len(perServer))
	sizes := make([]int, len(perServer))
	for i, cs := range perServer {
		counts[i] = len(cs)
		for _, c := range cs {
			sizes[i] += c.size
		}
	}
	countCap := waterFill(counts, o.MaxTools)
	for i, cs := range perServer {
		if len(cs) > countCap[i] {
			warn("server %q: %d tools exceed its share of the %d tool limit; the last %d (by name) were dropped", servers[i].name, len(cs), o.MaxTools, len(cs)-countCap[i])
			perServer[i] = cs[:countCap[i]]
			sizes[i] = 0
			for _, c := range perServer[i] {
				sizes[i] += c.size
			}
		}
	}
	byteCap := waterFill(sizes, o.MaxTotalBytes)
	for i, cs := range perServer {
		if sizes[i] <= byteCap[i] {
			continue
		}
		total, n := 0, 0
		for n < len(cs) && total+cs[n].size <= byteCap[i] {
			total += cs[n].size
			n++
		}
		warn("server %q: tool definitions need %d bytes but its share of the %d byte budget is %d; the last %d (by name) were dropped",
			servers[i].name, sizes[i], o.MaxTotalBytes, byteCap[i], len(cs)-n)
		perServer[i] = cs[:n]
	}

	var all []*candidate
	for _, cs := range perServer {
		all = append(all, cs...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].name < all[j].name })

	snap := &Snapshot{specs: make([]core.ToolSpec, len(all)), tools: make([]*mcpTool, len(all))}
	for i, c := range all {
		spec := core.ToolSpec{Name: c.name, Description: c.desc, InputSchema: c.schema, ReadOnly: c.readOnly}
		snap.specs[i] = spec
		snap.bytes += c.size
		snap.tools[i] = &mcpTool{m: m, server: c.server, tool: c.tool, spec: spec, remote: c.remote}
	}
	// The hash covers exactly what the model sees. ReadOnly is a scheduling hint
	// (json:"-") and rightly stays out of it.
	b, _ := core.MarshalStable(snap.specs)
	snap.hash = core.HashBytes(b)
	sort.Strings(warns)
	snap.warnings = warns
	return snap
}

func collisionList(group []*candidate) string {
	names := make([]string, len(group))
	for i, c := range group {
		names[i] = c.server + "/" + c.tool
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// serverCandidates vets one server's tools: usable definition, allowed by the
// entry's filters, no duplicate names, clean description and schema.
func serverCandidates(sv serverTools, o SnapshotOptions, warn func(string, ...any)) []*candidate {
	list := append([]Tool(nil), sv.tools...)
	sort.SliceStable(list, func(i, j int) bool { return list[i].Name < list[j].Name })

	counts := map[string]int{}
	for _, t := range list {
		counts[t.Name]++
	}
	var out []*candidate
	reported := map[string]bool{}
	capped := false
	for _, t := range list {
		if len(out) >= o.MaxToolsPerServer {
			// Only the first MaxToolsPerServer usable tools (by name) are exposed, so
			// the rest need not be vetted: a server listing thousands must not make
			// every snapshot cost thousands of sanitisations and regex scans.
			capped = true
			break
		}
		label := cleanStrict(t.Name)
		if label == "" {
			label = "(unnamed)"
		}
		switch {
		case t.Problem != "":
			warn("server %q: tool %q skipped: %s", sv.name, label, t.Problem)
			continue
		case counts[t.Name] > 1:
			// Which of several same-named tools is "the" tool would depend on the
			// order the server happened to list them in, and the snapshot must not.
			if !reported[t.Name] {
				reported[t.Name] = true
				warn("server %q: tool %q is listed %d times; all copies were dropped", sv.name, label, counts[t.Name])
			}
			continue
		}
		if !sv.cfg.toolAllowed(t.Name, exposedName(sv.name, t.Name)) {
			continue // filtered by configuration: not worth a warning
		}
		desc := t.Description
		if desc == "" {
			desc = t.Title
		}
		if desc == "" {
			desc = t.Annotations.Title
		}
		// Sanitise again here: the snapshot's promise (nothing invisible, capped, tidy)
		// must not depend on the tools having come through decodeTool.
		desc = cleanMeta(desc, o.MaxDescriptionChars*4)
		if why := detectInjection(desc); why != "" && !o.AllowSuspicious {
			warn("server %q: tool %q excluded: its description %s", sv.name, label, why)
			continue
		}
		schema, why, err := normalizeSchema(t.InputSchema, o.MaxSchemaBytes)
		if err != nil {
			warn("server %q: tool %q excluded: %v", sv.name, label, err)
			continue
		}
		if why != "" && !o.AllowSuspicious {
			warn("server %q: tool %q excluded: its input schema %s", sv.name, label, why)
			continue
		}
		desc, _ = truncateRunes(desc, o.MaxDescriptionChars)
		out = append(out, &candidate{
			server: sv.name, tool: t.Name, desc: desc, schema: schema,
			readOnly: t.Annotations.ReadOnly(), remote: sv.remote,
		})
	}
	if capped {
		warn("server %q offers more than %d usable tools; only the first %d (by name) are exposed", sv.name, o.MaxToolsPerServer, o.MaxToolsPerServer)
	}
	return out
}

// toolAllowed applies AllowTools and DenyTools. Patterns match either the
// server's own tool name or the exposed name; deny wins.
func (c ServerConfig) toolAllowed(tool, exposed string) bool {
	match := func(patterns []string) bool {
		for _, p := range patterns {
			if globMatch(p, tool) || globMatch(p, exposed) {
				return true
			}
		}
		return false
	}
	if match(c.DenyTools) {
		return false
	}
	return len(c.AllowTools) == 0 || match(c.AllowTools)
}

// waterFill divides total between demands: each demand at or under an equal
// share gets what it asked for, and what is left over is shared equally among
// the rest, repeatedly. It is deterministic (ties keep index order) and gives
// each demand at most what it asked for.
func waterFill(demands []int, total int) []int {
	out := make([]int, len(demands))
	idx := make([]int, len(demands))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return demands[idx[a]] < demands[idx[b]] })
	remaining := total
	for k, i := range idx {
		share := remaining / (len(idx) - k)
		out[i] = min(demands[i], share)
		remaining -= out[i]
	}
	return out
}
