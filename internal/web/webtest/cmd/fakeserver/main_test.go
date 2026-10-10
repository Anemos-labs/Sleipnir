package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/testutil"
)

// The command starts a server and its player; a test that leaves a goroutine behind fails the package.
func TestMain(m *testing.M) { os.Exit(testutil.CheckLeaks(m)) }

// syncBuf is a buffer a goroutine writes while a test reads it.
type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// newSyncBuf returns an empty buffer.
func newSyncBuf() *syncBuf { return &syncBuf{} }

// Write appends to the buffer.
func (b *syncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// String returns what was written.
func (b *syncBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestTheCommandPrintsTheAddressFirstServesAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out, errb := newSyncBuf(), newSyncBuf()
	done := make(chan error, 1)
	go func() { done <- run(ctx, []string{"-addr", "127.0.0.1:0", "-speed", "0"}, out, errb) }()
	var line string
	deadline := time.Now().Add(20 * time.Second)
	for line == "" && time.Now().Before(deadline) {
		if i := strings.Index(out.String(), "\n"); i >= 0 {
			line = out.String()[:i]
		}
		time.Sleep(5 * time.Millisecond)
	}
	m := regexp.MustCompile(`^http://127\.0\.0\.1:([1-9]\d*)/\?token=([A-Za-z0-9_-]{43})$`).FindStringSubmatch(line)
	if m == nil {
		t.Fatalf("first line of stdout = %q\nstderr: %s", line, errb.String())
	}
	req, _ := http.NewRequest("GET", "http://127.0.0.1:"+m[1]+"/api/hello", nil)
	req.Header.Set("Authorization", "Bearer "+m[2])
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"boot":"fakeboot"`) {
		t.Errorf("hello = %d %s", resp.StatusCode, body)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("run = %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the command did not stop")
	}
	if strings.Count(out.String(), "\n") != 1 || !strings.Contains(errb.String(), "/api/_fake/") || strings.Contains(errb.String(), m[2]) {
		t.Errorf("stdout %q stderr %q: the address is the only stdout line and the token is on no other stream", out.String(), errb.String())
	}
}

func TestTheCommandServesADirectoryAsTheUI(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html><title>from disk</title>"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	out := newSyncBuf()
	done := make(chan error, 1)
	go func() { done <- run(ctx, []string{"-ui", dir, "-speed", "0"}, out, io.Discard) }()
	var line string
	for deadline := time.Now().Add(20 * time.Second); line == "" && time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if i := strings.Index(out.String(), "\n"); i >= 0 {
			line = out.String()[:i]
		}
	}
	jar, _ := cookiejar.New(nil)
	resp, err := (&http.Client{Jar: jar}).Get(line) // the printed address signs the client in
	if err != nil {
		cancel()
		<-done
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	cancel()
	<-done
	if !strings.Contains(string(body), "from disk") {
		t.Errorf("served %q", body)
	}
}

func TestTheCommandRefusesWhatItShould(t *testing.T) {
	file := filepath.Join(t.TempDir(), "f")
	_ = os.WriteFile(file, nil, 0o644)
	for name, args := range map[string][]string{
		"an argument":       {"extra"},
		"an unknown flag":   {"-nope"},
		"a public address":  {"-addr", "0.0.0.0:0"},
		"no such directory": {"-ui", filepath.Join(t.TempDir(), "missing")},
		"a file as the UI":  {"-ui", file},
	} {
		if err := run(context.Background(), args, io.Discard, io.Discard); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	var usage bytes.Buffer
	if err := run(context.Background(), []string{"-h"}, io.Discard, &usage); err == nil || !strings.Contains(usage.String(), "-speed") || !strings.Contains(usage.String(), "-ui") {
		t.Errorf("-h: %v\n%s", err, usage.String())
	}
}
