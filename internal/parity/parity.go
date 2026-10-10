// Package parity keeps the terminal interface (the commands, the chat and its program) and the web interface equal in what a
// person can do with them.
//
// Every dimension that can be listed (the commands, the slash commands, the views, the events that are shown, the keys) is listed
// from the code of each interface by a test, and the two lists are compared with Differences.Check. An item that one interface
// has and the other lacks fails the test until it is built on the other side or entered in the dimension's file under contract/
// with the reason it is meant to differ. A known difference that is to be closed goes under gaps; it is reported on every run and
// fails the test as soon as it is closed (the entry is then removed) or when a new one appears, so the set of differences can
// only shrink on purpose.
//
// What the lists cannot say, whether a fix in one interface behaves as in the other, is held by tests that run the same
// recordings through both (see docs/PARITY.md).
package parity

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Entry names an item that differs on purpose, or is known to differ, and says why.
type Entry struct {
	// Item is the item as the dimension's lists spell it.
	Item string `json:"item"`
	// Reason says why the interfaces differ in it: what the other one offers instead, or what must happen to close the gap.
	Reason string `json:"reason"`
}

// Differences is the contract of one dimension: the items that differ.
type Differences struct {
	// TerminalOnly are items of the terminal interface that the web interface does not mirror, on purpose.
	TerminalOnly []Entry `json:"terminal_only,omitempty"`
	// WebOnly are items of the web interface that the terminal interface does not mirror, on purpose.
	WebOnly []Entry `json:"web_only,omitempty"`
	// Gaps are items that one interface lacks and is to be given: known drift, reported on every run.
	Gaps []Entry `json:"gaps,omitempty"`
}

// minReason is the least length of a reason: a word is not an explanation.
const minReason = 15

// Dir is the directory of the contract files, whatever the directory of the test that asks.
func Dir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "contract")
}

// Load reads the contract file of a dimension (contract/<name>.json) into v.
func Load(name string, v any) error { return loadFile(filepath.Join(Dir(), name+".json"), v) }

// loadFile reads a JSON file into v, refusing a field that v does not have: a misspelt key must not silently mean nothing.
func loadFile(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return nil
}

// Check compares what the terminal interface has with what the web interface has, in one dimension, and returns every
// problem found, in a stable order. An item on one side only is a problem unless the contract lists it; a listed item that no
// longer differs is a problem too, so that the contract never keeps an exception that is gone.
func (d Differences) Check(dimension string, terminal, web []string) []string {
	t, w := set(terminal), set(web)
	var out []string
	add := func(format string, args ...any) { out = append(out, dimension+": "+fmt.Sprintf(format, args...)) }

	listed := map[string]string{}
	note := func(list string, es []Entry, applies func(item string) bool, why string) {
		for _, e := range es {
			switch {
			case e.Item == "":
				add("an entry under %s has no item", list)
			case len(strings.TrimSpace(e.Reason)) < minReason:
				add("%s under %s needs a reason (at least %d characters): say why the interfaces differ in it", quote(e.Item), list, minReason)
			}
			if prev, dup := listed[e.Item]; dup && e.Item != "" {
				add("%s is listed under %s and under %s", quote(e.Item), prev, list)
			}
			listed[e.Item] = list
			if e.Item != "" && !applies(e.Item) {
				add("%s under %s is stale: %s; remove it", quote(e.Item), list, why)
			}
		}
	}
	note("terminal_only", d.TerminalOnly, func(i string) bool { return t[i] && !w[i] }, "the terminal interface no longer has it alone")
	note("web_only", d.WebOnly, func(i string) bool { return w[i] && !t[i] }, "the web interface no longer has it alone")
	note("gaps", d.Gaps, func(i string) bool { return t[i] != w[i] }, "both interfaces have it, or neither does, so the gap is closed")

	for _, item := range sorted(t) {
		if !w[item] && listed[item] == "" {
			add("%s exists in the terminal interface and not in the web interface: build it there, or list it under terminal_only in contract/%s.json with the reason it is not mirrored (a known difference that is to be closed goes under gaps)", quote(item), dimension)
		}
	}
	for _, item := range sorted(w) {
		if !t[item] && listed[item] == "" {
			add("%s exists in the web interface and not in the terminal interface: build it there, or list it under web_only in contract/%s.json with the reason it is not mirrored (a known difference that is to be closed goes under gaps)", quote(item), dimension)
		}
	}
	return out
}

// GapLines says each known gap, for a test to log: the open differences are visible on every run.
func (d Differences) GapLines(dimension string) []string {
	var out []string
	for _, g := range d.Gaps {
		out = append(out, fmt.Sprintf("%s: gap %s: %s", dimension, quote(g.Item), g.Reason))
	}
	return out
}

// set is the members of a list.
func set(items []string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, i := range items {
		m[i] = true
	}
	return m
}

// sorted is the members of a set, in order.
func sorted(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// quote spells an item in a message.
func quote(item string) string { return fmt.Sprintf("%q", item) }
