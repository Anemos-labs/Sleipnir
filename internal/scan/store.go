package scan

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Dir is where the scans of a project live.
func Dir(root string) string { return filepath.Join(root, ".sleipnir", "scan") }

// RunDir is the directory of one scan.
func RunDir(root, slug string) string { return filepath.Join(Dir(root), slug) }

// BriefPath is the scan's brief, relative to the project root.
func BriefPath(slug string) string {
	return filepath.ToSlash(filepath.Join(".sleipnir", "scan", slug, "BRIEF.md"))
}

var slugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// ValidSlug reports whether slug can name a scan.
func ValidSlug(slug string) bool { return slugRE.MatchString(slug) }

// Store is the scans of one project, loaded on demand and written back after every change. One session holds one, so every
// agent of a team writes the same ledger.
type Store struct {
	root   string
	mu     sync.Mutex
	runs   map[string]*Run
	active string
	// bound is set when the session's goal drives the active scan (Bind): calls naming another scan then go to it.
	bound bool
	now   func() time.Time
	// OnChange, when set, is called with a copy of a scan after every change is written (the report is rewritten from it).
	OnChange func(r *Run)
}

// NewStore returns the store of the scans under root.
func NewStore(root string) *Store {
	return &Store{root: root, runs: map[string]*Run{}, now: time.Now}
}

// Root is the project root.
func (s *Store) Root() string { return s.root }

// Create starts a scan of commit at depth, over the inventory the harness took, and makes it the active one.
func (s *Store) Create(commit, depth, focus string, inv Inventory) (*Run, error) {
	return s.create(commit, depth, focus, inv, "", nil, "")
}

