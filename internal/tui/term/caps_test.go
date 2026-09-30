package term

import (
	"os"
	"path/filepath"
	"testing"
)

func envOf(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

// A terminal in the table: each case names the environment, and only the fields it cares about are compared (the rest
// are checked by the properties below).
func TestFromEnvTable(t *testing.T) {
	const utf8 = "en_US.UTF-8"
	cases := []struct {
		name string
		env  map[string]string
		tty  bool
		w, h int

		color   ColorDepth
		unicode bool
		sync    bool
		paste   bool
		anim    bool
		dumb    bool
	}{
		{name: "xterm-256color utf-8", env: map[string]string{"TERM": "xterm-256color", "LANG": utf8}, tty: true,
			color: ColorANSI256, unicode: true, paste: true, anim: true},
		{name: "truecolor by COLORTERM", env: map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor", "LANG": utf8}, tty: true,
			color: ColorTrueColor, unicode: true, paste: true, anim: true},
		{name: "24bit by COLORTERM", env: map[string]string{"TERM": "xterm", "COLORTERM": "24bit", "LANG": utf8}, tty: true,
			color: ColorTrueColor, unicode: true, paste: true, anim: true},
		{name: "COLORTERM is case-insensitive", env: map[string]string{"TERM": "xterm", "COLORTERM": "TrueColor"}, tty: true,
			color: ColorTrueColor, paste: true, anim: true},
		{name: "kitty: truecolor and sync by TERM alone (ssh forwards TERM, not COLORTERM)", env: map[string]string{"TERM": "xterm-kitty", "LANG": utf8}, tty: true,
			color: ColorTrueColor, unicode: true, sync: true, paste: true, anim: true},
		{name: "ghostty", env: map[string]string{"TERM": "xterm-ghostty", "LC_ALL": utf8}, tty: true,
			color: ColorTrueColor, unicode: true, sync: true, paste: true, anim: true},
		{name: "foot", env: map[string]string{"TERM": "foot", "LANG": utf8}, tty: true,
			color: ColorTrueColor, unicode: true, sync: true, paste: true, anim: true},
		{name: "alacritty", env: map[string]string{"TERM": "alacritty", "LANG": utf8}, tty: true,
			color: ColorTrueColor, unicode: true, sync: true, paste: true, anim: true},
		{name: "wezterm by TERM_PROGRAM", env: map[string]string{"TERM": "xterm-256color", "TERM_PROGRAM": "WezTerm", "LANG": utf8}, tty: true,
			color: ColorTrueColor, unicode: true, sync: true, paste: true, anim: true},
		{name: "iTerm2 by TERM_PROGRAM", env: map[string]string{"TERM": "xterm-256color", "TERM_PROGRAM": "iTerm.app", "LANG": utf8}, tty: true,
			color: ColorTrueColor, unicode: true, sync: true, paste: true, anim: true},
		{name: "iTerm2 over ssh (LC_TERMINAL is forwarded)", env: map[string]string{"TERM": "xterm-256color", "LC_TERMINAL": "iTerm2", "LANG": utf8}, tty: true,
			color: ColorANSI256, unicode: true, sync: true, paste: true, anim: true},
		{name: "Apple Terminal has 256 colours and no sync", env: map[string]string{"TERM": "xterm-256color", "TERM_PROGRAM": "Apple_Terminal", "LANG": utf8}, tty: true,
			color: ColorANSI256, unicode: true, paste: true, anim: true},
		{name: "tmux hides the terminal behind it: no sync even in kitty", env: map[string]string{"TERM": "tmux-256color", "TMUX": "/tmp/tmux-0/default,1,0", "KITTY_WINDOW_ID": "3", "TERM_PROGRAM": "tmux", "LANG": utf8}, tty: true,
			color: ColorANSI256, unicode: true, paste: true, anim: true},
		{name: "screen", env: map[string]string{"TERM": "screen", "STY": "1.pts-0", "LANG": utf8}, tty: true,
			color: ColorANSI16, unicode: true, paste: true, anim: true},
		{name: "screen-256color", env: map[string]string{"TERM": "screen-256color", "LANG": utf8}, tty: true,
			color: ColorANSI256, unicode: true, paste: true, anim: true},
		{name: "plain xterm is 16 colours", env: map[string]string{"TERM": "xterm", "LANG": utf8}, tty: true,
			color: ColorANSI16, unicode: true, paste: true, anim: true},
		{name: "the Linux console: 16 colours, no Unicode glyphs, no paste", env: map[string]string{"TERM": "linux", "LANG": utf8}, tty: true,
			color: ColorANSI16, unicode: false, paste: false, anim: true},
		{name: "vt100: colour by convention, no bracketed paste", env: map[string]string{"TERM": "vt100"}, tty: true,
			color: ColorANSI16, paste: false, anim: true},
		{name: "unknown TERM, no colour", env: map[string]string{"TERM": "mystery"}, tty: true,
			color: ColorNone, paste: true, anim: true},
		{name: "unknown TERM, COLORTERM says colour", env: map[string]string{"TERM": "mystery", "COLORTERM": "yes"}, tty: true,
			color: ColorANSI16, paste: true, anim: true},

		{name: "NO_COLOR wins over everything", env: map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor", "NO_COLOR": "1", "LANG": utf8}, tty: true,
			color: ColorNone, unicode: true, paste: true, anim: false},
		{name: "NO_COLOR with any non-empty value", env: map[string]string{"TERM": "xterm-256color", "NO_COLOR": "0"}, tty: true,
			color: ColorNone, paste: true, anim: false},
		{name: "an empty NO_COLOR is not set", env: map[string]string{"TERM": "xterm-256color", "NO_COLOR": ""}, tty: true,
			color: ColorANSI256, paste: true, anim: true},
		{name: "SLEIPNIR_ANIM=0", env: map[string]string{"TERM": "xterm-256color", "SLEIPNIR_ANIM": "0"}, tty: true,
			color: ColorANSI256, paste: true, anim: false},
		{name: "SLEIPNIR_ANIM=false", env: map[string]string{"TERM": "xterm-256color", "SLEIPNIR_ANIM": "FALSE"}, tty: true,
			color: ColorANSI256, paste: true, anim: false},
		{name: "SLEIPNIR_ANIM=1 leaves it on", env: map[string]string{"TERM": "xterm-256color", "SLEIPNIR_ANIM": "1"}, tty: true,
			color: ColorANSI256, paste: true, anim: true},
		{name: "REDUCE_MOTION=1", env: map[string]string{"TERM": "xterm-256color", "REDUCE_MOTION": "1"}, tty: true,
			color: ColorANSI256, paste: true, anim: false},
		{name: "REDUCE_MOTION=0 is not a request", env: map[string]string{"TERM": "xterm-256color", "REDUCE_MOTION": "0"}, tty: true,
			color: ColorANSI256, paste: true, anim: true},
		{name: "REDUCE_MOTION=true", env: map[string]string{"TERM": "xterm-256color", "REDUCE_MOTION": "true"}, tty: true,
			color: ColorANSI256, paste: true, anim: false},

		{name: "TERM=dumb on a tty", env: map[string]string{"TERM": "dumb", "LANG": utf8}, tty: true,
			unicode: true, dumb: true},
		{name: "a pipe is plain whatever the environment says", env: map[string]string{"TERM": "xterm-kitty", "COLORTERM": "truecolor", "LANG": utf8}, tty: false,
			unicode: true, dumb: true},
		{name: "a pipe with no environment", env: map[string]string{}, tty: false, dumb: true},
		{name: "a tty with no TERM is dumb", env: map[string]string{"LANG": utf8}, tty: true, unicode: true, dumb: true},
		{name: "Windows Terminal: no TERM, no locale, but a modern terminal", env: map[string]string{"WT_SESSION": "abc"}, tty: true,
			color: ColorANSI16, unicode: true, paste: true, anim: true},
		{name: "Windows Terminal with a locale that says otherwise", env: map[string]string{"WT_SESSION": "abc", "LANG": "C"}, tty: true,
			color: ColorANSI16, unicode: false, paste: true, anim: true},

		{name: "LC_ALL overrides LANG", env: map[string]string{"TERM": "xterm", "LC_ALL": "C", "LANG": utf8}, tty: true,
			color: ColorANSI16, unicode: false, paste: true, anim: true},
		{name: "LC_CTYPE overrides LANG", env: map[string]string{"TERM": "xterm", "LC_CTYPE": "en_US.ISO-8859-1", "LANG": utf8}, tty: true,
			color: ColorANSI16, unicode: false, paste: true, anim: true},
		{name: "utf8 without the dash", env: map[string]string{"TERM": "xterm", "LANG": "C.utf8"}, tty: true,
			color: ColorANSI16, unicode: true, paste: true, anim: true},
		{name: "no locale at all", env: map[string]string{"TERM": "xterm"}, tty: true,
			color: ColorANSI16, unicode: false, paste: true, anim: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := FromEnv(envOf(c.env), c.tty, c.w, c.h)
			want := Caps{Color: c.color, Unicode: c.unicode, SyncOutput: c.sync, BracketedPaste: c.paste, Anim: c.anim, Dumb: c.dumb,
				Width: DefaultWidth, Height: DefaultHeight}
			if got != want {
				t.Errorf("FromEnv(%v, tty=%v)\n got  %+v\n want %+v", c.env, c.tty, got, want)
			}
		})
	}
}

