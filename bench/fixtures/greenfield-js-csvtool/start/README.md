# csvtool

A command line tool for small CSV files: `head`, `select`, `filter`, `sort` and `stats`. Node.js 22, no packages: only
the `node:` built-in modules. This document is the complete specification. Where it speaks it is exact; where it is
silent the behaviour is not specified and not tested.

## What to build

The entry point is `bin/csvtool.js`, run as

```
node bin/csvtool.js COMMAND [ARGUMENTS...] [FILE]
```

It reads CSV from `FILE`, or from standard input when there is no `FILE`, and writes CSV to standard output. You may put
the code in any modules under `src/`; `package.json` is given (CommonJS). Check your work with `npm test` (which runs
`node --test test/*.test.js`). `test/example.test.js` shows how the tests run the tool: they spawn
`node bin/csvtool.js ...` with `child_process`, feed it standard input, and compare the exit status, standard output and
standard error exactly. The complete acceptance tests, which cover everything below, are run when your work is verified.

## Command line

The first argument is the command. The remaining arguments are *options* when they start with `--`, and *positional*
arguments otherwise (so `-5` is positional). Options may appear anywhere after the command. The positional arguments
are the command's own arguments, in order, and then optionally the `FILE`.

| command | own arguments | options | what it does |
|---------|---------------|---------|--------------|
| `head` | `N` | | the header and the first `N` data rows |
| `select` | `COLS` | | only the listed columns, in the listed order |
| `filter` | `COL OP VALUE` | | the rows for which the condition holds |
| `sort` | `COL` | `--desc`, `--numeric` | the rows sorted by a column |
| `stats` | `COL` | | count, min, max and mean of a numeric column |

So `head 5` reads standard input, and `head 5 data.csv` reads `data.csv`. A command given options it does not take
treats them as unknown (`head 3 --desc` is an error).

## The CSV format

**Reading.** The input is UTF-8 text.

- Records are separated by `\n` or `\r\n`; the last record may have no line terminator.
- Fields in a record are separated by `,`. A record with n commas has n+1 fields, empty ones included: `a,,b` has three
  fields, and `a,b,` has three, the last one empty.
- A field is *unquoted* or *quoted*. An unquoted field is everything up to the next `,` or line terminator, taken
  literally (spaces included, nothing is trimmed); it must not contain a `"`. A quoted field starts with `"` and ends
  with the next `"` that is not doubled: inside it `""` stands for one `"`, and commas, `\n` and `\r` are ordinary
  characters. After the closing `"` the next character must be a `,` or a line terminator, or the input must end.
- An empty line (nothing between two line terminators, or before the first one) is skipped and is not a record. (A line
  with spaces on it is a record; `""` is a record with one empty field.)
- The first record is the *header*: the column names. Every other record is a *data row*, and must have as many fields
  as the header.

**Writing.** Every record is written as its fields joined by `,` and followed by `\n` (also the last one). A field is
written in quotes, with every `"` inside doubled, if it contains a `,`, a `"`, a `\n` or a `\r`, or if it is the only
field of its record and is empty (so that the line is not empty); otherwise it is written as it is. So the output
does not depend on how the input was quoted, and `\r\n` line endings come out as `\n`. The header is written like any
other record.

## Columns and numbers

A column is named by the text of its header field, matched exactly (case-sensitive, nothing trimmed). If two columns
have the same name the first one is meant.

A *number* is a value that matches `-?[0-9]+(\.[0-9]+)?`: an optional minus sign, digits, and optionally a dot with
more digits. Nothing else is a number: not `+5`, `.5`, `5.`, `1e3`, ` 5` (with a space), `0x10`, `NaN` or the empty
string. Leading zeros are fine (`007` is 7).

**Comparing.** `=`, `!=`, `<`, `<=`, `>` and `>=` compare a cell with the `VALUE`: if both are numbers they are compared
as numbers (so `1.0 = 1`, and `9 < 10`); otherwise as strings, character by character by UTF-16 code unit, the way
JavaScript's `<` compares strings (so `"9" > "10"`, `"B" < "a"`, and the empty string is less than any other). The
operator `contains` is true when the cell has `VALUE` as a substring (case-sensitive; an empty `VALUE` is in every
cell).

