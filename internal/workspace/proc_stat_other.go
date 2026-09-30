//go:build unix && !linux && !darwin

package workspace

// procStat: no /proc and no sysctl reader on this platform. Ownership of a tree is
// then judged by whether the pid exists at all, which never prunes a live owner's
// tree (it can keep an abandoned one until the pid goes away).
func procStat(pid int) (procInfo, bool) { return procInfo{}, false }
