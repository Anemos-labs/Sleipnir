package tools

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// FileState is what makes concurrent editing safe without locks: an edit is accepted only when what the agent last read of the file is
// what the file holds now, or the agent wrote it itself. These are the cases of that sentence.
func TestFileStateAnEditNeedsTheFileToBeTheOneThatWasRead(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	for _, tc := range []struct {
		name    string
		steps   func(f *FileState)
		agent   string
		onDisk  string
		require bool
		want    string // "" is accepted, else a word the refusal has
	}{
		{"read and unchanged", func(f *FileState) { f.RecordRead("a", "x.go", []byte("one")) }, "a", "one", true, ""},
		{"never read, a read is required", func(*FileState) {}, "a", "one", true, "has not been read by you yet"},
		{"never read, none required (a new file)", func(*FileState) {}, "a", "one", false, ""},
		{"read, then changed by something else", func(f *FileState) { f.RecordRead("a", "x.go", []byte("one")) }, "a", "two", true, "changed since you last read it (modified by another process)"},
		{"read, then another agent wrote it", func(f *FileState) {
			f.RecordRead("a", "x.go", []byte("one"))
			f.RecordWrite("b", "x.go", []byte("two"), now)
		}, "a", "two", true, "modified by agent b"},
		{"the writer may go on editing what it wrote", func(f *FileState) {
			f.RecordRead("a", "x.go", []byte("one"))
			f.RecordWrite("a", "x.go", []byte("two"), now)
		}, "a", "two", true, ""},
		{"the writer who finds it changed again is told", func(f *FileState) {
			f.RecordWrite("a", "x.go", []byte("two"), now)
		}, "a", "three", true, "changed since you last read it"},
		{"the other agent wrote, then this one read it again", func(f *FileState) {
			f.RecordRead("a", "x.go", []byte("one"))
			f.RecordWrite("b", "x.go", []byte("two"), now)
			f.RecordRead("a", "x.go", []byte("two"))
		}, "a", "two", true, ""},
		{"one agent's read does not stand for another's", func(f *FileState) { f.RecordRead("a", "x.go", []byte("one")) }, "b", "one", true, "has not been read by you yet"},
		{"one file's read does not stand for another file", func(f *FileState) { f.RecordRead("a", "y.go", []byte("one")) }, "a", "one", true, "has not been read by you yet"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := NewFileState()
			tc.steps(f)
			err := f.CheckFresh(tc.agent, "x.go", []byte(tc.onDisk), tc.require)
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("error = %v, want one saying %q", err, tc.want)
			}
			if err != nil && !strings.Contains(err.Error(), "x.go") {
				t.Errorf("the refusal does not name the file: %v", err)
			}
		})
	}
}

// Agents of a swarm read and write the same files at once: the tracker is shared, and under -race it must stay a consistent one.
func TestFileStateAgentsWorkingAtOnce(t *testing.T) {
	f := NewFileState()
	const agents, rounds = 8, 200
	var wg sync.WaitGroup
	for a := 0; a < agents; a++ {
		wg.Add(1)
		go func(agent string) {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				path := fmt.Sprintf("f%d.go", i%5)
				body := []byte(fmt.Sprintf("%s round %d", agent, i))
				f.RecordRead(agent, path, body)
				f.CheckFresh(agent, path, body, true)
				f.RecordWrite(agent, path, body, time.Unix(int64(i), 0))
			}
		}(fmt.Sprintf("a%d", a))
	}
	wg.Wait()
	// after the rush each agent's own last write is what it has seen, whatever the others did meanwhile
	for a := 0; a < agents; a++ {
		agent := fmt.Sprintf("a%d", a)
		last := []byte(fmt.Sprintf("%s round %d", agent, rounds-1))
		if err := f.CheckFresh(agent, fmt.Sprintf("f%d.go", (rounds-1)%5), last, true); err != nil {
			t.Errorf("%s: %v", agent, err)
		}
	}
}
