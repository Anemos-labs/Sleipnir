"""Errors raised while reading INI text."""


class IniError(ValueError):
    """A syntax error in INI text.

    `lineno` is the 1-based number of the first physical line of the logical line in which the
    problem was found (see README.md, rule 3). `str(error)` reads "line N: message".
    """

    def __init__(self, message, lineno):
        super().__init__(f"line {lineno}: {message}")
        self.message = message
        self.lineno = lineno
