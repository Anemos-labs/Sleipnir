package translate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/tui/state"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// Recorded sessions: a session directory no tab hosts, translated read-only from its log. Replay reads a log once (the Replay view of
// a recorded tab, which opens read-only and plays its log); FollowDir follows a log another process is writing, as `sleipnir watch`
// does. Both read every row from the log, as a followed log is translated, and clean and mask its text exactly as a live session's.

// flushAfter is how far past the last event of a finished log the clock is moved at the end of a Replay, so that what waits on a
// later moment (a state held back by the rate limit, a checkpoint's debounced update, a tool row that waited for its output) is sent.
const flushAfter = 3 * time.Second

// Replay translates a recorded session's log, dir/events.jsonl, into the page's UI events in order. It reads the log once, from one
// goroutine (the caller's), with the clock at each event's time, and returns the journal: when the log translates to more than the
// journal keeps (Config.Limits), the keyframe of what was evicted comes first (its events have no seq), then the retained events.
// t is counted from the first event unless Config.StartedAt says otherwise; Config.Root, when empty, is the root the log's
// session.start names. A log whose last run ended (session.end) is translated to its end; one that is still being written stops at its
// last event, so that what a later call adds to it follows what an earlier one returned. A log larger than 256 MiB is not read: one
// row says so. Damaged lines are skipped, as events.Scan skips them. ctx ends the reading.
func Replay(ctx context.Context, dir string, cfg Config) ([]json.RawMessage, error) {
	path := filepath.Join(dir, "events.jsonl")
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	var clock time.Time
	cfg.Publish = nil
	cfg.Now = func() time.Time { return clock }
	tr := newTranslator(cfg)
	tr.d.logOnly = true
	if fi.Size() > maxHistoryBytes {
		tr.put(&wire.Say{Who: "sys", Glyph: "↺", Text: fmt.Sprintf("↺ this recorded session's log is %d MiB, more than the %d MiB a page replays: sleipnir replay %s plays it in a terminal",
			fi.Size()>>20, maxHistoryBytes>>20, filepath.Base(dir))}, 0, 0, nil)
		return journalOf(tr), nil
	}
	n := 0
	err = events.Scan(path, func(e events.Event) error {
		if n++; n%1024 == 0 && ctx.Err() != nil {
			return ctx.Err()
		}
		if tr.started.IsZero() && !e.TS.IsZero() {
			tr.started = e.TS
		}
		if e.TS.After(clock) {
			clock = e.TS
		}
		tr.applyLog(e)
		tr.tick(clock)
		return nil
	})
	if err != nil && !errors.Is(err, events.ErrCorruptLog) {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if tr.d.ended {
		clock = clock.Add(flushAfter)
		tr.tick(clock)
	}
	return journalOf(tr), nil
}

// journalOf is a translator's keyframe and retained events, in order.
func journalOf(tr *Translator) []json.RawMessage {
	kf, evs, _ := tr.j.snapshot()
	out := make([]json.RawMessage, 0, len(kf)+len(evs))
	for _, r := range kf {
		out = append(out, r)
	}
	for _, r := range evs {
		out = append(out, r)
	}
	return out
}

// Following a recorded session: how often FollowDir looks whether the session ended, and how long a log with nobody holding its
// directory may stay silent before it counts as ended (a writer that was killed writes no session.end).
const (
	followCheck = time.Second
	followQuiet = 30 * time.Second
)

// FollowDir follows the log of a session another process is writing, dir/events.jsonl, and publishes its UI events with cfg.Publish
// (cfg.Tab names the read-only tab) as they are written, until ctx ends (it returns nil) or the session ends: its last run wrote
// session.end, or nobody holds the session directory's lock and the log has been silent for 30 seconds. It returns the error that
// stopped the reading of the log, if one did. Every goroutine it starts has stopped when it returns.
func FollowDir(ctx context.Context, cfg Config, dir string) error {
	tr := New(cfg)
	defer tr.Close()
	done, errp := tr.follow(filepath.Join(dir, "events.jsonl"))
	tick := time.NewTicker(followCheck)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-done:
			return *errp
		case <-tick.C:
			tr.mu.Lock()
			ended, last := tr.d.ended, tr.d.lastWall
			tr.mu.Unlock()
			if session.Locked(dir) {
				continue
			}
			if ended || (!last.IsZero() && time.Since(last) >= followQuiet) {
				return nil
			}
		}
	}
}

// follow starts following a log by polling (see Follow): done is closed when the reading stopped (Close, or an error the reading
// met, which *errp then holds).
func (t *Translator) follow(path string) (done <-chan struct{}, errp *error) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan struct{})
	var ferr error
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		cancel()
		close(ch)
		return ch, &ferr
	}
	t.d.logOnly = true
	t.mu.Unlock()
	go func() {
		defer close(ch)
		err := state.Tail(ctx, path, func(e events.Event) error {
			t.mu.Lock()
			defer t.mu.Unlock()
			if t.closed {
				return errStopScan
			}
			if t.started.IsZero() && !e.TS.IsZero() {
				t.started = e.TS
			}
			t.d.lastWall = time.Now()
			t.applyLog(e)
			t.publishRoster()
			return nil
		}, state.TailOptions{WaitForFile: true})
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, errStopScan) {
			ferr = err
		}
	}()
	stop := func() {
		cancel()
		<-ch
	}
	t.mu.Lock()
	t.d.stopFollow = stop
	t.mu.Unlock()
	return ch, &ferr
}
