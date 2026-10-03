package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/update"
)

// The chat says that a newer release is out from what the last check kept, and says nothing when there is nothing to say, when the build is
// one from source, or when the person turned the check off.
func TestTheChatSaysWhenANewerReleaseIsOut(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("SLEIPNIR_NO_UPDATE_CHECK", "")
	old := version
	defer func() { version = old }()
	version = "0.1.0"
	if n := updateNotice(); n != "" {
		t.Fatalf("with no check kept: %q", n)
	}
	update.Remember(updateOptions(), update.Status{Checked: time.Now(), Latest: "v0.1.5", Behind: 43})
	want := "A newer Sleipnir is out: v0.1.5, 43 commits ahead of yours. Run `sleipnir update`."
	if n := updateNotice(); n != want {
		t.Errorf("notice = %q", n)
	}
	t.Setenv("SLEIPNIR_NO_UPDATE_CHECK", "1")
	if n := updateNotice(); n != "" {
		t.Errorf("turned off: %q", n)
	}
	t.Setenv("SLEIPNIR_NO_UPDATE_CHECK", "")
	version = "dev"
	if n := updateNotice(); n != "" {
		t.Errorf("a build from source: %q", n)
	}
	version = "0.1.5"
	if n := updateNotice(); n != "" {
		t.Errorf("the latest version: %q", n)
	}
	if got := filepath.Base(updateOptions().CachePath); got != "update.json" {
		t.Errorf("cache file %q", got)
	}
}

func TestUpdateOnABuildFromSourceSaysHowToUpdateItAndTouchesNothing(t *testing.T) {
	var out bytes.Buffer
	exe := filepath.Join(t.TempDir(), "sleipnir")
	if err := runUpdate(context.Background(), &out, update.Options{}, "dev", "none", exe, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "from source") || !strings.Contains(out.String(), "go install") {
		t.Errorf("said %q", out.String())
	}
}
