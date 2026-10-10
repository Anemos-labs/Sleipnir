// help-keys.test.mjs: the Help sheet (the ? key) and Settings › Look › Keys list only keys the page does something with. The keys of both
// are read where a person reads them, from the page as it is drawn (the rows of the sheet itself and SHORTCUTS of js/11-data-live.js
// that follow them), and each key is looked up in internal/parity/testdata/keys-web.json, which keys-parity.test.mjs keeps equal to what
// the page does when the key is pressed. A row that names a key the page ignores (a key of the terminal's `sleipnir watch`, a key of the
// terminal that the page has no code for, a word where a key belongs) fails and names the row.
//
// The first column of a row holds keys and nothing else:
//   - `·` (and `/`, which is a key of the page too) between keys that are alternatives;
//   - a space between keys that are pressed one after another or are alternatives ("o c m b");
//   - `g then c r f` for the chords of a prefix; `alt+1 … alt+9` for a range of keys that differ in the last character;
//   - the key's name as people write it: Enter, Esc, Home, ctrl+k, shift+Left (the case does not matter here).
// What a key does belongs in the second column. The few phrases of a first column that describe a key and are not one are in PROSE.
//
// The other direction is held too: every key that opens a view (the single keys and the `g` chords) is named by the sheet and by the card.
// Those keys are not listed here: the single keys are found by pressing every single-character key of keys-web.json on the page and seeing
// which change the view, and the chords are the `g` + letter entries of the same file (the rail's own). A key that is bound and that a
// sheet leaves out fails the test and names the key; a key that is deliberately not advertised is in NOT_ADVERTISED, with the reason.
import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { boot, PATCH } from './pagedom.mjs';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const KEYS_WEB = path.resolve(HERE, '..', '..', '..', 'parity', 'testdata', 'keys-web.json');

/** Phrases of a first column that describe a key and are not keys: the backslash that ends a line before Enter is a character, which the
 *  page reads as "a new line" when Enter follows it, not a key of its own. */
const PROSE = ['\\ at end of line'];

/** The keys that open a view and are deliberately not named by the Help sheet or by Settings › Look › Keys: key (as keys-web.json spells it)
 *  -> why. There are none: a view's key that a sheet does not name is a key nobody finds. */
const NOT_ADVERTISED = {};

/** The keys that do something on the page: those of keys-web.json with at least one state. */
function handledKeys() {
  const rec = JSON.parse(fs.readFileSync(KEYS_WEB, 'utf8'));
  return new Set(Object.entries(rec.keys).filter(([, states]) => states.length > 0).map(([k]) => k));
}

/** The keys a first column names, canonically spelled (lowercase, as keys-web.json spells them), and what in it is not a key. */
export function parseKeys(text) {
  let s = String(text);
  for (const p of PROSE) s = s.split(p).join(' ');
  const tokens = s.split(/\s+/).filter(Boolean), keys = [], bad = [];
  let prefix = null;
  for (let i = 0; i < tokens.length; i++) {
    const t = tokens[i];
    if (t === '·') { prefix = null; continue; }
    if (t === 'then') {
      if (!keys.length) { bad.push('then (it follows a key)'); continue; }
      prefix = keys[keys.length - 1]; continue;
    }
    if (t === '…') {
      const a = keys[keys.length - 1], b = (tokens[++i] || '').toLowerCase();
      if (!a || !b || a.length < 2 || a.slice(0, -1) !== b.slice(0, -1) || !/\d/.test(a.slice(-1)) || !/\d/.test(b.slice(-1)) || a.slice(-1) > b.slice(-1)) { bad.push('… ' + (tokens[i] || '') + ' (a range of keys that differ in the last digit)'); continue; }
      for (let c = +a.slice(-1) + 1; c <= +b.slice(-1); c++) keys.push(a.slice(0, -1) + c);
      continue;
    }
    const k = t.toLowerCase();
    keys.push(prefix ? prefix + ' ' + k : k);
  }
  return { keys, bad };
}

