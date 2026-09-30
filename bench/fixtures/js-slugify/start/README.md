# slugify-lite

`slugify(text, options)` turns a title into a URL slug: lower-case ASCII words joined by a
separator.

```js
const { slugify } = require('./src/slugify');

slugify('Crème Brûlée, 2 for £5!');          // 'creme-brulee-2-for-5'
slugify('Hello World', { separator: '_' });  // 'hello_world'
slugify('Hello, World! Foo', { maxLength: 6 }); // 'hello'
```

Run the tests from the repository root with `node --test test/*.test.js` (Node 22, no
dependencies).

## `slugify(text, options = {})`

Returns a `string`. `text` must be a string (`typeof text === 'string'`), otherwise a `TypeError`
is thrown.

### How the slug is built

The steps are applied in this order.

1. **Normalize** `text` to Unicode form **NFKD** (`text.normalize('NFKD')`). Compatibility
   characters turn into their plain equivalents (`ﬁ` → `fi`, `①` → `1`, `²` → `2`, `Ａ` → `A`,
   `™` → `TM`, `Ⅻ` → `XII`) and accented letters split into a base letter plus combining marks.
2. **Remove every combining mark**: every character of Unicode general category M
   (`/\p{M}/u`), wherever it occurs. That is more than the usual accent block U+0300 to U+036F: it
   includes, for example, U+20D7 (combining right arrow above) and U+FE0F (variation selector-16).
3. **Lower-case** the result with `toLowerCase()`. This has to come after step 1, because NFKD
   can produce capital letters out of characters that have no case of their own: `™` becomes
   `TM` and must end up as `tm`.
4. A **word** is a maximal run of the ASCII characters `a-z` and `0-9`. **Every other character is
   a word boundary and is dropped.** That covers spaces and other whitespace, all punctuation
   (including `_` and the apostrophe), symbols, and **emoji**. It also covers every letter or
   digit that is still not ASCII after steps 1 to 3: **all non-Latin scripts** (Cyrillic, Greek,
   Arabic, Devanagari, Chinese, Japanese, Korean, ...) and the Latin letters that have no
   decomposition (`ß`, `æ`, `ø`, `ł`, `đ`, ...). Nothing is transliterated, so a title written only
   in such characters gives the empty slug.
5. **Join** the words with the separator. Runs of boundary characters give one separator, and the
   slug never starts or ends with the separator. If there are no words the result is `''`.
6. **Cut to `maxLength`** if that option is given (see below).

### Options

`options` is an optional object. Properties other than the two below are ignored. A property that
is `undefined` counts as not given.

- **`separator`** (default `'-'`): a non-empty string made only of ASCII punctuation characters,
  that is of characters from ``! " # $ % & ' ( ) * + , - . / : ; < = > ? @ [ \ ] ^ _ ` { | } ~``.
  It may be several characters long (`'--'`, `'-_-'`). Anything else (the empty string, a string
  with a letter, digit, space or non-ASCII character in it, or a value that is not a string, `null`
  included) throws a `TypeError`.
- **`maxLength`** (default: no limit): a positive integer (`1`, `2`, `3`, ...). Anything else
  (`0`, negative numbers, `1.5`, `NaN`, a string such as `'10'`, `null`) throws a `RangeError`.
  If the slug is longer than `maxLength` characters it is cut to its first `maxLength` characters
  (the cut may fall inside a word) and then every character at the end that is not `a-z` or `0-9`
  is removed, so the result is at most `maxLength` characters long and **never ends with (a part
  of) a separator**. A slug that is not longer than `maxLength` is returned unchanged.

### Examples

| call | result |
|------|--------|
| `slugify('Hello, World!')` | `'hello-world'` |
| `slugify('  Multiple   spaces -- and---dashes  ')` | `'multiple-spaces-and-dashes'` |
| `slugify('snake_case and CamelCase')` | `'snake-case-and-camelcase'` |
| `slugify("Don't stop")` | `'don-t-stop'` |
| `slugify('Crème Brûlée à la façon')` | `'creme-brulee-a-la-facon'` |
| `slugify('ﬁsh ① x² ＡＢＣ')` | `'fish-1-x2-abc'` |
| `slugify('v⃗ec')` (a `v` with a combining arrow) | `'vec'` |
| `slugify('Party 🎉 time')` | `'party-time'` |
| `slugify('Привет, мир!')` | `''` |
| `slugify('日本語 and English')` | `'and-english'` |
| `slugify('Straße')` | `'stra-e'` (`ß` is not ASCII and does not decompose) |
| `slugify('Łódź')` | `'odz'` |
| `slugify('Hello World', { separator: '--' })` | `'hello--world'` |
| `slugify('hello world foo', { separator: '--', maxLength: 7 })` | `'hello'` (`'hello--'` is cut, then the trailing `--` goes) |
| `slugify('Hello, World! Foo', { maxLength: 8 })` | `'hello-wo'` |
| `slugify('Hello, World! Foo', { maxLength: 11 })` | `'hello-world'` |
| `slugify('Hello, World! Foo', { maxLength: 12 })` | `'hello-world'` (`'hello-world-'` loses its trailing `-`) |
