package redact

import (
	"fmt"
	"testing"
)

func TestDbg(t *testing.T) {
	r := New(Config{Salt: "x"})
	in := `	cxBug(t, starts > 0, "%d cold-path compaction(s) started ('cache is cold: shrink for free by masking') although the provider merely does not report cache usage and the thread was only a few thousand tokens (MinThreadTokens=4000, Soft=20000)", starts)`
	out := r.String(in)
	fmt.Println(out)
	fmt.Println(r.Stats())
}
