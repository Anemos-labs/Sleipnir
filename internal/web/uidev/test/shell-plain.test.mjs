// shell-plain.test.mjs: the shell says each fact once and in plain words (80-ui-shell.js, 84-ui-chat.js, 96-ui-nav.js): the header keeps
// the goal, the spend and the team and says rate limits only while they slow the team; the menu is grouped (Work, Inspect, Manage) with
// whole-word labels and the same ids and chords; the harness's own start-up lines and warnings go to the collapsed Notices line, not into
// the conversation; and the composer's GitHub switches say what they do and are off, with the reason, while the Delivery plugin is off.
import test from 'node:test';
import assert from 'node:assert/strict';
import { boot, FakeEvent } from './pagedom.mjs';
import { pluginsApi } from '../mockapi.mjs';

/** A page whose server has the reference plugin catalogue with delivery on or off. */
async function page(deliveryOn, extra) {
  const c = pluginsApi().catalogue; Object.assign(c.plugins.find(p => p.id === 'delivery'), { enabled: deliveryOn, state: deliveryOn ? 'on' : 'off' }, extra || {});
  const L = await boot({ route(method, p) { if (method === 'GET' && (p === '/api/plugins' || /^\/api\/sessions\/[^/]+\/plugins$/.test(p))) return c; return undefined; } });
  await L.step(3); await L.settle(); return L;
}

test('the header: goal, spend and team in plain words; rate limits only while they slow the team', async () => {
  const L = await boot(); await L.step(3); await L.settle();
  const hud = L.$('#hud').textContent;
  assert.doesNotMatch(hud, /\brpm\b|429s|retries|prefix warm/i, 'no rate or prefix jargon in the header: ' + hud);
  assert.match(L.$('#hud .hm.budget .lab').textContent, /^Spent$/);
  assert.ok(L.$('#govTxt').hidden, 'no rate-limit line while nothing slows the team');
  const S = L.SL.sessions.active; S.m.r429 = 2; S.m.retries = 3; L.SL.loop.dirty = true; await L.step(2); await L.settle();
  assert.equal(L.$('#govTxt').hidden, false); assert.match(L.$('#govTxt').textContent, /slowed by rate limits: 2 refused \(429\) · 3 retries/);
  assert.doesNotMatch(L.$('#statusLine').textContent, /\$\d/, 'the spend is the header\'s, not the status bar\'s');
  assert.equal(L.$('#sBud'), null, 'one session: the spend of every session is the header\'s');
});

test('the menu: three groups, whole words, the same ids and chords', async () => {
  const L = await boot(); await L.step(2); await L.settle();
  const heads = [...L.doc.querySelectorAll('#nav .nvh').map(h => h.textContent)];
  assert.deepEqual(heads, ['Work', 'Inspect', 'Manage']);
  const labels = L.doc.querySelectorAll('#nav .nvi[data-nav] .nl').map(n => n.textContent);
  assert.ok(!labels.some(t => /^(RADIO|POINTS|WIDEN|Points|Widen)$/.test(t)), labels.join(','));
  assert.equal(L.$('#nvTog .nl').textContent, 'Expand');
  const ids = [...L.SL.ui.nav.items.map(i => i.id + ':' + i.key)].sort();
  assert.deepEqual(ids, ['board:b', 'cache:a', 'changes:h', 'checkpoints:k', 'cockpit:c', 'files:f', 'mail:m', 'memory:y', 'merge:q', 'plugins:u', 'radio:r', 'replay:p', 'research:w', 'scan:d', 'schedule:j', 'sessions:s', 'settings:e', 'tools:t']);
  assert.match(L.$('#nav [data-nav="radio"]').getAttribute('title'), /Radio rail/);
});

test('the harness\'s own notices wait in a collapsed line above the conversation', async () => {
  const L = await boot(); await L.step(2); await L.settle();
  await L.event('sys', { glyph: '⚠', text: 'skills: ~/.claude/skills/x/SKILL.md: [startup-hook] the name differs from the directory name "x"; the name is used' });
  await L.event('sys', { glyph: '◇', text: 'ready: heimdallm/m · default · swarm · session 1' });
  await L.step(2); await L.settle();
  assert.doesNotMatch(L.$('#talk').textContent, /name differs|ready: heimdallm/, 'not in the conversation');
  const bar = L.$('#noticeBar'); assert.equal(bar.hidden, false); assert.match(bar.textContent, /Notices.*1 warning/);
  assert.equal(L.$('#noticeList'), null, 'collapsed');
  L.$('#noticeBar [data-ntog]').click(); await L.settle();
  assert.match(L.$('#noticeList').textContent, /the name differs from the directory name/);
});

test('the GitHub switches explain themselves, and are off with the reason while the Delivery plugin is off', async () => {
  const on = await page(true);
  assert.equal(on.$('#autoFixCI').disabled, false); assert.match(on.$('#autoFixLab').getAttribute('title'), /pull request/);
  assert.match(on.$('#deliveryWhy').textContent, /next message/);
  const L = await page(false);
  assert.equal(L.$('#autoFixCI').disabled, true); assert.equal(L.$('#autoMergePR').disabled, true);
  assert.match(L.$('#deliveryWhy').textContent, /Delivery plugin/); assert.match(L.$('#autoMergeLab').getAttribute('title'), /^Off: the Delivery plugin is turned off/);
  L.$('#autoFixCI').checked = true; L.$('#autoFixCI').dispatchEvent(new FakeEvent('change', { bubbles: true }));
  await L.type('ship it'); L.press('enter'); await L.settle();
  const sent = L.posts.filter(p => p.path.endsWith('/messages')).at(-1);
  assert.equal(sent.body.autoFix, undefined, 'a switch that is off sends nothing');
});

test('the GitHub switches are off with the reason when the server has no gh command', async () => {
  const L = await page(true, { unavailable: 'needs the gh command, which is not installed here' });
  assert.equal(L.$('#autoFixCI').disabled, true); assert.equal(L.$('#autoMergePR').disabled, true);
  assert.match(L.$('#deliveryWhy').textContent, /gh auth login/); assert.match(L.$('#autoFixLab').getAttribute('title'), /^Off: GitHub delivery needs the gh command/);
});
