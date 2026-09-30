'use strict';
// Acceptance tests for csvtool: errors, files and large inputs. Every expectation follows from README.md.

const test = require('node:test');
const assert = require('node:assert');
const path = require('node:path');
const { ok, fails, group, withFiles, PEOPLE, ANN, BOB, CY, DEE, PEOPLE_HEADER } = require('./helpers');

// ------------------------------------------------------------------ usage errors

group(
  'usage errors: the command',
  [
    ['no arguments', [], 'csvtool: missing command\n'],
    ['an unknown command', ['frob'], 'csvtool: unknown command: frob\n'],
    ['--help is not a command', ['--help'], 'csvtool: unknown command: --help\n'],
    ['an unknown command with options', ['bogus', '--x'], 'csvtool: unknown command: bogus\n'],
    ['the name of an inherited property', ['toString'], 'csvtool: unknown command: toString\n'],
  ],
  (args, stderr) => fails(args, PEOPLE, 2, stderr),
);

group(
  'usage errors: options',
  [
    ['--desc on head', ['head', '1', '--desc'], 'csvtool: unknown option: --desc\n'],
    ['--numeric on stats', ['stats', 'age', '--numeric'], 'csvtool: unknown option: --numeric\n'],
    ['--desc on select', ['select', 'name', '--desc'], 'csvtool: unknown option: --desc\n'],
    ['an unknown option on sort', ['sort', 'age', '--ascending'], 'csvtool: unknown option: --ascending\n'],
    ['the first unknown option is reported', ['sort', 'age', '--bogus', '--worse'], 'csvtool: unknown option: --bogus\n'],
    ['an unknown option is reported before a wrong number of arguments', ['head', '--x'], 'csvtool: unknown option: --x\n'],
  ],
  (args, stderr) => fails(args, PEOPLE, 2, stderr),
);

group(
  'usage errors: the number of arguments',
  [
    ['head without N', ['head']],
    ['select without COLS', ['select']],
    ['select with too many', ['select', 'a', 'b.csv', 'c']],
    ['sort without COL', ['sort']],
    ['sort with only options', ['sort', '--desc', '--numeric']],
    ['stats without COL', ['stats']],
  ].map(([name, args]) => [name, args, `csvtool: ${args[0]}: wrong number of arguments\n`]),
  (args, stderr) => fails(args, PEOPLE, 2, stderr),
);

test('usage errors are reported before any input is read', async () => {
  await fails(['head', 'abc', '/no/such/file.csv'], '', 2, 'csvtool: head: N must be a non-negative integer\n');
  await fails(['filter', 'a', '??', '1', '/no/such/file.csv'], '', 2, 'csvtool: filter: unknown operator: ??\n');
  await fails(['frob', '/no/such/file.csv'], '', 2, 'csvtool: unknown command: frob\n');
  await fails(['sort', 'a', '--bogus', '/no/such/file.csv'], '', 2, 'csvtool: unknown option: --bogus\n');
});


// ------------------------------------------------------------------ input errors

group(
  'input errors: no records',
  [['nothing', ''], ['a newline', '\n'], ['empty lines', '\r\n\n\r\n']],
  (input) => fails(['head', '1'], input, 1, 'csvtool: empty input\n'),
);

group(
  'input errors: malformed CSV',
  [
    ['a quoted field that is never closed', 'a,b\n1,"2\n'],
    ['a quoted header that is never closed', '"a\n'],
    ['a quote inside an unquoted field', 'a,b\n1,x"y\n'],
    ['text after a closing quote', 'a,b\n"1"x,2\n'],
    ['text after a closing quote in the header', 'a,"b"c\n1,2\n'],
  ],
  (input) => fails(['head', '5'], input, 1, 'csvtool: malformed CSV\n'),
);

