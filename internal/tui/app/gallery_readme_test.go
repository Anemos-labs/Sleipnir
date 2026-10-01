package app

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// These two say what the README and the gallery owe each other. They use nothing but the manifest and the README, so they say it of any
// checkout, whatever the program can play.

// The chat is a recording of the gallery like the swarm's. It is the program a person is in for most of the time, and it was the one
// that had no recording of itself, only a hand-drawn sketch.
func TestTheGalleryHasTheRecordingOfTheChat(t *testing.T) {
	recs, err := LoadGallery(repoFile(t, galleryManifest))
	if err != nil {
		t.Fatal(err)
	}
	var chats int
	for _, r := range recs {
		if r.View == "chat" {
			chats++
		}
	}
	if chats != 1 {
		t.Errorf("%s lists %d recordings of the chat, want one: the chat program, played from a transcript of a session (scripts/record-demo.sh --new-chat makes the transcript)", galleryManifest, chats)
	}
}

// What the README shows is what the gallery lists. A recording that is made and checked against the code but left out of the README is a
// picture nobody sees, and a README that embeds a file the gallery does not make is one that goes stale.
func TestTheReadmeEmbedsEveryRecordingOfTheGallery(t *testing.T) {
	recs, err := LoadGallery(repoFile(t, galleryManifest))
	if err != nil {
		t.Fatal(err)
	}
	readme, err := os.ReadFile(repoFile(t, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	embedded := map[string]bool{}
	for _, m := range regexp.MustCompile(`<img[^>]*src="docs/media/([A-Za-z0-9_-]+)\.svg"`).FindAllStringSubmatch(string(readme), -1) {
		embedded[m[1]] = true
	}
	listed := map[string]bool{}
	for _, r := range recs {
		listed[r.Name] = true
		if !embedded[r.Name] {
			t.Errorf("README.md does not embed docs/media/%s.svg, which the gallery draws", r.Name)
		}
	}
	for name := range embedded {
		if _, err := os.Stat(repoFile(t, "docs/media/"+name+".svg")); err != nil {
			t.Errorf("README.md embeds docs/media/%s.svg: %v", name, err)
		}
		if !listed[name] && !strings.HasPrefix(name, "logo") {
			t.Errorf("README.md embeds docs/media/%s.svg, which %s does not draw", name, galleryManifest)
		}
	}
}
