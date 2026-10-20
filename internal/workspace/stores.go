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
			case stores[name] && e.Type()&(os.ModeSymlink|os.ModeIrregular) != 0:
				target, ok := linkTarget(dir, p)
				if !ok {
					continue // a dangling link reaches no code, and an irregular file that is no link none at all
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

// linkTarget is where the link at p (in dir) leads, resolved, and whether it leads anywhere that exists. A symbolic link
// is resolved by EvalSymlinks; a Windows junction, which Lstat reports as irregular and EvalSymlinks does not follow, by
// its Readlink target (relative ones from dir).
func linkTarget(dir, p string) (string, bool) {
	if t, err := filepath.EvalSymlinks(p); err == nil && t != p {
		return t, true
	}
	t, err := os.Readlink(p)
	if err != nil {
		return "", false
	}
	if !filepath.IsAbs(t) {
		t = filepath.Join(dir, t)
	}
	if _, err := os.Stat(t); err != nil {
		return "", false
	}
	if r, err := filepath.EvalSymlinks(t); err == nil {
		t = r
	}
	return filepath.Clean(t), true
}

// isStoreName reports whether name is one of gitx.DependencyStores.
func isStoreName(name string) bool {
	for _, n := range gitx.DependencyStores() {
		if n == name {
			return true
		}
	}
	return false
}
