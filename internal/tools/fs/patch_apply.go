package fs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	iofs "io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// ApplyPatch implements the apply_patch tool.
type ApplyPatch struct{}

// Spec implements tools.Tool.
func (ApplyPatch) Spec() core.ToolSpec {
	return core.ToolSpec{
		Name: "apply_patch",
		Description: "Apply a multi-file patch atomically: every file is validated first, and either all changes are written or none. " +
			"Format: `*** Begin Patch`, then per file `*** Add File: path` (every line starts with +), `*** Delete File: path`, " +
			"or `*** Update File: path` (optional `*** Move to: newpath`) followed by hunks. " +
			"A hunk starts with `@@` (optionally followed by a line of the file that precedes it); its lines start with a space (context), - (remove) or + (add); " +
			"`*** End of File` may end a hunk. Finish with `*** End Patch`. Context lines must match the file.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"patch":{"type":"string","description":"The patch text"}},` +
			`"required":["patch"]}`),
	}
}

// docLine is one line of a file with its own terminator, so a patch can change
// some lines of a file with mixed line endings without touching the others.
type docLine struct {
	text string
	eol  string // "\n", "\r\n", or "" for a final line without newline
}

func splitDoc(s string) []docLine {
	if s == "" {
		return nil
	}
	var out []docLine
	for len(s) > 0 {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			out = append(out, docLine{text: s})
			break
		}
		line, eol := s[:i], "\n"
		if strings.HasSuffix(line, "\r") {
			line, eol = line[:len(line)-1], "\r\n"
		}
		out = append(out, docLine{line, eol})
		s = s[i+1:]
	}
	return out
}

func joinDoc(lines []docLine) string {
	n := 0
	for _, l := range lines {
		n += len(l.text) + len(l.eol)
	}
	var b strings.Builder
	b.Grow(n)
	for _, l := range lines {
		b.WriteString(l.text)
		b.WriteString(l.eol)
	}
	return b.String()
}

func trimRightWS(s string) string { return strings.TrimRight(s, " \t\r") }

// hunkStats counts changed lines.
type hunkStats struct{ added, removed int }

type replacement struct {
	start, oldLen int
	lines         []docLine
}

// applyHunks applies the hunks of one Update File to the lines of a file. Every
// hunk is located and validated before anything is changed; on failure the
// returned message says which hunk and how the file differs.
//
// Matching is exact first and then tolerant of trailing whitespace only: leading
// whitespace is code, so indentation is never fuzzed.
func applyHunks(disp string, lines []docLine, hunks []patchHunk, dominant string) ([]docLine, hunkStats, string) {
	tr := make([]string, len(lines))
	for i, l := range lines {
		tr[i] = trimRightWS(l.text)
	}
	var reps []replacement
	var st hunkStats
	cursor := 0
	for hi, h := range hunks {
		lines2 := h.lines
		for _, a := range h.anchors {
			idx := findAnchor(lines, a, cursor)
			if idx < 0 {
				return nil, st, anchorMissing(disp, hi+1, h, a, lines)
			}
			cursor = idx + 1
		}
		old, _ := hunkSides(lines2)
		if len(old) == 0 {
			// A pure addition goes right after its @@ anchor, or at the end of the
			// file when it has none.
			at := len(lines)
			if len(h.anchors) > 0 {
				at = cursor
			}
			reps = append(reps, replacement{start: at, lines: newDocLines(lines2, lines, at, dominant)})
			st.added += len(lines2)
			cursor = at
			continue
		}
		idx := findSeq(lines, tr, old, cursor, h.eof)
		if idx < 0 {
			// Models often end a hunk with a blank context line the file does
			// not have there; retry without the trailing blank context lines.
			trimmed := lines2
			for len(trimmed) > 0 && trimmed[len(trimmed)-1].op == ' ' && trimmed[len(trimmed)-1].text == "" {
				trimmed = trimmed[:len(trimmed)-1]
			}
			if len(trimmed) != len(lines2) {
				if o2, _ := hunkSides(trimmed); len(o2) > 0 {
					if idx2 := findSeq(lines, tr, o2, cursor, h.eof); idx2 >= 0 {
						lines2, old, idx = trimmed, o2, idx2
					}
				}
			}
		}
		if idx < 0 {
			return nil, st, hunkMismatch(disp, hi+1, h, old, lines, tr, cursor)
		}
		reps = append(reps, replacement{start: idx, oldLen: len(old), lines: newDocLines(lines2, lines, idx, dominant)})
		for _, hl := range lines2 {
			switch hl.op {
			case '+':
				st.added++
			case '-':
				st.removed++
			}
		}
		cursor = idx + len(old)
	}

	out := make([]docLine, 0, len(lines))
	pos := 0
	for _, r := range reps {
		out = append(out, lines[pos:r.start]...)
		out = append(out, r.lines...)
		pos = r.start + r.oldLen
	}
	out = append(out, lines[pos:]...)
	return out, st, ""
}