/** The problems of a list of rows [{ keys: first column, text: second column }] against the keys the page handles, one line each. */
export function problemsOf(rows, handled, where) {
  const out = [];
  for (const r of rows) {
    const { keys, bad } = parseKeys(r.keys);
    if (!keys.length && !bad.length) out.push(where + ': the row ' + JSON.stringify(r.keys) + ' names no key');
    for (const b of bad) out.push(where + ': the row ' + JSON.stringify(r.keys) + ' has ' + b + ', which is not a key');
    const unknown = keys.filter(k => !handled.has(k));
    if (unknown.length) out.push(where + ': the row ' + JSON.stringify(r.keys) + ' (' + r.text + ') names ' + unknown.map(k => JSON.stringify(k)).join(' ') + ', which the page does nothing with (internal/parity/testdata/keys-web.json). Name the keys the page has, keep words in the second column, or take the row out');
    if (!String(r.text).trim()) out.push(where + ': the row ' + JSON.stringify(r.keys) + ' says nothing about what it does');
  }
  return out;
}

/** The keys that open a view, as the page binds them. The chords are the `g` + letter entries of keys-web.json (the views rail's chord
 *  table is the only thing that state holds). The single keys are found where the page decides: every single-character key that the page
 *  handles with nothing focused is pressed on the live page, once with the cockpit shown and once with Settings (a key that names the view
 *  already shown would show nothing), and the keys after which another view is shown are the keys of the views. */
async function viewKeys() {
  const rec = JSON.parse(fs.readFileSync(KEYS_WEB, 'utf8'));
  const chords = Object.keys(rec.keys).filter(k => /^g [a-z]$/.test(k)).sort();
  const singles = Object.entries(rec.keys).filter(([k, states]) => k.length === 1 && k !== 'g' && states.includes('page')).map(([k]) => k);
  const L = await boot(), opens = [];
  for (const k of singles) {
    for (const from of ['cockpit', 'settings']) {
      if (L.SL.ui.hasModal()) L.SL.ui.closeModal();
      L.SL.views.show(from); L.doc.body.focus(); await L.settle();
      const before = L.SL.views.current.name;
      L.press(k); await L.settle();
      if (L.SL.views.current.name !== before) { opens.push(k); break; }
    }
  }
  return { chords, singles: opens.sort() };
}

/** The view keys that the rows of a sheet leave out, one line each; `allow` are the keys that are not advertised on purpose. */
export function unnamed(rows, bound, where, allow = NOT_ADVERTISED) {
  const named = new Set(rows.flatMap(r => parseKeys(r.keys).keys));
  return bound.filter(k => !named.has(k) && !(k in allow)).map(k => where + ' does not name ' + JSON.stringify(k) + ', which opens a view on the page. Add it to the row of the keys of the views (or, if it is hidden on purpose, to NOT_ADVERTISED with the reason)');
}

/** The rows of the Help sheet and of Settings › Look › Keys, as the page draws them. */
async function sheets() {
  const L = await boot();
  const read = root => {
    const dt = L.doc.querySelectorAll(root + ' .keys2 dt'), dd = L.doc.querySelectorAll(root + ' .keys2 dd');
    assert.equal(dt.length, dd.length, root + ': a key without its action, or an action without its key');
    return dt.map((d, i) => ({ keys: d.textContent, text: dd[i].textContent }));
  };
  L.doc.body.focus(); L.press('?'); await L.settle();
  assert.ok(L.SL.ui.hasModal(), 'the ? key opens the Help sheet');
  const help = read('#overlayHost');
  L.SL.ui.closeModal();
  L.SL.ui.settingsPage('look'); await L.settle();
  const settings = read('body');
  return { help, settings, shortcuts: JSON.parse(JSON.stringify(L.SL.D.shortcuts)) }; // plain arrays: the page's own are of another realm
}

test('every key the Help sheet lists is a key the page does something with', async () => {
  const { help, shortcuts } = await sheets(), handled = handledKeys();
  assert.ok(help.length >= shortcuts.length + 3, 'the sheet draws its own rows and then every row of SHORTCUTS: ' + help.length + ' rows for ' + shortcuts.length);
  assert.deepEqual(help.slice(-shortcuts.length).map(r => [r.keys, r.text]), shortcuts, 'the sheet ends with SHORTCUTS, as it is');
  assert.deepEqual(problemsOf(help, handled, 'the Help sheet'), []);
});

test('every key Settings › Look › Keys lists is a key the page does something with', async () => {
  const { settings, shortcuts } = await sheets(), handled = handledKeys();
  assert.ok(settings.length >= 10, 'the card draws its own rows and the first rows of SHORTCUTS: ' + settings.length);
  assert.deepEqual(settings.slice(-12).map(r => [r.keys, r.text]), shortcuts.slice(0, 12), 'the card ends with the first twelve rows of SHORTCUTS');
  assert.deepEqual(problemsOf(settings, handled, 'Settings › Keys'), []);
});

