package research

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
)

// exportMark is the file an export leaves in its directory, so a later export of the same wiki may replace it and nothing else.
const exportMark = ".sleipnir-research"

// Export writes a rendered wiki (Render's files) into dir, a directory of the project at root given relative to it. Every write
// goes through an os.Root of the project, so neither dir nor a link inside the project can lead outside it. dir must be empty,
// absent, or an earlier export (it holds exportMark): an export never replaces files it did not write.
func Export(root, dir string, files map[string][]byte) error {
	dir = filepath.Clean(dir)
	if !filepath.IsLocal(dir) || dir == "." {
		return fmt.Errorf("%s is not a directory inside the project", dir)
	}
	pr, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer pr.Close()
	if f, err := pr.Open(dir); err == nil {
		ents, rerr := f.ReadDir(1)
		f.Close()
		if rerr == nil && len(ents) > 0 {
			if _, err := pr.Stat(filepath.Join(dir, exportMark)); err != nil {
				return fmt.Errorf("%s is not empty and is not an earlier export of a wiki: choose another directory", dir)
			}
			if err := pr.RemoveAll(dir); err != nil {
				return err
			}
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !fs.ValidPath(name) {
			return fmt.Errorf("export: bad file name %q", name)
		}
		out := filepath.Join(dir, filepath.FromSlash(name))
		if err := pr.MkdirAll(filepath.Join(dir, filepath.FromSlash(path.Dir(name))), 0o755); err != nil {
			return err
		}
		if err := pr.WriteFile(out, files[name], 0o644); err != nil {
			return err
		}
	}
	return pr.WriteFile(filepath.Join(dir, exportMark), []byte("a research wiki exported by sleipnir; a later export replaces this directory\n"), 0o644)
}
