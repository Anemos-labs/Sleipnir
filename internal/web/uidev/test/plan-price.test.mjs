// plan-price.test.mjs: usage a ChatGPT plan covered bills nothing per token and is shown at the API list price, marked: the helpers
// of 00-util.js, the reducer and its totals over the plan recording of the parity guard (internal/parity/testdata/plan.ui.jsonl), and
// the markup of the places that show such a figure (the budget page and its gauge, the Models page's rows, the recorded sessions).
// The terminal's half is internal/tui/app/plan_test.go; the wording of the mark is state.PlanHelp there and PLAN_HELP here.
import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { context, load } from './harness.mjs';
import { page } from './pages-harness.mjs';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const STREAM = path.resolve(HERE, '../../../parity/testdata/plan.ui.jsonl');
const FACTS = path.resolve(HERE, '../../../parity/testdata/facts-plan.json');
const MODS = ['00-namespace.js', '00-util.js', 'api.js', '11-data-live.js', '20-clock.js', '30-model.js'];
const PLAN_HELP = 'at API list price; billed to your ChatGPT plan';

test('the helpers: a plan\'s figure is the bill plus the list-price value, with ≈ and the plan tag; any other figure is the bill', () => {
  const U = page({}).SL.u;
  assert.equal(U.PLAN_HELP, PLAN_HELP, 'the same words as the terminal (state.PlanHelp)');
  assert.equal(U.fmtSpend({ cost: 0, list: 0.0076, plan: true }, 4), '≈$0.0076');
  assert.equal(U.fmtSpend({ cost: 0.5, list: 0.25, plan: true }, 2), '≈$0.75');
  assert.equal(U.fmtSpend({ cost: 0, list: 0.0038, plan: true }, 2), '≈$0.0038', 'a plan\'s figure under a cent is not ≈$0.00');
  assert.equal(U.fmtSpend({ cost: 0.0038, plan: false }, 2), '$0.00', 'a billed figure keeps the precision it is asked for');
  assert.equal(U.fmtSpend({ cost: 0.5, list: 0.25, plan: false }, 2), '$0.50', 'a list figure without the plan flag is not shown');
  assert.match(U.planTag({ plan: true }), /class="plantag" title="at API list price; billed to your ChatGPT plan">plan</);
  assert.equal(U.planTag({ plan: false }), '');
  assert.equal(U.fmtSpend({ cost: '<img>', list: 'x', plan: true }, 2), '≈$0.00', 'what is not a number is nothing');
  // a budget gauge fills with what was billed, also in a session that mixes plan usage and billed usage
  assert.equal(U.planOnly({ cost: 0, list: 0.3, plan: true }), true);
  assert.equal(U.planOnly({ cost: 0.2, list: 0.3, plan: true }), false, 'a mixed session has billed spend');
  assert.equal(U.planOnly({ cost: 0, plan: false }), false);
  assert.equal(U.budgetPct({ cost: 2.5, list: 9, plan: true }, 5), 50);
  assert.equal(U.budgetPct({ cost: 0, list: 9, plan: true }, 5), 0);
  assert.equal(U.budgetPct({ cost: 9 }, 5), 100);
  assert.equal(U.budgetPct({ cost: 1 }, 0), 0, 'no budget, no fill');
});

test('the reducer over a plan\'s recording: nothing billed, the list-price value per agent and in all, as the terminal computes it', () => {
  const SL = load(context({}), MODS).SL;
  const events = fs.readFileSync(STREAM, 'utf8').split('\n').filter(Boolean).map(l => JSON.parse(l));
  const roster = [{ id: 'mgr', role: 'manager', code: 'mgr', nth: 1, k: 0, leg: -1, scope: '', ro: false, model: 'gpt-6-luna', spawn: 0 },
    { id: 'be-1', role: 'backend', code: 'be', nth: 1, k: 1, leg: 0, scope: '', ro: false, model: 'gpt-6-luna', spawn: 0 }];
  const m = SL.model.newModel({ id: 't', meta: { goalText: '' }, roster }, {});
  events.forEach(e => SL.model.reduce(m, e));
  const T = SL.calc.totals(m), want = JSON.parse(fs.readFileSync(FACTS, 'utf8'));
  assert.equal(T.cost, 0); assert.equal(T.plan, true);
  assert.ok(Math.abs(T.list - want.list_usd) < 1.5e-6, T.list + ' vs ' + want.list_usd);
  const be = SL.calc.agent(m.ag['be-1']);
  assert.equal(be.plan, true); assert.ok(be.list > 0); assert.equal(be.cost, 0);
  assert.equal(SL.u.fmtSpend(T, 4), '≈$' + (T.list).toFixed(4));
});

const session = o => Object.assign({ id: 'p', name: 'p', roster: [{ id: 'mgr', role: 'manager', model: 'chatgpt/gpt-6-luna' }], wm: {}, m: {}, meta: { model: 'chatgpt/gpt-6-luna', mode: 'default', effort: 'default', budget: 5, roleModels: {}, rules: [], swarm: 0, isolation: 'none', verify: '', cwd: '/p', trustProject: true } }, o || {});

