package input

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
)

func mustOpen(t *testing.T, path string) *History {
	t.Helper()
	h, err := OpenHistory(path)
	if err != nil {
		t.Fatalf("OpenHistory(%s): %v", path, err)
	}
	return h
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

func TestHistoryInMemory(t *testing.T) {
	h := NewHistory()
	for _, s := range []string{"a", "b", "b", "  ", "", "c\n\n", "a"} {
		if err := h.Add(s); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"a", "b", "c", "a"}
	if got := h.Entries(); !reflect.DeepEqual(got, want) {
		t.Errorf("entries %q want %q (a duplicate of the last entry, blanks and trailing newlines are dropped)", got, want)
	}
	if h.Len() != 4 || h.At(0) != "a" || h.At(3) != "a" || h.At(4) != "" || h.At(-1) != "" {
		t.Error("Len and At")
	}
	if h.Path() != "" || h.Err() != nil {
		t.Error("in-memory history has no path and no error")
	}
	if err := h.Add(strings.Repeat("x", MaxEntryBytes+1)); err != nil || h.Len() != 4 {
		t.Error("an entry over MaxEntryBytes is not stored, and that is not an error")
	}
	if err := h.Add(strings.Repeat("x", MaxEntryBytes)); err != nil || h.Len() != 5 {
		t.Error("an entry of exactly MaxEntryBytes is stored")
	}
}

func TestHistoryRingKeepsTheNewest(t *testing.T) {
	h, _ := OpenHistoryLimits(filepath.Join(t.TempDir(), "h"), 3, 0)
	for i := 0; i < 10; i++ {
		h.Add(fmt.Sprint("e", i))
	}
	if got := h.Entries(); !reflect.DeepEqual(got, []string{"e7", "e8", "e9"}) {
		t.Errorf("%q", got)
	}
}

func TestHistoryFind(t *testing.T) {
	h := NewHistory()
	for _, s := range []string{"Git status", "go test", "git commit", "ls -la", "GIT push"} {
		h.Add(s)
	}
	cases := []struct {
		q    string
		from int
		want int
	}{
		{"git", 4, 4}, {"git", 3, 2}, {"git", 1, 0}, {"GIT", 4, 4}, {"GIT", 3, -1}, {"Git", 4, 0}, {"zzz", 4, -1},
		{"", 3, 3}, {"go", 99, 1}, {"git", -1, -1}, {"LS", 4, -1}, {"ls", 4, 3},
	}
	for _, c := range cases {
		if got := h.Find(c.q, c.from); got != c.want {
			t.Errorf("Find(%q, %d) = %d, want %d", c.q, c.from, got, c.want)
		}
	}
	if NewHistory().Find("x", 5) != -1 {
		t.Error("an empty history finds nothing")
	}
}

func TestHistoryPersistsAcrossEditors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "history")
	r1 := newRig(t, Options{History: mustOpen(t, path)})
	r1.send("first", kEnter, "two", kAltEnter, "lines", kEnter, "third \"quoted\" \\ back\\slash", kEnter)

	r2 := newRig(t, Options{History: mustOpen(t, path)})
	want := []string{"first", "two\nlines", "third \"quoted\" \\ back\\slash"}
	if got := r2.ed.History().Entries(); !reflect.DeepEqual(got, want) {
		t.Fatalf("second editor sees %q", got)
	}
	r2.send(kUp)
	if r2.state() != `third "quoted" \ back\slash|` {
		t.Errorf("Up in the second editor: %q", r2.state())
	}
	r2.send(kUp)
	if r2.state() != "two|\nlines" {
		t.Errorf("Up again (a multi-line entry, cursor at the end of its first line): %q", r2.state())
	}
	r2.send(kUp, kUp)
	if r2.state() != "first|" {
		t.Errorf("Up to the oldest and past it: %q", r2.state())
	}

	// the file: one JSON string per line, so the two-line entry is one line
	lines := readLines(t, path)
	if len(lines) != 3 {
		t.Fatalf("file has %d lines: %q", len(lines), lines)
	}
	for i, l := range lines {
		var s string
		if err := json.Unmarshal([]byte(l), &s); err != nil || s != want[i] {
			t.Errorf("line %d = %q (%v), want JSON of %q", i, l, err, want[i])
		}
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("history file mode %v (%v), want 0600", fi.Mode().Perm(), err)
		}
		if fi, err := os.Stat(filepath.Dir(path)); err != nil || fi.Mode().Perm() != 0o700 {
			t.Errorf("history directory mode %v (%v), want 0700", fi.Mode().Perm(), err)
		}
	}
}

func TestHistorySkipsCorruptLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history")
	good := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	content := strings.Join([]string{
		good("one"),
		"this is not json",
		"",
		`{"a": 1}`,
		"123",
		`"unterminated`,
		good("two"),
		`["array"]`,
		good("two"), // a duplicate of the previous entry
		"\xff\xfe\x00garbage",
		strings.Repeat("x", maxHistoryLine+10), // a line too long to hold
		good("three\nwith newline"),
		`null`,
		good("\x1b[31mred\x1b[0m \U0000202ebidi"), // escapes and a bidi override inside a JSON string
	}, "\n") + "\n" + good("no trailing newline")
	content = strings.TrimSuffix(content, "\n")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := OpenHistory(path)
	if err != nil {
		t.Fatalf("a corrupt file is not fatal: %v", err)
	}
	want := []string{"one", "two", "three\nwith newline", "red bidi", "no trailing newline"}
	if got := h.Entries(); !reflect.DeepEqual(got, want) {
		t.Errorf("entries\n got  %q\n want %q", got, want)
	}
	if h.Skipped() < 6 {
		t.Errorf("Skipped() = %d, want the corrupt lines counted", h.Skipped())
	}
	// it can still be appended to, and what is appended is readable
	if err := h.Add("four"); err != nil {
		t.Fatal(err)
	}
	h2 := mustOpen(t, path)
	if got := h2.Entries(); got[len(got)-1] != "four" || len(got) != len(want)+1 {
		t.Errorf("after append: %q", got)
	}
}

func TestHistoryEntriesAreOnlyData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history")
	h := mustOpen(t, path)
	nasty := []string{"%s %d %n", "$(touch /tmp/pwned)", "`id`", "; rm -rf /", "{{.Secret}}", "\x1b]52;c;ZXZpbA==\x07evil", "a\x1b[2Jb"}
	for _, s := range nasty {
		h.Add(s)
	}
	want := []string{"%s %d %n", "$(touch /tmp/pwned)", "`id`", "; rm -rf /", "{{.Secret}}", "evil", "ab"}
	if got := mustOpen(t, path).Entries(); !reflect.DeepEqual(got, want) {
		t.Errorf("got %q want %q", got, want)
	}
	r := newRig(t, Options{History: mustOpen(t, path)})
	r.send(kUp)
	if r.state() != "ab|" {
		t.Errorf("recalled text is data: %q", r.state())
	}
	for _, line := range readLines(t, path) {
		if strings.ContainsRune(line, 0x1b) {
			t.Errorf("an escape byte reached the file: %q", line)
		}
	}
}

func TestHistoryTightensPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions")
	}
	path := filepath.Join(t.TempDir(), "history")
	if err := os.WriteFile(path, []byte("\"old\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := mustOpen(t, path)
	if err := h.Add("new"); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
}

func TestHistoryWillNotUseANonRegularFile(t *testing.T) {
	dir := t.TempDir()
	h, err := OpenHistory(dir) // a directory
	if err == nil || h == nil {
		t.Fatalf("a directory is an error, with a usable history: %v %v", h, err)
	}
	if err := h.Add("x"); err == nil {
		t.Error("writing to a directory fails")
	}
	if h.Err() == nil {
		t.Error("Err reports the failed write")
	}
	if h.Len() != 1 {
		t.Error("the entry stays in memory for this session")
	}
	h2, err := OpenHistory("")
	if err == nil || h2 == nil {
		t.Error("an empty path is an error with a usable history")
	}
	if runtime.GOOS != "windows" {
		target := filepath.Join(dir, "target")
		os.WriteFile(target, []byte("\"x\"\n"), 0o600)
		link := filepath.Join(dir, "link")
		if os.Symlink(target, link) == nil {
			// a link to a regular file is followed, as a dotfile manager would want
			hl, err := OpenHistory(link)
			if err != nil || !reflect.DeepEqual(hl.Entries(), []string{"x"}) {
				t.Errorf("a symbolic link to a regular file works: %v %q", err, hl.Entries())
			}
			// a link to anything else is refused, like the thing itself
			dlink := filepath.Join(dir, "dirlink")
			if os.Symlink(dir, dlink) == nil {
				if _, err := OpenHistory(dlink); err == nil {
					t.Error("a symbolic link to a directory is refused")
				}
			}
		}
	}
}

func TestHistoryReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history")
	a, b := mustOpen(t, path), mustOpen(t, path)
	a.Add("from a")
	b.Add("from b")
	if err := a.Reload(); err != nil {
		t.Fatal(err)
	}
	if got := a.Entries(); !reflect.DeepEqual(got, []string{"from a", "from b"}) {
		t.Errorf("a after reload: %q", got)
	}
	if err := NewHistory().Reload(); err != nil {
		t.Error("reloading an in-memory history is a no-op")
	}
}

func TestHistoryFileLimits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history")
	h, _ := OpenHistoryLimits(path, 5, 0)
	for i := 0; i < 40; i++ {
		if err := h.Add(fmt.Sprintf("entry %02d", i)); err != nil {
			t.Fatal(err)
		}
		if n := len(readLines(t, path)); n > 2*5+1 {
			t.Fatalf("after %d adds the file has %d lines", i+1, n)
		}
	}
	got := mustOpenLimits(t, path, 5, 0).Entries()
	want := []string{"entry 35", "entry 36", "entry 37", "entry 38", "entry 39"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("reopened: %q", got)
	}

	// the byte cap
	path = filepath.Join(t.TempDir(), "history")
	h, _ = OpenHistoryLimits(path, 1000, 2000)
	for i := 0; i < 100; i++ {
		h.Add(fmt.Sprintf("%03d %s", i, strings.Repeat("y", 90)))
		if fi, err := os.Stat(path); err != nil || fi.Size() > 2000+200 {
			t.Fatalf("file size %d after %d adds (cap 2000)", fi.Size(), i+1)
		}
	}
	got = mustOpenLimits(t, path, 1000, 2000).Entries()
	if len(got) == 0 || !strings.HasPrefix(got[len(got)-1], "099 ") {
		t.Errorf("the newest entry survives compaction: %d entries, last %q", len(got), got[len(got)-1])
	}
	// an entry bigger than the whole cap is still kept when it is the newest
	h.Add(strings.Repeat("z", 5000))
	got = mustOpenLimits(t, path, 1000, 2000).Entries()
	if len(got) == 0 || got[len(got)-1] != strings.Repeat("z", 5000) {
		t.Errorf("the newest entry is never compacted away")
	}
}

func mustOpenLimits(t *testing.T, path string, n int, b int64) *History {
	t.Helper()
	h, err := OpenHistoryLimits(path, n, b)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestHistoryTailOfAHugeFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := bufio.NewWriter(f)
	for i := 0; i < 3000; i++ {
		fmt.Fprintf(w, "%q\n", fmt.Sprintf("entry %04d %s", i, strings.Repeat("p", 50)))
	}
	w.Flush()
	f.Close()
	h, err := OpenHistoryLimits(path, 10, 1000) // reads only the tail of the file: the last 4000 bytes
	if err != nil {
		t.Fatal(err)
	}
	got := h.Entries()
	if len(got) != 10 || !strings.HasPrefix(got[9], "entry 2999") {
		t.Errorf("%d entries, last %q", len(got), got[len(got)-1])
	}
	for _, e := range got {
		if !strings.HasPrefix(e, "entry ") || len(e) < 50 {
			t.Errorf("the line cut in half by the tail read must not become an entry: %q", e)
		}
	}
}

