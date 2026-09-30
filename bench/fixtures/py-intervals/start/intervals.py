"""Integer interval sets over half-open ranges [start, end). See README.md for the contract."""


def _check(ranges):
    """Return the ranges as a new list of (start, end) tuples; reject reversed ranges."""
    checked = []
    for start, end in ranges:
        if start > end:
            raise ValueError(f"invalid range: ({start}, {end})")
        checked.append((start, end))
    return checked


def merge(ranges):
    """The union of `ranges` as sorted, disjoint, non-touching, non-empty ranges."""
    ranges.sort()
    merged = []
    for start, end in _check(ranges):
        if start == end:
            continue  # an empty range covers nothing
        if merged and start < merged[-1][1]:
            merged[-1] = (merged[-1][0], max(merged[-1][1], end))
        else:
            merged.append((start, end))
    return merged


def subtract(a, b):
    """The integers that are in `a` and not in `b`, in canonical form."""
    cuts = merge(b)
    result = []
    for start, end in _check(a):
        pos = start  # everything in [start, pos) has been dealt with
        for cut_start, cut_end in cuts:
            if cut_end <= pos or cut_start >= end:
                continue  # this cut does not reach what is left of the range
            if cut_start >= pos:
                result.append((pos, cut_start))
            pos = cut_end
        if pos < end:
            result.append((pos, end))
    return result


def intersect(a, b):
    """The integers that are in both `a` and `b`, in canonical form."""
    xs, ys = merge(a), merge(b)
    result = []
    i = j = 0
    while i < len(xs) and j < len(ys):
        lo = max(xs[i][0], ys[j][0])
        hi = min(xs[i][1], ys[j][1])
        if lo <= hi:
            result.append((lo, hi))
        # move on in whichever list starts first
        if xs[i][0] < ys[j][0]:
            i += 1
        else:
            j += 1
    return result


def contains(ranges, x):
    """True if the integer `x` is covered by `ranges`."""
    return any(start <= x <= end for start, end in merge(ranges))
