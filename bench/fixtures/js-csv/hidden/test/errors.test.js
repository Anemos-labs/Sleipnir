'use strict';

const test = require('node:test');
const assert = require('node:assert');
const { parse, stringify, CsvError } = require('../src');

function errorFrom(fn) {
  try {
    fn();
  } catch (err) {
    return err;
  }
  return null;
}

test('malformed text is a CsvError that names the record', () => {
  const cases = [
    ['a"b', 1],
    ['a,b\nc,d"e\n', 2],
    ['a,b\nc, "d"\n', 2],
    ['ab""c', 1],
    ['"ab"c', 1],
    ['"ab" ,x', 1],
    ['a\n"b"c\n', 2],
    ['"ab"\rx', 1],
    ['"abc', 1],
    ['a,b\nc,"d', 2],
    ['a,b\nc,"d\n', 2],
    ['a,b\nc,"d\ne,f\n', 2],
    ['a,b\n\n\nc', 2],
    ['a,b\nc\n', 2],
    ['a,b\nc,d,e\n', 2],
    ['a,b\nc,d\ne\n', 3],
    ['a\n\nb,c\n', 2],
    ['a\n\n"b', 2],
    ['a\r\n\r\nb"c\r\n', 2],
    ['a\n\n\n"b"c\n', 2],
    ['\n\n"abc', 1],
  ];
  for (const [text, record] of cases) {
    const err = errorFrom(() => parse(text));
    assert.ok(err instanceof CsvError, `${JSON.stringify(text)} should throw a CsvError, got ${err}`);
    assert.ok(err instanceof Error);
    assert.strictEqual(err.record, record, JSON.stringify(text));
  }
});

test('the earliest problem is the one that is reported', () => {
  assert.strictEqual(errorFrom(() => parse('a,b\nc\nd"e\n')).record, 2);
  assert.strictEqual(errorFrom(() => parse('a,b\nc,d"e\nf\n')).record, 2);
  assert.strictEqual(errorFrom(() => parse('a,b\nc,d\ne,"f')).record, 3);
  assert.strictEqual(errorFrom(() => parse('a,b\nc,d\ne\nf"')).record, 3);
});

test('in header mode the header is record 1', () => {
  assert.strictEqual(errorFrom(() => parse('a,b\n1\n', { header: true })).record, 2);
  assert.strictEqual(errorFrom(() => parse('a,"b\n1,2\n', { header: true })).record, 1);
  assert.strictEqual(errorFrom(() => parse('a,b\n1,2\n3,4,5\n', { header: true })).record, 3);
});

test('what may follow a closing quote depends on the delimiter', () => {
  assert.deepStrictEqual(parse('"a";b', { delimiter: ';' }), [['a', 'b']]);
  assert.ok(errorFrom(() => parse('"a",b', { delimiter: ';' })) instanceof CsvError);
});

test('text that is not a string is a TypeError', () => {
  for (const text of [undefined, null, 42, {}, [], ['a,b']]) {
    assert.throws(() => parse(text), TypeError);
  }
  assert.throws(() => parse(), TypeError);
});

test('an invalid delimiter is a RangeError, even for an empty text', () => {
  for (const delimiter of ['', ',,', '"', '\n', '\r', '\r\n', 5, null, true]) {
    assert.throws(() => parse('', { delimiter }), RangeError, `delimiter ${JSON.stringify(delimiter)}`);
    assert.throws(() => parse('a,b', { delimiter }), RangeError, `delimiter ${JSON.stringify(delimiter)}`);
    assert.throws(() => stringify([['a']], { delimiter }), RangeError, `delimiter ${JSON.stringify(delimiter)}`);
  }
});

test('an invalid ragged policy is a RangeError', () => {
  for (const ragged of ['strict', 'PAD', '', null, true, 0]) {
    assert.throws(() => parse('a,b', { ragged }), RangeError, `ragged ${JSON.stringify(ragged)}`);
    assert.throws(() => parse('', { ragged }), RangeError, `ragged ${JSON.stringify(ragged)}`);
  }
});

test('undefined options mean the defaults', () => {
  assert.deepStrictEqual(parse('a,b\n', undefined), [['a', 'b']]);
  assert.deepStrictEqual(parse('a,b\n', {}), [['a', 'b']]);
  assert.deepStrictEqual(parse('a,b\n', { delimiter: undefined, header: undefined, ragged: undefined }), [['a', 'b']]);
  assert.strictEqual(stringify([['a']], { delimiter: undefined, newline: undefined, columns: undefined }), 'a\r\n');
});
