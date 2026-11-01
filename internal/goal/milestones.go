package goal

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/skills/mdfile"
)

// Milestones are the checkpoints of a goal that the judge rules on one by one. They come from the goal's text and its spec file
// (ParseMilestones): a list under a milestones heading or a "Milestones:" line, headings nested under a milestones heading, or headings
// that name a milestone ("## Milestone 2: Web UI"). What is written under a milestone (indented lines, paragraphs, deeper headings) is
// kept as its acceptance details.
//
// A goal with milestones is met only when one verdict confirms every milestone with cited evidence (State.Reconcile): a judge that says
// "done" without confirming each one is read as "continue", with the unconfirmed milestones as what is left.

// Limits of milestones and of a spec file.
const (
	MaxMilestones     = 24       // milestones kept; the rest of a longer list is ignored
	MaxMilestoneRunes = 600      // runes of one milestone's text, its details included
	MaxSpecBytes      = 64 << 10 // bytes of a spec file
)

// Milestone is one checkpoint of a goal. ID is "M1", "M2", ... in the order written; Met and Evidence are what the judge last ruled.
type Milestone struct {
	ID       string
	Text     string
	Met      bool
	Evidence string // the judge's citation for Met (record numbers and what they show)
}

// MilestoneVerdict is the judge's ruling on one milestone.
type MilestoneVerdict struct {
	ID       string `json:"id"`
	Met      bool   `json:"met"`
	Evidence string `json:"evidence"`
}

var (
	// heading is a markdown heading: its marks and its title.
	heading = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*$`)
	// sectionTitle is the title of a heading that starts a milestones section ("Milestones", "Acceptance milestones").
	sectionTitle = regexp.MustCompile(`(?i)\bmilestones\b`)
	// namedTitle is the start of a heading that names one milestone ("Milestone 2:", "M2 -"), which the numbering replaces.
	namedTitle = regexp.MustCompile(`(?i)^\**(?:milestone\s+\d{1,3}|m\d{1,3})\b\**\s*[:.)\-–—]?\s*`)
	// milestoneLabel is the plain line "Milestones:" that starts a list section.
	milestoneLabel = regexp.MustCompile(`(?i)^(?:\*\*)?(?:acceptance\s+)?milestones(?:\*\*)?\s*:?(?:\*\*)?\s*$`)
	// listItem is a list item: its marker, an optional checkbox, and the text.
	listItem = regexp.MustCompile(`^([-*+]|\d{1,3}[.)])\s+(?:\[[ xX]\]\s+)?(.*)$`)
	// ownID is an ID the person wrote in front of a milestone ("M3:", "**M3** -"), which the numbering replaces.
	ownID = regexp.MustCompile(`(?i)^\**m\d{1,3}\**\s*[:.)\-–—]\s*`)
	// writtenNumber is the number in an ID the person wrote ("M0", "Milestone 2"), which the milestone keeps when every one has its own.
	writtenNumber = regexp.MustCompile(`(?i)^\**(?:milestone\s+|m)(\d{1,3})\b`)
	// specLine is the line of a goal that names its spec file; specEnd is the same at the end of a goal typed on one line.
	specLine = regexp.MustCompile("(?im)^[ \t]*spec[ \t]*:[ \t]*[`\"']?([^\\s`\"']+)[`\"']?[ \t]*$")
	specEnd  = regexp.MustCompile("(?i)(?:^|\\s)spec[ \t]*:[ \t]*[`\"']?([^\\s`\"']+)[`\"']?\\s*$")
)

