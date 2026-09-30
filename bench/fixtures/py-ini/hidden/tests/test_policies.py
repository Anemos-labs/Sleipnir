import unittest

from ini import IniError, loads


class CaseTests(unittest.TestCase):
    def test_section_names_are_case_sensitive(self):
        cfg = loads("[Core]\na = 1\n[core]\na = 2\n")
        self.assertEqual(cfg.sections(), ["Core", "core"])
        self.assertEqual(cfg.get("Core", "a"), "1")
        self.assertEqual(cfg.get("core", "a"), "2")
        self.assertIsNone(cfg.get("CORE", "a"))
        self.assertEqual(cfg.get("CORE", "a", "d"), "d")

    def test_keys_are_case_insensitive_and_stored_lower_case(self):
        cfg = loads("[s]\nHost = A\nPORT = 1\nmixedCase = x\n")
        self.assertEqual(cfg.options("s"), ["host", "port", "mixedcase"])
        for spelling in ("host", "HOST", "Host", "hOsT"):
            with self.subTest(spelling=spelling):
                self.assertEqual(cfg.get("s", spelling), "A")
        self.assertEqual(cfg.getint("s", "Port"), 1)
        self.assertEqual(cfg.as_dict(), {"s": {"host": "A", "port": "1", "mixedcase": "x"}})

    def test_values_keep_their_case(self):
        cfg = loads("[s]\nk = MiXeD Case\n")
        self.assertEqual(cfg.get("s", "k"), "MiXeD Case")


class DuplicateKeyTests(unittest.TestCase):
    TEXT = "[s]\na = 1\nb = 2\nA = 3\n"

    def test_the_default_is_last_and_the_key_keeps_its_first_position(self):
        cfg = loads(self.TEXT)
        self.assertEqual(cfg.as_dict(), {"s": {"a": "3", "b": "2"}})
        self.assertEqual(cfg.options("s"), ["a", "b"])

    def test_last_can_be_asked_for_explicitly(self):
        cfg = loads(self.TEXT, duplicates="last")
        self.assertEqual(cfg.as_dict(), {"s": {"a": "3", "b": "2"}})
        self.assertEqual(cfg.options("s"), ["a", "b"])

    def test_first_keeps_the_first_value(self):
        cfg = loads(self.TEXT, duplicates="first")
        self.assertEqual(cfg.as_dict(), {"s": {"a": "1", "b": "2"}})
        self.assertEqual(cfg.options("s"), ["a", "b"])

    def test_error_reports_the_line_of_the_later_definition(self):
        with self.assertRaises(IniError) as caught:
            loads(self.TEXT, duplicates="error")
        self.assertEqual(caught.exception.lineno, 4)

    def test_a_section_that_was_opened_again_counts(self):
        text = "[s]\na = 1\n[t]\nb = 1\n[s]\na = 2\n"
        self.assertEqual(loads(text).get("s", "a"), "2")
        self.assertEqual(loads(text, duplicates="last").get("s", "a"), "2")
        self.assertEqual(loads(text, duplicates="first").get("s", "a"), "1")
        with self.assertRaises(IniError) as caught:
            loads(text, duplicates="error")
        self.assertEqual(caught.exception.lineno, 6)

    def test_the_same_key_in_different_sections_is_not_a_duplicate(self):
        text = "[a]\nk = 1\n[b]\nk = 2\n"
        for policy in ("last", "first", "error"):
            with self.subTest(policy=policy):
                cfg = loads(text, duplicates=policy)
                self.assertEqual(cfg.as_dict(), {"a": {"k": "1"}, "b": {"k": "2"}})

    def test_text_without_duplicates_is_fine_under_every_policy(self):
        for policy in ("last", "first", "error"):
            with self.subTest(policy=policy):
                self.assertEqual(loads("[s]\na = 1\nb = 2\n", duplicates=policy).options("s"), ["a", "b"])

    def test_an_unknown_policy_is_a_value_error_raised_before_parsing(self):
        for value in ("nope", "LAST", "", None, "warn"):
            with self.subTest(value=value):
                with self.assertRaises(ValueError) as caught:
                    loads("this is not valid ini text", duplicates=value)
                self.assertNotIsInstance(caught.exception, IniError)


if __name__ == "__main__":
    unittest.main()
