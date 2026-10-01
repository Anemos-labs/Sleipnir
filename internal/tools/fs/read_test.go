package fs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

func TestReadFormatting(t *testing.T) {
	tests := []struct {
		name    string
		content string
		input   map[string]any
		want    string
	}{
		{"basic", "alpha\nbeta\n", nil, "     1\talpha\n     2\tbeta"},
		{"no trailing newline", "alpha\nbeta", nil, "     1\talpha\n     2\tbeta"},
		{"crlf hidden", "alpha\r\nbeta\r\n", nil, "     1\talpha\n     2\tbeta"},
		{"bom hidden", "\xef\xbb\xbfalpha\n", nil, "     1\talpha"},
		{"bom and crlf", "\xef\xbb\xbfalpha\r\nbeta\r\n", nil, "     1\talpha\n     2\tbeta"},
		{"blank lines kept", "a\n\n\nb\n", nil, "     1\ta\n     2\t\n     3\t\n     4\tb"},
		{"unicode", "héllo wörld\n日本語\n", nil, "     1\théllo wörld\n     2\t日本語"},
		{"only newline", "\n", nil, "     1\t"},
		{"offset", "a\nb\nc\nd\n", map[string]any{"offset": 3}, "     3\tc\n     4\td"},
		{"limit", "a\nb\nc\nd\n", map[string]any{"limit": 2}, "     1\ta\n     2\tb\n[showing lines 1-2 of 4; continue with offset=3]"},
		{"offset and limit", "a\nb\nc\nd\ne\n", map[string]any{"offset": 2, "limit": 2}, "     2\tb\n     3\tc\n[showing lines 2-3 of 5; continue with offset=4]"},
		{"float offset accepted", "a\nb\nc\n", map[string]any{"offset": 2.0}, "     2\tb\n     3\tc"},
		{"offset zero is start", "a\nb\n", map[string]any{"offset": 0}, "     1\ta\n     2\tb"},
		{"lone CR kept", "a\rb\nc\n", nil, "     1\ta\rb\n     2\tc"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := testEnv(t)
			writeFile(t, filepath.Join(env.Cwd, "f.txt"), tc.content)
			in := map[string]any{"path": "f.txt"}
			for k, v := range tc.input {
				in[k] = v
			}
			got := mustOK(t, run(t, Read{}, env, in))
			if got != tc.want {
				t.Errorf("got:\n%q\nwant:\n%q", got, tc.want)
			}
		})
	}
}

func TestReadEmptyFile(t *testing.T) {
	env := testEnv(t)
	writeFile(t, filepath.Join(env.Cwd, "empty"), "")
	if got := mustOK(t, run(t, Read{}, env, map[string]any{"path": "empty"})); got != "(file is empty)" {
		t.Errorf("got %q", got)
	}
	// An empty file is still "read": overwriting it afterwards must be allowed.
	mustOK(t, run(t, Write{}, env, map[string]any{"path": "empty", "content": "x"}))
}

func TestReadDefaultsAndContinuation(t *testing.T) {
	env := testEnv(t)
	env.Limits.MaxOutputChars = 1 << 20 // no output-size paging in this test
	writeFile(t, filepath.Join(env.Cwd, "big.txt"), lines(5000))
	res := run(t, Read{}, env, map[string]any{"path": "big.txt"})
	got := mustOK(t, res)
	if !strings.Contains(got, "  2000\tline 2000") || strings.Contains(got, "\tline 2001") {
		t.Errorf("default should stop at line 2000")
	}
	if res.Meta["lines"] != 5000 || res.Meta["from"] != 1 || res.Meta["to"] != 2000 || res.Meta["path"] != "big.txt" {
		t.Errorf("meta = %v", res.Meta)
	}
	contains(t, got, "[showing lines 1-2000 of 5000; continue with offset=2001]")

	got = mustOK(t, run(t, Read{}, env, map[string]any{"path": "big.txt", "offset": 4990}))
	contains(t, got, "  4990\tline 4990", "  5000\tline 5000")
	notContains(t, got, "[showing")
}

