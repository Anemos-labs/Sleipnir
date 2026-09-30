package events

// The log has two readers of its own lines: Scan, which delivers events to consumers,
// and scanLog, which Open uses to decide where appending resumes and what is damage. A
// line one of them takes for an event and the other for damage makes the log say two
// things at once: consumers see events the writer has discarded (or the reverse), and
// the damage is never recorded where it is reported. These tests pin what they agree
// on, and FuzzLogFile looks for what they do not.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// verdicts says what each reader makes of a log of one good line, the candidate line
// and another good line: whether Scan delivers the candidate, and how many lines Open
// counts as corrupt.
func verdicts(t testing.TB, candidate string) (scanDelivers bool, scanBad, openBad int) {
	t.Helper()
	body := evLine(1, "a") + candidate + "\n" + evLine(3, "c")
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	delivered := 0
	err := Scan(path, func(Event) error { delivered++; return nil })
	var ce *CorruptError
	switch {
	case err == nil:
	case errors.As(err, &ce):
		scanBad = ce.Lines
	default:
		t.Fatalf("Scan: %v", err)
	}
	l, oerr := Open(dir, "s")
	if oerr != nil {
		t.Fatalf("Open: %v", oerr)
	}
	openBad = l.Recovery().CorruptLines
	l.Close()
	return delivered == 3, scanBad, openBad
}

// Lines that are events to both readers, and lines that are damage to both.
func TestScanAndOpenAgreeOnWhatIsDamage(t *testing.T) {
	for _, tc := range []struct {
		name, line string
		valid      bool
	}{
		{"a minimal event", `{"seq":2,"type":"x"}`, true},
		{"an event with every member", `{"seq":2,"ts":"2026-09-30T00:00:00Z","session":"s","agent":"a","type":"x","cause":1,"data":{"a":[1,2]},"v":1}`, true},
		{"a payload of any JSON type", `{"seq":2,"type":"x","data":"text"}`, true},
		{"a missing type", `{"seq":2}`, true},
		{"sequence number zero", `{"seq":0,"type":"x"}`, false},
		{"a missing sequence number", `{"type":"x"}`, false},
		{"a negative sequence number", `{"seq":-2,"type":"x"}`, false},
		{"a fractional sequence number", `{"seq":2.5,"type":"x"}`, false},
		{"a sequence number in exponent form", `{"seq":2e0,"type":"x"}`, false},
		{"a sequence number that is a string", `{"seq":"2","type":"x"}`, false},
		{"a type that is not a string", `{"seq":2,"type":7}`, false},
		{"text that is not JSON", `this is not json`, false},
		{"an array", `[2,"x"]`, false},
		{"a bare number", `2`, false},
		{"an event with text after it", `{"seq":2,"type":"x"} trailing`, false},
		{"two events on one line", `{"seq":2,"type":"x"}{"seq":3,"type":"y"}`, false},
		{"an unterminated string", `{"seq":2,"type":"x`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			delivers, scanBad, openBad := verdicts(t, tc.line)
			if delivers != tc.valid || (scanBad == 0) != tc.valid || (openBad == 0) != tc.valid {
				t.Fatalf("Scan delivers it: %v (skipped %d lines), Open counts %d corrupt lines; want valid=%v", delivers, scanBad, openBad, tc.valid)
			}
		})
	}
}

