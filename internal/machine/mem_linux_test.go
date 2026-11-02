package machine

import (
	"syscall"
	"testing"
)

func TestGroupRSSFindsALiveGroup(t *testing.T) {
	pg := syscall.Getpgrp()
	if got := GroupRSS([]int{pg}); got[pg] <= 0 {
		t.Errorf("the test's own group has no memory: %v", got)
	}
	if got := GroupRSS([]int{1 << 30}); len(got) != 0 {
		t.Errorf("a group that does not exist: %v", got)
	}
}
