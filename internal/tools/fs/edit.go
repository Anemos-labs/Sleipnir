package fs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	iofs "io/fs"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// Edit implements the edit tool.
type Edit struct{}

// Spec implements tools.Tool.
func (Edit) Spec() core.ToolSpec {
	return core.ToolSpec{
		Name: "edit",
		Description: "Replace exact text in a file. Give `old_string` and `new_string`; old_string must match exactly once, " +
			"whitespace included (add surrounding lines to make it unique, or set `replace_all`). " +
			"To change several places in one file pass `edits`, a list of {old_string,new_string,replace_all} applied in order, " +
			"all-or-nothing. Read the file first. Line endings, BOM and file mode are preserved. Returns a short diff.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"path":{"type":"string","description":"File to edit"},` +
			`"old_string":{"type":"string","description":"Exact text to replace"},` +
			`"new_string":{"type":"string","description":"Replacement text"},` +
			`"replace_all":{"type":"boolean","description":"Replace every occurrence"},` +
			`"edits":{"type":"array","description":"Several replacements, applied in order","items":{"type":"object","properties":{` +
			`"old_string":{"type":"string"},"new_string":{"type":"string"},"replace_all":{"type":"boolean"}},` +
			`"required":["old_string","new_string"]}}},` +
			`"required":["path"]}`),
	}
}

const (
	maxEdits          = 500
	maxListedMatches  = 8
	hintScanBytes     = 4 << 20
	editDiffLines     = 40
	metaDiffLines     = 400
	similarityMinimum = 0.5
	maxHintLines      = 400
)

type editItem struct {
	Old        *string `json:"old_string"`
	New        *string `json:"new_string"`
	ReplaceAll bool    `json:"replace_all"`
}

// editArgs spells the single-edit fields out instead of embedding editItem:
// encoding/json reports type errors for embedded fields as "editItem.old_string",
// and the model should be told about "old_string".
type editArgs struct {
	Path       string     `json:"path"`
	FilePath   string     `json:"file_path"` // alias, see Read
	Old        *string    `json:"old_string"`
	New        *string    `json:"new_string"`
	ReplaceAll bool       `json:"replace_all"`
	Edits      []editItem `json:"edits"`
}

type editSpec struct {
	old, new string
	all      bool
}

func (a *editArgs) specs() ([]editSpec, string) {
	if len(a.Edits) > 0 {
		if a.Old != nil || a.New != nil {
			return nil, "use either old_string/new_string or edits, not both"
		}
		if len(a.Edits) > maxEdits {
			return nil, fmt.Sprintf("too many edits (%d); send at most %d per call", len(a.Edits), maxEdits)
		}
		out := make([]editSpec, len(a.Edits))
		for i, e := range a.Edits {
			if e.Old == nil {
				return nil, fmt.Sprintf("edits[%d]: old_string is required", i)
			}
			if e.New == nil {
				return nil, fmt.Sprintf(`edits[%d]: new_string is required (use "" to delete the text)`, i)
			}
			out[i] = editSpec{*e.Old, *e.New, e.ReplaceAll}
		}
		return out, ""
	}
	if a.Old == nil {
		return nil, "old_string is required (or pass edits)"
	}
	if a.New == nil {
		return nil, `new_string is required (use "" to delete the text)`
	}
	return []editSpec{{*a.Old, *a.New, a.ReplaceAll}}, ""
}

