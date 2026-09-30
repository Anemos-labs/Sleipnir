import unittest

from ini import loads


class SeparatorAndWhitespaceTests(unittest.TestCase):
    def test_equals_and_colon(self):
        cfg = loads("[s]\na = 1\nb: 2\nc=3\nd:4\n")
        self.assertEqual(cfg.as_dict(), {"s": {"a": "1", "b": "2", "c": "3", "d": "4"}})

    def test_the_first_separator_wins(self):
        text = "[s]\nurl = http://h:80/a=b\ntime: 12:30\nx:y=z\nk=v:w\n"
        self.assertEqual(
            loads(text).as_dict(),
            {"s": {"url": "http://h:80/a=b", "time": "12:30", "x": "y=z", "k": "v:w"}},
        )

    def test_whitespace_is_trimmed_and_indentation_is_ignored(self):
        cfg = loads("[s]\n  \tkey \t=\t value  with   gaps \t \n")
        self.assertEqual(cfg.get("s", "key"), "value  with   gaps")

    def test_keys_may_contain_spaces(self):
        cfg = loads("[s]\nmy key = 1\n")
        self.assertEqual(cfg.options("s"), ["my key"])
        self.assertEqual(cfg.get("s", "MY KEY"), "1")

    def test_empty_values(self):
        cfg = loads('[s]\na =\nb:\nc = ""\nd =   \n')
        self.assertEqual(cfg.as_dict(), {"s": {"a": "", "b": "", "c": "", "d": ""}})
        self.assertEqual(cfg.get("s", "a", "default"), "")

    def test_non_ascii_text_passes_through(self):
        cfg = loads("[café]\nnaïve = crème brûlée\n")
        self.assertEqual(cfg.as_dict(), {"café": {"naïve": "crème brûlée"}})

    def test_a_value_that_looks_like_a_header_is_just_a_value(self):
        cfg = loads("[s]\nk = [x]\nj = =\nl: :\na[0] = 1\n")
        self.assertEqual(cfg.as_dict(), {"s": {"k": "[x]", "j": "=", "l": ":", "a[0]": "1"}})


class SectionTests(unittest.TestCase):
    def test_names_are_trimmed_and_may_contain_spaces_and_separators(self):
        cfg = loads("[ one ]\na=1\n  [two words]  \nb=2\n[a=b:c]\nc=3\n")
        self.assertEqual(cfg.sections(), ["one", "two words", "a=b:c"])
        self.assertEqual(cfg.get("two words", "b"), "2")

    def test_a_section_without_keys_exists(self):
        cfg = loads("[a]\n[b]\nk = v\n")
        self.assertEqual(cfg.sections(), ["a", "b"])
        self.assertEqual(cfg.options("a"), [])
        self.assertEqual(cfg.as_dict(), {"a": {}, "b": {"k": "v"}})

    def test_a_section_opened_again_continues_the_same_section(self):
        cfg = loads("[a]\nx = 1\n[b]\ny = 2\n[a]\nz = 3\n")
        self.assertEqual(cfg.sections(), ["a", "b"])
        self.assertEqual(cfg.options("a"), ["x", "z"])
        self.assertEqual(cfg.as_dict(), {"a": {"x": "1", "z": "3"}, "b": {"y": "2"}})

    def test_text_without_sections(self):
        for text in ("", "\n", "  \n\t\n", "; just a comment\n# another\n"):
            with self.subTest(text=text):
                cfg = loads(text)
                self.assertEqual(cfg.sections(), [])
                self.assertEqual(cfg.as_dict(), {})

    def test_options_of_an_unknown_section_is_a_key_error(self):
        with self.assertRaises(KeyError):
            loads("[a]\n").options("b")

    def test_what_the_accessors_return_is_a_copy(self):
        cfg = loads("[s]\na = 1\n")
        cfg.sections().append("x")
        cfg.options("s").append("y")
        snapshot = cfg.as_dict()
        snapshot["s"]["a"] = "changed"
        snapshot["new"] = {}
        self.assertEqual(cfg.sections(), ["s"])
        self.assertEqual(cfg.options("s"), ["a"])
        self.assertEqual(cfg.as_dict(), {"s": {"a": "1"}})
        self.assertEqual(cfg.get("s", "a"), "1")


class CommentTests(unittest.TestCase):
    def test_comment_lines_and_blank_lines_are_ignored(self):
        text = "; one\n# two\n\n   \n\t# indented\n[s]\n; inside\nk = v\n  ;x\n"
        self.assertEqual(loads(text).as_dict(), {"s": {"k": "v"}})

    def test_comment_characters_inside_a_line_are_ordinary(self):
        text = "[s]\nurl = http://x/#top\na = 1 ; two\nb = x # y\nc = ;\nd = #\n"
        self.assertEqual(
            loads(text).as_dict(),
            {"s": {"url": "http://x/#top", "a": "1 ; two", "b": "x # y", "c": ";", "d": "#"}},
        )


