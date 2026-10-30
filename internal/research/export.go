package research

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
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
			if err := onlyExported(pr, dir); err != nil {
				return err
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
	return pr.WriteFile(filepath.Join(dir, exportMark), []byte(markHeader+strings.Join(names, "\n")+"\n"), 0o644)
}

// maxMarkBytes bounds the mark an export reads back: far more than the names of any wiki's files, far less than memory.
const maxMarkBytes = 4 << 20

// readMark reads the mark at name: a regular file of at most maxMarkBytes. Anything else is no mark an export wrote.
func readMark(pr *os.Root, name string) ([]byte, error) {
	if fi, err := pr.Lstat(name); err != nil || !fi.Mode().IsRegular() {
		return nil, errors.New("no regular mark") // a pipe would hold the open, a link leads elsewhere
	}
	f, err := pr.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxMarkBytes+1))
	if err == nil && len(b) > maxMarkBytes {
		err = errors.New("the mark is too large")
	}
	return b, err
}

// markHeader opens exportMark; the names of the files the export wrote follow it, one a line.
const markHeader = "a research wiki exported by sleipnir; a later export replaces the files listed here\n"

// onlyExported checks that dir, a non-empty directory of the project, is an earlier export holding nothing else: its
// exportMark lists every file in it. The mark alone is no authority, since anyone who can write to the project can write one.
func onlyExported(pr *os.Root, dir string) error {
	refuse := fmt.Errorf("%s is not empty and is not an earlier export of a wiki: choose another directory", dir)
	b, err := readMark(pr, filepath.Join(dir, exportMark))
	if err != nil || !strings.HasPrefix(string(b), markHeader) {
		return refuse
	}
	listed := map[string]bool{exportMark: true}
	for _, n := range strings.Split(strings.TrimPrefix(string(b), markHeader), "\n") {
		listed[n] = true
	}
	return fs.WalkDir(pr.FS(), filepath.ToSlash(dir), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel := strings.TrimPrefix(p, filepath.ToSlash(dir)+"/")
		if !listed[rel] || !d.Type().IsRegular() {
			return fmt.Errorf("%s holds %s, which no earlier export wrote: move it or choose another directory", dir, rel)
		}
		return nil
	})
}
