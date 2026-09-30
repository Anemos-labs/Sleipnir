package events

// Security regression tests for docs/reviews/security-robustness.md (events
// findings S33-S36, part of F12).
//
// TestSecReview_S33..S36 pin the fixes; they began as repro tests that failed while
// the findings were open. The write-time redaction half of S35 was decided the
// other way: the log is verbatim and private, redaction happens at export
// (TestLogIsVerbatimAndPrivateByDesign).
//
// TestSecSound_* pin behaviour the review found sound.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/reee344/sleipnir/internal/core"
)

// secRevFiles lists every regular file under dir (independent of the blob layout).
func secRevFiles(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			out = append(out, p)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func secRevGate(t *testing.T) {
	t.Helper()
	if os.Getenv("SLEIPNIR_REVIEW") == "" {
		t.Skip("security-review repro: set SLEIPNIR_REVIEW=1 (asserts the secure behaviour, fails while the finding is open)")
	}
}

func secRevUnixPerms(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" || runtime.GOOS == "plan9" {
		t.Skip("permission bits are not meaningful here")
	}
}

// evLine builds one event line for hand-made logs.
func evLine(seq int, typ string) string {
	return fmt.Sprintf(`{"seq":%d,"ts":"2026-09-30T00:00:00Z","session":"s","type":%q}`+"\n", seq, typ)
}

// evSeqs scans path and returns the sequence numbers delivered and Scan's error.
func evSeqs(t *testing.T, path string) ([]uint64, error) {
	t.Helper()
	var seqs []uint64
	err := Scan(path, func(e Event) error {
		seqs = append(seqs, e.Seq)
		return nil
	})
	return seqs, err
}

