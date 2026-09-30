import unittest

from intervals import contains, intersect, merge, subtract


class MergeTests(unittest.TestCase):
    def test_readme_examples(self):
        self.assertEqual(merge([(5, 8), (1, 3), (3, 4), (7, 9), (12, 12)]), [(1, 4), (5, 9)])
        self.assertEqual(merge([(0, 10), (2, 3)]), [(0, 10)])
        self.assertEqual(merge([(4, 4)]), [])
        self.assertEqual(merge([]), [])

    def test_a_chain_of_touching_ranges_is_one_range(self):
        self.assertEqual(merge([(1, 2), (2, 3), (3, 4)]), [(1, 4)])
        self.assertEqual(merge([(3, 4), (1, 2), (2, 3)]), [(1, 4)])

    def test_a_gap_of_one_integer_keeps_ranges_apart(self):
        self.assertEqual(merge([(1, 2), (3, 4)]), [(1, 2), (3, 4)])

    def test_nested_and_overlapping_ranges(self):
        self.assertEqual(merge([(0, 10), (2, 3)]), [(0, 10)])
        self.assertEqual(merge([(2, 3), (0, 10)]), [(0, 10)])
        self.assertEqual(merge([(0, 10), (2, 30)]), [(0, 30)])
        self.assertEqual(merge([(0, 10), (3, 5), (4, 12), (20, 25), (21, 22)]), [(0, 12), (20, 25)])

    def test_duplicates(self):
        self.assertEqual(merge([(1, 2), (1, 2), (1, 2)]), [(1, 2)])

    def test_unsorted_input(self):
        self.assertEqual(merge([(5, 8), (1, 3)]), [(1, 3), (5, 8)])
        self.assertEqual(merge([(9, 10), (5, 6), (1, 2), (7, 8), (3, 4)]), [(1, 2), (3, 4), (5, 6), (7, 8), (9, 10)])

    def test_empty_ranges_cover_nothing_and_join_nothing(self):
        self.assertEqual(merge([(4, 4)]), [])
        self.assertEqual(merge([(1, 4), (4, 4), (4, 8)]), [(1, 8)])
        self.assertEqual(merge([(0, 5), (2, 2)]), [(0, 5)])
        self.assertEqual(merge([(1, 2), (3, 3), (4, 5)]), [(1, 2), (4, 5)])
        self.assertEqual(merge([(9, 9), (1, 2), (0, 0)]), [(1, 2)])

    def test_negative_numbers(self):
        self.assertEqual(merge([(-5, -1), (-1, 2), (4, 6)]), [(-5, 2), (4, 6)])

    def test_the_result_is_a_new_list_even_for_canonical_input(self):
        ranges = [(1, 2), (4, 5)]
        result = merge(ranges)
        self.assertEqual(result, [(1, 2), (4, 5)])
        self.assertIsNot(result, ranges)


class SubtractTests(unittest.TestCase):
    def test_readme_examples(self):
        self.assertEqual(subtract([(0, 10)], [(2, 4), (6, 8)]), [(0, 2), (4, 6), (8, 10)])
        self.assertEqual(subtract([(0, 10)], [(0, 3)]), [(3, 10)])
        self.assertEqual(subtract([(0, 5)], [(5, 9)]), [(0, 5)])
        self.assertEqual(subtract([(5, 8), (1, 3)], [(2, 6)]), [(1, 2), (6, 8)])
        self.assertEqual(subtract([(0, 5)], [(0, 5)]), [])

    def test_cuts_at_either_end(self):
        self.assertEqual(subtract([(0, 10)], [(7, 10)]), [(0, 7)])
        self.assertEqual(subtract([(0, 10)], [(0, 3), (7, 10)]), [(3, 7)])
        self.assertEqual(subtract([(0, 10)], [(-5, 3)]), [(3, 10)])
        self.assertEqual(subtract([(0, 10)], [(8, 20)]), [(0, 8)])

    def test_cuts_that_cover_everything(self):
        self.assertEqual(subtract([(2, 5)], [(0, 10)]), [])
        self.assertEqual(subtract([(2, 5), (7, 9)], [(0, 10)]), [])

    def test_a_cut_that_spans_several_ranges(self):
        self.assertEqual(subtract([(0, 3), (5, 8)], [(2, 6)]), [(0, 2), (6, 8)])
        self.assertEqual(subtract([(0, 3), (5, 8), (10, 12)], [(1, 11)]), [(0, 1), (11, 12)])

    def test_cuts_that_touch_each_other(self):
        self.assertEqual(subtract([(0, 10)], [(2, 4), (4, 6)]), [(0, 2), (6, 10)])

    def test_cuts_that_overlap_or_are_unsorted(self):
        self.assertEqual(subtract([(0, 10)], [(6, 8), (2, 4), (3, 5)]), [(0, 2), (5, 6), (8, 10)])

    def test_an_empty_cut_removes_nothing_and_splits_nothing(self):
        self.assertEqual(subtract([(0, 5)], [(2, 2)]), [(0, 5)])
        self.assertEqual(subtract([(0, 5)], [(0, 0), (5, 5)]), [(0, 5)])

    def test_a_cut_outside_the_range_is_ignored(self):
        self.assertEqual(subtract([(5, 8)], [(0, 5), (8, 12)]), [(5, 8)])

    def test_empty_arguments(self):
        self.assertEqual(subtract([], [(0, 5)]), [])
        self.assertEqual(subtract([(0, 5)], []), [(0, 5)])
        self.assertEqual(subtract([], []), [])
        self.assertEqual(subtract([(3, 3)], [(0, 9)]), [])

    def test_the_first_argument_need_not_be_sorted_or_disjoint(self):
        self.assertEqual(subtract([(3, 8), (0, 5)], [(4, 6)]), [(0, 4), (6, 8)])
        self.assertEqual(subtract([(0, 3), (3, 6)], []), [(0, 6)])
        self.assertEqual(subtract([(0, 3), (3, 6)], [(3, 3)]), [(0, 6)])
        self.assertEqual(subtract([(0, 5), (0, 5), (2, 9)], [(1, 2)]), [(0, 1), (2, 9)])
        self.assertEqual(subtract([(7, 9), (0, 2), (4, 4)], [(1, 8)]), [(0, 1), (8, 9)])

    def test_negative_numbers(self):
        self.assertEqual(subtract([(-10, 10)], [(-3, 3)]), [(-10, -3), (3, 10)])


