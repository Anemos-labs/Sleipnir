// team32.test.mjs: a large team in the New session dialog (87-ui-sheets.js) and on the Cockpit, on the shipped page booted on the
// fake DOM of pagedom.mjs. A team of 32, four of each role, is built with the steppers and started slot by slot; a role row opens to a
// model per slot and its own select sets every slot again. The horse's legs are the roles of the team (81-ui-hero.js), and above
// twelve workers the stalls are drawn in role bands, one line per worker.
import test from 'node:test';
import assert from 'node:assert/strict';
import { boot, FakeEvent } from './pagedom.mjs';

const DEFAULTS = { unlimitedEnv: 'SLEIPNIR_SWARM_BUDGET_USD=0', swarm: 8, maxWorkers: 0 };
const MODELS = { models: [{ ref: 'a/x', ctx: 1, in: 1, out: 2, cached: 0.1 }, { ref: 'b/y', ctx: 1, in: 1, out: 2, cached: 0.1 }], favs: [], roles: [], roleOrder: [], roleModels: {} };
async function open(defaults) {
  const L = await boot({ route: (m, p) => p === '/api/hello' ? { boot: 'b1', streamAfter: 7, server: { addr: '127.0.0.1:7777' }, tabs: [], active: '', ui: {}, defaults: Object.assign({}, DEFAULTS, defaults) } : p === '/api/models' ? MODELS : undefined });
  L.SL.ui.dialogs.newSession({ cwd: '/p/a', model: 'a/x' }); await L.settle(); L.advance(200); await L.settle();
  return L;
}
const cli = L => L.$('#nsCli').textContent;
const spec = L => { const p = L.posts.filter(x => x.path === '/api/sessions'); return p.length ? p[p.length - 1].body : null; };
/** The option a select is drawn with (the fake DOM keeps a select's value apart from its options). */
const chosen = s => { const o = s.querySelector('option[selected]'); return o ? o.textContent : ''; };
const change = async (L, el, v) => { el.value = v; el.dispatchEvent(new FakeEvent('change', { bubbles: true })); await L.settle(); };

test('a team of 32, four of each role: the steppers pass eight, a role row opens to a model per slot, and Start sends each slot with its model', async () => {
  const L = await open();
  const roles = L.doc.querySelectorAll('[data-trole]').map(r => r.dataset.trole);
  assert.equal(roles.length, 8);
  for (const r of roles) for (let k = 0; k < 3; k++) { L.$('[data-trole="' + r + '"] [data-tstep="1"]').click(); await L.settle(); }
  assert.match(L.$('#nsSw').textContent, /^manager \+ 32 workers · your team$/);
  assert.equal((cli(L).match(/--role-model/g) || []).length, 32, 'one --role-model per slot');
  assert.equal(L.$('[data-trole="backend"] [data-tstep="1"]').disabled, false, 'no ceiling: a 33rd worker can be added');
  const slots = () => L.doc.querySelectorAll('[data-tslot="backend"]');
  assert.equal(slots().length, 4, 'individual model selectors are visible without discovery');
  L.$('[data-topen="backend"]').click(); await L.settle(); assert.equal(slots().length, 0);
  L.$('[data-topen="backend"]').click(); await L.settle();
  assert.equal(slots().length, 4); assert.equal(L.$('[data-topen="backend"]').getAttribute('aria-expanded'), 'true');
  for (const [i, v] of [[0, 'a/x'], [1, 'a/x'], [2, 'b/y'], [3, 'b/y']]) await change(L, slots()[i], v);
  assert.deepEqual(slots().map(chosen), ['a/x', 'a/x', 'b/y', 'b/y'], 'each slot shows its model');
  assert.match(L.$('[data-tmodel="backend"]').textContent, /2 models: set all slots/, 'the row\'s own select says its slots differ');
  assert.equal((cli(L).match(/--role-model backend=b\/y/g) || []).length, 2);
  assert.equal((cli(L).match(/--role-model backend=a\/x/g) || []).length, 2);
  L.$('#nsStart').click(); await L.settle(); L.advance(200); await L.settle();
  const b = spec(L);
  assert.equal(b.swarm, 32); assert.equal(b.team.length, 32);
  assert.deepEqual(b.team.filter(t => t.role === 'backend').map(t => t.model), ['a/x', 'a/x', 'b/y', 'b/y']);
  for (const r of roles) assert.equal(b.team.filter(t => t.role === r).length, 4, r);
});

test('the row\'s own model sets every slot of the role again; a team whose slots differ keeps them slot by slot', async () => {
  const L = await open({ maxWorkers: 24 });
  L.$('[data-trole="tester"] [data-tstep="1"]').click(); await L.settle();
  await change(L, L.doc.querySelectorAll('[data-tslot="tester"]')[1], 'b/y');
  assert.match(cli(L), /--role-model tester --role-model tester=b\/y/, 'the first slot is the worker model, the second b/y');
  await change(L, L.$('[data-tmodel="tester"]'), 'a/x');
  assert.equal((cli(L).match(/--role-model tester=a\/x/g) || []).length, 2, 'set all slots');
  assert.deepEqual(L.doc.querySelectorAll('[data-tslot="tester"]').map(chosen), ['a/x', 'a/x']);
  const D = L.SL.ui.dialogs, st = { counts: { backend: 2 }, perSlot: { backend: ['a/x', 'b/y'] }, slotModels: {} };
  assert.equal(D.slotModel(st, 'backend', 1), 'b/y');
  D.setSlotModel(st, 'backend', 1, 'a/x');
  assert.equal(st.perSlot.backend, undefined, 'one model again: the role\'s'); assert.equal(st.slotModels.backend, 'a/x');
  const inherited = { counts: { backend: 2 }, perSlot: {}, slotModels: {}, roleModels: { backend: 'b/y' } };
  assert.equal(D.slotModel(inherited, 'backend', 0), 'b/y');
  D.setSlotModel(inherited, 'backend', 0, '');
  assert.equal(D.slotModel(inherited, 'backend', 0), '', 'explicit worker model overrides the configured role');
  assert.equal(D.slotModel(inherited, 'backend', 1), 'b/y');
  D.setSlotModel(inherited, 'backend', 1, '');
  assert.equal(inherited.perSlot.backend, undefined);
  assert.equal(inherited.slotModels.backend, '', 'collapsed empty override is retained');
  assert.equal(D.slotModel(inherited, 'backend', 0), '');
});