func TestSizeFallbacks(t *testing.T) {
	cases := []struct {
		name         string
		env          map[string]string
		w, h         int
		wantW, wantH int
	}{
		{"measured size wins", map[string]string{"COLUMNS": "200", "LINES": "60"}, 100, 30, 100, 30},
		{"nothing known: the default", map[string]string{}, 0, 0, 80, 24},
		{"COLUMNS and LINES when the terminal says nothing", map[string]string{"COLUMNS": "132", "LINES": "43"}, 0, 0, 132, 43},
		{"each dimension on its own", map[string]string{"COLUMNS": "132", "LINES": "43"}, 100, 0, 100, 43},
		{"garbage in COLUMNS", map[string]string{"COLUMNS": "wide", "LINES": "-3"}, 0, 0, 80, 24},
		{"zero in COLUMNS", map[string]string{"COLUMNS": "0"}, 0, 0, 80, 24},
		{"negative measured size", map[string]string{}, -5, -5, 80, 24},
	}
	for _, c := range cases {
		got := FromEnv(envOf(c.env), true, c.w, c.h)
		if got.Width != c.wantW || got.Height != c.wantH {
			t.Errorf("%s: got %dx%d, want %dx%d", c.name, got.Width, got.Height, c.wantW, c.wantH)
		}
	}
}