// Run implements tools.Tool.
func (Edit) Run(ctx context.Context, c *tools.Call) (*tools.Result, error) {
	k := begin(ctx, c, "edit")
	var a editArgs
	if r := k.decode(&a); r != nil {
		return r, nil
	}
	if a.Path == "" {
		a.Path = a.FilePath
	}
	specs, msg := a.specs()
	if msg != "" {
		return k.fail("%s", msg), nil
	}
	canon, disp, msg := k.resolveArg(a.Path)
	if msg != "" {
		return k.fail("%s", msg), nil
	}
	if r := k.unread(canon, disp); r != nil {
		return r, nil
	}
	if r := k.authorize("edit "+disp, true, perm.RiskMedium, canon); r != nil {
		return r, nil
	}

	unlock := fileLocks.acquire(canon)
	defer unlock()

	if fi, err := os.Stat(canon); err == nil && fi.IsDir() {
		return k.fail("%s is a directory; give a file path", disp), nil
	} else if errors.Is(err, iofs.ErrNotExist) {
		if len(specs) == 1 && specs[0].old == "" {
			return k.createViaEdit(canon, disp, specs[0].new), nil
		}
		return k.fail("%s; to create a new file use write", notFoundMessage(canon, disp)), nil
	}
	data, fi, res := k.readFile(canon, disp, maxFileBytes)
	if res != nil {
		return res, nil
	}
	if r := k.freshness(canon, disp, data, true); r != nil {
		return r, nil
	}
	if i := bytes.IndexByte(data, 0); i >= 0 {
		return k.fail("%s is a binary file (NUL byte at offset %d); edit works on text files only", disp, i), nil
	}
	bom, body := splitBOM(data)
	orig := string(body)
	updated, n, emsg := applyEdits(orig, specs, disp)
	if emsg != "" {
		return k.fail("%s", emsg), nil
	}
	if updated == orig {
		return k.ok(fmt.Sprintf("No change: the edits leave %s identical.", disp)), nil
	}
	final := make([]byte, 0, len(bom)+len(updated))
	final = append(final, bom...)
	final = append(final, updated...)
	if r := k.commit(canon, disp, final, fi); r != nil {
		return r, nil
	}

	d := diffFiles(orig, updated, diffContext, metaDiffLines)
	head := fmt.Sprintf("Edited %s: %s (+%d -%d lines)", disp, plural(n, "replacement"), d.added, d.removed)
	text := head
	if dt := d.text(editDiffLines); dt != "" {
		text += "\n" + dt
	} else {
		text += "\n(no line-level difference: only line endings or the final newline changed)"
	}
	out := k.ok(text)
	out.Meta = map[string]any{
		"path": disp, "replacements": n, "added": d.added, "removed": d.removed,
		"diff": d.text(metaDiffLines),
	}
	return out, nil
}

func (k *call) createViaEdit(canon, disp, content string) *tools.Result {
	if r := k.commit(canon, disp, []byte(content), nil); r != nil {
		return r
	}
	return k.ok(fmt.Sprintf("Created %s (%s, %s)", disp, plural(countLines([]byte(content)), "line"), plural(len(content), "byte")))
}

// applyEdits applies the edits in order to text (BOM already removed) and
// returns the new text and the number of replacements. Any failure returns a
// model-visible message and the caller writes nothing: an edit list is
// all-or-nothing, so a model never has to reason about a half-applied batch.
func applyEdits(text string, specs []editSpec, disp string) (string, int, string) {
	total := 0
	for i, sp := range specs {
		next, n, msg := applyOne(text, sp, disp)
		if msg != "" {
			if len(specs) > 1 {
				msg = fmt.Sprintf("edit %d of %d: %s (nothing was written)", i+1, len(specs), msg)
			}
			return "", 0, msg
		}
		text = next
		total += n
	}
	return text, total, ""
}

type span struct{ from, to int }

