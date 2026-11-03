// ctxmenu.test.mjs: the page's own right-click menu (86b-ui-ctxmenu.js), on the shipped page booted on the fake DOM. A right-click on
// a thing the views draw opens a menu with a section for each thing it is (closest first), each entry does what a button of the page
// does, the browser's menu is left alone where it is the useful one, and the keys move, run and close the menu.
import test from 'node:test';
import assert from 'node:assert/strict';
import { boot, FakeEvent } from './pagedom.mjs';

/** Right-click el; returns the event (defaultPrevented says whether the page took it). */
function rclick(el, extra) {
  const e = new FakeEvent('contextmenu', Object.assign({ bubbles: true, cancelable: true, button: 2, clientX: 40, clientY: 40 }, extra || {}));
  el.dispatchEvent(e); return e;
}
/** The text of a row of the open menu: '# Heading' for a heading, '(off) Label' for an entry that cannot be done now. */
const label = n => (n.classList.contains('dmh') ? '# ' + n.textContent : (n.disabled ? '(off) ' : '') + (n.querySelector('.dml') ? n.querySelector('.dml').childNodes[0].textContent : n.textContent));
const menuRows = L => { const m = L.$('.ctxmenu'); return m ? m.querySelectorAll('.dmh,.dmi').map(label) : null; };
/** Click the entry whose label starts with text. */
function pick(L, text) {
  const b = L.$('.ctxmenu').querySelectorAll('.dmi').find(n => label(n).startsWith(text)); assert.ok(b, 'no entry ' + text + ' in ' + menuRows(L).join(' | '));
  b.dispatchEvent(new FakeEvent('click', { bubbles: true }));
}

test('a session tab has its own section: switch, rename, a new browser tab, its id, stop, start again, close', async () => {
  const L = await boot({ two: true });
  const tab = L.$('.stab[data-sid="b"]'), e = rclick(tab);
  assert.equal(e.defaultPrevented, true, 'the browser menu is replaced');
  const r = menuRows(L);
  assert.equal(r[0], '# Session · b');
  for (const want of ['Switch to it', 'Rename…', 'Open in a new browser tab', 'Copy the session id', 'Stop the run…', 'Start the team again…', 'Close this session…']) assert.ok(r.includes(want), want + ' in ' + r.join(' | '));
  pick(L, 'Switch to it'); await L.settle();
  assert.equal(L.SL.sessions.active.id, 'b');
  assert.equal(L.$('.ctxmenu'), null, 'an entry closes the menu');
  rclick(L.$('.stab[data-sid="b"]'));
  assert.ok(menuRows(L).includes('(off) Switch to it'), 'the active tab cannot be switched to');
});

test('an agent that is also on a task gets both sections, closest first; its entries open the drawer and fill the message box', async () => {
  const L = await boot();
  const st = L.$('.stall[data-ag="be-1"]'); assert.ok(st, 'the cockpit draws a stall for be-1');
  rclick(st);
  const r = menuRows(L), ia = r.indexOf('# Agent · be-1'), it = r.indexOf('# Task · T1');
  assert.ok(ia >= 0 && it > ia, 'agent then task: ' + r.join(' | '));
  assert.ok(r.includes('Stop this worker…'));
  pick(L, 'Open its details'); await L.settle();
  assert.equal(L.SL.ui.drawerOpen(), true);
  L.SL.ui.closeDrawer();
  rclick(L.$('.stall[data-ag="be-1"]')); pick(L, 'Ask the manager about be-1'); L.advance(100); await L.settle();
  assert.equal(L.$('#input').value, 'About be-1: ');
});

test('a message of the Radio can be copied, quoted, and your own line edited again', async () => {
  const L = await boot(); await L.step(1);
  const you = L.$('#talk .msg.you'); assert.ok(you, 'the conversation has your line');
  rclick(you);
  assert.deepEqual(menuRows(L), ['# Your message', 'Copy the text', 'Quote it in a reply', 'Edit and send again']);
  pick(L, 'Quote it in a reply'); L.advance(100); await L.settle();
  assert.equal(L.$('#input').value, '> hi\n\n');
  L.$('#input').value = 'draft';
  rclick(L.$('#talk .msg.you')); pick(L, 'Edit and send again'); L.advance(100); await L.settle();
  assert.equal(L.$('#input').value, 'hi', 'edit replaces what was in the box');
});

test('nothing in particular: the page entries; the browser menu stays in fields, on links and with Shift', async () => {
  const L = await boot();
  rclick(L.$('#foot'));
  assert.deepEqual(menuRows(L), ['# Sleipnir', 'Command palette…', 'Message the manager', 'New session…', 'Resume a recorded session…', 'Help']);
  pick(L, 'Command palette…'); await L.settle();
  assert.ok(L.$('.palette, [role="dialog"]'), 'the palette opened');
  L.press('esc'); await L.settle();
  for (const el of [L.$('#input')]) { const e = rclick(el); assert.equal(e.defaultPrevented, false, 'a field keeps the browser menu'); assert.equal(L.$('.ctxmenu'), null); }
  const e = rclick(L.$('.stab'), { shiftKey: true }); assert.equal(e.defaultPrevented, false, 'Shift keeps the browser menu'); assert.equal(L.$('.ctxmenu'), null);
});

test('the keys: the arrows move between the entries that can be done, Escape closes and gives the focus back', async () => {
  const L = await boot({ two: true });
  const tab = L.$('.stab[data-sid="a"]'); tab.focus();
  rclick(tab, { button: 0, clientX: 0, clientY: 0 });
  const first = L.doc.activeElement; assert.equal(label(first), 'Rename…', 'the first entry that can be done has the focus (Switch is off)');
  const on = L.$('.ctxmenu').querySelectorAll('.dmi').filter(n => !n.disabled).map(label);
  L.press('down'); assert.equal(label(L.doc.activeElement), on[1], 'down moves to the next entry that can be done');
  L.press('up'); L.press('up'); assert.equal(label(L.doc.activeElement), 'Close this session…', 'the arrows wrap');
  L.press('esc'); await L.settle();
  assert.equal(L.$('.ctxmenu'), null);
  assert.equal(L.doc.activeElement, L.$('.stab[data-sid="a"]'), 'the focus goes back to the tab');
});

test('a section added by another module comes after the built-in ones and goes away with its remover', async () => {
  const L = await boot();
  const off = L.SL.ui.ctx.add(t => (t.closest('.stab') ? { title: 'Extra', items: [{ label: 'Pin it', act() {} }] } : null));
  rclick(L.$('.stab'));
  const r = menuRows(L); assert.deepEqual(r.slice(-2), ['# Extra', 'Pin it']);
  L.SL.ui.ctx.close(); off();
  rclick(L.$('.stab')); assert.ok(!menuRows(L).includes('# Extra'));
});

test('a session tab offers Pin or Unpin when the page can pin, and calls it with the tab\'s id', async () => {
  const L = await boot({ two: true }), seen = [];
  rclick(L.$('.stab[data-sid="b"]')); const has = menuRows(L).some(r => /^Pin|^Unpin/.test(r)); L.SL.ui.ctx.close();
  assert.equal(has, typeof L.SL.act.pinSession === 'function', 'the entry shows exactly when the action exists');
  const keep = L.SL.act.pinSession; L.SL.act.pinSession = id => { seen.push(id); return { ok: true }; };
  rclick(L.$('.stab[data-sid="b"]')); pick(L, 'Pin');
  assert.deepEqual(seen, ['b']);
  L.SL.act.pinSession = keep;
});
