/* 85-ui-approvals.js: SL.ui.approvals, the approval question and the cross-session inbox.
 *
 * The question box names the agent, the operation and its scope, shows the command and why it asks, and offers the three real choices.
 * Security rule (product requirement): an answer is accepted only after the keyboard has been QUIET for ~0.8 s since the question was shown
 * or since the last key; until then the buttons are inert and the meter fills. Text typed ahead goes to the prompt and never answers.
 * The same rule governs the inbox, which can answer a question of a background session. Several questions are asked one at a time.
 * The server keeps its own floor (350 ms, `409 too_soon`): the meter re-arms when it refuses. The header says what the question is
 * about (D-09); a question that offers it gets the TUI's fourth answer, builds and tests for the session (PARITY A2: on screen 3,
 * on the wire choice 4; No is 4 and Esc); an edit, a write or a patch shows the change it asks to make (PARITY A1). */
(function (SL) {
  'use strict';
  const U = SL.u, { $, $$, esc, agCol } = U, calc = SL.calc, ui = SL.ui = SL.ui || {};
  const QUIET = 0.8, shown = {};          // wall ms at which a question's answer UI first appeared

  const TESTS = 'Yes, and allow builds and tests (go, npm, cargo, pytest, make…) for this session';
  const remembers = q => q.kind === 'trust' || q.kind === 'mcp';
  /** The words of a choice (the wire's number): 1 yes, 2 yes and remember, 3 no, 4 the tests preset. */
  const choiceText = (q, c) => ({ 1: 'Yes', 2: remembers(q) ? 'Yes, and remember until they change' : 'Yes, and don\'t ask again for ' + (q.what || 'this command') + ' this session', 3: 'No, and tell Sleipnir what to do instead', 4: TESTS })[c] || '';
  /** The key a choice has on screen: with the fourth answer, the tests preset is 3 and No is 4. */
  const keyOf = (q, c) => q.offersTests ? ({ 1: 1, 2: 2, 4: 3, 3: 4 })[c] : c;
  const choiceOfKey = (q, k) => q.offersTests ? ({ 1: 1, 2: 2, 3: 4, 4: 3 })[k] : k;
  /** What the agent wants, by the question's kind (D-09; PARITY A1 names writes, patches and web searches). */
  function wants(q) {
    switch (q.kind) {
      case 'edit': return q.tool === 'write' ? 'wants to write a file' : q.tool === 'apply_patch' ? 'wants to apply a patch' : 'wants to edit a file';
      case 'web': return q.tool === 'web_search' ? 'wants to search the web' : 'wants to fetch a web page';
      case 'trust': return 'wants to use this project\'s files';
      case 'mcp': return 'wants to start a tool server';
      case 'read': return 'wants to read a file';
      case 'other': return 'wants to use a tool';
      default: return 'wants to run a command';
    }
  }
  /** The unified text of a change as diff lines (the Workspace's diff body, without the attribution gutter). */
  function diffLines(text) {
    let o = 0, n = 0; return String(text || '').split('\n').map(l => {
      if (l.indexOf('@@') === 0) { const mm = /-(\d+)(?:,\d+)? \+(\d+)/.exec(l); if (mm) { o = +mm[1]; n = +mm[2]; } return '<div class="ln hunk"><i></i><s></s><span>' + esc(l) + '</span></div>'; }
      if (/^(\+\+\+|---) /.test(l)) return '<div class="ln hunk"><i></i><s></s><span>' + esc(l) + '</span></div>';
      if (l.charAt(0) === '+') return '<div class="ln add"><i>' + (n++) + '</i><s>+</s><span>' + esc(l.slice(1)) + '</span></div>';
      if (l.charAt(0) === '-') return '<div class="ln del"><i>' + (o++) + '</i><s>−</s><span>' + esc(l.slice(1)) + '</span></div>';
      o++; return '<div class="ln ctx"><i>' + (n++) + '</i><s></s><span>' + esc(l.charAt(0) === ' ' ? l.slice(1) : l) + '</span></div>';
    }).join('');
  }
  const counts = text => { let a = 0, d = 0; String(text || '').split('\n').forEach(l => { if (/^\+(?!\+\+ )/.test(l)) a++; else if (/^-(?!-- )/.test(l)) d++; }); return [a, d]; };
  /** The change a question asks to make: a head with `Open it whole` and the diff, at most 40% of the rail's height. */
  function changeHtml(q) {
    if (!q.change) return ''; const [a, d] = counts(q.change);
    return '<div class="qscope"><b>change</b>the change it asks to make' + (q.path ? ' · <span class="mono">' + esc(q.path) + '</span>' : '') + ' · <span class="ok">+' + a + '</span> <span class="err">−' + d + '</span> <button class="btn sm" type="button" data-whole="' + esc(q.id) + '">Open it whole</button></div><div class="diff qdiff" style="max-height:min(40vh,320px);margin:0 0 6px">' + diffLines(q.change) + '</div>';
  }
  function openWhole(q) { ui.modal({ title: 'The change', kicker: q.agent + (q.task ? ' · ' + q.task : ''), desc: (q.path || '') + ' · as the agent asks to make it', wide: true, color: 'var(--warm)', body: '<div class="diff">' + diffLines(q.change) + '</div>' }); }
  const roTitle = S => S && S.readOnly ? (S.follow ? 'watching ' + S.sid + ' · read-only · the run belongs to another process' : 'a recorded session is read-only') : '';
  const opts = (q, pre, S) => { const ro = roTitle(S), dis = ro ? ' disabled title="' + esc(ro) + '"' : '', four = !!q.offersTests;
    return '<div class="qopts" data-q="' + esc(q.id) + '">' +
    '<button class="qopt" type="button" data-choice="1" aria-disabled="true"' + dis + '><kbd>1</kbd><span>Yes</span></button>' +
    '<button class="qopt" type="button" data-choice="2" aria-disabled="true"' + dis + '><kbd>2</kbd><span>' + esc(choiceText(q, 2)) + '</span></button>' +
    (four ? '<button class="qopt" type="button" data-choice="4" aria-disabled="true"' + dis + '><kbd>3</kbd><span>' + esc(TESTS) + '</span></button>' : '') +
    '<button class="qopt no" type="button" data-choice="3" aria-disabled="true"' + dis + '><kbd>' + (four ? 4 : 3) + '</kbd><span>No, and tell Sleipnir what to do instead <small>(esc)</small></span></button></div>' +
    '<div class="qmeter" data-m="' + esc(q.id) + '"' + (four ? ' data-four="1"' : '') + '><span class="bar"><i></i></span><span class="qmt">your typing goes to the prompt until you pause</span></div>'; };

  /** Is the answer UI of question qid armed (keyboard quiet for QUIET s since it appeared)? Returns {armed, frac}. */
  function arm(qid) {
    const T = SL.time.T, t0 = Math.max(shown[qid] == null ? T.wall : shown[qid], T.quietSince), frac = Math.min(1, (T.wall - t0) / 1000 / QUIET);
    return { armed: frac >= 1, frac };
  }
  function paintMeters(scopeEl) {
    $$('.qmeter', scopeEl).forEach(mt => {
      const qid = mt.dataset.m; if (shown[qid] == null) shown[qid] = SL.time.T.wall; const a = arm(qid), S = SL.sessions.active, rep = S && S.replay;
      const ok = a.armed && !rep; mt.classList.toggle('ready', ok); $('.bar i', mt).style.width = (a.frac * 100) + '%';
      const t = rep ? 'replay: go live to answer' : ok ? (mt.dataset.four ? 'ready: press 1, 2, 3 or 4 (esc is 4)' : 'ready: press 1, 2 or 3 (esc is 3)') : 'your typing goes to the prompt until you pause'; const el = $('.qmt', mt); if (el.textContent !== t) el.textContent = t;
      const box = mt.previousElementSibling; if (box && box.classList.contains('qopts')) $$('.qopt', box).forEach(b => b.setAttribute('aria-disabled', ok ? 'false' : 'true'));
    });
  }
  /** Answer if armed; otherwise explain. choice 3 asks for the instruction first (in the strip or the inbox row). */
  function answer(S, q, choice, boxEl) {
    const rep = S === SL.sessions.active && S.replay; if (rep) { ui.toast('go live to answer: a replay never changes the session', 'warm'); return false; }
    const a = arm(q.id); if (!a.armed) { ui.toast('the buttons wake up when the keyboard has been quiet for a moment', 'warm'); return false; }
    if (S.readOnly) { ui.toast(roTitle(S), 'warm'); return false; }
    if (choice === 3 && boxEl) { tellInstead(S, q, boxEl); return true; }
    const r = SL.act.answerQuestion(q.id, choice, undefined, S.id); if (r && r.ok === false) { if (r.why) ui.toast(r.why, 'warm'); return false; } delete shown[q.id];
    ui.toast(choice === 3 ? 'told ' + q.agent + ' what to do instead' : 'answered ' + keyOf(q, choice) + ': ' + q.agent + (q.kind && q.kind !== 'command' ? ' goes ahead' : ' runs the command'), 'ok'); return true;
  }
  function tellInstead(S, q, boxEl) {
    boxEl.innerHTML = '<div class="field" style="margin:0"><input class="tellIn" type="text" placeholder="tell Sleipnir what to do instead" aria-label="Tell Sleipnir what to do instead"></div><div class="row2"><button class="btn pri tellSend" type="button">Send to ' + esc(q.agent) + ' <kbd>⏎</kbd></button><button class="btn tellBack" type="button">Back <kbd>esc</kbd></button></div>';
    const inp = $('.tellIn', boxEl); inp.focus();
    const send = () => { SL.act.answerQuestion(q.id, 3, inp.value.trim() || 'don’t do that: find another way', S.id); delete shown[q.id]; ui.toast('told ' + q.agent + ' what to do instead', 'ok'); };
    $('.tellSend', boxEl).addEventListener('click', send);
    inp.addEventListener('keydown', e => { SL.time.noteKey(); if (e.key === 'Enter') { e.preventDefault(); send(); } else if (e.key === 'Escape') { e.stopPropagation(); back(); } });
    const back = () => { shown[q.id] = SL.time.T.wall; if (boxEl._back) boxEl._back(); delete boxEl.dataset.tell; };
    $('.tellBack', boxEl).addEventListener('click', back); boxEl.dataset.tell = '1';
  }

  /* ---------- the strip in the rail (active session) ---------- */
  function render(S, m) {
    const slot = $('#qSlot'), q = m ? calc.openQuestion(m) : null, done = m ? m.qs.filter(x => x.answered).slice(-1)[0] : null;
    if (!q) { const key = done ? 'd' + done.id + done.answered : ''; if (slot._k !== key) { slot._k = key; slot.innerHTML = done && SL.time.T.wall - (shown['_d' + done.id] || (shown['_d' + done.id] = SL.time.T.wall)) < 8000 ? '<div class="qdone"><b>' + (done.by && done.by !== 'you' ? '⊘' : '✓') + '</b><span><b style="color:var(--fg)">' + (done.by && done.by !== 'you' ? esc(BY[done.by] || 'refused') : keyOf(done, done.answered) + ' ' + esc(choiceText(done, done.answered))) + '</b><br><span class="dim">' + esc(done.agent) + (done.task ? ' · ' + esc(done.task) : '') + ' · <span class="mono">' + esc(done.cmd) + '</span></span></span></div>' : ''; } return; }
    const key = 'q' + q.id + '|' + calc.waiting(m) + '|' + S.id; if (slot._k === key && $('.qstrip', slot)) return; slot._k = key;
    const A = m.ag[q.agent], waiting = calc.waiting(m) - 1;
    slot.innerHTML = '<section class="qstrip" role="group" aria-label="Approval question from ' + esc(q.agent) + '" aria-describedby="qWhy"><div class="qh"><div><span class="qk">' + esc(q.agent) + ' · ' + esc(A ? A.role : '') + (q.task ? ' · ' + esc(q.task) : '') + '</span><b>' + wants(q) + '</b></div><span class="qw">waiting for you: 0s</span></div>' + (waiting > 0 ? '<div class="qmore">+' + waiting + ' waiting: asked one at a time</div>' : '') +
      cmdHtml(q) + changeHtml(q) + (q.scope ? '<div class="qscope"><b>scope</b>' + esc(q.scope) + '</div>' : '') + '<div class="qwhy" id="qWhy"><b>why it asks</b>' + esc(q.why) + '</div><div class="qbox">' + opts(q, null, S) + '</div></section>';
    const strip = $('.qstrip', slot), box = $('.qbox', strip), render2 = () => { box.innerHTML = opts(q, null, S); wire(); }; box._back = render2;
    const wb = $('[data-whole]', strip); if (wb) wb.addEventListener('click', () => openWhole(q));
    function wire() { $$('.qopt', box).forEach(b => b.addEventListener('click', () => answer(S, q, +b.dataset.choice, box))); }
    wire(); shown[q.id] = shown[q.id] == null ? SL.time.T.wall : shown[q.id];
  }
  /** Who closed a question when it was not the person (VOCAB 5.17 `by`). */
  const BY = { timeout: 'refused: nobody answered in time', canceled: 'refused: the turn was interrupted', closed: 'refused: the session closed', nobody: 'refused: no page was open to answer' };
  /** The command line: every line of a multi-line command shows (PARITY A1); a one-line command keeps the strip's own layout. */
  const cmdHtml = q => '<div class="qcmd"' + (/\n/.test(String(q.cmd || '')) ? ' style="white-space:pre-wrap"' : '') + '><i>' + esc(q.cwd || '.') + ' $</i> ' + esc(q.cmd) + '</div>';
  ui.approvals = { render, arm, answer, paintMeters, shown, wants, choiceText, keyOf, choiceOfKey, diffLines };
  /** Keys 1/2/3 and Esc answer the front question of the active session: only when armed. Returns true when consumed. */
  ui.approvals.tryKey = function (key) {
    const S = SL.sessions.active, q = S && S.m ? calc.openQuestion(S.m) : null; if (!q) return false;
    const a = arm(q.id); if (!a.armed || S.replay) return false; const slot = $('#qSlot'), box = $('.qbox', slot); if (box && box.dataset.tell) return false;
    if (key === '1' || key === '2' || key === '3' || (key === '4' && q.offersTests)) { answer(S, q, choiceOfKey(q, +key), box); return true; } if (key === 'Escape') { answer(S, q, 3, box); return true; } return false;
  };
  ui.approvals.pending = () => { const S = SL.sessions.active; return S && S.m ? calc.openQuestion(S.m) : null; };

  /* ---------- the inbox: open questions of every session ---------- */
  function mount(sc) {
    const app = $('#app'); let pop = null;
    function close() { if (pop) { pop.remove(); pop = null; } const b = $('#sInbox'); if (b) b.setAttribute('aria-expanded', 'false'); }
    function html() {
      const nd = SL.sessions.needs();
      return '<div class="ibx-h"><b>Needs you</b><span class="dim">' + nd.length + ' open question' + (nd.length === 1 ? '' : 's') + ' · answering follows the same quiet-period rule</span><button class="btn sm" type="button" data-close>esc</button></div>' +
        (nd.length ? nd.map(({ S, q, waiting }) => '<div class="ibx" data-sid="' + esc(S.id) + '" data-qid="' + esc(q.id) + '"><div class="ibx-t"><b style="color:var(--c-mgr)">' + esc(S.name) + '</b> <span class="dim">› ' + esc(q.agent) + (q.task ? ' · ' + q.task : '') + '</span>' + (waiting > 1 ? '<span class="tag warm">+' + (waiting - 1) + ' waiting</span>' : '') + '<button class="btn sm" type="button" data-open>Open session</button></div>' + cmdHtml(q) + changeHtml(q) + (q.scope ? '<div class="qscope"><b>scope</b>' + esc(q.scope) + '</div>' : '') + '<div class="qwhy"><b>why it asks</b>' + esc(q.why) + '</div><div class="qbox">' + opts(q, null, S) + '</div></div>').join('') : '<p class="stubnote" style="padding:14px 12px;margin:0">Nothing waits for you. A question in any session, background ones included, shows up here with a badge on its tab.</p>') +
        '<p class="ibx-f">Headless runs never ask: an action that needs approval is refused, with --ask-timeout shown.</p>';
    }
    function draw() {
      if (!pop) return; pop.innerHTML = html();
      $$('.ibx', pop).forEach(row => { const S = SL.sessions.get(row.dataset.sid), q = S && S.wm.qs.find(x => x.id === row.dataset.qid && !x.answered); if (!q) return; const box = $('.qbox', row);
        const rr = () => { box.innerHTML = opts(q, null, S); w(); }; box._back = rr; const wb = $('[data-whole]', row); if (wb) wb.addEventListener('click', () => openWhole(q)); const w = () => $$('.qopt', box).forEach(b => b.addEventListener('click', () => { const ok = answer(S, q, +b.dataset.choice, box); if (ok && +b.dataset.choice !== 3) sc.timeout(draw, 60); })); w(); shown[q.id] = SL.time.T.wall;
        $('[data-open]', row).addEventListener('click', () => { close(); SL.act.switchSession(S.id); }); });
      $('[data-close]', pop).addEventListener('click', close);
    }
    /** Open or close the inbox popover under `btn` (default: the `Needs you` button of the session strip). */
    function toggle(btn) {
      if (pop) { close(); return; }
      btn = btn || $('#sInbox'); if (!btn) return;
      pop = mk_('div', 'popover ibxpop'); pop.setAttribute('role', 'dialog'); pop.setAttribute('aria-label', 'Needs you: open questions'); app.appendChild(pop); btn.setAttribute('aria-expanded', 'true'); draw();
      const r = btn.getBoundingClientRect(), ar = app.getBoundingClientRect(); pop.style.top = (r.bottom - ar.top + 4) + 'px'; pop.style.right = Math.max(8, ar.right - r.right) + 'px';
    }
    const mk_ = (t, c) => { const e = document.createElement(t); e.className = c; return e; };
    sc.on('needs-changed', () => { if (pop) { const nd = SL.sessions.needs().length; draw(); if (!nd) { /* keep open: shows the empty state */ } } });
    sc.listen(document, 'pointerdown', e => { if (pop && !pop.contains(e.target) && !e.target.closest('#sInbox')) close(); });
    /* key 4: the fourth answer's No (94-keys.js routes 1, 2 and 3); the same rule as the other keys */
    sc.listen(document, 'keydown', e => { if (e.key !== '4' || e.ctrlKey || e.metaKey || e.altKey || ui.hasModal()) return; const t = e.target, inField = t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT' || t.isContentEditable); if (inField && !(t.id === 'input' && t.value === '')) return; const q = ui.approvals.pending(); if (q && q.offersTests && ui.approvals.tryKey('4')) { e.preventDefault(); e.stopPropagation(); } }, true);
    sc.frame(() => { if (pop) paintMeters(pop); paintMeters($('#qSlot')); const q = ui.approvals.pending(); const w = $('#qSlot .qw'); if (q && w) { const S = SL.sessions.active, s = Math.max(0, Math.floor(S.vt - q.t0)) + 1, t = 'waiting for you: ' + (s < 60 ? s + 's' : Math.floor(s / 60) + 'm ' + (s % 60) + 's'); if (w.textContent !== t) w.textContent = t; } });
    ui.inbox = { toggle, close, isOpen: () => !!pop };
  }
  ui.approvals.mount = mount;
})(SL);
