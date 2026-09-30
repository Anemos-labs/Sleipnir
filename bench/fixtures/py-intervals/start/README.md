# intervals

Set operations on ranges of integers.

A **range** is a tuple `(start, end)` of ints. It stands for the integers `x` with
`start <= x < end`: the start is included and the end is not (half-open). `(2, 5)` covers 2, 3
and 4. `(3, 3)` is an **empty range**: it covers nothing. `(5, 3)` is **invalid**.

```python
from intervals import merge, subtract, intersect, contains
```

Run the tests from the repository root with `python3 -m unittest discover -s tests -v`.

## The contract (all four functions)

- **Input.** A collection of ranges is any iterable of ranges (a list, a tuple, a generator) in any
  order: unsorted, overlapping, touching, containing duplicates and empty ranges, or holding no
  range at all.
- **Invalid ranges.** A range with `start > end` anywhere in any argument raises `ValueError`
  whose message starts with `invalid range`, whatever the other arguments are.
- **No side effects.** Arguments are only read. A list you pass in has the same content and the
  same order afterwards.
- **Output.** The result is always a **new list** (never one of the arguments) of `(start, end)`
  tuples in **canonical form**:
  1. sorted by `start`;
  2. no empty ranges;
  3. no two ranges overlap *or touch*: for neighbours `(a, b)` and `(c, d)`, `b < c`.

  Touching ranges such as `(1, 3)` and `(3, 5)` cover the integers 1, 2, 3, 4 without a gap, so in
  canonical form they are the single range `(1, 5)`. Two collections that cover the same integers
  therefore give equal results.

## The functions

### `merge(ranges)`

The canonical form of the union of `ranges`.

```python
merge([(5, 8), (1, 3), (3, 4), (7, 9), (12, 12)])   # [(1, 4), (5, 9)]
merge([(0, 10), (2, 3)])                            # [(0, 10)]
merge([(4, 4)])                                     # []
merge([])                                           # []
```

### `subtract(a, b)`

The integers that are in `a` and not in `b`, in canonical form.

```python
subtract([(0, 10)], [(2, 4), (6, 8)])   # [(0, 2), (4, 6), (8, 10)]
subtract([(0, 10)], [(0, 3)])           # [(3, 10)]
subtract([(0, 5)], [(5, 9)])            # [(0, 5)]   touching: nothing is removed
subtract([(5, 8), (1, 3)], [(2, 6)])    # [(1, 2), (6, 8)]
subtract([(0, 5)], [(0, 5)])            # []
```

### `intersect(a, b)`

The integers that are in both `a` and `b`, in canonical form.

```python
intersect([(0, 5)], [(5, 9)])                # []   touching ranges share no integer
intersect([(0, 10)], [(1, 2), (3, 4)])       # [(1, 2), (3, 4)]
intersect([(0, 4), (6, 10)], [(2, 8)])       # [(2, 4), (6, 8)]
intersect([(0, 5), (3, 8)], [(4, 6)])        # [(4, 6)]
```

### `contains(ranges, x)`

`True` if the int `x` is covered by `ranges`, else `False` (a `bool`).

```python
contains([(0, 5)], 0)    # True   the start is included
contains([(0, 5)], 4)    # True
contains([(0, 5)], 5)    # False  the end is not
contains([(3, 3)], 3)    # False  an empty range covers nothing
```
