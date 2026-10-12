// new-session-form.test.mjs: two rules of the New session dialog (87-ui-sheets.js), on the shipped page booted on the fake DOM of
// pagedom.mjs. The budget starts at off (no limit) and Start says so (budget 0: the server does not put its own cap on the session);
// a budget typed is sent as it is, and the command line under the dialog lifts the terminal's cap of a team in the same way. The
// pre-approved actions (--allow) cannot be changed in the modes that never ask (bypass, yolo): the field is disabled with its reason,
// the rules already chosen are kept for another mode, and none of them is sent or shown in the command line.
import test from 'node:test';
import assert from 'node:assert/strict';
import { boot, FakeEvent } from './pagedom.mjs';

/** The server's defaults that the dialog reads (the setting the terminal needs to run a team with no cap comes from the server). */
const DEFAULTS = { unlimitedEnv: 'SLEIPNIR_SWARM_BUDGET_USD=0', swarm: 8, maxWorkers: 24 };
async function open(seed, defaults) {
  const L = await boot({ route: (m, p) => (p === '/api/hello' ? { boot: 'b1', streamAfter: 7, server: { addr: '127.0.0.1:7777' }, tabs: [], active: '', ui: {}, defaults: Object.assign({}, DEFAULTS, defaults) } : undefined) });
  L.SL.ui.dialogs.newSession(Object.assign({ cwd: '/p/a' }, seed)); await L.settle(); L.advance(200); await L.settle();
  return L;
}
const spec = L => { const p = L.posts.filter(x => x.path === '/api/sessions'); return p.length ? p[p.length - 1].body : null; };
const start = async L => { L.$('#nsStart').click(); await L.settle(); L.advance(200); await L.settle(); };
const type = (L, sel, v) => { const i = L.$(sel); i.value = v; i.dispatchEvent(new FakeEvent('input', { bubbles: true })); };
const pickMode = async (L, m) => { L.$('[data-nsmode="' + m + '"]').click(); await L.settle(); };
const cli = L => L.$('#nsCli').textContent;

test('the budget starts at off and Start sends 0, which is no limit; a number typed is sent as it is', async () => {
  const L = await open();
  assert.equal(L.$('#nsBudget').value, 'off');
  await start(L);
  assert.equal(spec(L).budget, 0, 'off is said in the request: the server does not apply its own cap');
  const M = await open(); type(M, '#nsBudget', '20'); await M.settle(); await start(M);
  assert.equal(spec(M).budget, 20);
  const N = await open(); type(N, '#nsBudget', 'lots'); await start(N);
  assert.equal(spec(N), null, 'a budget that is not a number or off starts nothing');
  assert.match(N.$('#toasts').textContent, /a number of dollars, or off/);
});

test('the command line lifts the terminal\'s cap of a team when the budget is off, and says nothing of it otherwise', async () => {
  const L = await open({ swarm: 8 });
  assert.match(cli(L), /^SLEIPNIR_SWARM_BUDGET_USD=0 sleipnir chat /, 'a team with no budget: the terminal would cap it at its built-in amount');
  type(L, '#nsBudget', '20'); await L.settle();
  assert.match(cli(L), /^sleipnir chat .* --budget-usd 20/, 'a budget is the budget');
  const S = await open({ swarm: 0 });
  assert.match(cli(S), /^sleipnir chat /, 'a single agent has no cap to lift');
});

test('in bypass and yolo the allow rules cannot be changed, are not sent and are not in the command line; another mode gets them back', async () => {
  const L = await open({ rules: ['Bash(make:*)'] });
  assert.equal(L.$('#nsRuleSet').disabled, false);
  assert.equal(L.$('#nsRuleOff').hidden, true);
  assert.match(cli(L), /--allow 'Bash\(make:\*\)'|--allow Bash/, 'the rule is in the command line while it is used');
  for (const mode of ['bypass', 'yolo']) {
    await pickMode(L, mode);
    assert.equal(L.$('#nsRuleSet').disabled, true, mode + ': the fields are disabled');
    assert.equal(L.$('#nsRuleOff').hidden, false);
    assert.match(L.$('#nsRuleOff').textContent, /bypass and yolo never ask: the rules are not used here/);
    assert.doesNotMatch(cli(L), /--allow/);
    // a click that gets through anyway (a disabled button does not fire in a browser) changes nothing
    L.$('#nsRuleEx [data-ex="tests"]').click(); L.$('#nsRules [data-rr]').click(); await L.settle();
    assert.equal(L.doc.querySelectorAll('#nsRules .tag').length, 1, 'the rule kept is still the only one');
  }
  await pickMode(L, 'plan');
  assert.equal(L.$('#nsRuleSet').disabled, false);
  assert.equal(L.$('#nsRuleOff').hidden, true);
  assert.match(cli(L), /--allow/, 'the rules are back');
  await pickMode(L, 'yolo'); await start(L);
  assert.deepEqual([spec(L).mode, spec(L).rules], ['yolo', []]);
  const M = await open({ rules: ['tests'] }); await pickMode(M, 'plan'); await start(M);
  assert.deepEqual(spec(M).rules, ['tests'], 'in a mode that asks they are sent');
});

test('a rule typed and not added is not sent in bypass, where its field is disabled', async () => {
  const L = await open();
  type(L, '#nsRule', 'Bash(go test:*)'); await pickMode(L, 'bypass'); await start(L);
  assert.deepEqual(spec(L).rules, []);
});

