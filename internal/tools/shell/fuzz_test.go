package shell

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/tools"
)

// Seed corpora run as ordinary tests; `go test -fuzz FuzzName` explores further.
// The invariants are the ones the rest of the harness relies on: command output
// is arbitrary bytes, and nothing built from it may panic, mangle a character
// depending on where a read happened to end, or let control bytes through.

func FuzzSanitizer(f *testing.F) {
	for _, s := range []string{
		"", "hello\n", "\x1b[31mred\x1b[0m", "\x1b]0;title\x07", "\x1b]8;;http://x\x1b\\link", "h\xc3\xa9llo",
		"\xff\xfe", "\x00\x01\x02", "a\rb\r\nc", "\x1b", "\x1b[", "\x1b[3", "\x1b(B", "😀", "\xf0\x9f\x98",
		"\x1bP+q\x1b\\", "\u0085\u009b", "\x1b]" + strings.Repeat("x", 5000),
	} {
		f.Add([]byte(s), 3)
	}
	f.Fuzz(func(t *testing.T, in []byte, split int) {
		var whole sanitizer
		want := whole.flush(whole.feed(nil, in))
		if !utf8.Valid(want) {
			t.Fatalf("invalid UTF-8 from %q: %q", in, want)
		}
		for _, b := range want {
			if b == 0x1b || b == 0x7f || (b < 0x20 && b != '\n' && b != '\t' && b != '\r') {
				t.Fatalf("control byte %#x survived from %q: %q", b, in, want)
			}
		}
		// Splitting the stream anywhere must not change the result.
		if len(in) > 0 {
			if split < 0 {
				split = -split
			}
			k := split % (len(in) + 1)
			var parts sanitizer
			var got []byte
			got = parts.feed(got, in[:k])
			got = parts.feed(got, in[k:])
			got = parts.flush(got)
			if !bytes.Equal(got, want) {
				t.Fatalf("split at %d of %q:\n got %q\nwant %q", k, in, got, want)
			}
		}
		// The line-oriented pass on top of it is total too.
		out := tidyOutput(string(want))
		if !utf8.ValidString(out) || strings.ContainsRune(out, '\r') {
			t.Fatalf("tidyOutput(%q) = %q", want, out)
		}
	})
}

func FuzzCollapseCR(f *testing.F) {
	for _, s := range []string{"", "a\r\nb", "10%\r50%\rdone\n", "\r", "\r\r\n", "a\rb\rc", "x\r", "\n\r\n"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out := collapseCR(s)
		if strings.ContainsRune(out, '\r') {
			t.Fatalf("collapseCR(%q) = %q still holds a CR", s, out)
		}
		if len(out) > len(s) {
			t.Fatalf("collapseCR grew the text: %q -> %q", s, out)
		}
		if strings.Count(out, "\n") != strings.Count(s, "\n") {
			t.Fatalf("collapseCR changed the number of lines: %q -> %q", s, out)
		}
	})
}

func FuzzCapture(f *testing.F) {
	f.Add([]byte{5, 200, 3, 90, 255, 1}, 40)
	f.Add([]byte{}, 8)
	f.Add([]byte{255, 255, 255}, 100)
	f.Fuzz(func(t *testing.T, sizes []byte, total int) {
		if total < 4 {
			total = 4
		}
		total %= 4096
		if total < 4 {
			total = 4
		}
		c := newCapture(total)
		var all []byte
		n := byte('a')
		for _, sz := range sizes {
			chunk := bytes.Repeat([]byte{n}, int(sz))
			n = 'a' + (n-'a'+1)%26
			c.write(chunk)
			all = append(all, chunk...)
		}
		text := c.text()
		if c.dropped == 0 {
			if text != string(all) {
				t.Fatalf("nothing dropped but text differs: %d vs %d bytes", len(text), len(all))
			}
			return
		}
		if int64(len(all))-c.dropped != int64(c.headCap+c.tailCap) && int64(len(all))-c.dropped != int64(len(c.head)+c.rLen) {
			t.Fatalf("accounting: total=%d dropped=%d retained=%d", len(all), c.dropped, len(c.head)+c.rLen)
		}
		if !strings.HasPrefix(text, string(all[:len(c.head)])) {
			t.Fatal("head not preserved")
		}
		if !strings.HasSuffix(text, string(all[len(all)-c.rLen:])) {
			t.Fatal("tail not preserved")
		}
	})
}

func FuzzRolling(f *testing.F) {
	f.Add([]byte{10, 50, 200}, int64(0), 64)
	f.Add([]byte{255, 255}, int64(100), 10)
	f.Fuzz(func(t *testing.T, sizes []byte, since int64, max int) {
		if max < 1 {
			max = 1
		}
		max %= 512
		if max < 1 {
			max = 1
		}
		r := newRolling(max)
		var total int64
		for _, sz := range sizes {
			r.write("", bytes.Repeat([]byte{'x'}, int(sz)))
			total += int64(sz)
		}
		data, dropped, next := r.read(since)
		if next != total || r.total() != total {
			t.Fatalf("next=%d total=%d want %d", next, r.total(), total)
		}
		if dropped < 0 || int64(len(data)) > total {
			t.Fatalf("read(%d) = %d bytes, dropped %d of %d", since, len(data), dropped, total)
		}
		if since >= 0 && since <= total && dropped+int64(len(data)) != total-since {
			t.Fatalf("read(%d): %d + %d != %d - %d", since, dropped, len(data), total, since)
		}
	})
}

func FuzzArgs(f *testing.F) {
	for _, s := range []string{"", "null", "{}", `{"command":"ls"}`, `{"timeout":"5"}`, `{"timeout":1e999}`, `{"command":`, `[`, `{"run_in_background":"yes"}`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		a, err := parseArgs(json.RawMessage(raw))
		if err != nil {
			return
		}
		a.str("command")
		if v, ok, err := a.num("timeout"); err == nil && ok {
			d := foregroundTimeout(v, true, tools.Limits{})
			if d <= 0 || d > 10*time.Minute {
				t.Fatalf("timeout %v gave %v", v, d)
			}
			if b := backgroundTimeout(v, true); b < 0 || b > 24*time.Hour {
				t.Fatalf("background timeout %v gave %v", v, b)
			}
		}
		a.boolean("run_in_background")
		_ = a.missing("command", "command", "timeout")
	})
}

func FuzzCommandEnvAndQuoting(f *testing.F) {
	for _, s := range []string{"", "A=b", "MY_SECRET=x", "=C:=C:\\", "no equals", "api-key=1", "TERM=x\x00y"} {
		f.Add(s, "agent")
	}
	f.Fuzz(func(t *testing.T, kv, agent string) {
		env := commandEnv([]string{kv}, agent, "/dir", []string{"[", "*", kv})
		joined := strings.Join(env, "\x00")
		for _, want := range []string{"TERM=dumb", "NO_COLOR=1", "PAGER=cat", "PWD=/dir"} {
			if !strings.Contains(joined, want) {
				t.Fatalf("missing %s in %q", want, env)
			}
		}
		for _, e := range env {
			if strings.HasPrefix(e, "SLEIPNIR_AGENT=") && strings.ContainsRune(e, 0) {
				t.Fatalf("NUL in %q", e)
			}
		}
		if q := shQuote(kv); !strings.HasPrefix(q, "'") || !strings.HasSuffix(q, "'") {
			t.Fatalf("shQuote(%q) = %q", kv, q)
		}
		_ = summary(kv, agent)
		_ = wrapScript(kv, agent)
	})
}
