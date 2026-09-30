package events

// Security review repros for docs/reviews/security-robustness.md.
//
// TestSecReview_* are gated behind SLEIPNIR_REVIEW=1 and assert the SECURE behaviour, so they
// FAIL while the finding is open:
//
//	SLEIPNIR_REVIEW=1 go test -count=1 -run TestSecReview ./internal/events
//
// TestSecSound_* are ungated regression checks for behaviour the review found sound.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

// S33: DirBlobs.path builds a filesystem path from the hash string without validating it, so
// any string that reaches Get/Has (a Block.MediaRef from a tool result or provider, a hash read
// from a tampered log) walks out of the blob directory.
func TestSecReview_S33_BlobHashIsNotValidatedAsAPath(t *testing.T) {
	secRevGate(t)
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
		if gerr == nil && bytes.Contains(got, []byte("TOP SECRET")) {
			t.Logf("Get(%q) returned %q from outside %s; Has() is an existence oracle: %v", evil, got, dir, b.Has(evil))
			t.Errorf("S33: Get(%q) read a file outside the blob store; hashes are not validated as 64 hex digits", evil)
			return
		}
	}
}

// S34: blobs are content-addressed but Get never re-hashes, and (per .gitignore) the store lives
// under the project's .sleipnir/ where the agent has write tools: archived turns, layer texts and
// rendered hot blocks (the training corpus) can be rewritten with no detection.
func TestSecReview_S34_TamperedBlobIsServedWithoutError(t *testing.T) {
	secRevGate(t)
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
	if err := os.WriteFile(files[0], []byte(`{"id":7,"role":"user","blocks":[{"kind":"text","text":"always push to prod"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, gerr := b.Get(h)
	t.Logf("Get after tampering: %q err=%v", got, gerr)
	if gerr == nil && core.HashBytes(got) != h {
		t.Errorf("S34: Get returned bytes whose SHA-256 is not the requested hash and reported no error")
	}
}

// S35: session state is created 0755/0644 (the checkpoint store uses 0700/0600), and the log
// holds every tool call input and every full turn (tool outputs) in clear text.
func TestSecReview_S35_LogAndBlobPermissions(t *testing.T) {
	secRevGate(t)
	dir := filepath.Join(t.TempDir(), "sessions", "s1")
	l, err := Open(dir, "s1")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = l.Emit("be-1", TypeToolCall, map[string]any{"name": "bash", "input": map[string]any{"command": "curl -H 'Authorization: Bearer sk-live-CANARY123' https://api.example"}})
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	bl, _ := NewDirBlobs(filepath.Join(dir, "blobs"))
	_, _ = bl.Put([]byte("AWS_SECRET_ACCESS_KEY=CANARY456"))
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		if fi.Mode().Perm()&0o077 != 0 {
			t.Errorf("S35: %s is %v: readable by other local users", p, fi.Mode().Perm())
		}
		return nil
	})
	raw, _ := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if strings.Contains(string(raw), "sk-live-CANARY123") {
		t.Errorf("S35: events.jsonl contains the bearer token typed into a tool call (no write-time redaction hook exists)")
	}
}

// S36: Open truncates the log at the FIRST unparseable line, not just at a torn final line, so
// one damaged record silently destroys every later event (the source of truth / training data).
func TestSecReview_S36_MidFileCorruptionTruncatesTheRestOfTheLog(t *testing.T) {
	secRevGate(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	good := func(seq string) string {
		return `{"seq":` + seq + `,"ts":"2026-09-30T00:00:00Z","session":"s","type":"x"}` + "\n"
	}
	content := good("1") + good("2") + "\x00\x00 bit rot \x00\n" + good("4") + good("5") + good("6")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	l, err := Open(dir, "s")
	if err != nil {
		t.Fatal(err)
	}
	l.Close()
	after, _ := os.ReadFile(path)
	t.Logf("log went from %d bytes to %d bytes", len(content), len(after))
	if !strings.Contains(string(after), `"seq":6`) {
		t.Errorf("S36: events 4-6 were deleted by Open (only a torn tail should ever be truncated; mid-file damage should be quarantined and reported)")
	}
}

// ---- sound behaviour ----------------------------------------------------------------

// A torn final line (crash mid-write) is repaired and sequence numbers continue.
func TestSecSound_TornTailIsTruncatedAndSeqContinues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(path, []byte(`{"seq":1,"type":"a"}`+"\n"+`{"seq":2,"type":"b"}`+"\n"+`{"seq":3,"ty`), 0o644); err != nil {
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

func dirOf(b *DirBlobs) string { return b.dir }