func TestReadOutputBudgetPagesInsteadOfEliding(t *testing.T) {
	env := testEnv(t)
	env.Limits.MaxOutputChars = 2000
	writeFile(t, filepath.Join(env.Cwd, "big.txt"), lines(1000))
	res := run(t, Read{}, env, map[string]any{"path": "big.txt"})
	text := mustOK(t, res)
	if res.Truncated {
		t.Errorf("read must page within the budget, not let Finish elide the middle")
	}
	if len(text) > 2000 {
		t.Errorf("output %d chars exceeds budget", len(text))
	}
	// Contiguous from line 1 and the footer names the next offset.
	contains(t, text, "     1\tline 1", "continue with offset=")
	var last int
	fmt.Sscanf(text[strings.LastIndex(text, "offset=")+len("offset="):], "%d", &last)
	if last < 50 {
		t.Fatalf("continuation offset %d too small", last)
	}
	res2 := run(t, Read{}, env, map[string]any{"path": "big.txt", "offset": last})
	contains(t, mustOK(t, res2), fmt.Sprintf("%6d\tline %d", last, last))
}

func TestReadAlwaysReturnsAtLeastOneLine(t *testing.T) {
	env := testEnv(t)
	env.Limits.MaxOutputChars = 300
	writeFile(t, filepath.Join(env.Cwd, "wide.txt"), strings.Repeat("x", 1900)+"\nsecond\n")
	text := mustOK(t, run(t, Read{}, env, map[string]any{"path": "wide.txt"}))
	contains(t, text, "     1\txxx")
}

func TestReadLongLinesCut(t *testing.T) {
	env := testEnv(t)
	long := strings.Repeat("a", 2500)
	uni := strings.Repeat("é", 2100) // 4200 bytes but 2100 chars
	writeFile(t, filepath.Join(env.Cwd, "f"), long+"\n"+uni+"\nshort\n"+strings.Repeat("b", 2000)+"\n")
	text := mustOK(t, run(t, Read{}, env, map[string]any{"path": "f"}))
	contains(t, text,
		strings.Repeat("a", 2000)+"… [line truncated: 2500 chars]",
		strings.Repeat("é", 2000)+"… [line truncated: 2100 chars]",
		"\tshort",
		"\t"+strings.Repeat("b", 2000), // exactly 2000 chars is not cut
	)
	notContains(t, text, strings.Repeat("a", 2001))
	if strings.Count(text, "line truncated") != 2 {
		t.Errorf("only the two long lines should be marked:\n%s", text[:200])
	}
}

func TestReadInvalidUTF8IsSanitised(t *testing.T) {
	env := testEnv(t)
	writeFile(t, filepath.Join(env.Cwd, "latin1.txt"), "caf\xe9 au lait\n")
	text := mustOK(t, run(t, Read{}, env, map[string]any{"path": "latin1.txt"}))
	contains(t, text, "caf� au lait")
	// The file itself is untouched.
	if got := readFileT(t, filepath.Join(env.Cwd, "latin1.txt")); got != "caf\xe9 au lait\n" {
		t.Errorf("read modified the file: %q", got)
	}
}

func TestReadBinary(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"nul", "abc\x00def", "binary file"},
		{"nul late", strings.Repeat("a", 100000) + "\x00", "binary file"},
		{"utf16", "\xff\xfea\x00b\x00", "UTF-16"},
		{"elf-like", "\x7fELF\x02\x01\x01\x00", "binary file"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := testEnv(t)
			writeFile(t, filepath.Join(env.Cwd, "b.bin"), tc.content)
			contains(t, mustErr(t, run(t, Read{}, env, map[string]any{"path": "b.bin"})), tc.want, "b.bin")
		})
	}
}

