package traj_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/rl"
	"github.com/anemos-labs/sleipnir/internal/rl/traj"
)

var (
	fuzzOnce  sync.Once
	fuzzSeed  []events.Event
	fuzzBlobs *events.MemBlobs
)

func fuzzSetup() {
	fuzzOnce.Do(func() {
		s := buildSwarm()
		fuzzSeed, fuzzBlobs = s.run.Events(), s.run.Blobs
	})
}

// FuzzEpisode feeds arbitrarily damaged event logs (over the blobs of a valid
// swarm run) to the whole pipeline. Whatever the log looks like, nothing may
// panic, Verify and Episode must return, an episode must marshal, and every step
// of an episode must resolve or fail with an error, never a crash.
func FuzzEpisode(f *testing.F) {
	fuzzSetup()
	var seed bytes.Buffer
	for _, e := range fuzzSeed {
		b, _ := json.Marshal(e)
		seed.Write(b)
		seed.WriteByte('\n')
	}
	f.Add(seed.Bytes())
	f.Add([]byte(`{"seq":1,"type":"model.request","data":{"req":"a.1","manifest":{"model":"m","wire":"x","base":"a.1"}}}` + "\n" + `{"seq":2,"type":"model.response","data":{"req":"a.1"}}`))
	f.Add([]byte(`{"type":"agent.spawn","data":{"id":"x","parent":"x"}}` + "\n" + `{"type":"mail.send","data":{"id":"m","from":"x","to":"x"}}`))
	f.Add([]byte("\n\n{}\n[]\nnull\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		var evs []events.Event
		for _, line := range strings.Split(string(data), "\n") {
			var e events.Event
			if json.Unmarshal([]byte(line), &e) == nil && e.Type != "" {
				evs = append(evs, e)
			}
			if len(evs) >= 3000 {
				break
			}
		}
		r := traj.OpenWith(evs, fuzzBlobs)
		_ = r.Verify()
		ep, err := r.Episode(traj.Options{TaskID: "fz", Policy: rl.PolicyRef{Model: "policy-1"}})
		if err != nil {
			return
		}
		if _, err := json.Marshal(ep); err != nil {
			t.Fatalf("episode does not marshal: %v", err)
		}
		for _, a := range ep.Agents {
			for _, seg := range a.Segments {
				if seg.From > seg.To || seg.To >= len(a.Steps) {
					t.Fatalf("segment %+v of %s out of range (%d steps)", seg, a.ID, len(a.Steps))
				}
			}
			for _, st := range a.Steps {
				_, _ = r.Prompt(st.Prompt.Req)
			}
		}
	})
}