func applyOne(cur string, sp editSpec, disp string) (string, int, string) {
	old, repl := sp.old, sp.new
	if old == repl {
		return "", 0, "old_string and new_string are identical; nothing to change"
	}
	if !strings.Contains(cur, "\r") {
		// A pure-LF file must not pick up stray CRs from the model's strings.
		old = strings.ReplaceAll(old, "\r\n", "\n")
		repl = strings.ReplaceAll(repl, "\r\n", "\n")
		if old == repl {
			return "", 0, "old_string and new_string are identical; nothing to change"
		}
	}
	if old == "" {
		if cur == "" {
			return repl, 1, ""
		}
		return "", 0, "old_string is empty; give the text to replace (to replace the whole file use write)"
	}

	// The model only ever saw LF (read hides the CRs), so an LF old_string that
	// spans lines is matched against the text with CRLF folded to LF and the
	// match is mapped back to the original bytes: only the touched lines change,
	// even in a file that mixes endings. An old_string that names a CR itself is
	// matched literally.
	fold := strings.Contains(cur, "\r\n") && !strings.Contains(old, "\r") && strings.Contains(old, "\n")
	text := cur
	var qs []int
	if fold {
		text, qs = normalizeCRLF(cur)
	}
	starts := findAll(text, old, maxListedMatches+1)
	if len(starts) == 0 {
		return "", 0, explainMissing(text, old, disp)
	}
	if len(starts) > 1 && !sp.all {
		return "", 0, explainAmbiguous(text, starts, strings.Count(text, old), disp)
	}
	var spans []span
	if sp.all {
		for i := 0; ; {
			j := strings.Index(text[i:], old)
			if j < 0 {
				break
			}
			s := i + j
			spans = append(spans, span{s, s + len(old)})
			i = s + len(old)
		}
	} else {
		spans = []span{{starts[0], starts[0] + len(old)}}
	}
	if fold {
		// Offsets in the folded text map to the original by adding the CRs
		// removed before them.
		for i, s := range spans {
			spans[i] = span{s.from + sort.SearchInts(qs, s.from), s.to + sort.SearchInts(qs, s.to)}
		}
	}
	return splice(cur, spans, repl), len(spans), ""
}

// splice replaces each span of cur with repl. In a file that has CRs, replacement
// text written with bare LFs takes the line ending of the place it lands, so new
// lines blend in instead of leaving mixed endings behind.
func splice(cur string, spans []span, repl string) string {
	needBlend := strings.Contains(cur, "\r") && strings.Contains(repl, "\n") && !strings.Contains(repl, "\r")
	if !needBlend && len(spans) == 1 {
		return cur[:spans[0].from] + repl + cur[spans[0].to:]
	}
	crlf := ""
	dominant := "\n"
	if needBlend {
		crlf = strings.ReplaceAll(repl, "\n", "\r\n")
		dominant = dominantEOL(cur)
	}
	var b strings.Builder
	prev := 0
	for _, sp := range spans {
		b.WriteString(cur[prev:sp.from])
		if needBlend && localEOL(cur, sp.from, sp.to, dominant) == "\r\n" {
			b.WriteString(crlf)
		} else {
			b.WriteString(repl)
		}
		prev = sp.to
	}
	b.WriteString(cur[prev:])
	return b.String()
}

// localEOL is the line ending in force where cur[from:to] sits: the first one
// inside the span, else the one that ends the line it is on, else the previous
// line's, else the file's dominant style.
func localEOL(s string, from, to int, dominant string) string {
	if i := strings.IndexByte(s[from:to], '\n'); i >= 0 {
		return eolAt(s, from+i)
	}
	if i := strings.IndexByte(s[to:], '\n'); i >= 0 {
		return eolAt(s, to+i)
	}
	if i := strings.LastIndexByte(s[:from], '\n'); i >= 0 {
		return eolAt(s, i)
	}
	return dominant
}

func eolAt(s string, nl int) string {
	if nl > 0 && s[nl-1] == '\r' {
		return "\r\n"
	}
	return "\n"
}

// normalizeCRLF returns s with every CRLF folded to LF, plus for each removed CR
// the index in the folded text of the LF that followed it.
func normalizeCRLF(s string) (string, []int) {
	var b strings.Builder
	b.Grow(len(s))
	var qs []int
	last := 0
	for i := 0; ; {
		j := strings.Index(s[i:], "\r\n")
		if j < 0 {
			break
		}
		j += i
		b.WriteString(s[last:j])
		qs = append(qs, b.Len())
		last = j + 1
		i = j + 2
	}
	b.WriteString(s[last:])
	return b.String(), qs
}

// findAll returns up to limit start offsets of sub in s, overlapping matches
// included: "aa" in "aaa" is ambiguous and the model should be told.
func findAll(s, sub string, limit int) []int {
	var out []int
	for i := 0; len(out) < limit && i <= len(s); {
		j := strings.Index(s[i:], sub)
		if j < 0 {
			break
		}
		out = append(out, i+j)
		i += j + 1
	}
	return out
}