// hunkSides returns the lines a hunk expects to find and the lines it leaves.
func hunkSides(h []hunkLine) (old, new []string) {
	for _, l := range h {
		if l.op != '+' {
			old = append(old, l.text)
		}
		if l.op != '-' {
			new = append(new, l.text)
		}
	}
	return
}

// newDocLines builds the replacement for a matched hunk. Context lines keep the
// file's own text (a trailing-whitespace difference the matcher forgave must not
// be "fixed" as a side effect). Added lines take the line ending of the place
// they land, so new lines blend into a file that mixes endings: see blockEOL.
func newDocLines(h []hunkLine, file []docLine, at int, dominant string) []docLine {
	var out []docLine
	oi := 0 // old lines consumed so far
	for i := 0; i < len(h); {
		if h[i].op == ' ' {
			if at+oi < len(file) {
				out = append(out, file[at+oi])
			} else {
				out = append(out, docLine{h[i].text, dominant})
			}
			oi++
			i++
			continue
		}
		j := i
		for j < len(h) && h[j].op != ' ' {
			j++
		}
		eol := blockEOL(h[i:j], file, at+oi, dominant)
		for _, c := range h[i:j] {
			if c.op == '-' {
				oi++
			} else {
				out = append(out, docLine{c.text, eol})
			}
		}
		i = j
	}
	return out
}

// blockEOL is the ending for the added lines of one change block whose first old
// line sits at file[pos]: the ending of the first line the block removes; for a
// pure insertion, that of the line just before it, else the one just after,
// else the file's dominant style.
func blockEOL(block []hunkLine, file []docLine, pos int, dominant string) string {
	removes := false
	for _, l := range block {
		if l.op == '-' {
			removes = true
			break
		}
	}
	if removes && pos < len(file) && file[pos].eol != "" {
		return file[pos].eol
	}
	if pos > 0 && pos-1 < len(file) && file[pos-1].eol != "" {
		return file[pos-1].eol
	}
	if pos < len(file) && file[pos].eol != "" {
		return file[pos].eol
	}
	return dominant
}

func matchExact(lines []docLine, seq []string, at int) bool {
	for i, s := range seq {
		if lines[at+i].text != s {
			return false
		}
	}
	return true
}

func matchTrimmed(tr []string, seq []string, at int) bool {
	for i, s := range seq {
		if tr[at+i] != trimRightWS(s) {
			return false
		}
	}
	return true
}

// findSeq locates seq in the file at or after from: an exact match anywhere
// beats a whitespace-forgiving one earlier. With eof the match must end the file.
func findSeq(lines []docLine, tr []string, seq []string, from int, eof bool) int {
	n := len(seq)
	limit := len(lines) - n
	if limit < from {
		return -1
	}
	if eof {
		if matchExact(lines, seq, limit) || matchTrimmed(tr, seq, limit) {
			return limit
		}
		return -1
	}
	for i := from; i <= limit; i++ {
		if matchExact(lines, seq, i) {
			return i
		}
	}
	for i := from; i <= limit; i++ {
		if matchTrimmed(tr, seq, i) {
			return i
		}
	}
	return -1
}

