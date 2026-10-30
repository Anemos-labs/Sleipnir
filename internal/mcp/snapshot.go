package mcp

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// SnapshotOptions bound what an MCP server may add to the tool list that every
// agent of a session sends. The list is part of the cached prefix (G0), so each
// byte here is paid on every request of every agent, and each change rewrites
// the cache for all of them.
//
// A tool that does not fit (its schema is too large or too deep for the
// prefix, its server is past its share of the count or byte budget, or its
// entry sets defer_tools) is not dropped: it is loaded on demand. Two fixed
// tools, mcp__search and mcp__call, take its place in the list; the model finds
// the tool and its schema with the first and runs it with the second (see
// deferred.go). Only what is unusable either way is excluded with a warning.
type SnapshotOptions struct {
	// MaxDescriptionChars caps a tool description, marker included (default 600).
	MaxDescriptionChars int
	// MaxSchemaBytes is the largest canonical input schema sent in the tool
	// list (default 8 KiB). A schema cannot be truncated: a larger one makes its
	// tool load on demand.
	MaxSchemaBytes int
	// MaxToolsPerServer and MaxTools cap the counts in the tool list (defaults
	// 128 and 256). Tools past them load on demand.
	MaxToolsPerServer int
	MaxTools          int
	// MaxTotalBytes is the budget for names, descriptions and schemas together
	// (default 64 KiB, roughly 16k tokens), mcp__search and mcp__call included.
	// Servers share it fairly: a server that wants less than an equal share
	// keeps everything, and the surplus goes to the greedy ones.
	MaxTotalBytes int
	// MaxDeferredSchemaBytes rejects a tool loaded on demand whose schema is
	// larger (default 64 KiB); MaxDeferredTools caps how many tools load on
	// demand, over all servers (default 1024).
	MaxDeferredSchemaBytes int
	MaxDeferredTools       int
	// NoDefer drops what does not fit, with a warning, instead of loading it on
	// demand.
	NoDefer bool
	// AllowSuspicious keeps tools whose description or schema strings trip the
	// injection tripwire; by default they are excluded with a warning.
	AllowSuspicious bool
}

// defaults supplies positive MCP snapshot limits for descriptions, schemas, tool counts, and total
// bytes.
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
	if o.MaxDeferredSchemaBytes <= 0 {
		o.MaxDeferredSchemaBytes = 64 << 10
	}
	if o.MaxDeferredSchemaBytes < o.MaxSchemaBytes {
		o.MaxDeferredSchemaBytes = o.MaxSchemaBytes
	}
	if o.MaxDeferredTools <= 0 {
		o.MaxDeferredTools = 1024
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
	specs    []core.ToolSpec // the tool list: inline tools and, when any tool is deferred, mcp__search and mcp__call
	hash     core.Hash
	warnings []string
	notes    []string
	bytes    int
	tools    []*mcpTool // the inline tools
	deferred []*mcpTool // the tools loaded on demand, sorted by name
	meta     []tools.Tool
}

// Specs returns a copy of the tool specs, sorted by name.
func (s *Snapshot) Specs() []core.ToolSpec {
	out := make([]core.ToolSpec, len(s.specs))
	for i, sp := range s.specs {
		out[i] = cloneSpec(sp)
	}
	return out
}

// cloneSpec copies a tool specification and allocates independent input-schema bytes.
func cloneSpec(sp core.ToolSpec) core.ToolSpec {
	sp.InputSchema = append(json.RawMessage(nil), sp.InputSchema...)
	return sp
}

// Hash identifies the snapshot's model-visible content (names, descriptions,
// schemas, of the tool list and of the tools loaded on demand): equal hashes
// mean an identical prefix and identical search results.
func (s *Snapshot) Hash() core.Hash { return s.hash }

// Warnings lists what was dropped or altered, and why, in a stable order.
func (s *Snapshot) Warnings() []string { return append([]string(nil), s.warnings...) }

// Notes lists what is worth knowing but lost nothing, such as which tools load
// on demand, in a stable order.
func (s *Snapshot) Notes() []string { return append([]string(nil), s.notes...) }

// Len is the number of tools in the tool list.
func (s *Snapshot) Len() int { return len(s.specs) }

