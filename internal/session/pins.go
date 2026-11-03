package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/anemos-labs/sleipnir/internal/config"
)

// Pinned sessions: the sessions `sleipnir web` opens again by itself each time it starts, in the order they were pinned. The list is
// the person's, kept in the state directory (pins.json, beside trust.json), never in a project: a pin reopens a session with the
// server's own flags, as the Resume dialog does, and the verify command the person ran it with when they pinned it. A pinned session
// is never pruned. Every read-modify-write of the file in this process holds its write lock (config.WriteLock) and the file is
// replaced with an atomic rename.

// PinsFile is the name of the pin list in the state directory.
const PinsFile = "pins.json"

// MaxPins bounds the pin list: a server opens at most this many sessions by itself.
const MaxPins = 16

// maxPinsBytes bounds the file that is read, maxPinField a pin's name, folder and verify command.
const (
	maxPinsBytes = 256 << 10
	maxPinField  = 4096
)

// Pin is one pinned session: its id, the name its tab had, the folder it worked in, the verify command it ran with (the person's
// confirmation of it is what pinning it records), and when it was pinned.
type Pin struct {
	ID     string    `json:"id"`
	Name   string    `json:"name,omitempty"`
	Cwd    string    `json:"cwd,omitempty"`
	Verify string    `json:"verify,omitempty"`
	At     time.Time `json:"at"`
}

// pinsDoc is the file's shape.
type pinsDoc struct {
	Pins []Pin `json:"pins"`
}

// PinsPath is where the pin list of the state directory under home is kept.
func PinsPath(home string) string { return filepath.Join(stateRoot(home), PinsFile) }

// pinsPathOf is the pin list that belongs to the sessions directory root (the file beside it).
func pinsPathOf(root string) string {
	return filepath.Join(filepath.Dir(filepath.Clean(root)), PinsFile)
}

// LoadPins reads the pin list under home, in pin order. A missing file is an empty list; a file that is too large, not regular or not
// JSON is an error. Entries with an id that is not a session id's shape, and repeats of an id, are dropped.
func LoadPins(home string) ([]Pin, error) { return readPins(PinsPath(home)) }

// pinnedIDs is the set of the pinned session ids under the sessions directory root; a list that cannot be read pins nothing.
func pinnedIDs(root string) map[string]bool {
	pins, _ := readPins(pinsPathOf(root))
	out := make(map[string]bool, len(pins))
	for _, p := range pins {
		out[p.ID] = true
	}
	return out
}

// readPins reads the pin list at path (see LoadPins).
func readPins(path string) ([]Pin, error) {
	f, err := openRegularFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxPinsBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxPinsBytes {
		return nil, fmt.Errorf("%s is larger than %d bytes", PinsFile, maxPinsBytes)
	}
	var doc pinsDoc
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", PinsFile, err)
	}
	out := make([]Pin, 0, len(doc.Pins))
	for _, p := range doc.Pins {
		if !ValidID(p.ID) || slices.ContainsFunc(out, func(q Pin) bool { return q.ID == p.ID }) {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

// SetPin adds p to the end of the pin list under home, or updates the pin of p.ID in its place (its pin time kept). It refuses an id
// that is not a session id's shape, a field longer than 4096 bytes, and a new pin when MaxPins sessions are pinned already.
func SetPin(home string, p Pin) error {
	if !ValidID(p.ID) {
		return fmt.Errorf("%q is not a session id", p.ID)
	}
	if len(p.Name) > maxPinField || len(p.Cwd) > maxPinField || len(p.Verify) > maxPinField {
		return errors.New("a pin's name, folder or verify command is too long")
	}
	return editPins(home, func(pins []Pin) ([]Pin, error) {
		if i := slices.IndexFunc(pins, func(q Pin) bool { return q.ID == p.ID }); i >= 0 {
			if !pins[i].At.IsZero() {
				p.At = pins[i].At
			}
			pins[i] = p
			return pins, nil
		}
		if len(pins) >= MaxPins {
			return nil, fmt.Errorf("%d sessions are pinned already: unpin one first", MaxPins)
		}
		return append(pins, p), nil
	})
}

// RepinSession moves the pin of the session from to the session of p (a restart that continues in a new session), keeping its
// place and pin time. It does nothing when from is not pinned.
func RepinSession(home, from string, p Pin) error {
	if !ValidID(p.ID) {
		return fmt.Errorf("%q is not a session id", p.ID)
	}
	return editPins(home, func(pins []Pin) ([]Pin, error) {
		i := slices.IndexFunc(pins, func(q Pin) bool { return q.ID == from })
		if i < 0 {
			return pins, nil
		}
		p.At = pins[i].At
		pins[i] = p
		// the new session may have had a pin of its own: one place is enough
		for j := len(pins) - 1; j >= 0; j-- {
			if j != i && pins[j].ID == p.ID {
				pins = slices.Delete(pins, j, j+1)
			}
		}
		return pins, nil
	})
}

// Unpin removes the pin of the session id from the list under home; it reports whether there was one.
func Unpin(home, id string) (bool, error) {
	found := false
	err := editPins(home, func(pins []Pin) ([]Pin, error) {
		n := len(pins)
		pins = slices.DeleteFunc(pins, func(q Pin) bool { return q.ID == id })
		found = len(pins) != n
		return pins, nil
	})
	return found, err
}

// Pinned reports whether the session id is pinned under home.
func Pinned(home, id string) bool {
	pins, _ := LoadPins(home)
	return slices.ContainsFunc(pins, func(q Pin) bool { return q.ID == id })
}

// editPins changes the pin list under home with fn while holding the file's write lock, and writes it back when it changed.
func editPins(home string, fn func([]Pin) ([]Pin, error)) error {
	path := PinsPath(home)
	unlock := config.WriteLock(path)
	defer unlock()
	pins, err := readPins(path)
	if err != nil {
		pins = nil // a damaged list is replaced, not a reason to refuse a pin
	}
	before := slices.Clone(pins)
	pins, err = fn(pins)
	if err != nil {
		return err
	}
	if slices.Equal(before, pins) {
		return nil
	}
	return writePins(path, pins)
}

// writePins writes the pin list to path with an atomic rename (mode 0600).
func writePins(path string, pins []Pin) error {
	if pins == nil {
		pins = []Pin{}
	}
	b, err := json.MarshalIndent(pinsDoc{Pins: pins}, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".pins-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