group(
  'input errors: the number of fields',
  [
    ['a short row', 'a,b\n1,2\n3\n', 'csvtool: row 2 has 1 fields, expected 2\n'],
    ['a long row', 'a,b\n1,2,3\n', 'csvtool: row 1 has 3 fields, expected 2\n'],
    ['a short first row', 'a,b,c\n1,2\n', 'csvtool: row 1 has 2 fields, expected 3\n'],
    ['skipped empty lines are not counted', 'a,b\n\n1,2\n\n3\n', 'csvtool: row 2 has 1 fields, expected 2\n'],
    ['a multi-line quoted field is one row', 'a,b\n"x\ny",1\n3\n', 'csvtool: row 2 has 1 fields, expected 2\n'],
    ['a trailing comma is an extra empty field', 'a,b\n1,2,\n', 'csvtool: row 1 has 3 fields, expected 2\n'],
  ],
  (input, stderr) => fails(['sort', 'a'], input, 1, stderr),
);

test('input errors come before column errors', async () => {
  await fails(['select', 'nope'], 'a,b\n1\n', 1, 'csvtool: row 1 has 1 fields, expected 2\n');
  await fails(['filter', 'nope', '=', '1'], '', 1, 'csvtool: empty input\n');
  await fails(['stats', 'nope'], 'a,"b\n', 1, 'csvtool: malformed CSV\n');
});


// ------------------------------------------------------------------ files

test('a file argument is read instead of standard input', () =>
  withFiles({ 'people.csv': PEOPLE }, (p) =>
    Promise.all([
      ok(['head', '1', p['people.csv']], '', PEOPLE_HEADER + ANN),
      ok(['head', '1', p['people.csv']], 'ignored\n,,,\n', PEOPLE_HEADER + ANN),
      ok(['select', 'city', p['people.csv']], 'x\n1\n', 'city\n"Paris, FR"\nRome\nOslo\nRome\n'),
      ok(['filter', 'age', '>', '30', p['people.csv']], '', PEOPLE_HEADER + ANN + CY),
      ok(['sort', 'age', '--numeric', '--desc', p['people.csv']], '', PEOPLE_HEADER + CY + ANN + BOB + DEE),
      ok(['stats', 'age', p['people.csv']], '', 'count,min,max,mean\n4,25,40,30.25\n'),
    ]),
  ));

test('a file that cannot be read, and files with nothing usable in them', () =>
  withFiles({ 'empty.csv': '', 'blank.csv': '\n\n', 'bad.csv': 'a,b\n1,"2\n' }, (p, dir) => {
    const missing = path.join(dir, 'missing.csv');
    return Promise.all([
      fails(['head', '1', missing], PEOPLE, 1, `csvtool: cannot read ${missing}\n`),
      fails(['select', 'nope', missing], '', 1, `csvtool: cannot read ${missing}\n`),
      fails(['head', '1', dir], PEOPLE, 1, `csvtool: cannot read ${dir}\n`),
      fails(['head', '1', p['empty.csv']], PEOPLE, 1, 'csvtool: empty input\n'),
      fails(['head', '1', p['blank.csv']], PEOPLE, 1, 'csvtool: empty input\n'),
      fails(['head', '1', p['bad.csv']], PEOPLE, 1, 'csvtool: malformed CSV\n'),
    ]);
  }));


// ------------------------------------------------------------------ large inputs

test('a large input is read completely and a large output is written completely', async () => {
  const n = 20000;
  const rows = Array.from({ length: n }, (_, i) => [String(i + 1), String((i * 7919) % 10007)]);
  const input = 'id,v\n' + rows.map((r) => r.join(',')).join('\n') + '\n';
  assert.ok(input.length > 150000);

  const expected = rows
    .map((r, index) => ({ r, index }))
    .sort((a, b) => Number(b.r[1]) - Number(a.r[1]) || a.index - b.index)
    .map((x) => x.r.join(','));
  const ids = 'id\n' + rows.map((r) => r[0]).join('\n') + '\n';

  await Promise.all([
    ok(['head', '1000000'], input, input),
    ok(['sort', 'v', '--numeric', '--desc'], input, 'id,v\n' + expected.join('\n') + '\n'),
    ok(['stats', 'id'], ids, `count,min,max,mean\n${n},1,${n},${(n + 1) / 2}\n`),
  ]);
});
