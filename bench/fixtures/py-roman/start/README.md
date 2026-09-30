# roman

Convert between integers and Roman numerals.

```python
from roman import to_roman, from_roman

to_roman(1994)          # 'MCMXCIV'
from_roman('MCMXCIV')   # 1994
```

Run the tests from the repository root:

```
python3 -m unittest discover -s tests -v
```

## Specification

The supported range is **1 to 3999, both ends included**. Numerals are written with
upper-case letters `I V X L C D M` (1, 5, 10, 50, 100, 500, 1000) in standard subtractive
notation. The only subtractive pairs are `IV` (4), `IX` (9), `XL` (40), `XC` (90), `CD` (400)
and `CM` (900).

| number | numeral            | number | numeral |
|-------:|--------------------|-------:|---------|
|      1 | `I`                |     90 | `XC`    |
|      3 | `III`              |    400 | `CD`    |
|      4 | `IV`               |    900 | `CM`    |
|      9 | `IX`               |   1994 | `MCMXCIV` |
|     14 | `XIV`              |   2024 | `MMXXIV` |
|     40 | `XL`               |   3888 | `MMMDCCCLXXXVIII` |
|     49 | `XLIX`             |   3999 | `MMMCMXCIX` |

### `to_roman(n)`

Returns the numeral for the integer `n` as a `str`.

- `n` outside 1..3999 (0, negative numbers, 4000 and above) raises `ValueError`. The message
  starts with `out of range`, for example `out of range: 4000`.
- If `n` is not an `int` (for example `'12'`, `3.0` or `None`) it raises `TypeError`.

### `from_roman(s)`

Returns the integer value of the numeral `s` as an `int`.

Only the **canonical** spelling is accepted: `s` is valid if and only if it is exactly what
`to_roman` returns for some number in 1..3999. Everything else raises `ValueError` whose message
starts with `invalid roman numeral`, for example `invalid roman numeral: 'IIII'`. In particular
these are all invalid:

- the empty string;
- non-canonical spellings such as `IIII` (the numeral for 4 is `IV`), `VV`, `VX`, `IC` (99 is
  `XCIX`), `IIX`, `XXXX`, `VIV`, `CMC`;
- `MMMM` and anything else above 3999;
- lower-case letters (`iv`), spaces (`' IV'`), and any character that is not one of `IVXLCDM`.

If `s` is not a `str` (for example `5`, `None` or `b'V'`) it raises `TypeError`.

For every `n` in 1..3999, `from_roman(to_roman(n)) == n`.
