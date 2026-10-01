package shell

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// feedAll pushes chunks through one sanitizer and returns everything emitted.
func feedAll(chunks ...string) (string, int64) {
	var s sanitizer
	var out []byte
	for _, c := range chunks {
		out = s.feed(out, []byte(c))
	}
	out = s.flush(out)
	return string(out), s.ctrl
}

func TestSanitizer(t *testing.T) {
	tests := []struct {
		name   string
		chunks []string
		want   string
	}{
		{"plain", []string{"hello\nworld\t!\r\n"}, "hello\nworld\t!\r\n"},
		{"empty", []string{""}, ""},
		{"sgr colours removed", []string{"\x1b[31mred\x1b[0m ok"}, "red ok"},
		{"sgr with params", []string{"a\x1b[1;38;5;196mb\x1b[m c"}, "ab c"},
		{"escape split across chunks", []string{"x\x1b", "[3", "1mred\x1b[0", "m!"}, "xred!"},
		{"osc title with bel", []string{"a\x1b]0;window title\x07b"}, "ab"},
		{"osc title with st", []string{"a\x1b]0;window title\x1b\\b"}, "ab"},
		{"osc split", []string{"a\x1b]8;;http://x", "\x07link\x1b]8;;\x07"}, "alink"},
		{"charset designation", []string{"a\x1b(Bb"}, "ab"},
		{"two byte escape", []string{"a\x1bcb"}, "ab"},
		{"cursor movement", []string{"a\x1b[2K\x1b[1Gb"}, "ab"},
		{"unterminated csi does not eat text", []string{"a\x1b[3\x01 tail"}, "a tail"},
		{"stray esc before text", []string{"a\x1b\x01b"}, "ab"},
		{"unterminated osc is bounded", []string{"a\x1b]" + strings.Repeat("x", 6000) + "END"}, "a" + strings.Repeat("x", 6000-maxStrLen) + "END"},
		{"nul dropped", []string{"ab\x00cd"}, "abcd"},
		{"controls dropped", []string{"a\x01\x02\x03b\x7fc"}, "abc"},
		{"bel and backspace dropped", []string{"a\x07b\x08c"}, "abc"},
		{"utf8 preserved", []string{"héllo wörld ✓ 日本語 😀"}, "héllo wörld ✓ 日本語 😀"},
		{"utf8 split across chunks", []string{"h\xc3", "\xa9llo \xe2\x9c", "\x93 \xf0\x9f", "\x98", "\x80!"}, "héllo ✓ 😀!"},
		{"invalid utf8 replaced", []string{"caf\xe9!"}, "caf\uFFFD!"},
		{"lone continuation replaced", []string{"a\x80b"}, "a\uFFFDb"},
		{"truncated char at eof", []string{"abc\xe2\x9c"}, "abc\uFFFD"},
		{"tag characters dropped", []string{"a\U000E0041\U000E0042\U000E007Fb"}, "ab"},
		{"bidi overrides dropped", []string{"a\u202eb\u202ac\u2066d\u2069e"}, "abcde"},
		{"zero-width space and word joiner dropped", []string{"a\u200bb\u2060c\ufeffd\u00ade"}, "abcde"},
		{"joiners and marks kept", []string{"a\u200db\u200cc\u200ed\u200fe"}, "a\u200db\u200cc\u200ed\u200fe"},
		{"emoji zwj sequence kept", []string{"\U0001F468\u200d\U0001F469\u200d\U0001F467"}, "\U0001F468\u200d\U0001F469\u200d\U0001F467"},
		{"variation selector 16 kept, supplement dropped", []string{"\u2764\ufe0f\U000E0100"}, "\u2764\ufe0f"},
		{"c1 control dropped", []string{"a\u0085b\u009bc"}, "abc"},
		{"only escape", []string{"\x1b[0m"}, ""},
		{"state survives empty chunk", []string{"a\x1b[3", "", "1mb"}, "ab"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := feedAll(tt.chunks...)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("output %q is not valid UTF-8", got)
			}
		})
	}
}

