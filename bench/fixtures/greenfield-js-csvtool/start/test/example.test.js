'use strict';
// A few examples of README.md, and how the tests run the tool: spawn it, feed standard input, compare everything.
// The acceptance tests that verify your work cover the whole README.

const test = require('node:test');
const assert = require('node:assert');
const { spawnSync } = require('node:child_process');
const path = require('node:path');

const CLI = path.join(__dirname, '..', 'bin', 'csvtool.js');

function run(args, input = '') {
  const r = spawnSync(process.execPath, [CLI, ...args], { input, encoding: 'utf8' });
  return { status: r.status, stdout: r.stdout, stderr: r.stderr };
}

const PEOPLE = 'name,age,city\nAnn,31,"Paris, FR"\nBob,25,Rome\n"Cy ""the Kid""",40,Oslo\nDee,25,Rome\n';

test('head keeps the header and the first rows', () => {
  assert.deepStrictEqual(run(['head', '2'], PEOPLE), {
    status: 0,
    stdout: 'name,age,city\nAnn,31,"Paris, FR"\nBob,25,Rome\n',
    stderr: '',
  });
});

test('select reorders columns and writes the quoting the format asks for', () => {
  assert.deepStrictEqual(run(['select', 'city,name'], PEOPLE), {
    status: 0,
    stdout: 'city,name\n"Paris, FR",Ann\nRome,Bob\nOslo,"Cy ""the Kid"""\nRome,Dee\n',
    stderr: '',
  });
});

test('filter compares numbers as numbers', () => {
  assert.deepStrictEqual(run(['filter', 'age', '>=', '30'], PEOPLE), {
    status: 0,
    stdout: 'name,age,city\nAnn,31,"Paris, FR"\n"Cy ""the Kid""",40,Oslo\n',
    stderr: '',
  });
});

test('sort --numeric --desc is stable', () => {
  assert.deepStrictEqual(run(['sort', 'age', '--numeric', '--desc'], PEOPLE), {
    status: 0,
    stdout: 'name,age,city\n"Cy ""the Kid""",40,Oslo\nAnn,31,"Paris, FR"\nBob,25,Rome\nDee,25,Rome\n',
    stderr: '',
  });
});

test('stats', () => {
  assert.deepStrictEqual(run(['stats', 'age'], PEOPLE), {
    status: 0,
    stdout: 'count,min,max,mean\n4,25,40,30.25\n',
    stderr: '',
  });
});

test('errors go to standard error with an exit status', () => {
  assert.deepStrictEqual(run(['select', 'nope'], PEOPLE), {
    status: 1,
    stdout: '',
    stderr: 'csvtool: unknown column: nope\n',
  });
  assert.deepStrictEqual(run(['head', '-1'], PEOPLE), {
    status: 2,
    stdout: '',
    stderr: 'csvtool: head: N must be a non-negative integer\n',
  });
});
