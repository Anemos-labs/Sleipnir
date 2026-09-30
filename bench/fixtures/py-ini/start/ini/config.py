"""The parsed configuration: lookups and typed getters. See README.md, "The API"."""


class Config:
    """A read-only view of parsed INI data.

    `data` maps a section name to a dict of {lower-cased key: value}. Both levels keep the order in
    which their entries first appeared in the text. `loads()` (see parser.py) builds it.
    """

    def __init__(self, data):
        self._data = data

    def sections(self):
        """The section names, each once, in order of first appearance (a new list)."""
        raise NotImplementedError("Config.sections")

    def options(self, section):
        """The lower-cased keys of `section` in order of first appearance (a new list).

        Raises KeyError if there is no such section.
        """
        raise NotImplementedError("Config.options")

    def get(self, section, key, default=None):
        """The value of `key` in `section`, or `default` if the section or the key is missing."""
        raise NotImplementedError("Config.get")

    def getint(self, section, key, default=None):
        """Like get(), but the value must be an integer spelled as README.md says."""
        raise NotImplementedError("Config.getint")

    def getbool(self, section, key, default=None):
        """Like get(), but the value must be one of the boolean spellings in README.md."""
        raise NotImplementedError("Config.getbool")

    def as_dict(self):
        """All sections and keys as new plain dicts: {section: {key: value}}."""
        raise NotImplementedError("Config.as_dict")
