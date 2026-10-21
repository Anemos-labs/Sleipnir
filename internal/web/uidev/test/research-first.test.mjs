// research-first.test.mjs: the New session dialog (87-ui-sheets.js) offers to research the first goal before the team builds, as
// /goal --research=DEPTH does in the terminal. The control shows only while the research plugin is on; a depth with a goal sends the
// goal behind --research=DEPTH and the command line says so; a depth without a goal researches nothing, says why, and sends no goal.
import test from 'node:test';
import assert from 'node:assert/strict';
import { boot, FakeEvent } from './pagedom.mjs';

async function open(researchOn) {
  const L = await boot({ route: (m, p) => (p === '/api/hello' ? { boot: 'b1', streamAfter: 7, server: { addr: '127.0.0.1:7777' }, tabs: [], active: '', ui: {}, defaults: { swarm: 8 } } : undefined) });
  L.SL.plugins.cat = { plugins: [{ id: 'research', enabled: researchOn }] };
  L.SL.ui.dialogs.newSession({ cwd: '/p/a' }); await L.settle(); L.advance(200); await L.settle();
  return L;
}
const spec = L => { const p = L.posts.filter(x => x.path === '/api/sessions'); return p.length ? p[p.length - 1].body : null; };
const start = async L => { L.$('#nsStart').click(); await L.settle(); L.advance(200); await L.settle(); };
const type = (L, sel, v) => { const i = L.$(sel); i.value = v; i.dispatchEvent(new FakeEvent('input', { bubbles: true })); };
const pick = async (L, v) => { L.$('[data-seg="research"][data-v="' + v + '"]').click(); await L.settle(); };

test('research first starts off, and a depth sends the first goal behind --research=DEPTH', async () => {
  const L = await open(true);
  assert.equal(L.$('[data-seg="research"][data-v="off"]').getAttribute('aria-pressed'), 'true');
  assert.equal(L.$('#nsResWhy').hidden, true);
  type(L, '#nsGoal', 'build a ledger with tests'); await pick(L, 'deep');
  assert.match(L.$('#nsResWhy').textContent, /10 researchers over three rounds.*then the team builds/);
  assert.match(L.$('#nsCli').textContent, /# then: \/goal --research=deep build a ledger with tests/);
  await start(L);
  assert.equal(spec(L).goalText, '--research=deep build a ledger with tests');
});

test('off sends the goal as written; a depth without a goal says it needs one and sends no goal', async () => {
  const L = await open(true);
  type(L, '#nsGoal', 'build a ledger'); await pick(L, 'quick'); await pick(L, 'off'); await start(L);
  assert.equal(spec(L).goalText, 'build a ledger');
  const M = await open(true);
  await pick(M, 'standard');
  assert.match(M.$('#nsResWhy').textContent, /needs a first goal/);
  assert.doesNotMatch(M.$('#nsCli').textContent, /--research/);
  await start(M);
  assert.equal('goalText' in spec(M), false);
});

test('with the research plugin off there is no research control', async () => {
  const L = await open(false);
  assert.equal(L.$('[data-seg="research"]'), null);
  type(L, '#nsGoal', 'build a ledger'); await start(L);
  assert.equal(spec(L).goalText, 'build a ledger');
});
