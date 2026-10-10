package main

import (
	"reflect"
	"testing"
)

// Between the close of one generation and the start of the next, no lock holds the tab's session: the host still reports it hosted
// (and the one the start resumes), so that a delete or a prune of the page leaves it alone.
func TestARestartingTabStillHostsItsSessions(t *testing.T) {
	const old, next = "20261009-221530-a91c3e", "20261010-080000-bbbbbb"
	tab := &webTab{id: "shop", restarting: true, prevSID: old}
	h := &webHostImpl{tabs: []*webTab{tab}}
	if got := h.HostedSessions(); !reflect.DeepEqual(got, map[string]string{old: "shop"}) {
		t.Fatalf("restarting: %v", got)
	}
	tab.restarting, tab.starting, tab.args = false, true, []string{"--resume", next}
	if got := h.HostedSessions(); !reflect.DeepEqual(got, map[string]string{old: "shop", next: "shop"}) {
		t.Fatalf("starting a resume: %v", got)
	}
	tab.starting = false
	if got := h.HostedSessions(); len(got) != 0 {
		t.Fatalf("a tab whose start failed holds nothing: %v", got)
	}
}
