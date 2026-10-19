package research

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// exportMark is the file an export leaves in its directory, so a later export of the same wiki may replace it and nothing else.
const exportMark = ".sleipnir-research"

// Export copies a built wiki (src) to dst. dst must be empty, absent, or an earlier export (it holds exportMark): an export never
// replaces files it did not write.
func Export(src, dst string) error {
	if ents, err := os.ReadDir(dst); err == nil && len(ents) > 0 {
		if _, err := os.Stat(filepath.Join(dst, exportMark)); err != nil {
			return fmt.Errorf("%s is not empty and is not an earlier export of a wiki: choose another directory", dst)
		}
		if err := os.RemoveAll(dst); err != nil {
			return err
		}
	}
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(out, b, 0o644)
	})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dst, exportMark), []byte("a research wiki exported by sleipnir; a later export replaces this directory\n"), 0o644)
}
