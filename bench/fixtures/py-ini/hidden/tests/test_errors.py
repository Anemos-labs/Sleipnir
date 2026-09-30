import unittest

from ini import IniError, loads


class IniErrorTests(unittest.TestCase):
    def assertErrorLine(self, text, lineno, **options):
        with self.assertRaises(IniError) as caught:
            loads(text, **options)
        self.assertEqual(caught.exception.lineno, lineno, repr(text))

    def test_ini_error_is_a_value_error(self):
        self.assertTrue(issubclass(IniError, ValueError))

    def test_a_key_before_the_first_section(self):
        self.assertErrorLine("a = 1\n[s]\n", 1)
        self.assertErrorLine("\n# c\n\nx = 1\n[s]\n", 4)
        self.assertErrorLine("; only comments before\n  \nk: v", 3)

    def test_a_line_without_a_separator(self):
        self.assertErrorLine("[s]\nk = v\njust words\n", 3)
        self.assertErrorLine("[s]\nnovalue\n", 2)

    def test_an_empty_key(self):
        self.assertErrorLine("[s]\n= v\n", 2)
        self.assertErrorLine("[s]\n  : v\n", 2)
        self.assertErrorLine("[s]\n \t = \n", 2)

    def test_malformed_section_headers(self):
        for header in ("[", "[]", "[ ]", "[a", "[a] x", "[a]]", "[[a]]", "[a[b]", "[a]b]", "[a] ; c"):
            with self.subTest(header=header):
                self.assertErrorLine("[ok]\n" + header + "\n", 2)

    def test_a_duplicate_key_under_the_error_policy(self):
        self.assertErrorLine("[s]\na = 1\nb = 2\nB = 3\n", 4, duplicates="error")

    def test_the_line_of_a_joined_line_is_its_first_physical_line(self):
        self.assertErrorLine("[s]\na = 1\nbroken \\\n  line\nb = 2\n", 3)
        self.assertErrorLine("[s]\na = 1\\\n2\\\n3\nnot valid\n", 5)
        self.assertErrorLine("[s]\na = 1\nx = 1\nx = \\\n2\n", 4, duplicates="error")

    def test_crlf_does_not_change_line_numbers(self):
        self.assertErrorLine("[s]\r\nk = v\r\noops\r\n", 3)
        self.assertErrorLine("[s]\r\na = \\\r\n1\r\n\r\noops\r\n", 5)

    def test_unusual_but_valid_text_raises_nothing(self):
        texts = [
            "[s]\n\tk\t:\tv\n",
            "[s]\nk = \"\n",
            "[s]\n    \n   ; c\n",
            "[s]\nk = a\\b\\\n",
            "[s]\n[t]\n[s]\n",
            "[  spaced name  ]\n",
            "[s]\nk = v ; not a comment\n",
        ]
        for text in texts:
            with self.subTest(text=text):
                loads(text)


if __name__ == "__main__":
    unittest.main()
