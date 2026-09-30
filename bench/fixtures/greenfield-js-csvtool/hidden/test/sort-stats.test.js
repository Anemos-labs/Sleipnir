'use strict';
// Acceptance tests for csvtool: sort and stats. Every expectation follows from README.md.

const test = require('node:test');
const { ok, fails, group, PEOPLE, ANN, BOB, CY, DEE, PEOPLE_HEADER } = require('./helpers');

// ------------------------------------------------------------------ sort

const TIES = 'k,v\na,2\nb,1\nc,2\nd,1\ne,2\n';
const T = { a: 'a,2\n', b: 'b,1\n', c: 'c,2\n', d: 'd,1\n', e: 'e,2\n' };
const ties = (letters) => 'k,v\n' + [...letters].map((l) => T[l]).join('');

group(
  'sort',
  [
    ['strings, ascending (the default)', ['sort', 'n'], 'n\n9\n10\n2\n', 'n\n10\n2\n9\n'],
    ['strings, descending', ['sort', 'n', '--desc'], 'n\n9\n10\n2\n', 'n\n9\n2\n10\n'],
    ['numbers, ascending', ['sort', 'n', '--numeric'], 'n\n9\n10\n2\n', 'n\n2\n9\n10\n'],
    ['numbers, descending', ['sort', 'n', '--numeric', '--desc'], 'n\n9\n10\n2\n', 'n\n10\n9\n2\n'],
    ['negative and decimal numbers, ascending', ['sort', 'n', '--numeric'], 'n\n-1.5\n3\n-10\n2.25\n0\n', 'n\n-10\n-1.5\n0\n2.25\n3\n'],
    ['ties keep their order: numeric ascending', ['sort', 'v', '--numeric'], TIES, ties('bdace')],
    ['ties keep their order: numeric descending', ['sort', 'v', '--numeric', '--desc'], TIES, ties('acebd')],
    ['1.0 and 1 are equal numbers: ascending', ['sort', 'v', '--numeric'], 'k,v\nx,1.0\ny,1\nz,0.5\n', 'k,v\nz,0.5\nx,1.0\ny,1\n'],
    ['1.0 and 1 are equal numbers: descending', ['sort', 'v', '--numeric', '--desc'], 'k,v\nx,1.0\ny,1\nz,0.5\n', 'k,v\nx,1.0\ny,1\nz,0.5\n'],
    ['1.0 and 1 are different strings', ['sort', 'v'], 'k,v\nx,1.0\ny,1\nz,0.5\n', 'k,v\nz,0.5\ny,1\nx,1.0\n'],
    ['strings compare by code unit: upper case first', ['sort', 'v'], 'v\nb\nA\na\nB\n', 'v\nA\nB\na\nb\n'],
    ['strings compare by code unit: é after z', ['sort', 'v'], 'v\nz\né\na\n', 'v\na\nz\né\n'],
    ['an empty value is the smallest string', ['sort', 'v'], 'k,v\na,b\nb,\nc,a\n', 'k,v\nb,\nc,a\na,b\n'],
    ['an empty value, descending', ['sort', 'v', '--desc'], 'k,v\na,b\nb,\nc,a\n', 'k,v\na,b\nc,a\nb,\n'],
    ['whole rows move, with their quoting', ['sort', 'v', '--numeric'], 'k,v\n"x,y",2\n"p""q",1\n', 'k,v\n"p""q",1\n"x,y",2\n'],
    ['sorting by another column', ['sort', 'age', '--numeric'], PEOPLE, PEOPLE_HEADER + BOB + DEE + ANN + CY],
    ['a name with a space', ['sort', 'total cost', '--numeric', '--desc'], 'id,total cost\n1,5\n2,50\n3,7\n', 'id,total cost\n2,50\n3,7\n1,5\n'],
    ['options may come before the column', ['sort', '--desc', '--numeric', 'v'], TIES, ties('acebd')],
    ['options may come after the column, in any order', ['sort', '--numeric', 'v', '--desc'], TIES, ties('acebd')],
    ['only a header', ['sort', 'v', '--numeric'], 'k,v\n', 'k,v\n'],
  ],
  (args, input, expected) => ok(args, input, expected),
);

