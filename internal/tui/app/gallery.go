package app

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Recording is one picture of the gallery (docs/media): which screen of the recorded showcase session to play, how big, from where and
// for how long. The manifest that lists them is the one place these choices are written down; the command that draws them
// (sleipnir replay --gallery) and the test that checks the committed files against the code read it, so a recording cannot
// be made one way and checked another.
type Recording struct {
	// Name is the file stem: Name.svg is the recording.
	Name string `json:"name"`
	// View is the screen (cockpit, cache, mail, board) and Agent the agent of the cache view.
	View  string `json:"view"`
	Agent string `json:"agent,omitempty"`
	// Cols and Rows are the size of the screen, FPS the frames a second and Speed the seconds of the session a second of the recording.
	Cols  int     `json:"cols"`
	Rows  int     `json:"rows"`
	FPS   int     `json:"fps"`
	Speed float64 `json:"speed,omitempty"`
	// From and Length are durations ("4s", "12s") in the time of the session: where the recording starts and how much of the session it
	// holds (empty: from the start, to the end). Hold is how long the last frame stays before the loop starts again.
	From   string `json:"from,omitempty"`
	Length string `json:"length,omitempty"`
	Hold   string `json:"hold,omitempty"`
	// StillAt is the second of the recording at which a still picture is taken (docs/media/Name.png); zero takes none.
	StillAt float64 `json:"still_at,omitempty"`
	// Caption says what the recording shows, for whoever lists the gallery.
	Caption string `json:"caption,omitempty"`
}

// LoadGallery reads a manifest: a JSON array of Recordings.
func LoadGallery(path string) ([]Recording, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var recs []Recording
	if err := json.Unmarshal(b, &recs); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	seen := map[string]bool{}
	for i, r := range recs {
		if r.Name == "" || seen[r.Name] {
			return nil, fmt.Errorf("%s: recording %d has no name or repeats one", path, i+1)
		}
		seen[r.Name] = true
		if _, err := r.Options(); err != nil {
			return nil, fmt.Errorf("%s: %s: %w", path, r.Name, err)
		}
	}
	return recs, nil
}

// Options are the recorder's options for the recording.
func (r Recording) Options() (RecordOptions, error) {
	v, err := ParseView(r.View)
	if err != nil {
		return RecordOptions{}, err
	}
	o := RecordOptions{Cols: r.Cols, Rows: r.Rows, FPS: r.FPS, Speed: r.Speed, View: v, Agent: r.Agent, Follow: r.Agent == ""}
	for _, d := range []struct {
		name string
		text string
		into *time.Duration
	}{{"from", r.From, &o.From}, {"length", r.Length, &o.Length}, {"hold", r.Hold, &o.Hold}} {
		if d.text == "" {
			continue
		}
		dur, err := time.ParseDuration(d.text)
		if err != nil || dur < 0 {
			return RecordOptions{}, fmt.Errorf("%s %q is not a duration", d.name, d.text)
		}
		*d.into = dur
	}
	return o, nil
}

// RenderGallery draws every recording of the manifest from the session log and returns the SVGs by file name (Name.svg).
func RenderGallery(logPath string, recs []Recording) (map[string]string, error) {
	out := make(map[string]string, len(recs))
	for _, r := range recs {
		o, err := r.Options()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", r.Name, err)
		}
		doc, err := Record(logPath, o)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", r.Name, err)
		}
		out[r.Name+".svg"] = doc
	}
	return out, nil
}
