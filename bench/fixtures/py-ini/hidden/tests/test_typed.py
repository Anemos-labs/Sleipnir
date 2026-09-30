import unittest

from ini import loads


def one(text):
    """A Config whose section [s] has the single line `k = <text>`."""
    return loads("[s]\nk = " + text + "\n")


class GetIntTests(unittest.TestCase):
    def test_accepted_spellings(self):
        big = "123456789012345678901234567890"
        cases = [("0", 0), ("7", 7), ("-7", -7), ("+7", 7), ("007", 7), ("-0", 0), ("+0", 0), (big, int(big))]
        for text, expected in cases:
            with self.subTest(text=text):
                value = one(text).getint("s", "k")
                self.assertEqual(value, expected)
                self.assertIs(type(value), int)

    def test_rejected_spellings(self):
        rejected = [
            "abc", "1.5", "1.", ".5", "1e3", "1_000", "0x10", "--1", "+-1", "1-", "- 1", "ten",
            "12abc", "１２", "٣", "", '" 7"', '"7 "', '"7\t"', "1 2",
        ]
        for text in rejected:
            with self.subTest(text=text):
                with self.assertRaises(ValueError):
                    one(text).getint("s", "k")

    def test_a_missing_entry_gives_the_default_unchanged(self):
        cfg = loads("[s]\nk = 1\n")
        sentinel = object()
        self.assertIsNone(cfg.getint("s", "missing"))
        self.assertEqual(cfg.getint("s", "missing", 5), 5)
        self.assertEqual(cfg.getint("nope", "k", 6), 6)
        self.assertIs(cfg.getint("s", "missing", sentinel), sentinel)

    def test_the_default_does_not_hide_a_malformed_value(self):
        with self.assertRaises(ValueError):
            one("abc").getint("s", "k", 5)
        with self.assertRaises(ValueError):
            loads("[s]\nk =\n").getint("s", "k", 5)

    def test_the_key_is_looked_up_ignoring_case(self):
        self.assertEqual(loads("[s]\nPort = 80\n").getint("s", "PORT"), 80)


class GetBoolTests(unittest.TestCase):
    TRUE = ["1", "true", "yes", "on", "TRUE", "True", "YES", "Yes", "On", "ON", "oN", "tRuE"]
    FALSE = ["0", "false", "no", "off", "FALSE", "False", "NO", "No", "OFF", "Off", "fAlSe"]
    REJECTED = [
        "", "2", "-1", "+1", "01", "00", "t", "f", "y", "n", "enabled", "disabled", "truee",
        "tru", "yess", "of", "onn", "nope", "none", "null", "1 0", "true false",
        "ｔｒｕｅ", '" yes"', '"no "', '"on\t"',
    ]

    def test_true_spellings(self):
        for text in self.TRUE:
            with self.subTest(text=text):
                self.assertIs(one(text).getbool("s", "k"), True)

    def test_false_spellings(self):
        for text in self.FALSE:
            with self.subTest(text=text):
                self.assertIs(one(text).getbool("s", "k"), False)

    def test_everything_else_is_a_value_error(self):
        for text in self.REJECTED:
            with self.subTest(text=text):
                with self.assertRaises(ValueError):
                    one(text).getbool("s", "k")

    def test_a_missing_entry_gives_the_default_unchanged(self):
        cfg = loads("[s]\nk = yes\n")
        sentinel = object()
        self.assertIsNone(cfg.getbool("s", "missing"))
        self.assertIs(cfg.getbool("s", "missing", False), False)
        self.assertIs(cfg.getbool("s", "missing", True), True)
        self.assertIs(cfg.getbool("nope", "k", False), False)
        self.assertIs(cfg.getbool("s", "missing", sentinel), sentinel)

    def test_the_default_does_not_hide_a_malformed_value(self):
        with self.assertRaises(ValueError):
            one("maybe").getbool("s", "k", True)
        with self.assertRaises(ValueError):
            loads("[s]\nk =\n").getbool("s", "k", False)

    def test_the_key_is_looked_up_ignoring_case(self):
        self.assertIs(loads("[s]\nDebug = on\n").getbool("s", "DEBUG"), True)


class GetTests(unittest.TestCase):
    def test_get_returns_strings_and_an_empty_value_is_not_missing(self):
        cfg = loads("[s]\nn = 42\nempty =\n")
        self.assertEqual(cfg.get("s", "n"), "42")
        self.assertEqual(cfg.get("s", "empty", "dflt"), "")
        self.assertEqual(cfg.get("s", "missing", "dflt"), "dflt")
        self.assertIsNone(cfg.get("nosuch", "n"))


if __name__ == "__main__":
    unittest.main()
