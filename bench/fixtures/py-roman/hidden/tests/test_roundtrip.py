import itertools
import unittest

from roman import from_roman, to_roman

# An independent description of the canonical numerals, digit by digit.
_THOUSANDS = ["", "M", "MM", "MMM"]
_HUNDREDS = ["", "C", "CC", "CCC", "CD", "D", "DC", "DCC", "DCCC", "CM"]
_TENS = ["", "X", "XX", "XXX", "XL", "L", "LX", "LXX", "LXXX", "XC"]
_ONES = ["", "I", "II", "III", "IV", "V", "VI", "VII", "VIII", "IX"]


def oracle(n):
    return _THOUSANDS[n // 1000] + _HUNDREDS[n // 100 % 10] + _TENS[n // 10 % 10] + _ONES[n % 10]


class BoundaryTests(unittest.TestCase):
    def test_lowest_and_highest(self):
        self.assertEqual(to_roman(1), "I")
        self.assertEqual(to_roman(3999), "MMMCMXCIX")
        self.assertEqual(from_roman("I"), 1)
        self.assertEqual(from_roman("MMMCMXCIX"), 3999)

    def test_known_values(self):
        table = {
            1: "I", 3: "III", 4: "IV", 8: "VIII", 9: "IX", 14: "XIV", 19: "XIX", 40: "XL",
            44: "XLIV", 49: "XLIX", 90: "XC", 99: "XCIX", 400: "CD", 444: "CDXLIV",
            900: "CM", 999: "CMXCIX", 1000: "M", 1994: "MCMXCIV", 2024: "MMXXIV",
            3000: "MMM", 3888: "MMMDCCCLXXXVIII", 3999: "MMMCMXCIX",
        }
        for n, numeral in table.items():
            with self.subTest(n=n):
                self.assertEqual(to_roman(n), numeral)
                self.assertEqual(from_roman(numeral), n)

    def test_results_have_the_documented_types(self):
        self.assertIs(type(to_roman(12)), str)
        self.assertIs(type(from_roman("XII")), int)


class ExhaustiveTests(unittest.TestCase):
    def test_to_roman_matches_the_digit_table_for_every_number(self):
        wrong = [(n, to_roman(n), oracle(n)) for n in range(1, 4000) if to_roman(n) != oracle(n)]
        self.assertEqual(wrong[:10], [])

    def test_every_number_round_trips(self):
        bad = [n for n in range(1, 4000) if from_roman(to_roman(n)) != n]
        self.assertEqual(bad[:10], [])

    def test_only_canonical_spellings_are_accepted(self):
        # Every string of up to five letters from IVXLCDM is either the canonical numeral of some
        # number in 1..3999 (and then converts back to that number) or it is rejected.
        canonical = {oracle(n): n for n in range(1, 4000)}
        wrong = []
        for length in range(0, 6):
            for letters in itertools.product("IVXLCDM", repeat=length):
                text = "".join(letters)
                try:
                    result = from_roman(text)
                except ValueError as exc:
                    if text in canonical or not str(exc).startswith("invalid roman numeral"):
                        wrong.append((text, "raised", str(exc)))
                else:
                    if canonical.get(text) != result:
                        wrong.append((text, "returned", result))
        self.assertEqual(wrong[:10], [])


if __name__ == "__main__":
    unittest.main()
