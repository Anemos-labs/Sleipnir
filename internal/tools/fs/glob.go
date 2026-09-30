package fs

import (
	"context"
	"encoding/json"
	"fmt"
	iofs "io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/perm"
	"github.com/reee344/sleipnir/internal/tools"
)

// Glob implements the glob tool.
type Glob struct{}

// Spec implements tools.Tool.
func (Glob) Spec() core.ToolSpec {
	return core.ToolSpec{
		Name: "glob",
		Description: "Find files by glob pattern, newest first (max 200). Supports **, *, ?, {a,b} and [abc]. " +
			"A pattern without a slash matches only the top level of `path`; use **/*.go to search subdirectories. " +
			"A pattern ending in / matches directories. Respects .gitignore and skips .git. " +
			"`path` defaults to the working directory.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"pattern":{"type":"string","description":"Glob pattern, e.g. **/*.go"},` +
			`"path":{"type":"string","description":"Directory to search (default: working directory)"}},` +
			`"required":["pattern"]}`),
		ReadOnly: true,
	}
}

const maxGlobResults = 200

type globHit struct {
	path  string
	rel   string
	mtime time.Time
	dir   bool
}

// Run implements tools.Tool.
func (Glob) Run(ctx context.Context, c *tools.Call) (*tools.Result, error) {
	k := begin(ctx, c, "glob")
	var a struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if r := k.decode(&a); r != nil {
		return r, nil
	}
	if strings.TrimSpace(a.Pattern) == "" {
		return k.fail("pattern is required"), nil
	}
	pattern := strings.TrimSpace(a.Pattern)
	searchPath := a.Path
	if searchPath == "" {
		searchPath = "."
	}
	if filepath.IsAbs(pattern) {
		// An absolute pattern names its own search root: everything before the
		// first component with a metacharacter.
		searchPath, pattern = splitAbsPattern(pattern)
	}
	g, err := compileGlob(pattern)
	if err != nil {
		return k.fail("invalid pattern %q: %s", clip(a.Pattern, 200), err), nil
	}
	base, disp, msg := k.resolveArg(searchPath)
	if msg != "" {
		return k.fail("%s", msg), nil
	}
	if r := k.authorize("glob "+clip(a.Pattern, 80)+" in "+disp, false, perm.RiskLow, base); r != nil {
		return r, nil
	}
	fi, err := os.Stat(base)
	if err != nil {
		return k.statFailure(base, disp, err), nil
	}
	if !fi.IsDir() {
		return k.fail("%s is a file, not a directory; give a directory as path", disp), nil
	}

	defer k.bounded()()
	var hits []globHit
	w := k.newWalker(base, true)
	w.run(func(e entry) walkAction {
		switch e.kind {
		case kindDir:
			if !g.couldContain(e.segs) {
				return walkSkip
			}
			if g.dirOnly && g.match(e.segs) {
				hits = append(hits, globHit{path: e.path, rel: e.rel, mtime: dirTime(e), dir: true})
			}
		case kindFile, kindLink:
			if g.dirOnly || !g.match(e.segs) {
				return walkContinue
			}
			var info iofs.FileInfo
			if e.kind == kindLink {
				// A link counts as a file only if it points at one.
				st, err := os.Stat(e.path)
				if err != nil || !st.Mode().IsRegular() {
					return walkContinue
				}
				info = st
			} else if st, err := e.dir.Info(); err == nil {
				info = st
			} else {
				return walkContinue
			}
			hits = append(hits, globHit{path: e.path, rel: e.rel, mtime: info.ModTime()})
		}
		return walkContinue
	})
	if w.cancelled {
		return k.fail("search cancelled or timed out; narrow the pattern or path"), nil
	}

	sort.Slice(hits, func(i, j int) bool {
		if !hits[i].mtime.Equal(hits[j].mtime) {
			return hits[i].mtime.After(hits[j].mtime)
		}
		return hits[i].rel < hits[j].rel
	})
	if len(hits) == 0 {
		text := fmt.Sprintf("No files matched %q in %s (files ignored by .gitignore are skipped).", clip(a.Pattern, 200), disp)
		if !strings.Contains(strings.Trim(pattern, "/"), "/") && !strings.Contains(pattern, "**") {
			text += fmt.Sprintf(" A pattern without a slash matches only the top level; use \"**/%s\" to search subdirectories.", strings.TrimSuffix(pattern, "/"))
		}
		if w.truncated {
			text += " The search stopped early after visiting a very large number of entries."
		}
		return k.ok(text), nil
	}
	shown := hits
	if len(shown) > maxGlobResults {
		shown = shown[:maxGlobResults]
	}
	var sb strings.Builder
	for i, h := range shown {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(k.display(h.path))
		if h.dir {
			sb.WriteByte('/')
		}
	}
	if more := len(hits) - len(shown); more > 0 {
		fmt.Fprintf(&sb, "\n[%d more not shown; narrow the pattern or path]", more)
	}
	if w.truncated {
		sb.WriteString("\n[search stopped early after visiting a very large number of entries]")
	}
	if w.unreadable > 0 {
		fmt.Fprintf(&sb, "\n[%s could not be read and %s skipped]", plural(w.unreadable, "directory"), map[bool]string{true: "was", false: "were"}[w.unreadable == 1])
	}
	res := k.ok(sb.String())
	res.Meta = map[string]any{"matches": len(hits)}
	return res, nil
}

func dirTime(e entry) time.Time {
	if info, err := e.dir.Info(); err == nil {
		return info.ModTime()
	}
	return time.Time{}
}

// splitAbsPattern splits an absolute glob into the directory to search (the
// longest leading run of components without metacharacters, keeping at least
// the last component in the pattern) and the pattern relative to it.
func splitAbsPattern(p string) (root, rest string) {
	trailing := strings.HasSuffix(p, "/")
	vol := filepath.VolumeName(p)
	comps := splitPath(p[len(vol):])
	i := 0
	for i < len(comps)-1 && !hasMeta(comps[i]) {
		i++
	}
	root = vol + string(filepath.Separator) + strings.Join(comps[:i], string(filepath.Separator))
	rest = strings.Join(comps[i:], "/")
	if trailing {
		rest += "/"
	}
	return root, rest
}
