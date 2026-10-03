package fs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"regexp/syntax"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// Grep implements the grep tool.
//
// Ripgrep, when installed, is used only to decide which files contain a match
// (it is far faster at rejecting the thousands that do not). Everything the
// model sees (line numbers, context, ordering, truncation, binary and size
// handling) is produced by the same Go code for both engines, which is what
// makes their output identical by construction rather than by careful imitation.
// If ripgrep fails for any reason (it rejects some patterns Go accepts, such as
// interface{}) the pure-Go walker takes over silently.
//
// One residual difference is accepted: ripgrep decides "does this file contain a
// match" with its own Unicode-aware regex dialect, so on files that are not valid
// UTF-8 a pattern using "." or a negated class can match in Go and not in
// ripgrep. Patterns where the dialects visibly differ (see rgCompatible) and
// multiline searches never go to ripgrep.
type Grep struct {
	// RipgrepPath overrides the PATH lookup for the rg binary.
	RipgrepPath string
	// DisableRipgrep forces the pure-Go engine.
	DisableRipgrep bool
}

// Spec implements tools.Tool.
func (Grep) Spec() core.ToolSpec {
	return core.ToolSpec{
		Name: "grep",
		Description: "Search file contents with a regular expression (RE2 syntax: no lookaround or backreferences). " +
			"Respects .gitignore; skips binary files and files over 4 MB. `output_mode`: content (default; path:line:text), " +
			"files_with_matches, or count. Filter files with `glob` (e.g. *.go or *.{ts,tsx}); -i ignores case; " +
			"-A/-B/-C add context lines; `multiline` lets a pattern span lines; `head_limit` caps output lines (default 250, 0 = no cap). " +
			"Results are sorted by path.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"pattern":{"type":"string","description":"Regular expression"},` +
			`"path":{"type":"string","description":"File or directory (default: working directory)"},` +
			`"glob":{"type":"string","description":"Only search files matching this glob"},` +
			`"output_mode":{"type":"string","enum":["content","files_with_matches","count"]},` +
			`"-i":{"type":"boolean","description":"Case insensitive"},` +
			`"-n":{"type":"boolean","description":"Show line numbers (default true)"},` +
			`"-A":{"type":"integer","description":"Lines after each match"},` +
			`"-B":{"type":"integer","description":"Lines before each match"},` +
			`"-C":{"type":"integer","description":"Lines before and after each match"},` +
			`"multiline":{"type":"boolean","description":"Allow matches to span lines"},` +
			`"head_limit":{"type":"integer","description":"Max output lines (default 250)"}},` +
			`"required":["pattern"]}`),
		ReadOnly: true,
	}
}

const (
	defaultGrepLimit = 250
	hardGrepLimit    = 100_000
	maxGrepFileBytes = 4 << 20
	maxGrepLineChars = 300
	maxGrepContext   = 100
	rgTimeout        = 60 * time.Second
	rgMaxOutput      = 64 << 20
)

type grepArgs struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path"`
	Glob       string `json:"glob"`
	OutputMode string `json:"output_mode"`
	I          *bool  `json:"-i"`
	N          *bool  `json:"-n"`
	A          intArg `json:"-A"`
	B          intArg `json:"-B"`
	C          intArg `json:"-C"`
	Context    intArg `json:"context"`
	Multiline  bool   `json:"multiline"`
	HeadLimit  intArg `json:"head_limit"`
}