func lineNumbers(s string, starts []int) []int {
	out := make([]int, len(starts))
	line, pos := 1, 0
	for i, st := range starts {
		line += strings.Count(s[pos:st], "\n")
		pos = st
		out[i] = line
	}
	return out
}

func explainAmbiguous(text string, starts []int, count int, disp string) string {
	if len(starts) > count {
		count = len(starts)
	}
	lines := lineNumbers(text, starts)
	parts := make([]string, 0, len(lines))
	for i, l := range lines {
		if i == maxListedMatches {
			break
		}
		parts = append(parts, strconv.Itoa(l))
	}
	where := "lines " + strings.Join(parts, ", ")
	if count > len(parts) {
		where += fmt.Sprintf(", and %d more", count-len(parts))
	}
	return fmt.Sprintf("old_string matches %d places in %s (%s). Add surrounding lines to make it unique, or set replace_all=true to replace all of them.", count, disp, where)
}

var numberedLine = regexp.MustCompile(`^ *\d+\t`)

// explainMissing builds the "old_string not found" message. Nearly every miss is
// one of a few mechanical mistakes, so it checks for those in order of how
// often they happen and names the exact difference; only then does it fall back
// to pointing at the most similar line.
func explainMissing(cur, old, disp string) string {
	head := fmt.Sprintf("old_string not found in %s.", disp)
	const tail = " old_string must match the file exactly, including whitespace and indentation."
	if hint := missingHint(cur, old); hint != "" {
		return head + " " + hint + tail
	}
	return head + tail + " Re-read the file; it may have changed."
}

func missingHint(cur, old string) string {
	oldLines := strings.Split(strings.ReplaceAll(old, "\r\n", "\n"), "\n")

	numbered := 0
	nonEmpty := 0
	for _, l := range oldLines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		nonEmpty++
		if numberedLine.MatchString(l) {
			numbered++
		}
	}
	if nonEmpty > 0 && numbered == nonEmpty {
		return "It looks like it includes the line-number prefixes shown by read; leave those out."
	}

	if strings.Contains(old, "\uFFFD") && !strings.Contains(cur, "\uFFFD") {
		return "old_string contains U+FFFD, which read shows for bytes that are not valid UTF-8; edit around those bytes."
	}

	if len(oldLines) > maxHintLines {
		return "" // a hint is a courtesy; do not spend real time on a huge old_string
	}

	if t := strings.TrimSpace(old); t != "" && t != old {
		if i := strings.Index(cur, t); i >= 0 {
			return fmt.Sprintf("It matches at line %d once leading and trailing whitespace is trimmed; fix the whitespace at the start or end of old_string.", 1+strings.Count(cur[:i], "\n"))
		}
	}

	fileLines := strings.Split(cur, "\n")
	for i, l := range fileLines {
		fileLines[i] = strings.TrimSuffix(l, "\r")
	}
	if first, last, why, ok := whitespaceTwin(fileLines, oldLines); ok {
		return fmt.Sprintf("The same text exists ignoring whitespace at lines %d-%d; %s", first, last, why)
	}

	if start, ran, wrote, has, ok := divergence(fileLines, oldLines); ok {
		first := fmt.Sprintf("The first %d lines of old_string match the file at lines %d-%d", ran, start, start+ran-1)
		if ran == 1 {
			first = fmt.Sprintf("The first line of old_string matches the file at line %d", start)
		}
		there := "the file has " + strconv.Quote(clip(has, 120)) + " there"
		if start+ran > len(fileLines) {
			there = "the file ends there"
		}
		return fmt.Sprintf("%s; line %d of old_string is %s but %s.", first, ran+1, strconv.Quote(clip(wrote, 120)), there)
	}
	if best, ok := closestLines(fileLines, topTargets(old, 3), similarityMinimum, hintScanBytes); ok {
		return fmt.Sprintf("The closest line is line %d (%d%% similar): %s.", best.line, int(best.score*100), strconv.Quote(clip(fileLines[best.line-1], 160)))
	}
	return ""
}