// Whatever the environment, the caps are consistent: a plain stream has none of the things that need a terminal, and a size
// is always positive. The environments are drawn from the values that matter, so every combination is covered.
func TestCapsInvariants(t *testing.T) {
	terms := []string{"", "dumb", "xterm", "xterm-256color", "xterm-kitty", "linux", "screen", "tmux-256color", "vt100", "alacritty", "mystery"}
	colorterms := []string{"", "truecolor", "yes"}
	noColors := []string{"", "1"}
	anims := []string{"", "0"}
	for _, tty := range []bool{false, true} {
		for _, tm := range terms {
			for _, ct := range colorterms {
				for _, nc := range noColors {
					for _, an := range anims {
						env := map[string]string{"TERM": tm, "COLORTERM": ct, "NO_COLOR": nc, "SLEIPNIR_ANIM": an, "LANG": "en_US.UTF-8"}
						c := FromEnv(envOf(env), tty, 0, 0)
						if c.Width <= 0 || c.Height <= 0 {
							t.Fatalf("%v: size %dx%d", env, c.Width, c.Height)
						}
						if (!tty || tm == "dumb") && !c.Dumb {
							t.Fatalf("%v tty=%v: not plain: %+v", env, tty, c)
						}
						if c.Dumb && (c.Color != ColorNone || c.Anim || c.SyncOutput || c.BracketedPaste) {
							t.Fatalf("%v tty=%v: a dumb stream claims a capability: %+v", env, tty, c)
						}
						if nc != "" && (c.Color != ColorNone || c.Anim) {
							t.Fatalf("%v: NO_COLOR ignored: %+v", env, c)
						}
						if an == "0" && c.Anim {
							t.Fatalf("%v: SLEIPNIR_ANIM=0 ignored: %+v", env, c)
						}
						if c.SyncOutput && c.Dumb {
							t.Fatalf("%v: sync on a dumb stream", env)
						}
					}
				}
			}
		}
	}
}

