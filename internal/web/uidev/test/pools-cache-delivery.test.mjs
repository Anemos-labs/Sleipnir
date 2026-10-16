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

test('pool subtasks in their parent\'s column fold inside it, the others are cards of their own column, and each keeps its owner and status', async () => {
  const L = await boot();
  await L.event('task', { id: 'T10', title: 'Review combined result', owner: 'be-1', s: 'running', poolPhase: 'reconciling', poolMembers: ['be-1', 'be-2'] });
  await L.event('task', { id: 'T11', title: 'Review concurrency', owner: 'be-2', parent: 'T10', s: 'running' });
  await L.event('task', { id: 'T12', title: 'Review storage', owner: 'be-2', parent: 'T10', s: 'merged' });
  await L.step(5); L.SL.views.show('board'); await L.settle();
  const parent = L.$('.tcard[data-task="T10"]'); assert.ok(parent);
  assert.match(parent.textContent, /reconciling/);
  const members = parent.querySelectorAll('.dim').find(e => /reconciling/.test(e.textContent));
  assert.equal(members.textContent, 'reconciling · be-1, be-2', 'the members by name: a model id is on the member\'s card, not repeated here');
  const child = parent.querySelector('.subtasks .tcard[data-task="T11"]');
  assert.ok(child, 'open in a column of unfinished work'); assert.equal(child.dataset.owner, 'be-2');
  const merged = L.$('.kan [data-col="merged"] .tcard[data-task="T12"]');
  assert.ok(merged); assert.equal(merged.dataset.owner, 'be-2'); assert.match(merged.textContent, /✓/); assert.match(merged.textContent, /↳ T10/);
  parent.querySelector('[data-pool-toggle]').click(); await L.settle();
  assert.equal(L.$('.tcard[data-task="T11"]'), null);
  L.$('.tcard[data-task="T10"] [data-pool-toggle]').click(); await L.settle();
  const m=L.SL.sessions.active.m; m.torder=['T11','T12','T10'];
  await L.event('task',{id:'T10',title:'Updated aggregate',owner:'be-1',s:'running',poolPhase:'reconciling',poolMembers:['be-1','be-2']});
  await L.settle(); assert.ok(L.$('.tcard[data-task="T10"] .subtasks .tcard[data-task="T11"]'), 'a subtask listed before its parent still nests in it');
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


test('an expired cache page advances to a full retained page', async () => {
 const L=await boot(),S=L.SL.sessions.active;
 for(let i=1;i<=600;i++) L.SL.model.reduce(S.m,{k:'req',id:'mgr',t:i,ratio:.95},S);
 L.SL.ui.cacheFollow=false;L.SL.ui.cacheAgent='mgr';L.SL.views.show('cache');await L.settle();
 for(let i=0;i<20;i++){L.$('[data-cache-page="prev"]').click();await L.settle();}
 for(let i=601;i<=1100;i++) L.SL.model.reduce(S.m,{k:'req',id:'mgr',t:i,ratio:.95},S);
 await L.settle();L.$('[data-cache-page="next"]').click();await L.settle();
 const labels=L.doc.querySelectorAll('.hbar .nn').map(n=>Number(n.textContent));
 assert.ok(labels.length>=6);assert.ok(labels[0]>=701);assert.ok(labels.at(-1)>labels[0]);
});

test('browsing cache history keeps the selected agent when another worker answers', async () => {
  const L = await boot(), S = L.SL.sessions.active;
  for (let i = 1; i <= 100; i++) L.SL.model.reduce(S.m, { k: 'req', id: 'mgr', t: i, ratio: .95 }, S);
  L.SL.ui.cacheFollow = true; L.SL.views.show('cache'); await L.settle();
  L.$('[data-cache-page="prev"]').click(); await L.settle();
  const before = L.$('.c-hit .c-note').textContent;
  L.SL.model.reduce(S.m, { k: 'req', id: 'be-1', role: 'backend', t: 101, ratio: .8 }, S);
  await L.event('req', { id: 'be-1', role: 'backend', t: 102, ratio: .9 });
  await L.step(5); await L.settle();
  assert.equal(L.SL.ui.cacheAgent, 'mgr');
  assert.equal(L.$('.c-hit .c-note').textContent, before);
});


test('following again returns to the latest cache page even for the same agent', async () => {
  const L = await boot(), S = L.SL.sessions.active;
  for (let i = 1; i <= 100; i++) L.SL.model.reduce(S.m, { k: 'req', id: 'mgr', t: i, ratio: .95 }, S);
  L.SL.views.show('cache'); await L.settle();
  L.$('[data-cache-page="prev"]').click(); await L.settle();
  L.doc.body.dispatchEvent(new FakeEvent('keydown', { key: 'f', bubbles: true })); await L.settle();
  assert.equal(L.SL.ui.cacheFollow, true);
  assert.equal(L.doc.querySelectorAll('.hbar .nn').at(-1).textContent, '100');
});

test('standing goals consume automation choices without authorizing later goals', async () => {
 const L=await boot();
 for (const id of ['autoFixCI','autoMergePR']) { L.$('#'+id).checked=true; L.$('#'+id).dispatchEvent(new FakeEvent('change',{bubbles:true})); }
 await L.type('/goal repair and deliver'); L.press('enter'); await L.settle();
 const sent=L.posts.filter(p=>p.path.endsWith('/goal')).at(-1);
 assert.equal(sent.body.action,'set'); assert.equal(sent.body.autoFix,true); assert.equal(sent.body.autoMerge,true);
 assert.equal(L.$('#autoFixCI').checked,false); assert.equal(L.$('#autoMergePR').checked,false);
 L.SL.act.setGoal('another objective'); await L.settle();
 const next=L.posts.filter(p=>p.path.endsWith('/goal')).at(-1);
 assert.equal(next.body.autoFix,undefined); assert.equal(next.body.autoMerge,undefined);
});
