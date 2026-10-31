package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
)

// Save merges patch into the configuration file at path and writes the result
// back as canonical JSON (two-space indent, sorted keys, trailing newline),
// atomically: the file is replaced by renaming a temporary file, so a crash
// never leaves a half-written configuration.
//
// patch has the shape of the file, {"models": {"default": "x/y"}}, and merges
// with the same rules as layers (maps merge, lists and scalars replace, null
// deletes the key). The file is edited as a generic JSON document, so keys this
// version does not know survive, at any depth. Comments in an existing file are
// not preserved.
//
// Save refuses to overwrite a file that does not parse, and refuses to write a
// result that Load would reject, so it can never leave the user with a
// configuration that fails to start. A new file is created with mode 0600 (it
// is the user's, and may name environment variables they would rather not
// share); an existing file keeps its mode. A symlinked file (dotfiles) stays a
// symlink: the file it points to is replaced.
//
// Within one process, Saves of the same file run one at a time (WriteLock on
// the file the path resolves to), and each reads the file afresh, so two
// goroutines that patch different keys both keep their change. Between
// processes there is no lock: the later rename wins.
func Save(path string, patch map[string]any) error {
	if path == "" {
		return errors.New("config: Save needs a path")
	}
	target := path
	if real, err := filepath.EvalSymlinks(path); err == nil {
		target = real
	}
	defer WriteLock(target)() // keyed by the file the path resolves to, also while the file is not there yet
	return save(hostFiles{}, path, target, patch)
}

// SaveWithin is Save for a file of a project (path, under root): every read and write goes through root (os.Root), so a link or
// a directory of the project replaced while the file is read and written cannot take the write outside it.
func SaveWithin(root, path string, patch map[string]any) error {
	if root == "" || path == "" {
		return errors.New("config: SaveWithin needs a project and a path")
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || !filepath.IsLocal(rel) {
		return fmt.Errorf("config: %s is not in the project %s", path, root)
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return fmt.Errorf("config: %s: %s", root, pathReason(err))
	}
	defer r.Close()
	target := path
	if real, err := filepath.EvalSymlinks(path); err == nil {
		target = real
		// A file of the project that is a link to another file of the project is saved where it leads, as Save does: the
		// new file replaces the target, not the link. What the link leads to is named inside the root, which refuses it
		// if it is not there.
		if realRoot, err := filepath.EvalSymlinks(root); err == nil {
			if trel, err := filepath.Rel(realRoot, real); err == nil && filepath.IsLocal(trel) {
				rel = trel
			}
		}
	}
	defer WriteLock(target)()
	return save(rootFiles{r}, path, rel, patch)
}

// saveFiles are the file operations of a save: on the host's paths (Save) or within a project's root (SaveWithin).
type saveFiles interface {
	read(name string) ([]byte, error)
	stat(name string) (fs.FileInfo, error)
	mkdirAll(dir string) error
	createTemp(dir string) (*os.File, string, error)
	chmod(name string, mode fs.FileMode) error
	rename(from, to string) error
	remove(name string)
}

// save merges patch into the file target (path names it in errors) and replaces it, through files.
func save(files saveFiles, path, target string, patch map[string]any) error {
	existing := map[string]any{}
	mode := fs.FileMode(0o600)
	data, err := files.read(target)
	switch {
	case err == nil:
		root, _, perr := parseJSONC(path, data)
		if perr != nil {
			return fmt.Errorf("config: refusing to overwrite a file that does not parse: %w", perr)
		}
		if root.kind != nObject {
			return fmt.Errorf("config: %s: the top-level value must be an object; refusing to overwrite it", path)
		}
		existing = toValue(root).(map[string]any)
		if fi, serr := files.stat(target); serr == nil {
			mode = fi.Mode().Perm()
		}
	case errors.Is(err, fs.ErrNotExist):
	default:
		return fmt.Errorf("config: %s: cannot read: %s", path, pathReason(err))
	}

	if patch == nil {
		patch = map[string]any{}
	}
	pn, err := nodeFromValue(patch)
	if err != nil {
		return fmt.Errorf("config: the patch cannot be encoded as JSON: %w", err)
	}
	m := newMerger()
	m.tree = existing
	m.mergeObject(m.tree, toValue(pn).(map[string]any), nil, "patch")

	out, err := marshalIndent(m.tree)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if err := checkWritable(path, out, m); err != nil {
		return err
	}

	if err := files.mkdirAll(filepath.Dir(target)); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	tmp, name, err := files.createTemp(filepath.Dir(target))
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			files.remove(name)
		}
	}()
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return fmt.Errorf("config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if err := files.chmod(name, mode); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if err := files.rename(name, target); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	ok = true
	return nil
}

