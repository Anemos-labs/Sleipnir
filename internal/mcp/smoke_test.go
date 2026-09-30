package mcp

import (
	"strings"
	"testing"
)

func TestSmokeStdio(t *testing.T) {
	m := startManager(t, quickOpts(map[string]ServerConfig{"ref": helperCfg("", nil)}))
	snap := m.Snapshot()
	t.Logf("tools: %v warnings: %v", snap.Names(), snap.Warnings())
	if snap.Len() == 0 {
		t.Fatal("no tools")
	}
	env, _ := testEnv(newRecorder(true))
	res := runTool(t, findTool(t, snap.Tools(), "__echo"), env, `{"message":"hi"}`)
	if res.IsError || !strings.Contains(res.Text, "hi") {
		t.Fatalf("got %+v", res)
	}
}

func TestSmokeHTTP(t *testing.T) {
	ts, _ := httpServer(t, newRef(), mcpHTTPOpts())
	m := startManager(t, quickOpts(map[string]ServerConfig{"web": httpCfg(ts.URL)}))
	snap := m.Snapshot()
	env, _ := testEnv(newRecorder(true))
	res := runTool(t, findTool(t, snap.Tools(), "__echo"), env, `{"message":"over http"}`)
	if res.IsError || !strings.Contains(res.Text, "over http") {
		t.Fatalf("got %+v", res)
	}
}
