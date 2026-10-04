package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testGit(t *testing.T, dir, date string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "commit.gpgsign=false", "-c", "core.hooksPath="}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid", "GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, b)
	}
	return strings.TrimSpace(string(b))
}

func historyFixture(t *testing.T) (string, string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git is required to inspect history")
	}
	dir := t.TempDir()
	oldDate := "2010-01-02T12:00:00Z"
	newDate := "2020-03-04T12:00:00Z"
	testGit(t, dir, oldDate, "init", "-q")
	testGit(t, dir, oldDate, "config", "core.autocrlf", "false")
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("old.go", "package example\n\n// Preserve this existing behavior through formatting and renames.\nvar stableValue = 12345\n")
	write("binary.dat", "\x00\x01binary")
	write("empty.txt", "")
	testGit(t, dir, oldDate, "add", ".")
	testGit(t, dir, oldDate, "commit", "-qm", "initial")
	first := testGit(t, dir, oldDate, "rev-parse", "HEAD")
	if err := os.Rename(filepath.Join(dir, "old.go"), filepath.Join(dir, "space [x] ü.go")); err != nil {
		t.Fatal(err)
	}
	write("space [x] ü.go", "package example\n\n// Preserve this existing behavior through formatting and renames.\nvar   stableValue   =   12345\n")
	write("new.go", "package example\nvar addedValue = 54321\n")
	testGit(t, dir, newDate, "add", "-A")
	testGit(t, dir, newDate, "commit", "-qm", "rename and format")
	return dir, first, testGit(t, dir, newDate, "rev-parse", "HEAD")
}

func TestCollectUsesCommittedLineHistoryAcrossRenameAndFormatting(t *testing.T) {
	dir, first, head := historyFixture(t)
	// Local text conversion must not change the committed content read by the audit.
	testGit(t, dir, "2025-01-01T00:00:00Z", "config", "diff.audit.textconv", "echo converted")
	if err := os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte("*.go diff=audit\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// Working edits must not masquerade as committed history or affect line numbers.
	if err := os.WriteFile(filepath.Join(dir, "space [x] ü.go"), []byte("changed working copy\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "untracked.go"), []byte("untracked\n"), 0644); err != nil {
		t.Fatal(err)
	}
	r, err := collect(context.Background(), dir, "HEAD", 3, true, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if r.Revision != head || len(r.Files) != 4 || len(r.Errors) != 0 {
		t.Fatalf("unexpected inventory: %+v", r)
	}
	byPath := map[string]fileAge{}
	for _, f := range r.Files {
		byPath[f.Path] = f
	}
	f := byPath["space [x] ü.go"]
	if f.Lines != 3 || f.Oldest >= f.LastChange || f.Median != f.Oldest {
		t.Fatalf("rename/formatting reset the original line ages: %+v", f)
	}
	for i, sample := range f.Samples {
		if sample.Commit != first || sample.Line != []int{1, 3, 4}[i] {
			t.Errorf("incorrect surviving line: %+v", sample)
		}
	}
	if byPath["binary.dat"].Oldest != 0 || !strings.HasPrefix(byPath["binary.dat"].Status, "binary") || byPath["empty.txt"].Status != "empty" {
		t.Fatalf("binary or empty inventory lost: %+v", byPath)
	}
	older, err := collect(context.Background(), dir, first, 1, true, r.AsOf)
	if err != nil || len(older.Files) != 3 {
		t.Fatalf("pinned old revision: %+v, %v", older, err)
	}
}

func TestShallowHistoryIsVisibleAndCanBeRefused(t *testing.T) {
	dir, _, _ := historyFixture(t)
	clone := filepath.Join(t.TempDir(), "clone")
	p := filepath.ToSlash(dir)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	source := (&url.URL{Scheme: "file", Path: p}).String()
	testGit(t, dir, "2025-01-01T00:00:00Z", "clone", "-q", "--depth=1", source, clone)
	r, err := collect(context.Background(), clone, "HEAD", 2, false, time.Now())
	if err != nil || !r.Shallow || len(r.Warnings) == 0 {
		t.Fatalf("shallow history was not disclosed: %+v, %v", r, err)
	}
	if _, err := collect(context.Background(), clone, "HEAD", 2, true, time.Now()); err == nil {
		t.Fatal("full-history audit accepted a shallow clone")
	}
}

func TestReportEscapesNamesAndIncludesCollectionErrors(t *testing.T) {
	dir := t.TempDir()
	name := "</script><script>alert('x')</script>"
	r := report{Revision: "abc", Files: []fileAge{{Path: name}}, Errors: []string{"unreadable file"}}
	if err := writeReports(dir, r); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte(name)) || !bytes.Contains(b, []byte(`\u003c/script\u003e`)) || !bytes.Contains(b, []byte("unreadable file")) {
		t.Fatal("HTML failed to escape repository data or omitted collection errors")
	}
	b, err = os.ReadFile(filepath.Join(dir, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got report
	if err := json.Unmarshal(b, &got); err != nil || got.Files[0].Path != name {
		t.Fatalf("JSON report changed the path: %+v, %v", got, err)
	}
}

func TestBlameRejectsIncompleteMetadata(t *testing.T) {
	for _, input := range []string{"\tline\n", strings.Repeat("a", 40) + " 1 1 1\ncommitter-time invalid\n\tline\n"} {
		if _, err := parseBlame([]byte(input)); err == nil {
			t.Errorf("accepted invalid blame: %q", input)
		}
	}
}

func TestRunRefusesInvalidOptionsAndCanceledCollection(t *testing.T) {
	for _, args := range [][]string{{"-jobs=0"}, {"-jobs=33"}, {"unexpected"}} {
		if err := run(context.Background(), args, &bytes.Buffer{}); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := collect(ctx, ".", "HEAD", 1, false, time.Now()); err == nil {
		t.Fatal("canceled collection succeeded")
	}
}

func TestCollectionFailureRetainsFilesAndFailsTheCommand(t *testing.T) {
	dir, _, _ := historyFixture(t)
	// Git rejects this broken local configuration before processing blame flags.
	testGit(t, dir, "2025-01-01T00:00:00Z", "config", "blame.ignoreRevsFile", "missing-ignore-file")
	out := t.TempDir()
	if err := run(context.Background(), []string{"-repo", dir, "-out", out}, &bytes.Buffer{}); err == nil {
		t.Fatal("collection errors did not fail the command")
	}
	b, err := os.ReadFile(filepath.Join(out, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var r report
	if err := json.Unmarshal(b, &r); err != nil || len(r.Errors) != 2 || len(r.Files) != 4 {
		t.Fatalf("failed files disappeared from the inventory: %+v, %v", r, err)
	}
}
