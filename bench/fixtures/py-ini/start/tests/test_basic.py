import unittest

from ini import IniError, loads


class BasicTests(unittest.TestCase):
    def test_sections_and_both_separators(self):
        cfg = loads("[server]\nhost = example.org\nport: 8080\n\n[client]\nname=x\n")
        self.assertEqual(cfg.sections(), ["server", "client"])
        self.assertEqual(cfg.get("server", "host"), "example.org")
        self.assertEqual(cfg.get("server", "port"), "8080")
        self.assertEqual(
            cfg.as_dict(),
            {"server": {"host": "example.org", "port": "8080"}, "client": {"name": "x"}},
        )

    def test_comment_lines_and_blank_lines_are_ignored(self):
        cfg = loads("; first\n# second\n\n[s]\n; inside\nk = v\n")
        self.assertEqual(cfg.as_dict(), {"s": {"k": "v"}})

    def test_keys_are_case_insensitive(self):
        cfg = loads("[s]\nHost = A\n")
        self.assertEqual(cfg.options("s"), ["host"])
        self.assertEqual(cfg.get("s", "HOST"), "A")

    def test_missing_entries_give_the_default(self):
        cfg = loads("[s]\nk = v\n")
        self.assertIsNone(cfg.get("s", "nope"))
        self.assertEqual(cfg.get("s", "nope", "d"), "d")
        self.assertEqual(cfg.get("nope", "k", "d"), "d")

    def test_typed_getters(self):
        cfg = loads("[s]\nn = 42\nneg = -7\nflag = yes\noff = Off\n")
        self.assertEqual(cfg.getint("s", "n"), 42)
        self.assertEqual(cfg.getint("s", "neg"), -7)
        self.assertIs(cfg.getbool("s", "flag"), True)
        self.assertIs(cfg.getbool("s", "off"), False)
        self.assertEqual(cfg.getint("s", "missing", 5), 5)

    def test_a_quoted_value_keeps_its_spaces(self):
        cfg = loads('[s]\nk = "  padded  "\n')
        self.assertEqual(cfg.get("s", "k"), "  padded  ")

    def test_a_trailing_backslash_continues_the_line(self):
        cfg = loads("[s]\npath = /usr/bin:\\\n       /usr/local/bin\n")
        self.assertEqual(cfg.get("s", "path"), "/usr/bin:/usr/local/bin")

    def test_a_key_before_the_first_section_is_an_error(self):
        with self.assertRaises(IniError) as caught:
            loads("k = v\n[s]\n")
        self.assertEqual(caught.exception.lineno, 1)


if __name__ == "__main__":
    unittest.main()