// create is Create, for a review of the change from base when base is set, and for a rerun of the scan prev when prev is set.
func (s *Store) create(commit, depth, focus string, inv Inventory, base string, changes []Change, prev string) (*Run, error) {
	d, ok := DepthByID(depth)
	if !ok {
		return nil, errDepth(depth)
	}
	if len(inv.Files) == 0 {
		return nil, errors.New("there is nothing to scan: the commit has no source files (below the focus)")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	name := "scan-" + now.Format("20060102-1504")
	if base != "" {
		name = "review-" + now.Format("20060102-1504")
	}
	if len(commit) >= 7 {
		name += "-" + commit[:7]
	}
	slug := name
	for n := 2; ; n++ {
		_, err := os.Stat(RunDir(s.root, slug))
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		// only a name that is taken is tried again: another error (a file where the scans directory should be) would hold for
		// every name
		if err != nil {
			return nil, err
		}
		if n > 10000 {
			return nil, fmt.Errorf("no free name for a scan after %d tries", n)
		}
		slug = fmt.Sprintf("%s-%d", name, n)
	}
	r := &Run{Slug: slug, Name: filepath.Base(s.root), Commit: commit, Depth: d.ID, Focus: focus, Created: now.Format(time.RFC3339), Inventory: inv,
		Shards: MakeShards(inv, d), Base: base, Changes: changes, Previous: prev}
	fb, err := json.Marshal(inv.Functions)
	if err != nil {
		return nil, err
	}
	if err := WriteAtomic(s.root, filepath.Join(RunDir(s.root, slug), "functions.json"), append(fb, '\n')); err != nil {
		return nil, err
	}
	if err := s.saveLocked(r); err != nil {
		return nil, err
	}
	s.runs[slug] = r
	s.active = slug
	s.changed(r)
	return cloneRun(r), nil
}

// Bind opens the scan a session's goal drives: calls that name another scan go to it from now on, since the agents of that
// goal work on this scan only.
func (s *Store) Bind(slug string) (*Run, error) {
	r, err := s.Open(slug)
	if err == nil {
		s.mu.Lock()
		s.bound = true
		s.mu.Unlock()
	}
	return r, err
}

// Open loads a scan and makes it the active one.
func (s *Store) Open(slug string) (*Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.loadLocked(slug)
	if err != nil {
		return nil, err
	}
	s.active = slug
	return cloneRun(r), nil
}

// Snapshot is a copy of a scan as it is now.
func (s *Store) Snapshot(slug string) (*Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if slug == "" {
		var err error
		if slug, err = s.slugLocked(""); err != nil {
			return nil, err
		}
	}
	r, err := s.loadLocked(slug)
	if err != nil {
		return nil, err
	}
	return cloneRun(r), nil
}

// Active is the scan calls that name none mean, or "".
func (s *Store) Active() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

// slugLocked is the scan a call means: the one it names, else the active one, else the project's only unfinished scan.
func (s *Store) slugLocked(slug string) (string, error) {
	if slug != "" {
		return slug, nil
	}
	if s.active != "" {
		return s.active, nil
	}
	var open []string
	for _, r := range List(s.root) {
		if Check(r).Phase != PhaseDone {
			open = append(open, r.Slug)
		}
	}
	switch len(open) {
	case 0:
		return "", errors.New("no scan is open: start one with /scan")
	case 1:
		s.active = open[0]
		return open[0], nil
	}
	return "", fmt.Errorf("no scan is open and %d are unfinished: name one with scan (%s)", len(open), strings.Join(open, ", "))
}

// Resolve is the scan a call means. A call naming a scan that does not exist, or another scan than the one the session works
// on, goes to the one a call naming none would, with a note, rather than being refused: a scan's agents never write to an
// older scan of the same project, whose lead numbers mean other things.
func (s *Store) Resolve(slug string) (used, note string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if slug == "" {
		used, err = s.slugLocked("")
		return used, "", err
	}
	if s.bound && slug != s.active {
		return s.active, fmt.Sprintf("(this session works on %s: the call went there, and %s was left as it is; leave scan out)", s.active, slug), nil
	}
	if _, lerr := s.loadLocked(slug); lerr == nil {
		return slug, "", nil
	} else if ValidSlug(slug) {
		if _, serr := os.Stat(filepath.Join(RunDir(s.root, slug), "scan.json")); serr == nil {
			return "", "", lerr
		}
	}
	used, err = s.slugLocked("")
	if err != nil {
		return "", "", fmt.Errorf("no scan %q, and %w", slug, err)
	}
	return used, fmt.Sprintf("(there is no scan %q: this went to %s, the scan this session works on; leave scan out)", slug, used), nil
}

func (s *Store) loadLocked(slug string) (*Run, error) {
	if !ValidSlug(slug) {
		return nil, fmt.Errorf("no scan %q", slug)
	}
	if r, ok := s.runs[slug]; ok {
		return r, nil
	}
	r, err := Load(s.root, slug)
	if err != nil {
		return nil, err
	}
	s.runs[slug] = r
	return r, nil
}

// maxRecord bounds a scan record read back from disk (scan.json, functions.json): they come with the project, so a link to a
// device or a sparse file cannot make a load fill memory.
const maxRecord = 256 << 20

// readRecord reads the regular file at p, without following a final link, and at most maxRecord bytes.
func readRecord(p string) ([]byte, error) {
	fi, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", filepath.Base(p))
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() || fi.Size() > maxRecord {
		return nil, fmt.Errorf("%s is not a regular file of at most %d bytes", filepath.Base(p), maxRecord)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxRecord+1))
	if err == nil && len(b) > maxRecord {
		err = fmt.Errorf("%s is larger than %d bytes", filepath.Base(p), maxRecord)
	}
	return b, err
}

// Load reads a scan from disk, without a store.
func Load(root, slug string) (*Run, error) {
	if !ValidSlug(slug) {
		return nil, fmt.Errorf("no scan %q", slug)
	}
	b, err := readRecord(filepath.Join(RunDir(root, slug), "scan.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("no scan %q", slug)
	}
	if err != nil {
		return nil, err
	}
	var r Run
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("scan %s: %w", slug, err)
	}
	if r.Slug != slug {
		return nil, fmt.Errorf("scan %s: scan.json names %q", slug, r.Slug)
	}
	if fb, err := readRecord(filepath.Join(RunDir(root, slug), "functions.json")); err == nil {
		_ = json.Unmarshal(fb, &r.Inventory.Functions)
	}
	return &r, nil
}