// ParseMilestones reads the milestones of text, in order, numbering them from first. At most MaxMilestones are returned in all. When the
// person numbered every milestone themselves, with numbers that differ ("M0" to "M5"), the milestones keep those IDs, so that the plan,
// the judge and the goal's text name each one alike.
//
// Three forms are read: a list under a milestones section ("## Milestones" or "Milestones:"); headings nested under a milestones
// heading ("## Milestones" then "### Core API"), each with the lines under it as details; and headings that name a milestone themselves
// ("## Milestone 1: Core API", "### M2 - Web UI") wherever they are.
func ParseMilestones(text string, first int) []Milestone {
	var out []Milestone
	const (
		outside   = iota
		inList    // a list section: list items are milestones, indented lines their details
		inHeading // a milestone from a heading: every line until the next heading is a detail
	)
	// level is the heading level of the section (0 for a "Milestones:" line), item that of the heading the current milestone came from:
	// a heading deeper than item is one of its details.
	mode, level, item, details := outside, 0, 0, 0
	var written []string // the ID the person wrote for each milestone, "" for one without
	add := func(text string, own string) bool {
		if len(out) >= MaxMilestones {
			return false
		}
		text = strings.TrimSpace(ownID.ReplaceAllString(strings.TrimSpace(text), ""))
		if text == "" {
			return true
		}
		out = append(out, Milestone{ID: fmt.Sprintf("M%d", first+len(out)), Text: text})
		written = append(written, own)
		details = 0
		return true
	}
	detail := func(line string) {
		if len(out) == 0 {
			return
		}
		if m := listItem.FindStringSubmatch(line); m != nil {
			line = m[2]
		}
		if line = strings.TrimSpace(line); line == "" {
			return
		}
		sep := "; "
		if details == 0 {
			sep = " — "
		}
		details++
		if t := &out[len(out)-1].Text; utf8.RuneCountInString(*t) < MaxMilestoneRunes {
			*t += sep + line
		}
	}
	listed := -1 // the index of the milestone the current list item made, -1 when indented lines belong to none
	for _, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if h := heading.FindStringSubmatch(line); h != nil {
			lv, title := len(h[1]), strings.TrimSpace(h[2])
			switch {
			case mode == inHeading && lv > item:
				detail(title)
				continue
			case sectionTitle.MatchString(title):
				mode, level, listed = inList, lv, -1
				continue
			case namedTitle.MatchString(title):
				if !add(namedTitle.ReplaceAllString(title, ""), writtenID(title)) {
					return out
				}
				if mode == outside || lv <= level {
					level = 0 // only headings that name a milestone go on from here
				}
				mode, item, listed = inHeading, lv, -1
				continue
			case mode != outside && level > 0 && lv > level:
				if !add(title, writtenID(title)) {
					return out
				}
				mode, item, listed = inHeading, lv, -1
				continue
			}
			mode, level = outside, 0
			continue
		}
		if mode == outside && milestoneLabel.MatchString(line) {
			mode, level, listed = inList, 0, -1
			continue
		}
		switch {
		case mode == outside || line == "":
		case mode == inHeading:
			detail(line)
		case raw[0] != ' ' && raw[0] != '\t':
			if m := listItem.FindStringSubmatch(line); m != nil {
				n := len(out)
				if !add(m[2], writtenID(m[2])) {
					return out
				}
				listed = -1
				if len(out) > n {
					listed = len(out) - 1
				}
				continue
			}
			mode, level = outside, 0
		case listed >= 0 && listed == len(out)-1:
			detail(line)
		}
	}
	for i := range out {
		out[i].Text = oneLine(out[i].Text, MaxMilestoneRunes)
	}
	if ownIDs(written) {
		for i := range out {
			out[i].ID = written[i]
		}
	}
	return out
}

// writtenID is the ID the person wrote at the start of a milestone ("M0: Scaffold" is M0, "Milestone 2 - API" is M2), "" when none.
func writtenID(text string) string {
	m := writtenNumber.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil {
		return ""
	}
	n := strings.TrimLeft(m[1], "0")
	if n == "" {
		n = "0"
	}
	return "M" + n
}

// ownIDs reports whether every milestone has an ID the person wrote and no two share one.
func ownIDs(written []string) bool {
	seen := make(map[string]bool, len(written))
	for _, id := range written {
		if id == "" || seen[id] {
			return false
		}
		seen[id] = true
	}
	return len(written) > 0
}

// renumber gives ms the IDs M<first>, M<first+1>, ... in order.
func renumber(ms []Milestone, first int) {
	for i := range ms {
		ms[i].ID = fmt.Sprintf("M%d", first+i)
	}
}

// SpecRef is the spec file a goal names, "Spec: PATH" on a line of its own or at the very end of the goal (the path may be quoted or in
// backticks), or "" when it names none. A one-word value that does not look like a path ("Spec: TBD") is not a reference.
func SpecRef(objective string) string {
	m := specLine.FindStringSubmatch(objective)
	if m == nil {
		m = specEnd.FindStringSubmatch(objective)
	}
	if m == nil {
		return ""
	}
	p := m[1]
	if !strings.ContainsAny(p, "./\\") {
		return ""
	}
	return p
}

// Errors of reading a spec file.
var (
	ErrSpecOutside = errors.New("the spec file is outside the project")
	ErrSpecTooBig  = fmt.Errorf("the spec file is larger than %d KiB", MaxSpecBytes>>10)
	ErrSpecLinked  = errors.New("the spec file is a symbolic link, or is reached through one: name the file itself")
)