group(
  'sort --numeric: a value that is not a number is an error',
  [
    ['a word', 'k,v\na,1\nb,x\nc,2\n', 'x'],
    ['the first bad value is reported', 'k,v\na,1\nb,x\nc,y\n', 'x'],
    ['even in a single row', 'k,v\na,abc\n', 'abc'],
    ['no digits before the dot', 'k,v\na,.5\n', '.5'],
    ['an exponent', 'k,v\na,1e3\n', '1e3'],
    ['a leading space', 'k,v\na," 5"\n', ' 5'],
    ['a value written in quotes is shown unquoted', 'k,v\na,"a ""b"" c"\n', 'a "b" c'],
  ],
  (input, shown) => fails(['sort', 'v', '--numeric'], input, 1, `csvtool: not a number: ${shown}\n`),
);

test('sort: without --numeric any value can be sorted', async () => {
  await ok(['sort', 'v'], 'k,v\na,x\nb,1e3\nc,\n', 'k,v\nc,\nb,1e3\na,x\n');
});

test('sort: an unknown column is an error', async () => {
  await fails(['sort', 'nope'], 'a,b\n1,2\n', 1, 'csvtool: unknown column: nope\n');
  await fails(['sort', 'nope', '--numeric'], 'a,b\n', 1, 'csvtool: unknown column: nope\n');
});


// ------------------------------------------------------------------ stats

group(
  'stats',
  [
    ['integers', 'v\n1\n2\n3\n4\n', '4,1,4,2.5'],
    ['decimals: the mean is rounded to four places', 'v\n1.5\n2.5\n3\n', '3,1.5,3,2.3333'],
    ['negative numbers', 'v\n-1\n-2\n-4\n', '3,-4,-1,-2.3333'],
    ['numbers are written in the number format', 'v\n2.50\n007\n', '2,2.5,7,4.75'],
    ['min and max are rounded to four places too', 'v\n0.123456\n2\n', '2,0.1235,2,1.0617'],
    ['empty values are skipped', 'k,v\na,1\nb,\nc,2\nd,2\n', '3,1,2,1.6667'],
    ['only empty values', 'k,v\na,\nb,\n', '0,,,'],
    ['no rows', 'k,v\n', '0,,,'],
    ['a single value', 'v\n5\n', '1,5,5,5'],
    ['a sum that is not exact in binary', 'v\n0.1\n0.2\n', '2,0.1,0.2,0.15'],
    ['zero is written 0', 'v\n0\n-0\n', '2,0,0,0'],
    ['a negative mean', 'v\n-1\n0\n0\n', '3,-1,0,-0.3333'],
    ['a mean that is whole', 'v\n1\n2\n3\n', '3,1,3,2'],
  ],
  (input, row) => ok(['stats', 'v'], input, `count,min,max,mean\n${row}\n`),
);

test('stats: another column of a larger table', async () => {
  await ok(['stats', 'age'], PEOPLE, 'count,min,max,mean\n4,25,40,30.25\n');
});

group(
  'stats: a value that is not a number is an error',
  [
    ['a word', 'v\n1\nabc\n', 'abc'],
    ['the first bad value is reported', 'v\n1\nx\ny\n', 'x'],
    ['a plus sign', 'v\n+5\n', '+5'],
    ['an exponent', 'v\n1e3\n', '1e3'],
    ['a value of spaces is not empty', 'k,v\na,"  "\n', '  '],
  ],
  (input, shown) => fails(['stats', 'v'], input, 1, `csvtool: not a number: ${shown}\n`),
);

test('stats: an unknown column is an error', async () => {
  await fails(['stats', 'nope'], 'a,b\n1,2\n', 1, 'csvtool: unknown column: nope\n');
  await fails(['stats', 'nope'], 'a,b\n', 1, 'csvtool: unknown column: nope\n');
});
