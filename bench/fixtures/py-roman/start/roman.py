"""Conversion between integers and Roman numerals. See README.md for the specification."""

MAX_VALUE = 3999

# Symbols in descending order of value, subtractive pairs included.
_SYMBOLS = (
    (1000, "M"), (900, "CM"), (500, "D"), (400, "CD"),
    (100, "C"), (90, "XC"), (50, "L"), (40, "XL"),
    (10, "X"), (9, "IX"), (5, "V"), (4, "IV"), (1, "I"),
)

_VALUES = {"I": 1, "V": 5, "X": 10, "L": 50, "C": 100, "D": 500, "M": 1000}


def to_roman(n):
    """Return the canonical Roman numeral for the integer n (1..3999)."""
    if not isinstance(n, int):
        raise TypeError(f"expected an int, got {type(n).__name__}")
    if not 1 <= n < MAX_VALUE:
        raise ValueError(f"out of range: {n}")
    parts = []
    for value, symbol in _SYMBOLS:
        while n >= value:
            parts.append(symbol)
            n -= value
    return "".join(parts)


def from_roman(s):
    """Return the integer for the canonical Roman numeral s."""
    if not isinstance(s, str):
        raise TypeError(f"expected a str, got {type(s).__name__}")
    total = 0
    previous = 0
    # Scan from the right: a symbol smaller than the one to its right is subtracted.
    for ch in reversed(s):
        value = _VALUES.get(ch)
        if value is None:
            raise ValueError(f"invalid roman numeral: {s!r}")
        if value <= previous:
            total -= value
        else:
            total += value
        previous = value
    # Only the canonical spelling is valid: IIII, VX, IC, ... must be rejected.
    if not 1 <= total <= MAX_VALUE or to_roman(total) != s:
        raise ValueError(f"invalid roman numeral: {s!r}")
    return total
