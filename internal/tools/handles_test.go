package tools

import (
	"fmt"
	"math/rand"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/events"
)

// collide returns a hash that shares its first n hex digits with base and differs after that.
func collide(base core.Hash, n int, tag string) core.Hash {
	rest := core.HashString(tag + string(base))
	return core.Hash(string(base)[:n] + string(rest)[:len(base)-n])
}

var handleShape = regexp.MustCompile(`^out_[0-9a-f]{16,64}$`)

func TestHandleIsSixtyFourBitsOfTheHash(t *testing.T) {
	h := NewHandles()
	ref := core.HashString("some command output")
	id := h.Add(ref, 42)
	if want := "out_" + string(ref)[:16]; id != want {
		t.Fatalf("handle = %q, want %q", id, want)
	}
	if !handleShape.MatchString(id) {
		t.Fatalf("handle %q does not have the documented shape", id)
	}
	if got, n, ok := h.Resolve(id); !ok || got != ref || n != 42 {
		t.Fatalf("Resolve(%q) = %v %d %v", id, got, n, ok)
	}
}

func TestAddingTheSameBlobAgainReturnsTheSameHandle(t *testing.T) {
	h := NewHandles()
	ref := core.HashString("x")
	first := h.Add(ref, 1)
	for i := 0; i < 5; i++ {
		if again := h.Add(ref, 1); again != first {
			t.Fatalf("Add #%d returned %q, first was %q", i+2, again, first)
		}
	}
	if len(h.m) != 1 {
		t.Fatalf("table has %d entries for one blob", len(h.m))
	}
}

// The core of S32: a different blob with the same 64-bit prefix must not take over an issued handle.
func TestACollidingNewcomerGetsALongerHandleAndTheOwnerKeepsIts(t *testing.T) {
	h := NewHandles()
	a := core.HashString("output of the real test run")
	b := collide(a, 16, "b") // shares all 16 digits of a's handle
	c := collide(b, 20, "c") // shares 20 digits with b, so it meets a's handle and then b's
	d := collide(c, 24, "d") // shares 24 with c: it meets a's, b's and c's
	e := collide(a, 28, "e") // shares 28 with a, but nobody holds a's 20-digit form: it gets that
	x := core.HashString("unrelated")

	ida := h.Add(a, 10)
	idb := h.Add(b, 20)
	idc := h.Add(c, 30)
	idd := h.Add(d, 40)
	ide := h.Add(e, 50)
	idx := h.Add(x, 60)

	for _, w := range []struct {
		name, got, want string
	}{
		{"a (first)", ida, "out_" + string(a)[:16]},
		{"b (collides at 16)", idb, "out_" + string(b)[:20]},
		{"c (collides at 16 and 20)", idc, "out_" + string(c)[:24]},
		{"d (collides at 16, 20 and 24)", idd, "out_" + string(d)[:28]},
		{"e (collides at 16 only)", ide, "out_" + string(e)[:20]},
		{"x (unrelated)", idx, "out_" + string(x)[:16]},
	} {
		if w.got != w.want {
			t.Errorf("%s: handle %q, want %q", w.name, w.got, w.want)
		}
	}
	seen := map[string]bool{}
	for _, id := range []string{ida, idb, idc, idd, ide, idx} {
		if seen[id] {
			t.Fatalf("handle %q was issued twice", id)
		}
		seen[id] = true
		if !handleShape.MatchString(id) {
			t.Errorf("handle %q has an odd shape", id)
		}
	}
	for _, w := range []struct {
		id  string
		ref core.Hash
		n   int
	}{{ida, a, 10}, {idb, b, 20}, {idc, c, 30}, {idd, d, 40}, {ide, e, 50}, {idx, x, 60}} {
		if got, n, ok := h.Resolve(w.id); !ok || got != w.ref || n != w.n {
			t.Errorf("Resolve(%q) = %s %d %v, want %s %d", w.id, got.Short(), n, ok, w.ref.Short(), w.n)
		}
	}
	// Re-adding an extended newcomer finds its own handle again instead of growing another.
	for _, w := range []struct {
		ref core.Hash
		id  string
	}{{a, ida}, {b, idb}, {c, idc}, {d, idd}, {e, ide}, {x, idx}} {
		if again := h.Add(w.ref, 1); again != w.id {
			t.Errorf("re-adding %s gave %q, want %q", w.ref.Short(), again, w.id)
		}
	}
	if len(h.m) != 6 {
		t.Errorf("table has %d entries, want 6", len(h.m))
	}
}

