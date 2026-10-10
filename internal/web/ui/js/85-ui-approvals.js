/* 85-ui-approvals.js: SL.ui.approvals, the approval question and the cross-session inbox.
 *
 * The question box names the agent, the operation and its scope, shows the command and why it asks, and offers the three real choices.
 * Security rule (product requirement): an answer is accepted only after the keyboard has been QUIET for ~0.8 s since the question was shown
 * or since the last key; until then the buttons are inert and the meter fills. Text typed ahead goes to the prompt and never answers.
 * The same rule governs the inbox, which can answer a question of a background session. Several questions are asked one at a time. */
(function (SL) {
  'use strict';
  const U = SL.u, { $, $$, esc, agCol } = U, calc = SL.calc, ui = SL.ui = SL.ui || {};
  const QUIET = 0.8, shown = {};          // wall ms at which a question's answer UI first appeared

  const opts = (q, pre) => '<div class="qopts" data-q="' + esc(q.id) + '">' +
    '<button class="qopt" type="button" data-choice="1" aria-disabled="true"><kbd>1</kbd><span>Yes</span></button>' +
    '<button class="qopt" type="button" data-choice="2" aria-disabled="true"><kbd>2</kbd><span>Yes, and don\'t ask again for ' + esc(q.what || 'this command') + ' this session</span></button>' +
    '<button class="qopt no" type="button" data-choice="3" aria-disabled="true"><kbd>3</kbd><span>No, and tell Sleipnir what to do instead <small>(esc)</small></span></button></div>' +
    '<div class="qmeter" data-m="' + esc(q.id) + '"><span class="bar"><i></i></span><span class="qmt">your typing goes to the prompt until you pause</span></div>';

  /** Is the answer UI of question qid armed (keyboard quiet for QUIET s since it appeared)? Returns {armed, frac}. */
  function arm(qid) {
    const T = SL.time.T, t0 = Math.max(shown[qid] == null ? T.wall : shown[qid], T.quietSince), frac = Math.min(1, (T.wall - t0) / 1000 / QUIET);
    return { armed: frac >= 1, frac };
  }
  function paintMeters(scopeEl) {
    $$('.qmeter', scopeEl).forEach(mt => {
      const qid = mt.dataset.m; if (shown[qid] == null) shown[qid] = SL.time.T.wall; const a = arm(qid), S = SL.sessions.active, rep = S && S.replay;
      const ok = a.armed && !rep; mt.classList.toggle('ready', ok); $('.bar i', mt).style.width = (a.frac * 100) + '%';
      const t = rep ? 'replay: go live to answer' : ok ? 'ready: press 1, 2 or 3 (esc is 3)' : 'your typing goes to the prompt until you pause'; const el = $('.qmt', mt); if (el.textContent !== t) el.textContent = t;
      const box = mt.previousElementSibling; if (box && box.classList.contains('qopts')) $$('.qopt', box).forEach(b => b.setAttribute('aria-disabled', ok ? 'false' : 'true'));
    });
  }
  /** Answer if armed; otherwise explain. choice 3 asks for the instruction first (in the strip or the inbox row). */
  function answer(S, q, choice, boxEl) {
    const rep = S === SL.sessions.active && S.replay; if (rep) { ui.toast('go live to answer: a replay never changes the session', 'warm'); return false; }
    const a = arm(q.id); if (!a.armed) { ui.toast('the buttons wake up when the keyboard has been quiet for a moment', 'warm'); return false; }
    if (choice === 3 && boxEl) { tellInstead(S, q, boxEl); return true; }
    SL.act.answerQuestion(q.id, choice, undefined, S.id); delete shown[q.id];
    ui.toast(choice === 3 ? 'told ' + q.agent + ' what to do instead' : 'answered ' + choice + ': ' + q.agent + ' runs the command', 'ok'); return true;
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
    if (!q) { const key = done ? 'd' + done.id + done.answered : ''; if (slot._k !== key) { slot._k = key; slot.innerHTML = done && SL.time.T.wall - (shown['_d' + done.id] || (shown['_d' + done.id] = SL.time.T.wall)) < 8000 ? '<div class="qdone"><b>✓</b><span><b style="color:var(--fg)">' + done.answered + ' ' + esc({ 1: 'Yes', 2: 'Yes, and don\'t ask again for ' + (done.what || 'this command') + ' this session', 3: 'No, and tell Sleipnir what to do instead' }[done.answered]) + '</b><br><span class="dim">' + esc(done.agent) + (done.task ? ' · ' + done.task : '') + ' · <span class="mono">' + esc(done.cmd) + '</span></span></span></div>' : ''; } return; }
    const key = 'q' + q.id + '|' + calc.waiting(m) + '|' + S.id; if (slot._k === key && $('.qstrip', slot)) return; slot._k = key;
    const A = m.ag[q.agent], waiting = calc.waiting(m) - 1;
    slot.innerHTML = '<section class="qstrip" role="group" aria-label="Approval question from ' + esc(q.agent) + '" aria-describedby="qWhy"><div class="qh"><div><span class="qk">' + esc(q.agent) + ' · ' + esc(A ? A.role : '') + (q.task ? ' · ' + esc(q.task) : '') + '</span><b>wants to run a command</b></div><span class="qw">waiting for you: 0s</span></div>' + (waiting > 0 ? '<div class="qmore">+' + waiting + ' waiting: asked one at a time</div>' : '') +
      '<div class="qcmd"><i>' + esc(q.cwd || '.') + ' $</i> ' + esc(q.cmd) + '</div>' + (q.scope ? '<div class="qscope"><b>scope</b>' + esc(q.scope) + '</div>' : '') + '<div class="qwhy" id="qWhy"><b>why it asks</b>' + esc(q.why) + '</div><div class="qbox">' + opts(q) + '</div></section>';
    const strip = $('.qstrip', slot), box = $('.qbox', strip), render2 = () => { box.innerHTML = opts(q); wire(); }; box._back = render2;
    function wire() { $$('.qopt', box).forEach(b => b.addEventListener('click', () => answer(S, q, +b.dataset.choice, box))); }
    wire(); shown[q.id] = shown[q.id] == null ? SL.time.T.wall : shown[q.id];
  }
  ui.approvals = { render, arm, answer, paintMeters, shown };
  /** Keys 1/2/3 and Esc answer the front question of the active session: only when armed. Returns true when consumed. */
  ui.approvals.tryKey = function (key) {
    const S = SL.sessions.active, q = S && S.m ? calc.openQuestion(S.m) : null; if (!q) return false;
    const a = arm(q.id); if (!a.armed || S.replay) return false; const slot = $('#qSlot'), box = $('.qbox', slot); if (box && box.dataset.tell) return false;
    if (key === '1' || key === '2' || key === '3') { answer(S, q, +key, box); return true; } if (key === 'Escape') { answer(S, q, 3, box); return true; } return false;
  };
  ui.approvals.pending = () => { const S = SL.sessions.active; return S && S.m ? calc.openQuestion(S.m) : null; };

  /* ---------- the inbox: open questions of every session ---------- */
  function mount(sc) {
    const app = $('#app'); let pop = null;
    function close() { if (pop) { pop.remove(); pop = null; } const b = $('#sInbox'); if (b) b.setAttribute('aria-expanded', 'false'); }
    function html() {
      const nd = SL.sessions.needs();
      return '<div class="ibx-h"><b>Needs you</b><span class="dim">' + nd.length + ' open question' + (nd.length === 1 ? '' : 's') + ' · answering follows the same quiet-period rule</span><button class="btn sm" type="button" data-close>esc</button></div>' +
        (nd.length ? nd.map(({ S, q, waiting }) => '<div class="ibx" data-sid="' + esc(S.id) + '" data-qid="' + esc(q.id) + '"><div class="ibx-t"><b style="color:var(--c-mgr)">' + esc(S.name) + '</b> <span class="dim">› ' + esc(q.agent) + (q.task ? ' · ' + q.task : '') + '</span>' + (waiting > 1 ? '<span class="tag warm">+' + (waiting - 1) + ' waiting</span>' : '') + '<button class="btn sm" type="button" data-open>Open session</button></div><div class="qcmd"><i>' + esc(q.cwd || '.') + ' $</i> ' + esc(q.cmd) + '</div>' + (q.scope ? '<div class="qscope"><b>scope</b>' + esc(q.scope) + '</div>' : '') + '<div class="qwhy"><b>why it asks</b>' + esc(q.why) + '</div><div class="qbox">' + opts(q) + '</div></div>').join('') : '<p class="stubnote" style="padding:14px 12px;margin:0">Nothing waits for you. A question in any session, background ones included, shows up here with a badge on its tab.</p>') +
        '<p class="ibx-f">Headless runs (docs-sweep) never ask: an action that needs approval is refused, with --ask-timeout shown.</p>';
    }
    function draw() {
      if (!pop) return; pop.innerHTML = html();
      $$('.ibx', pop).forEach(row => { const S = SL.sessions.get(row.dataset.sid), q = S && S.wm.qs.find(x => x.id === row.dataset.qid && !x.answered); if (!q) return; const box = $('.qbox', row);
        const rr = () => { box.innerHTML = opts(q); w(); }; box._back = rr; const w = () => $$('.qopt', box).forEach(b => b.addEventListener('click', () => { const ok = answer(S, q, +b.dataset.choice, box); if (ok && +b.dataset.choice !== 3) sc.timeout(draw, 60); })); w(); shown[q.id] = SL.time.T.wall;
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
    sc.frame(() => { if (pop) paintMeters(pop); paintMeters($('#qSlot')); const q = ui.approvals.pending(); const w = $('#qSlot .qw'); if (q && w) { const S = SL.sessions.active, s = Math.max(0, Math.floor(S.vt - q.t0)) + 1, t = 'waiting for you: ' + (s < 60 ? s + 's' : Math.floor(s / 60) + 'm ' + (s % 60) + 's'); if (w.textContent !== t) w.textContent = t; } });
    ui.inbox = { toggle, close, isOpen: () => !!pop };
  }
  ui.approvals.mount = mount;
})(SL);
