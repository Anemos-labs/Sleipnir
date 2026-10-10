// ctrl-d.test.mjs: ctrl+d on the page, which is the terminal's ctrl+d at an empty prompt. The terminal ends the chat; a page's session ends
// by being closed, so the key asks the question the session tab's × asks (SL.ui.closeSessionAsk), and nothing closes without the answer.
// It acts in an empty message box and with nothing focused, and claims the key then (the browser's own ctrl+d bookmarks the page); with
// text in the message box, or with the focus on anything else, it is not the page's.
//
// The page is the shipped one, booted on the fake DOM of pagedom.mjs; the key is dispatched as a browser dispatches it, and what
// follows is read from the page: the dialog that opened, the requests that were made.
import test from 'node:test';
import assert from 'node:assert/strict';
import { boot, FakeEvent } from './pagedom.mjs';

const KEY = 'ctrl+d';
const q1 = { id: 'q_1', agent: 'be-1', task: 'T1', cmd: 'go test ./...', why: 'run the tests', what: 'this command', cwd: '/p/a', t0: 12, offersTests: true, rule: 'Bash(go test:*)' };

/** The dialog that is open: its title and its markup, or null. */
function dialog(L) {
  if (!L.SL.ui.hasModal()) return null;
  const root = L.$('#overlayHost .scrim');
  return { title: root.querySelector('h2').textContent, html: root.outerHTML, text: root.textContent, rec: L.SL.ui.modalState.cur };
}
/** Press the key of the session tab's ×, which is what a click on it does. */
const clickTabClose = (L, id) => L.$('.stx[data-close="' + id + '"]').click();
/** Answer the open dialog: its confirm button (what the person presses) or its cancel. */
const confirmDialog = async L => { L.$('#overlayHost [data-ok]').click(); await L.settle(); };
const cancelDialog = async L => { L.$('#overlayHost [data-no]').click(); await L.settle(); };

test('an empty message box: ctrl+d opens the confirmation of the tab\'s ×, claims the key, and closes nothing by itself', async () => {
  const L = await boot({ two: true });
  L.focus('#input');
  assert.equal(L.$('#input').value, '');
  const e = L.press(KEY);
  await L.settle();
  assert.equal(e.defaultPrevented, true, 'the browser\'s own ctrl+d (a bookmark) must not fire');
  const d = dialog(L);
  assert.ok(d, 'a dialog opened');
  assert.equal(d.title, 'Close the session');
  assert.deepEqual(L.calls, [], 'nothing was asked of the server before the answer');
  assert.equal(L.SL.sessions.list.length, 2, 'the session is still open');

  // the same dialog as the tab's ×, for the same session
  await cancelDialog(L);
  assert.equal(L.SL.ui.hasModal(), false);
  clickTabClose(L, 'a'); await L.settle();
  assert.equal(dialog(L).html, d.html, 'ctrl+d and the × of the tab ask the same question');
});

