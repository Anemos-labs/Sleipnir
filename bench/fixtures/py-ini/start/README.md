# ini

A small INI reader: `loads(text)` turns a string into a `Config`.

```python
from ini import loads

cfg = loads("""
; global settings
[server]
Host = example.org
port: 8080
debug = Yes
""")

cfg.sections()                  # ['server']
cfg.get("server", "host")       # 'example.org'  (keys are case-insensitive)
cfg.getint("server", "port")    # 8080
cfg.getbool("server", "debug")  # True
cfg.get("server", "nope", "-")  # '-'            (the default for a missing key)
```

Run the tests from the repository root with `python3 -m unittest discover -s tests -v`.

## The text format

The input is a `str`. The rules below are applied in this order.

### 1. Lines

Lines end with `\n` or `\r\n`; `\r\n` is treated exactly like `\n`. Line numbers are 1-based and
count physical lines. "Whitespace" below always means spaces and tabs only.

### 2. Blank lines and comments

A line that is empty or holds only whitespace is ignored. So is a **comment line**: a line whose
first non-whitespace character is `;` or `#`.

Comments exist only as whole lines. There are no inline comments: a `;` or `#` inside a line that
has other content is an ordinary character (`url = http://x/#top` has the value `http://x/#top`,
and `a = 1 ; two` has the value `1 ; two`).

### 3. Continuation lines

If a line that is not blank and not a comment ends with a backslash `\` (the backslash is the very
last character of the physical line, right before its line ending), the backslash is removed and
the next physical line is appended to it after removing that next line's leading whitespace. The
result is one **logical line**, which is what the rules below see.

```ini
path = /usr/bin:\
       /usr/local/bin
greeting = hello \
           world
```

gives `path` the value `/usr/bin:/usr/local/bin` and `greeting` the value `hello world` (the space
before the backslash is kept).

- The appended line is taken as it is, whatever it contains: if it is blank, starts with `;` or
  `#`, or looks like a section header, it is still just text to append. If it also ends with a
  backslash, the joining goes on.
- A comment line is never continued: `# note \` ends at that line.
- A backslash followed by anything else, even one space, is an ordinary character: the line
  `a = x\ ` (a space after the backslash) has the value `x\`.
- A backslash at the very end of the text has nothing to join: it is just removed.
- Errors in a logical line are reported with the number of its **first** physical line.

### 4. Section headers

A logical line whose first non-whitespace character is `[` is a section header. With surrounding
whitespace removed it must end with `]`, and the name between the brackets, with its own
surrounding whitespace removed, must not be empty and must not contain `[` or `]`. So `[server]`,
`  [ server ]  ` and `[my section]` are headers (names `server`, `server` and `my section`), while
`[]`, `[ ]`, `[a]]`, `[[a]]`, `[a` and `[a] x` are errors.

### 5. Key/value lines

Every other logical line is a key/value line. It is split at its first `=` or `:`, whichever comes
first. The key is the text before that character and the value is the text after it, each with
surrounding whitespace removed. So `url = http://h:80/a=b` has key `url` and value `http://h:80/a=b`,
`time: 12:30` has key `time` and value `12:30`, and `my key = 1` has the key `my key`. The value
may be empty (`a =` gives `""`). A line without `=` or `:`, or with an empty key, is an error.

### 6. Quotes

If the value is at least two characters long and its first and last characters are the same quote
character (`"` or `'`), that outer pair is removed and the text between is the value exactly as
written, spaces included. There are no escape sequences. Only one pair is removed. In every other
case quotes are ordinary characters. Keys are never unquoted.

| line                | value       | line                 | value        |
|---------------------|-------------|----------------------|--------------|
| `a = "hello"`       | `hello`     | `a = "a" "b"`        | `a" "b`      |
| `a = '  padded  '`  | `  padded  `| `a = ""x""`          | `"x"`        |
| `a = ""`            | (empty)     | `a = "unterminated`  | `"unterminated` |
| `a = "say 'hi'"`    | `say 'hi'`  | `a = "mixed'`        | `"mixed'`    |
| `a = 'say "hi"'`    | `say "hi"`  | `a = "`              | `"`          |

### 7. Sections

A key/value line belongs to the most recent section header. A key/value line before the first
header is an error. A section may be opened again later in the text: its later lines continue the
same section. A section without keys is allowed.

### Case

Section names are **case-sensitive**: `[Core]` and `[core]` are different sections. Keys are
**case-insensitive**: a key is stored lower-cased (with `str.lower()`), and every lookup lower-cases
the key it is given, so `Host`, `HOST` and `host` are the same key. Values keep their case.

### Duplicate keys

`loads(text, *, duplicates="last")`. The same key (after lower-casing) defined twice in the same
section, also when the section was opened again in between, is resolved by `duplicates`:

- `"last"` (the default): the later value replaces the earlier one. The key keeps the position of
  its first appearance.
- `"first"`: the first value is kept and later definitions are ignored.
- `"error"`: the later definition raises `IniError` (reported at its line).

Any other value for `duplicates` raises `ValueError` before anything is parsed. The same key in
different sections is never a duplicate.

## The API

`loads(text, *, duplicates="last") -> Config`

A `Config` offers:

- `sections() -> list[str]`: the section names, each once, in order of first appearance.
- `options(section) -> list[str]`: the (lower-cased) keys of a section in order of first
  appearance. Raises `KeyError` if the section does not exist.
- `get(section, key, default=None) -> str | None`: the value, or `default` if the section or the
  key does not exist. An empty value is a value, not a missing key.
- `getint(section, key, default=None) -> int | None`
- `getbool(section, key, default=None) -> bool | None`
- `as_dict() -> dict[str, dict[str, str]]`: all sections and keys as new plain dicts.
  `sections()`, `options()` and `as_dict()` return copies: changing what they return never changes
  the `Config`.

### Typed getters

Both work like `get` when the section or key is missing: they return `default` unchanged. When the
key exists but its value is not one of the accepted spellings they raise `ValueError`; `default`
is never used to hide a malformed value.

- `getint` accepts an optional `+` or `-` followed by one or more ASCII digits `0`-`9`, and nothing
  else: no spaces, no underscores, no `0x` prefix, no decimal point, no exponent and no non-ASCII
  digits. Leading zeros are fine (`007` is `7`, `-0` is `0`).
- `getbool` compares the value, lower-cased with `str.lower()`, with these spellings only:
  `1`, `true`, `yes`, `on` give `True`; `0`, `false`, `no`, `off` give `False`. Anything else, the
  empty string and values with surrounding spaces (possible when quoted) included, is an error.

### Errors

`IniError(message, lineno)` (in `ini/errors.py`) is a subclass of `ValueError`. Its attribute
`lineno` is the line number described in rule 3. `IniError` is raised for exactly these
situations and no others: a key/value line before the first section header (rule 7), a malformed
section header (rule 4), a key/value line without a separator or with an empty key (rule 5), and a
duplicate key when `duplicates="error"`.

## A complete example

```ini
; a comment
[server]
Host = example.org
port: 8080
url = http://example.org:8080/a=b#top
motto = "  keep   spaces  "
paths = /usr/bin:\
        /usr/local/bin

[flags]
debug = Yes
retries = +3
[server]
extra = 1
```

`loads(text).as_dict()` is

```python
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
}
```
