package fs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/perm"
	"github.com/reee344/sleipnir/internal/tools"
)

// LS implements the ls tool.
type LS struct{}

// Spec implements tools.Tool.
func (LS) Spec() core.ToolSpec {
	return core.ToolSpec{
		Name: "ls",
		Description: "List a directory as a compact tree: directories end with /, directories come first, " +
			"symlinks end with @. `depth` defaults to 2. Respects .gitignore and skips .git; `ignore` adds glob patterns to hide. " +
			"Output is capped at about 300 entries, shallow levels first.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"path":{"type":"string","description":"Directory (default: working directory)"},` +
			`"depth":{"type":"integer","description":"Levels to show (default 2)"},` +
			`"ignore":{"type":"array","items":{"type":"string"},"description":"Extra glob patterns to hide"}}}`),
		ReadOnly: true,
	}
}

const (
	defaultLSDepth = 2
	maxLSDepth     = 12
	maxLSEntries   = 300
	maxLSNodes     = 5000
)

type lsNode struct {
	name     string
	kind     walkKind
	depth    int // 1 for direct children of the listed directory
	parent   int // index in the node list, -1 for the root's children
	children int // admitted children found (collapsed directories: counted)
	shown    int // children selected for display
	open     bool
}

// Run implements tools.Tool.
func (LS) Run(ctx context.Context, c *tools.Call) (*tools.Result, error) {
	k := begin(ctx, c, "ls")
	var a struct {
		Path   string   `json:"path"`
		Depth  intArg   `json:"depth"`
		Ignore []string `json:"ignore"`
	}
	if r := k.decode(&a); r != nil {
		return r, nil
	}
	depth := defaultLSDepth
	if a.Depth.Set {
		if a.Depth.V < 1 {
			return k.fail("depth must be at least 1"), nil
		}
		depth = a.Depth.V
	}
	if depth > maxLSDepth {
		depth = maxLSDepth
	}
	path := a.Path
	if path == "" {
		path = "."
	}
	base, disp, msg := k.resolveArg(path)
	if msg != "" {
		return k.fail("%s", msg), nil
	}
	if r := k.authorize("list "+disp, false, perm.RiskLow, base); r != nil {
		return r, nil
	}
	fi, err := os.Stat(base)
	if err != nil {
		return k.statFailure(base, disp, err), nil
	}
	if !fi.IsDir() {
		return k.ok(fmt.Sprintf("%s (file, %s)", disp, humanBytes(fi.Size()))), nil
	}

	w := k.newWalker(base, true)
	if extra := compileIgnoreList(a.Ignore); extra != nil {
		w.exclude = func(segs []string, isDir bool) bool {
			ig, ok := extra.decide(segs, isDir)
			return ok && ig
		}
	}

	var nodes []lsNode
	capped := false
	var build func(dir string, rel []string, parent int)
	build = func(dir string, rel []string, parent int) {
		entries, done := w.list(dir, rel)
		defer done()
		sort.SliceStable(entries, func(i, j int) bool {
			di, dj := entries[i].kind == kindDir, entries[j].kind == kindDir
			if di != dj {
				return di
			}
			return entries[i].dir.Name() < entries[j].dir.Name()
		})
		if parent >= 0 {
			nodes[parent].children = len(entries)
		}
		for _, e := range entries {
			if len(nodes) >= maxLSNodes {
				capped = true
				return
			}
			if k.ctx.Err() != nil {
				capped = true
				return
			}
			idx := len(nodes)
			nodes = append(nodes, lsNode{name: e.dir.Name(), kind: e.kind, depth: len(rel) + 1, parent: parent})
			if e.kind != kindDir {
				continue
			}
			if len(rel)+1 < depth {
				nodes[idx].open = true
				build(e.path, e.segs, idx)
				if capped {
					return
				}
			} else {
				// At the depth limit: count what is inside so "pkg/ (5 items)"
				// tells the model whether going deeper is worthwhile.
				sub, subDone := w.list(e.path, e.segs)
				nodes[idx].children = len(sub)
				subDone()
			}
		}
	}
	build(base, nil, -1)

	// Choose what to print when there are more nodes than the cap: shallow
	// levels first (an orientation listing that shows every top-level entry and
	// a few deep ones beats one that shows one subtree completely), then in
	// tree order.
	selected := make([]bool, len(nodes))
	order := make([]int, len(nodes))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(x, y int) bool { return nodes[order[x]].depth < nodes[order[y]].depth })
	shownCount := 0
	for _, i := range order {
		if shownCount >= maxLSEntries {
			break
		}
		selected[i] = true
		shownCount++
		if p := nodes[i].parent; p >= 0 {
			nodes[p].shown++
		}
	}

	var sb strings.Builder
	sb.WriteString(strings.TrimSuffix(disp, "/") + "/")
	// closeGroup prints "… N more" for the parents whose listing was cut.
	var emitMore func(parent int, depth int)
	emitMore = func(parent int, depth int) {
		if parent < 0 {
			return
		}
		if n := nodes[parent].children - nodes[parent].shown; n > 0 && nodes[parent].open {
			sb.WriteString("\n" + strings.Repeat("  ", depth) + fmt.Sprintf("… %d more", n))
		}
	}
	// Walk the nodes in tree order; a run of children ends when the depth drops.
	var stack []int // open ancestors, for emitting "… N more" after their last shown child
	flushTo := func(depth int) {
		for len(stack) > 0 && nodes[stack[len(stack)-1]].depth >= depth {
			top := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			emitMore(top, nodes[top].depth+1)
		}
	}
	for i, n := range nodes {
		if !selected[i] {
			continue
		}
		flushTo(n.depth)
		sb.WriteString("\n" + strings.Repeat("  ", n.depth) + n.name)
		switch n.kind {
		case kindDir:
			sb.WriteByte('/')
			if !n.open && n.children > 0 {
				fmt.Fprintf(&sb, " (%s)", plural(n.children, "item"))
			}
			if n.open {
				stack = append(stack, i)
			}
		case kindLink:
			sb.WriteByte('@')
		}
	}
	flushTo(0)
	// Top level: entries cut by the cap.
	if top := countTop(nodes); top.total > top.shown {
		sb.WriteString(fmt.Sprintf("\n… %d more", top.total-top.shown))
	}
	if omitted := len(nodes) - shownCount; omitted > 0 || capped {
		if capped {
			sb.WriteString(fmt.Sprintf("\n[%d entries shown; the tree is larger than can be listed. Pass a subdirectory as path]", shownCount))
		} else {
			sb.WriteString(fmt.Sprintf("\n[%d of %d entries shown; pass a subdirectory as path or a smaller depth]", shownCount, len(nodes)))
		}
	}
	if w.cancelled {
		sb.WriteString("\n[listing cancelled]")
	}
	return k.ok(sb.String()), nil
}

type topCount struct{ total, shown int }

func countTop(nodes []lsNode) topCount {
	var t topCount
	for _, n := range nodes {
		if n.parent < 0 {
			t.total++
		}
	}
	return t
}

func compileIgnoreList(patterns []string) *ignoreFile {
	f := &ignoreFile{}
	for _, p := range patterns {
		alts, ok := expandBraces(strings.TrimSpace(p), maxBraceAlternatives)
		if !ok {
			continue
		}
		for _, alt := range alts {
			if r, ok := parseIgnoreLine(alt); ok {
				f.rules = append(f.rules, r)
			}
		}
	}
	if len(f.rules) == 0 {
		return nil
	}
	return f
}
