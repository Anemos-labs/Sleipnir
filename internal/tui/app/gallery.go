package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// viewChat is the View of a recording of the chat: it is not a screen of the cockpit but the chat program itself, played from a chat
// transcript (Recording.Source).
const viewChat = "chat"

// Recording is one picture of the gallery (docs/media): which screen of the recorded showcase session to play, how big, from where and
// for how long. The manifest that lists them is the one place these choices are written down; the command that draws them
// (sleipnir replay --gallery) and the test that checks the committed files against the code read it, so a recording cannot
// be made one way and checked another.
type Recording struct {
	// Name is the file stem: Name.svg is the recording.
	Name string `json:"name"`
	// View is the screen (cockpit, cache, mail, board) and Agent the agent of the cache view. View "chat" is the chat program, played
	// from the transcript in Source (a path relative to the manifest) instead of from the showcase session's log.
	View   string `json:"view"`
	Agent  string `json:"agent,omitempty"`
	Source string `json:"source,omitempty"`
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
	// StillAt is the second of the recording at which a still picture is taken (docs/media/Name.png); zero takes none. Stills are more
	// of them, each with a file name of its own.
	StillAt float64 `json:"still_at,omitempty"`
	Stills  []Still `json:"stills,omitempty"`
	// Caption says what the recording shows, for whoever lists the gallery.
	Caption string `json:"caption,omitempty"`

	dir string // the directory of the manifest that listed it
}

// Still is a picture taken from a recording at a moment of it: docs/media/Name.png.
type Still struct {
	Name string  `json:"name"`
	At   float64 `json:"at"`
}

// StillList is every still the recording asks for: the one named like it, then the others.
func (r Recording) StillList() []Still {
	var out []Still
	if r.StillAt > 0 {
		out = append(out, Still{Name: r.Name, At: r.StillAt})
	}
	return append(out, r.Stills...)
}

// Chat says whether the recording is of the chat program.
func (r Recording) isChat() bool { return r.View == viewChat }

// SourcePath is where the transcript of a chat recording is, as far as the manifest knows.
func (r Recording) sourcePath() string {
	if r.dir == "" || filepath.IsAbs(r.Source) {
		return filepath.FromSlash(r.Source)
	}
	return filepath.Join(r.dir, filepath.FromSlash(r.Source))
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
	for i := range recs {
		r := &recs[i]
		r.dir = filepath.Dir(path)
		if r.Name == "" || seen[r.Name] {
			return nil, fmt.Errorf("%s: recording %d has no name or repeats one", path, i+1)
		}
		seen[r.Name] = true
		if r.isChat() {
			if r.Source == "" {
				return nil, fmt.Errorf("%s: %s is a chat recording and names no source", path, r.Name)
			}
			if _, err := r.ChatOptions(); err != nil {
				return nil, fmt.Errorf("%s: %s: %w", path, r.Name, err)
			}
		} else if _, err := r.Options(); err != nil {
			return nil, fmt.Errorf("%s: %s: %w", path, r.Name, err)
		}
		for _, s := range r.Stills {
			if s.Name == "" || s.At <= 0 || seen[s.Name] {
				return nil, fmt.Errorf("%s: %s: a still has no name, no moment, or a name that is taken", path, r.Name)
			}
			seen[s.Name] = true
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
	if err := r.durations(&o.From, &o.Length, &o.Hold); err != nil {
		return RecordOptions{}, err
	}
	return o, nil
}

// ChatOptions are the options of the player for a recording of the chat.
func (r Recording) ChatOptions() (ChatPlayOptions, error) {
	if r.Speed != 0 && r.Speed != 1 {
		return ChatPlayOptions{}, fmt.Errorf("a chat recording is played at the speed of its session, not %g", r.Speed)
	}
	o := ChatPlayOptions{Cols: r.Cols, Rows: r.Rows, FPS: r.FPS}
	if err := r.durations(&o.From, &o.Length, &o.Hold); err != nil {
		return ChatPlayOptions{}, err
	}
	return o, nil
}

func (r Recording) durations(from, length, hold *time.Duration) error {
	for _, d := range []struct {
		name string
		text string
		into *time.Duration
	}{{"from", r.From, from}, {"length", r.Length, length}, {"hold", r.Hold, hold}} {
		if d.text == "" {
			continue
		}
		dur, err := time.ParseDuration(d.text)
		if err != nil || dur < 0 {
			return fmt.Errorf("%s %q is not a duration", d.name, d.text)
		}
		*d.into = dur
	}
	return nil
}

// RenderGallery draws every recording of the manifest and returns the SVGs by file name (Name.svg): the cockpit's from the session log
// at logPath, the chat's from the transcript each of them names.
func RenderGallery(logPath string, recs []Recording) (map[string]string, error) {
	out := make(map[string]string, len(recs))
	for _, r := range recs {
		if r.isChat() {
			o, err := r.ChatOptions()
			if err != nil {
				return nil, fmt.Errorf("%s: %w", r.Name, err)
			}
			doc, err := recordChat(r.sourcePath(), o)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", r.Name, err)
			}
			out[r.Name+".svg"] = doc
			continue
		}
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