// Every split of the same byte stream must sanitize identically: that is the
// whole point of carrying state across reads.
func TestSanitizerChunkingInvariance(t *testing.T) {
	input := "start \x1b[1;31mERR\x1b[0m héllo ✓ \x1b]0;title\x07 😀 caf\xe9 \x1b(B\x00end\r\nline2\x1b[2K"
	want, _ := feedAll(input)
	for size := 1; size <= len(input); size++ {
		var chunks []string
		for i := 0; i < len(input); i += size {
			chunks = append(chunks, input[i:min(i+size, len(input))])
		}
		got, _ := feedAll(chunks...)
		if got != want {
			t.Fatalf("chunk size %d: got %q, want %q", size, got, want)
		}
	}
}

func TestInvisible(t *testing.T) {
	for _, r := range []rune{0x00AD, 0x180E, 0x200B, 0x2060, 0x2061, 0x2064, 0xFEFF, 0x202A, 0x202E, 0x2066, 0x2069, 0xE0000, 0xE0041, 0xE007F, 0xE0100, 0xE01EF} {
		if !invisible(r) {
			t.Errorf("U+%04X should be dropped", r)
		}
	}
	for _, r := range []rune{'a', ' ', '\n', 0x00A0, 0x200C, 0x200D, 0x200E, 0x200F, 0xFE0F, 0x2065, 0x2070, 0xE0080, 0xE00FF, 0xE01F0, 0x1F600, 0x65E5, 0x061C} {
		if invisible(r) {
			t.Errorf("U+%04X should be kept", r)
		}
	}
}

func TestSanitizerCountsControlBytes(t *testing.T) {
	_, ctrl := feedAll("a\x00b\x01c\x02d")
	if ctrl != 3 {
		t.Errorf("ctrl = %d, want 3", ctrl)
	}
	_, ctrl = feedAll("a\x1b[31mb\x1b[0m\x07\x08 \t\r\n")
	if ctrl != 0 {
		t.Errorf("benign bytes counted as control: %d", ctrl)
	}
}

func TestCollapseCR(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"no cr", "a\nb\n", "a\nb\n"},
		{"crlf", "a\r\nb\r\n", "a\nb\n"},
		{"progress bar", "10%\r50%\r100%\rdone\n", "done\n"},
		{"overwrite shorter", "Downloading 100%\rDone\n", "Done\n"},
		{"trailing cr keeps line", "foo\r", "foo"},
		{"cr then crlf", "foo\r\r\nbar", "foo\nbar"},
		{"only earlier lines survive", "keep\nold\rnew\nkeep2", "keep\nnew\nkeep2"},
		{"leading cr", "\rabc", "abc"},
		{"empty", "", ""},
		{"lone cr", "\r", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := collapseCR(tt.in); got != tt.want {
				t.Errorf("collapseCR(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// A progress bar with a huge number of redraws after megabytes of earlier
// output must stay linear.
func TestCollapseCRLinear(t *testing.T) {
	in := strings.Repeat("keep this line\n", 200_000) + strings.Repeat("progress 1%\r", 500_000) + "done\n"
	start := time.Now()
	got := collapseCR(in)
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("collapseCR took %v: quadratic behaviour", d)
	}
	if !strings.HasSuffix(got, "keep this line\ndone\n") {
		t.Errorf("tail = %q", got[len(got)-40:])
	}
}

func TestCaptureHeadTail(t *testing.T) {
	// Deterministic content: byte i of the stream is 'a'+i%26.
	stream := func(n int) []byte {
		b := make([]byte, n)
		for i := range b {
			b[i] = byte('a' + i%26)
		}
		return b
	}
	tests := []struct {
		name    string
		total   int   // capture size: head = total/4
		writes  []int // sizes of successive writes
		dropped int64
	}{
		{"fits in head", 100, []int{10}, 0},
		{"exactly head", 100, []int{25}, 0},
		{"head plus small tail", 100, []int{60}, 0},
		{"exactly capacity", 100, []int{100}, 0},
		{"one over", 100, []int{101}, 1},
		{"many small writes", 100, []int{7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7}, 40},
		{"one huge write", 100, []int{10_000}, 9_900},
		{"huge after small", 100, []int{30, 10_000}, 9_930},
		{"wrap boundary", 100, []int{25, 40, 40, 40}, 45},
		{"write equals tail cap", 100, []int{25, 75, 75, 75}, 150},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newCapture(tt.total)
			all := 0
			for _, n := range tt.writes {
				all += n
			}
			data := stream(all)
			off := 0
			for _, n := range tt.writes {
				c.write(data[off : off+n])
				off += n
			}
			if c.dropped != tt.dropped {
				t.Fatalf("dropped = %d, want %d", c.dropped, tt.dropped)
			}
			got := c.text()
			head := tt.total / 4
			if tt.dropped == 0 {
				if got != string(data) {
					t.Fatalf("text = %q, want %q", got, data)
				}
				return
			}
			marker := fmt.Sprintf("\n… [%d bytes of output elided] …\n", tt.dropped)
			want := string(data[:head]) + marker + string(data[all-(tt.total-head):])
			if got != want {
				t.Fatalf("text mismatch:\n got %q\nwant %q", got, want)
			}
		})
	}
}

