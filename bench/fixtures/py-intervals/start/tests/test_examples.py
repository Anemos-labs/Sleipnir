import unittest

from intervals import contains, intersect, merge, subtract


class MergeExamples(unittest.TestCase):
    def test_overlapping_ranges_are_joined(self):
        self.assertEqual(merge([(1, 4), (3, 6)]), [(1, 6)])

    def test_touching_ranges_are_joined(self):
        self.assertEqual(merge([(1, 3), (3, 5)]), [(1, 5)])

    def test_empty_ranges_are_dropped(self):
        self.assertEqual(merge([(2, 2), (7, 9)]), [(7, 9)])


class SubtractExamples(unittest.TestCase):
    def test_a_cut_in_the_middle(self):
        self.assertEqual(subtract([(0, 10)], [(2, 4), (6, 8)]), [(0, 2), (4, 6), (8, 10)])

    def test_a_cut_at_the_start(self):
        self.assertEqual(subtract([(0, 10)], [(0, 3)]), [(3, 10)])


class IntersectExamples(unittest.TestCase):
    def test_overlapping_ranges(self):
        self.assertEqual(intersect([(0, 5)], [(3, 9)]), [(3, 5)])

    def test_touching_ranges_share_nothing(self):
        self.assertEqual(intersect([(0, 5)], [(5, 9)]), [])


class ContainsExamples(unittest.TestCase):
    def test_the_start_is_in_and_the_end_is_out(self):
        self.assertTrue(contains([(0, 5)], 0))
        self.assertTrue(contains([(0, 5)], 4))
        self.assertFalse(contains([(0, 5)], 5))


if __name__ == "__main__":
    unittest.main()
