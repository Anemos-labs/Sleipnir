'use strict';

const test = require('node:test');
const assert = require('node:assert');
const { slugify } = require('../src/slugify');

test('lower-cases and joins the words with a dash', () => {
  assert.strictEqual(slugify('Hello World'), 'hello-world');
});

test('punctuation and repeated whitespace give a single separator', () => {
  assert.strictEqual(slugify('Hello,   World!!'), 'hello-world');
  assert.strictEqual(slugify('snake_case and CamelCase'), 'snake-case-and-camelcase');
});

test('there is no leading or trailing separator', () => {
  assert.strictEqual(slugify('  --Hello--  '), 'hello');
});

test('digits are kept', () => {
  assert.strictEqual(slugify('Top 10 tips'), 'top-10-tips');
});

test('accents are removed', () => {
  assert.strictEqual(slugify('Crème Brûlée'), 'creme-brulee');
});

test('a custom separator', () => {
  assert.strictEqual(slugify('Hello World', { separator: '_' }), 'hello_world');
});

test('maxLength never leaves a trailing separator', () => {
  assert.strictEqual(slugify('Hello, World! Foo', { maxLength: 6 }), 'hello');
  assert.strictEqual(slugify('Hello, World! Foo', { maxLength: 8 }), 'hello-wo');
});
