'use strict';

const test = require('node:test');
const assert = require('node:assert');
const { slugify } = require('../src/slugify');

const CORPUS = [
  'Hello, World! Foo',
  'The quick brown fox jumps over the lazy dog',
  '  leading and trailing  ',
  'Crème Brûlée à la façon',
  'snake_case_and-kebab-case and CamelCase',
  'ﬁsh ① x² ＡＢＣ 1/2 ½ ™',
  'Party 🎉 time 🚀 now',
  'Привет мир hello мир',
  '日本語 and English and 中文',
  'Straße Łódź Ærø',
  'a',
  'a b',
  '--a--',
  '12345 67890',
  'Ünïcödé Tëst',
  'x⃗y️z',
  'one   two   three   four   five   six   seven',
  "it's a dog's life",
  'C++ & C# & F#',
  '',
];

const SEPARATORS = ['-', '_', '.', '--', '-_-', '~'];

const escapeRegExp = (s) => s.replace(/[.*+?^${}()|[\]\\\/-]/g, '\\$&');

test('the slug is lower-case ASCII words joined by single separators', () => {
  for (const separator of SEPARATORS) {
    const shape = new RegExp(`^(?:[a-z0-9]+(?:${escapeRegExp(separator)}[a-z0-9]+)*)?$`);
    for (const text of CORPUS) {
      const slug = slugify(text, { separator });
      assert.match(slug, shape, `slugify(${JSON.stringify(text)}, separator ${separator})`);
    }
  }
});

test('with the default separator only a-z, 0-9 and - can appear', () => {
  for (const text of CORPUS) {
    assert.match(slugify(text), /^[a-z0-9-]*$/, JSON.stringify(text));
  }
});

test('slugifying a slug with the same separator changes nothing', () => {
  for (const separator of SEPARATORS) {
    for (const text of CORPUS) {
      const slug = slugify(text, { separator });
      assert.strictEqual(slugify(slug, { separator }), slug, `${JSON.stringify(text)} / ${separator}`);
    }
  }
});

test('maxLength gives the longest prefix of the slug that fits and does not end in a separator', () => {
  for (const separator of SEPARATORS) {
    for (const text of CORPUS) {
      const full = slugify(text, { separator });
      for (let maxLength = 1; maxLength <= 40; maxLength++) {
        const cut = slugify(text, { separator, maxLength });
        const where = `${JSON.stringify(text)} / ${separator} / ${maxLength}`;
        assert.ok(cut.length <= maxLength, `too long: ${where}`);
        assert.ok(full.startsWith(cut), `not a prefix: ${where}`);
        assert.ok(!/[^a-z0-9]$/.test(cut), `ends with a separator: ${where}`);
        if (full.length <= maxLength) {
          assert.strictEqual(cut, full, `changed although it fits: ${where}`);
        }
        // whatever was taken off the window [0, maxLength) is nothing but separator characters
        const dropped = full.slice(cut.length, Math.min(maxLength, full.length));
        assert.ok(!/[a-z0-9]/.test(dropped), `cut too much: ${where}`);
      }
    }
  }
});
