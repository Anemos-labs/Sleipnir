package fs

import (
	"path/filepath"
	"testing"
)

const sectionDoc = "# Title\n\nintro\n\n## Setup\n\nstep one\n\n### Details\n\nfine print\n\n```sh\n# not a heading\n```\n\n## Usage ##\n\nrun it\n"

func TestReadSection(t *testing.T) {
	tests := []struct {
		name  string
		input map[string]any
		want  string
	}{
		{"by text", map[string]any{"path": "doc.md", "section": "Setup"},
			"     5\t## Setup\n     6\t\n     7\tstep one\n     8\t\n     9\t### Details\n    10\t\n    11\tfine print\n    12\t\n    13\t```sh\n    14\t# not a heading\n    15\t```\n    16\t"},
		{"case and hashes ignored", map[string]any{"path": "doc.md", "section": "## details"},
			"     9\t### Details\n    10\t\n    11\tfine print\n    12\t\n    13\t```sh\n    14\t# not a heading\n    15\t```\n    16\t"},
		{"closing hashes and end of file", map[string]any{"path": "doc.md", "section": "usage"},
			"    17\t## Usage ##\n    18\t\n    19\trun it"},
		{"path anchor", map[string]any{"path": "doc.md#setup"},
			"     5\t## Setup\n     6\t\n     7\tstep one\n     8\t\n     9\t### Details\n    10\t\n    11\tfine print\n    12\t\n    13\t```sh\n    14\t# not a heading\n    15\t```\n    16\t"},
		{"paged inside the section", map[string]any{"path": "doc.md", "section": "Setup", "limit": 2},
			"     5\t## Setup\n     6\t\n[showing lines 5-6 of 16; continue with offset=7]"},
		{"offset inside the section", map[string]any{"path": "doc.md", "section": "Setup", "offset": 15},
			"    15\t```\n    16\t"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := testEnv(t)
			writeFile(t, filepath.Join(env.Cwd, "doc.md"), sectionDoc)
			got := mustOK(t, run(t, Read{}, env, tc.input))
			if got != tc.want {
				t.Errorf("got:\n%q\nwant:\n%q", got, tc.want)
			}
		})
	}
}

func TestReadSectionRefusals(t *testing.T) {
	env := testEnv(t)
	writeFile(t, filepath.Join(env.Cwd, "doc.md"), sectionDoc)
	writeFile(t, filepath.Join(env.Cwd, "plain.txt"), "no headings here\n")

	msg := mustErr(t, run(t, Read{}, env, map[string]any{"path": "doc.md", "section": "Missing"}))
	contains(t, msg, `no heading "Missing"`, "# Title", "## Setup", "### Details", "## Usage")
	notContains(t, msg, "not a heading")

	msg = mustErr(t, run(t, Read{}, env, map[string]any{"path": "plain.txt", "section": "x"}))
	contains(t, msg, "no Markdown headings")

	writeFile(t, filepath.Join(env.Cwd, "empty.md"), "")
	msg = mustErr(t, run(t, Read{}, env, map[string]any{"path": "empty.md", "section": "x"}))
	contains(t, msg, "is empty", "no Markdown headings")

	msg = mustErr(t, run(t, Read{}, env, map[string]any{"path": "doc.md", "section": "Usage", "offset": 2}))
	contains(t, msg, "outside section", "lines 17-19")
}

func TestReadLiteralHashFileWins(t *testing.T) {
	env := testEnv(t)
	writeFile(t, filepath.Join(env.Cwd, "doc.md"), sectionDoc)
	writeFile(t, filepath.Join(env.Cwd, "doc.md#setup"), "literal\n")
	if got := mustOK(t, run(t, Read{}, env, map[string]any{"path": "doc.md#setup"})); got != "     1\tliteral" {
		t.Errorf("got %q", got)
	}
}

func TestHeadingSlug(t *testing.T) {
	for in, want := range map[string]string{
		"Getting Started":     "getting-started",
		"What's new? (v2)":    "whats-new-v2",
		"snake_case and-dash": "snake_case-and-dash",
	} {
		if got := headingSlug(in); got != want {
			t.Errorf("headingSlug(%q) = %q, want %q", in, got, want)
		}
	}
	if got := headingSlug("Install [SDK](guide.md) ![logo](l.png)"); got != "install-sdk-logo" {
		t.Errorf("linked heading slug = %q", got)
	}
	dup := mdHeadings([]byte("## Setup\na\n## Setup\nb\n## Install [SDK](guide.md)\nc\n"))
	if f, l, ok := findSection(dup, "setup-1", 6); !ok || f != 3 || l != 4 {
		t.Errorf("suffixed anchor = %d %d %v", f, l, ok)
	}
	if f, _, ok := findSection(dup, "#setup", 6); !ok || f != 1 {
		t.Errorf("first of repeated anchors = %d %v", f, ok)
	}
	if f, _, ok := findSection(dup, "install-sdk", 6); !ok || f != 5 {
		t.Errorf("linked heading anchor = %d %v", f, ok)
	}
	hs := mdHeadings([]byte("# What's new? (v2)\ntext\n"))
	if f, l, ok := findSection(hs, "whats-new-v2", 2); !ok || f != 1 || l != 2 {
		t.Errorf("slug lookup = %d %d %v", f, l, ok)
	}
}

func TestMdHeadingsIgnoresIndentedAndFenced(t *testing.T) {
	hs := mdHeadings([]byte("    # indented\n~~~\n# fenced\n~~~\n#nospace\n# real\n"))
	if len(hs) != 1 || hs[0].text != "real" || hs[0].line != 6 {
		t.Errorf("headings = %+v", hs)
	}
}
