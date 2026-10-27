package config

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// Origin keys do not collide: a key that holds the separator names a path of its own.
func TestOriginKeysAreCollisionFree(t *testing.T) {
	a := okey([]string{"providers", "x\x00base_url"})
	b := okey([]string{"providers", "x", "base_url"})
	if a == b || strings.HasPrefix(b, okey([]string{"providers", "x\x00"})+"\x00") {
		t.Fatalf("keys collide: %q %q", a, b)
	}
	for _, segs := range [][]string{{"a", `b\0`, "c\x00d"}, {`\`}, {""}} {
		if got := unkey(okey(segs)); !slices.Equal(got, segs) {
			t.Errorf("unkey(okey(%q)) = %q", segs, got)
		}
	}
	m := newMerger()
	m.record([]string{"providers", "x", "base_url"}, "https://h", "user")
	m.forget([]string{"providers", "x\x00base_url"})
	if m.origins[okey([]string{"providers", "x", "base_url"})] != "user" {
		t.Fatal("forgetting another key dropped the origin")
	}
}

// Many distinct rules merge in linear time, and duplicates still go.
func TestUnionListIsLinear(t *testing.T) {
	var have, add []any
	for i := 0; i < 60000; i++ {
		have = append(have, fmt.Sprintf("Bash(a%d:*)", i))
		add = append(add, fmt.Sprintf("Bash(b%d:*)", i))
	}
	add = append(add, "Bash(a1:*)", map[string]any{"k": 1.0}, map[string]any{"k": 1.0})
	start := time.Now()
	out := unionList(have, add)
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("took %v", d)
	}
	if len(out) != 120001 {
		t.Fatalf("len = %d", len(out))
	}
}

// A file of many duplicate keys is parsed quickly, with a bounded number of warnings.
func TestManyDuplicateKeysAreCheap(t *testing.T) {
	var b strings.Builder
	b.WriteString("{\n")
	for i := 0; i < 100000; i++ {
		b.WriteString(`"k": 1,` + "\n")
	}
	b.WriteString(`"k": 2}`)
	start := time.Now()
	_, warns, err := parseJSONC("f.json", []byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("took %v", d)
	}
	if len(warns) != maxDupWarnings+1 || warns[0].Line != 3 {
		t.Fatalf("%d warnings, first %+v", len(warns), warns[0])
	}
}