func TestCaptureMemoryBounded(t *testing.T) {
	c := newCapture(1 << 20)
	chunk := bytes.Repeat([]byte("x"), 64<<10)
	for i := 0; i < 2000; i++ { // 128 MB through a 1 MB capture
		c.write(chunk)
	}
	if len(c.head)+len(c.ring) > 1<<20+16 {
		t.Fatalf("capture holds %d bytes, cap 1 MiB", len(c.head)+len(c.ring))
	}
	if want := int64(2000*64<<10 - 1<<20); c.dropped != want {
		t.Errorf("dropped = %d, want %d", c.dropped, want)
	}
}

func TestRolling(t *testing.T) {
	r := newRolling(100)
	r.write("", []byte("0123456789"))
	data, dropped, next := r.read(0)
	if string(data) != "0123456789" || dropped != 0 || next != 10 {
		t.Fatalf("read(0) = %q,%d,%d", data, dropped, next)
	}
	data, _, next = r.read(4)
	if string(data) != "456789" || next != 10 {
		t.Fatalf("read(4) = %q,%d", data, next)
	}
	// Beyond the end clamps.
	data, dropped, next = r.read(500)
	if len(data) != 0 || dropped != 0 || next != 10 {
		t.Fatalf("read(500) = %q,%d,%d", data, dropped, next)
	}
	// Overflow: 1000 more bytes; only the newest ~100-125 remain.
	big := bytes.Repeat([]byte("z"), 1000)
	r.write("", big)
	if got := r.total(); got != 1010 {
		t.Fatalf("total = %d, want 1010", got)
	}
	data, dropped, next = r.read(0)
	if next != 1010 {
		t.Fatalf("next = %d", next)
	}
	if int64(len(data))+dropped != 1010 {
		t.Fatalf("len(data)+dropped = %d, want 1010", int64(len(data))+dropped)
	}
	if len(data) < 100 || len(data) > 125 {
		t.Fatalf("retained %d bytes, want 100..125", len(data))
	}
	if dropped == 0 {
		t.Fatal("expected dropped > 0")
	}
	// Reading from a live offset after trimming still lines up.
	data, dropped, _ = r.read(1000)
	if string(data) != strings.Repeat("z", 10) || dropped != 0 {
		t.Fatalf("read(1000) = %q,%d", data, dropped)
	}
	// Negative offsets are treated as zero.
	if _, _, n := r.read(-5); n != 1010 {
		t.Fatalf("read(-5) next = %d", n)
	}
}

