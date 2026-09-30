# csv

A CSV parser and writer in the style of RFC 4180.

```js
const { parse, stringify, CsvError } = require('./src');

parse('name,qty\r\nwidget,3\r\n');
// [['name', 'qty'], ['widget', '3']]

parse('name,qty\nwidget,3\n', { header: true });
// [{ name: 'widget', qty: '3' }]

stringify([['name', 'qty'], ['say "hi"', 3]]);
// 'name,qty\r\n"say ""hi""",3\r\n'
```

Run the tests from the repository root with `node --test test/*.test.js` (Node 22, no
dependencies). The entry point is `src/index.js`; `CsvError` is already defined in
`src/errors.js`.

## `parse(text, options = {})`

Returns an array. `text` must be a string, otherwise a `TypeError` is thrown.

### Options

| option      | default   | meaning |
|-------------|-----------|---------|
| `delimiter` | `','`     | The field separator: a string of exactly one character that is not `"`, CR (`\r`) or LF (`\n`). Anything else (a longer or empty string, a non-string, `null`) throws a `RangeError`. |
| `header`    | `false`   | If `true`, the first record holds the column names and the result is an array of objects instead of an array of arrays. |
| `ragged`    | `'error'` | What to do with a record that has another number of fields than the first record: `'error'`, `'pad'` or `'keep'`. Any other value throws a `RangeError`. |

`undefined` counts as "not given". The options are checked before the text is read, so an invalid
option throws even for an empty text.

### Records and blank lines

1. A record ends at a line break, which is `\n` or `\r\n`; both may occur in one text. A `\r` that
   is **not** followed by `\n` is not a line break but an ordinary character.
2. A **blank line** (nothing at all between two line breaks, before the first one, or after the
   last one) gives no record. So `''`, `'\n'` and `'\r\n\r\n'` all give `[]`, and a text gives the
   same result with or without a line break at its very end. A line that holds only spaces is not
   blank: it is a record with one field. A line that holds just `""` is a record with one empty
   field.

### Fields

3. The fields of a record are separated by the delimiter. A record has one more field than it has
   delimiters outside quotes: `a,b,` has three fields (`'a'`, `'b'` and `''`), `,` has two empty
   ones, `a,,b` has an empty field in the middle.
4. An **unquoted** field is the text up to the next delimiter or line break, taken as it is: spaces
   are kept, nothing is trimmed. It must not contain a double quote.
5. A field is **quoted** if its first character is `"`. It ends at the next `"` that is not half of
   a pair `""`. Inside the quotes `""` stands for one `"`, and every other character (the delimiter,
   `\n`, `\r` and `\r\n` included) is part of the value exactly as written: line breaks are not
   changed. The character right after the closing quote must be the delimiter, a line break or the
   end of the text.

In this table the first column shows the characters of the text; `\n`, `\r` and `\r\n` stand for
the real line-break characters.

| text | result |
|------|--------|
| `a,b,c` | `[['a', 'b', 'c']]` |
| `a,,b` | `[['a', '', 'b']]` |
| `,` | `[['', '']]` |
| `a,` | `[['a', '']]` |
| ` a , b ` | `[[' a ', ' b ']]` |
| `"a,b",c` | `[['a,b', 'c']]` |
| `"say ""hi"""` | `[['say "hi"']]` |
| `""""` | `[['"']]` |
| `""` | `[['']]` |
| `"a\nb",c` | `[['a\nb', 'c']]` |
| `"a\r\nb"` | `[['a\r\nb']]` (the `\r\n` is kept) |
| `a\rb,c` (a lone CR) | `[['a\rb', 'c']]` |
| `a\n\n\nb\n` | `[['a'], ['b']]` |

### Errors

`CsvError` (from `src/errors.js`, constructor `new CsvError(message, record)`) has the property
`record`: the **1-based number of the record in which the problem was found**. Blank lines are not
records, and in header mode the header record is record 1. When a text has several problems, the one
in the earliest record is reported. A `CsvError` is thrown for exactly these situations:

- a double quote inside an unquoted field: `ab"c`, and also `a, "b"` (the second field starts with
  a space, so it is unquoted);
- anything other than the delimiter, a line break or the end of the text right after the closing
  quote of a quoted field: `"ab"c`, `"ab" ,x`;
