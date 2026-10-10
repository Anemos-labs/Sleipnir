// Package trust is what lets a person say "yes, use what this project says" once, and have it mean exactly the files they saw.
//
// A repository can be somebody else's, and a few of the files in it are read by the harness as instructions: the AGENTS.md that is put
// in front of every agent, the settings that can start tool servers and run hooks, the skills, commands and agent definitions that
// the model may load. Until the project is trusted none of that is used (internal/session says so, and --trust-project is the
// per-run answer). Saying it every run is a chore that teaches the wrong habit (an alias with the flag in it), and "trusted from now
// on" would be the wrong answer: the repository changes with every pull, and what was read last month is not what is read today.
//
// So the answer is remembered for the contents. Scan lists what trusting a project would let the harness use, with a hash of each
// file, and a Ledger keeps the one digest of all of them under the directory the person answered for. When any of the files is
// added, removed or edited the digest is another one, the answer no longer applies, and the person is asked again (and told which
// file it was). It is what direnv does with a .envrc, for the same reason.
//
// What it vouches for is what the harness itself reads as text and settings. It does not vouch for the repository's code: a hook
// that runs ./scripts/lint.sh is covered, the script is not (it is code, and running code of a repository is what every approval of
// `go test` or `make` is already about; docs/SECURITY.md says so). The ledger lives in the user's state directory, never in the
// repository, since a repository must not be able to trust itself.
package trust

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/memory"
	"github.com/anemos-labs/sleipnir/internal/provider"
)

// Kind says what a file is to the harness.
type Kind string

const (
	// Instructions are put into the prompt every agent shares (AGENTS.md and what it imports).
	Instructions Kind = "instructions"
	// Settings are the project's configuration: providers, permissions, hooks, tool servers.
	Settings Kind = "settings"
	// ToolServers is a .mcp.json: servers that may start (each one still asks, by its own fingerprint).
	ToolServers Kind = "tool servers"
	// Skills, Commands and Agents are definitions the model may load or the person may run.
	Skills   Kind = "skills"
	Commands Kind = "commands"
	Agents   Kind = "agents"
)

// What one scan looks at, at the most: a repository that has more than this to say is not a project one reads before one says yes.
const (
	maxFiles   = 400
	maxBytes   = 16 << 20
	maxVisited = 50_000 // directory entries looked at in all, whatever they are
)

// File is one thing a trusted project would let the harness use.
type File struct {
	// Path is relative to the project root, with "/" separators. For an instruction file it is the path the harness shows.
	Path string
	Kind Kind
	Size int64
	// Sum is the SHA-256 of what is read, in hex.
	Sum string
}

// Footprint is what a session started in Cwd would take from the project at Root if it trusted it.
type Footprint struct {
	Root, Cwd string
	// Files are sorted by path.
	Files []File
	// Digest is the one hash of all of them (and of the kind of each): "sha256:" and 64 hex digits.
	Digest string
	// Partial says that the project has more to read than a scan looks at (the limits above): such a footprint cannot be remembered,
	// since the digest does not cover all of it.
	Partial bool
	// Unread names what made the footprint partial, at most maxUnread entries: a file that is there and could not be read as one (a
	// link that leaves the project), a file over the size limit, the first file over the count limit, a directory too large to walk.
	Unread []string
}

// maxUnread bounds Footprint.Unread.
const maxUnread = 50

// partial marks the footprint partial because of what path names.
func (s *scanner) partial(path string) {
	s.fp.Partial = true
	if len(s.fp.Unread) < maxUnread && !slices.Contains(s.fp.Unread, path) {
		s.fp.Unread = append(s.fp.Unread, path)
	}
}

// Empty reports whether there is nothing in the project that trust would unlock.
func (f *Footprint) Empty() bool { return len(f.Files) == 0 }

// Scan reads what the project at root (a session started in cwd) would use if it were trusted. home is the user's home directory, whose
// own instruction files are not the project's and are left out. A missing file is not an error: it is absent from the footprint.
func Scan(root, cwd, home string) (*Footprint, error) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	fp := &Footprint{Root: root, Cwd: cwd}
	sc := &scanner{fp: fp, files: maxFiles, bytes: maxBytes}

	// What the prompt would hold: the instruction files from the root down to cwd and what they import, as the loader renders
	// them (comments and hidden characters are removed, so the text is what the model would read and a change to a comment that the
	// loader drops is not a change).
	srcs, _ := memory.Load(memory.Opts{Root: root, Cwd: cwd, Home: home}) // the error is about files that were refused, which are not used either way
	for _, s := range srcs {
		if s.FromUser() {
			continue
		}
		sc.add(File{Path: s.Path, Kind: Instructions, Size: int64(len(s.Text)), Sum: sumOf([]byte(s.Text))})
	}

	r, err := os.OpenRoot(realRoot)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	for _, f := range []struct {
		rel  string
		kind Kind
	}{{".sleipnir/config.json", Settings}, {".sleipnir/config.local.json", Settings}, {".mcp.json", ToolServers}} {
		sc.file(r, f.rel, f.kind)
	}
	for _, kind := range []Kind{Skills, Commands, Agents} {
		for _, brand := range []string{".sleipnir", ".claude"} {
			sc.tree(r, brand+"/"+string(kind), kind)
		}
	}

	sort.Slice(fp.Files, func(i, j int) bool {
		if fp.Files[i].Path != fp.Files[j].Path {
			return fp.Files[i].Path < fp.Files[j].Path
		}
		return fp.Files[i].Kind < fp.Files[j].Kind
	})
	fp.Digest = digest(fp)
	return fp, nil
}

type scanner struct {
	fp      *Footprint
	files   int   // how many more files may be added
	bytes   int64 // how many more bytes may be read
	visited int   // directory entries looked at
}

