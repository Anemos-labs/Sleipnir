'use strict';

const test = require('node:test');
const assert = require('node:assert');
const { slugify } = require('../src/slugify');

function check(table) {
  for (const [input, expected] of table) {
    assert.strictEqual(slugify(input), expected, `slugify(${JSON.stringify(input)})`);
  }
}

test('accented Latin letters lose their accents', () => {
  check([
    ['Crème Brûlée à la façon', 'creme-brulee-a-la-facon'],
    ['Jürgen Müller', 'jurgen-muller'],
    ['São Paulo', 'sao-paulo'],
    ['Ñandú', 'nandu'],
    ['Åsa Öberg', 'asa-oberg'],
    ['ÀÉÎÕÜ', 'aeiou'],
    ['İstanbul', 'istanbul'],
  ]);
});

test('text that is already decomposed gives the same result as composed text', () => {
  assert.strictEqual(slugify('café'), 'cafe');
  assert.strictEqual(slugify('Crème Brûlée'), 'creme-brulee');
  assert.strictEqual(slugify('café'), slugify('café'));
});

test('compatibility characters are folded by NFKD', () => {
  check([
    ['ﬁsh', 'fish'],
    ['ﬃ', 'ffi'],
    ['① ② ③', '1-2-3'],
    ['x²', 'x2'],
    ['ＡＢＣ １２３', 'abc-123'],
    ['Ⅻ', 'xii'],
    ['™', 'tm'],
    ['㎏', 'kg'],
    ['½', '1-2'],
    ['ﬁsh ① x² ＡＢＣ', 'fish-1-x2-abc'],
    ['K', 'k'],
    ['a b', 'a-b'],
  ]);
});

test('all combining marks are removed, not only those of the accent block', () => {
  check([
    ['v⃗ec', 'vec'],
    ['a️b', 'ab'],
    ['x́̂̃y', 'xy'],
  ]);
});

test('Latin letters without a decomposition are not ASCII and are dropped', () => {
  check([
    ['Straße', 'stra-e'],
    ['Łódź', 'odz'],
    ['Ærø', 'r'],
    ['đura', 'ura'],
  ]);
});

test('emoji are dropped', () => {
  check([
    ['Party 🎉 time', 'party-time'],
    ['I ❤️ JS', 'i-js'],
    ['👨‍👩‍👧 family', 'family'],
    ['a🎉b', 'a-b'],
    ['🎉🎉🎉', ''],
    ['Launch 🚀🚀🚀 day', 'launch-day'],
    ['🇫🇷 France', 'france'],
  ]);
});

test('non-Latin scripts are dropped, not transliterated', () => {
  check([
    ['Привет, мир!', ''],
    ['日本語 and English', 'and-english'],
    ['Ελληνικά 101', '101'],
    ['مرحبا world', 'world'],
    ['हिन्दी text', 'text'],
    ['한국어 k-pop', 'k-pop'],
    ['abc日本def', 'abc-def'],
    ['Москва 2024', '2024'],
  ]);
});

test('only digits that are ASCII after NFKD are kept', () => {
  check([
    ['٣ apples', 'apples'],
    ['１２３', '123'],
    ['²³', '23'],
  ]);
});

test('text without any ASCII letter or digit gives the empty string', () => {
  for (const input of ['', ' ', '   \t\n', '!!!', '---', '🎉', '日本語', 'Привет', '  ¿¡ ', '́']) {
    assert.strictEqual(slugify(input), '', JSON.stringify(input));
  }
});

test('whitespace, punctuation and symbols all separate words', () => {
  check([
    ['Hello\tWorld\nAgain', 'hello-world-again'],
    ['one/two\\three', 'one-two-three'],
    ['a.b,c;d:e', 'a-b-c-d-e'],
    ['a+b=c', 'a-b-c'],
    ['3.14', '3-14'],
    ['snake_case', 'snake-case'],
    ["Don't stop", 'don-t-stop'],
    ['Crème Brûlée, 2 for £5!', 'creme-brulee-2-for-5'],
    ['--a--b--', 'a-b'],
    ['  Multiple   spaces -- and---dashes  ', 'multiple-spaces-and-dashes'],
  ]);
});

test('the result is always a string', () => {
  assert.strictEqual(typeof slugify('Hello'), 'string');
  assert.strictEqual(typeof slugify(''), 'string');
});
