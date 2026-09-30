'use strict';

const test = require('node:test');
const assert = require('node:assert');
const { stringify } = require('../src');

test('records end with the newline, the last one included', () => {
  assert.strictEqual(stringify([['a', 'b'], ['c', 'd']]), 'a,b\r\nc,d\r\n');
  assert.strictEqual(stringify([['a', 'b'], ['c', 'd']], { newline: '\r\n' }), 'a,b\r\nc,d\r\n');
  assert.strictEqual(stringify([['a', 'b'], ['c', 'd']], { newline: '\n' }), 'a,b\nc,d\n');
  assert.strictEqual(stringify([['a']]), 'a\r\n');
  assert.strictEqual(stringify([]), '');
  assert.strictEqual(stringify([], { newline: '\n' }), '');
});

test('fields are quoted when they contain the delimiter, a quote, LF or CR', () => {
  const cases = [
    [['a,b'], '"a,b"'],
    [['say "hi"'], '"say ""hi"""'],
    [['"'], '""""'],
    [['""'], '""""""'],
    [['line1\nline2'], '"line1\nline2"'],
    [['cr\rhere'], '"cr\rhere"'],
    [['crlf\r\nhere'], '"crlf\r\nhere"'],
    [['\n'], '"\n"'],
    [['a', 'b,c', 'd'], 'a,"b,c",d'],
    [['x', '"y'], 'x,"""y"'],
  ];
  for (const [row, line] of cases) {
    assert.strictEqual(stringify([row], { newline: '\n' }), `${line}\n`, JSON.stringify(row));
  }
});

test('spaces never cause quoting', () => {
  assert.strictEqual(stringify([[' a ', 'b c', ' ', '\t']]), ' a ,b c, ,\t\r\n');
  assert.strictEqual(stringify([[' ']]), ' \r\n');
});

test('empty strings are quoted only when they are the only field of their record', () => {
  assert.strictEqual(stringify([['', '']]), ',\r\n');
  assert.strictEqual(stringify([['']]), '""\r\n');
  assert.strictEqual(stringify([['a'], [''], ['b']]), 'a\r\n""\r\nb\r\n');
  assert.strictEqual(stringify([['', 'x']]), ',x\r\n');
  assert.strictEqual(stringify([['x', '']]), 'x,\r\n');
  assert.strictEqual(stringify([['', '', '']]), ',,\r\n');
  assert.strictEqual(stringify([[null]]), '""\r\n');
  assert.strictEqual(stringify([[undefined]]), '""\r\n');
});

test('cells that are not strings are converted', () => {
  assert.strictEqual(stringify([[1, true, null, undefined, 2.5, 'x', 0, false, -3]]), '1,true,,,2.5,x,0,false,-3\r\n');
  assert.strictEqual(stringify([[1, 'a,b', 10n]]), '1,"a,b",10\r\n');
  assert.strictEqual(stringify([[{ toString: () => 'a,b' }]]), '"a,b"\r\n');
  assert.strictEqual(stringify([[1.5, 2]], { delimiter: ';' }), '1.5;2\r\n');
});

test('the delimiter option decides what needs quotes', () => {
  assert.strictEqual(stringify([['a;b', 'c,d']], { delimiter: ';' }), '"a;b";c,d\r\n');
  assert.strictEqual(stringify([['a\tb', 'c']], { delimiter: '\t' }), '"a\tb"\tc\r\n');
  assert.strictEqual(stringify([['a b', 'c']], { delimiter: ' ' }), '"a b" c\r\n');
  assert.strictEqual(stringify([['a|b', 'c']], { delimiter: '|' }), '"a|b"|c\r\n');
  assert.strictEqual(stringify([['a.b', 'c']], { delimiter: '.' }), '"a.b".c\r\n');
  assert.strictEqual(stringify([['', '']], { delimiter: ';' }), ';\r\n');
  assert.strictEqual(stringify([['']], { delimiter: ';' }), '""\r\n');
});

test('columns: objects become records after a header record', () => {
  assert.strictEqual(
    stringify([{ a: 1, b: 'x' }, { b: 'y' }], { columns: ['a', 'b'] }),
    'a,b\r\n1,x\r\n,y\r\n',
  );
  assert.strictEqual(stringify([{ a: 1, b: 2, c: 3 }], { columns: ['b', 'a'] }), 'b,a\r\n2,1\r\n');
  assert.strictEqual(stringify([{ a: null, b: undefined }], { columns: ['a', 'b'] }), 'a,b\r\n,\r\n');
  assert.strictEqual(stringify([], { columns: ['a', 'b'] }), 'a,b\r\n');
  assert.strictEqual(stringify([{ x: 1 }], { columns: ['first name', 'a,b'] }), 'first name,"a,b"\r\n,\r\n');
  assert.strictEqual(stringify([{ a: 'q"q' }], { columns: ['a'], newline: '\n' }), 'a\n"q""q"\n');
  assert.strictEqual(stringify([{ a: '' }], { columns: ['a'] }), 'a\r\n""\r\n');
});

test('rows must be arrays, or objects when columns is given', () => {
  assert.throws(() => stringify('abc'), TypeError);
  assert.throws(() => stringify(undefined), TypeError);
  assert.throws(() => stringify(null), TypeError);
  assert.throws(() => stringify({ a: 1 }), TypeError);
  assert.throws(() => stringify([1]), TypeError);
  assert.throws(() => stringify(['a,b']), TypeError);
  assert.throws(() => stringify([['a'], 'x']), TypeError);
  assert.throws(() => stringify([null]), TypeError);
  assert.throws(() => stringify([{ a: 1 }]), TypeError);
  assert.throws(() => stringify([['a']], { columns: ['a'] }), TypeError);
  assert.throws(() => stringify([null], { columns: ['a'] }), TypeError);
  assert.throws(() => stringify(['a'], { columns: ['a'] }), TypeError);
});

test('a record with no cells is a RangeError', () => {
  assert.throws(() => stringify([[]]), RangeError);
  assert.throws(() => stringify([['a'], []]), RangeError);
  assert.throws(() => stringify([[], ['a']]), RangeError);
});

test('an invalid newline is a RangeError', () => {
  for (const newline of ['', '\r', ' ', '\n\n', '\r\n\r\n', '<br>', 10, null]) {
    assert.throws(() => stringify([['a']], { newline }), RangeError, `newline ${JSON.stringify(newline)}`);
    assert.throws(() => stringify([], { newline }), RangeError, `newline ${JSON.stringify(newline)}`);
  }
});

test('the README table', () => {
  assert.strictEqual(stringify([['a', 'b'], ['c', 'd']]), 'a,b\r\nc,d\r\n');
  assert.strictEqual(stringify([['a', 'b']], { newline: '\n' }), 'a,b\n');
  assert.strictEqual(stringify([['a,b', 'say "hi"', 'x\ny', ' z ']]), '"a,b","say ""hi""","x\ny", z \r\n');
  assert.strictEqual(stringify([['', ''], [''], ['a'], [null]]), ',\r\n""\r\na\r\n""\r\n');
  assert.strictEqual(stringify([[1, true, null, 2.5]]), '1,true,,2.5\r\n');
  assert.strictEqual(stringify([['a;b', 'c,d']], { delimiter: ';' }), '"a;b";c,d\r\n');
  assert.strictEqual(
    stringify([{ a: 1, b: 'x' }, { b: 'y' }], { columns: ['a', 'b'] }),
    'a,b\r\n1,x\r\n,y\r\n',
  );
  assert.strictEqual(stringify([['name', 'qty'], ['say "hi"', 3]]), 'name,qty\r\n"say ""hi""",3\r\n');
});
