package friction

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// A write looks only at the parts of files that could be the one written, read since their last change: a long log of reads
// of many files and writes of others takes time in proportion to its length.
func TestChangedVisitsOnlyTheFileWritten(t *testing.T) {
	var c ReadCounter
	in := json.RawMessage(`{}`)
	for i := 0; i < 20000; i++ {
		c.Read(fmt.Sprintf("/w/r%d.go", i), in)
	}
	start := time.Now()
	for i := 0; i < 20000; i++ {
		c.Changed(fmt.Sprintf("w%d.go", i))
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("took %v", d)
	}
	c.Changed("r7.go")
	if _, _, n := c.Read("/w/r7.go", in); n != 1 {
		t.Fatalf("n = %d after the file changed", n)
	}
	if _, _, n := c.Read("/w/r8.go", in); n != 2 {
		t.Fatalf("n = %d for a file that did not change", n)
	}
}