// add appends a trust fingerprint file while budget remains and otherwise marks the scan partial.
func (s *scanner) add(f File) {
	if s.files <= 0 {
		s.partial(f.Path + " (more files than a scan reads)")
		return
	}
	s.files--
	s.fp.Files = append(s.fp.Files, f)
}

// file adds one file of the project, if it is there and is a regular file. A file that is there and cannot be read as one (a link that
// leaves the project, which the loaders of the settings would follow where this does not) is a file the digest does not cover, and the
// footprint says so.
func (s *scanner) file(r *os.Root, rel string, kind Kind) {
	name := filepath.FromSlash(rel)
	f, err := r.OpenFile(name, openFlags, 0)
	if err != nil {
		if _, lerr := r.Lstat(name); lerr == nil {
			s.partial(rel)
		}
		return
	}
	defer f.Close()
	s.read(f, rel, kind, nil)
}

// read hashes an open file, which must be a regular one. prefix, when there is one, is hashed first (a link's target is part of what
// a link says).
func (s *scanner) read(f *os.File, rel string, kind Kind, prefix []byte) {
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return
	}
	if s.bytes <= 0 || fi.Size() > s.bytes {
		s.partial(rel + " (larger than a scan reads)")
		return
	}
	h := sha256.New()
	h.Write(prefix)
	n, err := io.Copy(h, io.LimitReader(f, s.bytes))
	if err != nil {
		s.partial(rel) // a file that cannot be read cannot be vouched for
		return
	}
	s.bytes -= n
	s.add(File{Path: rel, Kind: kind, Size: n, Sum: hex.EncodeToString(h.Sum(nil))})
}

// tree adds every file under dir (relative to the root). A link is not followed to be walked, but it is read: the loaders of skills,
// commands and agents follow a link that stays inside the project, and what it leads to is then part of what the model reads, so the
// file's own content and the link's target are both in its sum (one that leads out of the project, or to something that is not a
// file, is a file the digest does not cover).
func (s *scanner) tree(r *os.Root, dir string, kind Kind) {
	if fi, err := r.Lstat(filepath.FromSlash(dir)); err != nil || !fi.IsDir() {
		return // not there, or a link: the loaders refuse a directory that is one and leaves the project, and the one that stays is a file below
	}
	_ = fs.WalkDir(r.FS(), dir, func(p string, d fs.DirEntry, err error) error {
		if s.visited++; s.visited > maxVisited {
			s.partial(dir + " (more entries than a scan looks at)")
			return fs.SkipAll
		}
		if err != nil {
			s.partial(p)
			return nil
		}
		switch {
		case d.IsDir():
		case d.Type()&fs.ModeSymlink != 0:
			target, lerr := r.Readlink(filepath.FromSlash(p))
			f, oerr := r.OpenFile(filepath.FromSlash(p), openFlags, 0)
			if lerr != nil || oerr != nil {
				s.partial(p)
				return nil
			}
			s.read(f, p, kind, []byte("symlink\x00"+target+"\x00"))
			f.Close()
		case d.Type().IsRegular():
			s.file(r, p, kind)
		}
		return nil
	})
}

// sumOf returns the full hexadecimal SHA-256 digest used to identify trusted content.
func sumOf(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// digest is the one hash of a footprint: its files in order, each as its kind, its path and the hash of what is read, and a mark for a
// footprint that is partial (which is therefore never the digest of a complete one).
func digest(fp *Footprint) string {
	h := sha256.New()
	for _, f := range fp.Files {
		fmt.Fprintf(h, "%s\x00%s\x00%s\n", f.Kind, f.Path, f.Sum)
	}
	if fp.Partial {
		h.Write([]byte("partial\n"))
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// Describe is the footprint as a person reads it: one line for each file, a definition directory as one line, and what each is.
// Paths are the project's, so they are cleaned of whatever a terminal would act on before they are shown.
func (f *Footprint) Describe() string {
	type group struct {
		label string
		kind  Kind
		n     int
		size  int64
	}
	var groups []*group
	byLabel := map[string]*group{}
	for _, file := range f.Files {
		label := Show(file.Path)
		switch file.Kind {
		case Skills, Commands, Agents:
			// the directory: ".claude/skills/review/SKILL.md" is one of the files of ".claude/skills"
			parts := strings.SplitN(file.Path, "/", 3)
			if len(parts) >= 2 {
				label = Show(parts[0] + "/" + parts[1])
			}
		}
		key := string(file.Kind) + "\x00" + label
		g := byLabel[key]
		if g == nil {
			g = &group{label: label, kind: file.Kind}
			byLabel[key] = g
			groups = append(groups, g)
		}
		g.n++
		g.size += file.Size
	}
	var b strings.Builder
	for i, g := range groups {
		if i == 20 {
			fmt.Fprintf(&b, "and %d more\n", len(groups)-i)
			break
		}
		what := g.label
		if g.n > 1 || g.kind == Skills || g.kind == Commands || g.kind == Agents {
			what = fmt.Sprintf("%s/ (%d %s)", g.label, g.n, plural(g.n, "file", "files"))
		}
		fmt.Fprintf(&b, "%s: %s, %s\n", what, g.kind, size(g.size))
	}
	if f.Partial {
		b.WriteString("and more than can be read here: this project cannot be remembered\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// plural selects the singular form only for a count of one.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// size formats trust file sizes using binary multiples labeled B, KB, and MB.
func size(n int64) string {
	switch {
	case n < 1<<10:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
}

// Show makes a path of the project fit to be shown: the names in a repository are its author's, and a name may hold what a terminal
// acts on (an escape sequence, a control character) or what hides the rest of a line.
func Show(p string) string { return provider.SanitizeText(p, 120) }