// Run implements tools.Tool.
func (g Grep) Run(ctx context.Context, c *tools.Call) (*tools.Result, error) {
	k := begin(ctx, c, "grep")
	var a grepArgs
	if r := k.decode(&a); r != nil {
		return r, nil
	}
	if a.Pattern == "" {
		return k.fail("pattern is required"), nil
	}
	mode := a.OutputMode
	if mode == "" {
		mode = "content"
	}
	if mode != "content" && mode != "files_with_matches" && mode != "count" {
		return k.fail("output_mode must be content, files_with_matches or count (got %q)", clip(mode, 40)), nil
	}
	lineNumbers := a.N == nil || *a.N
	ignoreCase := a.I != nil && *a.I

	before, after := 0, 0
	ctxN := a.C
	if !ctxN.Set {
		ctxN = a.Context
	}
	for _, v := range []struct {
		name string
		val  intArg
	}{{"-A", a.A}, {"-B", a.B}, {"-C", ctxN}} {
		if v.val.Set && v.val.V < 0 {
			return k.fail("%s must not be negative", v.name), nil
		}
	}
	if ctxN.Set {
		before, after = ctxN.V, ctxN.V
	}
	if a.A.Set {
		after = a.A.V
	}
	if a.B.Set {
		before = a.B.V
	}
	if before > maxGrepContext {
		before = maxGrepContext
	}
	if after > maxGrepContext {
		after = maxGrepContext
	}
	if mode != "content" {
		before, after = 0, 0
	}

	limit := defaultGrepLimit
	if a.HeadLimit.Set {
		if a.HeadLimit.V < 0 {
			return k.fail("head_limit must not be negative (0 means no cap)"), nil
		}
		limit = a.HeadLimit.V
		if limit == 0 {
			limit = hardGrepLimit
		}
	}
	if limit > hardGrepLimit {
		limit = hardGrepLimit
	}

	re, msg := compileGrepPattern(a.Pattern, ignoreCase, a.Multiline)
	if msg != "" {
		return k.fail("%s", msg), nil
	}

	searchPath := a.Path
	if searchPath == "" {
		searchPath = "."
	}
	base, disp, emsg := k.resolveArg(searchPath)
	if emsg != "" {
		return k.fail("%s", emsg), nil
	}
	if r := k.authorize("grep "+clip(a.Pattern, 80)+" in "+disp, false, perm.RiskLow, base); r != nil {
		return r, nil
	}
	fi, err := os.Stat(base)
	if err != nil {
		return k.statFailure(base, disp, err), nil
	}

	defer k.bounded()()

	var files []string
	if fi.IsDir() {
		globs := splitGlobList(a.Glob)
		// Multiline is left to the Go engine: ripgrep does not treat "\n" in a
		// pattern as matching the CRLF of a Windows-style file, and multiline
		// searches are the rare case where its speed matters least.
		if rg := g.ripgrep(); rg != "" && !a.Multiline && rgCompatible(a.Pattern) {
			if list, ok := k.rgCandidates(rg, base, a, globs, ignoreCase); ok {
				files = list
			}
		}
		if files == nil {
			files = k.walkCandidates(base, globs)
		}
		sort.Strings(files)
	} else {
		files = []string{base}
	}
	if k.ctx.Err() != nil {
		return k.fail("search timed out or was cancelled; narrow path or pattern"), nil
	}

	opts := searchOpts{re: re, mode: mode, multiline: a.Multiline, before: before, after: after}
	out := &grepOutput{k: k, mode: mode, lineNumbers: lineNumbers, limit: limit, context: before > 0 || after > 0}
	searchFiles(k.ctx, files, opts, out.consume)
	if k.ctx.Err() != nil && !out.full {
		return k.fail("search timed out or was cancelled; narrow path or pattern"), nil
	}

	if n := len(out.lines); n > 0 && out.lines[n-1] == "--" {
		out.lines = out.lines[:n-1] // a group separator with nothing after it
	}
	if len(out.lines) == 0 {
		return k.ok("No matches found."), nil
	}
	text := strings.Join(out.lines, "\n")
	if out.full {
		text += fmt.Sprintf("\n[output truncated at head_limit=%d lines; narrow with path or glob, or raise head_limit]", limit)
	}
	return k.ok(text), nil
}

// rgCompatible says whether ripgrep may be trusted to pre-select files for the
// pattern. Ripgrep's regex engine is Unicode-aware where Go's \b \w \d \s are
// ASCII-only. For the positive classes that only makes ripgrep list more files
// (the Go pass filters them out again), but for word boundaries and negated
// classes it can make ripgrep skip a file Go would match ("\bfoo\b" in "éfoo").
// Such patterns are searched by the Go engine alone.
func rgCompatible(pattern string) bool {
	for _, bad := range []string{`\b`, `\B`, `\W`, `\D`, `\S`, `\P`, `[[:^`} {
		if strings.Contains(pattern, bad) {
			return false
		}
	}
	return true
}

