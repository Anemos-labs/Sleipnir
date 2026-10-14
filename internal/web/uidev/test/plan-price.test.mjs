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

test('a recorded session that ran on a plan shows its list-price figure, marked', () => {
  const { SL } = page({ modules: ['91-views-b.js'] }), S = SL.toolkit.sessions;
  const rec = (id, o) => Object.assign({ id, first: 'a prompt', model: 'chatgpt/gpt-6-luna', cost: 0, mb: 1, ageS: 60, agents: 1, resumable: true, lastWritten: 1 }, o);
  const html = S.recHtml([rec('a1', { list: 0.42, plan: true }), rec('b2', { cost: 0.42 })], { sel: new Set(), hosted: new Set(), now: 1e12 });
  const row = id => html.split('<tr data-rid="').find(p => p.startsWith(id + '"'));
  assert.match(row('a1'), /≈\$0\.42 <span class="plantag"/);
  assert.match(row('b2'), /<td class="r">\$0\.42<\/td>/);
});
