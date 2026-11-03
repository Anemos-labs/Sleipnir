package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPinsSetUpdateRepinUnpin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SLEIPNIR_HOME", "")
	a, b, c := "20261009-221530-a91c3e", "20261010-080000-000001", "20261011-090000-00000f"
	at := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	if err := SetPin(home, Pin{ID: a, Name: "shop", At: at}); err != nil {
		t.Fatal(err)
	}
	if err := SetPin(home, Pin{ID: b, Name: "docs", At: at.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	// an update keeps its place and its pin time
	if err := SetPin(home, Pin{ID: a, Name: "shop 2", Verify: "go test ./...", At: at.Add(2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	pins, err := LoadPins(home)
	if err != nil || len(pins) != 2 || pins[0].ID != a || pins[0].Name != "shop 2" || !pins[0].At.Equal(at) || pins[0].Verify != "go test ./..." || pins[1].ID != b {
		t.Fatalf("pins = %+v, %v", pins, err)
	}
	if !Pinned(home, a) || Pinned(home, c) {
		t.Fatal("Pinned does not read the list")
	}
	// a restart that continues in a new session moves the pin in place
	if err := RepinSession(home, a, Pin{ID: c, Name: "shop 2"}); err != nil {
		t.Fatal(err)
	}
	pins, _ = LoadPins(home)
	if len(pins) != 2 || pins[0].ID != c || !pins[0].At.Equal(at) {
		t.Fatalf("after repin: %+v", pins)
	}
	if err := RepinSession(home, a, Pin{ID: b}); err != nil { // a is no longer pinned: nothing changes
		t.Fatal(err)
	}
	if found, err := Unpin(home, c); err != nil || !found {
		t.Fatalf("Unpin = %v, %v", found, err)
	}
	if found, _ := Unpin(home, c); found {
		t.Fatal("a second Unpin found a pin")
	}
	pins, _ = LoadPins(home)
	if len(pins) != 1 || pins[0].ID != b {
		t.Fatalf("after unpin: %+v", pins)
	}
	if fi, err := os.Stat(PinsPath(home)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("pins file: %v, %v", fi, err)
	}
}

func TestPinsRefuseBadIDsAndTooMany(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SLEIPNIR_HOME", "")
	if err := SetPin(home, Pin{ID: "../etc"}); err == nil {
		t.Fatal("a path was pinned")
	}
	for i := range MaxPins {
		id := time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC).Format("20060102-150405") + "-abcdef"
		if err := SetPin(home, Pin{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := SetPin(home, Pin{ID: "20270101-000000-abcdef"}); err == nil {
		t.Fatal("more than MaxPins were pinned")
	}
}

func TestLoadPinsDropsBadEntriesAndRepeats(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SLEIPNIR_HOME", "")
	path := PinsPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"pins":[{"id":"20261009-221530-a91c3e"},{"id":"nope"},{"id":"20261009-221530-a91c3e","name":"again"}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	pins, err := LoadPins(home)
	if err != nil || len(pins) != 1 || pins[0].Name != "" {
		t.Fatalf("pins = %+v, %v", pins, err)
	}
	// a damaged list is replaced by the next pin
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPins(home); err == nil {
		t.Fatal("a damaged list was read")
	}
	if err := SetPin(home, Pin{ID: "20261010-080000-000001"}); err != nil {
		t.Fatal(err)
	}
	if pins, err := LoadPins(home); err != nil || len(pins) != 1 {
		t.Fatalf("after replacing a damaged list: %+v, %v", pins, err)
	}
}

func TestPrunePlanKeepsPinned(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SLEIPNIR_HOME", "")
	root := SessionsDir(home)
	old := time.Now().Add(-90 * 24 * time.Hour)
	ids := []string{"20250101-000000-000001", "20250101-000000-000002"}
	for _, id := range ids {
		dir := filepath.Join(root, id)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		log := filepath.Join(dir, "events.jsonl")
		if err := os.WriteFile(log, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(log, old, old)
		_ = os.Chtimes(dir, old, old)
	}
	if err := SetPin(home, Pin{ID: ids[0]}); err != nil {
		t.Fatal(err)
	}
	p, err := PlanPruneDir(root, time.Now(), 30*24*time.Hour, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Delete) != 1 || p.Delete[0].ID != ids[1] || len(p.Pinned) != 1 || p.Pinned[0] != ids[0] {
		t.Fatalf("plan = delete %+v, pinned %v", p.Delete, p.Pinned)
	}
	list, err := ListRecordedDir(root, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range list {
		if r.Pinned != (r.ID == ids[0]) {
			t.Fatalf("listing: %s pinned=%v", r.ID, r.Pinned)
		}
	}
}
