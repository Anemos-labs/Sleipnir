package perm

import (
	"runtime"
	"testing"
)

// ps -e selects every process on Linux and adds each process's environment on macOS and the BSDs; a format and process ids are
// values everywhere, so "etime" in -o is not the BSD "e".
func TestPsEnvironmentFormsDependOnTheSystem(t *testing.T) {
	for _, args := range [][]string{{"aux"}, {"-o", "pid,etime,cmd", "-p", "1"}, {"-O", "etime"}} {
		if sa := analysePs(args); !sa.ok {
			t.Errorf("ps %v refused: %s", args, sa.why)
		}
	}
	for _, args := range [][]string{{"auxe"}, {"eww"}, {"-E"}} {
		if sa := analysePs(args); sa.ok {
			t.Errorf("ps %v allowed", args)
		}
	}
	if sa := analysePs([]string{"-ef"}); sa.ok != (runtime.GOOS == "linux") {
		t.Errorf("ps -ef on %s: allowed %v (%s)", runtime.GOOS, sa.ok, sa.why)
	}
}
