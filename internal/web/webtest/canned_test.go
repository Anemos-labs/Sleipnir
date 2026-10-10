package webtest

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// newOfKind returns an empty event of a kind.
func newOfKind(t testing.TB, kind string) wire.Event {
	t.Helper()
	for _, e := range []wire.Event{
		&wire.Say{}, &wire.Sys{}, &wire.Tool{}, &wire.Note{}, &wire.State{}, &wire.Task{}, &wire.Plan{}, &wire.Verdict{}, &wire.Req{}, &wire.Use{}, &wire.Warm{},
		&wire.Gov{}, &wire.Mail{}, &wire.Ckpt{}, &wire.Ask{}, &wire.Answer{}, &wire.Queue{}, &wire.Merge{}, &wire.Break{}, &wire.Compact{}, &wire.Stream{},
		&wire.Diff{}, &wire.Goal{}, &wire.Final{}, &wire.Steer{}, &wire.Interrupt{}, &wire.Refuse{}, &wire.More{}, &wire.Turn{}, &wire.Stall{},
		&wire.Handover{}, &wire.Layers{},
	} {
		if wire.KindOf(e) == kind {
			return e
		}
	}
	t.Fatalf("no event type has the kind %q", kind)
	return nil
}

// strictDecode decodes JSON into v and fails on a field v does not have.
func strictDecode(t testing.TB, data []byte, v any) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		t.Fatalf("%v\n%s", err, data)
	}
}

func TestTheCannedSessionIsInTheContractsVocabulary(t *testing.T) {
	sc := Shop()
	all := append(append([]wire.Event{}, sc.History...), sc.Live...)
	kinds := map[string]bool{}
	var lastT float64
	for i, e := range all {
		b := raw(e)
		// every event decodes into its own type without a field the type lacks
		back := newOfKind(t, wire.KindOf(e))
		strictDecode(t, b, back)
		kinds[wire.KindOf(e)] = true
		h := wire.HeadOf(e)
		if h.K == "" || h.K != wire.KindOf(e) {
			t.Errorf("event %d (%T): kind %q", i, e, h.K)
		}
		if h.T < lastT-1e-9 {
			t.Errorf("event %d (%s): t %.1f goes back from %.1f: t is non-decreasing in seq order", i, h.K, h.T, lastT)
		}
		lastT = h.T
	}
	if lastT <= Now {
		t.Errorf("the continuation ends at %.1f, not after the snapshot at %.1f", lastT, Now)
	}
	if last := wire.HeadOf(sc.History[len(sc.History)-1]).T; last > Now {
		t.Errorf("the history reaches %.1f, past the snapshot time %.1f", last, Now)
	}
	if first := wire.HeadOf(sc.Live[0]).T; first <= Now {
		t.Errorf("the continuation starts at %.1f, not after %.1f", first, Now)
	}
	// What a page must be able to show: the common kinds, in both halves of the session.
	for _, k := range strings.Fields("say tool note state task plan verdict req use warm gov mail ckpt ask queue merge break compact stream diff goal steer refuse more turn stall layers") {
		if !kinds[k] {
			t.Errorf("the canned session has no %q event", k)
		}
	}
	for _, e := range sc.Keyframe {
		if h := wire.HeadOf(e); h.Seq != 0 || h.T != 0 {
			t.Errorf("keyframe event %s has t %v seq %d: keyframe events carry seq 0 at the keyframe's time", h.K, h.T, h.Seq)
		}
	}
}

func TestMessageIdsPointAtTheEventsThatOpenThem(t *testing.T) {
	sc := Shop()
	opened := map[string]bool{}
	for i, e := range append(append([]wire.Event{}, sc.History...), sc.Live...) {
		want := "m" + strconv.Itoa(i+1)
		switch v := e.(type) {
		case *wire.Say:
			if v.Mid != "" {
				if v.Mid != want {
					t.Errorf("seq %d opens message %q, want %q", i+1, v.Mid, want)
				}
				opened[v.Mid] = true
			}
		case *wire.Stream:
			if v.Mid != want {
				t.Errorf("seq %d opens message %q, want %q", i+1, v.Mid, want)
			}
			opened[v.Mid] = true
		case *wire.More:
			if !opened[v.Mid] {
				t.Errorf("seq %d continues %q, which is not open", i+1, v.Mid)
			}
		}
	}
}

func TestQuestionIdsHaveTheShapeOfTheContract(t *testing.T) {
	re := regexp.MustCompile(`^q_[a-z2-7]{26}$`)
	for _, q := range []wire.Question{ShopQuestion(), LaterQuestion()} {
		if !re.MatchString(q.ID) {
			t.Errorf("question id %q: want q_ and 26 base32 characters", q.ID)
		}
	}
}

