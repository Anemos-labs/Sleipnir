/* 87-ui-sheets.js: SL.ui.sheets (goal, mode, context, help, history) and SL.ui.dialogs (new session, resume, rename). Each one reads
 * the active session and changes it only through SL.act; what it shows after an action is what the server sends back.
 * The New session dialog starts from the server's defaults (its flags and configuration), offers the projects the server
 * allows (a new session never starts in a free path). It never decides that a project needs trust: the request goes first, and the
 * server's challenge (409 trust_required, for a new session and a resume alike) is the "Trust this project?" dialog (ui.trustStep), which
 * lists every file the server sent and wakes its yes only once that list is on screen; the yes repeats the request with the challenge's
 * own id. Any other confirmation the server asks for (428) is the dialog of askConfirm, with the server's reasons and the request. */
(function (SL) {
  'use strict';
  const U = SL.u, { $, $$, esc, fmtK } = U, D = SL.D, calc = SL.calc, ui = SL.ui = SL.ui || {};
  const S_ = () => SL.sessions.active;
  const sheets = ui.sheets = {}, dialogs = ui.dialogs = {};
  const chipTag = (t, c) => '<span class="tag ' + (c || '') + '">' + esc(t) + '</span>';
  const inp = (id, v, extra) => '<input id="' + id + '" type="text" value="' + esc(v == null ? '' : v) + '" ' + (extra || '') + '>';
  const seg = (name, list, cur) => '<div class="seg" role="group" aria-label="' + name + '">' + list.map(v => '<button type="button" data-seg="' + name + '" data-v="' + esc(v) + '" aria-pressed="' + (String(v) === String(cur)) + '">' + esc(v) + '</button>').join('') + '</div>';
  /* An explanation (ui.help, ui.helpAttr) is page markup: what the server sent into one is escaped by whoever puts it there. */
  /** A label's flag names kept whole: the narrow label column wraps it at its spaces, never inside "--compact-at" or between its two dashes. */
  const flagsWhole = label => label.replace(/\(?--[a-z][\w-]*\)?,?/gi, m => '<span class="nowrap">' + m + '</span>');
  /** A labelled control: hint is a line under it, help the markup of a ? dot (ui.help) beside the label. */
  const field = (label, inner, hint, help) => '<div class="fld">' + (help ? '<div class="fl"><label>' + flagsWhole(label) + '</label>' + help + '</div>' : '<label>' + flagsWhole(label) + '</label>') + '<div class="fc">' + inner + (hint ? '<small class="hint">' + hint + '</small>' : '') + '</div></div>';
  ui.fmt = { seg, field, inp, chipTag };
  /** Toast the why of a refused action; returns true when it was refused. */
  const refused = r => { if (r && r.ok === false) { if (r.why) ui.toast(r.why, 'warm'); return true; } return false; };

  /* ---------- goal ---------- */
  /** The goal as written: its lines kept (the sheet's CSS wraps them as typed) and each `code` span in the monospace face, where a
   *  flag such as --hosts reads as two hyphens rather than a dash. */
  const goalHtml = t => esc(t).replace(/`([^`\n]+)`/g, '<code>$1</code>');
  /** The Goal sheet. pending is the text of a goal just set that the session has not taken up yet (a set runs as the tab's next
   *  item): the sheet shows it, starting, rather than "No goal set". */
  /** Whether the research plugin is on: the Goal sheet then offers to research the goal first (/goal --research=DEPTH). */
  const researchOn = () => { const p = SL.plugins && SL.plugins.find('research'); return !!(p && p.enabled); };
  sheets.goal = function (pending) {
    const S = S_(), m = S.m || S.wm, known = S.meta.goalText || m.goal.objective || '', goalText = known || pending || '', gs = known || !pending ? m.goal.state : 'starting';
    /* evidence: the tasks merged, then what the judge says is left (unchecked), or its reason when the goal is met */
    const ev = m.torder.map(t => [t + ' ' + m.tasks[t].title + ' merged', m.tasks[t].st === 'merged']).concat(gs === 'met' ? (m.goal.reason ? [[m.goal.reason, true]] : []) : (m.left || []).map(x => [x, false]));
    const done = m.plan.filter(s => s === 'done').length, plan = S.plan, max = m.goal.max, used = m.goal.turns || 0;
    /* milestones: each one the judge rules on, met with its evidence or open; the goal is met only when all are */
    const ms = m.goal.milestones || [], msMet = ms.filter(x => x.met).length;
    const msHtml = ms.length ? '<h3 class="lab">milestones, ruled on one by one · ' + msMet + '/' + ms.length + ' met' + (m.goal.spec ? ' · spec <span class="mono" style="text-transform:none">' + esc(m.goal.spec) + '</span>' : '') + '</h3><ul class="plist ms">' + ms.map(x => '<li class="' + (x.met ? 'done' : '') + '"><span class="pg">' + (x.met ? '✓' : '◌') + '</span><span class="pt"><b>' + esc(x.id) + '</b> ' + esc(x.text) + (x.met && x.evidence ? '<small class="dim" style="display:block">evidence: ' + esc(x.evidence) + '</small>' : '') + '</span></li>').join('') + '</ul>' : '';
    ui.modal({ title: 'Goal', kicker: '/goal', desc: 'work until it is met, judged on evidence', wide: true, body:
      '<p class="goal-big">' + (goalText ? goalHtml(goalText) : '<span class="dim">No goal set. <kbd>/goal TEXT</kbd> sets one: the manager works until the judge finds the evidence.</span>') + '</p>' +
      (goalText ? '<div class="g2"><div><h3 class="lab">plan, from the planner</h3><ul class="plist">' + plan.map((p, i) => '<li class="' + m.plan[i] + '"><span class="pg">' + ({ done: '✓', verify: '▸', act: '✎', edit: '✎', ask: '?', queued: '◌', pending: '◌' }[m.plan[i]] || '◌') + '</span><span class="pt">' + (i + 1) + '. ' + esc(p) + '</span></li>').join('') + '</ul></div><div><h3 class="lab">the judge, on evidence</h3><div class="verdict ' + (gs === 'met' ? 'met' : '') + ' boxed"><b>verdict</b>' + esc((m.verdict || 'no verdict yet: the judge rules on the evidence when the team says the goal is met').replace(/`/g, '')) + '</div><ul class="plist">' + ev.map(([t, ok]) => '<li class="' + (ok ? 'done' : '') + '"><span class="pg">' + (ok ? '✓' : '◌') + '</span><span class="pt">' + esc(t) + '</span></li>').join('') + '</ul></div></div>' + msHtml +
        '<p class="stubnote">State <b class="st-' + gs + '">' + gs + '</b> · ' + done + '/' + plan.length + ' steps · ' + (ms.length ? msMet + '/' + ms.length + ' milestones · ' : '') + m.merged.length + '/' + m.torder.length + ' tasks merged · judged on evidence at every turn boundary' + (max ? ' · continuation limit ' + max + ' turns, used ' + used : '') + (m.goal.paused && gs === 'paused' ? ' · paused: ' + esc(m.goal.paused) : '') + '</p>' +
        '<div class="row2"><button class="btn" type="button" data-g="pause"' + (gs !== 'active' ? ' disabled' : '') + '>Pause the goal</button><button class="btn" type="button" data-g="resume"' + (gs !== 'paused' ? ' disabled' : '') + '>Resume</button><button class="btn danger" type="button" data-g="clear">Clear the goal</button></div>' : '<div class="field-row"><input id="goalIn" type="text" placeholder="e.g. add pagination to /orders; go test ./... must pass" aria-label="Goal text">' + (researchOn() ? '<select id="goalRs" aria-label="Research first" title="--research: a team researches the goal on the web first (mature projects, ideas, code snippets) and its findings go into every agent\u2019s shared prompt"><option value="">No research first</option><option value="quick">Research first: quick</option><option value="standard">Research first: standard</option><option value="deep">Research first: deep</option></select>' : '') + '<button class="btn pri" type="button" data-g="set">Set the goal</button></div>'),
      onMount(b, sc, close) { sc.listen(b, 'click', e => { const x = e.target.closest('[data-g]'); if (!x) return; const k = x.dataset.g; let r; if (k === 'pause') r = SL.act.pauseGoal(); else if (k === 'resume') r = SL.act.resumeGoal(); else if (k === 'clear') r = SL.act.clearGoal(); else if (k === 'set') { const rs = $('#goalRs', b), v = $('#goalIn', b).value; r = SL.act.setGoal(rs && rs.value && v.trim() ? '--research=' + rs.value + ' ' + v : v); } refused(r); close(); }); } });
  };

  /* ---------- mode ---------- */
  sheets.mode = function (preset) {
    const S = S_();
    ui.modal({ title: 'Mode', kicker: '/mode', desc: 'default | accept-edits | plan | bypass | yolo', color: 'var(--warm)', body:
      '<p class="stubnote"><kbd>shift</kbd> <kbd>tab</kbd> cycles <b>default → accept-edits → plan</b> and never enters bypass or yolo. Those two are set only by naming them here (or <span class="mono">/mode bypass</span>) and typing the name to confirm.</p><div class="modes">' + D.modes.map(mo => '<button class="btn mrow2' + (mo.danger ? ' dng' : '') + '" type="button" data-m="' + mo.id + '" aria-pressed="' + (S.meta.mode === mo.id) + '"><b class="mono">' + (mo.danger ? '⚠ ' : '') + mo.id + '</b><span>' + esc(mo.desc) + '</span>' + (S.meta.mode === mo.id ? chipTag('current', 'ok') : '') + '</button>').join('') + '</div>' +
      '<div id="cfBox" hidden class="dangerbox"><span id="cfTxt"></span><input id="cfIn" type="text" autocomplete="off" aria-label="Type the mode name to confirm"><div class="row2"><button class="btn danger" type="button" id="cfGo" disabled>Set it</button><button class="btn" type="button" id="cfNo">Cancel</button></div></div>',
      onMount(b, sc, close) {
        const ask = m => { $('#cfBox', b).hidden = false; $('#cfBox', b).dataset.m = m; $('#cfTxt', b).textContent = m === 'yolo' ? 'yolo asks nothing, the very dangerous included; deny rules and guarded paths still refuse. For sandboxes. Type yolo to confirm.' : 'bypass asks nothing except about the very dangerous; deny rules still apply. Type bypass to confirm.'; const i = $('#cfIn', b); i.value = ''; i.focus(); $('#cfGo', b).disabled = true; };
        $$('[data-m]', b).forEach(x => sc.listen(x, 'click', () => { const m = x.dataset.m; if (m === 'bypass' || m === 'yolo') ask(m); else { const r = SL.act.setMode(m); close(); if (!refused(r)) ui.toast('mode: ' + m); } }));
        sc.listen($('#cfIn', b), 'input', () => { $('#cfGo', b).disabled = $('#cfIn', b).value.trim() !== $('#cfBox', b).dataset.m; });
        sc.listen($('#cfGo', b), 'click', () => { const m = $('#cfBox', b).dataset.m; const r = SL.act.setMode(m, { confirm: m }); close(); if (!refused(r)) ui.toast('mode: ' + m + ' (shift+tab will not leave it unasked)', 'err'); }); sc.listen($('#cfNo', b), 'click', () => { $('#cfBox', b).hidden = true; });
        if (preset === 'bypass' || preset === 'yolo') ask(preset);
      } });
  };

  /* ---------- context: the six layers of the manager's latest prompt ---------- */
  sheets.context = function () {
    const L = D.layers, tot = L.reduce((s, l) => s + l.tok, 0), sh = L.slice(0, 3).reduce((s, l) => s + l.tok, 0), g5 = Object.keys(D.g5).map(k => D.g5[k]);
    const g5txt = g5.length ? (Math.min.apply(null, g5) === Math.max.apply(null, g5) ? fmtK(g5[0]) : fmtK(Math.min.apply(null, g5)) + '–' + fmtK(Math.max.apply(null, g5))) : '0';
    ui.modal({ title: 'Context', kicker: '/context', desc: 'what each layer of the prompt weighs', color: 'var(--be)', body: '<p class="stubnote">Six layers, stable to volatile. G0–G2 (' + fmtK(sh) + ' tokens) are one byte-prefix that every agent reads from the cache; changing any byte there is a declared, priced event.</p><div class="stack" style="margin:8px 0 0;height:30px">' + L.map((l, i) => '<span class="ls" style="width:' + (tot ? l.tok / tot * 100 : 100 / L.length) + '%;--lc:' + l.col + '"><i class="rd" style="width:100%;opacity:' + (i < 3 ? .85 : .5) + '"></i></span>').join('') + '</div><table class="tbl" style="margin-top:14px"><thead><tr><th>layer</th><th>what</th><th class="r">tokens</th><th>who reads it</th></tr></thead><tbody>' + L.map((l, i) => '<tr><td style="color:' + l.col + ';font-weight:700">' + l.id + ' ' + l.name + '</td><td>' + esc(l.note) + '</td><td class="r">' + (l.id === 'G5' ? g5txt : fmtK(l.tok)) + '</td><td>' + (i < 3 ? chipTag('shared prefix', 'ok') + ' every agent' : 'this agent only') + '</td></tr>').join('') + '</tbody></table>' });
  };

  /* ---------- help, history ---------- */
  sheets.help = function () {
    ui.modal({ title: 'Help', kicker: '/help', desc: 'this text, and your custom commands and skills', wide: true, color: 'var(--fe)', onMount(b, sc) { sc.listen(b, 'click', e => { if (e.target.closest('[data-tour]')) ui.tour.start(); }); }, body: '<div class="row2 helptour"><button class="btn sm pri" type="button" data-tour>Take the tour</button><span class="dim">each part of the page and how it works, one at a time</span></div><div class="g2"><div><h3 class="lab">keys</h3><dl class="kv keys2">' + D.shortcuts.map(([k, d]) => '<dt>' + esc(k) + '</dt><dd>' + esc(d) + '</dd>').join('') + '</dl><p class="stubnote" style="margin-top:10px">The page has no other shortcuts: what you type with nothing selected goes to the message box.</p></div><div><h3 class="lab">commands</h3>' + SL.palette.listHtml(SL.palette.filter('', true), -1).replace(/<button class="cmd"/g, '<div class="cmd"').replace(/<\/button>/g, '</div>') + '</div></div>' });
  };
  sheets.history = function () {
    const S = S_(), h = S.hist.slice().reverse();
    ui.modal({ title: 'History', kicker: 'ctrl+r', desc: 'lines you sent from this prompt', color: 'var(--fe)', body: h.length ? '<ul class="cmds">' + h.map((x, i) => '<li><button class="cmd" type="button" data-h="' + i + '"><span class="cn">' + esc(x) + '</span></button></li>').join('') + '</ul>' : '<p class="stubnote">The lines you send in this session appear here, newest first, to send again. Nothing has been sent yet.</p>', onMount(b, sc, close) { sc.listen(b, 'click', e => { const x = e.target.closest('[data-h]'); if (x) { close(); ui.setDraft(h[+x.dataset.h]); } }); } });
  };

  /* ---------- dialogs ---------- */
  dialogs.rename = function (S) {
    ui.modal({ title: 'Rename the session', kicker: 'rename', color: 'var(--mgr)', focus: '#rnIn', body: '<div class="field-row">' + inp('rnIn', S.name, 'aria-label="Session name"') + '<button class="btn pri" type="button" id="rnGo">Rename</button></div>', onMount(b, sc, close) { const go = () => { const v = $('#rnIn', b).value.trim(); if (!v) return; const r = SL.act.renameSession(S.id, v); close(); if (refused(r)) return; if (r && r.done) r.done.then(x => { if (x.ok) ui.toast('renamed to ' + v, 'ok'); }); }; sc.listen($('#rnGo', b), 'click', go); sc.listen($('#rnIn', b), 'keydown', e => { if (e.key === 'Enter') go(); }); } });
  };
  const ageTxt = s => s < 86400 ? Math.round(s / 3600) + 'h' : Math.round(s / 86400) + 'd';
  dialogs.resume = function () {
    function body() { const list = SL.sessions.recorded; return '<div class="row2" style="margin:0 0 10px"><button class="btn pri" type="button" data-r="latest">--continue: the latest session in this directory</button></div><table class="tbl"><thead><tr><th></th><th>session</th><th>first prompt</th><th class="r">agents</th><th class="r">cost</th><th>age</th></tr></thead><tbody>' + list.map(r => '<tr><td>' + (r.resumable ? '<span class="res" title="resumable">↺</span>' : '<span class="dim" title="too old to resume">·</span>') + '</td><td class="mono dim">' + esc(r.id) + '</td><td class="bright" title="' + esc(r.model ? 'model ' + r.model : '') + '">' + esc(r.first) + (r.interrupted ? ' <span class="warm">⚠ interrupted</span>' : '') + '</td><td class="r">' + (r.agents | 0) + '</td><td class="r">' + U.fmtUsd(r.cost || 0, 2) + '</td><td>' + ageTxt(r.ageS || 0) + '</td><td class="r nowrap"><button class="btn sm pri" type="button" data-r="' + esc(r.id) + '"' + (r.resumable ? '' : ' disabled') + '>↺ Resume</button> <button class="btn sm" type="button" data-rs="' + esc(r.id) + '"' + (r.resumable ? '' : ' disabled') + ' title="open New session with this session\'s model and team, then resume it">↺ with settings…</button></td></tr>').join('') + (list.length ? '' : '<tr><td colspan="7" class="dim">no recorded session yet: every session you start is recorded on disk, and appears here to continue</td></tr>') + '</tbody></table><p class="stubnote" style="margin-top:10px">Equivalent: <span class="mono">sleipnir chat --resume ID</span> or <span class="mono">--continue</span>. Sessions older than 30 days may have been pruned of their checkpoints.</p>'; }
    ui.modal({ title: 'Resume a session', kicker: '↺ /resume', desc: 'pick an earlier session from a menu, and continue it', wide: true, color: 'var(--mgr)', body: body(), onMount(b, sc, close, rec) {
      sc.on('recorded-changed', () => { rec.body.innerHTML = body(); });
      sc.listen(b, 'click', e => {
        const w = e.target.closest('[data-rs]'); if (w) { const r = SL.sessions.recorded.find(x => x.id === w.dataset.rs); if (r) { close(); dialogs.newSession({ resume: r.id, model: r.model || undefined, swarm: Math.max(0, (r.agents || 1) - 1), cwd: r.cwd || undefined }); } return; }
        const x = e.target.closest('[data-r]'); if (!x) return; const r = SL.act.resumeSession(x.dataset.r); if (refused(r)) return; close(); if (r.done) r.done.then(res => { if (res.ok) ui.toast('resumed', 'ok'); });
      });
      if (SL.data) SL.data.load('recorded'); } });
  };
  /** The permission mode of a new session: all five modes with one plain line each. bypass and yolo are drawn as dangerous and need no typed confirmation here: the warning under the list is the confirmation. */
  const modePick = cur => '<div class="nsmodes" id="nsModes" role="radiogroup" aria-label="Permission mode">' + D.modes.map(mo => '<button type="button" class="nsmode' + (mo.danger ? ' dng' : '') + '" role="radio" data-nsmode="' + mo.id + '" aria-checked="' + (mo.id === cur) + '" tabindex="' + (mo.id === cur ? 0 : -1) + '"><span class="nm">' + (mo.danger ? '<i aria-hidden="true">⚠</i> ' : '') + mo.id + '</span><span class="nd">' + esc(mo.desc) + '</span></button>').join('') + '</div><div class="nswarn" id="nsWarn" role="status" hidden></div>';
  const MODE_WARN = { bypass: 'This session will not ask before it edits files or runs commands, except for the very dangerous ones. Use it only where a mistake is cheap. Nothing to type: choosing it here is the confirmation.', yolo: 'This session will never ask, dangerous commands included. Deny rules and guarded paths still refuse. Use it only inside a sandbox. Nothing to type: choosing it here is the confirmation.' };
  function paintMode(b, mode) { $$('[data-nsmode]', b).forEach(r => { const on = r.dataset.nsmode === mode; r.setAttribute('aria-checked', String(on)); r.tabIndex = on ? 0 : -1; }); const w = $('#nsWarn', b); if (w) { w.hidden = !MODE_WARN[mode]; w.textContent = MODE_WARN[mode] ? '⚠ ' + mode + ': ' + MODE_WARN[mode] : ''; w.dataset.mode = mode; } }
  /** The seed of the New session dialog: the server's defaults (its flags over the configuration), else the page's own values. */
  /** the model a picker starts on when nothing chose one: the one the catalogue suggests (a provider's suggested_model), else the first listed */
  const firstModel = () => D.suggested && D.models.some(m => m.ref === D.suggested) ? D.suggested : D.models[0] ? D.models[0].ref : '';
  function seedOf() {
    const has = !!(SL.live && SL.live.defaults()), d = has ? SL.live.defaults() : {}, projects = (D.extra.projects || []), def = projects.find(p => p.default) || projects[0];
    const swarm = d.swarm != null ? d.swarm : 8, cwd = (def && def.dir) || d.cwd || '', tm = teamSeed(d.team);
    /* with the server's defaults an absent field is the CLI's own default (no verify command); the budget starts at off, which the server honours: a team has no cap unless one is set.
       The compaction share starts at sleipnir web's --compact-at (compactAt), else at the share a session takes without the flag (compactAtDefault: the configuration's, else 25; 0 when its token limits apply) */
    const act = SL.sessions && SL.sessions.active, actModel = act && !act.placeholder && !act.recorded ? act.meta.model : '';
    return { name: '', cwd, gitInit: true, testsSummary: d.testsPreset || '', unlimitedEnv: d.unlimitedEnv || '', model: d.model || actModel || firstModel(), mode: d.mode || 'default', swarm, isolation: d.isolation || (swarm ? 'worktree' : 'none'), verify: d.verify != null ? d.verify : has ? '' : 'go test {dirs}', commit: !!d.commit, mailman: !!d.mailman, mailmanDefault: !!d.mailman, budget: d.budget ? String(d.budget) : 'off', rules: (d.rules || []).slice(), trustProject: d.trustProject !== false, noMcp: !!d.noMcp, noMemory: !!d.noMemory, goalText: '', research: 'off', effort: d.effort || 'default', roleModels: Object.assign({}, d.roleModels || {}), maxWorkers: d.maxWorkers || 0, minWorkers: d.minWorkers || 8, counts: tm.counts, slotModels: tm.models, perSlot: tm.per, openRoles: Object.keys(tm.per).reduce((o, r) => { o[r] = true; return o; }, {}), single: swarm === 0, autoDocs: d.autoDocs !== false, managerApproves: d.managerApproves !== false, isoAuto: !d.isolationSet, resume: '', compactAt: d.compactAt ? +d.compactAt : null, compactAtBase: d.compactAtDefault != null ? +d.compactAtDefault : 25 };
  }
  /** The counts and models of a team the server names (one entry per slot), or the default team: one worker of each role. A role whose
   *  slots all run one model has it in models; a role whose slots run different models has them, slot by slot, in per. */
  function teamSeed(team) {
    const counts = {}, models = {}, per = {};
    D.roleOrder.forEach(r => { counts[r] = team && team.length ? 0 : 1; });
    (team || []).forEach(t => { counts[t.role] = (counts[t.role] || 0) + 1; (per[t.role] = per[t.role] || []).push(t.model || ''); });
    Object.keys(per).forEach(r => { if (per[r].every(m => m === per[r][0])) { if (per[r][0]) models[r] = per[r][0]; delete per[r]; } });
    return { counts, models, per };
  }
  /** The model of slot i: an explicit slot or role selection (including '' for the worker model), then the configured role default. */
  const slotModel = (st, r, i) => (st.perSlot && st.perSlot[r] && st.perSlot[r][i]) ?? (st.slotModels || {})[r] ?? (st.roleModels || {})[r] ?? '';
  dialogs.slotModel = slotModel;
  /** Set the model of slot i of role r; when every slot of the role then runs one model, it becomes the role's model again. */
  function setSlotModel(st, r, i, model) {
    const n = st.counts[r] || 0, per = []; for (let k = 0; k < n; k++) per.push(slotModel(st, r, k)); per[i] = model || '';
    st.perSlot = st.perSlot || {}; st.slotModels = st.slotModels || {};
    if (per.every(m => m === per[0])) { delete st.perSlot[r]; st.slotModels[r] = per[0]; } else st.perSlot[r] = per;
  }
  dialogs.setSlotModel = setSlotModel;
  /** The team of a New session state: its size, whether it is the default team (one of each role, no model of a slot's own), and its slots. */
  const teamOf = st => {
    const roles = Object.keys(st.counts), n = st.single ? 0 : roles.reduce((a, r) => a + (st.counts[r] || 0), 0);
    const dflt = !st.single && D.roleOrder.every(r => st.counts[r] === 1) && roles.every(r => D.roleOrder.indexOf(r) >= 0 || !st.counts[r]) && !Object.keys(st.slotModels || {}).length && !Object.keys(st.perSlot || {}).length;
    const slots = []; if (!st.single && !dflt) roles.forEach(r => { for (let i = 0; i < (st.counts[r] || 0); i++) slots.push({ role: r, model: slotModel(st, r, i) }); });
    return { n, dflt, slots, short: !st.single && n < (st.minWorkers || 8) };
  };
  dialogs.teamOf = teamOf;
  /** The models of the roles that are no worker slot (manager, mailman, judge, compactor, …) in role models rm: a team sent slot by slot
   *  still names them, or a saved team's judge and mailman would run on the manager's model. */
  const serviceModels = (st, rm) => Object.keys(rm || {}).reduce((o, r) => { if (rm[r] && !Object.prototype.hasOwnProperty.call(st.counts || {}, r)) o[r] = rm[r]; return o; }, {});
  dialogs.serviceModels = serviceModels;
  /** One argument for a POSIX shell: as it is when it holds only characters a shell leaves alone, else in single quotes (a single quote
   *  inside becomes '\''). */
  const shellQuote = a => { a = String(a == null ? '' : a); return /^[A-Za-z0-9_@%+=:,./-]+$/.test(a) ? a : "'" + a.replace(/'/g, "'\\''") + "'"; };
  ui.shellQuote = shellQuote;
  /** The --compact-at a New session state sends: its share, a whole percentage from 10 to 90, when one was chosen and it is not the
   *  share the session takes without the flag (compactAtBase: the configuration's, 0 for its token limits); else 0, nothing to send. */
  dialogs.compactAtSent = st => { const v = st.compactAt == null || st.compactAt === '' ? NaN : Math.round(Number(st.compactAt)); return v >= 10 && v <= 90 && v !== Number(st.compactAtBase) ? v : 0; };
  /** What the compaction control of a New session state says: the share, and whether it is the default (or the configuration's token
   *  limits when nothing was chosen and they apply). */
  dialogs.compactAtText = st => {
    const base = Number(st.compactAtBase), v = st.compactAt == null ? null : Math.round(Number(st.compactAt));
    if (v == null) return base > 0 ? base + '% · default' : 'token limits (default)';
    return v + '%' + (v === base ? ' · default' : base > 0 ? ' · default ' + base + '%' : ' · default: token limits');
  };
  /** The `sleipnir chat` command line of a New session state, every value quoted for a shell (Copy the command pastes it as it is); a
   *  comment above it says what the page does first when the folder is made or becomes a git repository (the command line has no flag
   *  for that), and the first goal follows as a comment, on one line. */
  dialogs.cliLine = function (st) {
    const q = shellQuote, off = (!st.budget || st.budget === 'off') && Number(st.swarm) > 0, f = [(off && st.unlimitedEnv ? st.unlimitedEnv + ' ' : '') + 'sleipnir chat'];   // a team of the terminal has a cap of its own unless the environment lifts it
    if (st.resume) f.push('--resume ' + q(st.resume)); if (st.cwd && st.cwd !== '.') f.push('--cwd ' + q(st.cwd)); f.push('--model ' + q(st.model)); f.push('--mode ' + q(st.mode)); const tm = st.counts ? teamOf(st) : { n: st.swarm, slots: [] }; f.push('--swarm ' + q(tm.n)); f.push('--isolation ' + q(st.isolation));
    if (st.verify) f.push('--verify ' + q(st.verify)); if (st.commit) f.push('--commit'); if (st.mailman) f.push('--mailman'); else if (st.mailmanDefault) f.push('--mailman=false'); if (tm.n > 0 && st.autoDocs === false) f.push('--auto-docs=false'); if (tm.n > 0 && st.managerApproves === false) f.push('--manager-approves=false');
    if (st.budget && st.budget !== 'off') f.push('--budget-usd ' + q(st.budget)); const ca = dialogs.compactAtSent(st); if (ca) f.push('--compact-at ' + q(ca)); if (tm.slots.length) { const sv = serviceModels(st, st.roleModels); Object.keys(sv).forEach(r => f.push('--role-model ' + q(r + '=' + sv[r]))); tm.slots.forEach(t => f.push('--role-model ' + q(t.model ? t.role + '=' + t.model : t.role))); } else Object.keys(st.roleModels || {}).forEach(r => f.push('--role-model ' + q(r + '=' + st.roleModels[r]))); (st.rules || []).forEach(r => f.push('--allow ' + q(r)));
    if (st.trustProject) f.push('--trust-project'); if (st.noMcp) f.push('--no-mcp'); if (st.noMemory) f.push('--no-memory');
    const first = st.makeDir || st.makeGit ? '# first, as the page does: ' + [st.makeDir ? 'make the folder' : '', st.makeGit ? 'run git init in it, with an empty first commit' : ''].filter(Boolean).join(', then ') + '\n' : '';
    const goal = dialogs.goalSent(st);
    return first + f.join(' ') + (goal ? '\n  # then: /goal ' + goal.replace(/[\r\n]+/g, ' ') : '');
  };
  /** The research depths a first goal can be researched at (/goal --research=DEPTH), each with what it runs before the build. */
  const RESEARCH_FIRST = { quick: '3 researchers in one round', standard: '6 researchers over two rounds, every page fact-checked', deep: '10 researchers over three rounds, two fact-checks per page' };
  /** The first goal a New session state sends: the goal, behind --research=DEPTH when a depth was chosen and the research plugin is on;
   *  '' without a goal (a depth alone researches nothing). */
  dialogs.goalSent = st => { const g = String(st.goalText || '').trim(); return g && RESEARCH_FIRST[st.research] && researchOn() ? '--research=' + st.research + ' ' + g : g; };

  /* ---------- the New session dialog: what each control does, in a ? beside the control (hover, focus or a tap) ---------- */
  const NS_HELP = {
    name: 'Shown on the tab. Empty: the name of the folder.',
    folder: ui.folder.helpText + ' A folder that does not exist is made.',
    git: 'Start a git repository here (<span class="mono">git init</span>, with an empty first commit): the work gets real history, and a team can give each worker its own worktree.',
    model: 'The model the manager and the workers run on. A role below can run on another.',
    workers: 'Who the manager leads. <b>Full team</b>: one worker of each role (8). Change a count to make your own team (at least 8 workers: drop docs and take two backends, say). <b>Single agent</b>: no manager and no team; one agent does everything itself.',
    autoDocs: 'The docs worker keeps README, docs/ and AGENTS.md current by itself after every accepted task, without the manager asking. Off: docs is a role the manager assigns like any other. Needs a docs slot.',
    isolation: '<b>none</b>: workers edit your folder directly.<br><b>worktree</b>: each worker edits a private copy of the project; finished work is checked, merged, and only then applied to your folder. Teams only.',
    verify: 'A command that checks a worker’s task before it counts as done: it runs when a worker hands a task in, and a failure sends the task back to the worker. With isolation <b>worktree</b> it also runs before each merge. <span class="mono">{dirs}</span> stands for the folders the task changed. Empty: no automatic check. Teams only. It runs on your machine, so you confirm it when you start.',
    commit: 'When the team’s verified work is applied, commit it to your current branch instead of leaving uncommitted edits. Needs isolation <b>worktree</b> and a clean checkout on a branch. Teams only.',
    managerApproves: 'The manager answers the workers’ routine questions itself (builds, tests and edits inside a worker’s own scope); each of its decisions shows in one collapsed line in the chat. Anything else still asks you. Teams only.',
    mailman: 'A small extra agent boils bursts of worker messages down to a digest, so the manager is not flooded. It runs on the <span class="mono">mailman</span> role model. Teams only.',
    trustProject: 'Use this project’s own instructions (AGENTS.md), skills, hooks, allow rules and tool servers. Only for code you trust: you are shown the files first.',
    noMcp: 'Do not start MCP tool servers (extra tools a project or your configuration adds).',
    noMemory: 'Start without the global memory: the notes agents saved in <span class="mono">~/.sleipnir/MEMORY.md</span> in earlier sessions are not shown to this session, and nothing it learns is saved there. Goals skip the lesson memory too (the embedder role), neither reading nor adding lessons.',
    allow: 'By default the session asks before it runs a command or edits a file. A rule here answers yes in advance, for this session only. A rule is a tool name, or a tool name with a pattern: <span class="mono">Tool(pattern)</span>. Click an example to add it. Not used in bypass and yolo, which never ask.',
    quiet: 'bypass and yolo never ask: the rules are not used here (they are kept for other modes).',
    compactAt: 'Compaction starts when an agent’s conversation fills this share of its model’s context window: higher keeps more of what it read in view, and costs more per request. It is forced halfway from there to 95%. The default is <span class="mono">cache.compact_at_percent</span> of the configuration, else 25%; where the configuration sets token limits instead, they apply until a share is chosen here.',
    budget: 'The session stops when it has spent this many US dollars. <span class="mono">off</span>: no limit, which is how it starts here (the terminal caps a team at $50 unless the configuration says otherwise).',
    roles: 'A role left on <i>worker model</i> runs on the model above.',
    goal: 'The first thing to work on. Empty: a plain chat. With a goal, the manager works until a judge finds the evidence that it is met.',
    research: 'Before anyone writes code, a team researches the first goal on the web: mature projects, ideas and code snippets, every quote checked on its page. The findings go into every agent’s shared prompt, and the build starts once the research is done. <b>quick</b>: 3 researchers, one round. <b>standard</b>: 6 researchers, two rounds, every page fact-checked. <b>deep</b>: 10 researchers, three rounds, two fact-checks per page. Needs a first goal. Later in a session: Research first in the Goal sheet.',
    mode: 'What the session may do without asking. Each mode says what it allows; bypass and yolo are for work where a mistake is cheap.',
  };
  /** Examples of allow rules, each with what it lets through; the first is the server's own preset (its summary comes with the defaults). */
  const RULE_EXAMPLES = [
    ['tests', 'the build and test commands of most projects, and a Python virtual environment in the workspace (no other install)'],
    ['Bash(go test:*)', 'the command go test, with any arguments'],
    ['Bash(npm run build)', 'exactly that command, nothing more'],
    ['Edit(docs/**)', 'editing files under docs/'],
    ['WebFetch(domain:pkg.go.dev)', 'fetching pages from one website'],
  ];
  /** Why a typed allow rule cannot be one, or '': a tool name, optionally with a pattern in parentheses (the server checks the rest). */
  const ruleProblem = v => /^[A-Za-z_][\w.:-]*(\([\s\S]*\))?$/.test(v) ? '' : '“' + v + '” is not a rule. Write a tool name, or a tool name with a pattern in parentheses, like Bash(go test:*).';
  dialogs.ruleProblem = ruleProblem;
  const FOLDER_START = /^([\\/]|[A-Za-z]:[\\/])/;
  /**
   * What the team controls of the New session dialog can be, for its state st and the folder's state (the server's DirState, null when
   * it is not known): a reason for each control that is blocked ('' when it is free), and the values that are sent and shown. No
   * combination that cannot start is sent: 0 workers is a single agent (isolation none, no verify, commit or mailman); worktree
   * isolation needs the root of a git repository with a commit, or a folder that git init makes one; --commit needs worktree isolation
   * and a clean checkout on a branch. What is not known is not blocked: the server checks again before it opens a tab. An isolation
   * that nobody chose (st.isoAuto) is worktree where the folder can have it and none elsewhere.
   */
  dialogs.nsGate = function (st, state) {
    const g = { team: '', worktree: '', commit: '', isolation: st.isolation === 'worktree' ? 'worktree' : 'none', commitOn: !!st.commit, mailman: !!st.mailman, verify: st.verify || '' };
    if (!(Number(st.swarm) > 0)) {
      g.team = g.worktree = g.commit = '0 workers: a single agent works in the folder itself, so there is no isolation, verify, commit or mailman.';
      return Object.assign(g, { isolation: 'none', commitOn: false, mailman: false, verify: '' });
    }
    const d = ui.folder.describe(state), r = state && state.repo, made = d.git === 'offer' && !!st.gitInit;
    if (state && d.level !== 'bad' && !made) {
      g.worktree = d.git === 'repo' ? (r && r.bare ? 'Worktree isolation needs a work tree, and this repository is bare.' : r && !r.hasCommits ? 'Worktree isolation starts every copy from a commit, and this repository has none yet: commit the project first.' : '')
        : d.git === 'bare' ? 'Worktree isolation needs a work tree, and this repository is bare.'
        : d.git === 'inside' ? 'Worktree isolation needs the root of the repository: this folder is inside ' + d.root + '. Pick that folder, or isolation none.'
        : d.git === 'offer' ? 'Not a git repository yet: check git init above to make one (with a first commit), or use isolation none.'
        : d.git === 'files' ? 'Not a git repository: run git init and commit there, or pick a repository’s root folder.' : '';
    }
    /* isolation nobody chose (st.isoAuto: neither the person nor the server's flags or configuration named one) follows the folder:
       worktree for the root of a repository with a commit, or one git init makes, else none */
    if (st.isoAuto) g.isolation = state && d.level !== 'bad' && !g.worktree && (made || (d.git === 'repo' && r && !r.bare && r.hasCommits)) ? 'worktree' : 'none';
    if (g.worktree) g.isolation = 'none';
    if (g.isolation !== 'worktree') g.commit = 'Commit needs isolation worktree' + (g.worktree ? ', which this folder cannot have.' : '.');
    else if (!made && r && r.detached) g.commit = 'Commit moves the checked-out branch, and HEAD is detached: check out a branch first.';
    else if (!made && r && !r.clean) g.commit = 'Commit needs a clean checkout: this one has uncommitted changes. Commit or stash them first.';
    if (g.commit) g.commitOn = false;
    return g;
  };
  dialogs.newSession = function (seed) {
    if (SL.data) { SL.data.load('projects'); SL.data.load('models'); }
    const st = Object.assign(seedOf(), seed || {}), folder = ui.folder;
    if (seed && seed.swarm != null && seed.single == null) st.single = +seed.swarm === 0;   // a seed that names a size: 0 is a single agent
    if (seed && seed.isolation != null && seed.isoAuto == null) st.isoAuto = false;          // ... and one that names the isolation chose it
    const models = () => { const l = D.models.map(m => m.ref); if (st.model && l.indexOf(st.model) < 0) l.unshift(st.model); return l; };
    /** under the model list when it is empty: no provider has a key yet (the server lists only the models a key can use), and where to add one */
    const noModelHint = () => models().length ? '' : 'No model to choose: no provider has a key yet. <button class="lnk" type="button" data-ns-providers>Add one in Settings › Providers</button>';
    /* the list shows its first model when none is chosen: that model is the one that starts (the catalogue may come after the seed, which would otherwise leave the model empty) */
    const adopt = () => { if (!st.model) st.model = firstModel(); };
    adopt();
    let fm = null;
    const fstate = () => (fm ? fm.state() : null);
    const quiet = () => st.mode === 'bypass' || st.mode === 'yolo';   // these modes never ask: an allow rule has nothing to answer
    const rulesNow = () => (quiet() ? [] : st.rules);
    const gate = () => dialogs.nsGate(st, fstate());
    const cli = () => { const s = fstate(), d = folder.describe(s), g = gate(); return dialogs.cliLine(Object.assign({}, st, { isolation: g.isolation, commit: g.commitOn, mailman: g.mailman, verify: g.verify }, { rules: rulesNow(), cwd: s ? s.path : st.cwd, makeDir: !!s && !s.exists && d.level === 'new', makeGit: d.git === 'offer' && !!st.gitInit })); };
    const help = (key, about) => ui.help(NS_HELP[key], about);
    const opt = (k, flag, about) => '<div class="nsopt"><label class="chk"><input type="checkbox" data-k="' + k + '"' + (st[k] ? ' checked' : '') + '> ' + flag + '</label>' + help(k, about) + '</div>';
    const team = inner => '<div class="nsteam" data-team>' + inner + '</div>';
    /* a button that says what it does when it is hovered or focused */
    const chip = (attr, cmd, why) => '<button type="button" class="nschip mono" ' + attr + '="' + esc(cmd) + '"' + ui.helpAttr(why) + '>' + esc(cmd) + '</button>';
    const ruleEx = RULE_EXAMPLES.map(([r, why]) => chip('data-ex', r, r === 'tests' && st.testsSummary ? 'the build and test commands of ' + esc(st.testsSummary) : why)).join('');
    ui.modal({ title: 'New session', kicker: '+ chat flags', desc: 'one tab = one sleipnir session: its own chat, team, log, folder and budget', wide: true, color: 'var(--ok)', focus: '#nsName', body:
      (st.error ? '<p class="nserr err" role="alert">' + esc(st.error) + '</p>' : '') + (st.resume ? '<p class="stubnote mono" style="margin:0 0 8px">resume ' + esc(st.resume) + '</p>' : '') +
      '<div class="g2 ns"><div><h3 class="lab nsh">Project</h3>' + field('name', inp('nsName', st.name, 'placeholder="shop-2" aria-label="Session name"'), '', help('name', 'the name')) +
      field('folder (--cwd)', folder.html(st.cwd), '', help('folder', 'the folder')) + '<div class="nsgit" id="nsGitRow" hidden><label class="chk"><input type="checkbox" id="nsGit" data-k="gitInit"' + (st.gitInit ? ' checked' : '') + '> git init</label>' + help('git', 'git init') + '</div>' +
      '<h3 class="lab nsh">Team</h3>' + field('model (--model)', '<select id="nsModel" aria-label="Model">' + models().map(r => '<option' + (r === st.model ? ' selected' : '') + '>' + esc(r) + '</option>').join('') + '</select><small class="hint" id="nsModelHint">' + noModelHint() + '</small>', '', help('model', 'the model')) +
      field('team (--swarm, --role-model)', '<div class="seg" role="group" aria-label="Team preset"><button type="button" data-tpreset="full">Full team</button><button type="button" data-tpreset="single">Single agent</button></div> <span class="dim" id="nsSw"></span><div id="nsTeamRows" class="nsteamrows"></div><div class="nserr err" id="nsTeamErr" role="alert" hidden></div>', '', help('workers', 'the team')) +
      team(field('isolation', seg('isolation', ['none', 'worktree'], st.isolation), '', help('isolation', 'isolation')) + field('verify tasks with (--verify)', inp('nsVerify', st.verify, 'aria-label="verify command" placeholder="e.g. go test {dirs}" spellcheck="false" autocomplete="off"') + '<div class="chips" id="nsVerifyHints"></div>', '', help('verify', '--verify')) +
        field('team options', opt('commit', '--commit', '--commit') + opt('mailman', '--mailman', '--mailman') + opt('autoDocs', '<span>auto-docs <span class="nowrap">(--auto-docs)</span></span>', '--auto-docs') + opt('managerApproves', '<span>manager answers routine questions <span class="nowrap">(--manager-approves)</span></span>', '--manager-approves'))) +
      '</div><div><h3 class="lab nsh">Permissions and limits</h3>' +
      field('pre-approve actions (--allow)', '<div class="nsd nsquiet" id="nsRuleOff" role="status" hidden>' + NS_HELP.quiet + '</div><fieldset class="nsfs" id="nsRuleSet"><div class="field-row" style="margin:0"><input id="nsRule" type="text" placeholder="tests, Bash(go test:*)" aria-label="Allow rule" spellcheck="false" autocomplete="off"><button class="btn sm" type="button" id="nsAddRule">Add</button></div><div class="nserr err" id="nsRuleErr" role="alert" hidden></div><div id="nsRules" class="tags"></div><div class="chips" id="nsRuleEx">' + ruleEx + '</div></fieldset>', '', help('allow', 'a pre-approved action')) +
      field('trust, tools and memory', opt('trustProject', '--trust-project', '--trust-project') + opt('noMcp', '--no-mcp', '--no-mcp') + opt('noMemory', '--no-memory', '--no-memory')) +
      field('budget (--budget-usd)', inp('nsBudget', st.budget, 'inputmode="decimal" aria-label="Budget in dollars"'), '', help('budget', 'the budget')) +
      field('compaction at (--compact-at)', '<div class="nscompact"><input id="nsCompact" type="range" min="10" max="90" step="1" value="' + (st.compactAt != null ? st.compactAt : st.compactAtBase || 25) + '" aria-label="Compaction share, percent of each model’s context window"><span class="nscv"><output id="nsCompactOut" for="nsCompact"></output><button type="button" class="btn sm" id="nsCompactReset" hidden>default</button></span></div>', '', help('compactAt', 'the compaction share')) +
      field('first goal (optional)', '<textarea id="nsGoal" rows="3" placeholder="e.g. add pagination to the orders endpoint, with tests" aria-label="Goal">' + esc(st.goalText) + '</textarea>', '', help('goal', 'the first goal')) +
      (researchOn() ? field('research first (--research)', seg('research', ['off', 'quick', 'standard', 'deep'], st.research) + '<small class="hint" id="nsResWhy" role="status"></small>', '', help('research', 'research first')) : '') + '</div></div><div class="nsmodefld">' + field('mode (--mode)', modePick(st.mode), '', help('mode', 'a mode')) + '</div>' +
      '<h3 class="lab" style="margin:12px 0 4px">the same command line</h3><pre class="pre cli" id="nsCli"></pre><div class="row2"><button class="btn pri" type="button" id="nsStart">Start the session</button><button class="btn" type="button" id="nsCopy">Copy the command</button><button class="btn" type="button" data-close>Cancel</button></div>',
      onMount(b, sc, close) {
        /* the reasons of the team controls that cannot be used (dialogs.nsGate), each under its control */
        const why = (id, at) => { const e = document.createElement('div'); e.className = 'nswhy'; e.id = id; e.setAttribute('role', 'status'); e.hidden = true; if (at) at.after(e); return e; };
        const teamEl = $('[data-team]', b), segIso = $('[data-seg="isolation"]', b), commitBox = $('[data-k="commit"]', b), mailBox = $('[data-k="mailman"]', b);
        const whyTeam = why('nsTeamWhy', null); if (teamEl) teamEl.insertBefore(whyTeam, teamEl.firstChild);
        const whyIso = why('nsIsoWhy', segIso && segIso.parentElement), whyCommit = why('nsCommitWhy', commitBox && commitBox.closest('.nsopt'));
        const say = (e, t) => { e.hidden = !t; if (e.textContent !== t) e.textContent = t; };
        function paintGate() {
          const g = gate(), team = !!g.team;
          say(whyTeam, g.team); say(whyIso, team ? '' : g.worktree); say(whyCommit, team ? '' : g.commit);
          $$('[data-seg="isolation"]', b).forEach(x => { x.disabled = team || (x.dataset.v === 'worktree' && !!g.worktree); x.setAttribute('aria-pressed', String(x.dataset.v === g.isolation)); });
          if (commitBox) { commitBox.disabled = !!g.commit; commitBox.checked = g.commitOn; }
          if (mailBox) { mailBox.disabled = team; mailBox.checked = g.mailman; }
          const mgrBox = $('[data-k="managerApproves"]', b); if (mgrBox) { mgrBox.disabled = team; mgrBox.checked = !team && st.managerApproves !== false; }
          const v = $('#nsVerify', b); if (v) { v.disabled = team; if (team) v.value = ''; else if (document.activeElement !== v && v.value !== st.verify) v.value = st.verify; }
          $$('[data-vh]', b).forEach(c => { c.disabled = team; });
        }
        const paintVerify = () => {
          const s = fstate(), hs = folder.verifyHints(s), el = $('#nsVerifyHints', b);
          el.innerHTML = !s ? '' : hs.length ? '<span class="dim">suggested for this folder:</span>' + hs.map(h => chip('data-vh', h.cmd, 'Suggested by ' + esc(h.why) + ' in this folder. Click to use it.')).join('') : s.exists && s.dir ? '<span class="dim">no build files recognised here: write the command yourself, like <span class="mono">npm test</span>.</span>' : '';
          $$('[data-vh]', el).forEach(c => c.setAttribute('aria-pressed', String(c.dataset.vh === st.verify)));
        };
        /* the team rows: each role with its ?, a stepper and the model its workers run on */
        const paintTeam = tm => {
          const rows = $('#nsTeamRows', b), err = $('#nsTeamErr', b);
          rows.hidden = st.single;
          err.hidden = !tm.short; err.textContent = tm.short ? 'A team has at least ' + (st.minWorkers || 8) + ' workers: add ' + ((st.minWorkers || 8) - tm.n) + ' more, or choose Single agent.' : '';
          const modelOpts = cur => { const refs = models(); if (cur && !refs.includes(cur)) refs.unshift(cur); return '<option value=""' + (cur || cur === null ? '' : ' selected') + '>worker model</option>' + refs.map(r => '<option' + (r === cur ? ' selected' : '') + '>' + esc(r) + '</option>').join(''); };
          const roles = D.roleOrder.concat(Object.keys(st.counts).filter(r => D.roleOrder.indexOf(r) < 0));
          const key = JSON.stringify([st.single, st.counts, st.slotModels, st.perSlot, st.openRoles, st.roleModels, tm.n]); if (rows._k === key) return; rows._k = key;
          const full = st.maxWorkers && tm.n >= st.maxWorkers;
          if (!st.single) rows.innerHTML = roles.map(r => {
            const R = D.roles[r] || SL.data.ROLES[r] || { code: 'sc', desc: '' }, n = st.counts[r] || 0, per = (st.perSlot || {})[r], open = n > 1 && (st.openRoles || {})[r] !== false;
            const mixed = per ? per.filter((m, i, a2) => a2.indexOf(m) === i).length : 0;
            const sel = (mixed ? '<option value="" disabled selected>' + mixed + ' models: set all slots</option>' : '') + modelOpts(mixed ? null : slotModel(st, r, 0));
            const disc = n > 1 ? '<button type="button" class="nstdisc" data-topen="' + esc(r) + '" aria-expanded="' + open + '" aria-controls="nsts-' + esc(r) + '" title="' + (open ? 'one model for all ' : 'a model for each of the ') + n + ' ' + esc(r) + ' slots">' + (open ? '▾' : '▸') + '</button>' : '<span class="nstdisc" aria-hidden="true"></span>';
            const slotRows = open ? '<div class="nstslots" id="nsts-' + esc(r) + '" role="group" aria-label="' + esc(r) + ' slots, one model each">' + Array.from({ length: n }, (x, i) => '<label class="nstslot"><span class="mono" style="color:var(--c-' + esc(R.code) + ')">' + esc(R.code) + '-' + (i + 1) + '</span><select data-tslot="' + esc(r) + '" data-ti="' + i + '" aria-label="model for ' + esc(r) + ' slot ' + (i + 1) + '">' + modelOpts(slotModel(st, r, i)) + '</select></label>').join('') + '</div>' : '';
            return '<div class="nstrow' + (n ? '' : ' zero') + '" data-trole="' + esc(r) + '"><span class="nstname" style="color:var(--c-' + esc(R.code) + ')">' + esc(r) + '</span>' + ui.help(esc(R.desc || '') + '.' + (R.ro ? ' <b>Read-only.</b>' : ''), r) + '<span class="nststep"><button type="button" class="btn sm" data-tstep="-1" aria-label="one ' + esc(r) + ' less"' + (n ? '' : ' disabled') + '>−</button><span class="nstn mono" aria-label="' + esc(r) + ' workers">' + n + '</span><button type="button" class="btn sm" data-tstep="1" aria-label="one more ' + esc(r) + '"' + (full ? ' disabled' : '') + '>+</button>' + disc + '</span><select data-tmodel="' + esc(r) + '" aria-label="model for ' + (n > 1 ? 'all ' + n + ' ' : '') + esc(r) + (n > 1 ? ' slots' : '') + '"' + (n ? '' : ' disabled') + '>' + sel + '</select></div>' + slotRows;
          }).join('');
        };
        const paint = () => {
          const tm = teamOf(st); st.swarm = tm.n;   // the team decides the size before the gate and the command line read it
          $('#nsCli', b).innerHTML = esc(cli()).replace(/--mode (bypass|yolo)/, '<span class="cli-danger">--mode $1</span>'); paintMode(b, st.mode);
          $('#nsSw', b).textContent = st.single ? 'single agent' : 'manager + ' + tm.n + ' worker' + (tm.n === 1 ? '' : 's') + (tm.dflt ? ' · full team' : ' · your team');
          $$('[data-tpreset]', b).forEach(x => x.setAttribute('aria-pressed', String(x.dataset.tpreset === 'single' ? st.single : tm.dflt)));
          paintTeam(tm);
          $$('[data-team]', b).forEach(e => e.classList.toggle('nsoff', st.single));
          const docsOpt = $('[data-k="autoDocs"]', b); if (docsOpt) { const noDocs = !st.single && !(st.counts.docs > 0); docsOpt.disabled = st.single || noDocs; docsOpt.closest('.nsopt').title = noDocs ? 'the team has no docs worker' : ''; }
          $('#nsStart', b).disabled = tm.short;
          $('#nsRules', b).innerHTML = st.rules.map((r, i) => '<span class="tag">' + esc(r) + ' <button type="button" data-rr="' + i + '" aria-label="remove ' + esc(r) + '">×</button></span>').join('');
          $('#nsGitRow', b).hidden = folder.describe(fstate()).git !== 'offer'; paintVerify();
          $('#nsRuleSet', b).disabled = quiet(); $('#nsRuleOff', b).hidden = !quiet();
          /* the compaction share: the slider at the share (the default's place when none was chosen), what it means beside it, and a way back to the default */
          const cs = $('#nsCompact', b), cpos = String(st.compactAt != null ? st.compactAt : st.compactAtBase || 25), ctext = dialogs.compactAtText(st);
          if (cs.value !== cpos) cs.value = cpos; cs.setAttribute('aria-valuetext', ctext); $('#nsCompactOut', b).textContent = ctext; $('#nsCompactReset', b).hidden = st.compactAt == null;
          const rw = $('#nsResWhy', b); if (rw) { const on = !!RESEARCH_FIRST[st.research]; rw.hidden = !on; rw.className = 'hint' + (on && !st.goalText.trim() ? ' warm' : ''); rw.textContent = !on ? '' : st.goalText.trim() ? RESEARCH_FIRST[st.research] + ', then the team builds' : 'needs a first goal: there is nothing to research without one'; }
          paintGate();
        };
        ui.helpMount(b, sc);
        fm = folder.mount(b, sc, st, { projects: () => D.extra.projects || [], onState: () => paint() }); paint();
        const ruleErr = msg => { const e = $('#nsRuleErr', b); e.hidden = !msg; e.textContent = msg || ''; };
        /* a rule typed or clicked is added once; a typed one that cannot be a rule is said so and kept in its field */
        const addRule = v => { v = String(v).trim(); if (!v || quiet()) return true; const bad = ruleProblem(v); ruleErr(bad); if (bad) return false; if (!st.rules.includes(v)) st.rules.push(v); return true; };
        // Checkbox input precedes change; repainting here would restore the old checked state before change reads it.
        sc.listen(b, 'input', e => { const t = e.target; if (t.dataset.k) return; if (t.id === 'nsName') st.name = t.value; else if (t.id === 'nsVerify') st.verify = t.value; else if (t.id === 'nsBudget') st.budget = t.value.trim(); else if (t.id === 'nsCompact') st.compactAt = Math.round(Number(t.value)); else if (t.id === 'nsGoal') st.goalText = t.value; else if (t.id === 'nsRule') ruleErr(''); paint(); });
        sc.listen(b, 'change', e => { const t = e.target; if (t.id === 'nsModel') st.model = t.value; else if (t.dataset.tmodel) { const r = t.dataset.tmodel; st.slotModels = st.slotModels || {}; if (st.perSlot) delete st.perSlot[r]; st.slotModels[r] = t.value; } else if (t.dataset.tslot) setSlotModel(st, t.dataset.tslot, +t.dataset.ti, t.value); else if (t.dataset.k) st[t.dataset.k] = t.checked; paint(); });
        sc.on('models-changed', () => {
          adopt();
          $('#nsModel', b).innerHTML = models().map(r => '<option' + (r === st.model ? ' selected' : '') + '>' + esc(r) + '</option>').join('');
          $('#nsModelHint', b).innerHTML = noModelHint();
          $('#nsTeamRows', b)._k = null;
          paint();
        });
        sc.listen(b, 'click', e => {
          if (e.target.closest('[data-ns-providers]')) { close(); ui.settingsPage('providers'); return; }
          const mr = e.target.closest('[data-nsmode]'); if (mr) { st.mode = mr.dataset.nsmode; paint(); return; }
          if (e.target.closest('#nsCompactReset')) { st.compactAt = null; paint(); $('#nsCompact', b).focus(); return; }
          const x = e.target.closest('[data-seg]'); if (x) { if (x.disabled) return; st[x.dataset.seg] = x.dataset.v; if (x.dataset.seg === 'isolation') st.isoAuto = false; $$('[data-seg="' + x.dataset.seg + '"]', b).forEach(y => y.setAttribute('aria-pressed', y === x)); paint(); return; }
          const vh = e.target.closest('[data-vh]'); if (vh) { if (vh.disabled) return; st.verify = vh.dataset.vh; $('#nsVerify', b).value = st.verify; paint(); return; }
          const ex = e.target.closest('[data-ex]'); if (ex) { if (!quiet()) addRule(ex.dataset.ex); paint(); return; }
          const rr = e.target.closest('[data-rr]'); if (rr && !quiet()) { st.rules.splice(+rr.dataset.rr, 1); paint(); }
          const tp = e.target.closest('[data-tpreset]'); if (tp) { if (tp.dataset.tpreset === 'single') st.single = true; else { st.single = false; D.roleOrder.forEach(r => { st.counts[r] = 1; }); Object.keys(st.counts).forEach(r => { if (D.roleOrder.indexOf(r) < 0) st.counts[r] = 0; }); } paint(); return; }
          const to = e.target.closest('[data-topen]'); if (to) { const r = to.dataset.topen; st.openRoles = st.openRoles || {}; st.openRoles[r] = st.openRoles[r] === false; paint(); const again = $('[data-topen="' + r + '"]', b); if (again) again.focus(); return; }
          const ts = e.target.closest('[data-tstep]'); if (ts) { const r = ts.closest('[data-trole]').dataset.trole; st.counts[r] = Math.max(0, (st.counts[r] || 0) + +ts.dataset.tstep); if (st.perSlot && st.perSlot[r]) { st.perSlot[r] = st.perSlot[r].slice(0, st.counts[r]); if (st.perSlot[r].length && st.perSlot[r].every(m => m === st.perSlot[r][0])) { st.slotModels[r] = st.perSlot[r][0]; delete st.perSlot[r]; } } paint(); const again = $('[data-trole="' + r + '"] [data-tstep="' + ts.dataset.tstep + '"]', b); if (again && !again.disabled) again.focus(); return; }
        });
        sc.listen($('#nsModes', b), 'keydown', e => { const k = e.key; if (!/^(Arrow(Up|Down|Left|Right)|Home|End)$/.test(k)) return; e.preventDefault(); const rs = $$('[data-nsmode]', b), f = rs.indexOf(document.activeElement), i = f >= 0 ? f : rs.findIndex(r => r.dataset.nsmode === st.mode), j = k === 'Home' ? 0 : k === 'End' ? rs.length - 1 : (i + (/Down|Right/.test(k) ? 1 : rs.length - 1)) % rs.length; st.mode = rs[j].dataset.nsmode; paint(); rs[j].focus(); });
        const addTyped = () => { const f = $('#nsRule', b); if (addRule(f.value)) f.value = ''; paint(); };
        sc.listen($('#nsAddRule', b), 'click', addTyped); sc.listen($('#nsRule', b), 'keydown', e => { if (e.key === 'Enter') { e.preventDefault(); addTyped(); } });
        sc.listen($('#nsCopy', b), 'click', () => U.copy(cli()).then(ok => ui.toast(ok ? 'command copied' : 'copy is not available here', ok ? 'ok' : 'warm')));
        let starting = false;
        sc.listen($('#nsStart', b), 'click', () => {
          if (starting) return;
          if (Array.from(dialogs.goalSent(st)).length > SL.goalMax()) { ui.toast('The goal is longer than ' + SL.goalMax().toLocaleString('en') + ' characters. Shorten it before starting.', 'err'); return; }
          const bud = st.budget === 'off' || st.budget === '' ? 0 : parseFloat(st.budget); if (st.budget !== 'off' && st.budget !== '' && !(bud > 0)) { ui.toast('the budget is a number of dollars, or off', 'err'); return; }
          /* what was typed or chosen but not added yet is part of what starts (an allow rule in its field, a role and model picked) */
          const pending = $('#nsRule', b); if (!quiet() && !addRule(pending.value)) return; if (!quiet()) pending.value = '';
          const tm = teamOf(st); if (tm.short) { $('#nsTeamErr', b).hidden = false; return; }
          starting = true;
          /* the folder is what the server says it is for the text in the box (asked now when it was not yet): a path that cannot start a session stops here */
          fm.ensure().then(state => {
            const d = folder.describe(state), typed = st.cwd.trim();
            if (d.level === 'bad') { starting = false; ui.toast(d.lead, 'err'); return; }
            if (!state && !FOLDER_START.test(typed)) { starting = false; ui.toast('The server could not check that folder: write its full path.', 'err'); return; }
            const roleModels = Object.assign({}, st.roleModels); if (tm.dflt) Object.keys(st.slotModels || {}).forEach(r => { if (st.slotModels[r]) roleModels[r] = st.slotModels[r]; });
            const spec = { name: st.name.trim() || undefined, cwd: state ? state.path : typed, model: st.model, mode: st.mode, swarm: tm.n, isolation: st.isolation, verify: st.verify, commit: st.commit, mailman: st.mailman, rules: rulesNow(), trustProject: st.trustProject, noMcp: st.noMcp, noMemory: st.noMemory, goalText: dialogs.goalSent(st) || undefined, effort: st.effort, roleModels: tm.slots.length ? serviceModels(st, roleModels) : roleModels };
            if (tm.slots.length) spec.team = tm.slots;
            if (tm.n > 0 && st.autoDocs === false) spec.autoDocs = false;
            if (tm.n > 0 && st.managerApproves === false) spec.managerApproves = false;   // on is the server's default: only an off is sent
            const g = gate(); Object.assign(spec, { isolation: g.isolation, verify: g.verify, commit: g.commitOn, mailman: g.mailman });   /* nothing that cannot start: dialogs.nsGate */
            spec.budget = bud > 0 ? bud : 0;   // 0 is off: the server does not put its own cap on the session
            const ca = dialogs.compactAtSent(st); if (ca) spec.compactAt = ca;   // the share the session takes anyway is not sent: the configuration keeps it
            if (st.resume) spec.resume = st.resume;
            const raises = [];
            if (state && !state.exists) { spec.createDir = true; raises.push('make the folder ' + spec.cwd); } else if (state && !state.known) raises.push('work in ' + spec.cwd + ', a folder this server has not worked in');
            if (d.git === 'offer' && st.gitInit) { spec.gitInit = true; raises.push('make ' + spec.cwd + ' a git repository'); }
            /* the dialog gives way to the trust step and the confirmation; when the server does not start the session it opens again as it
               was, with why under its title */
            const again = Object.assign({}, st, { rules: st.rules.slice(), roleModels: Object.assign({}, st.roleModels), counts: Object.assign({}, st.counts), slotModels: Object.assign({}, st.slotModels), perSlot: JSON.parse(JSON.stringify(st.perSlot || {})), openRoles: Object.assign({}, st.openRoles) });
            close(); start(spec, { raises }).then(res => { if (res && !res.ok) dialogs.newSession(Object.assign(again, { error: res.declined ? 'not started: the trust step or the confirmation was declined' : 'not started: ' + (res.message || res.why || 'the server refused it') })); });
          });
        });
      } });
  };
  /* ---------- the server's confirmation of what a request raises (any request: SL.api asks through this) ---------- */
  const ASK_TITLE = [[/^\/api\/sessions$/, 'Start the session'], [/^\/api\/sessions\/resume$/, 'Resume the session'], [/\/restart$/, 'Start again'], [/\/mode$/, 'Set the mode'], [/\/rules\/remove$/, 'Remove the rule'], [/\/rules$/, 'Add the rule'], [/\/command$/, 'Run the command'], [/\/ws\/restore/, 'Restore the files']];
  const TYPE_TEXT = { yolo: 'yolo asks nothing, the very dangerous included; deny rules and guarded paths still refuse. For sandboxes. Type yolo to confirm.', bypass: 'bypass asks nothing except about the very dangerous; deny rules still apply. Type bypass to confirm.' };
  /**
   * The person's answer to what the server says a request raises ({scope, reasons, message, method, path} -> Promise<boolean>): the
   * standard confirm with the server's reasons; a dangerous mode is confirmed by typing its name, as in the Mode sheet, except when the
   * request is the New session dialog's own start, where choosing the mode in the dialog is the confirmation (its own warning says so).
   */
  function askConfirm(o) {
    /* a long list of allow rules is shown as one line; the tests preset is named as such, which needs its rules (the Permissions cache) */
    const long = (o.reasons || []).some(r => /^allow /.test(String(r)) && ui.approvals.ruleList(String(r).slice(6)).length > LONG_RULES);
    const need = long && !(D.testsPreset && D.testsPreset.length) && SL.data && SL.sessions && SL.sessions.active && !SL.sessions.active.placeholder && !SL.sessions.active.recorded;
    if (!need) return askConfirmNow(o);
    return Promise.race([SL.data.load('permissions').catch(() => false), new Promise(r => setTimeout(r, 1500))]).then(() => askConfirmNow(o));
  }
  /** More allow rules than this in one reason are one line that opens to the list. */
  const LONG_RULES = 6;
  /** The tools the tests preset covers, in a few words (the server's summary before its colon, else the commands of its rules): "go, cargo, npm…". */
  function presetTools(rules) {
    const d = SL.live && SL.live.defaults && SL.live.defaults(), sum = d && d.testsPreset ? String(d.testsPreset).split(':')[0] : '';
    const words = sum ? sum.split(/,\s*|\s+and\s+/).map(w => w.trim()).filter(Boolean) : rules.map(r => (/^Bash\(([^\s:)]+)/.exec(r) || [])[1]).filter((w, i, a) => w && a.indexOf(w) === i);
    return words.slice(0, 3).join(', ') + (words.length > 3 ? '…' : '');
  }
  /**
   * The reasons of a confirmation as the dialog lists them: every reason on a line of its own, except that the rules of an "allow …" reason
   * that names more than LONG_RULES are one line at the end with their count and a ▸ that shows them all, and the rules of the tests
   * preset (all of them, D.testsPreset) are one line of their own ("allow the tests preset: …"). Returns {lines: the short reasons,
   * groups: [{label, html, rules}] of the long ones (html: the label's markup, the rules in it marked as a reason is)}.
   */
  function confirmReasons(reasons) {
    const preset = (D.testsPreset || []).map(String), lines = [], groups = [];
    reasons.forEach(r => {
      const m = /^allow ([\s\S]+)$/.exec(r); if (!m) { lines.push(r); return; }
      let rules = ui.approvals.ruleList(m[1]);
      if (preset.length && preset.every(p => rules.includes(p))) { groups.push({ label: 'allow the tests preset: build and test commands of ' + presetTools(preset) + ' (' + preset.length + ' rules)', html: esc('allow the tests preset: build and test commands of ' + presetTools(preset) + ' (' + preset.length + ' rules)'), rules: preset }); rules = rules.filter(x => !preset.includes(x)); }
      if (!rules.length) return;
      if (rules.length > LONG_RULES) groups.push({ label: 'allow ' + rules.length + ' rules: ' + rules.slice(0, 2).join(', ') + '…', html: 'allow ' + rules.length + ' rules: ' + ui.longText.mark(rules.slice(0, 2).join(', ')) + '…', rules }); else lines.push('allow ' + rules.join(', '));
    });
    return { lines, groups };
  }
  dialogs.confirmReasons = confirmReasons;
  function askConfirmNow(o) {
    return new Promise(resolve => {
      let done = false; const finish = v => { if (!done) { done = true; resolve(v); } };
      const path = String(o.path || '').split('?')[0], fromNew = o.method === 'POST' && path === '/api/sessions';
      const dm = ((o.reasons || []).map(r => /^permission mode (bypass|yolo)$/.exec(r)).find(Boolean) || [])[1], typed = !!dm && !fromNew;
      const t = ASK_TITLE.find(x => x[0].test(path)), S = SL.sessions && SL.sessions.active, who = S && !S.placeholder && !fromNew ? '<b>' + esc(S.name) + '</b>' : 'the session';
      const rs = (o.reasons || []).map(String), cr = confirmReasons(rs);
      /* every reason is on screen at once: the short ones first, then each long list of rules as one line with ▸ that opens it */
      const list = rs.length ? '<ul class="plist">' + cr.lines.map(r => '<li><span class="pg">⚠</span><span class="pt">' + ui.longText.mark(r) + '</span></li>').join('') +
        cr.groups.map((g, i) => '<li class="cf-rules"><span class="pg">⚠</span><span class="pt"><button class="cf-tog" type="button" data-rules="' + i + '" aria-expanded="false" aria-controls="cfRules' + i + '">' + g.html + ' <span class="dx">▸ show</span></button><ul class="cf-rl" id="cfRules' + i + '" hidden>' + g.rules.map(x => '<li class="mono">' + ui.longText.mark(x) + '</li>').join('') + '</ul></span></li>').join('') + '</ul>' : esc(o.message || 'this action needs a confirmation');
      /* the request it confirms, whole: the method and the path (with its query), and the scope the server named */
      const req = '<p class="cf-req"><span class="dim">the request</span> <span class="mono">' + esc(String(o.method || '').toUpperCase()) + ' ' + esc(o.path || '') + '</span>' + (o.scope ? '<br><span class="dim">its scope</span> <span class="mono">' + esc(o.scope) + '</span>' : '') + '</p>';
      ui.modal({ title: t ? t[1] : 'Confirm', kicker: 'confirm', desc: '', color: 'var(--err)', cls: 'confirm', focus: typed ? '#cfType' : '[data-no]', onClose: () => finish(false),
        body: '<p class="cf-t">This raises what ' + who + ' may do:</p>' + (cr.lines.length + cr.groups.length > 8 ? ui.longText.line(cr.lines.concat(cr.groups.map(g => g.label)).join('\n'), 'the reasons', true) : ui.longText.line(cr.lines.join('\n'), 'the reasons')) + '<div class="cf-d qblk">' + list + '</div>' + req + (typed ? '<div class="dangerbox"><span>' + esc(TYPE_TEXT[dm]) + '</span><input id="cfType" type="text" autocomplete="off" aria-label="Type the mode name to confirm"></div>' : '') + '<div class="row2"><button class="btn danger" type="button" data-ok' + (typed ? ' disabled' : '') + '>' + (typed ? 'Set it' : 'Yes, go ahead') + '</button><button class="btn" type="button" data-no>Cancel</button></div>',
        onMount(b, sc, close) {
          const ok = $('[data-ok]', b); sc.listen(b, 'click', ui.longText.click);
          sc.listen(b, 'click', e => { const t = e.target.closest && e.target.closest('[data-rules]'); if (!t) return; const open = t.getAttribute('aria-expanded') !== 'true', l = $('#cfRules' + t.dataset.rules, b); t.setAttribute('aria-expanded', String(open)); if (l) l.hidden = !open; $('.dx', t).textContent = open ? '▾ hide' : '▸ show'; });
          if (typed) { const i = $('#cfType', b); sc.listen(i, 'input', () => { ok.disabled = i.value.trim() !== dm; }); sc.listen(i, 'keydown', e => { if (e.key === 'Enter' && !ok.disabled) { e.preventDefault(); ok.click(); } }); }
          sc.listen(ok, 'click', () => { if (ok.disabled) return; finish(true); close(); }); sc.listen($('[data-no]', b), 'click', () => close());
        } });
    });
  }
  ui.askConfirm = askConfirm;
  if (SL.api && SL.api.cfg) SL.api.cfg.askConfirm = askConfirm;

  /** Start the session (the trust step and any other confirmation run inside SL.act.newSession). */
  /** Start the session; resolves the server's result ({ok:false, why} at once for a refusal on the page). */
  function start(spec, opt) {
    const r = SL.act.newSession(spec, opt);
    if (!r || r.ok === false) return Promise.resolve({ ok: false, why: r && r.why });
    return r.done.then(res => { if (res.ok) ui.toast('starting ' + ((res.data && res.data.tab && res.data.tab.name) || r.name) + ': it runs in the background too', 'ok'); return res; });
  }

  /* ---------- the trust step: the server's challenge, whole ---------- */
  const sizeOf = b => ui.longText.size(b > 0 ? b : 0);
  /** The rows of the trust list: every file the server sent (its path, kind and size), and each path it could not read. */
  const trustRows = files => files.map(f => f.kind === 'unread' ? '<li class="unread"><span class="mono">could not be read: ' + ui.longText.mark(f.path) + '</span></li>' : '<li><span class="mono">' + ui.longText.mark(f.path) + '</span><span class="dim">' + esc(f.kind || 'file') + ' · ' + sizeOf(+f.bytes || 0) + '</span></li>').join('');
  /** The challenge as the dialog uses it: its files (objects only), the unread ones apart, its id, what else a yes confirms. */
  function trustModel(ch, o) {
    ch = ch && typeof ch === 'object' ? ch : {}; o = o || {};
    const files = (Array.isArray(ch.files) ? ch.files : []).filter(f => f && typeof f === 'object'), unread = files.filter(f => f.kind === 'unread'), read = files.filter(f => f.kind !== 'unread');
    return { ch, files, read, unread, id: typeof ch.confirm === 'string' ? ch.confirm : '', dir: String(ch.dir || o.dir || ''), bytes: read.reduce((n, f) => n + (+f.bytes > 0 ? +f.bytes : 0), 0),
      also: [].concat(Array.isArray(ch.reasons) ? ch.reasons.map(String) : [], o.also || []), once: !!ch.partial || unread.length > 0, message: o.message ? String(o.message) : '' };
  }
  /** The body of the trust dialog (the list itself is filled on mount, and the yes stays disabled until it is). */
  function trustBody(t) {
    const n = t.read.length;
    return (t.message ? '<p class="cf-t">' + esc(t.message) + '</p>' : '') +
      '<p class="cf-t"><b class="mono trdir">' + ui.longText.mark(t.dir) + '</b> brings its own instructions, skills and hooks. Use them in this session?</p>' +
      '<div class="cf-d">' + (t.ch.changed ? 'Since your last yes: ' + esc(t.ch.changed) + '. ' : 'You have not said yes to these files before. ') +
      (t.once ? 'Part of this project could not be read, so a yes holds for this session only and is not remembered.' : 'The yes holds until one of them changes.') + '</div>' +
      '<div class="qlen"><span>' + n + ' file' + (n === 1 ? '' : 's') + ', ' + sizeOf(t.bytes) + (t.unread.length ? ' · ' + t.unread.length + ' could not be read' : '') + '</span><button class="btn sm" type="button" data-end>Show the end ↓</button></div>' +
      '<ol class="trlist qblk" id="trList" aria-label="The files you are asked to trust"></ol>' +
      (t.ch.partial ? '<p class="cf-d">The scan stopped before it read the whole project: what it did not read is not listed, except the entries marked could not be read.</p>' : '') +
      (t.also.length ? '<p class="cf-t">The yes also confirms:</p><ul class="plist qblk">' + t.also.map(a => '<li><span class="pg">⚠</span><span class="pt mono">' + ui.longText.mark(a) + '</span></li>').join('') + '</ul>' : '') +
      (t.id ? '' : '<p class="cf-d err">The server sent no confirmation with these files: there is nothing to say yes to here.</p>') +
      '<div class="row2"><button class="btn pri" type="button" data-ok disabled>Yes, trust these files</button><button class="btn" type="button" data-no>Cancel</button></div>';
  }
  /**
   * The trust step (ch: the server's TrustChallenge; o: {message, also, dir}) -> Promise of the challenge's confirmation id when the person
   * says yes, else null. Every file the server sent is listed (in a list of its own scroll, with a count), each path it could not read
   * as such; the yes wakes only when the list is on screen with all of them and the challenge carries an id. Nothing is accepted that
   * was not shown: the id resolved is the one of the challenge on screen.
   */
  function trustStep(ch, o) {
    const t = trustModel(ch, o);
    return new Promise(resolve => {
      let done = false; const finish = v => { if (!done) { done = true; resolve(v); } };
      ui.modal({ title: 'Trust this project?', kicker: 'trust', desc: '', color: 'var(--warm)', cls: 'confirm', focus: '[data-no]', onClose: () => finish(null), body: trustBody(t),
        onMount(b, sc, close) {
          const list = $('#trList', b), ok = $('[data-ok]', b);
          list.innerHTML = trustRows(t.files); sc.listen(b, 'click', ui.longText.click);
          const wake = () => { if (t.id && list.isConnected && list.children.length === t.files.length && list.getClientRects().length) ok.disabled = false; };
          if (typeof requestAnimationFrame === 'function') requestAnimationFrame(wake); else wake();
          sc.listen(ok, 'click', () => { if (ok.disabled) return; finish(t.id); close(); }); sc.listen($('[data-no]', b), 'click', () => close());
        } });
    });
  }
  trustStep.model = trustModel; trustStep.body = trustBody; trustStep.rows = trustRows;
  ui.trustStep = trustStep;
})(SL);