// Bytes is the size of the model-visible content of the tool list.
func (s *Snapshot) Bytes() int { return s.bytes }

// Names lists the names in the tool list in order.
func (s *Snapshot) Names() []string {
	out := make([]string, len(s.specs))
	for i, sp := range s.specs {
		out[i] = sp.Name
	}
	return out
}

// Deferred lists the names of the tools loaded on demand, sorted.
func (s *Snapshot) Deferred() []string {
	out := make([]string, len(s.deferred))
	for i, t := range s.deferred {
		out[i] = t.spec.Name
	}
	return out
}

// Tools returns the tools of this snapshot's tool list, ready to register.
// Each carries its frozen spec and runs against the live connection of its
// server.
func (s *Snapshot) Tools() []tools.Tool {
	out := make([]tools.Tool, 0, len(s.tools)+len(s.meta))
	for _, t := range s.tools {
		out = append(out, t)
	}
	out = append(out, s.meta...)
	sort.Slice(out, func(i, j int) bool { return out[i].Spec().Name < out[j].Spec().Name })
	return out
}

// Lookup resolves the name of a server tool, in the tool list or loaded on
// demand, to its server and the tool's own name.
func (s *Snapshot) Lookup(name string) (server, tool string, ok bool) {
	for _, list := range [][]*mcpTool{s.tools, s.deferred} {
		if t := lookupTool(list, name); t != nil {
			return t.server, t.tool, true
		}
	}
	return "", "", false
}

// lookupTool finds name in a list sorted by name.
func lookupTool(list []*mcpTool, name string) *mcpTool {
	i := sort.Search(len(list), func(i int) bool { return list[i].spec.Name >= name })
	if i < len(list) && list[i].spec.Name == name {
		return list[i]
	}
	return nil
}

// Register adds the snapshot's tools to a registry.
func (s *Snapshot) Register(reg *tools.Registry) {
	for _, t := range s.Tools() {
		reg.Register(t)
	}
}

// serverTools is one server's live tool list as input to a snapshot.
type serverTools struct {
	name   string
	cfg    ServerConfig // expanded or not: only filters and the transport type are read
	tools  []Tool
	remote bool
	red    *redactor // masks the credentials the server was configured with
}

// Why a tool loads on demand, in the order a note lists them.
const (
	deferNot    = iota
	deferConfig // its entry sets defer_tools
	deferSchema // its schema is too large or too deep for the tool list
	deferCount  // its server is past its share of the tool count
	deferBytes  // its server is past its share of the byte budget
)

type candidate struct {
	server, tool string
	name         string
	desc         string
	schema       json.RawMessage
	readOnly     bool
	remote       bool
	size         int
	deferred     int // deferNot, or why it loads on demand
}

// measure records the combined byte size of the candidate's name, description, and schema.
func (c *candidate) measure() { c.size = len(c.name) + len(c.desc) + len(c.schema) }