- a quoted field that is never closed (the text ends inside it);
- a record that breaks the `ragged` policy.

### `ragged`

Let the **width** be the number of fields of the first record (the header record in header mode).
Every later record with a different number of fields is handled by the policy:

- `'error'` (the default): a `CsvError` for that record.
- `'pad'`: a record with fewer fields is padded at the end with empty strings up to the width. A
  record with more fields is a `CsvError`.
- `'keep'`: nothing is checked or changed. In array mode the record is returned as it is. In header
  mode the object of a record gets only the keys it has a value for (a short record gives an object
  with fewer keys), and values beyond the number of columns are dropped.

### Header mode

With `header: true` the first record is the list of column names (strings, exactly as parsed). The
result holds one **plain object** (an ordinary object, as written with `{}`) per following record,
with the column names as keys and the field texts as values. If a name occurs twice the later
column wins. A text without records, or with only the header record, gives `[]`.

```js
parse('a,b\n1,2\n3,4\n', { header: true });   // [{ a: '1', b: '2' }, { a: '3', b: '4' }]
parse('a,b\n1\n', { header: true, ragged: 'pad' });   // [{ a: '1', b: '' }]
parse('a,b\n1\n', { header: true, ragged: 'keep' });  // [{ a: '1' }]
parse('a,b\n1\n');                                    // throws CsvError, record 2
```

## `stringify(rows, options = {})`

Returns a string.

### Options

| option      | default    | meaning |
|-------------|------------|---------|
| `delimiter` | `','`      | As for `parse`. |
| `newline`   | `'\r\n'`   | The line break that ends every record: `'\r\n'` or `'\n'`. Anything else throws a `RangeError`. |
| `columns`   | (none)     | An array of column names. If given, `rows` are objects, see below. |

As for `parse`, an option that is `undefined` counts as "not given".

### Rows

- `rows` must be an array, otherwise a `TypeError` is thrown. Without `columns` every row must be
  an array of cells, otherwise a `TypeError`.
- A **cell** that is `null` or `undefined` is written as the empty string. Every other cell is
  converted with `String(cell)` (numbers, booleans, ...).
- A record with no cells at all (an empty row array) throws a `RangeError`: it would be a blank
  line, which `parse` skips.
- With `columns`, the output starts with one record holding the column names, followed by one record
  per row. Every row must then be an object that is not `null` and not an array, otherwise a
  `TypeError`. Its cells are the values of the properties named by `columns`, in that order; a
  property that the object does not have counts as `undefined`. Other properties of the object are
  ignored. With no rows the output is just the header record.

### Quoting

A field is written in double quotes if, after conversion, it contains the delimiter, a double quote,
`\n` or `\r`, or if it is the empty string **and the only field of its record** (so that the record is
not a blank line). Inside the quotes every `"` is doubled. Every other field is written as it is:
spaces never cause quoting.

### Line breaks

Every record, the last one included, is followed by the newline. `stringify([])` is `''`.

| call | result |
|------|--------|
| `stringify([['a', 'b'], ['c', 'd']])` | `'a,b\r\nc,d\r\n'` |
| `stringify([['a', 'b']], { newline: '\n' })` | `'a,b\n'` |
| `stringify([['a,b', 'say "hi"', 'x\ny', ' z ']])` | `'"a,b","say ""hi""","x\ny", z \r\n'` |
| `stringify([['', ''], [''], ['a'], [null]])` | `',\r\n""\r\na\r\n""\r\n'` |
| `stringify([[1, true, null, 2.5]])` | `'1,true,,2.5\r\n'` |
| `stringify([['a;b', 'c,d']], { delimiter: ';' })` | `'"a;b";c,d\r\n'` |
| `stringify([{ a: 1, b: 'x' }, { b: 'y' }], { columns: ['a', 'b'] })` | `'a,b\r\n1,x\r\n,y\r\n'` |

### Round trip

For every array `rows` of non-empty arrays of strings that all have the same length, and for any
valid `delimiter` and `newline`, `parse(stringify(rows, { delimiter, newline }), { delimiter })`
returns `rows`. For objects: `parse(stringify(objects, { columns }), { header: true })` returns
`objects` when every object has exactly the properties in `columns` and all values are strings.
