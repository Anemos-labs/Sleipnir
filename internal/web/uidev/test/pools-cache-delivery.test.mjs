import test from 'node:test';
import assert from 'node:assert/strict';
import { boot, FakeEvent } from './pagedom.mjs';

test('600 requests retain cumulative counts and render a bounded window of readable absolute request numbers', async () => {
  const L = await boot(), S = L.SL.sessions.active;
  for (let i = 1; i <= 600; i++) {
    if (i === 599) L.SL.model.reduce(S.m, { k: 'compact', id: 'mgr', t: i - .5, from: 10000, to: 1000, pct: -90 }, S);
    L.SL.model.reduce(S.m, { k: 'req', id: 'mgr', t: i, ratio: .95 }, S);
  }
  L.SL.ui.cacheFollow = false; L.SL.ui.cacheAgent = 'mgr';
  L.SL.views.show('cache'); await L.settle();
  const A = S.m.ag.mgr;
  assert.equal(A.nreq, 600); assert.equal(A.ratios.length, 400);
  const labels = () => L.doc.querySelectorAll('.hbar .nn').map(n => Number(n.textContent));
  const count = labels().length; assert.ok(count >= 6 && count <= 30); assert.equal(labels()[0], 601 - count); assert.equal(labels().at(-1), 600);
  assert.match(L.$('.c-hit .c-note').textContent, /600 requests/);
  assert.equal(L.doc.querySelectorAll('.hbar .mk').filter(n => n.textContent.includes('◆')).length, 1);
  L.$('[data-cache-page="prev"]').click(); await L.settle();
  assert.equal(labels()[0], 601 - 2 * count); assert.equal(labels().at(-1), 600 - count);
  L.$('[data-cache-page="latest"]').click(); await L.settle(); assert.equal(labels().at(-1), 600);
});

test('pool subtasks expand inside their parent and retain their own owner and status', async () => {
  const L = await boot();
  await L.event('task', { id: 'T10', title: 'Review combined result', owner: 'be-1', s: 'running', poolPhase: 'reconciling', poolMembers: ['be-1', 'be-2'] });
  await L.event('task', { id: 'T11', title: 'Review concurrency', owner: 'be-2', parent: 'T10', s: 'merged' });
  await L.step(5); L.SL.views.show('board'); await L.settle();
  const parent = L.$('.tcard[data-task="T10"]'); assert.ok(parent);
  assert.match(parent.textContent, /reconciling/);
  assert.equal(L.$('.tcard[data-task="T11"]'), null);
  parent.querySelector('[data-pool-toggle]').click(); await L.settle();
  const child = parent.querySelector('.subtasks .tcard[data-task="T11"]');
  assert.ok(child); assert.equal(child.dataset.owner, 'be-2'); assert.match(child.textContent, /✓/);
});

test('GitHub toggles belong to one objective and do not leak across tabs', async () => {
  const L = await boot({ two: true });
  L.$('#autoFixCI').checked = true; L.$('#autoFixCI').dispatchEvent(new FakeEvent('change', { bubbles: true }));
  L.$('#autoMergePR').checked = true; L.$('#autoMergePR').dispatchEvent(new FakeEvent('change', { bubbles: true }));
  L.SL.act.switchSession('b'); await L.settle(); assert.equal(L.$('#autoMergePR').checked, false);
  L.SL.act.switchSession('a'); await L.settle(); assert.equal(L.$('#autoMergePR').checked, true);
  await L.type('repair this'); L.press('enter'); await L.settle();
  const sent = L.posts.filter(p => p.path.endsWith('/messages')).at(-1);
  assert.equal(sent.body.autoFix, true); assert.equal(sent.body.autoMerge, true);
  assert.equal(L.$('#autoFixCI').checked, false); assert.equal(L.$('#autoMergePR').checked, false);
  await L.type('another objective'); L.press('enter'); await L.settle();
  const next = L.posts.filter(p => p.path.endsWith('/messages')).at(-1);
  assert.equal(next.body.autoFix, undefined); assert.equal(next.body.autoMerge, undefined);
});