// findAnchor locates the "@@ context" line: exact, then trailing-whitespace
// tolerant, then ignoring indentation as well (an anchor only says where to start
// looking; the hunk's own lines are still validated exactly).
func findAnchor(lines []docLine, anchor string, from int) int {
	for pass := 0; pass < 3; pass++ {
		for i := from; i < len(lines); i++ {
			t := lines[i].text
			switch pass {
			case 0:
				if t == anchor {
					return i
				}
			case 1:
				if trimRightWS(t) == trimRightWS(anchor) {
					return i
				}
			default:
				if strings.TrimSpace(t) == strings.TrimSpace(anchor) {
					return i
				}
			}
		}
	}
	return -1
}

func quoteLine(s string) string { return strconv.Quote(clip(s, 120)) }

func anchorMissing(disp string, hunkNo int, h patchHunk, anchor string, lines []docLine) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s, hunk %d (patch line %d): the @@ context line %s was not found", disp, hunkNo, h.line, quoteLine(anchor))
	texts := make([]string, len(lines))
	for i, l := range lines {
		texts[i] = l.text
	}
	if best, ok := closestLines(texts, []string{anchor}, similarityMinimum, hintScanBytes); ok {
		fmt.Fprintf(&sb, "; the closest line is line %d: %s", best.line, quoteLine(texts[best.line-1]))
	}
	sb.WriteString(". It must be a line of the file that comes before the hunk (and after the previous hunk).")
	return sb.String()
}

// hunkMismatch explains a hunk whose old lines were not found: what was
// expected, where the file most resembles it, and the first differing line.
func hunkMismatch(disp string, hunkNo int, h patchHunk, old []string, lines []docLine, tr []string, cursor int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s, hunk %d (patch line %d): the lines to change were not found in the file.\nexpected:", disp, hunkNo, h.line)
	const show = 6
	for i, l := range old {
		if i == show {
			fmt.Fprintf(&sb, "\n| … (%d more)", len(old)-show)
			break
		}
		sb.WriteString("\n| " + clip(l, 160))
	}
	n := len(old)
	if len(lines) < n {
		fmt.Fprintf(&sb, "\nThe file has only %d lines.", len(lines))
		return sb.String()
	}
	oldTr := make([]string, n)
	for i, o := range old {
		oldTr[i] = trimRightWS(o)
	}
	best, bestScore := -1, 0
	positions := len(lines) - n + 1
	full := positions*n <= 8_000_000
	for s := 0; s < positions; s++ {
		if !full && tr[s] != oldTr[0] && tr[s+n-1] != oldTr[n-1] && tr[s+n/2] != oldTr[n/2] {
			continue
		}
		score := 0
		for i := 0; i < n; i++ {
			if tr[s+i] == oldTr[i] {
				score++
			}
		}
		if score > bestScore || (score == bestScore && score > 0 && best < cursor && s >= cursor) {
			best, bestScore = s, score
		}
	}
	if best < 0 {
		// No window shares a line: fall back to the single most similar line.
		if b, ok := closestLines(func() []string {
			t := make([]string, len(lines))
			for i, l := range lines {
				t[i] = l.text
			}
			return t
		}(), topTargets(strings.Join(old, "\n"), 3), similarityMinimum, hintScanBytes); ok {
			fmt.Fprintf(&sb, "\nNo run of lines matches; the closest single line is line %d: %s.", b.line, quoteLine(lines[b.line-1].text))
		} else {
			sb.WriteString("\nNothing similar was found in the file; re-read it.")
		}
		return sb.String()
	}
	fmt.Fprintf(&sb, "\nclosest match: lines %d-%d (%d of %d lines equal)", best+1, best+n, bestScore, n)
	for i := 0; i < n; i++ {
		if tr[best+i] != oldTr[i] {
			fmt.Fprintf(&sb, "; first difference at line %d:\n  expected: %s\n  found:    %s", best+i+1, quoteLine(old[i]), quoteLine(lines[best+i].text))
			break
		}
	}
	if best < cursor && bestScore == n {
		fmt.Fprintf(&sb, "\nThe lines exist at line %d, before the previous hunk: list hunks in the order they appear in the file.", best+1)
	}
	return sb.String()
}

// ---------------------------------------------------------------------------
// Planning and execution