**Number format** (used by `stats`): the value is rounded to the nearest multiple of 0.0001 and written in plain
decimal notation without trailing zeros and without a decimal point when it is whole: `3`, `2.5`, `3.3333`, `-0.3333`;
zero is always `0`.

## The commands

**`head N [FILE]`**: the header, then the first `N` data rows (all of them if there are fewer). `N` is a non-negative
integer written with the ASCII digits `0` to `9` only (`0`, `5`, `007`); `head 0` prints just the header.

**`select COLS [FILE]`**: `COLS` is a list of column names separated by `,` (there is no quoting: a name cannot contain
a comma). The output has these columns in this order, for the header and for every row; a name may be listed twice and
then the column appears twice.

**`filter COL OP VALUE [FILE]`**: the header, then the data rows, in their original order, for which the cell in column
`COL` compared to `VALUE` with `OP` holds. `OP` is one of `=`, `!=`, `<`, `<=`, `>`, `>=`, `contains`. Write `<` and `>`
quoted in a shell; the tool receives them as ordinary arguments. `VALUE` may be any text, including the empty string.

**`sort COL [--desc] [--numeric] [FILE]`**: the header, then the data rows sorted by column `COL`.

- By default the values are compared as strings (by UTF-16 code unit, like JavaScript's `<`), ascending.
- With `--numeric` every value in the column must be a number (see above), and they are compared as numbers
  (so `1.0` and `1` are equal). A value that is not a number is an error, even in a file with a single row.
- With `--desc` the order is reversed: the largest value first.
- The sort is **stable in both directions**: rows whose values compare equal stay in their original relative order,
  also with `--desc`.

**`stats COL [FILE]`**: prints the header `count,min,max,mean` and one data row. The values of column `COL` are taken
from every data row, except that empty values (the empty string) are skipped; every other value must be a number. `count`
is the number of values used, `min` and `max` are the smallest and largest of them and `mean` is their sum divided by
their count (the sum is taken in row order); the three are written in the number format above. When there are no
values at all, the row is `0,,,` (the count, and three empty fields).

## Errors and exit statuses

On an error nothing is written to standard output, exactly one line is written to standard error, and the exit status is
not 0. Every message starts with `csvtool: ` and ends with `\n`.

*Usage errors, exit status 2* (checked first, before any input is read, in this order):

| message | when |
|---------|------|
| `csvtool: missing command` | no arguments at all |
| `csvtool: unknown command: NAME` | the first argument is not one of the five commands |
| `csvtool: unknown option: OPTION` | an option that the command does not take (the first one, left to right) |
| `csvtool: COMMAND: wrong number of arguments` | fewer positional arguments than the command's own, or more than those plus a `FILE` |
| `csvtool: head: N must be a non-negative integer` | `N` does not match `[0-9]+` (`-1`, `1.5`, `abc`, `1e3` and the empty string are all invalid) |
| `csvtool: filter: unknown operator: OP` | `OP` is not one of the seven operators |

*Input errors, exit status 1* (checked in this order):

| message | when |
|---------|------|
| `csvtool: cannot read FILE` | `FILE` does not exist or cannot be read as a file |
| `csvtool: empty input` | the input has no record at all (it is empty, or only empty lines) |
| `csvtool: malformed CSV` | a `"` inside an unquoted field; a quoted field that is not closed before the input ends; text after a closing `"` other than `,` or a line terminator |
| `csvtool: row N has M fields, expected K` | data row number N (counting data rows from 1; the header and skipped empty lines do not count) has M fields, the header K |
| `csvtool: unknown column: NAME` | `COL` (for `select`, each name of `COLS`, the first unknown one) is not a column |
| `csvtool: not a number: VALUE` | `sort --numeric` or `stats` meets a value that is not a number: the first one in row order, shown as it is in the data (unquoted, unescaped) |

(The tests give an input at most one problem of the same kind, so what is reported for, say, two malformed rows is not
specified.)

## Examples

`people.csv`:

```
name,age,city
Ann,31,"Paris, FR"
Bob,25,Rome
"Cy ""the Kid""",40,Oslo
Dee,25,Rome
```

```
$ csvtool head 2 people.csv
name,age,city
Ann,31,"Paris, FR"
Bob,25,Rome

$ csvtool select city,name < people.csv
city,name
"Paris, FR",Ann
Rome,Bob
Oslo,"Cy ""the Kid"""
Rome,Dee

$ csvtool filter age '>=' 30 people.csv
name,age,city
Ann,31,"Paris, FR"
"Cy ""the Kid""",40,Oslo

$ csvtool filter city contains o people.csv       (case-sensitive: "Rome" and "Oslo" contain "o", "Paris, FR" does not)
name,age,city
Bob,25,Rome
"Cy ""the Kid""",40,Oslo
Dee,25,Rome

$ csvtool sort age --numeric people.csv          (stable: Bob before Dee)
name,age,city
Bob,25,Rome
Dee,25,Rome
Ann,31,"Paris, FR"
"Cy ""the Kid""",40,Oslo

$ csvtool sort age --numeric --desc people.csv   (stable: Bob still before Dee)
name,age,city
"Cy ""the Kid""",40,Oslo
Ann,31,"Paris, FR"
Bob,25,Rome
Dee,25,Rome

$ csvtool sort name people.csv                   (strings: "Ann" < "Bob" < "Cy ..." < "Dee")
(the four rows in file order)

$ csvtool stats age people.csv
count,min,max,mean
4,25,40,30.25
```

Other examples. Inputs are written with `\n`; the standard output is shown the same way.

| command | input | standard output | status |
|---------|-------|-----------------|--------|
| `sort n` | `n\n9\n10\n` | `n\n10\n9\n` (strings: `"10" < "9"`) | 0 |
| `sort n --numeric` | `n\n9\n10\n` | `n\n9\n10\n` | 0 |
| `stats v` | `k,v\na,1\nb,\nc,2\nd,2\n` | `count,min,max,mean\n3,1,2,1.6667\n` (the empty value is skipped) | 0 |
| `stats v` | `k,v\na,\nb,\n` | `count,min,max,mean\n0,,,\n` | 0 |
| `stats v` | `k,v\n` | `count,min,max,mean\n0,,,\n` | 0 |
| `select a` | `a,b\n,x\n` | `a\n""\n` (a lone empty field is quoted) | 0 |
| `head 1` | `a\r\n1\r\n2\r\n` | `a\n1\n` | 0 |
| `head 0` | `a,b` (no final newline) | `a,b\n` | 0 |
| `filter v = 1` | `v\n1.0\n01\nx\n` | `v\n1.0\n01\n` (both are numbers equal to 1) | 0 |
| `filter v '<' 10` | `v\n9\nabc\n` | `v\n9\n` (`abc` is not a number: `"abc" < "10"` is false as strings) | 0 |
| `select "first name"` | `"first name",age\nAnn,3\n` | `first name\nAnn\n` | 0 |
| `head -1` | (anything) | nothing; stderr `csvtool: head: N must be a non-negative integer\n` | 2 |
| `frob` | (anything) | nothing; stderr `csvtool: unknown command: frob\n` | 2 |
| `select nope` | `a,b\n1,2\n` | nothing; stderr `csvtool: unknown column: nope\n` | 1 |
| `stats name` | `name\nAnn\n` | nothing; stderr `csvtool: not a number: Ann\n` | 1 |
| `head 1` | `a,b\n1,2\n3\n` | nothing; stderr `csvtool: row 2 has 1 fields, expected 2\n` | 1 |
| `head 1` | `a,b\n1,"2\n` | nothing; stderr `csvtool: malformed CSV\n` | 1 |
| `head 1` | (empty) | nothing; stderr `csvtool: empty input\n` | 1 |
| `head 1 missing.csv` | | nothing; stderr `csvtool: cannot read missing.csv\n` | 1 |
