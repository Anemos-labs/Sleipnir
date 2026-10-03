package env

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/rl"
)

// ---- validation ----

// TaskError is one problem with one task. Errors always name the task id and
// the JSON field so a bad line in a 10 000-task file can be found at once.
type TaskError struct {
	Line  int    // 1-based line in the tasks file; 0 when unknown
	ID    string // task id ("" when the id itself is the problem)
	Field string // JSON path of the offending field, e.g. "verifier.timeout_s"
	Msg   string
}

// Error formats task validation details with optional source line, task ID, and field name.
func (e *TaskError) Error() string {
	var b strings.Builder
	if e.Line > 0 {
		fmt.Fprintf(&b, "line %d: ", e.Line)
	}
	if e.ID != "" {
		fmt.Fprintf(&b, "task %q: ", e.ID)
	} else {
		b.WriteString("task: ")
	}
	if e.Field != "" {
		b.WriteString(e.Field)
		b.WriteString(": ")
	}
	b.WriteString(e.Msg)
	return b.String()
}

var (
	taskIDRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@+=-]{0,127}$`)
	commitRe   = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./@+=^~{}-]{0,199}$`)
	blobRefRe  = regexp.MustCompile(`^blob:[0-9a-f]{64}$`)
	tagRe      = regexp.MustCompile(`^[^\s,\x00]+$`)
	scpURLRe   = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9._-]+:[^\s]+$`)
	knownKinds = map[string]bool{
		rl.TaskFix: true, rl.TaskFeature: true, rl.TaskRefactor: true,
		rl.TaskSwarm: true, rl.TaskRecall: true, rl.TaskCompaction: true,
	}
)

const (
	maxPromptBytes = 1 << 20
	maxSetupCmds   = 64
	maxTimeoutS    = 7 * 24 * 3600
)

// ValidTaskID reports whether id is usable as a path component of the run
// directory ("<runs>/<run>/<task>/<sample>"): no separators, no dot segments.
func ValidTaskID(id string) bool { return taskIDRe.MatchString(id) }

// ValidateTask checks one task and returns every problem found (joined), each
// a *TaskError naming the field. It does not touch the filesystem or network.
func ValidateTask(t rl.Task) error {
	errs := validateTask(t)
	if len(errs) == 0 {
		return nil
	}
	out := make([]error, len(errs))
	for i, e := range errs {
		out[i] = e
	}
	return errors.Join(out...)
}

func validateTask(t rl.Task) []*TaskError {
	var errs []*TaskError
	add := func(field, format string, args ...any) {
		errs = append(errs, &TaskError{ID: t.ID, Field: field, Msg: fmt.Sprintf(format, args...)})
	}

	if t.ID == "" {
		add("id", "is required")
	} else if !taskIDRe.MatchString(t.ID) {
		add("id", "must match %s (it becomes a directory name)", taskIDRe)
	}
	if !knownKinds[t.Kind] {
		add("kind", "unknown kind %q (want fix, feature, refactor, swarm, recall or compaction)", t.Kind)
	}
	if strings.TrimSpace(t.Prompt) == "" {
		add("prompt", "is required")
	} else if len(t.Prompt) > maxPromptBytes {
		add("prompt", "is larger than %d bytes", maxPromptBytes)
	}

	// repo
	if t.Repo.Path == "" && t.Repo.URL == "" {
		add("repo", "needs a path or a url")
	}
	if strings.ContainsRune(t.Repo.Path, 0) {
		add("repo.path", "contains a NUL byte")
	}
	if t.Repo.URL != "" {
		if err := validRepoURL(t.Repo.URL); err != nil {
			add("repo.url", "%v", err)
		}
	}
	switch {
	case t.Repo.Commit == "":
		add("repo.commit", "is required")
	case !commitRe.MatchString(t.Repo.Commit):
		// A leading '-' would turn the value into a git option.
		add("repo.commit", "%q is not a commit id or ref name", t.Repo.Commit)
	}
	if t.Repo.Subdir != "" {
		if err := validRelPath(t.Repo.Subdir); err != nil {
			add("repo.subdir", "%v", err)
		}
	}

	// setup
	if len(t.Setup) > maxSetupCmds {
		add("setup", "has %d commands (max %d)", len(t.Setup), maxSetupCmds)
	}
	for i, c := range t.Setup {
		if strings.TrimSpace(c) == "" {
			add(fmt.Sprintf("setup[%d]", i), "is empty")
		} else if strings.ContainsRune(c, 0) {
			add(fmt.Sprintf("setup[%d]", i), "contains a NUL byte")
		}
	}

	// team
	switch t.Team.Mode {
	case "", "single":
		if t.Team.Agents > 1 {
			add("team.agents", "is %d but team.mode is %q", t.Team.Agents, t.Team.Mode)
		}
	case "swarm":
	default:
		add("team.mode", "unknown mode %q (want single or swarm)", t.Team.Mode)
	}
	if t.Team.Agents < 0 {
		add("team.agents", "must not be negative")
	}
	seenRole := map[string]bool{}
	for i, r := range t.Team.Roles {
		switch {
		case strings.TrimSpace(r) == "":
			add(fmt.Sprintf("team.roles[%d]", i), "is empty")
		case seenRole[r]:
			add(fmt.Sprintf("team.roles[%d]", i), "duplicate role %q", r)
		}
		seenRole[r] = true
	}

	// budget
	b := t.Budget
	if b.Steps < 0 {
		add("budget.steps", "must not be negative")
	}
	if b.Requests < 0 {
		add("budget.requests", "must not be negative")
	}
	if b.WallS < 0 {
		add("budget.wall_s", "must not be negative")
	}
	if b.ContextWindow < 0 {
		add("budget.context_window", "must not be negative")
	}
	if b.ITE < 0 || math.IsNaN(b.ITE) || math.IsInf(b.ITE, 0) {
		add("budget.ite", "must be a finite, non-negative number")
	}

	// tags
	for i, tag := range t.Tags {
		if !tagRe.MatchString(tag) {
			add(fmt.Sprintf("tags[%d]", i), "%q must be non-empty without whitespace or commas", tag)
		}
	}

	errs = append(errs, validateVerifier(t)...)
	return errs
}

func validateVerifier(t rl.Task) []*TaskError {
	var errs []*TaskError
	add := func(field, format string, args ...any) {
		errs = append(errs, &TaskError{ID: t.ID, Field: "verifier." + field, Msg: fmt.Sprintf(format, args...)})
	}
	v := t.Verifier
	hasExpect := HasExpect(v)

	switch {
	case strings.TrimSpace(v.Cmd) == "" && !hasExpect:
		add("cmd", "is required (a recall task may use verifier.expect instead)")
	case strings.ContainsRune(v.Cmd, 0):
		add("cmd", "contains a NUL byte")
	}
	if t.Kind == rl.TaskRecall && !hasExpect {
		add("expect", "is required for a recall task")
	}
	if v.TimeoutS < 0 || v.TimeoutS > maxTimeoutS {
		add("timeout_s", "must be between 0 and %d", maxTimeoutS)
	}
	if _, err := parsePassMode(v.Pass); err != nil {
		add("pass", "%v", err)
	}
	for name, ref := range v.Hidden {
		field := fmt.Sprintf("hidden[%q]", name)
		if err := validRelPath(name); err != nil {
			add(field, "bad path: %v", err)
		}
		switch {
		case strings.HasPrefix(ref, "text:"):
		case blobRefRe.MatchString(ref):
		default:
			add(field, "value must be \"text:<content>\" or \"blob:<64 hex digits>\"")
		}
	}
	for i, g := range v.Protected {
		if err := ValidateGlob(g); err != nil {
			add(fmt.Sprintf("protected[%d]", i), "%v", err)
		}
	}
	if hasExpect {
		if _, err := ParseExpect(v.Expect); err != nil {
			add("expect", "%v", err)
		}
	}
	return errs
}

func validRepoURL(u string) error {
	if strings.HasPrefix(u, "-") {
		return errors.New("must not start with '-'")
	}
	if strings.ContainsAny(u, "\x00\r\n\t ") {
		return errors.New("must not contain whitespace or control characters")
	}
	if scpURLRe.MatchString(u) {
		return nil
	}
	p, err := url.Parse(u)
	if err != nil {
		return fmt.Errorf("unparseable: %v", err)
	}
	// "ext::" and other exotic transports execute commands; only well-known
	// network and file transports are accepted.
	switch p.Scheme {
	case "https", "http", "ssh", "git", "file":
		if p.Scheme != "file" && p.Host == "" {
			return errors.New("has no host")
		}
		return nil
	}
	return fmt.Errorf("unsupported scheme %q", p.Scheme)
}

// ValidateTasks validates every task and additionally requires ids to be
// unique. All problems are reported together.
func ValidateTasks(tasks []rl.Task) error {
	return validateAll(tasks, nil)
}

func validateAll(tasks []rl.Task, lines []int) error {
	var errs []error
	seen := map[string]int{}
	for i, t := range tasks {
		line := 0
		if i < len(lines) {
			line = lines[i]
		}
		for _, e := range validateTask(t) {
			e.Line = line
			errs = append(errs, e)
		}
		if t.ID != "" {
			if first, dup := seen[t.ID]; dup {
				errs = append(errs, &TaskError{Line: line, ID: t.ID, Field: "id",
					Msg: fmt.Sprintf("duplicate id (first used by entry %d)", first+1)})
			} else {
				seen[t.ID] = i
			}
		}
	}
	return joinLimited(errs, 25)
}

// joinLimited joins at most limit errors and a count of remaining problems; no errors returns nil
// and limit must be nonnegative.
func joinLimited(errs []error, limit int) error {
	if len(errs) == 0 {
		return nil
	}
	if len(errs) > limit {
		extra := len(errs) - limit
		errs = append(errs[:limit:limit], fmt.Errorf("... and %d more problems", extra))
	}
	return errors.Join(errs...)
}

// ---- pass modes and Expect ----

// HasExpect reports whether the verifier carries an answer check ("null" and
// whitespace count as absent).
func HasExpect(v rl.Verifier) bool {
	e := bytes.TrimSpace(v.Expect)
	return len(e) > 0 && string(e) != "null"
}

// PassKind is a parsed Verifier.Pass.
type PassKind int

const (
	PassExit0 PassKind = iota
	PassRegex
	PassJSONScore
)

// PassMode is a parsed Verifier.Pass value.
type PassMode struct {
	Kind  PassKind
	Regex *regexp.Regexp // for PassRegex
}

// String names regex and JSON-score verification modes, defaulting other values to exit0.
func (m PassMode) String() string {
	switch m.Kind {
	case PassRegex:
		return "regex"
	case PassJSONScore:
		return "json-score"
	}
	return "exit0"
}

// parsePassMode understands "", "exit0", "json-score" and "regex:<re>". The
// pattern is compiled with (?m) so ^ and $ anchor at line boundaries, which is
// what "regex on stdout" means to anyone writing "^ok\s". RE2 guarantees
// linear-time matching, so a hostile pattern in a task file cannot hang the
// verifier.
func parsePassMode(s string) (PassMode, error) {
	switch {
	case s == "" || s == "exit0":
		return PassMode{Kind: PassExit0}, nil
	case s == "json-score":
		return PassMode{Kind: PassJSONScore}, nil
	case strings.HasPrefix(s, "regex:"):
		expr := strings.TrimPrefix(s, "regex:")
		if expr == "" {
			return PassMode{}, errors.New("regex: needs a pattern")
		}
		re, err := regexp.Compile("(?m)" + expr)
		if err != nil {
			return PassMode{}, fmt.Errorf("bad regex: %v", err)
		}
		return PassMode{Kind: PassRegex, Regex: re}, nil
	}
	return PassMode{}, fmt.Errorf("unknown pass mode %q (want exit0, json-score or regex:<re>)", s)
}

// Expect is the decoded Verifier.Expect of a recall task: the final answer must
// contain every listed string.
type Expect struct {
	Contains []string `json:"contains"`
	// Fold makes the comparison case-insensitive.
	Fold bool `json:"fold,omitempty"`
}

// ParseExpect strictly decodes Verifier.Expect. Unknown keys and an empty
// "contains" list are errors: a typo must not turn into a check that passes
// vacuously.
func ParseExpect(raw json.RawMessage) (Expect, error) {
	var e Expect
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&e); err != nil {
		return Expect{}, fmt.Errorf("bad expect: %v", err)
	}
	if dec.More() {
		return Expect{}, errors.New("bad expect: trailing data")
	}
	if len(e.Contains) == 0 {
		return Expect{}, errors.New("bad expect: \"contains\" must list at least one string")
	}
	for i, c := range e.Contains {
		if c == "" {
			return Expect{}, fmt.Errorf("bad expect: contains[%d] is empty (it would match any answer)", i)
		}
	}
	return e, nil
}

// ---- JSONL I/O ----

const maxTaskLine = 64 << 20 // a task may embed sizeable hidden files as text:

// LoadTasks reads a tasks file: one JSON task per line, strictly decoded
// (unknown fields are errors), then validated. Blank lines are skipped;
// comments are not supported, because a task file is data.
func LoadTasks(p string) ([]rl.Task, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	tasks, err := ReadTasks(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	return tasks, nil
}

// ReadTasks is LoadTasks for an io.Reader.
func ReadTasks(r io.Reader) ([]rl.Task, error) {
	br := bufio.NewReaderSize(r, 1<<20)
	var (
		tasks []rl.Task
		lines []int
		errs  []error
		n     int
	)
	for {
		line, err := readLine(br, maxTaskLine)
		if err != nil && err != io.EOF {
			return nil, err
		}
		if len(line) > 0 || err == nil {
			n++
			line = bytes.TrimSpace(line)
			if n == 1 {
				line = bytes.TrimPrefix(line, []byte("\xef\xbb\xbf"))
			}
			if len(line) > 0 {
				t, derr := decodeTaskLine(line)
				if derr != nil {
					errs = append(errs, &TaskError{Line: n, ID: t.ID, Msg: derr.Error()})
				} else {
					tasks = append(tasks, t)
					lines = append(lines, n)
				}
			}
		}
		if err == io.EOF {
			break
		}
	}
	if len(errs) == 0 {
		if err := validateAll(tasks, lines); err != nil {
			return nil, err
		}
		return tasks, nil
	}
	return nil, joinLimited(errs, 25)
}

// readLine returns one line without its newline, failing on lines longer than
// max so a corrupt file cannot exhaust memory.
func readLine(br *bufio.Reader, max int) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := br.ReadSlice('\n')
		if len(buf)+len(chunk) > max {
			return nil, fmt.Errorf("line longer than %d bytes", max)
		}
		buf = append(buf, chunk...)
		if err == bufio.ErrBufferFull {
			continue
		}
		return bytes.TrimRight(buf, "\r\n"), err
	}
}

// decodeTaskLine decodes a task with unknown-field rejection and rejects additional values
// detected after the first JSON value.
func decodeTaskLine(line []byte) (rl.Task, error) {
	var t rl.Task
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&t); err != nil {
		return t, fmt.Errorf("invalid JSON: %v", err)
	}
	if dec.More() {
		return t, errors.New("invalid JSON: more than one value on a line")
	}
	return t, nil
}

// WriteTasks validates tasks and writes them to path as JSONL, atomically.
func WriteTasks(p string, tasks []rl.Task) error {
	var buf bytes.Buffer
	if err := EncodeTasks(&buf, tasks); err != nil {
		return err
	}
	return atomicWriteFile(p, buf.Bytes(), 0o644)
}

// EncodeTasks validates tasks and writes them as JSONL to w.
func EncodeTasks(w io.Writer, tasks []rl.Task) error {
	if err := ValidateTasks(tasks); err != nil {
		return err
	}
	bw := bufio.NewWriter(w)
	enc := json.NewEncoder(bw)
	enc.SetEscapeHTML(false)
	for _, t := range tasks {
		if err := enc.Encode(t); err != nil {
			return err
		}
	}
	return bw.Flush()
}

// ---- selection and splitting ----

// hash64 is the package's only source of "randomness" for selection and
// splitting: a keyed SHA-256, so results depend on nothing but the inputs, never
// on the Go version, map order or the input order. A dataset split that changes
// when the toolchain is upgraded would silently leak held-out tasks into
// training.
func hash64(seed int64, parts ...string) uint64 {
	h := sha256.New()
	var sb [8]byte
	binary.BigEndian.PutUint64(sb[:], uint64(seed))
	h.Write(sb[:])
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	sum := h.Sum(nil)
	return binary.BigEndian.Uint64(sum[:8])
}

// Filter narrows tasks.
//
//   - tags: a task must carry every listed tag; a tag prefixed with "!" must be
//     absent.
//   - ids: when non-empty only these ids pass; entries may use path.Match
//     wildcards ("mux-*").
//   - n > 0 keeps a deterministic pseudo-random subset of n tasks chosen by a
//     keyed hash of (seed, id), returned in the original order. Because the
//     choice depends only on the ids, adding tasks to the file does not
//     reshuffle the ones already picked.
func Filter(tasks []rl.Task, tags, ids []string, n int, seed int64) []rl.Task {
	var out []rl.Task
	for _, t := range tasks {
		if !hasTags(t, tags) || !matchIDs(t.ID, ids) {
			continue
		}
		out = append(out, t)
	}
	if n > 0 && n < len(out) {
		type ranked struct {
			idx int
			key uint64
		}
		r := make([]ranked, len(out))
		for i, t := range out {
			r[i] = ranked{i, hash64(seed, "filter", t.ID)}
		}
		sort.Slice(r, func(a, b int) bool {
			if r[a].key != r[b].key {
				return r[a].key < r[b].key
			}
			return out[r[a].idx].ID < out[r[b].idx].ID
		})
		keep := make([]int, n)
		for i := 0; i < n; i++ {
			keep[i] = r[i].idx
		}
		sort.Ints(keep)
		sel := make([]rl.Task, n)
		for i, k := range keep {
			sel[i] = out[k]
		}
		out = sel
	}
	return out
}

// hasTags requires every positive tag and excludes every tag prefixed with an exclamation mark.
func hasTags(t rl.Task, want []string) bool {
	for _, w := range want {
		neg := strings.HasPrefix(w, "!")
		w = strings.TrimPrefix(w, "!")
		has := false
		for _, tag := range t.Tags {
			if tag == w {
				has = true
				break
			}
		}
		if has == neg {
			return false
		}
	}
	return true
}

// matchIDs accepts all IDs for an empty filter and otherwise matches exact IDs or valid path
// globs.
func matchIDs(id string, ids []string) bool {
	if len(ids) == 0 {
		return true
	}
	for _, p := range ids {
		if p == id {
			return true
		}
		if ok, err := path.Match(p, id); err == nil && ok {
			return true
		}
	}
	return false
}

// RepoKey identifies the repository a task belongs to, independent of commit,
// scheme and ".git" suffix, so all tasks of one project split together.
func RepoKey(t rl.Task) string {
	if t.Repo.URL != "" {
		return normalizeRepoURL(t.Repo.URL)
	}
	p := t.Repo.Path
	if p == "" {
		return ""
	}
	return path.Clean(strings.ReplaceAll(p, "\\", "/"))
}

// normalizeRepoURL collapses URL and SCP-style repository spellings into host/path identity,
// removing credentials, trailing slash, and .git suffix.
func normalizeRepoURL(u string) string {
	s := strings.TrimSpace(u)
	if scpURLRe.MatchString(s) { // git@host:org/repo.git
		at := strings.Index(s, "@")
		s = s[at+1:]
		s = strings.Replace(s, ":", "/", 1)
	} else if p, err := url.Parse(s); err == nil && p.Host != "" {
		s = p.Hostname() + "/" + strings.TrimPrefix(p.Path, "/")
	}
	s = strings.TrimSuffix(strings.TrimRight(s, "/"), ".git")
	if i := strings.Index(s, "/"); i > 0 {
		s = strings.ToLower(s[:i]) + s[i:]
	}
	return s
}

// SplitSpec is a parsed "train:0.9,val:0.1" specification.
type SplitSpec []struct {
	Name   string
	Weight float64
}

// ParseSplitSpec parses "name:weight,name:weight". Weights are relative
// ("train:9,val:1" equals "train:0.9,val:0.1").
func ParseSplitSpec(spec string) (SplitSpec, error) {
	var out SplitSpec
	seen := map[string]bool{}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, w, ok := strings.Cut(part, ":")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			return nil, fmt.Errorf("split %q: want name:weight", part)
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(w), 64)
		if err != nil || f <= 0 || math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("split %q: weight must be a positive number", part)
		}
		if seen[name] {
			return nil, fmt.Errorf("split %q: duplicate name", name)
		}
		seen[name] = true
		out = append(out, struct {
			Name   string
			Weight float64
		}{name, f})
	}
	if len(out) == 0 {
		return nil, errors.New("empty split specification")
	}
	return out, nil
}

// Split partitions tasks into named parts by repository: every task of a
// repository lands in the same part, so the held-out parts contain no code the
// policy trained on. The assignment is deterministic and independent of the
// input order: repositories are ordered by a keyed hash and each goes to the
// part furthest below its target share (measured in tasks).
func Split(tasks []rl.Task, spec string, seed int64) (map[string][]rl.Task, error) {
	ps, err := ParseSplitSpec(spec)
	if err != nil {
		return nil, err
	}
	total := 0.0
	for _, p := range ps {
		total += p.Weight
	}
	byRepo := map[string][]int{}
	for i, t := range tasks {
		k := RepoKey(t)
		if k == "" {
			k = "\x00task:" + t.ID // no repo identity: each task is its own group
		}
		byRepo[k] = append(byRepo[k], i)
	}
	keys := make([]string, 0, len(byRepo))
	for k := range byRepo {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(a, b int) bool {
		ha, hb := hash64(seed, "split", keys[a]), hash64(seed, "split", keys[b])
		if ha != hb {
			return ha < hb
		}
		return keys[a] < keys[b]
	})
	target := make([]float64, len(ps))
	for i, p := range ps {
		target[i] = p.Weight / total * float64(len(tasks))
	}
	got := make([]float64, len(ps))
	out := make(map[string][]rl.Task, len(ps))
	for _, p := range ps {
		out[p.Name] = nil
	}
	assigned := make([][]int, len(ps))
	for _, k := range keys {
		best, bestDeficit := 0, math.Inf(-1)
		for i := range ps {
			if d := target[i] - got[i]; d > bestDeficit+1e-9 {
				best, bestDeficit = i, d
			}
		}
		assigned[best] = append(assigned[best], byRepo[k]...)
		got[best] += float64(len(byRepo[k]))
	}
	for i, p := range ps {
		idx := assigned[i]
		sort.Ints(idx) // keep the original task order within a part
		for _, j := range idx {
			out[p.Name] = append(out[p.Name], tasks[j])
		}
	}
	return out, nil
}

// TaskMeta is what generators record in rl.Task.Meta; Holdout reads it.
type TaskMeta struct {
	Commit     string `json:"commit,omitempty"`
	AuthorDate string `json:"author_date,omitempty"` // RFC 3339
}

// Holdout implements --holdout-repos and --holdout-after: it returns the tasks
// to train on and the tasks held out. A task is held out when its repository
// is listed (see RepoKey) or its commit's author date is after `after`. Tasks
// without a date are never held out by date, but Holdout reports them so a
// caller can decide whether that is acceptable.
func Holdout(tasks []rl.Task, repos []string, after time.Time) (train, held []rl.Task, undated int) {
	hold := map[string]bool{}
	for _, r := range repos {
		hold[normalizeRepoURL(r)] = true
		hold[path.Clean(strings.ReplaceAll(r, "\\", "/"))] = true
	}
	for _, t := range tasks {
		isHeld := hold[RepoKey(t)]
		if !after.IsZero() {
			var m TaskMeta
			if len(t.Meta) > 0 {
				_ = json.Unmarshal(t.Meta, &m)
			}
			if d, err := time.Parse(time.RFC3339, m.AuthorDate); err == nil {
				if d.After(after) {
					isHeld = true
				}
			} else {
				undated++
			}
		}
		if isHeld {
			held = append(held, t)
		} else {
			train = append(train, t)
		}
	}
	return train, held, undated
}

// ---- contamination guard ----

// ExcludeSet is the training-set list an evaluation must stay clear of.
type ExcludeSet struct {
	IDs   map[string]bool
	Pairs map[string]bool // "repo-key@commit" of training tasks that carried repo info
}

// LoadExcludeList reads a training-set list for `rl eval --exclude-file`. Each
// non-empty line is either a bare task id or a JSON value: a task object
// (its id and repo/commit are used), or a JSON string.
func LoadExcludeList(p string) (*ExcludeSet, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	ex := &ExcludeSet{IDs: map[string]bool{}, Pairs: map[string]bool{}}
	br := bufio.NewReaderSize(f, 1<<20)
	for n := 1; ; n++ {
		line, err := readLine(br, maxTaskLine)
		if err != nil && err != io.EOF {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		s := strings.TrimSpace(string(line))
		if s != "" && !strings.HasPrefix(s, "#") {
			switch s[0] {
			case '{':
				var t struct {
					ID   string `json:"id"`
					Repo struct {
						Path   string `json:"path"`
						URL    string `json:"url"`
						Commit string `json:"commit"`
					} `json:"repo"`
				}
				if jerr := json.Unmarshal([]byte(s), &t); jerr != nil {
					return nil, fmt.Errorf("%s:%d: %v", p, n, jerr)
				}
				if t.ID != "" {
					ex.IDs[t.ID] = true
				}
				key := RepoKey(rl.Task{Repo: rl.RepoSpec{Path: t.Repo.Path, URL: t.Repo.URL}})
				if key != "" && t.Repo.Commit != "" {
					ex.Pairs[key+"@"+t.Repo.Commit] = true
				}
			case '"':
				var id string
				if jerr := json.Unmarshal([]byte(s), &id); jerr != nil {
					return nil, fmt.Errorf("%s:%d: %v", p, n, jerr)
				}
				ex.IDs[id] = true
			default:
				ex.IDs[s] = true
			}
		}
		if err == io.EOF {
			break
		}
	}
	return ex, nil
}

// ExcludeFromTasks builds an ExcludeSet from tasks already in memory.
func ExcludeFromTasks(tasks []rl.Task) *ExcludeSet {
	ex := &ExcludeSet{IDs: map[string]bool{}, Pairs: map[string]bool{}}
	for _, t := range tasks {
		ex.IDs[t.ID] = true
		if k := RepoKey(t); k != "" && t.Repo.Commit != "" {
			ex.Pairs[k+"@"+t.Repo.Commit] = true
		}
	}
	return ex
}

// Overlap returns the ids of tasks that appear in the exclusion set: same id,
// or same repository at the same commit (a renamed copy of a training task is
// still contamination). The result is sorted.
func (e *ExcludeSet) Overlap(tasks []rl.Task) []string {
	if e == nil {
		return nil
	}
	var out []string
	for _, t := range tasks {
		hit := e.IDs[t.ID]
		if !hit {
			if k := RepoKey(t); k != "" && e.Pairs[k+"@"+t.Repo.Commit] {
				hit = true
			}
		}
		if hit {
			out = append(out, t.ID)
		}
	}
	sort.Strings(out)
	return out
}

// shortHash is a stable, short hex digest for ids derived from content.
func shortHash(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}
