// Package lru implements a fixed-capacity cache that evicts the entry that has
// gone unused for the longest time.
//
// README.md specifies the behaviour.
package lru

import (
	"container/list"
	"sync"
)

// Cache is a least-recently-used cache. It is shared between goroutines.
type Cache[K comparable, V any] struct {
	mu       sync.Mutex
	capacity int
	order    *list.List          // of *entry[K, V]; front = most recently used, back = least recently used
	index    map[K]*list.Element // key -> its element in order
}

type entry[K comparable, V any] struct {
	key   K
	value V
}

// New returns an empty cache that holds at most capacity entries.
// It panics if capacity is less than 1.
func New[K comparable, V any](capacity int) *Cache[K, V] {
	if capacity < 1 {
		panic("lru: capacity must be at least 1")
	}
	return &Cache[K, V]{
		capacity: capacity,
		order:    list.New(),
		index:    make(map[K]*list.Element),
	}
}

// Get returns the value stored for key and marks the entry as the most recently used.
func (c *Cache[K, V]) Get(key K) (V, bool) {
	el, ok := c.index[key]
	if !ok {
		var zero V
		return zero, false
	}
	c.order.MoveToBack(el)
	return el.Value.(*entry[K, V]).value, true
}

// Put stores value under key and marks the entry as the most recently used.
// If key is new and the cache is full, the least recently used entry is evicted.
func (c *Cache[K, V]) Put(key K, value V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.index[key]; ok {
		el.Value.(*entry[K, V]).value = value
		return
	}
	if c.order.Len() >= c.capacity {
		c.removeOldest()
	}
	c.index[key] = c.order.PushFront(&entry[K, V]{key: key, value: value})
}

// Delete removes key from the cache and reports whether it was there.
func (c *Cache[K, V]) Delete(key K) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.index[key]
	if !ok {
		return false
	}
	c.remove(el)
	return true
}

// Len returns the number of entries in the cache.
func (c *Cache[K, V]) Len() int {
	return c.order.Len()
}

// Keys returns the keys from the most recently used to the least recently used.
func (c *Cache[K, V]) Keys() []K {
	keys := make([]K, 0, c.order.Len())
	for el := c.order.Front(); el != nil; el = el.Next() {
		keys = append(keys, el.Value.(*entry[K, V]).key)
	}
	return keys
}

// removeOldest drops the least recently used entry. The caller holds c.mu.
func (c *Cache[K, V]) removeOldest() {
	if el := c.order.Back(); el != nil {
		c.remove(el)
	}
}

// remove unlinks el from the recency list and the index. The caller holds c.mu.
func (c *Cache[K, V]) remove(el *list.Element) {
	c.order.Remove(el)
	delete(c.index, el.Value.(*entry[K, V]).key)
}