test('the horse: a leg is a role, labelled with its worker or its count, and a large team is drawn in role bands', async () => {
  const roster = [{ id: 'mgr', role: 'manager', code: 'mgr', nth: 0, k: 0, leg: -1, scope: '-', ro: false, model: 'm', spawn: 0 }];
  const order = ['architect', 'scout', 'backend', 'frontend', 'devops', 'tester', 'docs', 'reviewer'], codes = ['ar', 'sc', 'be', 'fe', 'dv', 'ts', 'dc', 'rv'];
  order.forEach((r, k) => { for (let i = 1; i <= 4; i++) roster.push({ id: codes[k] + '-' + i, role: r, code: codes[k], nth: i, k: roster.length, leg: k, scope: '**', ro: false, model: i <= 2 ? 'a/x' : 'b/y', spawn: 1 }); });
  const snap = { tab: { id: 'a', sid: 's-a', name: 'crew', cwd: '/p/a', gen: 1, order: 0 }, gen: 1, seq: 2, now: 10, meta: { model: 'm', mode: 'default', startedAt: 1760000000000, swarm: 32, team: roster.slice(1).map(r => ({ role: r.role, model: r.model })) }, roster, keyframe: [], events: [{ seq: 1, t: 2, k: 'state', id: 'be-3', s: 'edit', doing: 'Edit x' }], hist: [], questions: [] };
  const L = await boot({ route: (m, p) => p === '/api/sessions/a/snapshot' ? snap : undefined });
  const labels = L.doc.querySelectorAll('.hlab .id').map(t => t.textContent);
  assert.deepEqual(labels, ['ar ×4', 'sc ×4', 'be ×4', 'fe ×4', 'dv ×4', 'ts ×4', 'dc ×4', 'rv ×4']);
  const legs = L.SL.ui.hero.roleLegs(L.SL.sessions.active);
  assert.deepEqual(JSON.parse(JSON.stringify(legs[2].ids)), ['be-1', 'be-2', 'be-3', 'be-4']);
  assert.equal(L.doc.querySelectorAll('.stalls.dense .sgroup').length, 8, 'eight role bands');
  assert.equal(L.doc.querySelectorAll('.stalls.dense .stall').length, 32, 'one stall per worker');
  assert.match(L.$('.sgroup[data-role="backend"] .sg-h').textContent, /backend×4x ×2y ×2/);
  /* a team of eight, one per role, is labelled with its workers, as before */
  const eight = Object.assign({}, snap, { meta: Object.assign({}, snap.meta, { swarm: 8, team: order.map(r => ({ role: r, model: '' })) }), roster: roster.filter(r => r.nth <= 1) });
  const E = await boot({ route: (m, p) => p === '/api/sessions/a/snapshot' ? eight : undefined });
  assert.deepEqual(E.doc.querySelectorAll('.hlab .id').map(t => t.textContent), ['ar-1', 'sc-1', 'be-1', 'fe-1', 'dv-1', 'ts-1', 'dc-1', 'rv-1']);
  assert.equal(E.doc.querySelectorAll('.stalls.dense').length, 0, 'eight stalls, not banded');
  const partial = Object.assign({}, snap, { roster: roster.slice(0, 11).map(r => Object.assign({}, r, { leg: -1 })) });
  const P = await boot({ route: (m, p) => p === '/api/sessions/a/snapshot' ? partial : undefined });
  assert.equal(P.doc.querySelectorAll('.stall.ghost').length, 22, 'all unoccupied slots appear beyond eight workers');
  assert.equal(P.$('.stall[data-role="backend"] .legn').textContent, 'leg 3', 'chips derive their leg from role, not stale roster indices');
  assert.equal(P.$('.stall.ghost').getAttribute('aria-label'), 'backend slot is free');
  assert.equal(P.$('.stall.ghost .s-id').textContent, 'free backend slot');
  const custom = Object.assign({}, snap, { meta: Object.assign({}, snap.meta, { team: [{ role: 'unspawned' }, { role: 'second' }, { role: 'first' }] }), roster: [roster[0], { ...roster[1], id: 'one', role: 'first' }, { ...roster[2], id: 'two', role: 'second' }] });
  const C = await boot({ route: (m, p) => p === '/api/sessions/a/snapshot' ? custom : undefined });
  assert.equal(C.SL.ui.hero.legOfRole(C.SL.sessions.active, 'first'), 0, 'custom roles follow roster spawn order, matching translator');
  assert.equal(C.SL.ui.hero.legOfRole(C.SL.sessions.active, 'second'), 1);
  assert.equal(C.SL.ui.hero.legOfRole(C.SL.sessions.active, 'unspawned'), 2);
});
