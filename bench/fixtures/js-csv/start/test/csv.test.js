'use strict';

const test = require('node:test');
const assert = require('node:assert');
const { parse, stringify, CsvError } = require('../src');

test('parses plain records', () => {
  assert.deepStrictEqual(parse('a,b,c\n1,2,3\n'), [['a', 'b', 'c'], ['1', '2', '3']]);
});

test('accepts CRLF and a missing line break at the end', () => {
  assert.deepStrictEqual(parse('a,b\r\nc,d'), [['a', 'b'], ['c', 'd']]);
});

test('quoted fields may hold delimiters, quotes and line breaks', () => {
  assert.deepStrictEqual(parse('"a,b","say ""hi""","x\ny"\n'), [['a,b', 'say "hi"', 'x\ny']]);
});

test('header mode returns objects', () => {
  assert.deepStrictEqual(parse('name,qty\nwidget,3\ngadget,4\n', { header: true }), [
    { name: 'widget', qty: '3' },
    { name: 'gadget', qty: '4' },
  ]);
});

test('an unterminated quote is a CsvError that names the record', () => {
  assert.throws(
    () => parse('a,b\nc,"d'),
    (err) => err instanceof CsvError && err.record === 2,
  );
});

test('a record of the wrong width is a CsvError by default', () => {
  assert.throws(
    () => parse('a,b\nc\n'),
    (err) => err instanceof CsvError && err.record === 2,
  );
});

test('stringify quotes only the fields that need it', () => {
  assert.strictEqual(stringify([['a', 'b,c', 'say "hi"']]), 'a,"b,c","say ""hi"""\r\n');
});

test('stringify ends every record with CRLF, or with the newline option', () => {
  assert.strictEqual(stringify([['a', 'b'], ['c', 'd']]), 'a,b\r\nc,d\r\n');
  assert.strictEqual(stringify([['a', 'b'], ['c', 'd']], { newline: '\n' }), 'a,b\nc,d\n');
});
