"""Turn INI text into a Config. The rules are in README.md."""

from .config import Config
from .errors import IniError

_POLICIES = ("last", "first", "error")
_WHITESPACE = " \t"


def _logical_lines(text):
    """Yield (lineno, line) for each logical line that is not blank and not a comment."""
    lines = text.replace("\r\n", "\n").split("\n")
    i = 0
    while i < len(lines):
        lineno = i + 1
        line = lines[i]
        i += 1
        stripped = line.strip(_WHITESPACE)
        if not stripped or stripped[0] in ";#":
            continue
        while line.endswith("\\"):
            line = line[:-1]
            if i >= len(lines):
                break
            line += lines[i].lstrip(_WHITESPACE)
            i += 1
        yield lineno, line


def _section_name(line, lineno):
    if not line.endswith("]"):
        raise IniError("unterminated section header", lineno)
    name = line[1:-1].strip(_WHITESPACE)
    if not name or "[" in name or "]" in name:
        raise IniError(f"invalid section name {name!r}", lineno)
    return name


def _unquote(value):
    if len(value) >= 2 and value[0] in "\"'" and value[-1] == value[0]:
        return value[1:-1]
    return value


def _key_value(line, lineno):
    cuts = [pos for pos in (line.find("="), line.find(":")) if pos != -1]
    if not cuts:
        raise IniError("expected 'key = value'", lineno)
    cut = min(cuts)
    key = line[:cut].strip(_WHITESPACE).lower()
    if not key:
        raise IniError("empty key", lineno)
    return key, _unquote(line[cut + 1:].strip(_WHITESPACE))


def loads(text, *, duplicates="last"):
    """Parse the INI text `text` and return a Config.

    `duplicates` is "last", "first" or "error" (see README.md, "Duplicate keys").
    Raises IniError for malformed text and ValueError for an unknown `duplicates` value.
    """
    if duplicates not in _POLICIES:
        raise ValueError(f"duplicates must be one of {_POLICIES}, not {duplicates!r}")
    data = {}
    section = None
    for lineno, line in _logical_lines(text):
        line = line.strip(_WHITESPACE)
        if line.startswith("["):
            section = data.setdefault(_section_name(line, lineno), {})
            continue
        if section is None:
            raise IniError("key/value line before the first section header", lineno)
        key, value = _key_value(line, lineno)
        if key in section:
            if duplicates == "error":
                raise IniError(f"duplicate key {key!r}", lineno)
            if duplicates == "first":
                continue
        section[key] = value
    return Config(data)
