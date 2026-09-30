package web

import (
	"container/list"
	"sync"
	"time"
)

// cache is the in-memory page cache shared by every agent of a session. Agents
// in a swarm tend to read the same documentation pages, and paging through a
// long document must not refetch it once per page, so converted pages are kept
// for a while, keyed by the requested URL alone.
//
// It is an LRU bounded both by entry count and by total size (a handful of
// 10 MB pages must not pin hundreds of megabytes), with a time-to-live so a page
// that changes is eventually seen again. Time is passed in (the tool's Env.Now)
// so expiry is testable without sleeping.
type cache struct {
	mu         sync.Mutex
	ll         *list.List // front = most recently used
	items      map[string]*list.Element
	bytes      int64
	maxEntries int
	maxBytes   int64
	ttl        time.Duration
}

type cacheEntry struct {
	key  string
	doc  *document
	at   time.Time
	size int64
}

func newCache(maxEntries int, maxBytes int64, ttl time.Duration) *cache {
	return &cache{
		ll:         list.New(),
		items:      map[string]*list.Element{},
		maxEntries: maxEntries,
		maxBytes:   maxBytes,
		ttl:        ttl,
	}
}

// get returns a live entry and marks it recently used; expired entries are
// dropped on sight.
func (c *cache) get(key string, now time.Time) (*document, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return nil, false
	}
	e := el.Value.(*cacheEntry)
	if now.Sub(e.at) >= c.ttl {
		c.removeLocked(el)
		return nil, false
	}
	c.ll.MoveToFront(el)
	return e.doc, true
}

// put stores doc. A document larger than the whole size budget is not cached.
func (c *cache) put(key string, doc *document, now time.Time) {
	size := doc.size()
	if size > c.maxBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		c.removeLocked(el)
	}
	c.items[key] = c.ll.PushFront(&cacheEntry{key: key, doc: doc, at: now, size: size})
	c.bytes += size
	for c.ll.Len() > c.maxEntries || c.bytes > c.maxBytes {
		c.removeLocked(c.ll.Back())
	}
}

func (c *cache) removeLocked(el *list.Element) {
	e := el.Value.(*cacheEntry)
	c.ll.Remove(el)
	delete(c.items, e.key)
	c.bytes -= e.size
}

func (c *cache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}