// buildSnapshot is a pure function of its inputs: the same server tool lists and
// options always yield the same snapshot, in any input order.
func buildSnapshot(m *Manager, servers []serverTools, o SnapshotOptions) *Snapshot {
	o.defaults()
	var warns, notes []string
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

	// From here on perServer holds the inline candidates; deferred ones are
	// set aside in name order per server.
	deferredOf := make([][]*candidate, len(perServer))
	for i, cs := range perServer {
		kept := cs[:0]
		for _, c := range cs {
			if c.deferred != deferNot {
				deferredOf[i] = append(deferredOf[i], c)
			} else {
				kept = append(kept, c)
			}
		}
		perServer[i] = kept
	}
	counts := make([]int, len(perServer))
	sizes := make([]int, len(perServer))
	deferring := false
	totalCount, totalSize := 0, 0
	for i, cs := range perServer {
		counts[i] = len(cs)
		for _, c := range cs {
			sizes[i] += c.size
		}
		totalCount += counts[i]
		totalSize += sizes[i]
		deferring = deferring || len(deferredOf[i]) > 0
	}
	// When anything loads on demand, mcp__search and mcp__call are in the list,
	// and their places and bytes come out of the budgets first.
	maxTools, maxBytes, catalogBytes := o.MaxTools, o.MaxTotalBytes, 0
	if !o.NoDefer && (deferring || totalCount > o.MaxTools || totalSize > o.MaxTotalBytes) {
		deferring = true
		catalogBytes = min(maxCatalogBytes, o.MaxTotalBytes/16)
		maxTools = max(0, maxTools-2)
		maxBytes = max(0, maxBytes-metaBaseBytes-catalogBytes)
	}

	// Counts and bytes are budgets shared fairly between servers.
	cut := func(i, n, why int) {
		for _, c := range perServer[i][n:] {
			c.deferred = why
			deferredOf[i] = append(deferredOf[i], c)
		}
		perServer[i] = perServer[i][:n]
		sizes[i] = 0
		for _, c := range perServer[i] {
			sizes[i] += c.size
		}
	}
	countCap := waterFill(counts, maxTools)
	for i, cs := range perServer {
		if len(cs) > countCap[i] {
			if o.NoDefer {
				warn("server %q: %d tools exceed its share of the %d tool limit; the last %d (by name) were dropped", servers[i].name, len(cs), o.MaxTools, len(cs)-countCap[i])
			}
			cut(i, countCap[i], deferCount)
		}
	}
	byteCap := waterFill(sizes, maxBytes)
	for i, cs := range perServer {
		if sizes[i] <= byteCap[i] {
			continue
		}
		total, n := 0, 0
		for n < len(cs) && total+cs[n].size <= byteCap[i] {
			total += cs[n].size
			n++
		}
		if o.NoDefer {
			warn("server %q: tool definitions need %d bytes but its share of the %d byte budget is %d; the last %d (by name) were dropped",
				servers[i].name, sizes[i], o.MaxTotalBytes, byteCap[i], len(cs)-n)
		}
		cut(i, n, deferBytes)
	}

	var all, later []*candidate
	for i, cs := range perServer {
		all = append(all, cs...)
		if !o.NoDefer {
			later = append(later, deferredOf[i]...)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].name < all[j].name })
	sort.Slice(later, func(i, j int) bool { return later[i].name < later[j].name })
	if len(later) > o.MaxDeferredTools {
		for _, c := range later[o.MaxDeferredTools:] {
			warn("server %q: tool %q dropped: more than %d tools would load on demand", c.server, c.tool, o.MaxDeferredTools)
		}
		later = later[:o.MaxDeferredTools]
	}
	notes = deferNotes(servers, perServer, later)

	snap := &Snapshot{tools: make([]*mcpTool, len(all))}
	for i, c := range all {
		spec := core.ToolSpec{Name: c.name, Description: c.desc, InputSchema: c.schema, ReadOnly: c.readOnly}
		snap.specs = append(snap.specs, spec)
		snap.bytes += c.size
		snap.tools[i] = &mcpTool{m: m, server: c.server, tool: c.tool, spec: spec, remote: c.remote}
	}
	for _, c := range later {
		spec := core.ToolSpec{Name: c.name, Description: c.desc, InputSchema: c.schema, ReadOnly: c.readOnly}
		snap.deferred = append(snap.deferred, &mcpTool{m: m, server: c.server, tool: c.tool, spec: spec, remote: c.remote})
	}
	if len(snap.deferred) > 0 {
		snap.meta = metaTools(snap, catalogBytes)
		for _, t := range snap.meta {
			sp := t.Spec()
			snap.specs = append(snap.specs, sp)
			snap.bytes += len(sp.Name) + len(sp.Description) + len(sp.InputSchema)
		}
		sort.Slice(snap.specs, func(i, j int) bool { return snap.specs[i].Name < snap.specs[j].Name })
	}
	// The hash covers exactly what the model sees, in the list or on demand.
	// ReadOnly is a scheduling hint (json:"-") and rightly stays out of it.
	b, _ := core.MarshalStable(snap.specs)
	if len(snap.deferred) > 0 {
		d := make([]core.ToolSpec, len(snap.deferred))
		for i, t := range snap.deferred {
			d[i] = t.spec
		}
		more, _ := core.MarshalStable(d)
		b = append(append(b, '\n'), more...)
	}
	snap.hash = core.HashBytes(b)
	sort.Strings(warns)
	snap.warnings = warns
	snap.notes = notes
	return snap
}

