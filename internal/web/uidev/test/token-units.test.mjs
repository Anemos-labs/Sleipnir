// token-units.test.mjs: token counts are written as the terminal writes them (widget.Tokens: k, M and B, one decimal above 999), and
// the footer counts the tokens of every agent of the session, not the manager's alone.
import test from 'node:test';
import assert from 'node:assert/strict';
import { boot } from './pagedom.mjs';

test('token counts have the units of the terminal: k, then M, then B', async () => {
  const L = await boot(), fmtK = L.SL.u.fmtK;
  // The cases of TestTokens (internal/tui/widget/gauge_test.go): the two interfaces write a count the same way.
  const cases = [[0, '0'], [812, '812'], [999, '999'], [1000, '1.0k'], [1234, '1.2k'], [12100, '12.1k'], [48300, '48.3k'], [99999, '100.0k'],
    [123456, '123.5k'], [999949, '999.9k'], [999950, '1.0M'], [1_200_000, '1.2M'], [999_949_999, '999.9M'], [999_950_000, '1.0B'],
    [3_400_000_000, '3.4B'], [-812, '-812'], [-12100, '-12.1k']];
  for (const [n, want] of cases) assert.equal(fmtK(n), want, 'fmtK(' + n + ')');
});

test('the footer counts the tokens of every agent of the session', async () => {
  const L = await boot(), S = L.SL.sessions.active;
  L.SL.model.reduce(S.m, { k: 'req', id: 'mgr', t: 1, ratio: .5, p: 2_000_000, o: 1000 }, S);
  L.SL.model.reduce(S.m, { k: 'req', id: 'be-1', role: 'backend', t: 2, ratio: .5, p: 3_000_000, o: 400_000 }, S);
  await L.step(5); await L.settle();
  const m = S.m, c = L.SL.calc.totals(m), mg = m.ag.mgr;   // the model the page draws (the events above went into it)
  assert.ok(c.prompt > mg.rd + mg.un && c.out > mg.out, 'the team used more than the manager');
  const line = L.$('#statusLine').textContent;
  assert.ok(line.includes('↑' + L.SL.u.fmtK(c.prompt) + ' ↓' + L.SL.u.fmtK(c.out)), 'the footer shows the team\'s tokens: ' + line);
  assert.ok(!line.includes('↑' + L.SL.u.fmtK(mg.rd + mg.un) + ' '), 'the footer does not show the manager\'s alone: ' + line);
});