// Ids depend only on the hashes and the order of the Add calls: a replayed session mints the same ones.
func TestHandleAssignmentIsDeterministic(t *testing.T) {
	base := core.HashString("base")
	seq := []core.Hash{base, collide(base, 16, "1"), collide(base, 24, "2"), core.HashString("other"), collide(base, 16, "3"), base}
	run := func() []string {
		h := NewHandles()
		var ids []string
		for i, r := range seq {
			ids = append(ids, h.Add(r, i))
		}
		return ids
	}
	x, y := run(), run()
	if fmt.Sprint(x) != fmt.Sprint(y) {
		t.Fatalf("two identical sessions minted different handles:\n%v\n%v", x, y)
	}
}

// A hostile agent that ground many blobs onto one prefix cannot make any of them share a handle.
func TestManyBlobsOnOnePrefixAllGetTheirOwnHandle(t *testing.T) {
	h := NewHandles()
	base := core.HashString("victim")
	const n = 500
	refs := make([]core.Hash, n)
	ids := make([]string, n)
	for i := range refs {
		refs[i] = collide(base, 16, fmt.Sprintf("grind-%d", i))
		ids[i] = h.Add(refs[i], i)
	}
	victim := h.Add(base, -1)
	seen := map[string]int{}
	for i, id := range append(ids, victim) {
		if j, dup := seen[id]; dup {
			t.Fatalf("blobs %d and %d share handle %q", j, i, id)
		}
		seen[id] = i
	}
	for i, id := range ids {
		if got, k, ok := h.Resolve(id); !ok || got != refs[i] || k != i {
			t.Fatalf("handle %q resolves to %s (%d), want blob %d", id, got.Short(), k, i)
		}
	}
	if got, k, ok := h.Resolve(victim); !ok || got != base || k != -1 {
		t.Fatalf("the victim's handle resolves to %s (%d)", got.Short(), k)
	}
}

// The width is not something the table may lean on: a random workload of realistic size never repeats a handle.
func TestRandomWorkloadNeverAliases(t *testing.T) {
	h := NewHandles()
	rng := rand.New(rand.NewSource(7))
	const n = 20_000
	ids := make(map[string]core.Hash, n)
	for i := 0; i < n; i++ {
		ref := core.HashString(fmt.Sprintf("output %d %d", i, rng.Int()))
		id := h.Add(ref, i)
		if other, dup := ids[id]; dup && other != ref {
			t.Fatalf("handle %q issued for two blobs", id)
		}
		ids[id] = ref
	}
	if len(ids) != n {
		t.Fatalf("%d distinct handles for %d blobs", len(ids), n)
	}
}

func TestOddHashesDoNotPanicOrOverwrite(t *testing.T) {
	h := NewHandles()
	odd := []core.Hash{"", "a", "abc", "0123456789abcde", "0123456789abcdef", "0123456789abcdef0", "0123456789abcdef01", "0123456789abcdef0123", core.Hash(strings.Repeat("f", 200))}
	ids := map[string]core.Hash{}
	for i, ref := range odd {
		id := h.Add(ref, i)
		if prev, dup := ids[id]; dup {
			t.Fatalf("handle %q issued for %q and %q", id, prev, ref)
		}
		ids[id] = ref
		if got, _, ok := h.Resolve(id); !ok || got != ref {
			t.Fatalf("Resolve(%q) = %q, want %q", id, got, ref)
		}
	}
	// Adding them all again changes nothing.
	for i, ref := range odd {
		id := h.Add(ref, i)
		if ids[id] != ref {
			t.Fatalf("re-adding %q returned %q which belongs to %q", ref, id, ids[id])
		}
	}
}

// Prefixes of one another (which no real hash pair is) exercise the numbered fallback.
func TestHashesThatArePrefixesOfEachOtherStayDistinct(t *testing.T) {
	h := NewHandles()
	short := core.Hash("0123456789abcdef")
	long := core.Hash("0123456789abcdef0123")
	longer := core.Hash("0123456789abcdef01234567")
	seen := map[string]core.Hash{}
	for _, ref := range []core.Hash{short, long, longer, short, long, longer} {
		id := h.Add(ref, 1)
		if prev, ok := seen[id]; ok && prev != ref {
			t.Fatalf("handle %q shared by %q and %q", id, prev, ref)
		}
		seen[id] = ref
	}
	if len(seen) != 3 {
		t.Fatalf("%d handles for 3 blobs: %v", len(seen), seen)
	}
}

