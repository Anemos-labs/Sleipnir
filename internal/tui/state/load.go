package state

import (
	"errors"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// errStop ends an events.Scan early; it is never returned to a caller.
var errStop = errors.New("state: stop")

// Fold reads the event log at path (events.jsonl) and folds every event into a new State.
//
// It reads the log the way events.Scan does, so it is tolerant of what a crash leaves: a torn last line (a write cut short) is
// ignored, a complete line that is not a valid event is skipped and counted (Stats.Corrupt), a line longer than
// events.MaxEventBytes is skipped without being buffered, and fields that Fold does not know are ignored. A log that does not
// exist, or cannot be read, is an error and the State is nil; damage to the log is not an error.
func Fold(path string) (*State, error) { return FoldUntil(path, 0) }

// FoldUntil is Fold that stops after the last event whose Seq is not above until. Zero means the whole log.
func FoldUntil(path string, until uint64) (*State, error) {
	st := New()
	if err := FoldInto(st, path, until); err != nil {
		return nil, err
	}
	return st, nil
}

// FoldInto folds the log at path into st, which may already hold earlier events (the State ignores the ones it has seen: an
// event whose Seq is not above the highest applied is a duplicate). until is as in FoldUntil.
func FoldInto(st *State, path string, until uint64) error {
	err := events.Scan(path, func(e events.Event) error {
		if until > 0 && e.Seq > until {
			return errStop
		}
		st.Apply(e)
		return nil
	})
	return tolerate(st, err)
}

// tolerate turns the two outcomes of a scan that are not failures into nil: the scan was stopped by the caller, or it skipped
// damaged lines (which are counted in the State).
func tolerate(st *State, err error) error {
	var ce *events.CorruptError
	switch {
	case err == nil, errors.Is(err, errStop):
		return nil
	case errors.As(err, &ce):
		st.AddCorrupt(ce.Lines)
		return nil
	}
	return err
}
