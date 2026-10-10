package session

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMetaRoundTripAndBounds(t *testing.T) {
	dir := t.TempDir()
	mod := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	writeLog(t, dir, mod, mod, [3]any{"session.start", "", map[string]any{}})
	if m, err := LoadMeta(dir); err != nil || m.Name != "" || m.Reviewed != nil {
		t.Fatalf("no sidecar yet: %+v %v", m, err)
	}
	if err := UpdateMeta(dir, func(m *Meta) {
		m.Name = "shop"
		m.Reviewed = map[string]string{"api/items.go": "c02"}
	}); err != nil {
		t.Fatal(err)
	}
	m, err := LoadMeta(dir)
	if err != nil || m.Name != "shop" || m.Reviewed["api/items.go"] != "c02" {
		t.Fatalf("round trip: %+v %v", m, err)
	}
	if fi, err := os.Stat(filepath.Join(dir, MetaFile)); err != nil {
		t.Fatal(err)
	} else if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(dir); !fi.ModTime().Equal(mod) {
		t.Errorf("the directory's time moved: %v", fi.ModTime())
	}
	if err := UpdateMeta(dir, func(m *Meta) { m.Name = strings.Repeat("x", 300) }); err == nil {
		t.Error("an over-long name was written")
	}
	if m, _ := LoadMeta(dir); m.Name != "shop" {
		t.Errorf("a refused change wrote anyway: %q", m.Name)
	}
	if err := UpdateMeta(t.TempDir(), func(m *Meta) { m.Name = "x" }); err == nil {
		t.Error("a directory that is not a session got a sidecar")
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".web-") {
			t.Errorf("a temporary file was left: %s", e.Name())
		}
	}
}

// Writers of one directory in this process do not lose each other's change.
func TestMetaWritersAreSerialised(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir, time.Now(), time.Now(), [3]any{"session.start", "", map[string]any{}})
	var wg sync.WaitGroup
	for i := range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := UpdateMeta(dir, func(m *Meta) {
				if m.Reviewed == nil {
					m.Reviewed = map[string]string{}
				}
				m.Reviewed[filepath.Join("f", string(rune('a'+i%26)), string(rune('a'+i/26)))] = "c1"
			}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if m, _ := LoadMeta(dir); len(m.Reviewed) != 40 {
		t.Fatalf("%d of 40 marks kept", len(m.Reviewed))
	}
}

func TestLoadMetaRefusesWhatIsNotASidecar(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, MetaFile), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadMeta(dir); err == nil {
		t.Error("a damaged sidecar was read")
	}
	other := filepath.Join(t.TempDir(), "elsewhere.json")
	if err := os.WriteFile(other, []byte(`{"name":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := t.TempDir()
	if err := os.Symlink(other, filepath.Join(link, MetaFile)); err != nil {
		t.Skipf("no symbolic links: %v", err)
	}
	if _, err := LoadMeta(link); err == nil {
		t.Error("a sidecar that is a link was followed")
	}
}