func TestReadErrors(t *testing.T) {
	env := testEnv(t)
	writeFile(t, filepath.Join(env.Cwd, "src", "main.go"), "package main\n")
	writeFile(t, filepath.Join(env.Cwd, "small.txt"), "a\nb\n")
	tests := []struct {
		name  string
		input any
		want  []string
	}{
		{"directory", map[string]any{"path": "src"}, []string{"is a directory", "use ls"}},
		{"missing", map[string]any{"path": "nope.txt"}, []string{"file not found: nope.txt"}},
		{"typo suggestion", map[string]any{"path": "src/main.gp"}, []string{"file not found", "did you mean src/main.go"}},
		{"missing dir", map[string]any{"path": "nodir/x.txt"}, []string{"file not found: nodir/x.txt"}},
		{"empty path", map[string]any{"path": ""}, []string{"path is required"}},
		{"blank path", map[string]any{"path": "   "}, []string{"path is required"}},
		{"nul in path", map[string]any{"path": "a\x00b"}, []string{"NUL"}},
		{"negative offset", map[string]any{"path": "small.txt", "offset": -1}, []string{"offset must be 1 or more"}},
		{"negative limit", map[string]any{"path": "small.txt", "limit": -5}, []string{"limit must be positive"}},
		{"offset past end", map[string]any{"path": "small.txt", "offset": 3}, []string{"offset 3 is past the end", "2 lines"}},
		{"string offset", `{"path":"small.txt","offset":"2"}`, []string{`argument "offset" must be an integer (got string)`}},
		{"fractional offset", `{"path":"small.txt","offset":1.5}`, []string{`argument "offset" must be an integer`}},
		{"number path", `{"path":5}`, []string{`argument "path" must be a string (got number)`}},
		{"not an object", `[1,2]`, []string{"arguments must be a JSON object"}},
		{"broken json", `{"path":`, []string{"not valid JSON"}},
		{"empty input", ``, []string{"path is required"}},
		{"null input", `null`, []string{"path is required"}},
		{"huge path", map[string]any{"path": strings.Repeat("a/", 20000)}, []string{"too long"}},
	}
	// On a case-insensitive file system (macOS, Windows) src/MAIN.go is src/main.go:
	// the read succeeds and there is nothing to suggest.
	if !caseInsensitiveDir(t, env.Cwd) {
		tests = append(tests, struct {
			name  string
			input any
			want  []string
		}{"case suggestion", map[string]any{"path": "src/MAIN.go"}, []string{"did you mean src/main.go"}})
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			contains(t, mustErr(t, run(t, Read{}, env, tc.input)), tc.want...)
		})
	}
}

func TestReadUnknownFieldsTolerated(t *testing.T) {
	env := testEnv(t)
	writeFile(t, filepath.Join(env.Cwd, "f"), "x\n")
	mustOK(t, run(t, Read{}, env, `{"path":"f","surprise":{"a":1},"another":[1,2]}`))
	// Some providers double-encode the arguments as a JSON string.
	mustOK(t, run(t, Read{}, env, `"{\"path\":\"f\"}"`))
}

func TestReadAbsoluteAndRelativePaths(t *testing.T) {
	env := testEnv(t)
	writeFile(t, filepath.Join(env.Cwd, "d", "f.txt"), "hello\n")
	for _, p := range []string{"d/f.txt", "./d/f.txt", "d/../d/f.txt", filepath.Join(env.Cwd, "d", "f.txt"), "d//f.txt"} {
		contains(t, mustOK(t, run(t, Read{}, env, map[string]any{"path": p})), "hello")
	}
}

func TestReadTildeExpandsToHome(t *testing.T) {
	home := realTemp(t)
	t.Setenv("HOME", home)
	writeFile(t, filepath.Join(home, "note.txt"), "from home\n")
	env := testEnv(t)
	contains(t, mustOK(t, run(t, Read{}, env, map[string]any{"path": "~/note.txt"})), "from home")
}

func TestReadSymlinks(t *testing.T) {
	env := testEnv(t)
	writeFile(t, filepath.Join(env.Cwd, "real.txt"), "target\n")
	if err := os.Symlink("real.txt", filepath.Join(env.Cwd, "link.txt")); err != nil {
		t.Skip("symlinks unsupported")
	}
	contains(t, mustOK(t, run(t, Read{}, env, map[string]any{"path": "link.txt"})), "target")

	// Loops must produce an error, not hang or panic.
	os.Symlink("loop_b", filepath.Join(env.Cwd, "loop_a"))
	os.Symlink("loop_a", filepath.Join(env.Cwd, "loop_b"))
	contains(t, mustErr(t, run(t, Read{}, env, map[string]any{"path": "loop_a"})), "symlink loop")
	os.Symlink(".", filepath.Join(env.Cwd, "self"))
	contains(t, mustErr(t, run(t, Read{}, env, map[string]any{"path": strings.Repeat("self/", 300) + "real.txt"})), "symlink loop")

	// A dangling link is "not found", not a crash.
	os.Symlink("missing.txt", filepath.Join(env.Cwd, "dangling"))
	contains(t, mustErr(t, run(t, Read{}, env, map[string]any{"path": "dangling"})), "not found")
}

func TestReadSizeLimits(t *testing.T) {
	env := testEnv(t)
	env.Limits.MaxReadBytes = 1000
	big := filepath.Join(env.Cwd, "big.txt")
	writeFile(t, big, strings.Repeat("0123456789\n", 500)) // 5500 bytes

	contains(t, mustErr(t, run(t, Read{}, env, map[string]any{"path": "big.txt"})), "larger than", "offset and limit")
	// With a range the same file is readable...
	contains(t, mustOK(t, run(t, Read{}, env, map[string]any{"path": "big.txt", "limit": 3})), "     3\t0123456789")
	contains(t, mustOK(t, run(t, Read{}, env, map[string]any{"path": "big.txt", "offset": 499})), "   500\t")

	// ...but never beyond the hard 32MB cap. A sparse file keeps the test cheap.
	huge := filepath.Join(env.Cwd, "huge.log")
	f, err := os.Create(huge)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(33 << 20); err != nil {
		t.Skip("cannot create sparse file")
	}
	f.Close()
	contains(t, mustErr(t, run(t, Read{}, env, map[string]any{"path": "huge.log", "limit": 5})), "33.0 MB", "grep")
}

