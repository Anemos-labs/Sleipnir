'use strict';

const test = require('node:test');
const assert = require('node:assert');
const { parse, CsvError } = require('../src');

function errorFrom(fn) {
  try {
    fn();
  } catch (err) {
    return err;
  }
  return null;
}

test('header mode: one plain object per record', () => {
  assert.deepStrictEqual(parse('a,b\n1,2\n3,4\n', { header: true }), [
    { a: '1', b: '2' },
    { a: '3', b: '4' },
  ]);
  assert.deepStrictEqual(parse('a\r\n"x\ny"\r\n', { header: true }), [{ a: 'x\ny' }]);
  assert.deepStrictEqual(
    parse('"first name","last, name"\nAda,Lovelace\n', { header: true }),
    [{ 'first name': 'Ada', 'last, name': 'Lovelace' }],
  );
  assert.deepStrictEqual(parse('a,b\n,\n', { header: true }), [{ a: '', b: '' }]);
  const [row] = parse('a,b\n1,2\n', { header: true });
  assert.strictEqual(Object.getPrototypeOf(row), Object.prototype);
  assert.deepStrictEqual(Object.keys(row), ['a', 'b']);
});

test('header mode: no data records give an empty array', () => {
  assert.deepStrictEqual(parse('a,b\n', { header: true }), []);
  assert.deepStrictEqual(parse('a,b', { header: true }), []);
  assert.deepStrictEqual(parse('', { header: true }), []);
  assert.deepStrictEqual(parse('\n\r\n', { header: true }), []);
  assert.deepStrictEqual(parse('a,b\n\n\n', { header: true }), []);
});

test('header mode: a name that occurs twice takes the later value', () => {
  assert.deepStrictEqual(parse('a,a,b\n1,2,3\n', { header: true }), [{ a: '2', b: '3' }]);
});

test('header mode: the header is not a row and header: false gives arrays', () => {
  assert.deepStrictEqual(parse('a,b\n1,2\n', { header: false }), [['a', 'b'], ['1', '2']]);
  assert.deepStrictEqual(parse('a,b\n1,2\n', { header: true }).length, 1);
});

test('the default policy is error', () => {
  for (const text of ['a,b\nc\n', 'a,b\nc,d,e\n']) {
    const err = errorFrom(() => parse(text));
    assert.ok(err instanceof CsvError, JSON.stringify(text));
    assert.strictEqual(err.record, 2);
    const explicit = errorFrom(() => parse(text, { ragged: 'error' }));
    assert.ok(explicit instanceof CsvError);
    assert.strictEqual(explicit.record, 2);
  }
  assert.deepStrictEqual(parse('a,b\nc,d\n', { ragged: 'error' }), [['a', 'b'], ['c', 'd']]);
});

test('pad: short records are padded, long ones are an error', () => {
  assert.deepStrictEqual(
    parse('a,b,c\nd\ne,f\ng,h,i\n', { ragged: 'pad' }),
    [['a', 'b', 'c'], ['d', '', ''], ['e', 'f', ''], ['g', 'h', 'i']],
  );
  const err = errorFrom(() => parse('a,b\nc,d,e\n', { ragged: 'pad' }));
  assert.ok(err instanceof CsvError);
  assert.strictEqual(err.record, 2);
  assert.deepStrictEqual(parse('a,b\n\n\nc\n', { ragged: 'pad' }), [['a', 'b'], ['c', '']]);
});

test('keep: records are returned as they are', () => {
  assert.deepStrictEqual(
    parse('a,b\nc\nd,e,f\n', { ragged: 'keep' }),
    [['a', 'b'], ['c'], ['d', 'e', 'f']],
  );
  assert.deepStrictEqual(parse('a,b,c\n', { ragged: 'keep' }), [['a', 'b', 'c']]);
});

test('header mode with each policy', () => {
  const text = 'a,b\n1\n1,2,3\n4,5\n';
  assert.strictEqual(errorFrom(() => parse(text, { header: true })).record, 2);
  assert.deepStrictEqual(parse('a,b\n1\n', { header: true, ragged: 'pad' }), [{ a: '1', b: '' }]);
  assert.strictEqual(errorFrom(() => parse(text, { header: true, ragged: 'pad' })).record, 3);
  assert.deepStrictEqual(parse('a,b\n1\n', { header: true, ragged: 'keep' }), [{ a: '1' }]);
  assert.deepStrictEqual(parse(text, { header: true, ragged: 'keep' }), [
    { a: '1' },
    { a: '1', b: '2' },
    { a: '4', b: '5' },
  ]);
  assert.deepStrictEqual(parse('a,b,c\n1,2,3\n', { header: true, ragged: 'pad' }), [{ a: '1', b: '2', c: '3' }]);
});

test('the width is the width of the first record, however wide', () => {
  assert.deepStrictEqual(parse('x\ny\nz\n'), [['x'], ['y'], ['z']]);
  assert.strictEqual(errorFrom(() => parse('x\ny,z\n')).record, 2);
  assert.strictEqual(errorFrom(() => parse(',\n,,\n')).record, 2);
  assert.deepStrictEqual(parse(',\n,\n'), [['', ''], ['', '']]);
});

test('the README examples for header mode', () => {
  assert.deepStrictEqual(parse('a,b\n1,2\n3,4\n', { header: true }), [{ a: '1', b: '2' }, { a: '3', b: '4' }]);
  assert.deepStrictEqual(parse('a,b\n1\n', { header: true, ragged: 'pad' }), [{ a: '1', b: '' }]);
  assert.deepStrictEqual(parse('a,b\n1\n', { header: true, ragged: 'keep' }), [{ a: '1' }]);
  assert.strictEqual(errorFrom(() => parse('a,b\n1\n')).record, 2);
});
