'use strict';
// Acceptance tests for csvtool: filter. Every expectation follows from README.md.

const test = require('node:test');
const { ok, fails, group, PEOPLE, ANN, BOB, CY, DEE, PEOPLE_HEADER } = require('./helpers');

// ------------------------------------------------------------------ filter

const V = 'k,v\na,9\nb,10\nc,abc\nd,\ne,-5\nf,2.50\ng,1.0\nh,01\n';
const VROWS = {
  a: 'a,9\n',
  b: 'b,10\n',
  c: 'c,abc\n',
  d: 'd,\n',
  e: 'e,-5\n',
  f: 'f,2.50\n',
  g: 'g,1.0\n',
  h: 'h,01\n',
};
const vrows = (letters) => 'k,v\n' + [...letters].map((l) => VROWS[l]).join('');

group(
  'filter: the operators on a column of numbers, words and an empty value',
  [
    ['= 1 (numbers: 1.0 and 01 are 1)', 'v', '=', '1', 'gh'],
    ['!= 1', 'v', '!=', '1', 'abcdef'],
    ['< 10 (an empty value is a string that is less than "10")', 'v', '<', '10', 'adefgh'],
    ['<= 10', 'v', '<=', '10', 'abdefgh'],
    ['> 1 ("abc" > "1" as strings)', 'v', '>', '1', 'abcf'],
    ['= with an empty VALUE', 'v', '=', '', 'd'],
    ['< abc (all but abc)', 'v', '<', 'abc', 'abdefgh'],
    ['> -6 (a negative number is a positional argument)', 'v', '>', '-6', 'abcefgh'],
    ['= 2.5', 'v', '=', '2.5', 'f'],
    ['contains 0', 'v', 'contains', '0', 'bfgh'],
    ['contains is case-sensitive', 'v', 'contains', 'AB', ''],
    ['contains with an empty VALUE is always true', 'v', 'contains', '', 'abcdefgh'],
  ],
  (col, op, value, letters) => ok(['filter', col, op, value], V, vrows(letters)),
);

group(
  'filter: strings are compared by UTF-16 code unit',
  [
    ['> M: upper case before lower case', 'name', '>', 'M', 'name,n\nAnn,1\nBob,2\nZed,3\nalice,4\n', 'name,n\nZed,3\nalice,4\n'],
    ['< a', 'name', '<', 'a', 'name,n\nAnn,1\nBob,2\nZed,3\nalice,4\n', 'name,n\nAnn,1\nBob,2\nZed,3\n'],
    ['>= é: beyond the ASCII letters', 'w', '>=', 'é', 'w,n\nzebra,1\nécole,2\neagle,3\n', 'w,n\nécole,2\n'],
  ],
  (col, op, value, input, expected) => ok(['filter', col, op, value], input, expected),
);

group(
  'filter: rows keep their order and their quoting is normalised',
  [
    ['numbers', ['filter', 'age', '>=', '30'], PEOPLE, PEOPLE_HEADER + ANN + CY],
    ['equal numbers', ['filter', 'age', '=', '25'], PEOPLE, PEOPLE_HEADER + BOB + DEE],
    ['contains is case-sensitive', ['filter', 'city', 'contains', 'o'], PEOPLE, PEOPLE_HEADER + BOB + CY + DEE],
    ['a value with a comma', ['filter', 'city', '=', 'Paris, FR'], PEOPLE, PEOPLE_HEADER + ANN],
    ['a value with quotes', ['filter', 'name', '=', 'Cy "the Kid"'], PEOPLE, PEOPLE_HEADER + CY],
    ['nothing matches: the header only', ['filter', 'age', '>', '99'], PEOPLE, PEOPLE_HEADER],
  ],
  (args, input, expected) => ok(args, input, expected),
);

test('filter: only the documented syntax is a number', async () => {
  // `v = 5` holds for every number equal to 5; any other text is compared as a string, and only "5" equals "5"
  const cells = ['5', '5.0', '05', '5.00', '+5', '5.', '5e0', '0x5', '" 5"', '"5 "', '5_0', '٥'];
  const input = 'k,v\n' + cells.map((c, i) => `${i},${c}`).join('\n') + '\n';
  await ok(['filter', 'v', '=', '5'], input, 'k,v\n0,5\n1,5.0\n2,05\n3,5.00\n');
});

group(
  'filter: usage errors',
  [
    ['==', ['filter', 'v', '==', '1'], 'csvtool: filter: unknown operator: ==\n'],
    ['CONTAINS', ['filter', 'v', 'CONTAINS', '1'], 'csvtool: filter: unknown operator: CONTAINS\n'],
    ['no arguments', ['filter'], 'csvtool: filter: wrong number of arguments\n'],
    ['too many arguments', ['filter', 'v', '=', '1', 'f.csv', 'extra'], 'csvtool: filter: wrong number of arguments\n'],
  ],
  (args, stderr) => fails(args, V, 2, stderr),
);

test('filter: an unknown column is an error, even when there are no rows', async () => {
  await fails(['filter', 'nope', '=', '1'], 'a,b\n', 1, 'csvtool: unknown column: nope\n');
  await fails(['filter', 'nope', '=', '1'], V, 1, 'csvtool: unknown column: nope\n');
});
