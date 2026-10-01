package fs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/tools"
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
	depth    int // 0 for the listed directory itself, 1 for its children
	parent   int // index into the node list; -1 for the root
	children int // admitted entries inside (for collapsed directories: counted, not listed)
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

	defer k.bounded()()
	w := k.newWalker(base, true)
	if extra := compileIgnoreList(a.Ignore); extra != nil {
		w.exclude = func(segs []string, isDir bool) bool {
			ig, ok := extra.decide(segs, isDir)
			return ok && ig
		}
	}

	nodes := []lsNode{{name: disp, kind: kindDir, depth: 0, parent: -1, open: true}}
	capped := false
	var build func(dir string, rel []string, parent int)
	build = func(dir string, rel []string, parent int) {
		entries, done := w.list(dir, rel)
		defer done()
		// Directories first, then files, each by name: stable and scannable.
		sort.SliceStable(entries, func(i, j int) bool {
			di, dj := entries[i].kind == kindDir, entries[j].kind == kindDir
			if di != dj {
				return di
			}
			return entries[i].dir.Name() < entries[j].dir.Name()
		})
		nodes[parent].children = len(entries)
		for _, e := range entries {
			if len(nodes) > maxLSNodes || k.ctx.Err() != nil {
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
	build(base, nil, 0)

	// When there are more nodes than the cap, print shallow levels first (a
	// listing that shows every top-level entry and a few deep ones beats one
	// that shows a single subtree completely), then in tree order. A node's
	// parent is always shallower, so the selection is closed under ancestors.
	selected := make([]bool, len(nodes))
	selected[0] = true
	order := make([]int, 0, len(nodes)-1)
	for i := 1; i < len(nodes); i++ {
		order = append(order, i)
	}
	sort.SliceStable(order, func(x, y int) bool { return nodes[order[x]].depth < nodes[order[y]].depth })
	shownCount := 0
	for _, i := range order {
		if shownCount >= maxLSEntries {
			break
		}
		selected[i] = true
		shownCount++
		nodes[nodes[i].parent].shown++
	}

	var sb strings.Builder
	var open []int // listed directories whose children are still being printed
	closeDir := func(i int) {
		if n := nodes[i].children - nodes[i].shown; n > 0 {
			sb.WriteString("\n" + strings.Repeat("  ", nodes[i].depth+1) + fmt.Sprintf("… %d more", n))
		}
	}
	closeTo := func(depth int) {
		for len(open) > 0 && nodes[open[len(open)-1]].depth >= depth {
			closeDir(open[len(open)-1])
			open = open[:len(open)-1]
		}
	}
	sb.WriteString(strings.TrimSuffix(disp, "/") + "/")
	open = append(open, 0)
	for i := 1; i < len(nodes); i++ {
		if !selected[i] {
			continue
		}
		n := nodes[i]
		closeTo(n.depth)
		sb.WriteString("\n" + strings.Repeat("  ", n.depth) + safeName(n.name))
		switch n.kind {
		case kindDir:
			sb.WriteByte('/')
			// A directory none of whose entries made the cut reads like a
			// collapsed one: the count says what is inside without a separate
			// "… N more" line per directory.
			if n.open && (n.shown > 0 || n.children == 0) {
				open = append(open, i)
			} else if n.children > 0 {
				fmt.Fprintf(&sb, " (%s)", plural(n.children, "item"))
			}
		case kindLink:
			sb.WriteByte('@')
		}
	}
	closeTo(0)
	switch {
	case capped:
		fmt.Fprintf(&sb, "\n[%d entries shown; the tree is too large to list fully. Pass a subdirectory as path]", shownCount)
	case shownCount < len(nodes)-1:
		fmt.Fprintf(&sb, "\n[%d of %d entries shown; pass a subdirectory as path or a smaller depth]", shownCount, len(nodes)-1)
	}
	if k.ctx.Err() != nil {
		sb.WriteString("\n[listing cancelled or timed out; it may be incomplete]")
	}
	if w.unreadable > 0 {
		fmt.Fprintf(&sb, "\n[%s could not be read]", plural(w.unreadable, "directory"))
	}
	return k.ok(sb.String()), nil
}

func compileIgnoreList(patterns []string) *ignoreFile {
	f := &ignoreFile{}
	for _, p := range patterns {
		if len(p) > maxPatternBytes {
			continue
		}
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
