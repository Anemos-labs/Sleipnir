package repocheck

import (
	"net/url"
	"os"
	"path"
	"regexp"
	"strings"
	"testing"
)

var (
	fencedBlock   = regexp.MustCompile("(?ms)^[ \t]*(```|~~~).*?^[ \t]*(```|~~~)[ \t]*$")
	htmlComment   = regexp.MustCompile(`(?s)<!--.*?-->`)
	inlineCode    = regexp.MustCompile("`[^`\n]*`")
	inlineLink    = regexp.MustCompile(`!?\[[^\]\n]*\]\(\s*(?:<([^>\n]*)>|([^)\s]*))[^)\n]*\)`)
	referenceLink = regexp.MustCompile(`(?m)^ {0,3}\[[^\]\n]+\]:\s*(?:<([^>\n]*)>|(\S+))`)
	htmlAttribute = regexp.MustCompile(`(?i)\b(?:src|href|srcset|poster)\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	uriScheme     = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)
)

// markdownTargets returns the link targets of a markdown document that are meant to be files of the repository: inline
// and reference links and images, and the src, href and srcset of embedded HTML. Code blocks, code spans and HTML comments
// hold examples, not links, and are removed first.
func markdownTargets(text string) []string {
	text = fencedBlock.ReplaceAllString(text, "")
	text = htmlComment.ReplaceAllString(text, "")
	text = inlineCode.ReplaceAllString(text, "")
	var out []string
	add := func(s string) {
		for _, candidate := range strings.Split(s, ",") { // srcset: "a.svg 1x, b.svg 2x"
			if f := strings.Fields(candidate); len(f) > 0 {
				out = append(out, f[0])
			}
		}
	}
	for _, m := range inlineLink.FindAllStringSubmatch(text, -1) {
		out = append(out, m[1]+m[2])
	}
	for _, m := range referenceLink.FindAllStringSubmatch(text, -1) {
		out = append(out, m[1]+m[2])
	}
	for _, m := range htmlAttribute.FindAllStringSubmatch(text, -1) {
		add(m[1] + m[2])
	}
	return out
}

// localTarget turns a link target into a path relative to the repository root, or reports that it is not a file of the
// repository (another site, a mail address, an anchor on the same page).
func localTarget(docDir, target string) (rel string, ok bool) {
	target = strings.TrimSpace(target)
	if target == "" || strings.HasPrefix(target, "#") || strings.HasPrefix(target, "//") || uriScheme.MatchString(target) {
		return "", false
	}
	if i := strings.IndexAny(target, "#?"); i >= 0 {
		target = target[:i]
	}
	if target == "" {
		return "", false
	}
	if u, err := url.PathUnescape(target); err == nil {
		target = u
	}
	if strings.HasPrefix(target, "/") {
		return path.Clean(strings.TrimPrefix(target, "/")), true
	}
	return path.Clean(path.Join(docDir, target)), true
}

// existsExactly reports whether rel exists under the root with exactly this spelling: on a case-insensitive file system
// (macOS, Windows) a link with the wrong case would pass here and be dead on GitHub.
func existsExactly(root, rel string) bool {
	dir := root
	if rel == "." {
		return true
	}
	for _, part := range strings.Split(rel, "/") {
		if part == ".." {
			return false
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return false
		}
		found := false
		for _, e := range entries {
			if e.Name() == part {
				found = true
				break
			}
		}
		if !found {
			return false
		}
		dir = dir + string(os.PathSeparator) + part
	}
	return true
}

// Every relative link of every document must point at something that exists: a moved or renamed file otherwise leaves a
// dead link that nothing notices, in the README, the docs, the contributor guide or a template.
func TestMarkdownLinksResolve(t *testing.T) {
	r := root(t)
	checked, docs := 0, 0
	for _, f := range treeFiles(t) {
		if !strings.HasSuffix(f, ".md") || strings.HasPrefix(f, "bench/fixtures/") || strings.Contains(f, "/testdata/") {
			continue
		}
		docs++
		for _, target := range markdownTargets(read(t, f)) {
			rel, ok := localTarget(path.Dir(f), target)
			if !ok {
				continue
			}
			checked++
			if strings.HasPrefix(rel, "../") || rel == ".." {
				t.Errorf("%s: the link %q leaves the repository", f, target)
				continue
			}
			if !existsExactly(r, rel) {
				t.Errorf("%s: the link %q points at %s, which does not exist (moved or renamed? fix the link, mind the case)", f, target, rel)
			}
		}
	}
	if docs == 0 || checked == 0 {
		t.Fatalf("found %d documents and %d relative links: the test is not looking at the repository's documents", docs, checked)
	}
}

func TestMarkdownTargets(t *testing.T) {
	doc := "" +
		"[a](A.md) ![img](media/x.svg \"title\") [b](<with space.md>) [c](#anchor) [d](https://example.com/x.md) [e](mailto:a@b.c)\n" +
		"[f](F.md#frag) [g](../up.md?x=1)\n\n" +
		"[ref]: R.md\n" +
		"<img src=\"media/logo.svg\" width=\"5\"> <source srcset=\"media/dark.svg 1x, media/dark2.svg 2x\">\n\n" +
		"```sh\n[not](a/link.md)\n```\n" +
		"inline `[nor](this.md)` code and <!-- [nor](comment.md) --> a comment\n"
	var got []string
	for _, tgt := range markdownTargets(doc) {
		if rel, ok := localTarget("docs", tgt); ok {
			got = append(got, rel)
		}
	}
	want := []string{
		"docs/A.md", "docs/media/x.svg", "docs/with space.md", "docs/F.md", "up.md", "docs/R.md",
		"docs/media/logo.svg", "docs/media/dark.svg", "docs/media/dark2.svg",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("link targets:\n got  %q\n want %q", got, want)
	}
}

func TestLocalTarget(t *testing.T) {
	for _, tc := range []struct {
		dir, target, want string
		ok                bool
	}{
		{"docs", "SECURITY.md", "docs/SECURITY.md", true},
		{"docs", "../README.md", "README.md", true},
		{"docs", "/go.mod", "go.mod", true},
		{".", "docs/A%20B.md", "docs/A B.md", true},
		{"docs", "", "", false},
		{"docs", "#top", "", false},
		{"docs", "https://x.y/z", "", false},
		{"docs", "mailto:a@b.c", "", false},
		{"docs", "//cdn.example/x", "", false},
		{"docs", "a.md#frag", "docs/a.md", true},
	} {
		got, ok := localTarget(tc.dir, tc.target)
		if got != tc.want || ok != tc.ok {
			t.Errorf("localTarget(%q, %q) = %q, %v; want %q, %v", tc.dir, tc.target, got, ok, tc.want, tc.ok)
		}
	}
}
