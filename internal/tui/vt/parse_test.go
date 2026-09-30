package vt

import (
	"reflect"
	"strings"
	"testing"
)

// The parser: operating system commands and the strings that are consumed, sequences that are not known, and what interrupts or
// cancels a sequence.

func TestOperatingSystemCommands(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		rows     string
		rejected []string
	}{
		{"title, BEL", "a\x1b]0;a title\x07b", "ab", nil},
		{"title, ST", "a\x1b]2;a title\x1b\\b", "ab", nil},
		{"title, C1 ST", "a\x1b]2;a title\u009cb", "ab", nil},
		{"clipboard, BEL", "a\x1b]52;c;aGVsbG8=\x07b", "ab", []string{"52;c;aGVsbG8="}},
		{"clipboard query", "a\x1b]52;c;?\x07b", "ab", []string{"52;c;?"}},
		{"clipboard, ST", "a\x1b]52;p;AAA\x1b\\b", "ab", []string{"52;p;AAA"}},
		{"hyperlink", "a\x1b]8;;https://x.test/\x07link\x1b]8;;\x07b", "alinkb", []string{"8;;https://x.test/", "8;;"}},
		{"hyperlink with parameters", "\x1b]8;id=1;https://x.test/\x1b\\t\x1b]8;;\x1b\\", "t", []string{"8;id=1;https://x.test/", "8;;"}},
		{"C1 OSC introducer", "a\u009d52;c;AAA\u009cb", "ab", []string{"52;c;AAA"}},
		{"another ESC ends the string and starts a sequence", "a\x1b]52;c;AAA\x1b[2Jb", " b", []string{"52;c;AAA"}}, // the 2J ran, so the b lands after the erased a
		{"CAN ends the string", "a\x1b]52;c;AAA\x18b", "ab", []string{"52;c;AAA"}},
		{"other numbers are consumed", "a\x1b]7;file:///x\x07\x1b]133;A\x07\x1b]1337;File=x\x07b", "ab", nil},
		{"not a number", "a\x1b]zzz;52\x07b", "ab", nil},
		{"no number", "a\x1b]\x07b", "ab", nil},
		{"DCS", "a\x1bP1$r0m\x1b\\b", "ab", nil},
		{"DCS does not end at BEL", "a\x1bPq\x07still inside\x1b\\b", "ab", nil},
		{"APC", "a\x1b_Gi=1;AAAA\x1b\\b", "ab", nil},
		{"PM", "a\x1b^private\x1b\\b", "ab", nil},
		{"SOS", "a\x1bXstring\x1b\\b", "ab", nil},
		{"C1 DCS", "a\u0090q\u009cb", "ab", nil},
		{"C0 controls inside a string are ignored", "a\x1b]0;t\nt\r\x08\x07b", "ab", nil},
	}
	for _, c := range cases {
		v := run(t, 40, 3, c.in)
		if got := v.Rows()[0]; got != c.rows {
			t.Errorf("%s: row %q, want %q", c.name, got, c.rows)
		}
		if !reflect.DeepEqual(v.Rejected, c.rejected) {
			t.Errorf("%s: rejected %q, want %q", c.name, v.Rejected, c.rejected)
		}
		if !v.Idle() {
			t.Errorf("%s: not idle", c.name)
		}
	}
}

func TestUnterminatedStringKeepsTheParserBusy(t *testing.T) {
	v := run(t, 10, 2, "a\x1b]52;c;AAAA")
	if v.Idle() {
		t.Error("an OSC that has not ended is not idle")
	}
	wantRows(t, v, "a")
	v.WriteString("\x07b")
	if !v.Idle() || len(v.Rejected) != 1 {
		t.Errorf("finished by the next write: idle %v rejected %q", v.Idle(), v.Rejected)
	}
	wantRows(t, v, "ab")
}

func TestVeryLongStringIsBounded(t *testing.T) {
	v := New(10, 2)
	v.WriteString("\x1b]52;c;" + strings.Repeat("A", 1<<20) + "\x07z")
	wantRows(t, v, "z")
	if len(v.Rejected) != 1 || len(v.Rejected[0]) > maxString {
		t.Errorf("the record of a long string is bounded: %d entries", len(v.Rejected))
	}
}

func TestUnknownSequencesAreIgnored(t *testing.T) {
	cases := []string{
		"\x1b[?9999h", "\x1b[99;99;99z", "\x1b(B", "\x1b(0", "\x1b)B", "\x1b#8", "\x1b[>c", "\x1b[=c", "\x1b[22;0;0t", "\x1b[>4;2m",
		"\x1b[1;24r", "\x1b[?1h\x1b=", "\x1b>", "\x1b[ q", "\x1b[0 q", "\x1b[!p", "\x1b[?1004h", "\x1b[$p", "\x1b[?2026$p", "\x1b[1;2;3;4;5;6;7;8;9;10;11;12;13;14;15;16;17;18;19;20;21;22;23;24;25;26;27;28;29;30;31;32;33;34;35h",
		"\x1bZ", "\x1bH", "\x1b~", "\x1b\\", "\u0085", "\x1b[<1h", "\x1b[1<h", "\x1b[?1;2$y",
	}
	for _, in := range cases {
		v := run(t, 10, 3, "ab"+in+"cd")
		got := v.Rows()[0]
		// NEL (U+0085) is a control that moves the cursor (to the start of the next row); everything else leaves the text as it was.
		if in == "\u0085" {
			wantRows(t, v, "ab", "cd")
			continue
		}
		if got != "abcd" {
			t.Errorf("%q: row %q", in, got)
		}
		if !v.Idle() {
			t.Errorf("%q: not idle", in)
		}
	}
}

func TestEscapeInterruptsAndCancels(t *testing.T) {
	v := run(t, 20, 2, "a\x1b[12\x1b[2Jb") // ESC inside a CSI abandons it and starts a new one
	wantRows(t, v, " b")
	v = run(t, 20, 2, "a\x1b[12\x18b") // CAN cancels
	wantRows(t, v, "ab")
	v = run(t, 20, 2, "a\x1b[12\x1ab") // SUB cancels
	wantRows(t, v, "ab")
	v = run(t, 20, 2, "a\x1b\x1b[1mb")
	wantRows(t, v, "ab")
	v = run(t, 20, 2, "a\x1b[1\nmb") // a control inside a sequence is executed and the sequence goes on
	wantRows(t, v, "a", " b")
	v = run(t, 20, 2, "a\x1b[1\xc3\xa9mb") // a non-ASCII byte abandons the sequence; the rune is printed
	wantRows(t, v, "aémb")
	v = run(t, 20, 2, "a\x1b[1?2mb") // a private marker after a digit is malformed: ignored through its final byte
	wantRows(t, v, "ab")
	v = run(t, 20, 2, "a\x1b[ 1mb") // a digit after an intermediate is malformed too
	wantRows(t, v, "ab")
}
