package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// Tools loaded on demand.
//
// Some servers offer more than the tool list can carry: hundreds of tools, or
// schemas of tens of kilobytes. Every byte of the list is paid on every request
// of every agent, and the list is frozen, so it cannot grow when a tool is
// wanted. What does not fit is therefore kept out of the list and reached
// through two fixed tools that are in it:
//
//	mcp__search  finds tools by words or exact names, and returns each one's
//	             name, description and full input schema as a tool result;
//	mcp__call    runs one of them with arguments that follow that schema.
//
// The tools are vetted exactly like the ones in the list (sanitised, tripwire,
// no remote $ref, canonical JSON), only under larger limits, and frozen in the
// same snapshot: what mcp__search returns never changes under a session. A call
// through mcp__call is the tool's own call: the same permission request, under
// the tool's own name, so a rule written for mcp__server__tool applies to it.
// Hooks see the call as mcp__call, with the tool's name in its input.
const (
	searchToolName = "mcp__search"
	callToolName   = "mcp__call"

	// maxCatalogBytes caps the list of on-demand tool names in mcp__search's
	// description (and so in the prefix); the rest are found by searching.
	maxCatalogBytes = 4 << 10
	// searchDefault and searchMax bound how many tools one search returns.
	searchDefault = 5
	searchMax     = 20
)

const searchDesc = "Find MCP tools that are not in your tool list: they load on demand. Returns each match's name, description and input schema; " +
	"run one with " + callToolName + ". Search with words describing what you need (\"compare captures\"), or load exact names with \"select:name1,name2\"."

const callDesc = "Run an MCP tool that loads on demand, by its full name as " + searchToolName + " returns it. " +
	"Pass its arguments as an object that follows the input schema " + searchToolName + " showed; search first if you have not seen it."

var searchSchema = json.RawMessage(`{"properties":{"limit":{"description":"At most this many tools (default 5, at most 20).","type":"integer"},` +
	`"query":{"description":"Words to match against tool names and descriptions, or select: followed by comma-separated exact names.","type":"string"}},"required":["query"],"type":"object"}`)

var callSchema = json.RawMessage(`{"properties":{"arguments":{"description":"The tool's arguments, following its input schema.","type":"object"},` +
	`"tool":{"description":"The tool's full name, mcp__server__tool.","type":"string"}},"required":["tool","arguments"],"type":"object"}`)

// metaBaseBytes is what mcp__search and mcp__call cost in the tool list before
// the catalog: reserved from the byte budget whenever anything loads on demand.
var metaBaseBytes = len(searchToolName) + len(searchDesc) + len(searchSchema) + len(callToolName) + len(callDesc) + len(callSchema) +
	len("\n\nOn demand: ")

// metaTools builds mcp__search and mcp__call for a snapshot whose deferred tools
// are set; catalog bounds the names listed in mcp__search's description.
func metaTools(s *Snapshot, catalog int) []tools.Tool {
	desc := searchDesc
	if names := catalogOf(s.deferred, catalog); names != "" {
		desc += "\n\nOn demand: " + names
	}
	return []tools.Tool{
		&searchTool{snap: s, spec: core.ToolSpec{Name: searchToolName, Description: desc, InputSchema: searchSchema, ReadOnly: true}},
		&callTool{snap: s, spec: core.ToolSpec{Name: callToolName, Description: callDesc, InputSchema: callSchema}},
	}
}

// catalogOf lists the deferred names within limit bytes, grouped by their
// mcp__server__ prefix ("mcp__rea__{a, b}"), and says how many more there are.
func catalogOf(list []*mcpTool, limit int) string {
	var b strings.Builder
	open, shown := "", 0
	for _, t := range list { // sorted by name, so a prefix's names are adjacent
		prefix, rest := "", t.spec.Name
		if srv, tool, ok := ParseName(t.spec.Name); ok {
			prefix, rest = toolPrefix+srv+"__", tool
		}
		piece := ", " + rest
		if shown == 0 || prefix != open {
			piece = prefix + "{" + rest
			if shown > 0 {
				piece = "}; " + piece
			}
		}
		if b.Len()+len(piece)+48 > limit { // room for the closing brace and the tail
			break
		}
		b.WriteString(piece)
		open = prefix
		shown++
	}
	if shown > 0 {
		b.WriteString("}")
	}
	if more := len(list) - shown; more > 0 && shown > 0 {
		fmt.Fprintf(&b, " and %d more (search for them)", more)
	}
	return b.String()
}

// searchTool is mcp__search.
type searchTool struct {
	snap *Snapshot
	spec core.ToolSpec
}

// Spec implements tools.Tool.
func (t *searchTool) Spec() core.ToolSpec { return cloneSpec(t.spec) }