// S33: DirBlobs.path builds a filesystem path from the hash string without validating it, so
// any string that reaches Get/Has (a Block.MediaRef from a tool result or provider, a hash read
// from a tampered log) walked out of the blob directory. Only 64 lowercase hex digits are hashes now.
func TestSecReview_S33_BlobHashIsNotValidatedAsAPath(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "state", "blobs")
	secret := filepath.Join(base, "secret.txt")
	if err := os.WriteFile(secret, []byte("TOP SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := NewDirBlobs(dir)
	if err != nil {
		t.Fatal(err)
	}
	// path() is Join(dir, h[:2], h[2:4], h). For h = "..//" + "../"*k + name the components are
	// "..", "//" (a no-op after Clean) and the whole string, which climbs k+1 levels from dir/..
	for k := 0; k <= 6; k++ {
		evil := core.Hash("..//" + strings.Repeat("../", k) + "secret.txt")
		got, gerr := b.Get(evil)
		if gerr == nil || len(got) != 0 {
			t.Fatalf("S33: Get(%q) = %q, %v: a hash that is not 64 hex digits must be refused", evil, got, gerr)
		}
		if !errors.Is(gerr, ErrInvalidHash) {
			t.Fatalf("Get(%q) err = %v, want ErrInvalidHash", evil, gerr)
		}
		if b.Has(evil) {
			t.Fatalf("S33: Has(%q) is an existence oracle for files outside the store", evil)
		}
	}
}

// Everything that is not exactly 64 lowercase hex digits is refused before any path is built, and
// the error neither echoes control characters nor grows with the input.
func TestSecReview_S33_MalformedHashesAreRejected(t *testing.T) {
	b, err := NewDirBlobs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	good := string(core.HashString("x"))
	bad := []string{
		"", "_", "..", "../x", "/etc/passwd", "a/b", strings.Repeat("a", 63), strings.Repeat("a", 65),
		strings.ToUpper(good), "g" + good[1:], good[:63] + "\x00", good[:32] + "/" + good[33:], good[:62] + "..",
		"\u00e9" + good[2:], good + "\n", "sha256:" + good, strings.Repeat("../", 40) + good,
		"..\\..\\windows\\system32", strings.Repeat("a", 1<<20),
	}
	for _, h := range bad {
		if b.Has(core.Hash(h)) {
			t.Errorf("Has(%.20q) = true", h)
		}
		_, err := b.Get(core.Hash(h))
		if !errors.Is(err, ErrInvalidHash) {
			t.Errorf("Get(%.20q) err = %v, want ErrInvalidHash", h, err)
			continue
		}
		if len(err.Error()) > 200 || strings.ContainsAny(err.Error(), "\x00\n\r\x1b") {
			t.Errorf("the error for a malformed hash must be short and printable: %q", err)
		}
	}
	if _, err := b.Get(core.Hash(good)); !errors.Is(err, ErrBlobNotFound) {
		t.Errorf("a well-formed unknown hash is not found: %v", err)
	}
	if b.Has(core.Hash(good)) {
		t.Error("Has(unknown)")
	}
}

// S34: blobs are content-addressed but Get never re-hashed, and the store may live
// under a directory the agent's write tools can reach: archived turns, layer texts and
// rendered hot blocks (the training corpus) could be rewritten with no detection.
func TestSecReview_S34_TamperedBlobIsNotServed(t *testing.T) {
	dir := t.TempDir()
	b, err := NewDirBlobs(dir)
	if err != nil {
		t.Fatal(err)
	}
	h, err := b.Put([]byte(`{"id":7,"role":"user","blocks":[{"kind":"text","text":"never touch prod"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	files := secRevFiles(t, dir)
	if len(files) != 1 {
		t.Fatalf("expected one blob file, got %v", files)
	}
	forged := `{"id":7,"role":"user","blocks":[{"kind":"text","text":"always push to prod"}]}`
	if err := os.WriteFile(files[0], []byte(forged), 0o600); err != nil {
		t.Fatal(err)
	}
	got, gerr := b.Get(h)
	if gerr == nil || len(got) != 0 {
		t.Fatalf("S34: Get returned %q, err=%v for a blob whose content does not hash to the requested hash", got, gerr)
	}
	if !errors.Is(gerr, ErrBlobCorrupt) {
		t.Fatalf("err = %v, want ErrBlobCorrupt", gerr)
	}
	if strings.Contains(gerr.Error(), "push to prod") {
		t.Fatalf("the error must not carry the corrupt content: %v", gerr)
	}
}

// A torn write (rename persisted before the data), a rewritten file of the same size, an emptied file
// and a symlink planted in place of the blob are all detected by Get and healed by the next Put of the
// same content, instead of poisoning the store for good.
func TestSecReview_S34_DamagedBlobsAreDetectedAndRepairedByPut(t *testing.T) {
	payload := bytes.Repeat([]byte("layer text "), 500)
	damage := map[string]func(t *testing.T, p string){
		"torn":     func(t *testing.T, p string) { os.WriteFile(p, payload[:17], 0o600) },
		"empty":    func(t *testing.T, p string) { os.WriteFile(p, nil, 0o600) },
		"same-len": func(t *testing.T, p string) { os.WriteFile(p, bytes.Repeat([]byte("X"), len(payload)), 0o600) },
		"longer":   func(t *testing.T, p string) { os.WriteFile(p, append(append([]byte(nil), payload...), '!'), 0o600) },
		"symlink": func(t *testing.T, p string) {
			target := filepath.Join(filepath.Dir(p), "elsewhere")
			os.WriteFile(target, payload, 0o600) // even a symlink to the right bytes is not a blob
			os.Remove(p)
			if err := os.Symlink(target, p); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
		},
	}
	for name, hurt := range damage {
		t.Run(name, func(t *testing.T) {
			d, err := NewDirBlobs(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			h, err := d.Put(payload)
			if err != nil {
				t.Fatal(err)
			}
			hurt(t, d.path(h))
			if got, err := d.Get(h); err == nil {
				t.Fatalf("Get served %d bytes from a damaged blob", len(got))
			} else if !errors.Is(err, ErrBlobCorrupt) {
				t.Fatalf("err = %v, want ErrBlobCorrupt", err)
			}
			h2, err := d.Put(payload) // the same content again: repairs
			if err != nil || h2 != h {
				t.Fatalf("Put: %v %v", h2, err)
			}
			got, err := d.Get(h)
			if err != nil || !bytes.Equal(got, payload) {
				t.Fatalf("after Put the blob must be whole: %d bytes, %v", len(got), err)
			}
			if fi, err := os.Lstat(d.path(h)); err != nil || !fi.Mode().IsRegular() {
				t.Fatalf("the repaired blob must be a regular file: %v %v", fi, err)
			}
		})
	}
}

// S35: session state was created 0755/0644 (the checkpoint store uses 0700/0600). Everything this package
// creates is private to the user now, temp files included, and an older version's world-readable log is
// tightened when it is reopened.
func TestSecReview_S35_LogAndBlobPermissions(t *testing.T) {
	secRevUnixPerms(t)
	base := t.TempDir()
	dir := filepath.Join(base, "sessions", "s1")
	l, err := Open(dir, "s1")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = l.Emit("be-1", TypeToolCall, map[string]any{"name": "bash", "input": map[string]any{"command": "true"}})
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	bl, err := NewDirBlobs(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bl.Put([]byte("some blob")); err != nil {
		t.Fatal(err)
	}
	seen := 0
	err = filepath.WalkDir(filepath.Join(base, "sessions"), func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		seen++
		if fi.Mode().Perm()&0o077 != 0 {
			t.Errorf("S35: %s is %v: readable by other local users", p, fi.Mode().Perm())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen < 6 { // sessions, s1, events.jsonl, blobs, ab, cd, blob
		t.Fatalf("walked only %d entries", seen)
	}
}

func TestSecReview_S35_ExistingLooseLogIsTightenedButDirectoriesAreLeftAlone(t *testing.T) {
	secRevUnixPerms(t)
	base := t.TempDir()
	dir := filepath.Join(base, "old-session")
	if err := os.MkdirAll(filepath.Join(dir, "blobs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(evLine(1, "log.open")), 0o644); err != nil {
		t.Fatal(err)
	}
	// Whatever the umask was, leave it as an older version would have: open to everyone.
	for p, mode := range map[string]os.FileMode{dir: 0o755, filepath.Join(dir, "blobs"): 0o755, filepath.Join(dir, "events.jsonl"): 0o644} {
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
	}
	l, err := Open(dir, "s")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if _, err := NewDirBlobs(filepath.Join(dir, "blobs")); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(dir, "events.jsonl")); err != nil || fi.Mode().Perm()&0o077 != 0 {
		t.Errorf("the existing log is still %v (%v)", fi.Mode().Perm(), err)
	}
	// A directory that exists may have been chosen on purpose (--session-dir .): its mode is not ours to change.
	for _, p := range []string{dir, filepath.Join(dir, "blobs")} {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o755 {
			t.Errorf("%s was changed to %v (%v)", p, fi.Mode().Perm(), err)
		}
	}
}

// S35 (second half), decided: the log is verbatim and private, and redaction happens at export.
// The log is the source of truth for exact-prompt replay (rl/traj rebuilds every prompt from it and
// checks the wire hash against what the provider was sent); a redacted log could not reproduce that,
// so secrets are kept out of the training data by rl/redact at export time, and out of everyone
// else's reach by the permissions S35 pinned above (directories 0700, files 0600). This test fixes
// the decision: if a hook ever rewrites the text on the way into the log, exact replay breaks and
// this test says why.
func TestLogIsVerbatimAndPrivateByDesign(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions", "s1")
	l, err := Open(dir, "s1")
	if err != nil {
		t.Fatal(err)
	}
	cmd := "curl -H 'Authorization: Bearer canary-token-value' https://api.example"
	if _, err := l.Emit("be-1", TypeToolCall, map[string]any{"name": "bash", "input": map[string]any{"command": cmd}}); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "canary-token-value") {
		t.Error("the log must keep the exact text: replay and wire-hash verification depend on it (redaction is applied at export)")
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(filepath.Join(dir, "events.jsonl")); err != nil || fi.Mode().Perm()&0o077 != 0 {
			t.Errorf("a log that keeps secrets verbatim must be private, got %v (%v)", fi.Mode().Perm(), err)
		}
	}
}

// S36: Open truncated the log at the FIRST unparseable line, not just at a torn final line, so
// one damaged record silently destroyed every later event (the source of truth / training data).
func TestSecReview_S36_MidFileCorruptionDoesNotTruncateTheRestOfTheLog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	content := evLine(1, "a") + evLine(2, "b") + "\x00\x00 bit rot \x00\n" + evLine(4, "d") + evLine(5, "e") + evLine(6, "f")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := Open(dir, "s")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := l.Recovery(), (Recovery{CorruptLines: 1, FirstCorruptLine: 3}); got != want {
		t.Fatalf("Recovery = %+v, want %+v", got, want)
	}
	seq, err := l.Emit("", "g", nil)
	if err != nil {
		t.Fatal(err)
	}
	if seq != 8 { // 7 is the log.corrupt event Open recorded
		t.Fatalf("next seq = %d, want 8 (events 4-6 survived, so 7 is next)", seq)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.HasPrefix(after, []byte(content)) {
		t.Fatalf("S36: the log's existing bytes were changed by Open (%d -> %d bytes)", len(content), len(after))
	}
	seqs, err := evSeqs(t, path)
	if want := []uint64{1, 2, 4, 5, 6, 7, 8}; fmt.Sprint(seqs) != fmt.Sprint(want) {
		t.Fatalf("Scan delivered %v, want %v", seqs, want)
	}
	var ce *CorruptError
	if !errors.Is(err, ErrCorruptLog) || !errors.As(err, &ce) || ce.Lines != 1 || ce.First != 3 {
		t.Fatalf("Scan should report the skipped line after delivering the rest: %v", err)
	}
	// The damage was written down once: a resume neither forgets nor repeats it.
	l2, err := Open(dir, "s")
	if err != nil {
		t.Fatal(err)
	}
	if rec := l2.Recovery(); rec.CorruptLines != 0 {
		t.Fatalf("already-recorded damage counted again: %+v", rec)
	}
	seq, _ = l2.Emit("", "h", nil)
	l2.Close()
	if seq != 9 {
		t.Fatalf("seq after a clean resume = %d, want 9", seq)
	}
	var corruptEvents int
	err = Scan(path, func(e Event) error {
		if e.Type == TypeLogCorrupt {
			corruptEvents++
			var r Recovery
			if json.Unmarshal(e.Data, &r) != nil || r.CorruptLines != 1 || r.FirstCorruptLine != 3 {
				t.Errorf("log.corrupt payload = %s", e.Data)
			}
		}
		return nil
	})
	if !errors.Is(err, ErrCorruptLog) || corruptEvents != 1 {
		t.Fatalf("%d log.corrupt events (%v), want exactly one", corruptEvents, err)
	}
}

// Damage that turns up after an earlier one was recorded is recorded too.
func TestSecReview_S36_NewDamageAfterARecordedOneIsRecordedAgain(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(path, []byte(evLine(1, "a")+"junk\n"+evLine(3, "c")), 0o600); err != nil {
		t.Fatal(err)
	}
	l, _ := Open(dir, "s")
	l.Emit("", "x", nil)
	l.Close()
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString("more junk\nand more\n")
	f.Close()
	l, err := Open(dir, "s")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if rec := l.Recovery(); rec.CorruptLines != 2 || rec.FirstCorruptLine != 6 {
		t.Fatalf("Recovery = %+v (want the 2 new lines, first at line 6)", rec)
	}
}

// Only a torn FINAL line is ever cut: a crash leaves no newline, and either a fragment (truncated) or a
// whole event that only lost its newline (kept). A garbage line that did get its newline is damage, not
// a crash tail, so it stays.
func TestSecReview_S36_OnlyATornFinalLineIsTruncated(t *testing.T) {
	cases := []struct {
		name      string
		content   string
		wantSeq   uint64 // seq of the next Emit, not counting the log.corrupt event Open records for damage
		wantKeeps string // substring that must still be in the file
		wantCut   string // substring that must be gone
		wantRec   Recovery
	}{
		{"fragment", evLine(1, "a") + evLine(2, "b") + `{"seq":3,"ty`, 3, evLine(2, "b"), `{"seq":3,"ty`, Recovery{TornBytes: 12}},
		{"nul-tail", evLine(1, "a") + strings.Repeat("\x00", 4096), 2, evLine(1, "a"), "\x00", Recovery{TornBytes: 4096}},
		{"whole-event-missing-newline", evLine(1, "a") + strings.TrimSuffix(evLine(2, "b"), "\n"), 3, evLine(2, "b"), "", Recovery{}},
		{"garbage-with-newline-last", evLine(1, "a") + evLine(2, "b") + "garbage\n", 3, "garbage\n", "", Recovery{CorruptLines: 1, FirstCorruptLine: 3}},
		{"garbage-then-fragment", evLine(1, "a") + "garbage\n" + `{"seq":3,`, 2, "garbage\n", "", Recovery{CorruptLines: 1, FirstCorruptLine: 2, TornBytes: 9}},
		{"not-a-log-at-all", "this is not\na log\n", 2, "this is not\na log\n", "", Recovery{CorruptLines: 2, FirstCorruptLine: 1}}, // log.open is seq 1; nothing is destroyed
		{"empty-lines-are-damage-not-events", evLine(1, "a") + "\n\n" + evLine(2, "b"), 3, "\n\n", "", Recovery{CorruptLines: 2, FirstCorruptLine: 2}},
		{"seq-zero-and-huge", evLine(1, "a") + evLine(0, "z") + `{"seq":18446744073709551615,"type":"x"}` + "\n" + `{"seq":9007199254740993}` + "\n", 2, "18446744073709551615", "", Recovery{CorruptLines: 3, FirstCorruptLine: 2}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "events.jsonl")
			if err := os.WriteFile(path, []byte(c.content), 0o600); err != nil {
				t.Fatal(err)
			}
			l, err := Open(dir, "s")
			if err != nil {
				t.Fatal(err)
			}
			if got := l.Recovery(); got != c.wantRec {
				t.Fatalf("Recovery = %+v, want %+v", got, c.wantRec)
			}
			seq, err := l.Emit("", "next", nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := l.Close(); err != nil {
				t.Fatal(err)
			}
			raw, _ := os.ReadFile(path)
			// Emit's own seq is one past the recorded log.corrupt event, when there was damage.
			if c.wantRec.CorruptLines > 0 {
				c.wantSeq++
			}
			if seq != c.wantSeq {
				t.Fatalf("next seq = %d, want %d", seq, c.wantSeq)
			}
			if !strings.Contains(string(raw), c.wantKeeps) {
				t.Errorf("the log lost %q:\n%q", c.wantKeeps, raw)
			}
			if c.wantCut != "" && strings.Contains(string(raw), c.wantCut) {
				t.Errorf("the torn tail %q should have been cut:\n%q", c.wantCut, raw)
			}
			if !bytes.HasSuffix(raw, []byte("\n")) {
				t.Errorf("every line must end with a newline: %q", raw[max(0, len(raw)-40):])
			}
			// Nothing appended ever fuses with what was there.
			for i, line := range bytes.Split(bytes.TrimSuffix(raw, []byte("\n")), []byte("\n")) {
				if bytes.Contains(line, []byte(`}{"seq"`)) {
					t.Errorf("line %d fused two records: %q", i+1, line)
				}
			}
		})
	}
}

// A read error is not the end of the file: cutting the log where a transient failure
// happened would destroy every event after it.
func TestSecReview_S36_ReadErrorsAreNotTreatedAsEndOfFile(t *testing.T) {
	content := evLine(1, "a") + evLine(2, "b") + evLine(3, "c")
	boom := errors.New("input/output error")
	r := io.MultiReader(strings.NewReader(content[:len(evLine(1, "a"))+5]), iotest.ErrReader(boom))
	sc, err := scanLog(r)
	if !errors.Is(err, boom) {
		t.Fatalf("scanLog err = %v (%+v), want the read error", err, sc)
	}
	// Through Open: the file is left exactly as it was.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "events.jsonl"), 0o700); err != nil { // reading a directory fails
		t.Fatal(err)
	}
	if _, err := Open(dir, "s"); err == nil {
		t.Fatal("Open must not carry on over an unreadable log")
	}
}

// Log lines, on disk or emitted, are bounded: a huge line is skipped without being buffered
// (memory stays bounded however long it is) and Emit never writes a line the readers would refuse.
func TestSecReview_S36_LineSizeIsBounded(t *testing.T) {
	old := maxLineBytes
	maxLineBytes = 4096
	t.Cleanup(func() { maxLineBytes = old })

	huge := `{"seq":3,"type":"x","data":"` + strings.Repeat("A", 5<<20) + `"}` + "\n" // 5 MiB, no reader may hold it
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	content := evLine(1, "a") + evLine(2, "b") + huge + evLine(4, "d") + evLine(5, "e") + strings.Repeat("Z", 3<<20) // ...and an unterminated giant
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	seqs, err := evSeqs(t, path)
	if want := []uint64{1, 2, 4, 5}; fmt.Sprint(seqs) != fmt.Sprint(want) {
		t.Fatalf("Scan delivered %v, want %v", seqs, want)
	}
	var ce *CorruptError
	if !errors.As(err, &ce) || ce.Lines != 1 || ce.First != 3 {
		t.Fatalf("Scan err = %v", err)
	}
	l, err := Open(dir, "s")
	if err != nil {
		t.Fatal(err)
	}
	if rec := l.Recovery(); rec.CorruptLines != 1 || rec.FirstCorruptLine != 3 || rec.TornBytes != 3<<20 {
		t.Fatalf("Recovery = %+v", rec)
	}
	if _, err := l.Emit("", "big", strings.Repeat("B", 8192)); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("an event over the limit must be refused, got %v", err)
	}
	if seq, err := l.Emit("", "small", "ok"); err != nil || seq != 7 { // 6 is log.corrupt; the refused one took no number
		t.Fatalf("Emit after a refusal: %d %v", seq, err)
	}
	l.Close()

	// The reader itself never keeps more than the limit.
	lr := newLineReader(strings.NewReader(huge+evLine(9, "z")), 4096)
	line, n, complete, tooLong, err := lr.next()
	if err != nil || !tooLong || !complete || line != nil || n != len(huge) {
		t.Fatalf("next() = %d bytes, complete=%v tooLong=%v, %v", n, complete, tooLong, err)
	}
	if cap(lr.buf) > 8192 {
		t.Fatalf("the reader buffered %d bytes of a line it was going to skip", cap(lr.buf))
	}
	if line, _, _, tooLong, err = lr.next(); err != nil || tooLong || string(line) != evLine(9, "z") {
		t.Fatalf("the line after the giant one must read normally: %q %v", line, err)
	}
}

// A sequence number near the top of the range must not be resumed from: counting on from it would wrap
// to 0 and every later event would be unreadable.
func TestSecReview_S36_HugeSequenceNumbersCannotWrapTheCounter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(path, []byte(evLine(1, "a")+`{"seq":18446744073709551615,"type":"x"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := Open(dir, "s")
	if err != nil {
		t.Fatal(err)
	}
	seq, _ := l.Emit("", "y", nil)
	l.Close()
	if seq == 0 || seq > 1<<20 {
		t.Fatalf("seq = %d", seq)
	}
}

// ---- sound behaviour ----------------------------------------------------------------

// A torn final line (crash mid-write) is repaired and sequence numbers continue.
func TestSecSound_TornTailIsTruncatedAndSeqContinues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(path, []byte(`{"seq":1,"type":"a"}`+"\n"+`{"seq":2,"type":"b"}`+"\n"+`{"seq":3,"ty`), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := Open(dir, "s")
	if err != nil {
		t.Fatal(err)
	}
	seq, _ := l.Emit("", "c", nil)
	l.Close()
	if seq != 3 {
		t.Fatalf("next seq = %d, want 3", seq)
	}
}

// Blob writes are atomic and content-addressed; concurrent identical Puts are safe.
func TestSecSound_BlobPutIsIdempotentAndFilesArePrivate(t *testing.T) {
	secRevUnixPerms(t)
	b, err := NewDirBlobs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h1, _ := b.Put([]byte("x"))
	h2, _ := b.Put([]byte("x"))
	if h1 != h2 {
		t.Fatal("not content addressed")
	}
	for _, f := range secRevFiles(t, dirOf(b)) {
		fi, err := os.Stat(f)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm()&0o077 != 0 {
			t.Fatalf("blob file %s mode %v", f, fi.Mode().Perm())
		}
	}
}

// Many goroutines Putting and Getting the same and different blobs, with the store being
// damaged underneath them, never see a wrong byte: every Get returns the exact content or an error.
func TestSecSound_ConcurrentPutGetNeverServesWrongBytes(t *testing.T) {
	d, err := NewDirBlobs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	payloads := make([][]byte, 8)
	hashes := make([]core.Hash, 8)
	for i := range payloads {
		payloads[i] = bytes.Repeat([]byte(fmt.Sprintf("payload %d ", i)), 200)
		hashes[i] = core.HashBytes(payloads[i])
	}
	done := make(chan struct{})
	errc := make(chan error, 16)
	for g := 0; g < 8; g++ {
		go func(g int) {
			for i := 0; i < 200; i++ {
				k := (g + i) % len(payloads)
				if _, err := d.Put(payloads[k]); err != nil {
					errc <- err
					return
				}
				if got, err := d.Get(hashes[k]); err == nil && !bytes.Equal(got, payloads[k]) {
					errc <- fmt.Errorf("Get(%s) served wrong bytes", hashes[k].Short())
					return
				}
			}
			errc <- nil
		}(g)
	}
	vandalGone := make(chan struct{})
	go func() { // a vandal
		defer close(vandalGone)
		for i := 0; ; i++ {
			select {
			case <-done:
				return
			default:
			}
			k := i % len(payloads)
			os.WriteFile(d.path(hashes[k]), []byte("vandalised"), 0o600)
		}
	}()
	var first error
	for g := 0; g < 8; g++ {
		if err := <-errc; err != nil && first == nil {
			first = err
		}
	}
	close(done)
	<-vandalGone // so the temp dir is not being written to while it is removed
	if first != nil {
		t.Fatal(first)
	}
}

// Callers that must bound their memory can ask for a blob with a size limit; an oversized one is refused
// before it is read, and the limit is exact.
func TestSecSound_GetMaxRefusesOversizedBlobsBeforeReadingThem(t *testing.T) {
	d, err := NewDirBlobs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := NewMemBlobs()
	payload := bytes.Repeat([]byte("x"), 1000)
	for name, store := range map[string]interface {
		Blobs
		GetMax(core.Hash, int64) ([]byte, error)
	}{"dir": d, "mem": m} {
		h, err := store.Put(payload)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := store.GetMax(h, 1000); err != nil || !bytes.Equal(got, payload) {
			t.Fatalf("%s: a blob exactly at the limit must be served: %d bytes, %v", name, len(got), err)
		}
		if got, err := store.GetMax(h, 999); !errors.Is(err, ErrBlobTooLarge) || got != nil {
			t.Fatalf("%s: one byte over must be refused: %d bytes, %v", name, len(got), err)
		}
		if got, err := store.GetMax(h, 0); !errors.Is(err, ErrBlobTooLarge) || got != nil {
			t.Fatalf("%s: %d bytes, %v", name, len(got), err)
		}
		if got, err := store.Get(h); err != nil || !bytes.Equal(got, payload) {
			t.Fatalf("%s: plain Get must still serve any size: %d bytes, %v", name, len(got), err)
		}
	}
	if _, err := d.GetMax(core.Hash("../x"), 10); !errors.Is(err, ErrInvalidHash) {
		t.Fatalf("GetMax must vet the hash too: %v", err)
	}
	// Empty content is a blob like any other (and not "corrupt").
	h, _ := d.Put(nil)
	if got, err := d.Get(h); err != nil || len(got) != 0 {
		t.Fatalf("empty blob: %q %v", got, err)
	}
}

func TestSecSound_ValidHashIsExactlyLowercaseSHA256Hex(t *testing.T) {
	good := core.HashString("x")
	if !ValidHash(good) {
		t.Fatal("core.HashString output must be valid")
	}
	for _, bad := range []core.Hash{"", good[:63], good + "0", core.Hash(strings.ToUpper(string(good))), "../" + good[3:], good[:63] + "g"} {
		if ValidHash(bad) {
			t.Fatalf("%q must not be valid", bad)
		}
	}
}

func dirOf(b *DirBlobs) string { return b.dir }
