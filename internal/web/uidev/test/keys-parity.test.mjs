// keys-parity.test.mjs: which keys do something on the page, found by pressing every candidate key (the same list the terminal's
// test presses, see internal/tui/app/keys_parity_test.go) in a set of neutral states of the real page, and compared with the file
// internal/parity/testdata/keys-web.json. internal/parity/keys_test.go reads that file and the terminal's, and fails when one
// interface has a key the other lacks and the contract (internal/parity/contract/keys.json) does not say why.
//
// The page is the shipped one: index.html, every script of it in order, booted on the small fake DOM of pagedom.mjs (no DOM library;
// the page's own key handlers, the composer's, the session tabs', the palette's and the views' all run) against a fake fetch and a
// fake event stream. Nothing waits for time: the page's timers and clock are moved by the test.
//
// A key "does something" in a state when, after it, the page changed anything a person could see (an element, an attribute, a
// class, the text, which element has the focus), changed its own state (the view clock, the replay, the hold, the selection), made
// a request, or claimed the key (preventDefault). The text box's own editing (backspace, the caret keys, undo) is the browser's and is
// not page code, so it is not simulated; typing a character is, because the page reacts to the text (the `/` and `@` menus).
//
// Update the file after a deliberate change to the page's keys:  node internal/web/uidev/test/keys-parity.test.mjs --update
//   (or KEYS_UPDATE=1 node --test internal/web/uidev/test/keys-parity.test.mjs); read the diff, then update the contract.
// To see what each state found, and how (page: the page changed, state: its state changed, claimed: preventDefault):
//   node internal/web/uidev/test/keys-parity.test.mjs --report [--state=page --state=composer]
import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { createHash } from 'node:crypto';
import { fileURLToPath } from 'node:url';
import { boot, LOG, FakeEvent, PATCH, eventFor, typesText } from './pagedom.mjs';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const OUT = path.resolve(HERE, '..', '..', '..', 'parity', 'testdata', 'keys-web.json');
const UPDATE = process.env.KEYS_UPDATE === '1' || process.argv.includes('--update');
const REPORT = process.argv.includes('--report');

// ---------------------------------------------------------------------------------------------------------------------------
// The candidate keys. The terminal's test builds the same list; both write its digest, and internal/parity/keys_test.go fails
// when the digests differ. A key is spelled lowercase, modifiers in the order ctrl+ alt+ shift+, then the character or the name.

const SYMBOLS = '!"#$%&\'()*+,-./:;<=>?@[\\]^_`{|}~';
/** The named keys, each with the modifiers it is pressed with on purpose: shift+tab; the three newlines; shift+left and shift+right (a minute
 *  of a replay); an Esc with ctrl held, or a Tab with alt, is the system's, and ctrl+enter is the byte of ctrl+j in a terminal. */
const NAMED = { tab: ['', 'shift+'], esc: [''], enter: ['', 'shift+', 'alt+'], backspace: [''], delete: [''], insert: [''],
  up: [''], down: [''], left: ['', 'shift+'], right: ['', 'shift+'], home: [''], end: [''], pgup: [''], pgdn: [''] };
const LOWER = 'abcdefghijklmnopqrstuvwxyz';
function candidates() {
  const out = [];
  for (const c of LOWER) out.push(c);
  for (const c of '0123456789') out.push(c);
  out.push('space');
  for (const c of SYMBOLS) out.push(c);
  for (const c of LOWER) out.push('shift+' + c);
  for (const c of LOWER) out.push('ctrl+' + c);
  for (const c of ['space', '\\', ']', '^', '_']) out.push('ctrl+' + c);
  for (const c of LOWER) out.push('alt+' + c);
  for (const c of '0123456789') out.push('alt+' + c);
  for (const c of SYMBOLS) out.push('alt+' + c);
  for (let i = 1; i <= 12; i++) out.push('f' + i);
  for (const n of Object.keys(NAMED)) for (const m of NAMED[n]) out.push(m + n);
  return out;
}
const digest = list => createHash('sha256').update(list.slice().sort().join('\n')).digest('hex').slice(0, 16);

// ---------------------------------------------------------------------------------------------------------------------------
// Neutral states. Each is a function that brings a fresh page to it. `keys` limits the candidates of a state whose widget takes
// only some keys (the others would only show that a menu closes).