func (g Grep) ripgrep() string {
	if g.DisableRipgrep {
		return ""
	}
	if g.RipgrepPath != "" {
		return g.RipgrepPath
	}
	p, err := exec.LookPath("rg")
	if err != nil {
		return ""
	}
	return p
}

// compileGrepPattern compiles the model's pattern. Line mode runs the regex on
// one line at a time, so a pattern that spells out a newline could never match;
// that is reported instead of silently returning nothing.
func compileGrepPattern(pattern string, ignoreCase, multiline bool) (*regexp.Regexp, string) {
	flags := ""
	if ignoreCase {
		flags += "i"
	}
	if multiline {
		flags += "m"
	}
	full := pattern
	if flags != "" {
		full = "(?" + flags + ")" + pattern
	}
	re, err := regexp.Compile(full)
	if err != nil {
		return nil, fmt.Sprintf("invalid regex %q: %s (RE2 syntax: no lookaround or backreferences; escape literal ( ) [ ] { } with a backslash)",
			clip(pattern, 200), strings.TrimPrefix(err.Error(), "error parsing regexp: "))
	}
	if !multiline {
		if tree, perr := syntax.Parse(pattern, syntax.Perl); perr == nil && matchesNewlineLiteral(tree) {
			return nil, "pattern contains a newline, which never matches because lines are searched one at a time; set multiline=true to match across lines"
		}
	}
	return re, ""
}

func matchesNewlineLiteral(re *syntax.Regexp) bool {
	if re.Op == syntax.OpLiteral {
		for _, r := range re.Rune {
			if r == '\n' {
				return true
			}
		}
	}
	for _, sub := range re.Sub {
		if matchesNewlineLiteral(sub) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Candidate selection

// rgCandidates asks ripgrep which files contain a match. ok is false when the
// caller should fall back to the pure-Go walk.
//
// The flags pin ripgrep to the behaviour the Go walker implements: hidden files
// searched, .git skipped, only .gitignore files honoured (and honoured even
// outside a git repository, which is how the tests and scratch dirs work),
// files over 4 MB skipped, CRLF treated as a line end, no user config.
func (k *call) rgCandidates(rg, base string, a grepArgs, globs []string, ignoreCase bool) ([]string, bool) {
	args := []string{
		"--no-config", "--files-with-matches", "--null", "--color=never",
		"--hidden", "--no-require-git", "--no-ignore-dot", "--no-ignore-global", "--no-ignore-exclude",
		"--max-filesize=4M", "--crlf", "--glob=!.git",
	}
	if ignoreCase {
		args = append(args, "--ignore-case")
	}
	for _, gl := range globs {
		args = append(args, "--glob="+gl)
	}
	args = append(args, "--regexp="+a.Pattern, "--", ".")

	ctx, cancel := context.WithTimeout(k.ctx, rgTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, rg, args...)
	cmd.Dir = base
	// On overflow the process is killed at once: an undrained pipe would leave it
	// blocked in write until the timeout.
	stdout := cappedBuffer{max: rgMaxOutput, kill: cancel}
	cmd.Stdout = &stdout
	cmd.Stderr = io.Discard
	err := cmd.Run()
	if stdout.overflow {
		return nil, false
	}
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			return nil, false
		}
		switch ee.ExitCode() {
		case 1: // no file matched
			return []string{}, true
		case 2:
			// Some paths were unreadable but results exist: use them. No results
			// means ripgrep rejected something (its regex dialect differs from
			// Go's); let the Go engine decide.
			if stdout.Len() == 0 {
				return nil, false
			}
		default:
			return nil, false
		}
	}
	out := []string{}
	for _, p := range bytes.Split(stdout.Bytes(), []byte{0}) {
		if len(p) == 0 {
			continue
		}
		rel := strings.TrimPrefix(string(p), "./")
		out = append(out, base+string(os.PathSeparator)+rel)
	}
	return out, true
}

// walkCandidates lists every regular file the ignore rules and the glob filter
// admit, for the pure-Go engine.
func (k *call) walkCandidates(base string, globs []string) []string {
	files := []string{}
	w := k.newWalker(base, true)
	if len(globs) > 0 {
		ov := compileOverride(globs)
		w.exclude = ov.excludes
	}
	w.run(func(e entry) walkAction {
		if e.kind == kindFile {
			files = append(files, e.path)
		}
		return walkContinue
	})
	return files
}

