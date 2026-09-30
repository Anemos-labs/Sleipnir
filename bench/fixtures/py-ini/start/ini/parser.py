"""Turn INI text into a Config. The rules are in README.md."""

from .config import Config
from .errors import IniError


def loads(text, *, duplicates="last"):
    """Parse the INI text `text` and return a Config.

    `duplicates` is "last", "first" or "error" (see README.md, "Duplicate keys").
    Raises IniError for malformed text and ValueError for an unknown `duplicates` value.
    """
    raise NotImplementedError("loads")