// vfile is a file as the patch sees it: the on-disk state it started from and
// the state after the operations applied so far. Operations run against this
// overlay, so a patch can add then update a file, delete then recreate one, or
// chain moves, and nothing touches the disk until every operation has validated.
type vfile struct {
	path, disp  string
	diskExisted bool
	diskData    []byte
	diskInfo    iofs.FileInfo
	exists      bool
	data        []byte
	modeFrom    iofs.FileInfo // whose mode a newly created file inherits (moves)
	fresh       bool          // staleness already checked
	touched     bool
	// linkTarget is set when the entry is a symlink (only Delete File reaches
	// one): removing it must remove the link, and a rollback must restore it.
	linkTarget string
	isLink     bool
}

type patchPlan struct {
	k     *call
	files map[string]*vfile
	order []*vfile
	lines []string
	total hunkStats
}

func (pl *patchPlan) get(path, disp string) (*vfile, *tools.Result) {
	if vf := pl.files[path]; vf != nil {
		return vf, nil
	}
	k := pl.k
	vf := &vfile{path: path, disp: disp}
	if li, lerr := os.Lstat(path); lerr == nil && li.Mode()&iofs.ModeSymlink != 0 {
		target, rerr := os.Readlink(path)
		if rerr != nil {
			return nil, k.fail("cannot read symlink %s: %s", disp, osReason(rerr))
		}
		vf.isLink, vf.linkTarget = true, target
		vf.diskExisted, vf.diskInfo, vf.exists = true, li, true
		pl.files[path] = vf
		pl.order = append(pl.order, vf)
		return vf, nil
	}
	fi, err := os.Stat(path)
	switch {
	case err == nil:
		if fi.IsDir() {
			return nil, k.fail("%s is a directory, not a file", disp)
		}
		data, _, res := k.readFile(path, disp, maxFileBytes)
		if res != nil {
			return nil, res
		}
		vf.diskExisted, vf.diskData, vf.diskInfo = true, data, fi
		vf.exists, vf.data = true, data
	case errors.Is(err, iofs.ErrNotExist):
	default:
		return nil, k.fail("cannot stat %s: %s", disp, osReason(err))
	}
	pl.files[path] = vf
	pl.order = append(pl.order, vf)
	return vf, nil
}

// checkFresh applies FileState's staleness rule once per file that existed on
// disk before the patch. requireRead is false: a patch's context lines validate
// the content themselves, so an unread file is fine, but one the agent read
// and someone has since changed is refused.
func (pl *patchPlan) checkFresh(vf *vfile) *tools.Result {
	if vf.fresh || !vf.diskExisted {
		return nil
	}
	vf.fresh = true
	return pl.k.freshness(vf.path, vf.disp, vf.diskData, false)
}

// Run implements tools.Tool.
func (ApplyPatch) Run(ctx context.Context, c *tools.Call) (*tools.Result, error) {
	k := begin(ctx, c, "apply_patch")
	var a struct {
		Patch *string `json:"patch"`
	}
	if r := k.decode(&a); r != nil {
		return r, nil
	}
	if a.Patch == nil {
		return k.fail("patch is required"), nil
	}
	ops, msg := parsePatch(*a.Patch)
	if msg != "" {
		return k.fail("apply_patch: %s", msg), nil
	}

	// Resolve every path first: a bad path anywhere fails the whole patch before
	// any permission prompt is shown.
	type target struct {
		path, disp string
		move       string
		moveDisp   string
	}
	targets := make([]target, len(ops))
	for i, op := range ops {
		resolve := k.resolve
		if op.kind == opDelete {
			resolve = k.resolveEntry
		}
		p, d, m := k.resolveArgWith(op.path, resolve)
		if m != "" {
			return k.fail("apply_patch: %s", m), nil
		}
		targets[i] = target{path: p, disp: d}
		if op.move != "" {
			mp, md, m := k.resolveArg(op.move)
			if m != "" {
				return k.fail("apply_patch: %s", m), nil
			}
			targets[i].move, targets[i].moveDisp = mp, md
		}
	}

	// One permission request per distinct file, in patch order, all before any
	// read or write.
	asked := map[string]bool{}
	ask := func(summary, path string) *tools.Result {
		if asked[path] {
			return nil
		}
		asked[path] = true
		return k.authorize(summary, true, perm.RiskMedium, path)
	}
	for i, op := range ops {
		t := targets[i]
		var r *tools.Result
		switch op.kind {
		case opAdd:
			r = ask("create "+t.disp, t.path)
		case opDelete:
			r = ask("delete "+t.disp, t.path)
		case opUpdate:
			if t.move != "" {
				if r = ask("move "+t.disp+" -> "+t.moveDisp, t.path); r == nil {
					r = ask("move "+t.disp+" -> "+t.moveDisp, t.move)
				}
			} else {
				r = ask("patch "+t.disp, t.path)
			}
		}
		if r != nil {
			return r, nil
		}
	}

	paths := make([]string, 0, len(ops)*2)
	for _, t := range targets {
		paths = append(paths, t.path)
		if t.move != "" {
			paths = append(paths, t.move)
		}
	}
	unlock := fileLocks.acquire(paths...)
	defer unlock()

	pl := &patchPlan{k: k, files: map[string]*vfile{}}
	for i, op := range ops {
		t := targets[i]
		if r := pl.apply(op, t.path, t.disp, t.move, t.moveDisp); r != nil {
			return r, nil
		}
	}
	return pl.commit(), nil
}

