package config

import (
	"encoding/json"
	"reflect"
	"slices"
	"sort"
	"strings"
)

// layer is one source of configuration after parsing and checking.
type layer struct {
	kind   string // "user", "project", "local", "env" or "overrides"
	source string // file path, "env:NAME" or "overrides": what Report names
	found  bool   // the file exists (always true for env and overrides)
	src    []byte // document text, for positions; nil when it has none
	root   *node  // parsed document (positions valid when src != nil)
	tree   map[string]any
}

// position fills an issue's line and column from where its path sits in this
// layer's document. Findings about a setting being present point at its key;
// findings about a value point at the value.
func (l *layer) position(is *Issue) { l.positionAt(is, true) }

// positionAt fills an issue's source line and column from its path, optionally preferring the
// property key, and leaves unresolvable positions unchanged.
func (l *layer) positionAt(is *Issue, preferKey bool) {
	if l.root == nil || l.src == nil {
		return
	}
	n, keyOff, ok := l.root.locate(is.segs)
	if !ok {
		return
	}
	off := n.off
	if preferKey && keyOff >= 0 {
		off = keyOff
	}
	if off >= 0 {
		is.Line, is.Col = lineCol(l.src, off)
	}
}

// merger folds layers together, lowest precedence first, remembering which
// layer supplied each value.
type merger struct {
	tree    map[string]any
	origins map[string]string // path (segments joined by \x00) -> source
	// under counts the origin records strictly below each path, so forget leaves a path nothing was recorded under at once.
	under   map[string]int
	sources map[string]string // top-level key -> source
	kind    string            // kind of the layer being merged
}

// newMerger initializes independent maps for merged values, origins, and source texts.
func newMerger() *merger {
	return &merger{tree: map[string]any{}, origins: map[string]string{}, under: map[string]int{}, sources: map[string]string{}}
}

// okey joins field-path segments with NUL separators for internal origin-map keys. A segment is a key of the file, which may
// hold a NUL itself: each is escaped first, so no two paths share a key and a null at one cannot drop another's origin.
func okey(segs []string) string {
	esc := make([]string, len(segs))
	for i, s := range segs {
		esc[i] = keyEscaper.Replace(s)
	}
	return strings.Join(esc, "\x00")
}

// unkey is the inverse of okey.
func unkey(k string) []string {
	segs := strings.Split(k, "\x00")
	for i, s := range segs {
		segs[i] = keyUnescaper.Replace(s)
	}
	return segs
}

