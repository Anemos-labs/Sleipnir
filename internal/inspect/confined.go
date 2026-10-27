package inspect

import (
	"io/fs"
	"os"
	"path/filepath"
)

// confinedDir names a directory as a trusted base plus a path below it. Every
// open resolves that path through an os.Root on the base, so a symlink planted at
// any component (a session directory, blobs/ab/cd, the file itself) cannot lead
// a read outside the base. A root is opened per call: sessions are loaded and
// dropped freely, and a retained handle would leak a descriptor each time.
type confinedDir struct{ base, rel string }

// confine names dir below base. A dir outside base (or an empty base) is
// confined to itself.
func confine(base, dir string) confinedDir {
	if base != "" {
		if rel, err := filepath.Rel(base, dir); err == nil && filepath.IsLocal(rel) {
			return confinedDir{base: base, rel: rel}
		}
	}
	return confinedDir{base: dir, rel: "."}
}

// sub names a directory below d.
func (d confinedDir) sub(name string) confinedDir {
	return confinedDir{base: d.base, rel: filepath.Join(d.rel, name)}
}

// open opens name below d for reading if it is a regular file (see openRegularIn).
func (d confinedDir) open(name string) (*os.File, error) {
	r, err := os.OpenRoot(d.base)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return openRegularIn(r, filepath.Join(d.rel, name))
}

// lstat describes name below d without following a final symlink.
func (d confinedDir) lstat(name string) (fs.FileInfo, error) {
	r, err := os.OpenRoot(d.base)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return r.Lstat(filepath.Join(d.rel, name))
}

// stat describes name below d, following symlinks only within the base.
func (d confinedDir) stat(name string) (fs.FileInfo, error) {
	r, err := os.OpenRoot(d.base)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return r.Stat(filepath.Join(d.rel, name))
}