// cappedBuffer collects at most max bytes. It deliberately does not embed
// bytes.Buffer: the embedded ReadFrom would be picked by io.Copy and bypass the
// cap.
type cappedBuffer struct {
	buf      bytes.Buffer
	max      int
	overflow bool
	kill     func()
}

// Write rejects a chunk that would exceed the grep output cap, marks overflow, and invokes
// optional process cancellation.
func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.buf.Len()+len(p) > b.max {
		b.overflow = true
		if b.kill != nil {
			b.kill()
		}
		return 0, io.ErrShortWrite
	}
	return b.buf.Write(p)
}

// Len returns the number of retained bytes in the capped grep buffer.
func (b *cappedBuffer) Len() int { return b.buf.Len() }

// Bytes returns the retained grep bytes, sharing the buffer's storage.
func (b *cappedBuffer) Bytes() []byte { return b.buf.Bytes() }

// ---------------------------------------------------------------------------
// Searching

type searchOpts struct {
	re        *regexp.Regexp
	mode      string
	multiline bool
	before    int
	after     int
}

type lineRec struct {
	n     int
	text  string
	match bool
}

type fileResult struct {
	path    string
	matches int // matching lines
	recs    []lineRec
}

// searchFiles searches files in order, several at a time, and hands the results
// to consume in the same order (so output is deterministic) until it returns
// false. Work is done a small batch ahead, so stopping early wastes at most one
// batch.
func searchFiles(ctx context.Context, files []string, o searchOpts, consume func(fileResult) bool) {
	workers := runtime.GOMAXPROCS(0)
	if workers > 8 {
		workers = 8
	}
	if workers < 1 {
		workers = 1
	}
	batch := workers * 4
	for i := 0; i < len(files); i += batch {
		if ctx.Err() != nil {
			return
		}
		end := i + batch
		if end > len(files) {
			end = len(files)
		}
		results := make([]fileResult, end-i)
		if workers == 1 || end-i == 1 {
			for j := i; j < end; j++ {
				results[j-i] = searchFile(files[j], o)
			}
		} else {
			var wg sync.WaitGroup
			sem := make(chan struct{}, workers)
			for j := i; j < end; j++ {
				wg.Add(1)
				sem <- struct{}{}
				go func(j int) {
					defer wg.Done()
					defer func() { <-sem }()
					results[j-i] = searchFile(files[j], o)
				}(j)
			}
			wg.Wait()
		}
		for _, r := range results {
			if r.matches > 0 && !consume(r) {
				return
			}
		}
	}
}

// searchFile searches one file. Files that are unreadable, over 4 MB, not
// regular, or binary (a NUL byte anywhere) yield no result: a hit inside binary
// data is noise to a model, and ripgrep makes the same call.
func searchFile(path string, o searchOpts) fileResult {
	res := fileResult{path: path}
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxGrepFileBytes {
		return res
	}
	f, _, err := openRegular(path)
	if err != nil {
		return res
	}
	data, err := io.ReadAll(io.LimitReader(f, maxGrepFileBytes+1))
	f.Close()
	if err != nil || len(data) > maxGrepFileBytes || bytes.IndexByte(data, 0) >= 0 {
		return res
	}
	if o.multiline && bytes.Contains(data, []byte("\r\n")) {
		// Multiline patterns are written against LF; fold CRLF so they match.
		data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	}
	// A literal every match must start with rules a file out without running the
	// automaton over it line by line.
	if prefix, _ := o.re.LiteralPrefix(); prefix != "" && !bytes.Contains(data, []byte(prefix)) {
		return res
	}
	if o.multiline {
		return searchMultiline(res, data, o)
	}
	return searchLines(res, data, o)
}

