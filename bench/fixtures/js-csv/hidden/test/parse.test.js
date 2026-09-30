'use strict';

const test = require('node:test');
const assert = require('node:assert');
const { parse } = require('../src');

function table(cases, options) {
  for (const [text, expected] of cases) {
    assert.deepStrictEqual(parse(text, options), expected, JSON.stringify(text));
  }
}

test('plain records and line breaks', () => {
  table([
    ['a,b,c', [['a', 'b', 'c']]],
    ['a,b,c\n', [['a', 'b', 'c']]],
    ['a,b,c\r\n', [['a', 'b', 'c']]],
    ['a,b\r\nc,d\ne,f\r\n', [['a', 'b'], ['c', 'd'], ['e', 'f']]],
    ['a\nb', [['a'], ['b']]],
    ['single', [['single']]],
  ]);
});

test('blank lines give no record', () => {
  table([
    ['', []],
    ['\n', []],
    ['\r\n', []],
    ['\r\n\r\n', []],
    ['\n\n\n', []],
    ['\n\na\n\n\nb\n\n', [['a'], ['b']]],
    ['\r\n\r\na\r\n\r\n', [['a']]],
    ['a\n', [['a']]],
    ['a', [['a']]],
    ['a\n\n', [['a']]],
  ]);
});

test('a line of spaces is a record, and so is a line with just two quotes', () => {
  table([
    ['   \n', [['   ']]],
    ['a\n \nb\n', [['a'], [' '], ['b']]],
    ['""\n', [['']]],
    ['""', [['']]],
    ['a\n""\nb\n', [['a'], [''], ['b']]],
  ]);
});

test('empty fields', () => {
  table([
    ['a,,b', [['a', '', 'b']]],
    [',', [['', '']]],
    [',,', [['', '', '']]],
    ['a,', [['a', '']]],
    [',a', [['', 'a']]],
    ['a,\n,b\n', [['a', ''], ['', 'b']]],
    ['a,b,\r\n,,\r\n', [['a', 'b', ''], ['', '', '']]],
  ]);
});

test('nothing is trimmed', () => {
  table([
    [' a , b ', [[' a ', ' b ']]],
    ['\ta\t,\tb', [['\ta\t', '\tb']]],
    ['a  b,c', [['a  b', 'c']]],
  ]);
});

test('quoted fields', () => {
  table([
    ['"a"', [['a']]],
    ['"a,b",c', [['a,b', 'c']]],
    ['a,"b,c"', [['a', 'b,c']]],
    ['"say ""hi"""', [['say "hi"']]],
    ['""""', [['"']]],
    ['""""""', [['""']]],
    ['"a""b""c"', [['a"b"c']]],
    ['"a\nb",c', [['a\nb', 'c']]],
    ['"a\r\nb"', [['a\r\nb']]],
    ['"a\rb"', [['a\rb']]],
    ['"line1\r\nline2\nline3",x\r\n', [['line1\r\nline2\nline3', 'x']]],
    ['"x"\r\n"y"\n', [['x'], ['y']]],
    ['"",""', [['', '']]],
    ['"a",,"c"', [['a', '', 'c']]],
    ['"  spaces  "', [['  spaces  ']]],
    ['"\n"', [['\n']]],
    ['"a,b\nc,d"\n', [['a,b\nc,d']]],
  ]);
});

test('a lone CR is an ordinary character, not a line break', () => {
  table([
    ['a\rb,c', [['a\rb', 'c']]],
    ['a,b\r', [['a', 'b\r']]],
    ['\r', [['\r']]],
    ['a\r\rb', [['a\r\rb']]],
  ]);
});

test('the delimiter option', () => {
  table([
    ['a;b;c\n1;2;3\n', [['a', 'b', 'c'], ['1', '2', '3']]],
    ['a,b;c', [['a,b', 'c']]],
    ['"a;b";c', [['a;b', 'c']]],
  ], { delimiter: ';' });
  table([
    ['a\tb\tc', [['a', 'b', 'c']]],
    ['"a\tb"\tc', [['a\tb', 'c']]],
    ['a,b\tc', [['a,b', 'c']]],
  ], { delimiter: '\t' });
  table([['a|b||c', [['a', 'b', '', 'c']]]], { delimiter: '|' });
  table([['a.b.c', [['a', 'b', 'c']]]], { delimiter: '.' });
  table([['a b  c', [['a', 'b', '', 'c']]], ['"a b" c', [['a b', 'c']]]], { delimiter: ' ' });
});

test('the README examples', () => {
  assert.deepStrictEqual(parse('name,qty\r\nwidget,3\r\n'), [['name', 'qty'], ['widget', '3']]);
  assert.deepStrictEqual(parse('name,qty\nwidget,3\n', { header: true }), [{ name: 'widget', qty: '3' }]);
  table([
    ['a,b,c', [['a', 'b', 'c']]],
    ['a,,b', [['a', '', 'b']]],
    [',', [['', '']]],
    ['a,', [['a', '']]],
    [' a , b ', [[' a ', ' b ']]],
    ['"a,b",c', [['a,b', 'c']]],
    ['"say ""hi"""', [['say "hi"']]],
    ['""""', [['"']]],
    ['""', [['']]],
    ['"a\nb",c', [['a\nb', 'c']]],
    ['"a\r\nb"', [['a\r\nb']]],
    ['a\rb,c', [['a\rb', 'c']]],
    ['a\n\n\nb\n', [['a'], ['b']]],
  ]);
});

test('the results are plain arrays of strings', () => {
  const rows = parse('a,b\n1,2\n');
  assert.ok(Array.isArray(rows));
  for (const row of rows) {
    assert.ok(Array.isArray(row));
    for (const cell of row) {
      assert.strictEqual(typeof cell, 'string');
    }
  }
});