class IntersectTests(unittest.TestCase):
    def test_readme_examples(self):
        self.assertEqual(intersect([(0, 5)], [(5, 9)]), [])
        self.assertEqual(intersect([(0, 10)], [(1, 2), (3, 4)]), [(1, 2), (3, 4)])
        self.assertEqual(intersect([(0, 4), (6, 10)], [(2, 8)]), [(2, 4), (6, 8)])
        self.assertEqual(intersect([(0, 5), (3, 8)], [(4, 6)]), [(4, 6)])

    def test_touching_ranges_share_no_integer(self):
        self.assertEqual(intersect([(0, 5)], [(5, 9)]), [])
        self.assertEqual(intersect([(5, 9)], [(0, 5)]), [])
        self.assertEqual(intersect([(0, 5), (9, 12)], [(5, 9)]), [])

    def test_overlap_of_one_integer(self):
        self.assertEqual(intersect([(0, 5)], [(4, 9)]), [(4, 5)])
        self.assertEqual(intersect([(4, 9)], [(0, 5)]), [(4, 5)])

    def test_containment_and_identity(self):
        self.assertEqual(intersect([(0, 10)], [(3, 4)]), [(3, 4)])
        self.assertEqual(intersect([(3, 4)], [(0, 10)]), [(3, 4)])
        self.assertEqual(intersect([(2, 7)], [(2, 7)]), [(2, 7)])

    def test_a_wide_range_against_many_narrow_ones(self):
        self.assertEqual(intersect([(0, 10)], [(1, 2), (3, 4), (5, 6)]), [(1, 2), (3, 4), (5, 6)])
        self.assertEqual(intersect([(1, 2), (3, 4), (5, 6)], [(0, 10)]), [(1, 2), (3, 4), (5, 6)])

    def test_many_ranges_on_both_sides(self):
        self.assertEqual(
            intersect([(0, 3), (5, 9), (12, 15)], [(2, 6), (8, 13)]),
            [(2, 3), (5, 6), (8, 9), (12, 13)],
        )

    def test_unsorted_overlapping_inputs(self):
        self.assertEqual(intersect([(6, 10), (0, 4), (3, 5)], [(9, 12), (2, 7)]), [(2, 5), (6, 7), (9, 10)])

    def test_empty_ranges_and_empty_arguments(self):
        self.assertEqual(intersect([(3, 3)], [(0, 9)]), [])
        self.assertEqual(intersect([(0, 9)], [(3, 3)]), [])
        self.assertEqual(intersect([], [(0, 1)]), [])
        self.assertEqual(intersect([(0, 1)], []), [])
        self.assertEqual(intersect([], []), [])

    def test_the_order_of_the_arguments_does_not_matter(self):
        a = [(0, 4), (6, 10), (12, 13)]
        b = [(3, 7), (9, 20)]
        self.assertEqual(intersect(a, b), intersect(b, a))
        self.assertEqual(intersect(a, b), [(3, 4), (6, 7), (9, 10), (12, 13)])


class ContainsTests(unittest.TestCase):
    def test_readme_examples(self):
        self.assertIs(contains([(0, 5)], 0), True)
        self.assertIs(contains([(0, 5)], 4), True)
        self.assertIs(contains([(0, 5)], 5), False)
        self.assertIs(contains([(3, 3)], 3), False)

    def test_boundaries_of_several_ranges(self):
        ranges = [(10, 12), (0, 3), (3, 5)]
        inside = {0, 1, 2, 3, 4, 10, 11}
        for x in range(-2, 15):
            with self.subTest(x=x):
                self.assertIs(contains(ranges, x), x in inside)

    def test_nothing_is_contained_in_nothing(self):
        self.assertIs(contains([], 0), False)
        self.assertIs(contains([(2, 2), (7, 7)], 2), False)
        self.assertIs(contains([(2, 2), (7, 7)], 7), False)

    def test_negative_numbers(self):
        self.assertIs(contains([(-5, -2)], -5), True)
        self.assertIs(contains([(-5, -2)], -2), False)
        self.assertIs(contains([(-5, -2)], -3), True)


if __name__ == "__main__":
    unittest.main()