var (
	keyEscaper   = strings.NewReplacer(`\`, `\\`, "\x00", `\0`)
	keyUnescaper = strings.NewReplacer(`\\`, `\`, `\0`, "\x00")
)

// opaque reports whether the value at segs is treated as a unit that replaces
// rather than merges: entries of hooks and mcp, whose shape this package does
// not know, and top-level keys it does not know at all.
func opaque(segs []string) bool {
	switch len(segs) {
	case 1:
		return !knownKey(segs[0])
	case 2:
		return segs[0] == "hooks" || segs[0] == "mcp"
	}
	return false
}

// additive reports whether the value at segs is a list that layers supplied by a
// repository may only add to: the user's deny and ask rules (a project may always
// add restrictions, but must not be able to remove the user's by replacing the
// list, emptying it or setting it to null) and the entries of hooks (a trusted
// project's hooks run after the user's, they do not switch them off).
func additive(segs []string) bool {
	switch len(segs) {
	case 2:
		return (segs[0] == "permissions" && (segs[1] == "deny" || segs[1] == "ask")) || segs[0] == "hooks"
	case 4:
		return segs[0] == "permissions" && segs[1] == "roles" && (segs[3] == "deny" || segs[3] == "ask")
	}
	return false
}

// holdsAdditive reports whether the value at segs is a section that contains additive lists: permissions, permissions.roles, one
// role's section and hooks. A repository may add inside such a section but must not unset it or replace it by a scalar or a list,
// because that would take the user's deny and ask rules or hooks with it.
func holdsAdditive(segs []string) bool {
	switch len(segs) {
	case 1:
		return segs[0] == "permissions" || segs[0] == "hooks"
	case 2:
		return segs[0] == "permissions" && segs[1] == "roles"
	case 3:
		return segs[0] == "permissions" && segs[1] == "roles"
	}
	return false
}

// repoSupplied reports whether the layer being merged arrives with a repository.
func (m *merger) repoSupplied() bool { return m.kind == "project" || m.kind == "local" }

// unionList appends the elements of add that are not already in have, keeping the
// order (lower layers first).
// Membership is a set of each element's JSON encoding (object keys sorted, so equal values encode alike), which keeps a list of
// many rules linear to merge.
func unionList(have any, add []any) []any {
	out, _ := have.([]any)
	out = append([]any(nil), out...)
	seen := make(map[string]bool, len(out))
	key := func(v any) (string, bool) {
		b, err := json.Marshal(v)
		return string(b), err == nil
	}
	for _, h := range out {
		if k, ok := key(h); ok {
			seen[k] = true
		}
	}
	for _, a := range add {
		k, ok := key(a)
		if ok && seen[k] {
			continue
		}
		if !ok && slices.ContainsFunc(out, func(h any) bool { return reflect.DeepEqual(h, a) }) {
			continue
		}
		if ok {
			seen[k] = true
		}
		out = append(out, deepCopy(a))
	}
	return out
}

// apply merges one layer over what is already there.
func (m *merger) apply(l *layer) {
	m.kind = l.kind
	m.mergeObject(m.tree, l.tree, nil, l.source)
	for _, k := range sortedKeys(l.tree) {
		if l.tree[k] == nil {
			delete(m.sources, k)
		} else {
			m.sources[k] = l.source
		}
	}
}

func (m *merger) mergeObject(dst, src map[string]any, segs []string, source string) {
	for _, k := range sortedKeys(src) {
		v := src[k]
		p := cloneSegs(segs, k)
		if _, isMap := v.(map[string]any); m.repoSupplied() && holdsAdditive(p) && !isMap {
			continue
		}
		if m.repoSupplied() && additive(p) {
			// A repository adds to these lists and cannot take from them: null and
			// an empty list change nothing, a longer list appends.
			if add, ok := v.([]any); ok && len(add) > 0 {
				dst[k] = unionList(dst[k], add)
				m.forget(p)
				m.record(p, dst[k], source)
			}
			continue
		}
		if v == nil { // null: unset whatever lower layers said
			delete(dst, k)
			m.forget(p)
			continue
		}
		sv, srcMap := v.(map[string]any)
		dv, dstMap := dst[k].(map[string]any)
		if srcMap && dstMap && !opaque(p) {
			m.mergeObject(dv, sv, p, source)
			continue
		}
		dst[k] = deepCopy(v)
		m.forget(p)
		m.record(p, v, source)
	}
}

// record notes where a value came from: every leaf of a merged object, or the
// value itself for scalars, lists and opaque objects.
func (m *merger) record(p []string, v any, source string) {
	if sub, ok := v.(map[string]any); ok && !opaque(p) && len(sub) > 0 {
		for _, k := range sortedKeys(sub) {
			if sub[k] != nil {
				m.record(cloneSegs(p, k), sub[k], source)
			}
		}
		return
	}
	key := okey(p)
	if _, ok := m.origins[key]; !ok {
		m.count(p, 1)
	}
	m.origins[key] = source
}

// count adds n to the records under each path above p.
func (m *merger) count(p []string, n int) {
	for i := 0; i < len(p); i++ {
		m.under[okey(p[:i])] += n
	}
}

// forget drops origin records at and below p.
func (m *merger) forget(p []string) {
	key := okey(p)
	if _, ok := m.origins[key]; ok {
		delete(m.origins, key)
		m.count(p, -1)
	}
	if m.under[key] == 0 {
		return
	}
	for k := range m.origins {
		if strings.HasPrefix(k, key+"\x00") {
			delete(m.origins, k)
			m.count(unkey(k), -1)
		}
	}
}

// originFor finds the source of the value at segs, or of the nearest enclosing
// value that has one.
func (m *merger) originFor(segs []string) string {
	for n := len(segs); n > 0; n-- {
		if s, ok := m.origins[okey(segs[:n])]; ok {
			return s
		}
	}
	return ""
}

// deepCopy recursively copies generic JSON maps and slices while retaining scalar values.
func deepCopy(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = deepCopy(e)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = deepCopy(e)
		}
		return out
	}
	return v
}

// displayOrigins renders the origin table with human-readable paths.
func (m *merger) displayOrigins() map[string]string {
	out := make(map[string]string, len(m.origins))
	for k, src := range m.origins {
		out[fmtPath(unkey(k))] = src
	}
	return out
}

// decode turns the merged tree into a Config laid over the defaults.
func (m *merger) decode() (*Config, error) {
	// No HTML escaping: unknown top-level values are preserved verbatim in
	// Config.Extra and must not be rewritten on the way through.
	b, err := marshalSorted(m.tree)
	if err != nil {
		return nil, err
	}
	cfg := Defaults()
	if err := json.Unmarshal(b, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// layerKeys lists a layer's top-level keys (those it sets, not those it unsets).
func layerKeys(l *layer) []string {
	var keys []string
	for k, v := range l.tree {
		if v != nil {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}
