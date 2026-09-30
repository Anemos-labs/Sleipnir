# bitset

`BitSet` is a growable set of `usize` values ("bits"), stored as a bit vector in 64-bit words (`Vec<u64>`). Only the
standard library is used.

## Model

A set has a **capacity**: the number of bits it can hold. Only the values `0..capacity` can be elements. The capacity is
not the number of elements: a set with capacity 1000 may hold one element.

| call | behaviour |
|------|-----------|
| `BitSet::new()` | the empty set, capacity 0 (`BitSet::default()` is the same) |
| `BitSet::with_capacity(n)` | the empty set, capacity exactly `n` |
| `capacity()` | the capacity |
| `insert(bit)` | adds `bit` and returns `true` if it was not in the set yet. If `bit >= capacity`, the capacity first grows to exactly `bit + 1`; otherwise it does not change |
| `remove(bit)` | removes `bit` and returns `true` if it was in the set. Never changes the capacity. Returns `false` when `bit >= capacity` |
| `contains(bit)` | is `bit` in the set? `false` when `bit >= capacity` |
| `len()` | the number of elements |
| `is_empty()` | `true` when there are no elements |
| `resize(n)` | sets the capacity to exactly `n`. Growing adds room and no elements. **Shrinking removes every element `>= n`, for good**: growing the set again later never brings them back, and `len`, `is_empty`, `contains` and `iter` never see them |
| `iter()` | the elements in ascending order (`Iter<'_>` implements `Iterator<Item = usize>`) |
| `union(&other)` | a new set holding the elements of `self` or `other`; its capacity is the larger of the two capacities |
| `intersection(&other)` | a new set holding the elements of both; its capacity is the smaller of the two capacities |
| `difference(&other)` | a new set holding the elements of `self` that are not in `other`; its capacity is that of `self` |
| `is_subset(&other)` | `true` when every element of `self` is in `other`. Capacities do not matter: a set with a huge capacity and few elements can be a subset of a small set. The empty set is a subset of every set, and every set is a subset of itself |

The four set operations take `&self` and `&other`, never change either operand, and work for any pair of sets, whatever
their capacities. `BitSet` is `Clone`, `Debug` and `Default`; it deliberately has no `PartialEq` (compare sets by
collecting `iter()`).

No method panics, except that `insert` of an enormous bit tries to allocate `bit / 8` bytes. The bits at word
boundaries (0, 63, 64, 127, 128, ...) are ordinary bits: nothing is special about them, and everything above must hold
for them as for any other bit.

## Examples

```rust
let mut s = BitSet::new();
assert!(s.insert(5));
assert!(!s.insert(5));            // already there
assert_eq!(s.capacity(), 6);      // grew to 5 + 1
s.insert(64);
assert_eq!(s.capacity(), 65);
assert_eq!(s.iter().collect::<Vec<_>>(), vec![5, 64]);
assert!(!s.contains(65) && !s.remove(1000));
assert_eq!(s.capacity(), 65);     // remove and contains never grow the set

// shrinking, then growing again
let mut t = BitSet::new();
for bit in [3, 64, 100] {
    t.insert(bit);
}
t.resize(64);                     // removes 64 and 100
assert_eq!(t.iter().collect::<Vec<_>>(), vec![3]);
assert_eq!((t.len(), t.capacity()), (1, 64));
t.resize(200);                    // room for more, but nothing comes back
assert_eq!(t.iter().collect::<Vec<_>>(), vec![3]);
assert!(!t.contains(100));

// sets of different capacities
let mut a = BitSet::new();
a.insert(1);
a.insert(2);                      // {1, 2}, capacity 3
let mut b = BitSet::new();
b.insert(70);                     // {70}, capacity 71
assert_eq!(a.union(&b).iter().collect::<Vec<_>>(), vec![1, 2, 70]);
assert_eq!(a.union(&b).capacity(), 71);
assert!(a.intersection(&b).is_empty());
assert_eq!(a.intersection(&b).capacity(), 3);
assert!(!b.is_subset(&a));        // 70 is not in `a`, although `a` is the smaller set
assert!(BitSet::new().is_subset(&a));
```

## Checking your work

```
cargo test --offline --quiet
```

`tests/basics.rs` holds a few checks. The complete set of checks, which covers every row of the table above
(including sets of different capacities, bits at the 64-bit word boundaries, and shrinking followed by growing), is
run when your work is verified.
