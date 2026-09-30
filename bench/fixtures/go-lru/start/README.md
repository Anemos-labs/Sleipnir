# lru

`lru` is a fixed-capacity cache that throws away the entry that has gone unused
for the longest time. One `Cache` is shared by many goroutines.

```go
func New[K comparable, V any](capacity int) *Cache[K, V]

func (c *Cache[K, V]) Get(key K) (V, bool)
func (c *Cache[K, V]) Put(key K, value V)
func (c *Cache[K, V]) Delete(key K) bool
func (c *Cache[K, V]) Len() int
func (c *Cache[K, V]) Keys() []K
```

## Behaviour

- **Capacity.** `New(capacity)` panics if `capacity < 1`. A cache never holds more
  than `capacity` entries.

- **Recency.** The entries are kept in a recency order, from the most recently
  used to the least recently used. *Using* an entry means one of these:
  adding it with `Put`, overwriting its value with `Put`, or reading it with a
  `Get` that finds it. A used entry becomes the most recently used one. Nothing
  else changes the order: a `Get` that finds nothing, `Delete`, `Len` and `Keys`
  are not uses, and removing an entry leaves the order of the others as it was.

- **`Get(key)`** returns the stored value and `true`, or the zero value and
  `false` if `key` is not in the cache.

- **`Put(key, value)`** stores `value` under `key`, replacing any earlier value.
  If `key` is new and the cache is already full, the least recently used entry is
  evicted to make room. Overwriting an existing key never evicts anything.

- **`Delete(key)`** removes the entry and reports whether it was there. The freed
  slot can be used by the next new key without evicting anything.

- **`Len()`** is the number of entries currently held.

- **`Keys()`** returns the keys ordered from the most recently used to the least
  recently used, in a new slice that the caller is free to modify. For an empty
  cache the result has length 0.

- **Concurrency.** Every method may be called from any number of goroutines at the
  same time. Note that `Get` changes the recency order, so it modifies the cache
  even though it looks like a read.

## Examples

Capacity 3 (keys are shown most recently used first):

```go
c := New[string, int](3)
c.Put("a", 1); c.Put("b", 2); c.Put("c", 3) // c b a
c.Get("a")                                  // a c b
c.Put("d", 4)                               // d a c   ("b" was evicted)
```

Overwriting is a use (capacity 2):

```go
c := New[string, int](2)
c.Put("a", 1); c.Put("b", 2) // b a
c.Put("a", 10)               // a b   (no eviction)
c.Put("c", 3)                // c a   ("b" was evicted)
```

Deleting frees a slot (capacity 2):

```go
c := New[string, int](2)
c.Put("a", 1); c.Put("b", 2) // b a
c.Delete("a")                // b
c.Put("c", 3)                // c b   (nothing was evicted)
```

## Checking your work

```
go test -race -timeout 60s ./...
```

`lru_test.go` contains these examples and a small concurrent test. More tests that
check the behaviour above are run when your solution is verified.