// Prepare is New for a goal that may name a spec file (SpecRef): the file is read now, relative to dir and inside root (dir when root is
// ""), and its text is kept with the goal, so that the judge holds the work to the spec as it was set and not as an agent may have edited
// it since. The milestones are those of the objective, then those of the spec. The project's rubric (RubricFile under root), when there is
// one, is read and kept the same way.
func Prepare(objective, dir, root string) (*State, error) {
	g := New(objective)
	g.Milestones = ParseMilestones(g.Objective, 1)
	if err := g.readRubric(dir, root); err != nil {
		return nil, err
	}
	ref := SpecRef(g.Objective)
	if ref == "" {
		return g, nil
	}
	text, err := readSpec(ref, dir, root)
	if err != nil {
		return nil, fmt.Errorf("spec %s: %w", ref, err)
	}
	g.Spec, g.SpecText = ref, text
	if room := MaxMilestones - len(g.Milestones); room > 0 {
		more := ParseMilestones(text, len(g.Milestones)+1)
		if len(more) > room {
			more = more[:room]
		}
		g.Milestones = append(g.Milestones, more...)
		ids := make([]string, len(g.Milestones))
		for i, m := range g.Milestones {
			ids[i] = m.ID
		}
		if !ownIDs(ids) {
			// the goal's own numbers and the spec's overlap: one sequence names them all
			renumber(g.Milestones, 1)
		}
	}
	return g, nil
}

// RubricFile is where a project keeps its rubric, relative to the project root: the standing criteria of good work that the judge holds
// every goal in the project to, beside the goal's own requirements.
const RubricFile = ".sleipnir/rubric.md"

// readRubric keeps the project's rubric with the goal when root (dir when root is "") has one: a missing file is no rubric, an empty one
// too, and one that cannot be read (a dangling link among them) or is larger than MaxSpecBytes is an error, since a goal set without the criteria its project asks for
// would be judged by a lower bar than the person set.
func (g *State) readRubric(dir, root string) error {
	if root == "" {
		root = dir
	}
	text, err := readSpec(RubricFile, root, root)
	switch {
	case errors.Is(err, errNoSuchFile) && !rubricEntryExists(root):
		return nil
	case errors.Is(err, errNoSuchFile):
		return fmt.Errorf("rubric %s: a link to a file that does not exist", RubricFile)
	case err != nil:
		return fmt.Errorf("rubric %s: %w", RubricFile, err)
	}
	if text != "" {
		g.Rubric, g.RubricText = RubricFile, text
	}
	return nil
}

// rubricEntryExists says whether root has an entry at RubricFile of any kind, a dangling symbolic link included: a project that put one
// there means to have a rubric, so a link that leads nowhere is an unreadable rubric and not the absence of one.
func rubricEntryExists(root string) bool {
	r, err := os.OpenRoot(root)
	if err != nil {
		return false
	}
	defer r.Close()
	_, err = r.Lstat(filepath.FromSlash(RubricFile))
	return err == nil
}

// errNoSuchFile is readSpec's error for a file that does not exist.
var errNoSuchFile = errors.New("no such file")

// specOpened runs between the open of a spec and the check of its path; a test moves the project away there.
var specOpened = func() {}

// specChecked runs between the check of a spec's path and its open; a test puts a pipe in the file's place there.
var specChecked = func() {}