test('the keys the sheet names are read as keys: spellings, alternatives, chords and ranges', () => {
  assert.deepEqual(parseKeys('ctrl+t · alt+t').keys, ['ctrl+t', 'alt+t']);
  assert.deepEqual(parseKeys('Up / Down').keys, ['up', '/', 'down']);
  assert.deepEqual(parseKeys('Left Right · shift+Left shift+Right · + - · Home End').keys, ['left', 'right', 'shift+left', 'shift+right', '+', '-', 'home', 'end']);
  assert.deepEqual(parseKeys('g then c r · m').keys, ['g', 'g c', 'g r', 'm']);
  assert.deepEqual(parseKeys('alt+1 … alt+3').keys, ['alt+1', 'alt+2', 'alt+3']);
  assert.deepEqual(parseKeys('\\ at end of line · alt+enter').keys, ['alt+enter']);
  assert.deepEqual(parseKeys('/ · ctrl+k').keys, ['/', 'ctrl+k']);
  assert.equal(parseKeys('alt+3 … alt+1').bad.length, 1, 'a range runs upward');
  assert.equal(parseKeys('then c').bad.length, 1, 'a chord needs its prefix');
});

test('the guard fails on a row of keys the page ignores, and on prose where keys belong', async () => {
  const handled = handledKeys();
  assert.ok(handled.has('ctrl+d') && handled.has('ctrl+c') && handled.has('g c'), 'keys-web.json has the keys of the page');
  assert.ok(!handled.has('q') && !handled.has('a'), 'q and a are keys of the terminal\'s watch and replay programs only');
  const fake = [
    { keys: 'q', text: 'leave (watch, replay)' }, { keys: 'a', text: 'animation off' }, { keys: 'o c m b · 1-4 · Tab', text: 'views' }, { keys: 'Ctrl-C', text: 'spelled as the terminal\'s help spells it' },
    { keys: '(web) Space over the chat', text: 'prose in the first column' }, { keys: 'ctrl+q', text: 'a key nobody binds' }, { keys: 'Enter', text: '' }, { keys: '', text: 'no key at all' },
  ];
  const got = problemsOf(fake, handled, 'x');
  assert.equal(got.length, fake.length, 'one problem for each row\n' + got.join('\n'));
  fake.forEach((r, i) => assert.ok(got[i].includes(JSON.stringify(r.keys)), 'the problem names its row: ' + got[i]));
  for (const want of ['names "q",', 'names "a",', '"1-4"', '"ctrl-c"', '"(web)"', '"over"', 'names "ctrl+q",', 'says nothing about what it does', 'names no key']) {
    assert.ok(got.some(m => m.includes(want)), 'not reported: ' + want + '\n' + got.join('\n'));
  }
  assert.deepEqual(problemsOf([{ keys: 'Enter', text: 'send' }, { keys: 'ctrl+t · alt+t', text: 'stats' }, { keys: 'g then c r', text: 'views' }], handled, 'x'), []);
});

test('a row added to SHORTCUTS that names a key the page ignores fails the sheet\'s check', async () => {
  const anchor = "    ['Enter', 'send the message'],";
  PATCH['11-data-live.js'] = src => { assert.ok(src.includes(anchor), 'the row this test adds a row after has moved'); return src.replace(anchor, anchor + "\n    ['q', 'leave (watch, replay)'],\n    ['ctrl+q', 'a key nobody binds'],"); };
  try {
    const { help, settings } = await sheets(), handled = handledKeys();
    const got = problemsOf(help, handled, 'the Help sheet');
    assert.equal(got.length, 2, got.join('\n'));
    assert.ok(got[0].includes('"q"') && got[1].includes('"ctrl+q"'), got.join('\n'));
    assert.equal(problemsOf(settings, handled, 'Settings › Keys').length, 2, 'the card shows the first twelve rows, and these are among them');
  } finally { delete PATCH['11-data-live.js']; }
});

// ---------------------------------------------------------------------------------------------------------------------------
// Every key that opens a view is named: the single keys and the `g` chords.