const WIDGET = ['up', 'down', 'left', 'right', 'home', 'end', 'pgup', 'pgdn', 'enter', 'space', 'tab', 'shift+tab', 'esc', 'delete', 'f2'];
const MENU = ['up', 'down', 'tab', 'shift+tab', 'enter', 'esc', 'pgup', 'pgdn', 'home', 'end'];
const ANSWER = ['1', '2', '3', '4', 'esc', 'enter', 'ctrl+c', 'up', 'down', 'tab'];
/** The keys with no modifier but shift: what a view might bind to a letter, a digit, a symbol or a named key. */
const PLAIN = candidates().filter(k => !/^(ctrl|alt|shift)\+[a-z0-9]/.test(k) && !/^(ctrl|alt)\+/.test(k));
const q1 = { id: 'q_1', agent: 'be-1', task: 'T1', cmd: 'go test ./...', why: 'run the tests', what: 'this command', cwd: '/p/a', t0: 12, offersTests: true, rule: 'Bash(go test:*)' };
const idle = async L => { L.doc.body.focus(); };
const asked = async L => { await L.event('ask', { q: q1 }); await L.step(0.2); await L.step(2); };
const STATES = [
  { name: 'composer', about: 'the message box is focused and empty', setup: async L => { L.focus('#input'); } },
  { name: 'composer-text', about: 'the message box holds text, the caret in the middle', setup: async L => { await L.type('hello world'); L.$('#input').setSelectionRange(5, 5); } },
  { name: 'composer-menu', about: 'the message box holds a "/" and its command menu is open', keys: MENU, setup: async L => { await L.type('/'); } },
  { name: 'composer-running', about: 'a turn is running; the message box is focused and empty', keys: ['ctrl+c', 'esc', 'enter', 'up'], setup: async L => { await L.frame('meta', { tab: 'a', patch: { running: true } }); L.focus('#input'); } },
  { name: 'page', about: 'nothing is focused; the cockpit is shown; two session tabs', two: true, setup: idle },
  { name: 'running', about: 'a turn is running; nothing is focused', keys: ['esc', 'ctrl+c', 'space', 'enter'], setup: async L => { await L.frame('meta', { tab: 'a', patch: { running: true } }); L.doc.body.focus(); } },
  { name: 'question', about: 'a question is open and the keyboard has been quiet', setup: async L => { await asked(L); L.doc.body.focus(); } },
  { name: 'question-composer', about: 'a question is open; the message box is focused and empty', keys: ANSWER, setup: async L => { await asked(L); L.focus('#input'); } },
  { name: 'replay', about: 'the view is a paused replay of the session', setup: async L => { await L.step(1); L.SL.sessions.active.seek(L.SL.sessions.active.vt, false); L.doc.body.focus(); await L.settle(); } },
  { name: 'cache', about: 'the Cache view is shown', setup: async L => { L.SL.views.show('cache'); L.doc.body.focus(); await L.settle(); } },
  { name: 'hold', about: 'the hold on the conversation is pinned', setup: async L => { L.SL.ui.togglePin(); L.doc.body.focus(); await L.settle(); } },
  { name: 'help', about: 'the keys and commands sheet is open', keys: ['esc', 'tab', 'shift+tab', 'enter'], setup: async L => { L.doc.body.focus(); L.press('?'); await L.settle(); } },
  { name: 'palette', about: 'the command palette is open', keys: MENU, setup: async L => { L.press('ctrl+k'); await L.settle(); } },
  { name: 'session-tab', about: 'a session tab has the focus', keys: WIDGET, two: true, setup: async L => { L.focus('.stab'); } },
  { name: 'views-rail', about: 'an item of the views rail has the focus', keys: WIDGET, setup: async L => { L.focus('.nvi'); } },
  { name: 'mode-menu', about: 'the permission mode menu is open', keys: WIDGET, setup: async L => { L.$('#modeBtn').click(); await L.settle(); } },
  { name: 'rail-handle', about: 'the handle that resizes the Radio rail has the focus', keys: WIDGET, setup: async L => { L.focus('#railSplit'); } },
  { name: 'chord', about: '"g" was pressed and the next key chooses a view', keys: LOWER.split(''), chord: true, setup: async L => {
    const nav = L.SL.ui.nav, chord = nav.chord; L.chords = []; nav.chord = k => { const r = chord(k); L.chords.push([k, r]); return r; }; // the page's own decision whether a key after g was a chord
    L.doc.body.focus(); L.press('g'); await L.settle(); L.chords = [];
  } },
  // every other view, with nothing focused: a view may bind a key of its own
  ...['mail', 'board', 'replay', 'sessions', 'tools', 'doctor', 'schedule', 'settings', 'workspace', 'runner'].map(v => ({ name: 'view-' + v, about: 'the ' + v + ' view is shown', keys: PLAIN, setup: async L => { L.SL.views.show(v); L.doc.body.focus(); await L.settle(); } })),
];

