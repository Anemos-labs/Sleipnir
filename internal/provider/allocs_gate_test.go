//go:build !race

package provider

import (
	"bytes"
	"io"
	"testing"
)

// A stream is read an event at a time, and what it costs per event (about three allocations) is what a long answer of fifty agents
// multiplies. Held to what it is, with a fifth to spare (measured: 1513 for 500 chunks).
func TestSSEReaderAllocationsAreHeld(t *testing.T) {
	stream := benchStream(500)
	got := testing.AllocsPerRun(20, func() {
		r := NewSSEReader(bytes.NewReader(stream))
		for {
			if _, err := r.Next(); err == io.EOF {
				return
			} else if err != nil {
				t.Fatal(err)
			}
		}
	})
	if got > 1850 {
		t.Errorf("reading a stream of 500 chunks allocates %.0f times, more than the 1850 it is held to", got)
	}
}