// List reads every scan of a project, newest first.
func List(root string) []*Run {
	ents, err := os.ReadDir(Dir(root))
	if err != nil {
		return nil
	}
	var out []*Run
	for _, e := range ents {
		if !e.IsDir() || !ValidSlug(e.Name()) {
			continue
		}
		if r, err := Load(root, e.Name()); err == nil {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Created != out[j].Created {
			return out[i].Created > out[j].Created
		}
		return out[i].Slug > out[j].Slug
	})
	return out
}

// Update runs fn on the scan under the store's lock and writes it back when fn succeeds; a failed fn leaves the scan as it was.
func (s *Store) Update(slug string, fn func(r *Run) error) (*Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	slug, err := s.slugLocked(slug)
	if err != nil {
		return nil, err
	}
	r, err := s.loadLocked(slug)
	if err != nil {
		return nil, err
	}
	before, _ := json.Marshal(r)
	restore := func() {
		var back Run
		_ = json.Unmarshal(before, &back)
		back.Inventory.Functions = r.Inventory.Functions
		*r = back
	}
	if err := fn(r); err != nil {
		restore()
		return nil, err
	}
	r.Version++
	if err := s.saveLocked(r); err != nil {
		restore()
		return nil, err
	}
	s.changed(r)
	return cloneRun(r), nil
}

// Report marks the scan's report as written from the ledger as it is now and writes the report files: the report phase is
// done once nothing before it is missing. It returns the scan as marked.
func (s *Store) Report(slug string) (*Run, error) {
	r, err := s.Update(slug, func(r *Run) error {
		n := 0
		for _, p := range Check(r).Problems {
			if p.Phase != PhaseReport {
				n++
			}
		}
		r.Reported = &Mark{Version: r.Version + 1, Problems: n}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return r, WriteReport(s.root, r)
}

// View runs fn on the scan under the store's lock, changing nothing.
func (s *Store) View(slug string, fn func(r *Run) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	slug, err := s.slugLocked(slug)
	if err != nil {
		return err
	}
	r, err := s.loadLocked(slug)
	if err != nil {
		return err
	}
	return fn(r)
}

func (s *Store) changed(r *Run) {
	if s.OnChange != nil {
		s.OnChange(cloneRun(r))
	}
}

func (s *Store) saveLocked(r *Run) error {
	b, err := json.MarshalIndent(r, "", " ")
	if err != nil {
		return err
	}
	return WriteAtomic(s.root, filepath.Join(RunDir(s.root, r.Slug), "scan.json"), append(b, '\n'))
}

// cloneRun is a deep copy of a scan; the functions, which never change, are shared.
func cloneRun(r *Run) *Run {
	b, _ := json.Marshal(r)
	var c Run
	_ = json.Unmarshal(b, &c)
	c.Inventory.Functions = r.Inventory.Functions
	return &c
}

// WriteAtomic writes data to path, which is inside the project root, through a temporary file in the same directory. Every
// step goes through the root (os.Root): a directory on the way (.sleipnir, the scan directory, the run's) that a link
// replaced cannot take the write, or the rename over a report, outside the project.
func WriteAtomic(root, path string, data []byte) error {
	rel, err := filepath.Rel(root, path)
	if err != nil || !filepath.IsLocal(rel) {
		return fmt.Errorf("%s is not inside %s", path, root)
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer r.Close()
	dir := filepath.Dir(rel)
	if err := r.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var f *os.File
	var name string
	for range 100 {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			return err
		}
		name = filepath.Join(dir, ".tmp-"+hex.EncodeToString(b[:]))
		if f, err = r.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600); !errors.Is(err, os.ErrExist) {
			break
		}
	}
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		r.Remove(name)
		return err
	}
	if err := f.Close(); err != nil {
		r.Remove(name)
		return err
	}
	if err := r.Rename(name, rel); err != nil {
		r.Remove(name)
		return err
	}
	return nil
}