test('what needs explaining has a ? beside it, and the explanation shows on hover, on focus and on a tap, and Escape hides it before it closes the dialog', async () => {
  const L = await open();
  const dots = L.doc.querySelectorAll('#overlayHost .qh');
  assert.ok(dots.length >= 14, dots.length + ' dots');
  for (const d of dots) {
    assert.equal(d.textContent, '?');
    assert.ok(d.getAttribute('aria-label'), 'a dot is named');
    assert.ok(d.getAttribute('data-help').length > 20, 'and explains');
  }
  const tip = () => L.$('#helpTip');
  assert.equal(tip().hidden, true, 'nothing shows until it is asked for');
  // no paragraph of explanation stands under a control any more
  assert.equal(L.doc.querySelectorAll('#overlayHost small.hint').length, 0);
  const verify = L.$('#nsVerify').parentNode.parentNode.querySelector('.qh');
  verify.dispatchEvent(new FakeEvent('mouseover', { bubbles: true }));
  assert.equal(tip().hidden, false);
  assert.match(tip().innerHTML, /checks a worker’s task before it counts as done/);
  assert.equal(verify.getAttribute('aria-describedby'), 'helpTip');
  verify.dispatchEvent(new FakeEvent('mouseout', { bubbles: true, relatedTarget: L.$('#nsName') }));
  assert.equal(tip().hidden, true, 'it goes when the pointer does');
  // the keyboard: focus shows it, Escape hides it and the dialog stays; the next Escape closes the dialog
  const budget = L.$('#nsBudget').parentNode.parentNode.querySelector('.qh');
  L.focus('.qh[aria-label="What is the budget?"]');
  assert.equal(tip().hidden, false); assert.match(tip().innerHTML, /stops when it has spent this many US dollars/);
  assert.equal(L.press('esc').defaultPrevented, true);
  assert.equal(tip().hidden, true); assert.equal(L.SL.ui.hasModal(), true);
  // a tap shows it, another hides it
  budget.dispatchEvent(new FakeEvent('click', { bubbles: true })); assert.equal(tip().hidden, false);
  budget.dispatchEvent(new FakeEvent('click', { bubbles: true })); assert.equal(tip().hidden, true);
  // the example rules and the suggested checks explain themselves the same way
  L.$('#nsRuleEx [data-ex="Bash(go test:*)"]').dispatchEvent(new FakeEvent('mouseover', { bubbles: true }));
  assert.match(tip().innerHTML, /go test, with any arguments/);
  // the popover goes with the dialog
  L.SL.ui.closeModal(); assert.equal(L.$('#helpTip'), null);
});

test('what the server sent into an explanation is text, never markup', async () => {
  const hostile = '<img src=x onerror=window.__pwn=1>';
  const L = await open({}, { testsPreset: hostile });
  L.$('#nsRuleEx [data-ex="tests"]').dispatchEvent(new FakeEvent('mouseover', { bubbles: true }));
  assert.equal(L.doc.querySelectorAll('#helpTip img').length, 0, 'no element came out of it');
  assert.match(L.$('#helpTip').textContent, /<img src=x onerror=window\.__pwn=1>/, 'it is shown as it was written');
});

test('the team starts full, one worker of each role, and Start sends no team of its own', async () => {
  const L = await open();
  assert.equal(L.doc.querySelectorAll('[data-trole]').length, 8, 'a row for each role');
  assert.match(L.$('#nsSw').textContent, /manager \+ 8 workers · full team/);
  assert.doesNotMatch(cli(L), /--role-model/);
  await start(L);
  const b = spec(L);
  assert.equal(b.swarm, 8); assert.equal(b.team, undefined, 'the default team is the server\'s own');
});

test('a team of your own: two backends and no docs is sent slot by slot, and auto-docs has nothing to keep', async () => {
  const L = await open();
  const step = (role, d) => L.$('[data-trole="' + role + '"] [data-tstep="' + d + '"]').click();
  step('docs', -1); await L.settle(); step('backend', 1); await L.settle();
  assert.match(L.$('#nsSw').textContent, /manager \+ 8 workers · your team/);
  assert.ok(L.$('[data-k="autoDocs"]').disabled, 'no docs slot: auto-docs is off');
  assert.equal((cli(L).match(/--role-model/g) || []).length, 8, 'one --role-model per slot');
  await start(L);
  const b = spec(L);
  assert.equal(b.swarm, 8);
  assert.equal(b.team.filter(t => t.role === 'backend').length, 2);
  assert.equal(b.team.filter(t => t.role === 'docs').length, 0);
});

test('a team under eight workers cannot start, and says how many to add; Single agent is the other way', async () => {
  const L = await open();
  L.$('[data-trole="docs"] [data-tstep="-1"]').click(); await L.settle();
  assert.ok(L.$('#nsStart').disabled, 'seven workers: Start is off');
  assert.match(L.$('#nsTeamErr').textContent, /at least 8 workers: add 1 more/);
  L.$('[data-tpreset="single"]').click(); await L.settle();
  assert.ok(!L.$('#nsStart').disabled);
  await start(L);
  assert.equal(spec(L).swarm, 0); assert.equal(spec(L).team, undefined);
});
