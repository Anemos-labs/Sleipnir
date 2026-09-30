# rpn

A small reverse-polish-notation (postfix) calculator library. `tokenize` splits a string into tokens, `evaluate` runs a
token list on a stack machine, and `eval` does both.

```rust
pub enum Token { Num(f64), Add, Sub, Mul, Div, Rem, Pow, Neg }

pub enum RpnError { DivisionByZero, StackUnderflow, UnknownToken(String), TooManyOperands }

pub fn tokenize(input: &str) -> Result<Vec<Token>, RpnError>;
pub fn evaluate(tokens: &[Token]) -> Result<f64, RpnError>;
pub fn eval(input: &str) -> Result<f64, RpnError>;
```

The types, their derives and the function signatures are already in `src/` and must stay exactly as they are (the tests
are compiled against them). The function bodies are `todo!()`: fill them in. Only the standard library is available (no
crates, the build is offline).

## Tokens

`tokenize` splits `input` into *words*. Words are separated by runs of whitespace (space, tab, `\n`, `\r`); whitespace
at the start or the end is ignored. An input without any word (empty, or only whitespace) gives `Ok(vec![])`.

Each word becomes exactly one token:

| word | token |
|------|-------|
| `+` `-` `*` `/` `%` `^` | `Add` `Sub` `Mul` `Div` `Rem` `Pow` |
| `neg` | `Neg` |
| a number: one or more ASCII digits, optionally followed by `.` and one or more ASCII digits, i.e. `[0-9]+(\.[0-9]+)?` | `Num(v)`, where `v` is the `f64` nearest to the decimal value (what `str::parse::<f64>` returns for that text) |
| anything else | the error `UnknownToken(word)`, with the word exactly as it was written |

Words are matched whole and case-sensitively, and only whitespace separates words: `4+` is one word, not two.

The number syntax is strict: no sign, no exponent, no missing digits. So `-3`, `+3`, `.5`, `5.`, `1e3`, `1_000`,
`0x10`, `inf`, `nan`, `1.2.3` and `NEG` are all unknown words (a negative number is written `3 neg`). Leading zeros are
fine: `007` is the number 7.

If several words are unknown, `tokenize` reports the first one (the leftmost).

## Evaluation

`evaluate` runs the tokens from left to right on a stack of `f64` values that starts empty:

- `Num(x)` pushes `x`.
- `Neg` pops one value and pushes its negation.
- every other operator pops two values, first `b` (the top of the stack) and then `a` (the value below it), and pushes
  `a op b`. The operands keep the order in which they were written: `10 4 -` is 10 - 4 = 6.

| token | pushes |
|-------|--------|
| `Add` | `a + b` |
| `Sub` | `a - b` |
| `Mul` | `a * b` |
| `Div` | `a / b` |
| `Rem` | `a % b`, Rust's `%` on `f64` (truncated division): the result takes the sign of `a`, so `-7 % 3` is -1 and `7 % -3` is 1 |
| `Pow` | `a.powf(b)` |

Errors (the first error stops the evaluation):

- an operator needs more values than the stack holds: `StackUnderflow`
- `Div` or `Rem` with `b` equal to zero (negative zero included): `DivisionByZero`

Nothing else is an error. In particular `Pow` never fails, and overflow is not checked: the result is whatever the `f64`
operation returns.

When the last token has been processed the stack must hold exactly one value, and that value is the result. An empty
stack (this includes `evaluate(&[])`) is `StackUnderflow`; two or more values are `TooManyOperands`.

## eval

`eval(input)` tokenizes the whole input first and only then evaluates the tokens. So an unknown word anywhere in the
input is reported instead of any evaluation error, even one that would have happened earlier in the input: `1 0 / foo`
is `UnknownToken("foo")`, not `DivisionByZero`.

## Display

`RpnError` implements `std::fmt::Display` with exactly these messages (`std::error::Error` is already implemented):

| variant | message |
|---------|---------|
| `DivisionByZero` | `division by zero` |
| `StackUnderflow` | `stack underflow` |
| `UnknownToken(t)` | `unknown token: ` followed by `t`, e.g. `unknown token: foo` |
| `TooManyOperands` | `too many operands` |

## Examples

| call | result |
|------|--------|
| `eval("3 4 +")` | `Ok(7.0)` |
| `eval("5 1 2 + 4 * + 3 -")` | `Ok(14.0)` |
| `eval("10 4 -")` | `Ok(6.0)` |
| `eval("7 2 /")` | `Ok(3.5)` |
| `eval("2 3 2 ^ ^")` | `Ok(512.0)` (2 to the power of 9) |
| `eval("7 neg 3 %")` | `Ok(-1.0)` |
| `eval("5 neg neg")` | `Ok(5.0)` |
| `eval("  2.5\t4 *\n")` | `Ok(10.0)` |
| `eval("")` | `Err(StackUnderflow)` |
| `eval("1 +")` | `Err(StackUnderflow)` |
| `eval("1 2")` | `Err(TooManyOperands)` |
| `eval("1 2 3 +")` | `Err(TooManyOperands)` |
| `eval("1 0 /")` | `Err(DivisionByZero)` |
| `eval("4 0 neg %")` | `Err(DivisionByZero)` |
| `eval("1 0 / foo")` | `Err(UnknownToken("foo".to_string()))` |
| `eval("-3 4 +")` | `Err(UnknownToken("-3".to_string()))` |
| `eval("3 4+")` | `Err(UnknownToken("4+".to_string()))` |
| `tokenize("2 neg")` | `Ok(vec![Token::Num(2.0), Token::Neg])` |
| `tokenize("1.50 %")` | `Ok(vec![Token::Num(1.5), Token::Rem])` |
| `evaluate(&[])` | `Err(StackUnderflow)` |
| `evaluate(&[Token::Num(9.0), Token::Num(3.0), Token::Sub])` | `Ok(6.0)` |

## Checking your work

```
cargo test --offline --quiet
```

`tests/examples.rs` holds some of the examples above. The complete set of checks (more inputs, all error cases and the
`Display` messages) is run when your work is verified, and it follows this document only.