func TestResolve(t *testing.T) {
	h := NewHandles()
	ref := core.HashString("x")
	id := h.Add(ref, 5)
	for in, want := range map[string]bool{
		id:                                     true,
		"  " + id + "\n":                       true,
		"":                                     false,
		"out_":                                 false,
		id[:len(id)-1]:                         false, // no prefix matching: a truncated handle is unknown, never someone else's
		id + "0":                               false,
		strings.ToUpper(id):                    false,
		string(ref):                            false,
		"out_" + string(ref):                   false,
		"out_0000000000000000":                 false,
		strings.Replace(id, "out_", "out-", 1): false,
	} {
		if _, _, ok := h.Resolve(in); ok != want {
			t.Errorf("Resolve(%q) ok = %v, want %v", in, ok, want)
		}
	}
}

// Many agents add overlapping, colliding blobs at once: every blob ends up with exactly one handle,
// no two blobs share one, and every handle resolves to its blob. Run with -race.
func TestConcurrentAddsWithCollisions(t *testing.T) {
	h := NewHandles()
	base := core.HashString("concurrent")
	refs := make([]core.Hash, 64)
	for i := range refs {
		if i%2 == 0 {
			refs[i] = collide(base, 16, fmt.Sprint("c", i))
		} else {
			refs[i] = core.HashString(fmt.Sprint("plain ", i))
		}
	}
	const workers = 16
	got := make([]map[core.Hash]string, workers)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(w)))
			mine := map[core.Hash]string{}
			for _, i := range rng.Perm(len(refs)) {
				mine[refs[i]] = h.Add(refs[i], i)
				h.Resolve(mine[refs[i]])
			}
			got[w] = mine
		}()
	}
	wg.Wait()
	owner := map[string]core.Hash{}
	for _, ref := range refs {
		id := got[0][ref]
		for w := 1; w < workers; w++ {
			if got[w][ref] != id {
				t.Fatalf("workers disagree on the handle of one blob: %q vs %q", id, got[w][ref])
			}
		}
		if prev, dup := owner[id]; dup {
			t.Fatalf("blobs %s and %s share handle %q", prev.Short(), ref.Short(), id)
		}
		owner[id] = ref
		if r, _, ok := h.Resolve(id); !ok || r != ref {
			t.Fatalf("handle %q does not resolve to its blob", id)
		}
	}
}

// End to end through Env.Finish: the handle in the result text is the one Resolve knows, even when
// two outputs are as alike as blobs can be.
func TestFinishIssuesResolvableHandles(t *testing.T) {
	env := (&Env{Blobs: events.NewMemBlobs(), Limits: Limits{MaxOutputChars: 100}}).Defaults()
	var results []*Result
	for i := 0; i < 3; i++ {
		text := strings.Repeat(fmt.Sprintf("line %d of a long command output\n", i), 50)
		res := env.Finish(text, false)
		if !res.Truncated || res.Handle == "" {
			t.Fatalf("output %d was not truncated with a handle: %+v", i, res)
		}
		if !handleShape.MatchString(res.Handle) {
			t.Fatalf("handle %q has an odd shape", res.Handle)
		}
		if !strings.Contains(res.Text, res.Handle) {
			t.Fatalf("the model-visible text does not name the handle %q:\n%s", res.Handle, res.Text)
		}
		results = append(results, res)
	}
	for i, res := range results {
		ref, n, ok := env.Handles.Resolve(res.Handle)
		if !ok || ref != res.FullRef {
			t.Fatalf("result %d: %q resolves to %q (ok=%v), want %q", i, res.Handle, ref, ok, res.FullRef)
		}
		if b, err := env.Blobs.Get(ref); err != nil || len(b) != n {
			t.Fatalf("result %d: blob has %d bytes (err %v), handle says %d", i, len(b), err, n)
		}
	}
	if results[0].Handle == results[1].Handle {
		t.Fatalf("two different outputs share a handle")
	}
}
