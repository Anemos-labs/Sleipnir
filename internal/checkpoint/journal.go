package checkpoint

import (
	"io/fs"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
)

// The write journal. A checkpoint's record of a path keeps, besides the state before
// the first write (Pre), one entry per write that After was told of: who wrote, when,
// and the content the write left, saved in the blob store. Replaying the entries over
// the first pre-image says who wrote each line of a file, exactly as long as every
// change between the entries was a recorded write (Authorship). A change made by
// anything that does not record (a shell command, a person's editor) is noticed when
// it can be: the next Before finds the file different from what the last write left
// (the entry after it is marked Gap), or the next checkpoint's pre-image differs from
// the last entry of the previous one.

const (
	// maxJournalBytes is the largest content a journal entry keeps: past it the
	// entry records who wrote and the checksum, not the bytes (the same bound as a
	// diff's input).
	maxJournalBytes = maxDiffBytes
	// maxJournal bounds the entries of one record; older entries beyond it are
	// dropped and counted in WritesLost.
	maxJournal = 1000
)

// jwrite is one journaled write.
type jwrite struct {
	Agent string    `json:"agent,omitempty"`
	At    time.Time `json:"at"`
	// Kind is what the write left at the path ("" when it is not known: another
	// agent wrote the same path while this write was being fingerprinted).
	Kind kind `json:"kind,omitempty"`
	// Blob is the content the write left, for a regular file of at most
	// maxJournalBytes; Sum its checksum (set for every regular file).
	Blob  core.Hash `json:"blob,omitempty"`
	Sum   core.Hash `json:"sum,omitempty"`
	Size  int64     `json:"size,omitempty"`
	MTime int64     `json:"mtime,omitempty"`
	// Gap says that the file was changed by someone who does not record between
	// the previous journaled write and this one.
	Gap bool `json:"gap,omitempty"`
}

// known reports whether the entry says what content the write left.
func (w jwrite) known() bool {
	switch w.Kind {
	case kAbsent:
		return true
	case kFile:
		return w.Blob != ""
	}
	return false
}

// journal appends an entry, dropping the oldest beyond the cap.
func (r *fileRec) journal(w jwrite) {
	r.Writes = append(r.Writes, w)
	if n := len(r.Writes) - maxJournal; n > 0 {
		r.Writes = append([]jwrite(nil), r.Writes[n:]...)
		r.WritesLost += n
	}
}

// vetJournal drops the journal entries of a loaded record that do not check out and
// counts them as lost.
func (s *Store) vetJournal(r *fileRec) {
	if r.WritesLost < 0 {
		r.WritesLost = 0
	}
	kept := r.Writes[:0]
	for _, w := range r.Writes {
		if !s.validJournalEntry(&w) {
			r.WritesLost++
			continue
		}
		kept = append(kept, w)
	}
	r.Writes = kept
	if n := len(r.Writes) - maxJournal; n > 0 {
		r.Writes = append([]jwrite(nil), r.Writes[n:]...)
		r.WritesLost += n
	}
}

// validJournalEntry vets (and tidies) one entry read from a manifest.
func (s *Store) validJournalEntry(w *jwrite) bool {
	switch w.Kind {
	case "", kAbsent, kFile, kLink, kDir, kOther, kUnsaved:
	default:
		return false
	}
	if w.Blob != "" && (!events.ValidHash(w.Blob) || w.Kind != kFile) {
		return false
	}
	if w.Sum != "" && !events.ValidHash(w.Sum) {
		return false
	}
	if w.Size < 0 || w.Size > s.maxBytes {
		return false
	}
	w.Agent = cleanText(w.Agent, 128)
	return true
}

// journalBlob saves the content a write left, when it is a regular file small enough
// to keep; "" otherwise. A failure to save is not an error for the write: the entry
// then records the checksum only.
func (s *Store) journalBlob(st state, data []byte) core.Hash {
	if st.Kind != kFile || int64(len(data)) > maxJournalBytes {
		return ""
	}
	h, err := s.blobs.Put(data)
	if err != nil {
		return ""
	}
	return h
}

// changedSince reports whether what is on disk (fi, or lerr when it could not be
// inspected) is no longer what a write left (post): a different size or modification
// time for a file, or a file where there was none. It is a cheap test, made without
// reading the file.
func changedSince(post state, fi fs.FileInfo, lerr error) bool {
	switch post.Kind {
	case kFile:
		if lerr != nil || !fi.Mode().IsRegular() {
			return true
		}
		return fi.Size() != post.Size || fi.ModTime().UnixNano() != post.MTime
	case kAbsent:
		return lerr == nil
	}
	return false
}

// Write is one recorded tool write: which checkpoint was current, which agent wrote
// and when, the blob of the content the write left ("" when it was not kept: a file
// over the journal's size bound, not a regular file, or a write whose result another
// agent's write overtook), its checksum, whether the file was gone afterwards, and
// whether the file had been changed by someone who does not record since the
// previous recorded write (Gap).
type Write struct {
	Checkpoint, Agent string
	Blob              core.Hash
	At                time.Time
	Sum               core.Hash
	Deleted           bool
	Gap               bool
}

// Writes returns the recorded writes of a file in order (the write journal, for exact
// authorship). path is relative to the store's root (or absolute inside it). Writes
// that a restore undid are no longer listed: the restore forgets the records of the
// paths it puts back.
func (s *Store) Writes(path string) []Write {
	key, err := s.keyOf(path)
	if err != nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Write
	for _, cp := range s.cps {
		r := cp.index[key]
		if r == nil {
			continue
		}
		for _, w := range r.Writes {
			out = append(out, Write{Checkpoint: cp.ID, Agent: w.Agent, Blob: w.Blob, At: w.At, Sum: w.Sum,
				Deleted: w.Kind == kAbsent, Gap: w.Gap})
		}
	}
	return out
}

// LastWriter names the agent of the newest journaled write of path, and the
// checkpoint it was made in.
func (s *Store) LastWriter(path string) (agent, checkpointID string, ok bool) {
	key, err := s.keyOf(path)
	if err != nil {
		return "", "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.cps) - 1; i >= 0; i-- {
		r := s.cps[i].index[key]
		if r == nil {
			continue
		}
		if n := len(r.Writes); n > 0 {
			return r.Writes[n-1].Agent, s.cps[i].ID, true
		}
		if len(r.Agents) > 0 { // a record of a manifest written before the journal existed
			return r.Agents[len(r.Agents)-1], s.cps[i].ID, true
		}
	}
	return "", "", false
}