func TestCommandEnv(t *testing.T) {
	base := []string{
		"PATH=/usr/bin",
		"HOME=/home/u",
		"OPENAI_API_KEY=sk-1",
		"anthropic_api-key=sk-2",
		"MY_SECRET=s",
		"GITHUB_TOKEN=t",
		"DB_PASSWORD=p",
		"DB_PASSWD=p2",
		"AWS_CREDENTIALS=c",
		"Credential_Helper=h",
		"TERM=xterm-256color",
		"NO_COLOR=0",
		"PAGER=less",
		"SLEIPNIR_AGENT=spoofed",
		"PWD=/stale/dir",
		"=C:=C:\\dir",
		"NOEQUALS",
		"MONKEY=banana", // "key" alone is not "api key"
		"KEYBOARD=us",
	}
	tests := []struct {
		name      string
		pass      []string
		present   []string
		absent    []string
		wantForce map[string]string
	}{
		{
			name:    "scrub all",
			present: []string{"PATH=/usr/bin", "HOME=/home/u", "=C:=C:\\dir", "NOEQUALS", "MONKEY=banana", "KEYBOARD=us"},
			absent: []string{"OPENAI_API_KEY", "anthropic_api-key", "MY_SECRET", "GITHUB_TOKEN", "DB_PASSWORD",
				"DB_PASSWD", "AWS_CREDENTIALS", "Credential_Helper", "spoofed", "xterm", "less"},
		},
		{
			name:    "pass exact case-insensitive",
			pass:    []string{"openai_api_key"},
			present: []string{"OPENAI_API_KEY=sk-1"},
			absent:  []string{"MY_SECRET", "GITHUB_TOKEN"},
		},
		{
			name:    "pass wildcard",
			pass:    []string{"GITHUB_*", "  "},
			present: []string{"GITHUB_TOKEN=t"},
			absent:  []string{"OPENAI_API_KEY", "MY_SECRET"},
		},
		{
			name:    "bad pattern is ignored",
			pass:    []string{"[", "MY_SECRET"},
			present: []string{"MY_SECRET=s"},
			absent:  []string{"GITHUB_TOKEN"},
		},
	}
	forced := []string{"TERM=dumb", "NO_COLOR=1", "GIT_TERMINAL_PROMPT=0", "PAGER=cat", "GIT_PAGER=cat", "SLEIPNIR_AGENT=alice", "PWD=/work/dir"}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := commandEnv(base, "alice", "/work/dir", tt.pass)
			joined := "\n" + strings.Join(env, "\n") + "\n"
			for _, want := range append(tt.present, forced...) {
				if !strings.Contains(joined, "\n"+want+"\n") {
					t.Errorf("missing %q in env", want)
				}
			}
			for _, bad := range tt.absent {
				if strings.Contains(joined, bad) {
					t.Errorf("env still contains %q", bad)
				}
			}
			// Forced variables appear exactly once.
			for _, f := range forced {
				name, _, _ := strings.Cut(f, "=")
				if n := strings.Count(joined, "\n"+name+"="); n != 1 {
					t.Errorf("%s appears %d times", name, n)
				}
			}
		})
	}
	if env := commandEnv(nil, "a\x00b", "", nil); !strings.Contains(strings.Join(env, "\n"), "SLEIPNIR_AGENT=ab") {
		t.Errorf("NUL in agent id not stripped: %v", env)
	}
}

