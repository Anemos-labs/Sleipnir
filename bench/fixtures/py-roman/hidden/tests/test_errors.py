import unittest

from roman import from_roman, to_roman


class ToRomanErrorTests(unittest.TestCase):
    def test_out_of_range_is_a_value_error_with_the_documented_prefix(self):
        for n in (0, -1, -3999, 4000, 4001, 10**9):
            with self.subTest(n=n):
                with self.assertRaises(ValueError) as caught:
                    to_roman(n)
                self.assertTrue(str(caught.exception).startswith("out of range"), str(caught.exception))

    def test_non_integers_are_a_type_error(self):
        for value in ("12", 3.0, 12.5, None, [12], b"12"):
            with self.subTest(value=value):
                with self.assertRaises(TypeError):
                    to_roman(value)


class FromRomanErrorTests(unittest.TestCase):
    INVALID = [
        "",
        "IIII", "VV", "VX", "IC", "IIX", "XXXX", "VIV", "CMC", "LL", "DD", "IL", "XM", "IXI",
        "MMMM", "MMMMCMXCIX", "MMMCMXCIXI",
        "iv", "Iv", "mcmxciv", " IV", "IV ", "I V", "X\n",
        "A", "IVA", "1", "MCMXCIV!", "Ⅳ",
    ]

    def test_invalid_numerals_are_a_value_error_with_the_documented_prefix(self):
        for text in self.INVALID:
            with self.subTest(text=text):
                with self.assertRaises(ValueError) as caught:
                    from_roman(text)
                self.assertTrue(
                    str(caught.exception).startswith("invalid roman numeral"), str(caught.exception)
                )

    def test_non_strings_are_a_type_error(self):
        for value in (5, 5.0, None, b"V", ["V"], ("V",)):
            with self.subTest(value=value):
                with self.assertRaises(TypeError):
                    from_roman(value)

    def test_just_below_and_above_the_top_of_the_range(self):
        self.assertEqual(from_roman("MMMCMXCVIII"), 3998)
        self.assertEqual(from_roman("MMMCMXCIX"), 3999)
        with self.assertRaises(ValueError):
            from_roman("MMMM")


if __name__ == "__main__":
    unittest.main()