// hostFiles are the host's paths, as they resolve.
type hostFiles struct{}

func (hostFiles) read(name string) ([]byte, error)          { return readConfigFile(name) }
func (hostFiles) stat(name string) (fs.FileInfo, error)     { return os.Stat(name) }
func (hostFiles) mkdirAll(dir string) error                 { return os.MkdirAll(dir, 0o700) }
func (hostFiles) chmod(name string, mode fs.FileMode) error { return os.Chmod(name, mode) }
func (hostFiles) rename(from, to string) error              { return os.Rename(from, to) }
func (hostFiles) remove(name string)                        { os.Remove(name) }
func (hostFiles) createTemp(dir string) (*os.File, string, error) {
	f, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return nil, "", err
	}
	return f, f.Name(), nil
}

// rootFiles are the paths of a project, relative to its root and resolved within it.
type rootFiles struct{ r *os.Root }

func (f rootFiles) read(name string) ([]byte, error) {
	fi, err := f.r.Stat(name)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	if fi.Size() > maxFileSize {
		return nil, fmt.Errorf("file is larger than %d KB", maxFileSize>>10)
	}
	return f.r.ReadFile(name)
}
func (f rootFiles) stat(name string) (fs.FileInfo, error)     { return f.r.Stat(name) }
func (f rootFiles) mkdirAll(dir string) error                 { return f.r.MkdirAll(dir, 0o700) }
func (f rootFiles) chmod(name string, mode fs.FileMode) error { return f.r.Chmod(name, mode) }
func (f rootFiles) rename(from, to string) error              { return f.r.Rename(from, to) }
func (f rootFiles) remove(name string)                        { f.r.Remove(name) }
func (f rootFiles) createTemp(dir string) (*os.File, string, error) {
	for range 100 {
		name := filepath.Join(dir, fmt.Sprintf(".config-%d.tmp", rand.Uint64()))
		tf, err := f.r.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		return tf, name, err
	}
	return nil, "", errors.New("no temporary file name is free")
}

// checkWritable applies the checks Load would: the schema and the values.
func checkWritable(path string, doc []byte, m *merger) error {
	root, _, err := parseJSONC(path, doc)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	c := &checker{file: path}
	c.convert(root, configType, nil, true)
	if err := Errors(c.issues); err != nil {
		return fmt.Errorf("config: refusing to write an invalid configuration:\n%w", err)
	}
	cfg, err := m.decode()
	if err != nil {
		return fmt.Errorf("config: refusing to write an invalid configuration: %w", err)
	}
	if err := Errors(cfg.Validate()); err != nil {
		return fmt.Errorf("config: refusing to write an invalid configuration:\n%w", err)
	}
	return nil
}

// marshalIndent encodes JSON with two-space indentation, no HTML escaping, and a final newline.
func marshalIndent(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// CheckProjectTarget refuses a project or local configuration path that, through a link in the repository, resolves outside
// the project root: a change meant for one project must not land in the user's own file or another project's. The links of the
// user's own file (a dotfiles checkout) are not held to this; it is for the files a repository supplies.
func CheckProjectTarget(root, path string) error {
	if root == "" {
		return errors.New("config: there is no project to write to")
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("config: %s: %s", root, pathReason(err))
	}
	resolved := resolveExisting(path)
	if rel, err := filepath.Rel(realRoot, resolved); err != nil || !filepath.IsLocal(rel) {
		return fmt.Errorf("config: %s resolves to %s, outside the project; refusing to write through the link", path, resolved)
	}
	return nil
}
