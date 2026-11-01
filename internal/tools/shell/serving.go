package shell

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A background job that serves something (a dev server, a docs preview, a mock API) prints where: `Local: http://localhost:5173/`,
// `Listening on http://127.0.0.1:8080`. The sniffer reads a job's sanitized output as it arrives and keeps the first few local
// addresses it names, so the person can open what the agents run without reading logs; the job's event says so once per address.

// maxJobURLs bounds the addresses one job keeps: a server names one or two, and a log that prints hundreds is not serving them.
const maxJobURLs = 4

// maxSniffLine bounds the partial line carried between two reads of one stream; an address is never longer.
const maxSniffLine = 2048

// localURL matches an http(s) address on this machine: localhost (and its subdomains), 127.0.0.1, 0.0.0.0 or [::1]/[::],
// with an optional port and path.
var localURL = regexp.MustCompile(`https?://(?:[A-Za-z0-9-]+\.)*localhost(?::\d{1,5})?(?:/[^\s"'<>()\[\]{}` + "`" + `]*)?|https?://(?:127\.0\.0\.1|0\.0\.0\.0|\[::1?\])(?::\d{1,5})?(?:/[^\s"'<>()\[\]{}` + "`" + `]*)?`)

// urlSniffer is a sink in front of a job's rolling buffer that reports the local addresses the output names.
type urlSniffer struct {
	next  sink
	found func(url string) // called once per new address, outside the lock, at most maxJobURLs times

	mu    sync.Mutex
	carry map[string][]byte // the unfinished last line of each stream
	seen  []string
}

// newURLSniffer wraps next; found is told each new address.
func newURLSniffer(next sink, found func(string)) *urlSniffer {
	return &urlSniffer{next: next, found: found, carry: map[string][]byte{}}
}

// write passes p on and looks for addresses in the complete lines it finishes.
func (u *urlSniffer) write(stream string, p []byte) {
	u.next.write(stream, p)
	var fresh []string
	u.mu.Lock()
	if len(u.seen) < maxJobURLs {
		buf := append(u.carry[stream], p...)
		end := strings.LastIndexAny(string(buf), "\n\r")
		if end >= 0 {
			fresh = u.scanLocked(string(buf[:end]))
			buf = buf[end+1:]
		}
		if len(buf) > maxSniffLine {
			buf = buf[len(buf)-maxSniffLine:]
		}
		u.carry[stream] = append([]byte(nil), buf...)
	}
	u.mu.Unlock()
	for _, s := range fresh {
		u.found(s)
	}
}

// scanLocked returns the addresses in text that were not seen before, normalised, and records them; the caller holds u.mu.
func (u *urlSniffer) scanLocked(text string) []string {
	var fresh []string
	for _, ix := range localURL.FindAllStringIndex(text, -1) {
		if len(u.seen) >= maxJobURLs {
			break
		}
		if hostGoesOn(text, ix[1]) {
			continue // http://localhost.example.com is not this machine
		}
		s := normalURL(text[ix[0]:ix[1]])
		if s == "" || slices.Contains(u.seen, s) {
			continue
		}
		u.seen = append(u.seen, s)
		fresh = append(fresh, s)
	}
	return fresh
}

// hostGoesOn reports whether the text at end continues the host name a match stopped at: a letter, a digit, a hyphen, or a dot
// followed by one of them.
func hostGoesOn(text string, end int) bool {
	word := func(c byte) bool {
		return c == '-' || c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
	}
	if end >= len(text) {
		return false
	}
	if c := text[end]; word(c) {
		return true
	}
	return text[end] == '.' && end+1 < len(text) && word(text[end+1])
}

// detach stops the buffer behind the sniffer from taking more output.
func (u *urlSniffer) detach() { u.next.detach() }

