package taskgen

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

// Rejection reasons that only GitOptions.Picks produce: a picked change is named
// on purpose, so every pick that is not a candidate is reported with its reason
// instead of being skipped the way a history scan skips commits.
const (
	ReasonUnknownCommit = "the commit or its parent is not in the repository"
	ReasonNoTestChange  = "the change adds or edits no test file"
)

// Pick names one change to mine in place of a history scan (GitOptions.Picks).
type Pick struct {
	// Commit is the end of the change: a commit id or another revision.
	Commit string `json:"commit"`
	// Parent is the start the change is compared with and the task's starting
	// commit (default: Commit's first parent, which for a merge commit covers the
	// whole merged branch).
	Parent string `json:"parent,omitempty"`
	// Issue, when set, is the task's prompt in place of the commit message, and
	// its title decides the task kind: the task then says what was wrong rather
	// than how it was fixed.
	Issue *Issue `json:"issue,omitempty"`
	// Source is provenance (for example the pull request and issue URLs), copied
	// unchanged into Meta.Source and never shown to the agent.
	Source map[string]string `json:"source,omitempty"`
}

// Issue is the text of the issue a change resolved.
type Issue struct {
	// Ref names the issue ("#123" or "org/repo#123"). It is recorded in
	// Meta.Issue and left out of the prompt.
	Ref   string `json:"ref,omitempty"`
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
}

// ReadPicks reads a list of picks, one per line: a JSON object with the fields
// of Pick (unknown fields are an error), or a bare revision (the first word of
// the line). Blank lines and lines starting with # are skipped. Line numbers in
// errors count from 1.
func ReadPicks(r io.Reader) ([]Pick, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 16<<20) // an issue body can be long
	var out []Pick
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var p Pick
		if strings.HasPrefix(line, "{") {
			dec := json.NewDecoder(strings.NewReader(line))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&p); err != nil {
				return nil, fmt.Errorf("line %d: %w", n, err)
			}
		} else {
			p.Commit = strings.Fields(line)[0]
		}
		p.Commit, p.Parent = strings.TrimSpace(p.Commit), strings.TrimSpace(p.Parent)
		switch {
		case p.Commit == "":
			return nil, fmt.Errorf("line %d: no commit", n)
		case strings.HasPrefix(p.Commit, "-") || strings.HasPrefix(p.Parent, "-"):
			return nil, fmt.Errorf("line %d: a revision cannot start with '-'", n)
		case p.Issue != nil && strings.TrimSpace(p.Issue.Title+p.Issue.Body) == "":
			return nil, fmt.Errorf("line %d: the issue has neither a title nor a body", n)
		}
		out = append(out, p)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, errors.New("no commits listed")
	}
	return out, nil
}