// Lines the two readers used to disagree about. Scan decoded the whole Event and accepted any sequence number above zero;
// Open read only the sequence number and the type and refused one above 2^53. So a line whose timestamp, cause, version,
// agent or session had the wrong type was damage to Scan and an event to Open (which counted on from its sequence number
// and recorded nothing), and a line with a huge sequence number was an event to Scan and damage to Open: a consumer read
// an event that the log had already discarded, or never heard of damage that it skipped on every read. Both now use
// decodeEvent, which found by the fuzz target below (and reported, with these cases, by the tests that cut a log at every
// byte).
func TestScanAndOpenAgreeOnWhatAnEventIs(t *testing.T) {
	for _, tc := range []struct{ name, line string }{
		{"the largest sequence number", `{"seq":18446744073709551615,"type":"x"}`},
		{"a sequence number just above 2^53", `{"seq":9007199254740993,"type":"x"}`},
		{"a timestamp that is not a time", `{"seq":2,"ts":"yesterday","type":"x"}`},
		{"a negative cause", `{"seq":2,"type":"x","cause":-3}`},
		{"a version that is a string", `{"seq":2,"type":"x","v":"one"}`},
		{"an agent that is a number", `{"seq":2,"type":"x","agent":5}`},
		{"a session that is a list", `{"seq":2,"type":"x","session":["s"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			delivers, scanBad, openBad := verdicts(t, tc.line)
			if (scanBad == 0) != (openBad == 0) || delivers != (openBad == 0) {
				t.Fatalf("Scan delivers it: %v (skipped %d lines), Open counts %d corrupt lines", delivers, scanBad, openBad)
			}
		})
	}
}

// A log that already holds the highest sequence number Open accepts (2^53) used to make Emit write the next one, 2^53+1,
// which Open refuses: the line the log had just been given was damage the next time it was opened. Only a damaged or hostile
// log gets there (nothing emits 2^53 events), but the writer must not produce a line its own reader rejects: Emit stops.
func TestEmitStopsAtTheLastSequenceNumber(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(path, []byte(evLine(1, "a")+fmt.Sprintf(`{"seq":%d,"type":"x"}`+"\n", uint64(maxSeq))), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := Open(dir, "s")
	if err != nil {
		t.Fatal(err)
	}
	seq, err := l.Emit("", "y", nil)
	l.Close()
	if err == nil {
		t.Fatalf("Emit wrote seq %d, above the %d that Open accepts", seq, uint64(maxSeq))
	}
	if b, rerr := os.ReadFile(path); rerr != nil || strings.Count(string(b), "\n") != 2 {
		t.Errorf("the refused event must not reach the file (%v): %q", rerr, b)
	}
}

// FuzzLogFile treats arbitrary bytes as an existing events.jsonl. Whatever they are:
// Scan delivers only events with a sequence number and ends with nil or a *CorruptError
// that counts what it skipped; Open succeeds and continues the sequence above every
// valid event; what Open counts as corrupt, Scan skips; the appended event is the last
// thing Scan delivers; and a second Open has nothing left to report.
func FuzzLogFile(f *testing.F) {
	good := evLine(1, "a") + evLine(2, "b")
	for _, s := range []string{
		"", "\n", "\x00\x00\x00", good, good + `{"seq":3,"ty`, good + `{"seq":3,"type":"c"}`, good + "garbage\n" + evLine(3, "c"),
		good + `{"seq":0,"type":"x"}` + "\n", good + `[1,2,3]` + "\n", good + strings.Repeat("x", 70000) + "\n" + evLine(3, "c"),
		`{"seq":1,"type":"log.corrupt","data":{"corrupt_lines":1}}` + "\n" + good,
		good + `{"seq":18446744073709551615,"type":"x"}` + "\n", good + `{"seq":2,"ts":"yesterday","type":"x"}` + "\n",
		good + `{"seq":9007199254740992,"type":"x"}` + "\n", // the highest sequence number Open accepts
		"\r\n" + good, good + "\r\n", strings.Repeat(good, 20),
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip("large inputs only slow the target down")
		}
		dir := t.TempDir()
		path := filepath.Join(dir, "events.jsonl")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}

		var before []Event
		err := Scan(path, func(e Event) error { before = append(before, e); return nil })
		var ce *CorruptError
		scanBad := 0
		switch {
		case err == nil:
		case errors.As(err, &ce):
			scanBad = ce.Lines
		default:
			t.Fatalf("Scan: %v", err)
		}
		var maxValid uint64
		for _, e := range before {
			if e.Seq == 0 {
				t.Fatalf("Scan delivered an event with seq 0")
			}
			maxValid = max(maxValid, e.Seq)
		}
		l, err := Open(dir, "s")
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		rec := l.Recovery()
		// What Open counts as damage is what Scan skips, except that Scan also counts damage
		// an earlier Open recorded (a log.corrupt event after it), which Open does not.
		recorded := false
		for _, e := range before {
			recorded = recorded || e.Type == TypeLogCorrupt
		}
		if !recorded && rec.CorruptLines != scanBad {
			l.Close()
			t.Fatalf("Open counts %d corrupt lines, Scan skips %d", rec.CorruptLines, scanBad)
		}
		l.SetClock(func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) })
		seq, err := l.Emit("a", "appended", nil)
		if maxValid >= maxSeq { // a log at its last sequence number takes nothing more, and says so
			l.Close()
			if err == nil {
				t.Fatalf("Emit wrote seq %d into a log at its last sequence number", seq)
			}
			return
		}
		if err != nil {
			l.Close()
			t.Fatalf("Emit: %v", err)
		}
		if seq <= maxValid {
			l.Close()
			t.Fatalf("the appended event has seq %d, not above the highest valid one (%d)", seq, maxValid)
		}
		if err := l.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}

		var after []Event
		err = Scan(path, func(e Event) error { after = append(after, e); return nil })
		if err != nil && !errors.Is(err, ErrCorruptLog) {
			t.Fatalf("Scan after the append: %v", err)
		}
		if len(after) == 0 || after[len(after)-1].Seq != seq || after[len(after)-1].Type != "appended" {
			t.Fatalf("the appended event (seq %d) is not the last thing Scan delivers: %v", seq, fmt.Sprint(after))
		}
		l2, err := Open(dir, "s")
		if err != nil {
			t.Fatalf("second Open: %v", err)
		}
		defer l2.Close()
		if rec := l2.Recovery(); rec != (Recovery{}) {
			t.Fatalf("a second Open reports damage again: %+v", rec)
		}
	})
}
