//go:build linux

package workspace

import (
	"os"
	"strconv"
	"strings"
)

// procStat reads /proc/<pid>/stat.
func procStat(pid int) (procInfo, bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return procInfo{}, false
	}
	s := string(b)
	// The command name is parenthesised and may contain spaces and parentheses:
	// the fields we want come after the last ')'.
	i := strings.LastIndexByte(s, ')')
	if i < 0 || i+2 >= len(s) {
		return procInfo{}, false
	}
	f := strings.Fields(s[i+2:])
	// f[0] is state (field 3 overall); starttime is field 22 overall -> f[19].
	if len(f) < 20 || len(f[0]) == 0 {
		return procInfo{}, false
	}
	return procInfo{state: f[0][0], start: atoi64(f[19])}, true
}
