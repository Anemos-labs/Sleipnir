package lru

import (
	"reflect"
	"sync"
	"testing"
)

func TestPutGetDelete(t *testing.T) {
	c := New[string, int](2)
	c.Put("a", 1)
	if v, ok := c.Get("a"); !ok || v != 1 {
		t.Fatalf(`Get("a") = %d, %v; want 1, true`, v, ok)
	}
	if v, ok := c.Get("missing"); ok || v != 0 {
		t.Fatalf(`Get("missing") = %d, %v; want 0, false`, v, ok)
	}
	if !c.Delete("a") {
		t.Fatal(`Delete("a") = false, want true`)
	}
	if c.Delete("a") {
		t.Fatal(`second Delete("a") = true, want false`)
	}
	if n := c.Len(); n != 0 {
		t.Fatalf("Len() = %d, want 0", n)
	}
}

// The first example of README.md.
func TestEvictsLeastRecentlyUsed(t *testing.T) {
	c := New[string, int](3)
	c.Put("a", 1)
	c.Put("b", 2)
	c.Put("c", 3)
	c.Get("a")
	c.Put("d", 4)
	if got, want := c.Keys(), []string{"d", "a", "c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Keys() = %v, want %v", got, want)
	}
	if _, ok := c.Get("b"); ok {
		t.Fatal(`"b" should have been evicted`)
	}
}

// The second example of README.md.
func TestOverwriteCountsAsUse(t *testing.T) {
	c := New[string, int](2)
	c.Put("a", 1)
	c.Put("b", 2)
	c.Put("a", 10)
	c.Put("c", 3)
	if got, want := c.Keys(), []string{"c", "a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Keys() = %v, want %v", got, want)
	}
	if v, _ := c.Get("a"); v != 10 {
		t.Fatalf(`Get("a") = %d, want 10`, v)
	}
}

// The third example of README.md.
func TestDeleteFreesRoom(t *testing.T) {
	c := New[string, int](2)
	c.Put("a", 1)
	c.Put("b", 2)
	c.Delete("a")
	c.Put("c", 3)
	if got, want := c.Keys(), []string{"c", "b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Keys() = %v, want %v", got, want)
	}
}

func TestConcurrentSmoke(t *testing.T) {
	c := New[int, int](8)
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				c.Put(i%16, i)
				c.Get((i + 3) % 16)
				c.Len()
			}
		}()
	}
	wg.Wait()
	if n := c.Len(); n > 8 {
		t.Fatalf("Len() = %d, more than the capacity 8", n)
	}
}