class QuoteTests(unittest.TestCase):
    CASES = [
        ('a = "hello"', "hello"),
        ("a = '  padded  '", "  padded  "),
        ('a = ""', ""),
        ('a = "say \'hi\'"', "say 'hi'"),
        ("a = 'say \"hi\"'", 'say "hi"'),
        ('a = "a" "b"', 'a" "b'),
        ('a = ""x""', '"x"'),
        ('a = "unterminated', '"unterminated'),
        ("a = \"mixed'", "\"mixed'"),
        ('a = "', '"'),
        ('a = x"y"', 'x"y"'),
        ('a = "  "', "  "),
        ('a = "x"   ', "x"),
        ('a =    "x"', "x"),
        ('a = "a=b:c"', "a=b:c"),
        ("a = 'it''s'", "it''s"),
        ('a = "back\\slash"', "back\\slash"),
    ]

    def test_how_a_value_is_unquoted(self):
        for line, expected in self.CASES:
            with self.subTest(line=line):
                self.assertEqual(loads("[s]\n" + line + "\n").get("s", "a"), expected)

    def test_keys_are_never_unquoted(self):
        self.assertEqual(loads('[s]\n"k" = v\n').options("s"), ['"k"'])


class ContinuationTests(unittest.TestCase):
    def test_lines_are_joined(self):
        text = "[s]\npath = /usr/bin:\\\n        /usr/local/bin:\\\n\t/opt/bin\nnext = 1\n"
        cfg = loads(text)
        self.assertEqual(cfg.get("s", "path"), "/usr/bin:/usr/local/bin:/opt/bin")
        self.assertEqual(cfg.get("s", "next"), "1")

    def test_spaces_before_the_backslash_are_kept(self):
        cfg = loads("[s]\ngreeting = hello \\\n     world\n")
        self.assertEqual(cfg.get("s", "greeting"), "hello world")

    def test_a_quoted_value_can_span_lines(self):
        cfg = loads('[s]\nmsg = "  two \\\n   words  "\n')
        self.assertEqual(cfg.get("s", "msg"), "  two words  ")

    def test_the_appended_line_is_taken_as_it_is(self):
        cfg = loads("[s]\na = one \\\n# not a comment\nb = 2\n")
        self.assertEqual(cfg.as_dict(), {"s": {"a": "one # not a comment", "b": "2"}})
        cfg = loads("[s]\na = one \\\n[not a header]\nb = 2\n")
        self.assertEqual(cfg.as_dict(), {"s": {"a": "one [not a header]", "b": "2"}})

    def test_an_appended_blank_line_is_consumed(self):
        cfg = loads("[s]\na = one \\\n\nb = 2\n")
        self.assertEqual(cfg.as_dict(), {"s": {"a": "one", "b": "2"}})

    def test_joining_goes_on_while_lines_end_with_a_backslash(self):
        cfg = loads("[s]\na = 1\\\n2\\\n3\\\n4\nb = 5\n")
        self.assertEqual(cfg.as_dict(), {"s": {"a": "1234", "b": "5"}})

    def test_a_backslash_followed_by_a_space_is_an_ordinary_character(self):
        cfg = loads("[s]\na = x\\ \nb = 2\n")
        self.assertEqual(cfg.as_dict(), {"s": {"a": "x\\", "b": "2"}})

    def test_backslashes_elsewhere_are_ordinary_characters(self):
        cfg = loads("[s]\na = C:\\dir\\file\n")
        self.assertEqual(cfg.get("s", "a"), "C:\\dir\\file")

    def test_comment_lines_are_never_continued(self):
        cfg = loads("[s]\n# note \\\nk = v\n; other \\\nj = w\n")
        self.assertEqual(cfg.as_dict(), {"s": {"k": "v", "j": "w"}})

    def test_a_backslash_at_the_very_end_of_the_text_is_removed(self):
        self.assertEqual(loads("[s]\na = b\\").get("s", "a"), "b")
        self.assertEqual(loads("[s]\na = b\\\n").get("s", "a"), "b")

    def test_crlf_input_behaves_like_lf_input(self):
        lf = "[s]\na = 1\nb = x \\\n   y\n; c\nc : 3\n"
        crlf = lf.replace("\n", "\r\n")
        self.assertEqual(loads(crlf).as_dict(), loads(lf).as_dict())
        self.assertEqual(loads(crlf).as_dict(), {"s": {"a": "1", "b": "x y", "c": "3"}})


EXAMPLE = """\
; a comment
[server]
Host = example.org
port: 8080
url = http://example.org:8080/a=b#top
motto = "  keep   spaces  "
paths = /usr/bin:\\
        /usr/local/bin

[flags]
debug = Yes
retries = +3
[server]
extra = 1
"""


class ReadmeExampleTests(unittest.TestCase):
    def test_the_complete_example(self):
        self.assertEqual(
            loads(EXAMPLE).as_dict(),
            {
                "server": {
                    "host": "example.org",
                    "port": "8080",
                    "url": "http://example.org:8080/a=b#top",
                    "motto": "  keep   spaces  ",
                    "paths": "/usr/bin:/usr/local/bin",
                    "extra": "1",
                },
                "flags": {"debug": "Yes", "retries": "+3"},
            },
        )

    def test_the_usage_snippet(self):
        cfg = loads("\n; global settings\n[server]\nHost = example.org\nport: 8080\ndebug = Yes\n")
        self.assertEqual(cfg.sections(), ["server"])
        self.assertEqual(cfg.get("server", "host"), "example.org")
        self.assertEqual(cfg.getint("server", "port"), 8080)
        self.assertIs(cfg.getbool("server", "debug"), True)
        self.assertEqual(cfg.get("server", "nope", "-"), "-")


if __name__ == "__main__":
    unittest.main()
