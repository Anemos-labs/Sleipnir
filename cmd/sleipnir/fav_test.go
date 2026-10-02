package main

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/config"
)

func favoritesOnDisk(t *testing.T, home string) []string {
	t.Helper()
	cfg, _, err := config.Load(config.LoadOpts{Home: home, UntrustedProject: true, Environ: func() []string { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Models.Favorites
}

// /fav stars a model and unstars it: in the user's configuration, where `sleipnir models` reads it, and in the menu /model completes from,
// which is ordered again at once (starred first, marked with a star).
func TestFavTogglesAModelInTheMenuAndTheConfiguration(t *testing.T) {
	home := t.TempDir()
	m := &modelMenu{home: home, fav: map[string]bool{}}
	m.rows = []modelRow{
		row("heimdall/qwen/qwen3.8-27b", 262_000, 0.25, "tools"),
		row("heimdall/qwen/qwen3.8-flash-next", 1_000_000, 0.035, "tools"),
		row("ollama/llama3:8b", 8_000, 0),
	}
	m.mu.Lock()
	m.rebuild()
	m.mu.Unlock()
	refs := func() []string {
		var out []string
		for _, c := range m.Choices() {
			out = append(out, c.Text)
		}
		return out
	}
	if got := refs(); !slices.Equal(got, []string{"heimdall/qwen/qwen3.8-27b", "heimdall/qwen/qwen3.8-flash-next", "ollama/llama3:8b"}) {
		t.Fatalf("by reference to begin with: %v", got)
	}

	starred, err := m.Toggle("ollama/llama3:8b")
	if err != nil || !starred {
		t.Fatalf("starred=%v err=%v", starred, err)
	}
	got := m.Choices()
	if got[0].Text != "ollama/llama3:8b" || !strings.HasPrefix(got[0].Detail, "* ") || strings.HasPrefix(got[1].Detail, "* ") {
		t.Errorf("the starred model leads and says so: %+v", got)
	}
	if favs := favoritesOnDisk(t, home); !slices.Equal(favs, []string{"ollama/llama3:8b"}) {
		t.Errorf("kept in the user's configuration: %v", favs)
	}

	starred, err = m.Toggle("ollama/llama3:8b")
	if err != nil || starred {
		t.Fatalf("a second /fav unstars: starred=%v err=%v", starred, err)
	}
	if got := refs(); got[0] != "heimdall/qwen/qwen3.8-27b" {
		t.Errorf("back in its place: %v", got)
	}
	if favs := favoritesOnDisk(t, home); len(favs) != 0 {
		t.Errorf("removed from the configuration: %v", favs)
	}
	if _, err := m.Toggle("not-a-reference"); err == nil {
		t.Error("a word that is no provider/model is refused")
	}
	if _, err := os.Stat(config.UserConfigPath(home)); err != nil {
		t.Errorf("the file is there: %v", err)
	}
}

// The command says what it did, in the terminal program and in the line chat, and a session whose model is not known by name (a provider
// handed in as a value) has no model to star until one is named.
func TestFavCommandSaysWhatItDid(t *testing.T) {
	s := chatSession(t, false, nil)
	home := s.Home()
	h := &sessionHost{s: s}
	var out strings.Builder
	if _, ok := h.programCommand("/fav", &out); !ok || !strings.Contains(out.String(), "usage: /fav") {
		t.Errorf("no model named and none known: %q", out.String())
	}
	out.Reset()
	if _, ok := h.programCommand("/fav heimdall/qwen/qwen3.8-27b", &out); !ok || !strings.HasPrefix(out.String(), "starred heimdall/qwen/qwen3.8-27b") {
		t.Errorf("starred: %q", out.String())
	}
	if favs := favoritesOnDisk(t, home); !slices.Equal(favs, []string{"heimdall/qwen/qwen3.8-27b"}) {
		t.Errorf("the session's own home holds it, and not the machine's: %v", favs)
	}
	out.Reset()
	if slashTo(t.Context(), s, "/fav heimdall/qwen/qwen3.8-27b", &out, &out); !strings.HasPrefix(out.String(), "unstarred ") {
		t.Errorf("the line chat unstars with the same words: %q", out.String())
	}
	out.Reset()
	if _, ok := h.programCommand("/fav a b", &out); !ok || !strings.Contains(out.String(), "usage: /fav") {
		t.Errorf("two names: %q", out.String())
	}
}
