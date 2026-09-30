# jsonpath

`jsonpath` evaluates a small subset of JSONPath over a document that was decoded
with `encoding/json`.

```go
// Eval returns the values of doc that path selects, in the order defined below.
func Eval(doc any, path string) ([]any, error)
```

`doc` is a value as `json.Unmarshal` produces it when decoding into an `any`:
`map[string]any` (object), `[]any` (array), `string`, `float64`, `bool` or `nil`
(JSON null). The selected values are returned as they are in `doc`, not copies.
`Eval` never panics, whatever the path.

This is a deliberately small subset. Where it differs from full JSONPath (no `.*`,
no negative indexes, no filters, `..` only with a plain name), this file is what
counts.

## Path syntax

A path is `$` followed by zero or more segments:

| segment | meaning |
|---|---|
| `.name` | child: the member called `name` of an object |
| `['text']` | child: the member called `text` of an object |
| `[n]` | the element with index `n` of an array (the first element is 0) |
| `[*]` | wildcard: all elements of an array, or all member values of an object |
| `..name` | recursive descent: members called `name` anywhere below (see below) |

- `name` is one or more of the characters `A-Z a-z 0-9 _` (ASCII only, and it may
  start with a digit). A member called `a-b` or `café` can only be reached with
  `['a-b']` or `['café']`. Names are matched exactly, including case.
- `text` is any sequence of characters other than `'`, possibly empty. It ends at
  the first `'`. There are no escape sequences (a backslash is an ordinary
  character) and double quotes are not supported.
- `n` is one or more decimal digits. There are no signs: `[-1]` is not valid.
- There is no whitespace anywhere outside `'...'`.

Anything else is a **syntax error**. In particular these are all invalid: an empty
path or one that does not start with `$`; a `.` or `..` that is not followed
directly by a name (so `$.`, `$.*`, `$.[0]`, `$..*`, `$..[0]`, `$..['a']` and
`$...a` are invalid); a bracket that holds anything but `*`, digits, or a quoted
text (so `[a]`, `[1,2]`, `[0:2]`, `[?(@.a)]`, `[ 0 ]` and `["a"]` are invalid); a
`[` or a `'` that is never closed; and any character that cannot start a segment
(`$ `, `$.a-b`, `$a`, `$[0]]`).

## Evaluation

Start with a list that holds only `doc`. For each segment in turn, apply it to
every value of the list, in order, and concatenate what it selects. The final
list is the result, so `$` alone selects `doc` itself: the result is `[doc]`.
Nothing is de-duplicated: a value that is selected twice appears twice.

- **`.name` and `['text']`** select the member value if the current value is an
  object that has that member. On anything else (an array, a string, a number, a
  boolean, null) they select nothing. A member whose value is `null` is selected
  like any other value.
- **`[n]`** selects element `n` if the current value is an array with more than
  `n` elements. Otherwise it selects nothing. In particular it never indexes an
  object, and a number too large for an `int` is valid and selects nothing.
- **`[*]`** selects, for an array, all its elements in index order; for an object,
  all its member values in **ascending order of member name**, compared as plain Go
  strings (byte by byte, so `"B"` comes before `"a"`), because a Go map has no
  order of its own; for anything else, nothing.
- **`..name`** walks the current value and everything inside it in *pre-order*:

  > visit(v): if v is an object that has a member `name`, select that member's
  > value; then visit each child of v in turn, where the children of an array are
  > its elements in index order and the children of an object are its member
  > values in ascending order of member name.

  So a value's own `name` member is selected before anything found deeper inside
  it, and each subtree is finished before the next sibling starts.

## Results and errors

- A valid path that selects nothing is **not an error**: `Eval` returns an empty
  result (length 0) and a nil error. This includes a path into something that is
  not there, such as `$.a.b` on `{"a": 1}`.
- A path that is not valid according to the syntax above makes `Eval` return a nil
  result and a non-nil error. The whole path is checked **before** anything is
  evaluated, so `$.missing[abc]` is an error even though `.missing` selects
  nothing, and `Eval(nil, "$.")` is an error too.

## Examples

With this document:

```json
{
  "store": {
    "book": [
      {"title": "Sayings", "price": 8.95},
      {"title": "Sword", "price": 12.99, "isbn": "0-553-21311-3"}
    ],
    "bicycle": {"color": "red", "price": 19.95}
  }
}
```

| path | result |
|---|---|
| `$.store.bicycle.color` | `["red"]` |
| `$['store']['book'][1].title` | `["Sword"]` |
| `$.store.book[*].title` | `["Sayings", "Sword"]` |
| `$.store.book[*].isbn` | `["0-553-21311-3"]` (the first book has no isbn) |
| `$..price` | `[19.95, 8.95, 12.99]` (`bicycle` sorts before `book`) |
| `$.store.book[5]` | `[]` |
| `$.nothing.here` | `[]` |
| `$.store[` | error |

Recursive descent selects a value's own member before the ones inside it. With
the document `{"a": {"x": 5}, "x": 6}`, the path `$..x` gives `[6, 5]`: the `x`
of the top-level object first, then the `x` inside `a`.

Nothing is de-duplicated. With `{"a": {"a": {"b": 1}}}`, the path `$..a` selects
the outer `a` value and then the inner one, and `..b` is applied to both, so
`$..a..b` gives `[1, 1]`.

## Checking your work

```
go test ./...
```

`jsonpath_test.go` contains the examples above. More tests that check the rules
above are run when your solution is verified.
