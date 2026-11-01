package machine

import (
	"bufio"
	"bytes"
	"os"
	"strconv"
	"strings"
)

// totalMemory is the memory the harness's commands can have: the machine's, or its cgroup's limit when that is smaller.
func totalMemory() (int64, bool) {
	total, ok := meminfo("MemTotal:")
	if !ok {
		return 0, false
	}
	if limit, ok := cgroupValue("memory.max"); ok && limit > 0 && limit < total {
		total = limit
	}
	return total, true
}

// availableMemory is the memory that can be had now without swapping (MemAvailable), or what the cgroup's limit leaves when
// that is less.
func availableMemory() (int64, bool) {
	avail, ok := meminfo("MemAvailable:")
	if !ok {
		return 0, false
	}
	if limit, ok := cgroupValue("memory.max"); ok && limit > 0 {
		if used, ok := cgroupValue("memory.current"); ok && limit-used < avail {
			avail = max(0, limit-used)
		}
	}
	return avail, true
}

// meminfo reads one field of /proc/meminfo, in bytes.
func meminfo(field string) (int64, bool) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, false
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, field) {
			continue
		}
		f := strings.Fields(line[len(field):])
		if len(f) == 0 {
			return 0, false
		}
		kb, err := strconv.ParseInt(f[0], 10, 64)
		if err != nil {
			return 0, false
		}
		return kb << 10, true
	}
	return 0, false
}

// cgroupValue reads a number from a file of the harness's own cgroup (v2). ok is false for "max", for cgroup v1 and where
// there is no such file.
func cgroupValue(name string) (int64, bool) {
	b, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return 0, false
	}
	path := ""
	for _, line := range strings.Split(string(b), "\n") {
		if p, ok := strings.CutPrefix(line, "0::"); ok {
			path = p
			break
		}
	}
	if path == "" {
		return 0, false
	}
	v, err := os.ReadFile("/sys/fs/cgroup" + path + "/" + name)
	if err != nil {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(v)), 10, 64)
	return n, err == nil
}

// GroupRSS is the resident memory, in bytes, of the live members of each process group of pgids, from one look at /proc.
// A group with no live member is absent.
func GroupRSS(pgids []int) map[int]int64 {
	want := make(map[int]bool, len(pgids))
	for _, g := range pgids {
		want[g] = true
	}
	out := map[int]int64{}
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return out
	}
	page := int64(os.Getpagesize())
	for _, e := range ents {
		name := e.Name()
		if name == "" || name[0] < '0' || name[0] > '9' {
			continue
		}
		b, err := os.ReadFile("/proc/" + name + "/stat")
		if err != nil {
			continue // exited while we were looking
		}
		s := string(b)
		i := strings.LastIndexByte(s, ')') // the command name may contain anything
		if i < 0 || i+2 >= len(s) {
			continue
		}
		f := strings.Fields(s[i+2:]) // field 3 (state) onwards: f[k] is field k+3
		if len(f) < 22 || f[0] == "Z" || f[0] == "X" {
			continue
		}
		g, err := strconv.Atoi(f[2]) // field 5, pgrp
		if err != nil || !want[g] {
			continue
		}
		rss, err := strconv.ParseInt(f[21], 10, 64) // field 24, rss in pages
		if err != nil {
			continue
		}
		out[g] += rss * page
	}
	return out
}
