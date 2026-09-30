package lru

import (
	"math/rand"
	"reflect"
	"sync"
	"testing"
)

// expectKeys checks Keys() and Len() at once; with no arguments it expects an empty cache.
func expectKeys(t *testing.T, c *Cache[string, int], want ...string) {
	t.Helper()
	got := c.Keys()
	if len(got) != len(want) || (len(want) > 0 && !reflect.DeepEqual(got, want)) {
		t.Fatalf("Keys() = %v, want %v", got, want)
	}
	if n := c.Len(); n != len(want) {
		t.Fatalf("Len() = %d, want %d", n, len(want))
	}
}

func TestBehaviourGetRefreshesRecency(t *testing.T) {
	c := New[string, int](3)
	c.Put("a", 1)
	c.Put("b", 2)
	c.Put("c", 3)
	expectKeys(t, c, "c", "b", "a")
	if v, ok := c.Get("b"); !ok || v != 2 {
		t.Fatalf(`Get("b") = %d, %v; want 2, true`, v, ok)
	}
	expectKeys(t, c, "b", "c", "a")
	c.Put("d", 4) // evicts "a"
	expectKeys(t, c, "d", "b", "c")
	if _, ok := c.Get("a"); ok {
		t.Fatal(`"a" should have been evicted`)
	}
	c.Get("d") // already the most recent: nothing changes
	expectKeys(t, c, "d", "b", "c")
	c.Get("c") // the least recent becomes the most recent
	expectKeys(t, c, "c", "d", "b")
}

func TestBehaviourMissingGetChangesNothing(t *testing.T) {
	c := New[string, int](2)
	c.Put("a", 1)
	c.Put("b", 2)
	if v, ok := c.Get("zzz"); ok || v != 0 {
		t.Fatalf(`Get("zzz") = %d, %v; want 0, false`, v, ok)
	}
	expectKeys(t, c, "b", "a")
}

func TestBehaviourOverwrite(t *testing.T) {
	c := New[string, int](2)
	c.Put("a", 1)
	c.Put("b", 2)
	c.Put("a", 10) // a is used again: it becomes the most recent, nothing is evicted
	expectKeys(t, c, "a", "b")
	if v, _ := c.Get("a"); v != 10 {
		t.Fatalf(`Get("a") = %d, want 10`, v)
	}
	c.Put("b", 20)
	expectKeys(t, c, "b", "a")
	c.Put("c", 3) // evicts "a"
	expectKeys(t, c, "c", "b")
	if v, _ := c.Get("b"); v != 20 {
		t.Fatalf(`Get("b") = %d, want 20`, v)
	}
}

func TestBehaviourOverwriteNeverEvicts(t *testing.T) {
	c := New[string, int](2)
	c.Put("a", 1)
	c.Put("b", 2)
	for i := 0; i < 5; i++ {
		c.Put("a", i)
		c.Put("b", i)
	}
	expectKeys(t, c, "b", "a")
}

func TestBehaviourDelete(t *testing.T) {
	c := New[string, int](4)
	for i, k := range []string{"a", "b", "c", "d"} {
		c.Put(k, i)
	}
	expectKeys(t, c, "d", "c", "b", "a")
	if !c.Delete("c") {
		t.Fatal(`Delete("c") = false, want true`)
	}
	if c.Delete("c") {
		t.Fatal(`second Delete("c") = true, want false`)
	}
	if c.Delete("nope") {
		t.Fatal(`Delete("nope") = true, want false`)
	}
	expectKeys(t, c, "d", "b", "a")
	if _, ok := c.Get("c"); ok {
		t.Fatal(`Get("c") found a deleted entry`)
	}
	c.Put("e", 5) // the freed slot is used: nothing is evicted
	expectKeys(t, c, "e", "d", "b", "a")
	c.Put("f", 6) // now the least recently used entry goes
	expectKeys(t, c, "f", "e", "d", "b")
}

func TestBehaviourDeleteThenReinsert(t *testing.T) {
	c := New[string, int](2)
	c.Put("a", 1)
	c.Put("b", 2)
	c.Delete("a")
	c.Put("a", 3) // a again, as a new entry
	expectKeys(t, c, "a", "b")
	c.Put("c", 4) // evicts "b"
	expectKeys(t, c, "c", "a")
	if v, ok := c.Get("a"); !ok || v != 3 {
		t.Fatalf(`Get("a") = %d, %v; want 3, true`, v, ok)
	}
	c.Delete("c")
	expectKeys(t, c, "a")
	c.Delete("a")
	expectKeys(t, c)
}

func TestBehaviourCapacityOne(t *testing.T) {
	c := New[string, int](1)
	c.Put("a", 1)
	c.Put("b", 2) // evicts "a"
	expectKeys(t, c, "b")
	if _, ok := c.Get("a"); ok {
		t.Fatal(`"a" should have been evicted`)
	}
	c.Put("b", 3) // an overwrite evicts nothing
	expectKeys(t, c, "b")
	if v, _ := c.Get("b"); v != 3 {
		t.Fatalf(`Get("b") = %d, want 3`, v)
	}
}