func TestShQuote(t *testing.T) {
	for in, want := range map[string]string{
		"":          `''`,
		"plain":     `'plain'`,
		"it's":      `'it'\''s'`,
		"a b;c$d":   `'a b;c$d'`,
		"'":         `''\'''`,
		"new\nline": "'new\nline'",
	} {
		if got := shQuote(in); got != want {
			t.Errorf("shQuote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestParseArgs(t *testing.T) {
	type check func(t *testing.T, a *args)
	tests := []struct {
		name    string
		input   string
		wantErr string
		check   check
	}{
		{"empty input", ``, "", func(t *testing.T, a *args) {
			if _, ok, _ := a.str("x"); ok {
				t.Error("field present in empty input")
			}
		}},
		{"null input", `null`, "", nil},
		{"array", `[1]`, "must be a JSON object", nil},
		{"string", `"ls"`, "must be a JSON object", nil},
		{"number", `42`, "must be a JSON object", nil},
		{"truncated", `{"command":"ec`, "not valid JSON", nil},
		{"garbage", `{{`, "not valid JSON", nil},
		{"null field is absent", `{"x":null}`, "", func(t *testing.T, a *args) {
			if _, ok, err := a.str("x"); ok || err != nil {
				t.Errorf("null treated as present: %v %v", ok, err)
			}
		}},
		{"string field", `{"x":"hi"}`, "", func(t *testing.T, a *args) {
			if s, ok, err := a.str("x"); s != "hi" || !ok || err != nil {
				t.Errorf("got %q %v %v", s, ok, err)
			}
		}},
		{"string field wrong type", `{"x":5}`, "", func(t *testing.T, a *args) {
			if _, _, err := a.str("x"); err == nil || !strings.Contains(err.Error(), `"x" must be a string`) {
				t.Errorf("err = %v", err)
			}
		}},
		{"number forms", `{"a":5,"b":"7.5","c":" 3 ","d":1e2}`, "", func(t *testing.T, a *args) {
			for name, want := range map[string]float64{"a": 5, "b": 7.5, "c": 3, "d": 100} {
				if f, ok, err := a.num(name); f != want || !ok || err != nil {
					t.Errorf("num(%s) = %v %v %v, want %v", name, f, ok, err, want)
				}
			}
		}},
		{"number bad", `{"a":"abc","b":true,"c":[1],"d":{}}`, "", func(t *testing.T, a *args) {
			for _, name := range []string{"a", "b", "c", "d"} {
				if _, _, err := a.num(name); err == nil || !strings.Contains(err.Error(), `"`+name+`"`) {
					t.Errorf("num(%s) err = %v", name, err)
				}
			}
		}},
		{"bool forms", `{"a":true,"b":"TRUE","c":"no","d":1,"e":0,"f":"yes","g":false}`, "", func(t *testing.T, a *args) {
			for name, want := range map[string]bool{"a": true, "b": true, "c": false, "d": true, "e": false, "f": true, "g": false} {
				if b, ok, err := a.boolean(name); b != want || !ok || err != nil {
					t.Errorf("boolean(%s) = %v %v %v, want %v", name, b, ok, err, want)
				}
			}
		}},
		{"bool bad", `{"a":"maybe","b":2,"c":[]}`, "", func(t *testing.T, a *args) {
			for _, name := range []string{"a", "b", "c"} {
				if _, _, err := a.boolean(name); err == nil || !strings.Contains(err.Error(), `"`+name+`"`) {
					t.Errorf("boolean(%s) err = %v", name, err)
				}
			}
		}},
		{"missing names unknown keys", `{"cmd":"ls","zzz":1}`, "", func(t *testing.T, a *args) {
			err := a.missing("command", "command", "timeout")
			if err == nil || !strings.Contains(err.Error(), `"cmd"`) || !strings.Contains(err.Error(), `"zzz"`) || !strings.Contains(err.Error(), `"command"`) {
				t.Errorf("err = %v", err)
			}
		}},
		{"missing without unknown keys", `{}`, "", func(t *testing.T, a *args) {
			if err := a.missing("command", "command"); err == nil || strings.Contains(err.Error(), "unknown") {
				t.Errorf("err = %v", err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, err := parseArgs(json.RawMessage(tt.input))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.check != nil {
				tt.check(t, a)
			}
		})
	}
}

func TestTimeoutSelection(t *testing.T) {
	lim := tools.Limits{DefaultTimeout: 2 * time.Minute, MaxTimeout: 10 * time.Minute}
	tests := []struct {
		name string
		sec  float64
		has  bool
		lim  tools.Limits
		want time.Duration
	}{
		{"default", 0, false, lim, 2 * time.Minute},
		{"explicit", 30, true, lim, 30 * time.Second},
		{"fractional", 0.5, true, lim, 500 * time.Millisecond},
		{"zero means default", 0, true, lim, 2 * time.Minute},
		{"capped", 3600, true, lim, 10 * time.Minute},
		{"absurd", 1e300, true, lim, 10 * time.Minute},
		{"default above max is capped", 0, false, tools.Limits{DefaultTimeout: time.Hour, MaxTimeout: time.Minute}, time.Minute},
		{"unset limits use package defaults", 0, false, tools.Limits{MaxOutputChars: 10}, 2 * time.Minute},
		{"tiny", 0.001, true, lim, time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := foregroundTimeout(tt.sec, tt.has, tt.lim); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
	if backgroundTimeout(0, false) != 0 || backgroundTimeout(-1, true) != 0 {
		t.Error("background jobs must have no deadline unless asked")
	}
	if got := backgroundTimeout(90, true); got != 90*time.Second {
		t.Errorf("background explicit = %v", got)
	}
	if got := backgroundTimeout(1e300, true); got != 24*time.Hour {
		t.Errorf("background absurd = %v", got)
	}
}

func TestFmtSeconds(t *testing.T) {
	for d, want := range map[time.Duration]string{time.Second: "1s", 1500 * time.Millisecond: "1.5s", 2 * time.Minute: "120s", 250 * time.Millisecond: "0.25s"} {
		if got := fmtSeconds(d); got != want {
			t.Errorf("fmtSeconds(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestSummary(t *testing.T) {
	tests := []struct{ desc, cmd, want string }{
		{"list files", "ls -la", "list files"},
		{"", "ls -la", "ls -la"},
		{"", "\n\n  echo hi  \nsecond", "echo hi"},
		{"  \n ", "make test", "make test"},
		{"", strings.Repeat("é", 500), strings.Repeat("é", 200) + "…"},
	}
	for _, tt := range tests {
		if got := summary(tt.desc, tt.cmd); got != tt.want {
			t.Errorf("summary(%q, %q) = %q, want %q", tt.desc, tt.cmd, got, tt.want)
		}
	}
}

func TestSpecs(t *testing.T) {
	reg := tools.NewRegistry()
	Register(reg, NewManager())
	specs, err := reg.Specs()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range specs {
		names = append(names, s.Name)
		if w := len(strings.Fields(s.Description)); w == 0 || w > 120 {
			t.Errorf("%s: description has %d words, want 1..120", s.Name, w)
		}
		var schema struct {
			Type       string                    `json:"type"`
			Properties map[string]map[string]any `json:"properties"`
			Required   []string                  `json:"required"`
		}
		if err := json.Unmarshal(s.InputSchema, &schema); err != nil {
			t.Fatalf("%s: schema: %v", s.Name, err)
		}
		if schema.Type != "object" || len(schema.Required) == 0 {
			t.Errorf("%s: schema = %s", s.Name, s.InputSchema)
		}
		for _, r := range schema.Required {
			if _, ok := schema.Properties[r]; !ok {
				t.Errorf("%s: required %q has no property", s.Name, r)
			}
		}
		if _, err := core.Canonical(s.InputSchema); err != nil {
			t.Errorf("%s: schema is not canonicalisable: %v", s.Name, err)
		}
		if len(s.InputSchema) > 900 {
			t.Errorf("%s: schema is %d bytes; keep schemas minimal", s.Name, len(s.InputSchema))
		}
	}
	want := []string{"bash", "bash_kill", "bash_output"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("tool names = %v, want %v", names, want)
	}
	// The contract names the parameters models are prompted with.
	for _, s := range specs {
		if s.Name == "bash" {
			for _, p := range []string{"command", "timeout", "description", "run_in_background"} {
				if !strings.Contains(string(s.InputSchema), `"`+p+`"`) {
					t.Errorf("bash schema lacks %q", p)
				}
			}
			if s.ReadOnly {
				t.Error("bash must not be marked read-only")
			}
		}
		if s.Name == "bash_output" && !s.ReadOnly {
			t.Error("bash_output should be read-only")
		}
	}
}

func TestOptionsMerge(t *testing.T) {
	m := NewManager(Options{PassEnv: []string{"A"}, KillGrace: time.Second}, Options{PassEnv: []string{"B"}, MaxJobs: 3})
	if got := strings.Join(m.opts.PassEnv, ","); got != "A,B" {
		t.Errorf("PassEnv = %q", got)
	}
	if m.opts.KillGrace != time.Second || m.opts.MaxJobs != 3 {
		t.Errorf("opts = %+v", m.opts)
	}
	d := NewManager().opts
	if d.MaxOutputBytes != 1<<30 || d.CaptureBytes != 8<<20 || d.JobBuffer != 5<<20 || d.KillGrace != 2*time.Second {
		t.Errorf("defaults = %+v", d)
	}
}

func TestWithin(t *testing.T) {
	tests := []struct {
		path, dir string
		want      bool
	}{
		{"/a/b", "/a", true},
		{"/a", "/a", true},
		{"/a/b/c", "/a/b", true},
		{"/ab", "/a", false},
		{"/", "/a", false},
		{"/x/y", "/a", false},
		{"/a/..b", "/a", true}, // a directory literally named "..b" is inside
	}
	for _, tt := range tests {
		if got := within(tt.path, tt.dir); got != tt.want {
			t.Errorf("within(%q, %q) = %v, want %v", tt.path, tt.dir, got, tt.want)
		}
	}
}

func TestLooksSecret(t *testing.T) {
	secret := []string{
		// the spec's pattern
		"OPENAI_API_KEY=x", "anthropic_api-key=x", "MY_SECRET=x", "GITHUB_TOKEN=x", "DB_PASSWORD=x", "DB_PASSWD=x",
		"AWS_CREDENTIALS=x", "GOOGLE_APPLICATION_CREDENTIALS=/k.json", "AWS_SESSION_TOKEN=x",
		// last-word names
		"STRIPE_KEY=x", "HEIMDALL_KEY=x", "KEY=x", "GPG_KEY=x", "PRIVATE_KEY=x", "MYSQL_PWD=x", "GH_PAT=x", "DB_PASS=x",
		"SSH_PASSPHRASE=x", "SENTRY_DSN=x", "HTTP_AUTH=x", "AUTHORIZATION=x", "COOKIE=x", "Set-Cookie=x", "PRIVATEKEY=x",
		"SSH_AUTH_SOCK=/tmp/agent.1",
		// values that give the secret away whatever the name is
		"DATABASE_URL=postgres://app:hunter2@db:5432/prod", "REDIS_URL=redis://:hunter2@cache", "BROKER=amqps://u:p@mq/",
	}
	// Credential-shaped values are assembled from fragments: no literal in this
	// file looks like a real token to a secret scanner.
	rep := strings.Repeat
	for _, v := range []string{
		"-----BEGIN " + "RSA PRIVATE" + " KEY-----",
		"-----BEGIN " + "PRIVATE" + " KEY-----",
		"sk" + "-ant-api03-" + rep("a", 22),
		"sk" + "_live_" + rep("a", 12),
		"gh" + "p_" + rep("a", 36),
		"github" + "_pat_" + rep("A", 40),
		"xox" + "b-1234567890-" + rep("a", 12),
		"AK" + "IA" + rep("A", 16),
		"AI" + "za" + rep("A", 35),
		"ey" + "J" + rep("a", 12) + ".ey" + "J" + rep("a", 12) + "." + rep("b", 10),
	} {
		secret = append(secret, "X="+v)
	}
	plain := []string{
		"PATH=/usr/bin:/bin", "HOME=/home/u", "PWD=/work", "OLDPWD=/work", "SHELL=/bin/bash", "LANG=C.UTF-8", "TERM=xterm",
		"MONKEY=banana", "KEYBOARD=us", "PASSENGERS=3", "COMPASS=north", "OAUTH_CLIENT=x", "DISPLAY=:0", "EDITOR=vim", "TMPDIR=/tmp",
		"GOPATH=/go", "NODE_ENV=production", "CI=true", "AWS_ACCESS_KEY_ID=AKIA-not-a-real-id", "AWS_REGION=us-east-1",
		"GIT_SSH_COMMAND=ssh -i /k", "SSH_KEY_PATH=/k", "ORIGIN=git@github.com:o/r.git", "URL=https://example.com/a@b:c",
		"PROXYISH=1", "JAVA_HOME=/jdk", "SUDO_USER=root", "LS_COLORS=rs=0:di=01;34", "EMPTY=",
		"BASE=http://localhost:8080/path", "ADDR=user@host", "SK=sk-short", "T=eyJ.eyJ.x",
		// proxy settings carry credentials routinely and are needed to reach the network
		"HTTPS_PROXY=http://user:pw@proxy.corp:3128", "http_proxy=http://user:pw@proxy:3128", "NO_PROXY=localhost",
	}
	for _, kv := range secret {
		name, val, _ := strings.Cut(kv, "=")
		if !looksSecret(name, val) {
			t.Errorf("%s should be scrubbed", kv)
		}
	}
	for _, kv := range plain {
		name, val, _ := strings.Cut(kv, "=")
		if looksSecret(name, val) {
			t.Errorf("%s should pass through", kv)
		}
	}
	// PassEnv overrides both name and value rules.
	env := commandEnv([]string{"DATABASE_URL=postgres://a:b@h/d", "X=gh" + "p_" + rep("a", 36)}, "a", "", []string{"database_url"})
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "DATABASE_URL=") || strings.Contains(joined, "X=") {
		t.Errorf("env = %v", env)
	}
}
