package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The recordings of the gallery are drawn by `sleipnir replay --gallery`, and scripts/record-demo.sh takes the stills from what it
// prints: one line for each still of each recording, "still SVG PNG SECONDS", the picture PNG.png taken from SVG.svg that many seconds
// into its loop. The chat is one of the recordings, and it has two stills.
func TestReplayGalleryDrawsTheChatFromItsTranscript(t *testing.T) {
	if testing.Short() {
		t.Skip("it draws every recording of the gallery")
	}
	root := filepath.Join("..", "..")
	var out, errs bytes.Buffer
	dir := t.TempDir()
	if err := renderGallery(&out, &errs, filepath.Join(root, "docs", "media", "showcase", "events.jsonl"), filepath.Join(root, "docs", "media", "gallery.json"), dir); err != nil {
		t.Fatal(err)
	}
	var chat []string
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		f := strings.Fields(line)
		if len(f) != 4 || f[0] != "still" {
			t.Errorf("the line %q is not of the form still SVG PNG SECONDS", line)
			continue
		}
		if _, err := strconv.ParseFloat(f[3], 64); err != nil {
			t.Errorf("the line %q does not end in a number of seconds", line)
		}
		if f[1] == "chat" {
			chat = append(chat, f[2])
		}
	}
	sort.Strings(chat)
	if want := []string{"chat", "chat-ask"}; !reflect.DeepEqual(chat, want) {
		t.Errorf("the stills taken from the chat recording are %v, want %v; the whole output:\n%s", chat, want, out.String())
	}

	// and the files it writes are the ones that are committed, byte for byte, which is what the script's --check says too
	for _, name := range []string{"chat.svg", "swarm.svg", "cache.svg", "fold.svg"} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Errorf("%v", err)
			continue
		}
		want, err := os.ReadFile(filepath.Join(root, "docs", "media", name))
		if err != nil {
			t.Errorf("%v", err)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s is not what `sleipnir replay --gallery` draws: scripts/record-demo.sh draws it again", name)
		}
	}
}