func TestReadRangeOnLargeFileUsesWholeFileForStaleness(t *testing.T) {
	env := testEnv(t)
	env.Limits.MaxReadBytes = 100
	p := filepath.Join(env.Cwd, "f.txt")
	writeFile(t, p, lines(100))
	mustOK(t, run(t, Read{}, env, map[string]any{"path": "f.txt", "offset": 1, "limit": 2}))
	// A partial read counts as having read the file...
	mustOK(t, run(t, Edit{}, env, map[string]any{"path": "f.txt", "old_string": "line 50\n", "new_string": "LINE 50\n"}))
	// ...and a change by someone else after it is detected.
	other := agentEnv(env, "a2")
	mustOK(t, run(t, Read{}, other, map[string]any{"path": "f.txt", "limit": 1}))
	mustOK(t, run(t, Edit{}, other, map[string]any{"path": "f.txt", "old_string": "line 60\n", "new_string": "LINE 60\n"}))
	contains(t, mustErr(t, run(t, Edit{}, env, map[string]any{"path": "f.txt", "old_string": "line 70\n", "new_string": "LINE 70\n"})),
		"changed since you last read it", "agent a2")
}

func TestReadImages(t *testing.T) {
	png := "\x89PNG\r\n\x1a\n" + strings.Repeat("\x00", 40)
	jpg := "\xff\xd8\xff\xe0" + strings.Repeat("\x00", 20)
	gif := "GIF89a" + strings.Repeat("\x00", 20)
	webp := "RIFF\x24\x00\x00\x00WEBPVP8 " + strings.Repeat("\x00", 20)
	tests := []struct {
		name, file, data, mt string
	}{
		{"png", "a.png", png, "image/png"},
		{"jpg", "a.jpg", jpg, "image/jpeg"},
		{"jpeg upper", "A.JPEG", jpg, "image/jpeg"},
		{"gif", "a.gif", gif, "image/gif"},
		{"webp", "a.webp", webp, "image/webp"},
		{"extension lies", "photo.png", jpg, "image/jpeg"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := testEnv(t)
			writeFile(t, filepath.Join(env.Cwd, tc.file), tc.data)
			res := run(t, Read{}, env, map[string]any{"path": tc.file})
			text := mustOK(t, res)
			contains(t, text, tc.file, tc.mt)
			if len(res.Blocks) != 1 || res.Blocks[0].Kind != core.BlockImage || res.Blocks[0].MediaType != tc.mt {
				t.Fatalf("blocks = %+v", res.Blocks)
			}
			data, err := env.Blobs.Get(core.Hash(res.Blocks[0].MediaRef))
			if err != nil || string(data) != tc.data {
				t.Errorf("blob does not hold the image bytes (err=%v)", err)
			}
		})
	}

	t.Run("fake image is text", func(t *testing.T) {
		env := testEnv(t)
		writeFile(t, filepath.Join(env.Cwd, "notes.png"), "just text\n")
		res := run(t, Read{}, env, map[string]any{"path": "notes.png"})
		contains(t, mustOK(t, res), "just text")
		if len(res.Blocks) != 0 {
			t.Errorf("text file must not become an image block")
		}
	})
	t.Run("too large", func(t *testing.T) {
		env := testEnv(t)
		env.Limits.MaxReadBytes = 64
		writeFile(t, filepath.Join(env.Cwd, "big.png"), png+strings.Repeat("\x00", 100))
		contains(t, mustErr(t, run(t, Read{}, env, map[string]any{"path": "big.png", "limit": 1})), "image", "limit")
	})
	t.Run("no blob store", func(t *testing.T) {
		env := testEnv(t)
		env.Blobs = nil
		writeFile(t, filepath.Join(env.Cwd, "a.png"), png)
		contains(t, mustErr(t, run(t, Read{}, env, map[string]any{"path": "a.png"})), "no blob store")
	})
}