// readSpec reads a spec file named by ref, relative to dir, inside root: through os.Root, so that neither ".." nor a symbolic link
// takes it out of the project.
func readSpec(ref, dir, root string) (string, error) {
	if root == "" {
		root = dir
	}
	p := ref
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	rel, err := filepath.Rel(root, p)
	if err != nil || !filepath.IsLocal(rel) {
		return "", ErrSpecOutside
	}
	// the reference may come from text the goal quotes (a finding's reproducer), not from the person: a file that holds secrets is
	// no spec
	if why := mdfile.SensitivePath(filepath.ToSlash(rel)); why != "" {
		return "", fmt.Errorf("the spec file is refused: %s", why)
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return "", err
	}
	defer r.Close()
	if fi, err := r.Stat(rel); err == nil && !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", ref) // opening a pipe would wait for a writer
	}
	specChecked()
	// Non-blocking, so a pipe put in the file's place after the check above cannot hold the open; the check of the opened
	// file below refuses it.
	f, err := r.OpenFile(rel, os.O_RDONLY|openNonblock, 0)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "", errNoSuchFile
	case err != nil && strings.Contains(err.Error(), "escapes"):
		return "", ErrSpecOutside
	case err != nil:
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	specOpened()
	// the file named is the file read: a link in the project (SPEC.md -> .env) would put whatever it reaches in front of the model
	// under the name the person chose. The path is resolved after the open and must name, without a link, the very file opened.
	{
		realRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			return "", err // the project cannot be resolved now (moved away): nothing says the file read is the file named
		}
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			return "", ErrSpecLinked
		}
		realRel, err := filepath.Rel(realRoot, real)
		if err != nil || !filepath.IsLocal(realRel) {
			return "", ErrSpecOutside
		}
		// Windows resolves a name to the case it has on disk (spec.md is SPEC.md); the Lstat below still tells a link from the file
		if named := filepath.Clean(rel); realRel != named && (runtime.GOOS != "windows" || !strings.EqualFold(realRel, named)) {
			return "", ErrSpecLinked
		}
		if lst, err := r.Lstat(rel); err != nil || !os.SameFile(lst, st) {
			return "", ErrSpecLinked
		}
	}
	if st.IsDir() {
		return "", errors.New("it is a directory")
	}
	if !st.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", ref)
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxSpecBytes+1))
	if err != nil {
		return "", err
	}
	if len(b) > MaxSpecBytes {
		return "", ErrSpecTooBig
	}
	return strings.TrimSpace(string(b)), nil
}

// Reconcile takes the judge's milestone rulings into the goal and returns the verdict the harness acts on. A milestone is met when the
// judge says so and cites evidence; a ruling the judge leaves out keeps the milestone as it was. "done" stands only when this verdict
// confirms every milestone with evidence: otherwise it becomes "continue", and the unconfirmed milestones lead what is left. A goal with no
// milestones gets v back as it is.
func (g *State) Reconcile(v Verdict) Verdict {
	if len(g.Milestones) == 0 {
		return v
	}
	ruled := map[string]MilestoneVerdict{}
	for _, r := range v.Milestones {
		ruled[strings.ToUpper(strings.TrimSpace(r.ID))] = r
	}
	var unconfirmed []Milestone
	for i := range g.Milestones {
		m := &g.Milestones[i]
		r, ok := ruled[m.ID]
		if ok {
			m.Met = r.Met && strings.TrimSpace(r.Evidence) != ""
			m.Evidence = ""
			if m.Met {
				m.Evidence = oneLine(r.Evidence, 200)
			}
		}
		if !ok || !m.Met {
			unconfirmed = append(unconfirmed, *m)
		}
	}
	if v.Kind != Done || len(unconfirmed) == 0 {
		return v
	}
	ids := make([]string, len(unconfirmed))
	left := make([]string, 0, len(unconfirmed)+len(v.Left))
	for i, m := range unconfirmed {
		ids[i] = m.ID
		left = append(left, oneLine(m.ID+": "+m.Text, 160))
	}
	left = append(left, v.Left...)
	if len(left) > 8 {
		left = left[:8]
	}
	return Verdict{Kind: Continue, Reason: oneLine("the judge did not confirm milestone "+strings.Join(ids, ", ")+" with evidence", 300), Left: left, Milestones: v.Milestones}
}

// MetCount is how many milestones the judge last found met.
func (g *State) MetCount() int {
	n := 0
	for _, m := range g.Milestones {
		if m.Met {
			n++
		}
	}
	return n
}

// milestonesForJudge is the milestones section of a judge's request: each milestone with its last ruling, and how to answer for them.
func milestonesForJudge(g *State) string {
	var b strings.Builder
	b.WriteString("MILESTONES (from the goal and its spec; rule on every one, in this verdict):\n")
	for _, m := range g.Milestones {
		state := "open"
		if m.Met {
			state = "met last time: " + m.Evidence
		}
		fmt.Fprintf(&b, "- %s [%s] %s\n", m.ID, state, m.Text)
	}
	b.WriteString(`With milestones, the final verdict carries a ruling for each one: {"verdict":"...","reason":"...","left":[...],"milestones":[{"id":"M1","met":true,"evidence":"records 12, 40: go test ./... passed after the last change"},{"id":"M2","met":false,"evidence":""}]}. "met" needs evidence you inspected, cited by record number; a milestone met last time is re-checked against newer changes. A milestone whose criteria a user reaches through a user interface over an API, or a client over a service, needs an end-to-end run of the assembled program newer than its last change; tests of each side alone leave it unmet. "done" requires every milestone met.` + "\n\n")
	return b.String()
}