func searchLines(res fileResult, data []byte, o searchOpts) fileResult {
	var matchLines []int
	lineNo := 0
	for pos := 0; pos < len(data); {
		end := bytes.IndexByte(data[pos:], '\n')
		var line []byte
		if end < 0 {
			line = data[pos:]
			pos = len(data)
		} else {
			line = data[pos : pos+end]
			pos += end + 1
			if n := len(line); n > 0 && line[n-1] == '\r' {
				line = line[:n-1]
			}
		}
		lineNo++
		if o.re.Match(line) {
			res.matches++
			if o.mode == "files_with_matches" {
				return res
			}
			if o.mode == "content" {
				matchLines = append(matchLines, lineNo)
			}
		}
	}
	if o.mode == "content" && len(matchLines) > 0 {
		res.recs = buildRecords(data, matchLines, o.before, o.after)
	}
	return res
}

func searchMultiline(res fileResult, data []byte, o searchOpts) fileResult {
	if o.mode == "files_with_matches" {
		if o.re.Match(data) {
			res.matches = 1
		}
		return res
	}
	var matchLines []int
	seen := map[int]bool{}
	line, pos := 1, 0
	for _, m := range o.re.FindAllIndex(data, -1) {
		s, e := m[0], m[1]
		if e == s && s == len(data) && len(data) > 0 && data[len(data)-1] == '\n' {
			continue
		}
		line += bytes.Count(data[pos:s], []byte{'\n'})
		pos = s
		last := line
		if e > s {
			last = line + bytes.Count(data[s:e-1], []byte{'\n'})
		}
		for l := line; l <= last; l++ {
			if !seen[l] {
				seen[l] = true
				matchLines = append(matchLines, l)
			}
		}
	}
	res.matches = len(matchLines)
	if o.mode == "content" && len(matchLines) > 0 {
		res.recs = buildRecords(data, matchLines, o.before, o.after)
	}
	return res
}

// buildRecords turns match line numbers (ascending) into the lines to print:
// each match plus its context, with overlapping and touching windows merged.
func buildRecords(data []byte, matchLines []int, before, after int) []lineRec {
	lines := bytes.Split(data, []byte{'\n'})
	if n := len(lines); n > 0 && len(lines[n-1]) == 0 {
		lines = lines[:n-1]
	}
	isMatch := make(map[int]bool, len(matchLines))
	for _, m := range matchLines {
		isMatch[m] = true
	}
	var recs []lineRec
	next := 1 // first line number not yet emitted
	for _, m := range matchLines {
		from, to := m-before, m+after
		if from < next {
			from = next
		}
		if to > len(lines) {
			to = len(lines)
		}
		for l := from; l <= to; l++ {
			recs = append(recs, lineRec{n: l, text: string(bytes.TrimSuffix(lines[l-1], []byte{'\r'})), match: isMatch[l]})
		}
		if to+1 > next {
			next = to + 1
		}
	}
	return recs
}

// ---------------------------------------------------------------------------
// Output

type grepOutput struct {
	k           *call
	mode        string
	lineNumbers bool
	limit       int
	context     bool
	lines       []string
	full        bool
	groups      int
}

// add appends one grep output line when capacity remains and otherwise marks the output full.
func (o *grepOutput) add(s string) bool {
	if len(o.lines) >= o.limit {
		o.full = true
		return false
	}
	o.lines = append(o.lines, s)
	return true
}

// consume formats one file's result. It returns false once the limit is hit.
func (o *grepOutput) consume(r fileResult) bool {
	name := o.k.display(r.path)
	switch o.mode {
	case "files_with_matches":
		return o.add(name)
	case "count":
		return o.add(name + ":" + strconv.Itoa(r.matches))
	}
	prev := 0
	for _, rec := range r.recs {
		if prev == 0 || rec.n != prev+1 {
			if o.context && o.groups > 0 {
				if !o.add("--") {
					return false
				}
			}
			o.groups++
		}
		prev = rec.n
		sep := "-"
		if rec.match {
			sep = ":"
		}
		line := name + sep
		if o.lineNumbers {
			line += strconv.Itoa(rec.n) + sep
		}
		line += grepText(rec.text)
		if !o.add(line) {
			return false
		}
	}
	return true
}

// grepText bounds one output line and makes it valid UTF-8.
func grepText(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "\uFFFD")
	}
	if len(s) > maxGrepLineChars {
		if n := utf8.RuneCountInString(s); n > maxGrepLineChars {
			return cutRunes(s, maxGrepLineChars) + fmt.Sprintf("… [+%d chars]", n-maxGrepLineChars)
		}
	}
	return s
}