test('the keys that open a view are found by pressing them: the single keys of the views and the chords of the rail', async () => {
  const { chords, singles } = await viewKeys();
  /* floors, so that a search that finds nothing cannot pass the checks below; a key added to the page is found without a change here */
  const rail = await boot().then(L => Array.from(L.SL.ui.nav.items, it => 'g ' + it.key).sort());
  assert.deepEqual(chords, rail, 'the chords of keys-web.json are the rail\'s: one for each item of it');
  for (const k of ['g c', 'g q', 'g e']) assert.ok(chords.includes(k), k);
  for (const k of ['o', 'c', 'm', 'b', 'r', 's', ',', '.']) assert.ok(singles.includes(k), 'the key ' + k + ' opens a view on the page, and this test did not find it: ' + singles.join(' '));
  for (const k of ['?', '/', 'g', '+', '-', '=', 'space']) assert.ok(!singles.includes(k), k + ' opens no view');
});

test('the Help sheet names every key that opens a view', async () => {
  const { help } = await sheets(), { chords, singles } = await viewKeys();
  assert.deepEqual(unnamed(help, [...chords, ...singles], 'the Help sheet'), []);
});

test('Settings › Look › Keys names every key that opens a view', async () => {
  const { settings } = await sheets(), { chords, singles } = await viewKeys();
  assert.deepEqual(unnamed(settings, [...chords, ...singles], 'Settings › Keys'), []);
});

test('the keys that are not advertised on purpose are keys of the views that the sheets leave out, each with a reason', async () => {
  const { help, settings } = await sheets(), { chords, singles } = await viewKeys(), bound = [...chords, ...singles];
  for (const [k, why] of Object.entries(NOT_ADVERTISED)) {
    assert.ok(bound.includes(k), k + ' is in NOT_ADVERTISED and opens no view');
    assert.ok(String(why).trim().split(/\s+/).length >= 4, k + ' is in NOT_ADVERTISED without a reason');
    assert.ok(unnamed(help, [k], 'x', {}).length && unnamed(settings, [k], 'x', {}).length, k + ' is in NOT_ADVERTISED and a sheet names it: take it out');
  }
});

test('the guard fails when a key of a view is left out of a sheet', async () => {
  const { chords, singles } = await viewKeys(), bound = [...chords, ...singles];
  const chordRow = 'g then c r f h k q a m b p s t e';
  // the chord to the Merge view out of the Help sheet and of the card
  for (const file of ['87-ui-sheets.js', '98-ui-settings.js']) {
    PATCH[file] = src => { assert.ok(src.includes(chordRow), 'the row this test takes a key out of has moved in ' + file); return src.replace(chordRow, 'g then c r f h k a m b p s t e'); };
    try {
      const { help, settings } = await sheets();
      const got = file === '87-ui-sheets.js' ? unnamed(help, bound, 'the Help sheet') : unnamed(settings, bound, 'Settings › Keys');
      assert.equal(got.length, 1, file + ': ' + got.join('\n'));
      assert.ok(got[0].includes('"g q"'), got[0]);
      assert.equal(unnamed(file === '87-ui-sheets.js' ? settings : help, bound, 'the other sheet').length, 0, 'the other sheet still names it');
    } finally { delete PATCH[file]; }
  }
  // the single key of the Tools view out of the Help sheet (SHORTCUTS) and out of the card's own row of single keys
  const keysRow = "['o c m b r s , .', 'a view:";
  PATCH['11-data-live.js'] = src => { assert.ok(src.includes(keysRow), 'the SHORTCUTS row of the keys of the views has moved'); return src.replace(keysRow, "['o c m b r s ,', 'a view:"); };
  try {
    const { help } = await sheets(), got = unnamed(help, bound, 'the Help sheet');
    assert.equal(got.length, 1, got.join('\n')); assert.ok(got[0].includes('"."'), got[0]);
  } finally { delete PATCH['11-data-live.js']; }
  const cardRow = "['o c m b r s , .', 'the round-1";
  PATCH['98-ui-settings.js'] = src => { assert.ok(src.includes(cardRow), 'the card\'s row of single keys has moved'); return src.replace(cardRow, "['o c m b r s ,', 'the round-1"); };
  try {
    const { settings } = await sheets(), got = unnamed(settings, bound, 'Settings › Keys');
    assert.equal(got.length, 1, got.join('\n')); assert.ok(got[0].includes('"."'), got[0]);
  } finally { delete PATCH['98-ui-settings.js']; }
  // a key that is allowed to be missing is not reported, and only that one
  assert.deepEqual(unnamed([{ keys: 'o c m b r s ,', text: 'x' }], ['o', '.'], 'x', { '.': 'a reason of enough words' }), []);
  assert.equal(unnamed([{ keys: 'o c m b r s ,', text: 'x' }], ['o', '.', 'g q'], 'x', { '.': 'a reason of enough words' }).length, 1);
});