// pickCommits turns GitOptions.Picks into candidates, in the order given and
// whatever their dates (Since, Until and MaxCommits do not apply). Each pick must
// change at least MinSourceFiles source files and one test file of a selected
// language, like a scanned commit; one that does not is rejected with its reason.
func pickCommits(ctx context.Context, r *repo, opts GitOptions, rep *Report) ([]commitInfo, error) {
	langOK := func(l string) bool { return len(opts.Languages) == 0 || containsFold(opts.Languages, l) }
	var res []commitInfo
	for i := range opts.Picks {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		p := &opts.Picks[i]
		rep.Examined++
		hash, err := r.resolve(ctx, p.Commit)
		if err != nil {
			rep.reject(p.Commit, "", ReasonUnknownCommit, firstLine(err.Error()))
			continue
		}
		parentRev := p.Parent
		if parentRev == "" {
			parentRev = hash + "^1"
		}
		parent, err := r.resolve(ctx, parentRev)
		if err != nil {
			rep.reject(hash, "", ReasonUnknownCommit, "parent "+parentRev+": "+firstLine(err.Error()))
			continue
		}
		head, err := r.git(ctx, "show", "-s", "--format=%aI%x1f%s", "--end-of-options", hash)
		if err != nil {
			return nil, err
		}
		dateText, subject, _ := strings.Cut(strings.TrimSpace(string(head)), "\x1f")
		date, err := time.Parse(time.RFC3339, dateText)
		if err != nil {
			return nil, fmt.Errorf("author date of %s: %w", hash, err)
		}
		changes, err := r.changes(ctx, parent, hash)
		if err != nil {
			return nil, err
		}
		ci := commitInfo{Hash: hash, Parent: parent, AuthorDate: date, Subject: subject, Pick: p}
		src, tests, bad := 0, 0, ""
		for _, c := range changes {
			if needsQuoting(c.Path) {
				bad = c.Path
				break
			}
			ci.Files = append(ci.Files, nameStatus{Status: c.Status, Path: c.Path})
			lang, kind := Classify(c.Path)
			switch {
			case kind == Source && langOK(lang):
				src++
			case kind == Test && c.Status != "D" && langOK(lang):
				tests++
			}
		}
		switch {
		case bad != "":
			rep.reject(hash, subject, ReasonUnsupportedName, bad)
		case src < opts.MinSourceFiles:
			rep.reject(hash, subject, ReasonSourceMinimum, fmt.Sprintf("%d", src))
		case tests < 1:
			rep.reject(hash, subject, ReasonNoTestChange, "")
		default:
			rep.Candidates++
			res = append(res, ci)
		}
	}
	return res, nil
}

// needsQuoting reports whether git would quote path in its default output (a
// control character, a double quote, a backslash or a non-ASCII byte): the
// history scan skips such commits, and a pick is rejected for them.
func needsQuoting(path string) bool {
	for i := 0; i < len(path); i++ {
		if c := path[i]; c < 0x20 || c == 0x7f || c == '"' || c == '\\' || c >= 0x80 {
			return true
		}
	}
	return false
}

// htmlCommentRe matches the HTML comments that issue templates leave in a body.
var htmlCommentRe = regexp.MustCompile(`(?s)<!--.*?-->`)

var blankRunRe = regexp.MustCompile(`\n{3,}`)

// maxIssueBytes bounds the issue text in a prompt; a longer body (typically a
// pasted log) is cut at a line boundary and the cut is marked.
const maxIssueBytes = 16 << 10

// CleanIssue removes HTML comments, links to commits and pull requests, and bare
// commit ids from an issue's text, and normalises line endings and blank lines.
// Unlike CleanMessage it keeps every line: an issue has no trailers, and a line
// such as "Bug: ..." is content.
func CleanIssue(text string) string {
	text = htmlCommentRe.ReplaceAllString(strings.ReplaceAll(text, "\r\n", "\n"), "")
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		line = upstreamLinkRe.ReplaceAllString(line, "")
		lines[i] = strings.TrimRight(sha40Re.ReplaceAllString(line, ""), " \t")
	}
	return strings.TrimSpace(blankRunRe.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}

// BuildIssuePrompt turns an issue into the task prompt: its cleaned title and
// body (at most maxIssueBytes of body), then the same closing instruction as
// BuildPrompt. The issue's Ref is not included.
func BuildIssuePrompt(is Issue) string {
	title := strings.Join(strings.Fields(CleanIssue(is.Title)), " ")
	body := CleanIssue(is.Body)
	if len(body) > maxIssueBytes {
		cut := strings.LastIndexByte(body[:maxIssueBytes], '\n')
		if cut <= 0 {
			cut = maxIssueBytes
			for cut > 0 && body[cut]&0xC0 == 0x80 { // not inside a UTF-8 sequence
				cut--
			}
		}
		body = strings.TrimRight(body[:cut], " \t\n") + "\n\n[The rest of the issue is omitted.]"
	}
	var b strings.Builder
	b.WriteString(strings.TrimSpace("Issue: " + title))
	if body != "" {
		b.WriteString("\n\n" + body)
	}
	b.WriteString("\n\nResolve the issue above. Your work will be checked by a test suite that you cannot see, so check the behaviour carefully; do not edit existing test files.")
	return b.String()
}
