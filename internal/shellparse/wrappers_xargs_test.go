package shellparse

import "testing"

// xargs running a shell through a wrapper turns its input into code as surely as xargs sh does.
func TestXargsRunsShellThroughWrappers(t *testing.T) {
	for _, c := range []struct {
		args []string
		want bool
	}{
		{[]string{"sh", "-c", "x"}, true},
		{[]string{"env", "sh", "-c", "x"}, true},
		{[]string{"env", "-i", "FOO=1", "bash"}, true},
		{[]string{"-0", "nice", "-n", "5", "eval"}, true},
		{[]string{"--", "timeout", "5", "env", "sh"}, true},
		{[]string{"sudo", "-u", "x", "sh"}, true},
		{[]string{"env", "grep", "x"}, false},
		{[]string{"grep", "sh"}, false},
		{[]string{"-n", "1", "echo"}, false},
	} {
		if got := xargsRunsShell(c.args); got != c.want {
			t.Errorf("xargs %v: runs a shell = %v, want %v", c.args, got, c.want)
		}
	}
}
