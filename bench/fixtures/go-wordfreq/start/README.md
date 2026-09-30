# wordfreq

`wordfreq` counts how often words occur in a piece of text.

```go
// Count is a word together with the number of times it occurs.
type Count struct {
	Word  string // the word, in lower case
	Count int    // how many times it occurs
}

// TopN returns the n most frequent words of text, most frequent first.
func TopN(text string, n int, stop []string) []Count
```

Only `TopN` is missing; `Count` is already defined in `wordfreq.go`.

## Rules

1. **Letters.** A *letter* is any character for which `unicode.IsLetter` reports
   true. `a`, `é`, `Ж` and `日` are letters; digits, `_`, spaces, punctuation and
   symbols are not.

2. **Words.** A *word* is a maximal run of letters. Every character that is not a
   letter separates words, so `x1y` is the two words `x` and `y`, and `stop-go`
   is the two words `stop` and `go`.

   One exception: an apostrophe (the ASCII character `'`, U+0027) is part of a word
   when the character immediately before it **and** the character immediately after
   it are both letters. `don't` and `rock'n'roll` are each one word. In `'quoted'`,
   `dogs'` and `it''s` the apostrophes are not between two letters, so they
   separate words (giving `quoted`, `dogs`, and `it`, `s`). A position before the
   start or after the end of the text is not a letter. Other apostrophe-like
   characters such as `’` are ordinary separators.

3. **Case.** Words are compared and reported in lower case, as computed by
   `strings.ToLower`. `Go`, `GO` and `go` are the same word, and `Count.Word` is
   always the lower-case form.

4. **Stop words.** A word is ignored when its lower-case form equals
   `strings.ToLower(s)` for some `s` in `stop` (so the comparison ignores case on
   both sides). `stop` may be nil. Ignored words are not counted at all, so they do
   not occupy one of the `n` places. `TopN` must not modify `stop`.

5. **Order.** The result is sorted by `Count`, highest first. Words with the same
   count are sorted by `Word` ascending, using plain Go string comparison (`<`,
   which compares bytes). For example `zebra` sorts before `élan`, because the first
   byte of `é` is larger than the byte of `z`.

6. **How many.** The result has at most `n` entries. If fewer than `n` different
   words remain, all of them are returned. If `n <= 0` the result is empty
   (length 0).

## Examples

| call | result |
|---|---|
| `TopN("the cat and the hat and the bat", 2, nil)` | `{the 3} {and 2}` |
| `TopN("b a c a b", 3, nil)` | `{a 2} {b 2} {c 1}` |
| `TopN("Élan élan ÉLAN Zèbre zèbre", 5, nil)` | `{élan 3} {zèbre 2}` |
| `TopN("To be or not to be", 3, []string{"TO", "or"})` | `{be 2} {not 1}` |
| `TopN("Don't stop-believing, it's 2 late; rock'n'roll!", 10, nil)` | `{believing 1} {don't 1} {it's 1} {late 1} {rock'n'roll 1} {stop 1}` |

## Checking your work

```
go test ./...
```

`wordfreq_test.go` contains these examples. More tests that check the rules above
are run when your solution is verified.