func TestScenariosAreIndependentValues(t *testing.T) {
	a, b := Shop(), Shop()
	a.History[0].(*wire.Say).Text = "changed"
	if b.History[0].(*wire.Say).Text == "changed" {
		t.Error("two scenarios share events")
	}
	ja, _ := json.Marshal(Shop())
	jb, _ := json.Marshal(Shop())
	if !bytes.Equal(ja, jb) {
		t.Error("the canned session is not deterministic")
	}
}

func TestFrameClassesAreThoseOfTheContract(t *testing.T) {
	var c Classifier
	cases := []struct {
		e           wire.Event
		critical    bool
		coalescable bool
		key         string
	}{
		{&wire.Say{Who: "you"}, true, false, ""},
		{&wire.State{ID: "be-1"}, true, false, ""},
		{&wire.Tool{ID: "be-1"}, true, false, ""},
		{&wire.Ask{}, true, false, ""},
		{&wire.Answer{}, true, false, ""},
		{&wire.Req{ID: "be-1"}, true, false, ""},
		{&wire.Use{ID: "be-1"}, false, true, "use/shop/be-1"},
		{&wire.Layers{ID: "mgr"}, false, true, "layers/shop/mgr"},
		{&wire.Gov{}, false, true, "gov/shop"},
		{&wire.Warm{}, false, true, "warm/shop"},
		{&wire.Plan{}, false, true, "plan/shop"},
		{&wire.Verdict{}, false, true, "verdict/shop"},
		{&wire.Queue{}, false, true, "queue/shop"},
		{&wire.Diff{File: "a.go"}, false, true, "diff/shop/a.go"},
		{&wire.Ckpt{CID: "c01"}, true, false, ""},
		{&wire.Ckpt{CID: "c01", Files: 2}, false, true, "ckpt/shop/c01"},
		{&wire.Ckpt{CID: "c02"}, true, false, ""},
		{&wire.More{Mid: "m1"}, false, false, ""},
	}
	for _, tc := range cases {
		wire.Stamp(tc.e, 1, 1)
		f := c.EvFrame("shop", tc.e)
		if f.Type != "ev" || f.Tab != "shop" || f.Critical != tc.critical || f.Coalescable != tc.coalescable || f.Key != tc.key {
			t.Errorf("%T: %+v, want critical %v coalescable %v key %q", tc.e, f, tc.critical, tc.coalescable, tc.key)
		}
		if f.Critical && f.Coalescable {
			t.Errorf("%T is both", tc.e)
		}
		d, ok := f.Data.(wire.EvFrame)
		if !ok || d.Tab != "shop" || !bytes.Contains(d.Ev, []byte(`"k":"`+wire.KindOf(tc.e)+`"`)) {
			t.Errorf("%T: data %+v", tc.e, f.Data)
		}
	}
	for _, tc := range []struct {
		typ          string
		crit, coal   bool
		wantKeyEmpty bool
	}{{"tab", true, false, true}, {"reset", true, false, true}, {"roster", true, false, true}, {"meta", true, false, true}, {"bye", true, false, true}, {"ping", false, true, false}, {"recorded", false, false, true}, {"run", false, false, true}, {"toast", false, false, true}} {
		f := Frames(tc.typ, "shop", nil)
		if f.Critical != tc.crit || f.Coalescable != tc.coal || (f.Key == "") != tc.wantKeyEmpty {
			t.Errorf("frame %s: %+v", tc.typ, f)
		}
	}
}

func TestEverySlashEntryAndTheSpecAreServedWithModes(t *testing.T) {
	spec := CLISpec()
	if len(spec.Commands) < 50 || len(spec.ChatSlash) < 30 || len(spec.ExitCodes) == 0 {
		t.Fatalf("spec: %d commands, %d slash, %d exit codes", len(spec.Commands), len(spec.ChatSlash), len(spec.ExitCodes))
	}
	want := map[string]string{
		"chat": "tty_only", "watch": "tty_only", "login": "tty_only", "config": "run", "sessions": "run", "doctor": "net", "models": "net", "run": "net",
		"swarm": "net", "mock": "server", "daemon": "server", "rl serve": "server", "trust add": "priv", "schedule add": "priv", "init": "priv",
		"rl taskgen git": "net", "rl report": "run", "sessions prune": "run",
	}
	seen := map[string]string{}
	for _, c := range spec.Commands {
		if c.Mode == "" {
			t.Errorf("%v has no mode", c.Path)
		}
		seen[strings.Join(c.Path, " ")] = c.Mode
		if c.Mode == "tty_only" && c.Why == "" {
			t.Errorf("%v is refused without saying what to use instead", c.Path)
		}
	}
	for path, mode := range want {
		if got, ok := seen[path]; ok && got != mode {
			t.Errorf("%s has mode %q, want %q", path, got, mode)
		}
	}
	custom := 0
	for _, s := range Slash() {
		if s.Custom {
			custom++
		}
	}
	if custom != 2 || len(Slash()) != len(spec.ChatSlash)+2 {
		t.Errorf("slash list: %d entries, %d custom", len(Slash()), custom)
	}
}
