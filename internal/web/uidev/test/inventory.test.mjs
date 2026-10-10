// inventory.test.mjs: the inventory of the page (internal/parity/webui.json, made by internal/web/uidev/inventory.mjs) is current, and says
// true things about the page: every entry of the rail, the palette and the Settings pages leads to something that exists, every flag of every
// command has a control in the runner's form, and every flag of the New session dialog is a flag of `sleipnir chat`. The Go tests of the
// parity guard read the file, so a stale file would let a change to the page pass them.
import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import { build, generate, render, classify, controlsOf, OUT_FILE, SPEC_FILE, REGENERATE } from '../inventory.mjs';

const spec = JSON.parse(fs.readFileSync(SPEC_FILE, 'utf8'));
let current = null;
const gen = async () => current || (current = await generate());

test('the checked-in inventory is what the page makes now', async () => {
  const { text } = await gen();
  const have = fs.existsSync(OUT_FILE) ? fs.readFileSync(OUT_FILE, 'utf8') : '';
  assert.ok(have === text, 'internal/parity/webui.json is out of date with the page (or with internal/web/clispec/clispec.json): regenerate it with `' + REGENERATE + '` and commit it');
});

test('every control of every Settings page is classified in SETTING_MAP', async () => {
  const { inv } = await gen();
  assert.deepEqual(inv.unclassifiedSettings, [], 'classify these controls in SETTING_MAP of internal/web/uidev/inventory.mjs');
  assert.equal(classify('seg:a-control-nobody-classified'), null, 'an unknown control is not classified');
  assert.deepEqual(controlsOf('<button data-do="x"></button><input id="y"><select data-sel="z"></select><div id="no"></div>'), ['do:x', 'id:y', 'sel:z']);
});

test('the rail, the palette and the Settings pages lead to views, pages and tabs that exist', async () => {
  const { inv } = await gen();
  const views = new Set(inv.views.map(v => v.name)), navIds = new Set(inv.nav.map(n => n.id)), pages = new Set(inv.settings.map(p => p.id));
  const tabs = new Set(inv.nav.filter(n => n.tab).map(n => n.id));
  for (const n of inv.nav) if (n.view) assert.ok(views.has(n.view), 'rail item ' + n.id + ' opens the view ' + n.view + ', which is not registered');
  for (const p of inv.palette) for (const e of p.effects) {
    const [kind, what] = e.split(/:(.*)/s);
    if (kind === 'view') assert.ok(views.has(what), 'palette entry ' + p.name + ' opens the view ' + what + ', which is not registered');
    if (kind === 'settings') assert.ok(pages.has(what), 'palette entry ' + p.name + ' opens the Settings page ' + what + ', which is not there');
    if (kind === 'nav') assert.ok(navIds.has(what) && tabs.has(what), 'palette entry ' + p.name + ' opens the Workspace tab ' + what + ', which is not on the rail');
    assert.ok(kind !== 'error', 'palette entry ' + p.name + ' failed when it was run: ' + what);
  }
  for (const [cmd, effects] of Object.entries(inv.slash.handlers)) for (const e of effects) {
    const [kind, what] = e.split(/:(.*)/s);
    if (kind === 'view') assert.ok(views.has(what), cmd + ' opens the view ' + what + ', which is not registered');
    if (kind === 'settings') assert.ok(pages.has(what), cmd + ' opens the Settings page ' + what + ', which is not there');
  }
  for (const v of inv.views.filter(x => x.nav)) {
    const reached = inv.nav.some(n => n.view === v.name) || inv.palette.some(p => p.effects.includes('view:' + v.name));
    assert.ok(reached, 'the view ' + v.name + ' is registered but neither the rail nor the palette opens it');
  }
});

test('the runner has a control for every flag and every positional of every command of the spec', async () => {
  const { inv } = await gen();
  assert.deepEqual([...inv.runner.listed].sort(), spec.commands.map(c => c.path.join(' ')).sort(), 'the runner lists every command of the spec');
  for (const c of spec.commands) {
    const key = c.path.join(' '), r = inv.runner.commands[key];
    assert.ok(r, 'the runner does not list ' + key);
    assert.deepEqual([...r.flags].sort(), c.flags.map(f => f.name).sort(), key + ': the form has the flags of the spec');
    assert.deepEqual([...r.positionals].sort(), (c.positional || []).map(p => p.name).sort(), key + ': the form has the positionals of the spec');
    assert.equal(r.mode, c.mode, key + ': the runner runs it in the mode of the spec');
  }
});

test('the New session dialog advertises only flags that its command line builds, and those are flags of `sleipnir chat`', async () => {
  const { inv } = await gen();
  const chat = new Set(spec.commands.find(c => c.path.join(' ') === 'chat').flags.map(f => f.name));
  for (const f of inv.newSession.flags) assert.ok(chat.has(f), 'the New session command line has --' + f + ', which `sleipnir chat` does not');
  for (const f of inv.newSession.controlFlags) assert.ok(inv.newSession.flags.includes(f), 'a control of the New session dialog sets --' + f + ' but its command line never builds it');
  for (const f of inv.newSession.flags.filter(x => x !== 'resume')) assert.ok(inv.newSession.controlFlags.includes(f), 'the command line of the New session dialog builds --' + f + ' but no control of the dialog sets it');
  assert.ok(inv.resume.latestButton && inv.resume.rowButton && inv.resume.settingsButton, 'the Resume dialog offers --continue, a Resume button and a "with settings" button for a recorded session');
  for (const f of inv.runLine.flags) assert.ok(chat.has(f), 'the Run settings line has --' + f + ', which `sleipnir chat` does not');
  for (const s of inv.settings) for (const c of s.controls) if (c.flag) assert.ok(chat.has(c.flag), s.id + ' ' + c.id + ' sets --' + c.flag + ', which `sleipnir chat` does not have');
  assert.ok(inv.newSession.modes.includes('bypass') && inv.newSession.modes.length === 5, 'the dialog offers the five permission modes');
});

test('the inventory reads the spec: a command and a flag added to it appear in the runner', async () => {
  const fake = JSON.parse(JSON.stringify(spec));
  fake.commands.push({ path: ['zzz-fake'], usage: 'sleipnir zzz-fake', summary: 'a command only this test has', positional: [{ name: 'WHERE', required: false }], flags: [{ name: 'zzz-flag', arg: 'bool', default: null, repeatable: false, desc: 'x' }], mode: 'run' });
  const inv = await build(fake);
  assert.deepEqual(inv.runner.commands['zzz-fake'].flags, ['zzz-flag']);
  assert.deepEqual(inv.runner.commands['zzz-fake'].positionals, ['WHERE']);
  assert.ok(inv.palette.some(p => p.name === 'sleipnir zzz-fake'), 'the palette lists it');
  assert.notEqual(render(inv), (await gen()).text, 'and the file would change');
});