// ---------------------------------------------------------------------------------------------------------------------------
// What a key did: the changes to what is on the page, the page's own state, the requests, and whether it claimed the key.

const maskTyping = rows => rows.filter(r => !/^textarea#input /.test(r));
function snapshotOf(L) {
  const SL = L.SL, S = SL.sessions.active, T = SL.time.T, a = L.doc.activeElement;
  return JSON.stringify({
    focus: a ? a.localName + '#' + a.id + '.' + (a._attrs.get('class') || '') + '/' + (a._attrs.get('data-sid') || a._attrs.get('data-nav') || '') : '', view: SL.views.current && SL.views.current.name, active: S && S.id,
    time: { pinned: T.pinned, suppress: T.suppress, hover: T.hover, focus: T.focus }, dirty: SL.loop.dirty, link: SL.link.sel,
    session: S && { metaVer: S.metaVer, vt: Math.round(S.vt * 1000), replay: S.replay, hold: S.hold, feedOpen: S.ui.feedOpen, planOpen: S.ui.planOpen, verbose: S.ui.verbose },
    sessions: SL.sessions.list.map(x => x.id), calls: L.calls.length,
  });
}
/** Press `spec` in the page and say what it did: an empty list for nothing, else the kinds of effect (page, state, claimed). */
async function observe(L, spec) {
  const a = L.doc.activeElement, field = a && /^(input|textarea)$/.test(a.localName) ? a : null, was = field && { value: field.value, sel: field._sel.slice() };
  const before = snapshotOf(L); LOG.rows = []; const e = L.press(spec); await L.settle(); L.advance(50); await L.settle(); const rows = LOG.rows; LOG.rows = [];
  const out = [];
  if (field && field.value !== was.value) { // a character went into the box: the box is put back, and only what the page did about it counts
    out.restore = () => { field.value = was.value; field._sel = was.sel; field.dispatchEvent(new FakeEvent('input', { bubbles: true })); };
  }
  if ((typesText(spec) ? maskTyping(rows) : rows).length) out.push('page');
  if (snapshotOf(L) !== before) out.push('state');
  if (e.defaultPrevented) out.push('claimed');
  return out;
}

/** Every candidate in every state: the keys that did something, and in which states. */
async function sweep(only) {
  const all = candidates(), seen = {}, perState = {}, fails = [];
  for (const st of STATES) {
    if (only && !only.includes(st.name)) continue;
    const keys = st.keys ? all.filter(k => st.keys.includes(k)) : all; let L = null;
    for (const k of keys) {
      if (!L) { L = await boot({ two: st.two }); await st.setup(L); }
      await L.step(1); // every key is pressed with the keyboard quiet and the page's frame drawn, as a person's first key is
      const r = await observe(L, k), name = st.chord ? 'g ' + k : k;
      if (LOG.errors.length) { fails.push(st.name + ' ' + k + ': ' + LOG.errors[0]); LOG.errors = []; }
      const hit = st.chord ? k !== 'g' && L.chords.some(([c, taken]) => c === k && taken === true) : r.length > 0;
      if (hit) { (seen[name] = seen[name] || []).push(st.name); (perState[st.name] = perState[st.name] || []).push(name + ' [' + (st.chord ? 'chord' : r.join(',')) + ']'); }
      if (r.restore && !r.length) { r.restore(); await L.settle(); } else if (hit || r.length || st.chord) L = null; // a key that changed anything is not pressed on again: the next key gets a fresh page
    }
  }
  return { seen, perState, fails };
}

// ---------------------------------------------------------------------------------------------------------------------------
// The file, and the tests.

const ABOUT = 'Written by internal/web/uidev/test/keys-parity.test.mjs (node internal/web/uidev/test/keys-parity.test.mjs --update): every candidate key pressed in each state of the shipped page. A key is listed with the states in which it did something (changed the page or its state, made a request, or claimed the key); a chord after g is spelled "g c". The terminal\'s counterpart is keys-terminal.json.';
/** The file's text for a sweep. Each key is on a line of its own, so that a change reads as a diff. */
function render(r) {
  const all = candidates(), keys = Object.keys(r.seen).sort();
  return ['{', '  "dimension": "keys",', '  "interface": "web",', '  "about": ' + JSON.stringify(ABOUT) + ',', '  "candidates": ' + all.length + ',', '  "candidates_sha256": "' + digest(all) + '",',
    '  "states": ' + JSON.stringify(STATES.map(x => x.name)) + ',', '  "keys": {', keys.map(k => '    ' + JSON.stringify(k) + ': ' + JSON.stringify(r.seen[k])).join(',\n'), '  }', '}', ''].join('\n');
}
let swept = null;
/** The sweep of the page as it is, made once for the tests of this file. */
const theSweep = () => swept || (swept = sweep());

if (REPORT) {
  const only = process.argv.filter(a => a.startsWith('--state=')).map(a => a.slice(8)), t0 = Date.now(), r = await sweep(only.length ? only : null);
  for (const [name, ks] of Object.entries(r.perState)) console.log(name + ' (' + ks.length + '): ' + ks.join('  '));
  console.log('failures:', r.fails.slice(0, 20), '\nkeys:', Object.keys(r.seen).length, 'ms', Date.now() - t0);
}

test('the keys that do something on the page are the ones in internal/parity/testdata/keys-web.json', async () => {
  const r = await theSweep(), got = render(r);
  assert.deepEqual(r.fails, [], 'the page threw while a key was pressed (a fault of the page, or of the fake DOM that a new page code needs more of)');
  if (UPDATE) { fs.mkdirSync(path.dirname(OUT), { recursive: true }); fs.writeFileSync(OUT, got); return; }
  let want = null; try { want = fs.readFileSync(OUT, 'utf8'); } catch (e) { /* missing */ }
  if (want === got) return;
  const old = want ? JSON.parse(want).keys : {}, now = JSON.parse(got).keys, added = Object.keys(now).filter(k => !(k in old)), gone = Object.keys(old).filter(k => !(k in now));
  const moved = Object.keys(now).filter(k => k in old && JSON.stringify(old[k]) !== JSON.stringify(now[k]));
  assert.fail('internal/parity/testdata/keys-web.json is ' + (want ? 'out of date' : 'missing') + ': the page\'s keys changed.\n' +
    (added.length ? '  keys the page now handles: ' + added.join('  ') + '\n' : '') + (gone.length ? '  keys the page no longer handles: ' + gone.join('  ') + '\n' : '') +
    (moved.length ? '  keys that now do something in other states: ' + moved.join('  ') + '\n' : '') +
    'If this is intended: node internal/web/uidev/test/keys-parity.test.mjs --update, read the diff of the file, then keep the other interface level with it: build the key in the terminal too, or list it in internal/parity/contract/keys.json with the reason it differs, and document it in the Keys table of docs/UX.md.');
});

test('the candidate keys are canonical and unique, and each stands for the event a browser makes', () => {
  const all = candidates();
  assert.equal(new Set(all).size, all.length, 'a candidate is listed twice');
  for (const k of all) { assert.equal(k, k === 'shift+' ? k : k.toLowerCase(), k + ' is not lowercase'); assert.ok(eventFor(k).key, k + ' has no event'); }
  for (const must of ['ctrl+k', 'alt+t', 'shift+tab', 'esc', 'enter', 'space', 'f12', 'alt+9', '?', '/', '@', '+', '-', ',', '.', 'pgup', 'pgdn', 'home', 'end', 'shift+left', 'shift+h', 'alt+enter']) assert.ok(all.includes(must), must + ' is a candidate');
  assert.deepEqual(eventFor('alt+1'), { key: '1', code: 'Digit1', ctrlKey: false, altKey: true, shiftKey: false, metaKey: false });
  assert.deepEqual(eventFor('shift+h'), { key: 'H', code: 'KeyH', ctrlKey: false, altKey: false, shiftKey: true, metaKey: false });
  assert.deepEqual(eventFor('shift+tab'), { key: 'Tab', code: '', ctrlKey: false, altKey: false, shiftKey: true, metaKey: false });
  assert.equal(eventFor('+').key, '+'); assert.equal(eventFor('alt++').key, '+'); assert.equal(eventFor('alt++').altKey, true);
  assert.equal(eventFor('?').shiftKey, true, 'a question mark is typed with shift'); assert.equal(eventFor('ctrl+_').key, '_');
});

// The three tests below edit the source of a page file in memory (PATCH), bind a key that nothing binds, and look for it in the sweep.
// The key is chosen among a few candidates as one the sweep of the page as it is does not find, so that binding it for real later does
// not make these tests fail for the wrong reason.
async function unbound(state, among, prefix = '') {
  const r = await sweep([state]), key = among.find(k => !r.seen[prefix + k]);
  assert.ok(key, 'every key of ' + among.join(' ') + ' is already bound in the state ' + state + ': name more to choose from');
  return { key, seen: r.seen };
}
/** The keys the sweep found, without `except`. */
const others = (seen, except) => Object.keys(seen).filter(k => k !== except).sort();

test('a key bound in the page\'s global key handler is found by the sweep, and nothing else is', async () => {
  const { key, seen } = await unbound('page', ['f12', 'f11', 'f10', 'f9', 'f8', 'f7']);
  PATCH['94-keys.js'] = src => { assert.ok(src.includes("if (k === '?')"), 'the handler this test edits has moved'); return src.replace("if (k === '?')", "if (k === '" + key.toUpperCase() + "') { e.preventDefault(); ui.toast('bound'); return; } if (k === '?')"); };
  try {
    const r = await sweep(['page']);
    assert.deepEqual(r.seen[key], ['page'], 'the sweep found the new binding');
    assert.deepEqual(others(r.seen, key), others(seen), 'and only it');
  } finally { delete PATCH['94-keys.js']; }
});

test('a key bound on the message box is found by the sweep', async () => {
  const { key, seen } = await unbound('composer', ['ctrl+q', 'ctrl+x', 'ctrl+b', 'ctrl+e', 'ctrl+f']);
  PATCH['84-ui-chat.js'] = src => { assert.ok(src.includes("if (e.key === 'r' && e.ctrlKey)"), 'the handler this test edits has moved'); return src.replace("if (e.key === 'r' && e.ctrlKey)", "if (e.key === '" + key.slice(5) + "' && e.ctrlKey) { e.preventDefault(); ui.toast('bound'); return; } if (e.key === 'r' && e.ctrlKey)"); };
  try {
    const r = await sweep(['composer']);
    assert.deepEqual(r.seen[key], ['composer']);
    assert.deepEqual(others(r.seen, key), others(seen));
  } finally { delete PATCH['84-ui-chat.js']; }
});

test('a key bound in a view, and a chord, are found by the sweep', async () => {
  const view = await unbound('view-sessions', ['x', 'z', 'v', 'y', 'w']), chord = await unbound('chord', ['w', 'x', 'y', 'z', 'v'], 'g ');
  PATCH['91-views-b.js'] = src => { assert.ok(src.includes("reg({ name: 'sessions', title: 'Sessions', mount(sc, root) {"), 'the view this test edits has moved'); return src.replace("reg({ name: 'sessions', title: 'Sessions', mount(sc, root) {", "reg({ name: 'sessions', title: 'Sessions', mount(sc, root) { sc.listen(document, 'keydown', e => { if (e.key === '" + view.key + "') SL.ui.toast('bound'); });"); };
  PATCH['96-ui-nav.js'] = src => { assert.ok(src.includes("{ id: 'tools', view: 'tools', label: 'Tools', key: 't' }"), 'the rail this test edits has moved'); return src.replace("{ id: 'tools', view: 'tools', label: 'Tools', key: 't' }", "{ id: 'tools', view: 'tools', label: 'Tools', key: '" + chord.key + "' }"); };
  try {
    const r = await sweep(['view-sessions', 'chord']);
    assert.deepEqual(r.seen[view.key], ['view-sessions']);
    assert.deepEqual(r.seen['g ' + chord.key], ['chord'], 'the chord of Tools moved to the new letter');
    assert.equal(r.seen['g t'], undefined, 'and left the old one');
  } finally { delete PATCH['91-views-b.js']; delete PATCH['96-ui-nav.js']; }
});
