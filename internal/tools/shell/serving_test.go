package shell

import (
	"slices"
	"sync"
	"testing"
)

// discardSink is a sink that keeps nothing.
type discardSink struct{}

func (discardSink) write(string, []byte) {}
func (discardSink) detach()              {}

// sniff feeds chunks to a urlSniffer, each on the stream named before it ("out:" or "err:"), and returns the addresses reported.
func sniff(chunks ...string) []string {
	var mu sync.Mutex
	var got []string
	u := newURLSniffer(discardSink{}, func(s string) { mu.Lock(); got = append(got, s); mu.Unlock() })
	for _, c := range chunks {
		stream := "stdout"
		if len(c) > 4 && c[:4] == "err:" {
			stream, c = "stderr", c[4:]
		}
		u.write(stream, []byte(c))
	}
	return got
}

func TestURLSnifferFindsLocalAddresses(t *testing.T) {
	cases := []struct {
		name   string
		chunks []string
		want   []string
	}{
		{"vite", []string{"  VITE v5.4.0  ready in 310 ms\n\n  ➜  Local:   http://localhost:5173/\n  ➜  Network: http://192.168.1.4:5173/\n"}, []string{"http://localhost:5173/"}},
		{"a wildcard host opens as localhost", []string{"Listening on http://0.0.0.0:8080\n"}, []string{"http://localhost:8080/"}},
		{"ipv6 wildcard", []string{"serving at http://[::]:3000/app\n"}, []string{"http://localhost:3000/app"}},
		{"loopback with a path and a full stop", []string{"Open http://127.0.0.1:8000/docs.\n"}, []string{"http://127.0.0.1:8000/docs"}},
		{"split across two reads", []string{"Server running at http://local", "host:4000\n"}, []string{"http://localhost:4000/"}},
		{"an unfinished line is not read yet", []string{"http://localhost:9000"}, nil},
		{"each stream has its own line", []string{"http://local", "err:boom\n", "host:1234\n"}, []string{"http://localhost:1234/"}},
		{"repeats are told once", []string{"http://localhost:5173/\nhttp://localhost:5173/\n"}, []string{"http://localhost:5173/"}},
		{"a public address is not local", []string{"see https://example.com:443/x and http://localhost.example.com/\n"}, nil},
		{"a subdomain of localhost is", []string{"http://app.localhost:8080/\n"}, []string{"http://app.localhost:8080/"}},
		{"at most four", []string{"http://localhost:1/ http://localhost:2/ http://localhost:3/ http://localhost:4/ http://localhost:5/\n"},
			[]string{"http://localhost:1/", "http://localhost:2/", "http://localhost:3/", "http://localhost:4/"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sniff(c.chunks...); !slices.Equal(got, c.want) {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestURLSnifferBoundsTheCarriedLine(t *testing.T) {
	u := newURLSniffer(discardSink{}, func(string) {})
	big := make([]byte, 10*maxSniffLine)
	for i := range big {
		big[i] = 'x'
	}
	u.write("stdout", big)
	if n := len(u.carry["stdout"]); n > maxSniffLine {
		t.Fatalf("carried %d bytes, want at most %d", n, maxSniffLine)
	}
}

func TestStopJobOfUnknownAndNilManager(t *testing.T) {
	var m *Manager
	if err := m.StopJob("job_1"); err == nil {
		t.Error("a nil manager stopped a job")
	}
	if got := m.Jobs(); got != nil {
		t.Errorf("a nil manager lists %v", got)
	}
	m = NewManager()
	defer m.Shutdown()
	if err := m.StopJob("job_9"); err == nil || err.Error() != "no background job job_9" {
		t.Errorf("unknown job: %v", err)
	}
}
