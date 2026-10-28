package main

import (
	"context"
	"strings"
	"testing"
)

func TestScanDiffFlagIsChecked(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--cwd", dir, "--start", "--diff", ""}, "--diff needs a BASE"},
		{[]string{"--cwd", dir, "--diff", ""}, "--diff goes with --start"},
		{[]string{"--cwd", dir, "--start", "--diff", "main", "sub"}, "leave DIR out"},
	} {
		if err := cmdScan(context.Background(), c.args); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("scan %q: %v, want %q", c.args, err, c.want)
		}
	}
}
