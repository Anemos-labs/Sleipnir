package workspace

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/gitx"
)

// Bounds of EscapingStores' walk: how deep it looks for dependency stores (a monorepo's packages/app/node_modules is at
// depth 3) and how many entries it reads at most, so that the check stays cheap on a large tree.
const (
	maxStoreDepth   = 4
	maxStoreEntries = 20000
)

// EscapingStores lists the dependency stores (gitx.DependencyStores: node_modules, .venv, …) in the tree at root that are
// symbolic links resolving outside it, as "web/node_modules -> /src/app/web/node_modules". A test run in such a tree can pass
// against the code the link reaches (a workspace package or an editable install in the main checkout) instead of the tree's
// own edits: a false green. Stores inside other stores are not looked at.
func EscapingStores(root string) []string {
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil
	}
	stores := map[string]bool{}
	for _, n := range gitx.DependencyStores() {
		stores[n] = true
	}
	var out []string
	seen := 0
	var walk func(dir, rel string, depth int)
	walk = func(dir, rel string, depth int) {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range ents {
			if seen++; seen > maxStoreEntries {
				return
			}
			name := e.Name()
			p, r := filepath.Join(dir, name), name
			if rel != "" {
				r = rel + "/" + name
			}
			switch {
			case stores[name] && e.Type()&os.ModeSymlink != 0:
				target, err := filepath.EvalSymlinks(p)
				if err != nil {
					continue // a dangling link reaches no code
				}
				if inside, err := filepath.Rel(real, target); err != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) || filepath.IsAbs(inside) {
					out = append(out, r+" -> "+target)
				}
			case e.IsDir() && !stores[name] && name != ".git" && depth < maxStoreDepth:
				walk(p, r, depth+1)
			}
		}
	}
	walk(real, "", 1)
	return out
}