// Two sessions appending to one file at the same time (separate History values, as two processes would have) never corrupt it.
func TestHistoryConcurrentAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history")
	const writers, each = 8, 60
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for w := 0; w < writers; w++ {
		h, err := OpenHistoryLimits(path, 100000, 1<<30)
		if err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func(w int, h *History) {
			defer wg.Done()
			<-start
			for i := 0; i < each; i++ {
				// multi-line entries with a long body, so a torn write would show
				if err := h.Add(fmt.Sprintf("writer %d entry %d\n%s", w, i, strings.Repeat("ab", 500))); err != nil {
					errs <- err
					return
				}
			}
		}(w, h)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	checkAppendedFile(t, path, writers, each)
}

func checkAppendedFile(t *testing.T, path string, writers, each int) {
	t.Helper()
	seen := map[string]bool{}
	for i, l := range readLines(t, path) {
		if l == "" {
			continue // a blank line is legal: an appender that saw a half-written line ahead of its own adds a newline first
		}
		var s string
		if err := json.Unmarshal([]byte(l), &s); err != nil {
			t.Fatalf("line %d is damaged: %v: %.80q", i, err, l)
		}
		first, _, _ := strings.Cut(s, "\n")
		seen[first] = true
	}
	if len(seen) != writers*each {
		t.Errorf("%d distinct entries in the file, want %d", len(seen), writers*each)
	}
	h := mustOpen(t, path)
	if h.Skipped() != 0 || h.Len() != writers*each {
		t.Errorf("reopened: skipped %d, %d entries", h.Skipped(), h.Len())
	}
}

// The same with real processes: the test binary runs itself as the writers.
func TestHistoryTwoProcesses(t *testing.T) {
	if os.Getenv("SLEIPNIR_HISTORY_HELPER") != "" {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Skip("cannot find the test binary")
	}
	path := filepath.Join(t.TempDir(), "history")
	const procs, each = 3, 80
	var cmds []*exec.Cmd
	var outs []*strings.Builder
	for p := 0; p < procs; p++ {
		cmd := exec.Command(exe, "-test.run=^TestHistoryHelperProcess$")
		cmd.Env = append(os.Environ(), fmt.Sprintf("SLEIPNIR_HISTORY_HELPER=%s|%d|%d", path, p, each))
		out := &strings.Builder{}
		cmd.Stdout, cmd.Stderr = out, out
		outs = append(outs, out)
		cmds = append(cmds, cmd)
	}
	for _, c := range cmds {
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
	}
	for i, c := range cmds {
		if err := c.Wait(); err != nil {
			t.Fatalf("writer %d: %v\n%s", i, err, outs[i])
		}
	}
	checkAppendedFile(t, path, procs, each)
}