func TestReadPermission(t *testing.T) {
	env := testEnv(t)
	h := &hooks{denyPerm: func(r perm.Request) string { return "secrets are off limits" }}
	withHooks(env, h)
	writeFile(t, filepath.Join(env.Cwd, "s.txt"), "secret\n")
	text := mustErr(t, run(t, Read{}, env, map[string]any{"path": "s.txt"}))
	contains(t, text, "permission denied", "secrets are off limits")
	notContains(t, text, "secret\n")

	reqs := h.Requests()
	if len(reqs) != 1 {
		t.Fatalf("want 1 request, got %d", len(reqs))
	}
	r := reqs[0]
	if r.Tool != "read" || r.Writes || len(r.Paths) != 1 || r.Paths[0] != filepath.Join(env.Cwd, "s.txt") || r.Agent != "a1" || r.Summary == "" {
		t.Errorf("bad request: %+v", r)
	}
}

func TestReadPermissionRunsBeforeAnyFileAccess(t *testing.T) {
	// A denied read of a missing file must say "denied", not "not found": the
	// engine, not the filesystem, decides what an agent may learn.
	env := testEnv(t)
	withHooks(env, &hooks{denyPerm: func(perm.Request) string { return "no" }})
	contains(t, mustErr(t, run(t, Read{}, env, map[string]any{"path": "ghost.txt"})), "permission denied")
}

func TestReadRecordsFileStateForWholeFile(t *testing.T) {
	env := testEnv(t)
	p := filepath.Join(env.Cwd, "f.txt")
	writeFile(t, p, "one\ntwo\nthree\n")
	mustOK(t, run(t, Read{}, env, map[string]any{"path": "f.txt", "limit": 1}))
	if err := env.Files.CheckFresh("a1", p, []byte("one\ntwo\nthree\n"), true); err != nil {
		t.Errorf("a partial read should record the whole file: %v", err)
	}
}

func TestReadNilEnvFieldsAreDefaulted(t *testing.T) {
	// Tools must cope with an Env whose optional fields are all nil, and a nil
	// Env inside the call.
	dir := realTemp(t)
	writeFile(t, filepath.Join(dir, "f"), "x\n")
	env := &tools.Env{Cwd: dir, Perm: perm.AllowAll{}}
	contains(t, mustOK(t, run(t, Read{}, env, map[string]any{"path": "f"})), "x")
	if env.Files == nil || env.Guard == nil || env.Perm == nil {
		t.Errorf("Defaults() was not applied to the caller's Env")
	}
	// A call with no Env at all is tolerated, and fails closed: with no permission policy
	// to ask, nothing is read (S46).
	res, err := Read{}.Run(context.Background(), &tools.Call{Input: []byte(`{"path":"` + filepath.Join(dir, "f") + `"}`)})
	if err != nil || !res.IsError || !strings.Contains(res.Text, "no permission policy") {
		t.Errorf("a nil Env must not panic and must be denied: %v %v", err, res)
	}
}

func TestFilePathAliasIsAccepted(t *testing.T) {
	// Other harnesses call the parameter file_path; models trained on them use it.
	env := testEnv(t)
	writeFile(t, filepath.Join(env.Cwd, "f.txt"), "one\ntwo\n")
	contains(t, mustOK(t, run(t, Read{}, env, `{"file_path":"f.txt"}`)), "     1\tone")
	mustOK(t, run(t, Edit{}, env, `{"file_path":"f.txt","old_string":"one","new_string":"1"}`))
	mustOK(t, run(t, Write{}, env, `{"file_path":"g.txt","content":"x"}`))
	if readFileT(t, filepath.Join(env.Cwd, "f.txt")) != "1\ntwo\n" || readFileT(t, filepath.Join(env.Cwd, "g.txt")) != "x" {
		t.Errorf("alias not honoured")
	}
	// path wins when both are given.
	contains(t, mustOK(t, run(t, Read{}, env, `{"path":"g.txt","file_path":"f.txt"}`)), "x")
}

func TestPatternAsPathGetsAHint(t *testing.T) {
	env := testEnv(t)
	contains(t, mustErr(t, run(t, &Grep{}, env, map[string]any{"pattern": "x", "path": "src/**/*.go"})), "not a pattern", "glob")
	contains(t, mustErr(t, run(t, LS{}, env, map[string]any{"path": "*.go"})), "not a pattern")
	contains(t, mustErr(t, run(t, Read{}, env, map[string]any{"path": "src/*.go"})), "not a pattern")
	notContains(t, mustErr(t, run(t, Read{}, env, map[string]any{"path": "plain.go"})), "not a pattern")
}