func TestNilEnvIsEmpty(t *testing.T) {
	got := FromEnv(nil, true, 10, 5)
	if !got.Dumb || got.Width != 10 || got.Height != 5 {
		t.Errorf("%+v", got)
	}
}

func TestPlain(t *testing.T) {
	full := Caps{Color: ColorTrueColor, Unicode: true, Width: 120, Height: 40, SyncOutput: true, BracketedPaste: true, Anim: true}
	got := full.Plain()
	want := Caps{Unicode: true, Width: 120, Height: 40, Dumb: true}
	if got != want {
		t.Errorf("Plain() = %+v, want %+v", got, want)
	}
	if !full.Anim {
		t.Error("Plain must not change its receiver")
	}
}

func TestColorDepthString(t *testing.T) {
	for d, want := range map[ColorDepth]string{ColorNone: "none", ColorANSI16: "ansi16", ColorANSI256: "ansi256", ColorTrueColor: "truecolor"} {
		if d.String() != want {
			t.Errorf("%d: %q", d, d.String())
		}
	}
}

// Detect on streams that are not terminals: none of these may be an error, and all are plain.
func TestDetectNonTerminals(t *testing.T) {
	env := envOf(map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor", "LANG": "en_US.UTF-8", "COLUMNS": "100", "LINES": "50"})

	if c := Detect(env, nil); !c.Dumb || c.Color != ColorNone || c.Anim || c.Width != 100 || c.Height != 50 || !c.Unicode {
		t.Errorf("nil file: %+v", c)
	}

	regular, err := os.Create(filepath.Join(t.TempDir(), "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer regular.Close()
	if c := Detect(env, regular); !c.Dumb || c.Color != ColorNone || c.Anim || c.SyncOutput {
		t.Errorf("regular file: %+v", c)
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if c := Detect(env, w); !c.Dumb || c.Color != ColorNone || c.Anim || c.SyncOutput || c.BracketedPaste {
		t.Errorf("pipe: %+v", c)
	}
	if c := Detect(nil, w); c.Width != DefaultWidth || c.Height != DefaultHeight {
		t.Errorf("pipe, no environment: %+v", c)
	}
}

func TestGetSizeErrors(t *testing.T) {
	if _, err := GetSize(nil); err == nil {
		t.Error("a nil file has no size")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if s, err := GetSize(w); err == nil || s != (Size{}) {
		t.Errorf("a pipe has no size: %+v, %v", s, err)
	}
}

func TestMakeRawOnANonTerminalIsAnErrorWithASafeRestore(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	restore, err := MakeRaw(r)
	if err == nil {
		t.Fatal("a pipe cannot be put in raw mode")
	}
	if restore == nil {
		t.Fatal("restore must never be nil")
	}
	if err := restore(); err != nil {
		t.Errorf("restore after a failed MakeRaw: %v", err)
	}
	if _, err := MakeRaw(nil); err == nil {
		t.Error("nil file")
	}
}