func TestBehaviourNewPanicsOnBadCapacity(t *testing.T) {
	for _, capacity := range []int{0, -1, -100} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("New(%d) did not panic", capacity)
				}
			}()
			New[string, int](capacity)
		}()
	}
}

func TestBehaviourZeroValuesAreStored(t *testing.T) {
	c := New[string, int](2)
	c.Put("zero", 0)
	if v, ok := c.Get("zero"); !ok || v != 0 {
		t.Fatalf(`Get("zero") = %d, %v; want 0, true`, v, ok)
	}
	p := New[int, *int](2)
	p.Put(1, nil)
	if v, ok := p.Get(1); !ok || v != nil {
		t.Fatalf("Get(1) = %v, %v; want nil, true", v, ok)
	}
}

func TestBehaviourKeysIsACopy(t *testing.T) {
	c := New[string, int](3)
	c.Put("a", 1)
	c.Put("b", 2)
	keys := c.Keys()
	keys[0] = "zzz"
	expectKeys(t, c, "b", "a")
	if got := New[string, int](1).Keys(); len(got) != 0 {
		t.Fatalf("Keys() of an empty cache = %v, want length 0", got)
	}
}

// model is a slice-based LRU cache used as the oracle for TestBehaviourMatchesModel.
type model struct {
	capacity int
	keys     []int // most recently used first
	vals     map[int]int
}

func newModel(capacity int) *model { return &model{capacity: capacity, vals: map[int]int{}} }

func (m *model) drop(k int) {
	for i, x := range m.keys {
		if x == k {
			m.keys = append(m.keys[:i:i], m.keys[i+1:]...)
			return
		}
	}
}

func (m *model) use(k int) {
	m.drop(k)
	m.keys = append([]int{k}, m.keys...)
}

func (m *model) put(k, v int) {
	if _, ok := m.vals[k]; !ok && len(m.keys) == m.capacity {
		oldest := m.keys[len(m.keys)-1]
		m.drop(oldest)
		delete(m.vals, oldest)
	}
	m.vals[k] = v
	m.use(k)
}

func (m *model) get(k int) (int, bool) {
	v, ok := m.vals[k]
	if ok {
		m.use(k)
	}
	return v, ok
}

func (m *model) del(k int) bool {
	if _, ok := m.vals[k]; !ok {
		return false
	}
	m.drop(k)
	delete(m.vals, k)
	return true
}

func TestBehaviourMatchesModel(t *testing.T) {
	for _, capacity := range []int{1, 2, 3, 5, 8} {
		rng := rand.New(rand.NewSource(int64(capacity)))
		c := New[int, int](capacity)
		m := newModel(capacity)
		for step := 0; step < 3000; step++ {
			k := rng.Intn(2*capacity + 1)
			switch op := rng.Intn(10); {
			case op < 4:
				c.Put(k, step)
				m.put(k, step)
			case op < 8:
				gv, gok := c.Get(k)
				wv, wok := m.get(k)
				if gv != wv || gok != wok {
					t.Fatalf("capacity %d, step %d: Get(%d) = %d, %v; want %d, %v", capacity, step, k, gv, gok, wv, wok)
				}
			default:
				if g, w := c.Delete(k), m.del(k); g != w {
					t.Fatalf("capacity %d, step %d: Delete(%d) = %v, want %v", capacity, step, k, g, w)
				}
			}
			got := c.Keys()
			if len(got) != len(m.keys) || (len(got) > 0 && !reflect.DeepEqual(got, m.keys)) {
				t.Fatalf("capacity %d, step %d: Keys() = %v, want %v", capacity, step, got, m.keys)
			}
			if n := c.Len(); n != len(m.keys) {
				t.Fatalf("capacity %d, step %d: Len() = %d, want %d", capacity, step, n, len(m.keys))
			}
		}
	}
}

func TestBehaviourConcurrentUse(t *testing.T) {
	const (
		workers  = 8
		ops      = 4000
		capacity = 32
	)
	c := New[int, int](capacity)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(w)))
			for i := 0; i < ops; i++ {
				k := rng.Intn(64)
				switch rng.Intn(6) {
				case 0, 1:
					c.Put(k, i)
				case 2, 3:
					c.Get(k)
				case 4:
					c.Delete(k)
				default:
					if n := c.Len(); n > capacity {
						t.Errorf("Len() = %d, more than the capacity %d", n, capacity)
					}
					_ = c.Keys()
				}
			}
		}(w)
	}
	wg.Wait()

	keys := c.Keys()
	if len(keys) != c.Len() || len(keys) > capacity {
		t.Fatalf("after the run: %d keys, Len() = %d, capacity %d", len(keys), c.Len(), capacity)
	}
	seen := make(map[int]bool)
	for _, k := range keys {
		if seen[k] {
			t.Errorf("key %d is listed twice by Keys()", k)
		}
		seen[k] = true
		if _, ok := c.Get(k); !ok {
			t.Errorf("key %d is listed by Keys() but Get does not find it", k)
		}
	}
}
