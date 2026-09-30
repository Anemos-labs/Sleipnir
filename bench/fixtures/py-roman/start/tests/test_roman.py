import unittest

from roman import from_roman, to_roman


class ToRomanTests(unittest.TestCase):
    def test_small_numbers(self):
        expected = {1: "I", 2: "II", 3: "III", 4: "IV", 5: "V", 6: "VI", 9: "IX", 10: "X"}
        for n, numeral in expected.items():
            with self.subTest(n=n):
                self.assertEqual(to_roman(n), numeral)

    def test_subtractive_pairs(self):
        expected = {14: "XIV", 40: "XL", 49: "XLIX", 90: "XC", 400: "CD", 900: "CM"}
        for n, numeral in expected.items():
            with self.subTest(n=n):
                self.assertEqual(to_roman(n), numeral)

    def test_years(self):
        self.assertEqual(to_roman(1994), "MCMXCIV")
        self.assertEqual(to_roman(2024), "MMXXIV")

    def test_zero_is_out_of_range(self):
        with self.assertRaises(ValueError):
            to_roman(0)


class FromRomanTests(unittest.TestCase):
    def test_single_letters(self):
        for numeral, n in {"I": 1, "V": 5, "X": 10, "L": 50, "C": 100, "D": 500, "M": 1000}.items():
            with self.subTest(numeral=numeral):
                self.assertEqual(from_roman(numeral), n)

    def test_subtractive_pairs(self):
        self.assertEqual(from_roman("IV"), 4)
        self.assertEqual(from_roman("XLIX"), 49)
        self.assertEqual(from_roman("MCMXCIV"), 1994)

    def test_repeated_letters(self):
        self.assertEqual(from_roman("III"), 3)
        self.assertEqual(from_roman("XX"), 20)
        self.assertEqual(from_roman("MMXXIV"), 2024)

    def test_non_canonical_spelling_is_invalid(self):
        for text in ("IIII", "VX", "IC"):
            with self.subTest(text=text):
                with self.assertRaises(ValueError):
                    from_roman(text)


if __name__ == "__main__":
    unittest.main()