// apply validates and applies one operation to the overlay.
func (pl *patchPlan) apply(op patchOp, path, disp, move, moveDisp string) *tools.Result {
	k := pl.k
	fail := func(format string, args ...any) *tools.Result {
		return k.fail("apply_patch failed; nothing was written. "+format, args...)
	}
	switch op.kind {
	case opAdd:
		vf, res := pl.get(path, disp)
		if res != nil {
			return fail("%s", res.Text)
		}
		if vf.exists {
			return fail("Add File %s: the file already exists (patch line %d); use Update File to change it, or delete it first in the same patch.", disp, op.line)
		}
		var b strings.Builder
		for _, l := range op.add {
			b.WriteString(l)
			b.WriteByte('\n')
		}
		vf.exists, vf.data, vf.touched = true, []byte(b.String()), true
		pl.lines = append(pl.lines, fmt.Sprintf("A %s (+%d)", disp, len(op.add)))
		pl.total.added += len(op.add)

	case opDelete:
		vf, res := pl.get(path, disp)
		if res != nil {
			return fail("%s", res.Text)
		}
		if !vf.exists {
			return fail("Delete File %s: the file does not exist (patch line %d).", disp, op.line)
		}
		if r := pl.checkFresh(vf); r != nil {
			return fail("%s", r.Text)
		}
		vf.exists, vf.data, vf.touched = false, nil, true
		pl.lines = append(pl.lines, "D "+disp)

	case opUpdate:
		vf, res := pl.get(path, disp)
		if res != nil {
			return fail("%s", res.Text)
		}
		if !vf.exists {
			return fail("Update File %s: the file does not exist (patch line %d); use Add File to create it.", disp, op.line)
		}
		if r := pl.checkFresh(vf); r != nil {
			return fail("%s", r.Text)
		}
		if bytes.IndexByte(vf.data, 0) >= 0 {
			return fail("Update File %s: it is a binary file.", disp)
		}
		bom, body := splitBOM(vf.data)
		text := string(body)
		lines := splitDoc(text)
		dom := dominantEOL(text)
		out, st, msg := applyHunks(disp, lines, op.hunks, dom)
		if msg != "" {
			return fail("%s", msg)
		}
		if len(out) > 0 {
			// Keep the file's trailing-newline state: only a final line that had no
			// newline stays without one, and every other line needs a terminator.
			endsNL := len(lines) == 0 || lines[len(lines)-1].eol != ""
			for i := range out[:len(out)-1] {
				if out[i].eol == "" {
					out[i].eol = dom
				}
			}
			last := &out[len(out)-1]
			if endsNL {
				if last.eol == "" {
					last.eol = dom
				}
			} else {
				last.eol = ""
			}
		}
		newData := append(append([]byte(nil), bom...), joinDoc(out)...)
		pl.total.added += st.added
		pl.total.removed += st.removed
		stats := fmt.Sprintf("+%d -%d", st.added, st.removed)
		if move == "" || move == path {
			vf.data, vf.touched = newData, true
			pl.lines = append(pl.lines, fmt.Sprintf("M %s (%s)", disp, stats))
			break
		}
		dst, res := pl.get(move, moveDisp)
		if res != nil {
			return fail("%s", res.Text)
		}
		if dst.exists {
			return fail("Update File %s: cannot move it to %s (patch line %d) because that file already exists.", disp, moveDisp, op.line)
		}
		dst.exists, dst.data, dst.touched = true, newData, true
		dst.modeFrom = vf.diskInfo
		vf.exists, vf.data, vf.touched = false, nil, true
		pl.lines = append(pl.lines, fmt.Sprintf("R %s -> %s (%s)", disp, moveDisp, stats))
	}
	return nil
}