test('answering the confirmation of ctrl+d closes the session through the request the tab\'s × makes', async () => {
  const viaKey = await boot({ two: true });
  viaKey.focus('#input'); viaKey.press(KEY); await viaKey.settle(); await confirmDialog(viaKey);
  const viaTab = await boot({ two: true });
  clickTabClose(viaTab, 'a'); await viaTab.settle(); await confirmDialog(viaTab);
  assert.ok(viaTab.calls.length > 0, 'closing a session makes a request');
  assert.deepEqual(viaKey.calls, viaTab.calls);
  assert.match(viaKey.calls[0], /^DELETE \/api\/sessions\//);

  // declined, nothing is requested
  const no = await boot({ two: true });
  no.focus('#input'); no.press(KEY); await no.settle(); await cancelDialog(no);
  assert.deepEqual(no.calls, []);
});

test('with nothing focused ctrl+d does the same', async () => {
  const L = await boot({ two: true });
  L.doc.body.focus();
  const e = L.press(KEY); await L.settle();
  assert.equal(e.defaultPrevented, true);
  assert.equal(dialog(L).title, 'Close the session');
  await cancelDialog(L);
  clickTabClose(L, 'a'); await L.settle();
  const viaTab = dialog(L).html;
  await cancelDialog(L);
  L.doc.body.focus(); L.press(KEY); await L.settle();
  assert.equal(dialog(L).html, viaTab);
});

test('a turn that runs does not skip the confirmation: the key asks, and the turn goes on until the answer', async () => {
  const L = await boot({ two: true });
  await L.frame('meta', { tab: 'a', patch: { running: true } });
  L.focus('#input');
  const e = L.press(KEY); await L.settle();
  assert.equal(e.defaultPrevented, true);
  const d = dialog(L);
  assert.ok(d && d.title === 'Close the session', 'the confirmation of the close');
  assert.match(d.text, /Its team stops/);
  assert.deepEqual(L.calls, [], 'no request: the turn is not stopped and the session is not closed before the answer');
  assert.equal(L.SL.sessions.active.meta.running, true);
  await cancelDialog(L);
  assert.deepEqual(L.calls, []);
  assert.equal(L.SL.sessions.list.length, 2);
});

test('an open question is named in the confirmation, as the tab\'s × names it', async () => {
  const L = await boot({ two: true });
  await L.event('ask', { q: q1 }); await L.step(0.2); await L.step(2);
  L.focus('#input');
  const e = L.press(KEY); await L.settle();
  assert.equal(e.defaultPrevented, true);
  const d = dialog(L);
  assert.match(d.text, /A question is still open in this session/);
  assert.deepEqual(L.calls, [], 'ctrl+d does not answer the question');
  await cancelDialog(L);
  clickTabClose(L, 'a'); await L.settle();
  assert.equal(dialog(L).html, d.html);
});

test('the only session: the confirmation says it cannot be closed, as the tab\'s × says it', async () => {
  const L = await boot();
  L.focus('#input'); L.press(KEY); await L.settle();
  const d = dialog(L);
  assert.match(d.text, /This is the last session/);
  await cancelDialog(L);
  clickTabClose(L, 'a'); await L.settle();
  assert.equal(dialog(L).html, d.html);
});

test('in a replay it asks what the × asks there', async () => {
  const L = await boot({ two: true });
  await L.step(1); L.SL.sessions.active.seek(L.SL.sessions.active.vt, false); await L.settle();
  assert.ok(L.SL.sessions.active.replay, 'the view is a replay');
  L.doc.body.focus();
  const e = L.press(KEY); await L.settle();
  assert.equal(e.defaultPrevented, true);
  const d = dialog(L);
  assert.equal(d.title, 'Close the session');
  await cancelDialog(L);
  clickTabClose(L, 'a'); await L.settle();
  assert.equal(dialog(L).html, d.html);
});

test('a recorded session (read only, with its message box disabled) asks what the × asks there', async () => {
  const L = await boot({ two: true });
  const S = L.SL.sessions.active; S.recorded = true; S.readOnly = true; // as the page makes the tab of a recorded session
  L.doc.body.focus();
  const e = L.press(KEY); await L.settle();
  assert.equal(e.defaultPrevented, true);
  const d = dialog(L);
  assert.equal(d.title, 'Close the session');
  await cancelDialog(L);
  clickTabClose(L, 'a'); await L.settle();
  assert.equal(dialog(L).html, d.html);
});

test('text in the message box: ctrl+d does nothing and is not claimed (the browser\'s text box keeps it)', async () => {
  const L = await boot({ two: true });
  await L.type('hello world'); L.$('#input').setSelectionRange(5, 5);
  const e = L.press(KEY); await L.settle();
  assert.equal(e.defaultPrevented, false, 'not claimed');
  assert.equal(dialog(L), null, 'no dialog');
  assert.equal(L.$('#input').value, 'hello world', 'the text is as it was');
  assert.deepEqual(L.calls, []);
});

test('the focus on anything else: not the page\'s key', async () => {
  for (const sel of ['.stab', '.nvi', '#modeBtn']) {
    const L = await boot({ two: true });
    L.focus(sel);
    const e = L.press(KEY); await L.settle();
    assert.equal(e.defaultPrevented, false, sel + ': not claimed');
    assert.equal(dialog(L), null, sel + ': no dialog');
  }
});

test('only ctrl+d: another modifier, or the letter alone, asks nothing', async () => {
  for (const key of ['d', 'shift+d', 'alt+d', 'ctrl+shift+d', 'ctrl+alt+d']) {
    const L = await boot({ two: true });
    L.doc.body.focus();
    const e = L.press(key); await L.settle();
    assert.equal(dialog(L), null, key + ' opened a dialog');
    assert.equal(e.defaultPrevented, false, key + ' was claimed');
  }
});

test('a dialog that is open keeps its place: a second ctrl+d opens no other', async () => {
  const L = await boot({ two: true });
  L.focus('#input'); L.press(KEY); await L.settle();
  const first = dialog(L).rec;
  // the dialog has the focus now (on its cancel button): the key is claimed, so that the browser does not bookmark the page, and opens nothing
  assert.notEqual(L.doc.activeElement, L.$('#input'));
  const again = L.press(KEY); await L.settle();
  assert.equal(again.defaultPrevented, true, 'the browser must not bookmark behind the dialog');
  assert.equal(L.SL.ui.modalState.cur, first, 'the same dialog');
  // with the focus lost to the page it is the same
  L.doc.body.focus();
  const third = L.press(KEY); await L.settle();
  assert.equal(third.defaultPrevented, true);
  assert.equal(L.SL.ui.modalState.cur, first, 'still the same dialog');
  assert.deepEqual(L.calls, []);
  // a field of a dialog keeps the key for itself
  L.SL.ui.dialogs.rename(L.SL.sessions.active); await L.settle();
  const field = L.$('#overlayHost input'); assert.ok(field, 'the rename dialog has a text field'); field.focus();
  const inField = L.press(KEY); await L.settle();
  assert.equal(inField.defaultPrevented, false, 'the key in a field of a dialog is the browser\'s');
  assert.equal(L.SL.ui.modalState.cur.el.querySelector('h2').textContent, 'Rename the session', 'and the dialog is the rename one');
});

test('no session: nothing to close, and the key is the browser\'s', async () => {
  const L = await boot();
  L.SL.sessions.activate(null);
  assert.ok(L.SL.sessions.active.placeholder);
  L.doc.body.focus();
  const e = L.press(KEY); await L.settle();
  assert.equal(e.defaultPrevented, false);
  assert.equal(dialog(L), null);
});

test('a key that an input method is composing with never triggers it', async () => {
  const L = await boot({ two: true });
  L.focus('#input');
  const composing = { bubbles: true, key: 'd', ctrlKey: true, altKey: false, shiftKey: false, metaKey: false, isComposing: true, keyCode: 229 };
  const e = new FakeEvent('keydown', composing);
  L.doc.activeElement.dispatchEvent(e); await L.settle();
  assert.equal(L.SL.ui.hasModal(), false, 'no dialog');
  assert.equal(e.defaultPrevented, false, 'not claimed');
});
