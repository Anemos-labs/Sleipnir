'use strict';

const test = require('node:test');
const assert = require('node:assert');
const { parse, stringify } = require('../src');

// A tiny deterministic generator, so that the tests do not depend on Math.random.
function generator(seed) {
  let state = seed >>> 0;
  return (n) => {
    state = (Math.imul(state, 1664525) + 1013904223) >>> 0;
    return (state >>> 8) % n;
  };
}

const TOKENS = ['a', 'b', 'xyz', ' ', ',', ';', '"', '""', '\n', '\r', '\r\n', '\t', '|', "'", '', 'é', '1'];
const DELIMITERS = [',', ';', '\t', '|', ' '];
const NEWLINES = ['\r\n', '\n'];

function randomField(next) {
  let text = '';
  for (let n = next(5); n > 0; n--) {
    text += TOKENS[next(TOKENS.length)];
  }
  return text;
}

function randomRows(next) {
  const width = 1 + next(4);
  const rows = [];
  for (let n = 1 + next(4); n > 0; n--) {
    const row = [];
    for (let i = 0; i < width; i++) {
      row.push(randomField(next));
    }
    rows.push(row);
  }
  return rows;
}

test('parse(stringify(rows)) gives back the rows', () => {
  const next = generator(20240607);
  for (let trial = 0; trial < 600; trial++) {
    const rows = randomRows(next);
    const delimiter = DELIMITERS[next(DELIMITERS.length)];
    const newline = NEWLINES[next(NEWLINES.length)];
    const text = stringify(rows, { delimiter, newline });
    assert.deepStrictEqual(
      parse(text, { delimiter }),
      rows,
      `trial ${trial}: ${JSON.stringify({ rows, delimiter, newline, text })}`,
    );
  }
});

test('round trips of the awkward single-field records', () => {
  const awkward = [[''], [' '], ['"'], ['\n'], ['\r'], ['\r\n'], [','], ['a'], [''], ['""'], ['\t']];
  for (const newline of NEWLINES) {
    assert.deepStrictEqual(parse(stringify(awkward, { newline })), awkward);
  }
  assert.deepStrictEqual(parse(stringify(awkward, { delimiter: ';' }), { delimiter: ';' }), awkward);
  assert.deepStrictEqual(parse(stringify(awkward, { delimiter: ' ' }), { delimiter: ' ' }), awkward);
});

test('round trip with columns and header mode', () => {
  const objects = [
    { id: '1', name: 'Ada Lovelace', note: 'says "hi", twice' },
    { id: '2', name: '', note: 'two\nlines' },
    { id: '3', name: ' spaced ', note: '' },
  ];
  const columns = ['id', 'name', 'note'];
  const text = stringify(objects, { columns });
  assert.deepStrictEqual(parse(text, { header: true }), objects);
  assert.deepStrictEqual(parse(stringify([], { columns }), { header: true }), []);
});

test('the text written for a known table parses back to it', () => {
  const rows = [['name', 'qty'], ['say "hi"', '3'], ['multi\nline', ''], ['', '']];
  const text = stringify(rows);
  assert.strictEqual(text, 'name,qty\r\n"say ""hi""",3\r\n"multi\nline",\r\n,\r\n');
  assert.deepStrictEqual(parse(text), rows);
});
