package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
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
func Save(path string, patch map[string]any) error {
	if path == "" {
		return errors.New("config: Save needs a path")
	}
	target := path
	if real, err := filepath.EvalSymlinks(path); err == nil {
		target = real
	}

	existing := map[string]any{}
	mode := fs.FileMode(0o600)
	data, err := readConfigFile(target)
	switch {
	case err == nil:
		root, _, perr := parseJSONC(target, data)
		if perr != nil {
			return fmt.Errorf("config: refusing to overwrite a file that does not parse: %w", perr)
		}
		if root.kind != nObject {
			return fmt.Errorf("config: %s: the top-level value must be an object; refusing to overwrite it", path)
		}
		existing = toValue(root).(map[string]any)
		if fi, serr := os.Stat(target); serr == nil {
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

	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	name := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			os.Remove(name)
		}
	}()
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return fmt.Errorf("config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if err := os.Chmod(name, mode); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if err := os.Rename(name, target); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	ok = true
	return nil
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
