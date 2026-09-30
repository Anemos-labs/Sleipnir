'use strict';

const test = require('node:test');
const assert = require('node:assert');
const { slugify } = require('../src/slugify');

test('the separator can be any string of ASCII punctuation', () => {
  const cases = [
    ['_', 'hello_world_foo'],
    ['.', 'hello.world.foo'],
    ['--', 'hello--world--foo'],
    ['~', 'hello~world~foo'],
    ['+', 'hello+world+foo'],
    ['/', 'hello/world/foo'],
    ['\\', 'hello\\world\\foo'],
    ['-_-', 'hello-_-world-_-foo'],
  ];
  for (const [separator, expected] of cases) {
    assert.strictEqual(slugify('Hello, World!  Foo', { separator }), expected, JSON.stringify(separator));
  }
});

test('the separator is used only between words', () => {
  assert.strictEqual(slugify('  -- a -- ', { separator: '_' }), 'a');
  assert.strictEqual(slugify('a - b', { separator: '_' }), 'a_b');
  assert.strictEqual(slugify('a_b', { separator: '_' }), 'a_b');
  assert.strictEqual(slugify('a__b', { separator: '.' }), 'a.b');
  assert.strictEqual(slugify('', { separator: '_' }), '');
  assert.strictEqual(slugify('日本語', { separator: '_' }), '');
});

test('undefined or unknown options mean the defaults', () => {
  assert.strictEqual(slugify('a b'), 'a-b');
  assert.strictEqual(slugify('a b', undefined), 'a-b');
  assert.strictEqual(slugify('a b', {}), 'a-b');
  assert.strictEqual(slugify('a b', { separator: undefined, maxLength: undefined }), 'a-b');
  assert.strictEqual(slugify('a b', { unknown: true, lower: false }), 'a-b');
});

test('an invalid separator throws a TypeError', () => {
  const invalid = [
    '', ' ', 'a', 'Z', '1', '-a', 'a-', '- -', '%20', 'é', '—', '🎉', '\t', '\n',
    5, null, true, {}, ['-'],
  ];
  for (const separator of invalid) {
    assert.throws(() => slugify('a b', { separator }), TypeError, `separator ${JSON.stringify(separator)}`);
  }
});

test('maxLength cuts the slug and never leaves a trailing separator', () => {
  const cases = [
    [1, 'h'],
    [2, 'he'],
    [5, 'hello'],
    [6, 'hello'],
    [7, 'hello-w'],
    [8, 'hello-wo'],
    [11, 'hello-world'],
    [12, 'hello-world'],
    [13, 'hello-world-f'],
    [14, 'hello-world-fo'],
    [15, 'hello-world-foo'],
    [16, 'hello-world-foo'],
    [100, 'hello-world-foo'],
  ];
  for (const [maxLength, expected] of cases) {
    assert.strictEqual(slugify('Hello, World! Foo', { maxLength }), expected, `maxLength ${maxLength}`);
  }
});

test('maxLength with a separator of several characters', () => {
  const cases = [
    [5, 'hello'],
    [6, 'hello'],
    [7, 'hello'],
    [8, 'hello--w'],
    [12, 'hello--world'],
    [13, 'hello--world'],
    [14, 'hello--world'],
    [15, 'hello--world--f'],
    [17, 'hello--world--foo'],
    [30, 'hello--world--foo'],
  ];
  for (const [maxLength, expected] of cases) {
    assert.strictEqual(
      slugify('hello world foo', { separator: '--', maxLength }),
      expected,
      `maxLength ${maxLength}`,
    );
  }
  assert.strictEqual(slugify('ab cd ef', { separator: '-_-', maxLength: 3 }), 'ab');
  assert.strictEqual(slugify('ab cd ef', { separator: '-_-', maxLength: 4 }), 'ab');
  assert.strictEqual(slugify('ab cd ef', { separator: '-_-', maxLength: 5 }), 'ab');
  assert.strictEqual(slugify('ab cd ef', { separator: '-_-', maxLength: 6 }), 'ab-_-c');
});

test('maxLength applies to the slug, not to the text', () => {
  assert.strictEqual(slugify('Crème brûlée', { maxLength: 5 }), 'creme');
  assert.strictEqual(slugify('Привет мир hello мир', { maxLength: 3 }), 'hel');
  assert.strictEqual(slugify('supercalifragilistic', { maxLength: 5 }), 'super');
  assert.strictEqual(slugify('a b', { maxLength: 1 }), 'a');
  assert.strictEqual(slugify('', { maxLength: 3 }), '');
  assert.strictEqual(slugify('日本語', { maxLength: 3 }), '');
});

test('a slug that is not longer than maxLength is returned unchanged', () => {
  assert.strictEqual(slugify('hello-world', { maxLength: 11 }), 'hello-world');
  assert.strictEqual(slugify('hello world', { maxLength: 11 }), 'hello-world');
  assert.strictEqual(slugify('hello', { maxLength: 5 }), 'hello');
  assert.strictEqual(slugify('hello', { maxLength: 50 }), 'hello');
});

test('separator and maxLength work together', () => {
  assert.strictEqual(slugify('Hello, World! Foo', { separator: '_', maxLength: 12 }), 'hello_world');
  assert.strictEqual(slugify('Hello, World! Foo', { separator: '.', maxLength: 13 }), 'hello.world.f');
});

test('an invalid maxLength throws a RangeError', () => {
  for (const maxLength of [0, -1, -5, 1.5, NaN, '10', null]) {
    assert.throws(() => slugify('a b', { maxLength }), RangeError, `maxLength ${String(maxLength)}`);
  }
});

test('text that is not a string throws a TypeError', () => {
  for (const text of [undefined, null, 42, true, {}, [], ['a'], Symbol('a'), () => 'a']) {
    assert.throws(() => slugify(text), TypeError, `text ${typeof text}`);
  }
  assert.throws(() => slugify(), TypeError);
});