test('the budget page of a plan\'s session: the list-price figure marked, and a gauge the plan does not fill', () => {
  const { SL } = page({ modules: ['91-views-b.js', '98-ui-settings.js'] });
  SL.calc.totals = () => ({ cost: 0, list: 0.0076, plan: true, pct: 70, prompt: 1000, out: 100, saved: 0.002 });
  const html = SL.toolkit.settings.pages.budget(session(), SL.toolkit.settings.newState({ page: 'budget' }));
  assert.match(html, /id="budSpent">≈\$0\.0076</);
  assert.match(html, /plan: at API list price; billed to your ChatGPT plan/);
  assert.match(html, /class="gauge wide plan"><i id="budBar" style="width:0%"/, 'the budget counts what is billed: nothing fills it');
  assert.match(html, /id="budPct">\$0\.00 billed: the plan counts against no budget</);
  // a session that mixes plan usage and billed usage: its gauge fills with the bill, and no exemption is claimed
  SL.calc.totals = () => ({ cost: 2.5, list: 0.75, plan: true, pct: 70, prompt: 1000, out: 100, saved: 0.002 });
  const mixed = SL.toolkit.settings.pages.budget(session(), SL.toolkit.settings.newState({ page: 'budget' }));
  assert.match(mixed, /id="budSpent">≈\$3\.2500</);
  assert.match(mixed, /class="gauge wide plan"><i id="budBar" style="width:50%"/);
  assert.match(mixed, /id="budPct">50% used \(billed; the plan&#39;s part is not counted\)</);
  assert.doesNotMatch(mixed, /counts against no budget/);
  assert.match(mixed, /spent, with plan usage at API list price/);
  const T = SL.toolkit.settings;
  assert.equal(T.budNote({ cost: 0, plan: true }, 0), 'no limit; the plan counts against no budget');
  assert.equal(T.budNote({ cost: 1, plan: false }, 4), '25% used');
  SL.calc.totals = () => ({ cost: 2.5, list: 0, plan: false, pct: 70, prompt: 1000, out: 100, saved: 0.002 });
  const billed = SL.toolkit.settings.pages.budget(session(), SL.toolkit.settings.newState({ page: 'budget' }));
  assert.match(billed, /id="budSpent">\$2\.5000</); assert.match(billed, /class="gauge wide"><i id="budBar" style="width:50%"/);
  assert.doesNotMatch(billed, /plan/);
});

test('the Models page shows a plan\'s model at the API list price, tagged plan', () => {
  const { SL } = page({ modules: ['91-views-b.js', '98-ui-settings.js'] });
  SL.D.models = [{ ref: 'chatgpt/gpt-6-luna', provider: 'chatgpt', ctx: 272000, in: 0.1, out: 0.5, cached: 0.01, tools: true, reasoning: true, plan: true },
    { ref: 'a/priced', provider: 'a', ctx: 8000, in: 1, out: 2, cached: 0.1, tools: true, reasoning: false }];
  const html = SL.toolkit.settings.pages.models(session(), SL.toolkit.settings.newState({ page: 'models' }));
  const row = ref => html.split('<tr').find(p => p.includes('>' + ref + '<')).split('</tr>')[0];
  assert.match(row('chatgpt/gpt-6-luna'), /<td class="r">0\.10<\/td><td class="r">0\.50<\/td>/);
  assert.match(row('chatgpt/gpt-6-luna'), /class="tag ok" title="a ChatGPT plan covers its use[^"]*">plan</);
  assert.doesNotMatch(row('chatgpt/gpt-6-luna'), /price unknown/);
  assert.doesNotMatch(row('a/priced'), />plan</);
});

test('a live session card: the plan\'s label, tooltip and gauge are drawn again when the session\'s first plan usage arrives', () => {
  const S = { id: 's1', name: 'work', sid: 'x', recorded: false, state: () => 'run', roster: [{ id: 'mgr' }], wm: { merged: [], torder: [] }, meta: { cwd: '/p', model: 'chatgpt/gpt-6-luna', mode: 'default', launch: 'sleipnir chat', started: '', budget: 5 } };
  const { SL } = page({ modules: ['91-views-b.js'], sessions: [S] }), K = SL.toolkit.sessions;
  let totals = { cost: 0, list: 0, plan: false, pct: 0, prompt: 0, out: 0, saved: 0 };
  SL.calc.totals = () => totals;
  const before = K.liveSig([S], [], 0, 0), card = K.cardHtml(S);
  assert.doesNotMatch(card, /plan ·|gauge wide plan/);
  totals = { cost: 0, list: 0.0038, plan: true, pct: 80, prompt: 1000, out: 10, saved: 0 };
  assert.notEqual(K.liveSig([S], [], 0, 0), before, 'the cards are drawn again: their plan markup is drawn with them');
  const planned = K.cardHtml(S);
  assert.match(planned, /data-c="cost" title="at API list price; billed to your ChatGPT plan">≈\$0\.0038</);
  assert.match(planned, /plan · of \$5\.00/);
  assert.match(planned, /class="gauge wide plan"><i style="width:0%"/);
  totals = { cost: 1, list: 0.5, plan: true, pct: 80, prompt: 1000, out: 10, saved: 0 };   // mixed: the bill fills the gauge
  assert.match(K.cardHtml(S), /class="gauge wide plan"><i style="width:20%"/);
  assert.equal(K.liveSig([S], [], 0, 0), K.liveSig([S], [], 0, 0), 'the signature is stable while nothing changes');
});

test('a recorded session that ran on a plan shows its list-price figure, marked', () => {
  const { SL } = page({ modules: ['91-views-b.js'] }), S = SL.toolkit.sessions;
  const rec = (id, o) => Object.assign({ id, first: 'a prompt', model: 'chatgpt/gpt-6-luna', cost: 0, mb: 1, ageS: 60, agents: 1, resumable: true, lastWritten: 1 }, o);
  const html = S.recHtml([rec('a1', { list: 0.42, plan: true }), rec('b2', { cost: 0.42 })], { sel: new Set(), hosted: new Set(), now: 1e12 });
  const row = id => html.split('<tr data-rid="').find(p => p.startsWith(id + '"'));
  assert.match(row('a1'), /≈\$0\.42 <span class="plantag"/);
  assert.match(row('b2'), /<td class="r">\$0\.42<\/td>/);
});
