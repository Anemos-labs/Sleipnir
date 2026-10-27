package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectRefusesPlainHTTPOffLoopback(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	err := cmdInspect(context.Background(), []string{"--addr", "0.0.0.0:0", "--token", "t0ken-t0ken-t0ken", dir})
	if err == nil || !strings.Contains(err.Error(), "plain HTTP") {
		t.Fatalf("a non-loopback bind without TLS: %v", err)
	}
	err = cmdInspect(context.Background(), []string{"--tls-cert", "c.pem", dir})
	if err == nil || !strings.Contains(err.Error(), "go together") {
		t.Fatalf("--tls-cert without --tls-key: %v", err)
	}
}

func TestInspectURLNamesTheScheme(t *testing.T) {
	a := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 8787}
	if u := inspectURL(a, "", true); u != "https://127.0.0.1:8787/" {
		t.Errorf("https URL = %s", u)
	}
	if u := inspectURL(a, "", false); u != "http://127.0.0.1:8787/" {
		t.Errorf("http URL = %s", u)
	}
}