// normalURL is an address as a browser on this machine opens it: a wildcard host (0.0.0.0, [::]) becomes localhost, and the
// punctuation a sentence puts after an address is taken off. An address without a port or a path beyond "/" on a bare host is
// kept: `http://localhost/` is a server too.
func normalURL(s string) string {
	s = strings.TrimRight(s, ".,;:!?'\"")
	for _, wild := range []string{"0.0.0.0", "[::]"} {
		if i := strings.Index(s, "://"+wild); i >= 0 {
			s = s[:i+3] + "localhost" + s[i+3+len(wild):]
		}
	}
	if strings.HasSuffix(s, "://localhost") || strings.Count(s, "/") == 2 {
		s += "/"
	}
	return s
}

// JobInfo is what a person is shown of a background job: who started it, what it runs, whether it still runs, and the local
// addresses its output named.
type JobInfo struct {
	ID      string    `json:"id"`
	Agent   string    `json:"agent"`
	Command string    `json:"command"`
	Status  string    `json:"status"` // "running", "exited" or "killed"
	Exit    *int      `json:"exit,omitempty"`
	Started time.Time `json:"started"`
	URLs    []string  `json:"urls,omitempty"`
}

// Jobs lists the background jobs the Manager remembers, oldest first. A nil Manager has none.
func (m *Manager) Jobs() []JobInfo {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	js := make([]*job, 0, len(m.order))
	for _, id := range m.order {
		if j := m.jobs[id]; j != nil {
			js = append(js, j)
		}
	}
	m.mu.Unlock()
	out := make([]JobInfo, 0, len(js))
	for _, j := range js {
		state, _, exit := j.snapshot()
		ji := JobInfo{ID: j.id, Agent: j.agent, Command: clip(firstLine(j.command), 300), Started: j.started, URLs: j.urlList()}
		switch state {
		case jobRunning:
			ji.Status = "running"
		case jobKilled:
			ji.Status, ji.Exit = "killed", &exit
		default:
			ji.Status, ji.Exit = "exited", &exit
		}
		out = append(out, ji)
	}
	return out
}

// StopJob ends a background job and everything it started, or a stray (Strays) when id names one, for the person (the agents use
// bash_kill). It waits for the job to
// end, at most the kill grace and a few seconds more, and reports an unknown id or a job that outlived that wait.
func (m *Manager) StopJob(id string) error {
	if m == nil {
		return errNoJob(id)
	}
	j := m.job(strings.TrimSpace(id))
	if j == nil {
		if m.stopStray(strings.TrimSpace(id)) {
			return nil
		}
		return errNoJob(id)
	}
	if state, _, _ := j.snapshot(); state != jobRunning {
		if !j.p.pumpsFinished() || probesGroup && j.p.alive(j.p.pid) {
			j.p.killTree() // something the job started outlived it, holding its pipes or not
		}
		return nil
	}
	j.p.requestKill(killRequested)
	wait := time.NewTimer(m.opts.KillGrace + 8*time.Second)
	defer wait.Stop()
	select {
	case <-j.finished:
		return nil
	case <-wait.C:
		return jobError("job " + j.id + " was sent SIGTERM and SIGKILL but has not exited yet")
	}
}

// jobError is an error of StopJob, written for a person.
type jobError string

// Error returns the message.
func (e jobError) Error() string { return string(e) }

// errNoJob is StopJob's error for an id the Manager does not know.
func errNoJob(id string) error {
	id = clip(strings.TrimSpace(id), 40)
	if n, err := strconv.Atoi(strings.TrimPrefix(id, StrayPrefix)); err == nil && n > 0 && strings.HasPrefix(id, StrayPrefix) {
		return jobError("no stray process group " + id + " (it may have ended)")
	}
	return jobError("no background job " + id)
}

// addURL records an address the job's output named and announces it, until the job has maxJobURLs of them.
func (j *job) addURL(u string) {
	j.mu.Lock()
	if len(j.urls) >= maxJobURLs || slices.Contains(j.urls, u) {
		j.mu.Unlock()
		return
	}
	j.urls = append(j.urls, u)
	urls := append([]string(nil), j.urls...)
	j.mu.Unlock()
	j.emitServing(urls)
}

// urlList is a copy of the addresses the job named.
func (j *job) urlList() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]string(nil), j.urls...)
}
