"""Every operation against a brute-force oracle that works on sets of integers."""

import copy
import unittest

from intervals import contains, intersect, merge, subtract

LOW, HIGH = -4, 10  # random ranges start in [LOW, HIGH)


def covered(ranges):
    """The set of integers covered by `ranges`."""
    points = set()
    for start, end in ranges:
        points.update(range(start, end))
    return points


def canonical(points):
    """A set of integers as canonical ranges."""
    result = []
    for x in sorted(points):
        if result and result[-1][1] == x:
            result[-1] = (result[-1][0], x + 1)
        else:
            result.append((x, x + 1))
    return result


class Lcg:
    """A tiny deterministic generator, so the tests do not depend on the random module."""

    def __init__(self, seed):
        self.state = seed

    def below(self, n):
        self.state = (self.state * 6364136223846793005 + 1442695040888963407) % (1 << 64)
        return (self.state >> 33) % n


def random_ranges(rng):
    lengths = [0, 0, 1, 1, 2, 3, 4, 6]
    ranges = []
    for _ in range(rng.below(6)):
        start = LOW + rng.below(HIGH - LOW)
        ranges.append((start, start + lengths[rng.below(len(lengths))]))
    return ranges


class DifferentialTests(unittest.TestCase):
    def test_random_collections(self):
        rng = Lcg(20240607)
        problems = []
        for trial in range(1500):
            a, b = random_ranges(rng), random_ranges(rng)
            before = copy.deepcopy((a, b))
            in_a, in_b = covered(a), covered(b)
            got = {
                "merge": merge(a),
                "subtract": subtract(a, b),
                "intersect": intersect(a, b),
                "contains": [contains(a, x) for x in range(LOW - 2, HIGH + 8)],
            }
            want = {
                "merge": canonical(in_a),
                "subtract": canonical(in_a - in_b),
                "intersect": canonical(in_a & in_b),
                "contains": [x in in_a for x in range(LOW - 2, HIGH + 8)],
            }
            for name in got:
                if got[name] != want[name]:
                    problems.append((name, "a=%r b=%r" % (before[0], before[1]), got[name], want[name]))
            if (a, b) != before:
                problems.append(("an argument was modified", before, (a, b)))
        self.assertEqual(problems[:5], [])

    def test_every_pair_of_small_collections(self):
        singles = [(s, e) for s in range(0, 5) for e in range(s, 5)]
        collections = [[]] + [[r] for r in singles] + [[r, q] for r in singles for q in singles]
        problems = []
        for a in collections:
            in_a = covered(a)
            if merge(a) != canonical(in_a) and len(problems) < 5:
                problems.append(("merge", a, merge(a), canonical(in_a)))
            for b in collections:
                in_b = covered(b)
                got = (subtract(a, b), intersect(a, b))
                want = (canonical(in_a - in_b), canonical(in_a & in_b))
                if got != want and len(problems) < 5:
                    problems.append((a, b, got, want))
        self.assertEqual(problems, [])


if __name__ == "__main__":
    unittest.main()