// deferNotes says, one line per server, how many of its tools load on demand
// and why.
func deferNotes(servers []serverTools, inline [][]*candidate, later []*candidate) []string {
	why := map[string]*[5]int{}
	for _, c := range later {
		if why[c.server] == nil {
			why[c.server] = &[5]int{}
		}
		why[c.server][c.deferred]++
	}
	var notes []string
	for i, sv := range servers {
		w := why[sv.name]
		if w == nil {
			continue
		}
		n := w[deferConfig] + w[deferSchema] + w[deferCount] + w[deferBytes]
		var parts []string
		if w[deferConfig] > 0 {
			parts = append(parts, "its entry sets defer_tools")
		}
		if k := w[deferSchema]; k > 0 {
			parts = append(parts, fmt.Sprintf("%d with input schemas too large or too deep for the tool list", k))
		}
		if k := w[deferCount] + w[deferBytes]; k > 0 {
			parts = append(parts, fmt.Sprintf("%d past its share of the tool list's budget", k))
		}
		notes = append(notes, fmt.Sprintf("server %q: %d of its %d tools load on demand through mcp__search and mcp__call (%s)",
			sv.name, n, n+len(inline[i]), strings.Join(parts, "; ")))
	}
	return notes
}

// collisionList formats colliding server/tool names in lexical order.
func collisionList(group []*candidate) string {
	names := make([]string, len(group))
	for i, c := range group {
		names[i] = c.server + "/" + c.tool
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// serverCandidates vets one server's tools: usable definition, allowed by the
// entry's filters, no duplicate names, clean description and schema. A tool
// whose schema fits only the limits of a tool loaded on demand is marked so.
func serverCandidates(sv serverTools, o SnapshotOptions, warn func(string, ...any)) []*candidate {
	list := append([]Tool(nil), sv.tools...)
	sort.SliceStable(list, func(i, j int) bool { return list[i].Name < list[j].Name })

	counts := map[string]int{}
	for _, t := range list {
		counts[t.Name]++
	}
	limit := o.MaxToolsPerServer
	lim := schemaLimits{bytes: o.MaxSchemaBytes, depth: maxSchemaDepth, nodes: maxSchemaNodes}
	if !o.NoDefer {
		limit += o.MaxDeferredTools
		lim = schemaLimits{bytes: o.MaxDeferredSchemaBytes, depth: maxDeferredSchemaDepth, nodes: maxDeferredSchemaNodes, compact: true}
	}
	var out []*candidate
	reported := map[string]bool{}
	capped := false
	inline := 0
	for _, t := range list {
		if len(out) >= limit {
			// Only the first usable tools (by name) are offered, so the rest need
			// not be vetted: a server listing thousands must not make every
			// snapshot cost thousands of sanitisations and regex scans.
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
		desc = sv.red.apply(desc) // the model reads it, as it reads a result, where the credential is masked too
		n, err := normalizeSchemaWithin(t.InputSchema, lim)
		if err != nil {
			warn("server %q: tool %q excluded: %v", sv.name, label, err)
			continue
		}
		if sv.red.inJSON(n.schema) {
			warn("server %q: tool %q excluded: its input schema holds a credential the server was configured with", sv.name, label)
			continue
		}
		if n.injection != "" && !o.AllowSuspicious {
			warn("server %q: tool %q excluded: its input schema %s", sv.name, label, n.injection)
			continue
		}
		deferred := deferNot
		switch {
		case !n.inline(o.MaxSchemaBytes):
			deferred = deferSchema
		case sv.cfg.DeferTools && !o.NoDefer:
			deferred = deferConfig
		case inline >= o.MaxToolsPerServer:
			deferred = deferCount
		default:
			inline++
		}
		desc, _ = truncateRunes(desc, o.MaxDescriptionChars)
		out = append(out, &candidate{
			server: sv.name, tool: t.Name, desc: desc, schema: n.schema,
			readOnly: t.Annotations.ReadOnly(), remote: sv.remote, deferred: deferred,
		})
	}
	if capped {
		warn("server %q offers more than %d usable tools; only the first %d (by name) are offered", sv.name, limit, limit)
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
