package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// An export's output file that was there is made private before it is emptied, and a device such as /dev/null is written as is.
func TestOpenOutputModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes")
	}
	p := filepath.Join(t.TempDir(), "out.jsonl")
	if err := os.WriteFile(p, []byte("old contents\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w, done, err := openOutput(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("new\n")); err != nil {
		t.Fatal(err)
	}
	if err := done(true); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "new\n" {
		t.Errorf("output = %q, want only what was written", b)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
	before, err := os.Stat(os.DevNull)
	if err != nil {
		t.Skip(err)
	}
	w, done, err = openOutput(os.DevNull, nil)
	if err != nil {
		t.Fatalf("writing to %s: %v", os.DevNull, err)
	}
	w.Write([]byte("x"))
	done(false) // a failed export removes its partial file, never a device
	if _, err := os.Stat(os.DevNull); err != nil {
		t.Fatalf("%s is gone: %v", os.DevNull, err)
	}
	if after, _ := os.Stat(os.DevNull); after.Mode() != before.Mode() {
		t.Errorf("%s changed mode: %v, was %v", os.DevNull, after.Mode(), before.Mode())
	}
}