// Run implements tools.Tool. It reads only the frozen snapshot: nothing is
// sent to a server, so it asks no permission.
func (t *searchTool) Run(_ context.Context, c *tools.Call) (*tools.Result, error) {
	var in struct {
		Query string          `json:"query"`
		Limit json.RawMessage `json:"limit"`
	}
	if err := json.Unmarshal(orEmpty(c.Input), &in); err != nil {
		return tools.Errorf("%s takes {\"query\": \"...\"}: %v", searchToolName, err), nil
	}
	limit := searchDefault
	if len(in.Limit) > 0 && string(in.Limit) != "null" {
		var n float64
		if err := json.Unmarshal(bytes.Trim(in.Limit, `"`), &n); err != nil || n < 1 {
			return tools.Errorf("limit must be a positive integer"), nil
		}
		limit = min(int(n), searchMax)
	}
	query := strings.TrimSpace(in.Query)
	if query == "" {
		return tools.Errorf("query is empty: describe the tool you need, or use select:name"), nil
	}
	var found []*mcpTool
	var missing []string
	if names, ok := strings.CutPrefix(query, "select:"); ok {
		for _, n := range strings.Split(names, ",") {
			if n = strings.TrimSpace(n); n == "" {
				continue
			}
			if tl := t.snap.resolveDeferred(n); tl != nil {
				found = append(found, tl)
			} else {
				missing = append(missing, n)
			}
		}
		found = found[:min(len(found), searchMax)]
	} else {
		found = rankDeferred(t.snap.deferred, query, limit)
	}

	budget := 24_000
	if c.Env != nil && c.Env.Limits.MaxOutputChars > 0 {
		budget = c.Env.Limits.MaxOutputChars
	}
	var b strings.Builder
	if len(found) == 0 {
		fmt.Fprintf(&b, "No on-demand tool matches %q.", oneLine(query, 200))
	}
	var brief []*mcpTool
	for i, tl := range found {
		entry := fmt.Sprintf("%s\n%s\ninput schema: %s\n\n", tl.spec.Name, tl.spec.Description, tl.spec.InputSchema)
		// The first match is always shown whole (a cut schema is useless); the
		// others while they fit the output limit.
		if i > 0 && b.Len()+len(entry) > budget {
			brief = append(brief, tl)
			continue
		}
		b.WriteString(entry)
	}
	if len(brief) > 0 {
		b.WriteString("Also matching (load with select:name to see the schema):\n")
		for _, tl := range brief {
			d, _ := truncateRunes(oneLine(tl.spec.Description, 200), 120)
			fmt.Fprintf(&b, "- %s: %s\n", tl.spec.Name, d)
		}
	}
	for _, n := range missing {
		if _, _, ok := t.snap.Lookup(n); ok {
			fmt.Fprintf(&b, "%s is in your tool list: call it directly.\n", oneLine(n, 100))
		} else {
			fmt.Fprintf(&b, "%s: no such on-demand tool.\n", oneLine(n, 100))
		}
	}
	if len(found) > 0 {
		fmt.Fprintf(&b, "Run one with %s {\"tool\": \"<name>\", \"arguments\": {...}}.", callToolName)
	} else {
		fmt.Fprintf(&b, " %d tools load on demand; try other words.", len(t.snap.deferred))
	}
	return &tools.Result{Text: strings.TrimSpace(b.String()), IsError: len(found) == 0}, nil
}

// resolveDeferred finds a deferred tool by its exposed name, or by the server's
// own name for it when that is unambiguous.
func (s *Snapshot) resolveDeferred(name string) *mcpTool {
	if tl := lookupTool(s.deferred, name); tl != nil {
		return tl
	}
	var hit *mcpTool
	for _, tl := range s.deferred {
		if tl.tool == name {
			if hit != nil {
				return nil
			}
			hit = tl
		}
	}
	return hit
}

// rankDeferred orders the deferred tools by how well they match the words of
// query: a word in the tool's name counts three times one in its description.
// Ties keep name order, so a search is deterministic.
func rankDeferred(list []*mcpTool, query string, limit int) []*mcpTool {
	words := searchWords(query)
	if len(words) == 0 {
		return nil
	}
	type hit struct {
		t     *mcpTool
		score int
	}
	var hits []hit
	for _, tl := range list {
		name := strings.Join(searchWords(tl.spec.Name), " ")
		desc := strings.ToLower(tl.spec.Description)
		score := 0
		for _, w := range words {
			switch {
			case strings.Contains(name, w):
				score += 3
			case strings.Contains(desc, w):
				score++
			}
		}
		if score > 0 {
			hits = append(hits, hit{tl, score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	out := make([]*mcpTool, 0, min(limit, len(hits)))
	for _, h := range hits[:min(limit, len(hits))] {
		out = append(out, h.t)
	}
	return out
}

// searchWords splits text into lower-case words on anything but letters and
// digits, so "compare_web_captures" and "compare web captures" match.
func searchWords(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r > 127)
	})
}

// callTool is mcp__call.
type callTool struct {
	snap *Snapshot
	spec core.ToolSpec
}

// Spec implements tools.Tool.
func (t *callTool) Spec() core.ToolSpec { return cloneSpec(t.spec) }

// Run implements tools.Tool: it runs the named tool exactly as if the model
// had called it by name, permission request included.
func (t *callTool) Run(ctx context.Context, c *tools.Call) (*tools.Result, error) {
	var in struct {
		Tool      string          `json:"tool"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(orEmpty(c.Input), &in); err != nil {
		return tools.Errorf("%s takes {\"tool\": \"mcp__server__tool\", \"arguments\": {...}}: %v", callToolName, err), nil
	}
	name := strings.TrimSpace(in.Tool)
	if name == "" {
		return tools.Errorf("tool is empty: pass the full name %s returned", searchToolName), nil
	}
	target := t.snap.resolveDeferred(name)
	if target == nil {
		target = lookupTool(t.snap.tools, name) // in the list already: run it all the same
	}
	if target == nil {
		return tools.Errorf("no MCP tool named %q; find it with %s", oneLine(name, 100), searchToolName), nil
	}
	args := bytes.TrimSpace(in.Arguments)
	if len(args) > 0 && args[0] == '"' {
		// Models sometimes send the object as a JSON string: accept it when it is one.
		var s string
		if json.Unmarshal(args, &s) == nil && json.Valid([]byte(s)) {
			args = bytes.TrimSpace([]byte(s))
		}
	}
	return target.Run(ctx, &tools.Call{ID: c.ID, Name: target.spec.Name, Input: args, Env: c.Env})
}

// orEmpty is input, or an empty object when the model sent nothing.
func orEmpty(input json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(input)) == 0 {
		return json.RawMessage("{}")
	}
	return input
}