func TestHistoryHelperProcess(t *testing.T) {
	spec := os.Getenv("SLEIPNIR_HISTORY_HELPER")
	if spec == "" {
		return
	}
	var path string
	var id, n int
	parts := strings.Split(spec, "|")
	path = parts[0]
	fmt.Sscan(parts[1], &id)
	fmt.Sscan(parts[2], &n)
	h, err := OpenHistoryLimits(path, 100000, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if err := h.Add(fmt.Sprintf("writer %d entry %d\n%s", id, i, strings.Repeat("cd", 500))); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEditorHistoryNavigation(t *testing.T) {
	h := NewHistory()
	for _, s := range []string{"one", "two", "three"} {
		h.Add(s)
	}
	r := newRig(t, Options{History: h})
	r.send("dra")
	steps := []struct {
		key  string
		want string
	}{
		{kUp, "three|"}, {kUp, "two|"}, {kUp, "one|"}, {kUp, "one|"}, // the oldest stays
		{kDown, "two|"}, {kDown, "three|"}, {kDown, "dra|"}, // back to the draft
		{kDown, "dra|"}, // nothing newer than the draft
		{ctrl('p'), "three|"}, {ctrl('n'), "dra|"},
	}
	for _, s := range steps {
		r.send(s.key)
		if got := r.state(); got != s.want {
			t.Fatalf("after %q: %q want %q", s.key, got, s.want)
		}
	}
	// the draft keeps its cursor
	r.send(kLeft, kUp, kDown)
	if got := r.state(); got != "dr|a" {
		t.Errorf("draft cursor: %q", got)
	}
	// Up on a buffer with several lines moves inside it first
	r.ed.Reset()
	r.send("a", kAltEnter, "b", kUp)
	if got := r.state(); got != "a|\nb" {
		t.Errorf("Up inside the buffer: %q", got)
	}
	r.send(kUp)
	if got := r.state(); got != "three|" {
		t.Errorf("Up from the first line walks the history: %q", got)
	}
	r.send(kDown)
	if got := r.state(); got != "a|\nb" {
		t.Errorf("the multi-line draft comes back whole: %q", got)
	}
}

func TestEditorHistoryEditsAreDiscardedOnMove(t *testing.T) {
	h := NewHistory()
	h.Add("alpha")
	h.Add("beta")
	r := newRig(t, Options{History: h})
	r.send(kUp, "X", kUp)
	if got := r.state(); got != "alpha|" {
		t.Fatalf("got %q", got)
	}
	r.send(kDown)
	if got := r.state(); got != "beta|" {
		t.Errorf("the edit of beta is gone: %q", got)
	}
	if !reflect.DeepEqual(h.Entries(), []string{"alpha", "beta"}) {
		t.Error("history itself is untouched by edits")
	}
	// editing a recalled entry and submitting stores the edited text as a new entry
	r.send("!", kEnter)
	if got := h.Entries(); got[len(got)-1] != "beta!" || len(got) != 3 {
		t.Errorf("entries %q", got)
	}
	// and navigation starts over from the newest
	r.send(kUp)
	if got := r.state(); got != "beta!|" {
		t.Errorf("%q", got)
	}
}

func TestEditorHistoryCursorOnMultiLineEntries(t *testing.T) {
	h := NewHistory()
	h.Add("x\ny")
	h.Add("one\ntwo\nthree")
	r := newRig(t, Options{History: h})
	r.send(kUp)
	if got := r.state(); got != "one|\ntwo\nthree" {
		t.Fatalf("going up lands at the end of the first line: %q", got)
	}
	r.send(kUp)
	if got := r.state(); got != "x|\ny" {
		t.Fatalf("one more Up goes on through the history: %q", got)
	}
	// Down moves inside a multi-line entry first, and leaves it from its last line
	r.send(kDown)
	if got := r.state(); got != "x\ny|" {
		t.Fatalf("Down inside the entry: %q", got)
	}
	r.send(kDown)
	if got := r.state(); got != "one\ntwo\nthree|" {
		t.Fatalf("going down lands at the end: %q", got)
	}
	r.send(kDown)
	if got := r.state(); got != "|" {
		t.Fatalf("back to the (empty) draft: %q", got)
	}
}

func TestHistoryAppendRepairsAMissingNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history")
	if err := os.WriteFile(path, []byte(`"one"`+"\n"+`"two"`), 0o600); err != nil { // the last line was cut short of its newline
		t.Fatal(err)
	}
	h := mustOpen(t, path)
	if err := h.Add("three"); err != nil {
		t.Fatal(err)
	}
	if got := mustOpen(t, path).Entries(); !reflect.DeepEqual(got, []string{"one", "two", "three"}) {
		t.Errorf("%q", got)
	}
}

func TestSubmittedTextGoesToHistoryExpanded(t *testing.T) {
	h := NewHistory()
	r := newRig(t, Options{History: h})
	big := strings.Repeat("line of pasted text\n", 10)
	r.send("x ", paste(big), kEnter)
	got := h.Entries()
	if len(got) != 1 || got[0] != "x "+strings.TrimRight(big, "\n") {
		t.Errorf("history holds the expanded text: %q", got)
	}
	_ = sort.Strings
}
