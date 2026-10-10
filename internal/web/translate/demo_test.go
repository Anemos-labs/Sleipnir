package translate

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/demo"
	"github.com/anemos-labs/sleipnir/internal/events"
)

// runDemo runs a scenario of the repository's demo in-process (the real harness against its mock endpoint) and returns the session's
// log.
func runDemo(t *testing.T, scenario string, during func(path string)) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("SLEIPNIR_HOME", filepath.Join(dir, "state"))
	path := filepath.Join(dir, "session", "events.jsonl")
	if during != nil {
		during(path)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if _, err := demo.Run(ctx, demo.Options{Scenario: scenario, Scale: 0.25, Topics: 4, Dir: dir}); err != nil {
		t.Fatal(err)
	}
	return path
}

// A session the demo runs now translates within the journal's rules, with the kinds a team's run is made of; followed while it is
// written (the shop) and read afterwards (the handbook).
func TestDemoRunsTranslate(t *testing.T) {
	t.Run("handbook", func(t *testing.T) {
		path := runDemo(t, "handbook", nil)
		var evs []events.Event
		if err := events.Scan(path, func(e events.Event) error { evs = append(evs, e); return nil }); err != nil {
			t.Fatal(err)
		}
		for _, logOnly := range []bool{false, true} {
			h := translateLog(t, evs, "", logOnly)
			raws := h.raws()
			checkStream(t, raws)
			for _, k := range []string{`"k":"state"`, `"k":"task"`, `"k":"req"`, `"k":"use"`, `"k":"layers"`, `"k":"note"`} {
				if !containsRaw(raws, k) {
					t.Errorf("logOnly %v: no %s", logOnly, k)
				}
			}
		}
	})
	t.Run("shop", func(t *testing.T) {
		if testing.Short() {
			t.Skip("the shop scenario takes several seconds")
		}
		tr := New(Config{Tab: "shop"})
		defer tr.Close()
		path := runDemo(t, "shop", func(path string) { tr.Follow(path) })
		waitFor(t, func() bool {
			_, evs, _, _ := tr.Journal()
			return containsRaw(evs, `"k":"final"`) && containsRaw(evs, `"k":"merge"`)
		})
		_, raws, _, _ := tr.Journal()
		checkStream(t, raws)
		for _, k := range []string{`"k":"tool"`, `"k":"mail"`, `"k":"queue"`, `"k":"break"`, `"k":"compact"`, `"k":"ckpt"`, `"who":"mgr"`} {
			if !containsRaw(raws, k) {
				t.Errorf("no %s in a followed shop run", k)
			}
		}
		_ = path
	})
}