type planned struct {
	vf     *vfile
	create bool
	del    bool
}

// commit runs the guards and snapshots for every changed file, then writes.
// Writes happen before deletions so a move never loses data if the destination
// cannot be written, and a failure part-way rolls back what was already done.
func (pl *patchPlan) commit() *tools.Result {
	k := pl.k
	var acts []planned
	sorted := append([]*vfile(nil), pl.order...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].path < sorted[j].path })
	for _, vf := range sorted {
		if !vf.touched {
			continue
		}
		switch {
		case vf.exists && !vf.diskExisted:
			acts = append(acts, planned{vf: vf, create: true})
		case vf.exists && vf.diskExisted:
			if !bytes.Equal(vf.data, vf.diskData) {
				acts = append(acts, planned{vf: vf})
			}
		case !vf.exists && vf.diskExisted:
			acts = append(acts, planned{vf: vf, del: true})
		}
	}
	if len(acts) == 0 {
		return k.ok("No changes: the patch leaves every file identical.")
	}

	for _, a := range acts {
		if r := k.beforeWrite(a.vf.path, a.vf.disp); r != nil {
			return r
		}
	}

	sort.SliceStable(acts, func(i, j int) bool { return !acts[i].del && acts[j].del })
	var done []planned
	for _, a := range acts {
		var err error
		switch {
		case a.del:
			err = removeFile(a.vf.path)
		default:
			info := a.vf.diskInfo
			if info == nil {
				info = a.vf.modeFrom
			}
			if !a.vf.diskExisted {
				if err = os.MkdirAll(filepath.Dir(a.vf.path), 0o755); err != nil {
					err = fmt.Errorf("cannot create directory: %s", osReason(err))
				}
			}
			if err == nil {
				err = atomicWrite(a.vf.path, a.vf.data, info)
			}
		}
		if err != nil {
			rb := pl.rollback(done)
			msg := fmt.Sprintf("apply_patch failed while writing %s (%s); the files already written were restored.", a.vf.disp, osReason(err))
			if rb != "" {
				msg = fmt.Sprintf("apply_patch failed while writing %s (%s) and could not fully restore the earlier files: %s", a.vf.disp, osReason(err), rb)
			}
			return k.fail("%s", msg)
		}
		done = append(done, a)
	}

	for _, a := range acts {
		if a.del {
			k.afterWrite(a.vf.path, nil)
		} else {
			k.afterWrite(a.vf.path, a.vf.data)
		}
	}
	head := fmt.Sprintf("Patch applied: %s changed (+%d -%d lines).", plural(len(acts), "file"), pl.total.added, pl.total.removed)
	res := k.ok(head + "\n" + strings.Join(pl.lines, "\n"))
	files := make([]string, 0, len(acts))
	for _, a := range acts {
		files = append(files, a.vf.disp)
	}
	res.Meta = map[string]any{"files": files, "added": pl.total.added, "removed": pl.total.removed}
	return res
}

// rollback undoes completed steps in reverse order and reports what it could not
// undo.
func (pl *patchPlan) rollback(done []planned) string {
	var problems []string
	for i := len(done) - 1; i >= 0; i-- {
		a := done[i]
		var err error
		switch {
		case a.create:
			err = removeFile(a.vf.path)
		case a.vf.isLink:
			err = os.Symlink(a.vf.linkTarget, a.vf.path)
		default:
			err = atomicWrite(a.vf.path, a.vf.diskData, a.vf.diskInfo)
		}
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %s", a.vf.disp, osReason(err)))
		}
	}
	return strings.Join(problems, "; ")
}
