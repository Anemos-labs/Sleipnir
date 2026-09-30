import copy
import unittest

from intervals import contains, intersect, merge, subtract


class InvalidRangeTests(unittest.TestCase):
    def assertInvalid(self, call, *args):
        with self.assertRaises(ValueError) as caught:
            call(*args)
        self.assertTrue(str(caught.exception).startswith("invalid range"), str(caught.exception))

    def test_merge(self):
        self.assertInvalid(merge, [(5, 3)])
        self.assertInvalid(merge, [(1, 2), (9, 4), (6, 7)])

    def test_subtract(self):
        self.assertInvalid(subtract, [(0, 5)], [(4, 2)])
        self.assertInvalid(subtract, [(4, 2)], [(0, 5)])
        self.assertInvalid(subtract, [(4, 2)], [])
        self.assertInvalid(subtract, [], [(4, 2)])

    def test_intersect(self):
        self.assertInvalid(intersect, [(1, 2)], [(3, 1)])
        self.assertInvalid(intersect, [(3, 1)], [(1, 2)])
        self.assertInvalid(intersect, [(3, 1)], [])
        self.assertInvalid(intersect, [], [(3, 1)])

    def test_contains(self):
        self.assertInvalid(contains, [(3, 1)], 2)
        self.assertInvalid(contains, [(0, 5), (3, 1)], 2)

    def test_empty_ranges_are_not_invalid(self):
        self.assertEqual(merge([(3, 3)]), [])
        self.assertEqual(subtract([(3, 3)], [(4, 4)]), [])
        self.assertEqual(intersect([(3, 3)], [(3, 3)]), [])
        self.assertIs(contains([(3, 3)], 3), False)


class NoSideEffectTests(unittest.TestCase):
    COLLECTIONS = [
        [(5, 8), (1, 3), (3, 4), (7, 9), (12, 12)],  # unsorted, touching, overlapping, empty
        [(1, 2), (3, 4)],                            # already canonical
        [(9, 10), (0, 1)],                           # only unsorted
        [(2, 2)],
        [],
        [(0, 10), (2, 3), (0, 10)],
    ]

    def test_arguments_are_left_alone(self):
        for ranges in self.COLLECTIONS:
            for other in self.COLLECTIONS:
                with self.subTest(ranges=ranges, other=other):
                    a, b = copy.deepcopy(ranges), copy.deepcopy(other)
                    merge(a)
                    subtract(a, b)
                    intersect(a, b)
                    contains(a, 3)
                    self.assertEqual(a, ranges)
                    self.assertEqual(b, other)

    def test_the_same_list_passed_twice(self):
        a = [(5, 8), (1, 3)]
        self.assertEqual(subtract(a, a), [])
        self.assertEqual(intersect(a, a), [(1, 3), (5, 8)])
        self.assertEqual(a, [(5, 8), (1, 3)])


class ResultTests(unittest.TestCase):
    def test_results_are_lists_of_tuples(self):
        a = [(1, 2), (4, 5)]
        b = [(4, 5), (8, 9)]
        for result in (merge(a), subtract(a, []), subtract(a, b), intersect(a, a), intersect(a, b)):
            self.assertIs(type(result), list)
            for item in result:
                self.assertIs(type(item), tuple)

    def test_results_are_new_lists(self):
        a = [(1, 2), (4, 5)]
        b = [(1, 2), (4, 5)]
        for result in (merge(a), subtract(a, []), subtract(a, [(0, 0)]), intersect(a, b)):
            self.assertEqual(result, [(1, 2), (4, 5)])
            self.assertIsNot(result, a)
            self.assertIsNot(result, b)
            result.append((9, 9))
        self.assertEqual(a, [(1, 2), (4, 5)])
        self.assertEqual(b, [(1, 2), (4, 5)])


class InputKindTests(unittest.TestCase):
    DATA = [(5, 8), (1, 3), (3, 4)]

    def test_any_iterable_of_ranges_is_accepted(self):
        data = self.DATA
        self.assertEqual(merge(tuple(data)), [(1, 4), (5, 8)])
        self.assertEqual(merge(iter(data)), [(1, 4), (5, 8)])
        self.assertEqual(merge(r for r in data), [(1, 4), (5, 8)])
        self.assertEqual(subtract(tuple(data), iter([(2, 6)])), [(1, 2), (6, 8)])
        self.assertEqual(subtract(iter(data), tuple([(2, 6)])), [(1, 2), (6, 8)])
        self.assertEqual(intersect(iter(data), iter([(2, 6)])), [(2, 4), (5, 6)])
        self.assertIs(contains(iter(data), 6), True)
        self.assertIs(contains(tuple(data), 4), False)


if __name__ == "__main__":
    unittest.main()
