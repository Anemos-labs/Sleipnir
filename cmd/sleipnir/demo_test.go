package main

import (
	"context"
	"testing"
)

func TestDemoCommandRunsWithoutAKey(t *testing.T) {
	for _, k := range []string{"HEIMDALL_API_KEY", "OPENROUTER_API_KEY", "OPENAI_API_KEY"} {
		t.Setenv(k, "")
	}
	if err := cmdDemo(context.Background(), []string{"--topics", "4", "--dir", t.TempDir()}); err != nil {
		t.Fatal(err)
	}
}