// divergence finds where a block of old's lines is right and then is not: the place in the file where old's first lines match, line for
// line, for the longest run, and the first line after the run (the one old has and the file does not). Most misses of a block are one wrong
// line in it, and "the closest line is 100% similar" tells a model nothing about which. start is the file's line number (from 1) where the
// run begins; ran is how many lines of old match. A run of one line counts only when that line says something (a bare "}" is everywhere).
func divergence(file, old []string) (start, ran int, wrote, has string, ok bool) {
	for len(old) > 0 && strings.TrimSpace(old[len(old)-1]) == "" {
		old = old[:len(old)-1]
	}
	if len(old) < 2 {
		return 0, 0, "", "", false
	}
	best := 0
	for i := range file {
		if file[i] != old[0] {
			continue
		}
		n := 1
		for n < len(old) && i+n < len(file) && file[i+n] == old[n] {
			n++
		}
		if n > best {
			best, start = n, i
		}
	}
	if best == 0 || best == len(old) || (best == 1 && len(strings.TrimSpace(old[0])) < 12) {
		return 0, 0, "", "", false
	}
	if start+best < len(file) {
		has = file[start+best]
	}
	return start + 1, best, old[best], has, true
}

func squash(s string) string { return strings.Join(strings.Fields(s), " ") }

// whitespaceTwin looks for a run of file lines equal to old's lines once all
// whitespace is squashed, and says precisely where the raw text differs.
func whitespaceTwin(file, old []string) (first, last int, why string, ok bool) {
	var want []string
	var wantRaw []string
	for _, l := range old {
		if s := squash(l); s != "" {
			want = append(want, s)
			wantRaw = append(wantRaw, l)
		}
	}
	if len(want) == 0 {
		return
	}
	sq := make([]string, len(file))
	for i, l := range file {
		sq[i] = squash(l)
	}
	for i := range file {
		if sq[i] != want[0] {
			continue
		}
		idx := []int{i}
		j := i + 1
		for len(idx) < len(want) && j < len(file) {
			if sq[j] == "" {
				j++
				continue
			}
			if sq[j] != want[len(idx)] {
				break
			}
			idx = append(idx, j)
			j++
		}
		if len(idx) != len(want) {
			continue
		}
		for n, fi := range idx {
			if file[fi] != wantRaw[n] {
				return idx[0] + 1, idx[len(idx)-1] + 1, describeWhitespace(file[fi], wantRaw[n], fi+1), true
			}
		}
		return idx[0] + 1, idx[len(idx)-1] + 1, "the blank lines between them differ.", true
	}
	return
}

func describeWhitespace(fileLine, oldLine string, lineNo int) string {
	fl, ol := leadingWS(fileLine), leadingWS(oldLine)
	if fl != ol {
		return fmt.Sprintf("line %d differs in indentation (file has %s, old_string has %s).", lineNo, describeWS(fl), describeWS(ol))
	}
	ft, ot := strings.TrimLeft(fileLine, " \t"), strings.TrimLeft(oldLine, " \t")
	if strings.TrimRight(ft, " \t") == strings.TrimRight(ot, " \t") {
		return fmt.Sprintf("line %d differs in trailing whitespace (file has %s, old_string has %s).", lineNo, describeWS(trailingWS(ft)), describeWS(trailingWS(ot)))
	}
	return fmt.Sprintf("line %d differs in the spacing inside the line.", lineNo)
}

func leadingWS(s string) string  { return s[:len(s)-len(strings.TrimLeft(s, " \t"))] }
func trailingWS(s string) string { return s[len(strings.TrimRight(s, " \t")):] }

func describeWS(ws string) string {
	if ws == "" {
		return "none"
	}
	tabs := strings.Count(ws, "\t")
	spaces := strings.Count(ws, " ")
	switch {
	case spaces == 0:
		return plural(tabs, "tab")
	case tabs == 0:
		return plural(spaces, "space")
	}
	return fmt.Sprintf("%s and %s", plural(tabs, "tab"), plural(spaces, "space"))
}
