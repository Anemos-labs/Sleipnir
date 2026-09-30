"""The parsed configuration: lookups and typed getters. See README.md, "The API"."""

import re

_INTEGER = re.compile(r"[+-]?[0-9]+")
_TRUE = frozenset({"1", "true", "yes", "on"})
_FALSE = frozenset({"0", "false", "no", "off"})


class Config:
    """A read-only view of parsed INI data.

    `data` maps a section name to a dict of {lower-cased key: value}. Both levels keep the order in
    which their entries first appeared in the text. `loads()` (see parser.py) builds it.
    """

    def __init__(self, data):
        self._data = data

    def sections(self):
        """The section names, each once, in order of first appearance (a new list)."""
        return list(self._data)

    def options(self, section):
        """The lower-cased keys of `section` in order of first appearance (a new list).

        Raises KeyError if there is no such section.
        """
        return list(self._data[section])

    def get(self, section, key, default=None):
        """The value of `key` in `section`, or `default` if the section or the key is missing."""
        return self._data.get(section, {}).get(key.lower(), default)

    def getint(self, section, key, default=None):
        """Like get(), but the value must be an integer spelled as README.md says."""
        value = self.get(section, key)
        if value is None:
            return default
        if not _INTEGER.fullmatch(value):
            raise ValueError(f"not an integer: {value!r}")
        return int(value)

    def getbool(self, section, key, default=None):
        """Like get(), but the value must be one of the boolean spellings in README.md."""
        value = self.get(section, key)
        if value is None:
            return default
        spelling = value.lower()
        if spelling in _TRUE:
            return True
        if spelling in _FALSE:
            return False
        raise ValueError(f"not a boolean: {value!r}")

    def as_dict(self):
        """All sections and keys as new plain dicts: {section: {key: value}}."""
        return {name: dict(options) for name, options in self._data.items()}
