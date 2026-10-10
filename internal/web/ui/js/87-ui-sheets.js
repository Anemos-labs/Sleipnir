/* 87-ui-sheets.js: SL.ui.sheets (goal, mode, context, help, history) and SL.ui.dialogs (new session, resume, rename). Each one reads
 * the active session and changes it only through SL.act; what it shows after an action is what the server sends back.
 * The New session dialog starts from the server's defaults (its flags and configuration, PARITY A4), offers the projects the server
 * allows (a new session never starts in a free path), and runs the trust step: the server's challenge is the "Trust this project?"
 * confirm, and the person's yes repeats the request with its confirmation id. */
(function (SL) {
  'use strict';
  const U = SL.u, { $, $$, esc, fmtK } = U, D = SL.D, calc = SL.calc, ui = SL.ui = SL.ui || {};
  const S_ = () => SL.sessions.active;
  const sheets = ui.sheets = {}, dialogs = ui.dialogs = {};
  const chipTag = (t, c) => '<span class="tag ' + (c || '') + '">' + esc(t) + '</span>';
  const inp = (id, v, extra) => '<input id="' + id + '" type="text" value="' + esc(v == null ? '' : v) + '" ' + (extra || '') + '>';
  const seg = (name, list, cur) => '<div class="seg" role="group" aria-label="' + name + '">' + list.map(v => '<button type="button" data-seg="' + name + '" data-v="' + esc(v) + '" aria-pressed="' + (String(v) === String(cur)) + '">' + esc(v) + '</button>').join('') + '</div>';
  const field = (label, inner, hint) => '<div class="fld"><label>' + label + '</label><div class="fc">' + inner + (hint ? '<small class="hint">' + hint + '</small>' : '') + '</div></div>';
  ui.fmt = { seg, field, inp, chipTag };
  /** Toast the why of a refused action; returns true when it was refused. */
  const refused = r => { if (r && r.ok === false) { if (r.why) ui.toast(r.why, 'warm'); return true; } return false; };

  /* ---------- goal ---------- */
  sheets.goal = function () {
    const S = S_(), m = S.m || S.wm, gs = m.goal.state, goalText = S.meta.goalText || m.goal.objective || '';
    /* evidence: the tasks merged, then what the judge says is left (unchecked), or its reason when the goal is met (D-07) */
    const ev = m.torder.map(t => [t + ' ' + m.tasks[t].title + ' merged', m.tasks[t].st === 'merged']).concat(gs === 'met' ? (m.goal.reason ? [[m.goal.reason, true]] : []) : (m.left || []).map(x => [x, false]));
    const done = m.plan.filter(s => s === 'done').length, plan = S.plan, max = m.goal.max, used = m.goal.turns || 0;
    ui.modal({ title: 'Goal', kicker: '/goal', desc: 'work until it is met, judged on evidence', wide: true, body:
      '<p class="goal-big">' + (goalText ? esc(goalText) : '<span class="dim">No goal set. <kbd>/goal TEXT</kbd> sets one: the manager works until the judge finds the evidence.</span>') + '</p>' +
      (goalText ? '<div class="g2"><div><h3 class="lab">plan, from the planner</h3><ul class="plist">' + plan.map((p, i) => '<li class="' + m.plan[i] + '"><span class="pg">' + ({ done: '✓', verify: '▸', act: '✎', edit: '✎', ask: '?', queued: '◌', pending: '◌' }[m.plan[i]] || '◌') + '</span><span class="pt">' + (i + 1) + '. ' + esc(p) + '</span></li>').join('') + '</ul></div><div><h3 class="lab">the judge, on evidence</h3><div class="verdict ' + (gs === 'met' ? 'met' : '') + ' boxed"><b>verdict</b>' + esc((m.verdict || 'no verdict yet').replace(/`/g, '')) + '</div><ul class="plist">' + ev.map(([t, ok]) => '<li class="' + (ok ? 'done' : '') + '"><span class="pg">' + (ok ? '✓' : '◌') + '</span><span class="pt">' + esc(t) + '</span></li>').join('') + '</ul></div></div>' +
        '<p class="stubnote">State <b class="st-' + gs + '">' + gs + '</b> · ' + done + '/' + plan.length + ' steps · ' + m.merged.length + '/' + m.torder.length + ' tasks merged · judged on evidence at every turn boundary' + (max ? ' · continuation limit ' + max + ' turns, used ' + used : '') + (m.goal.paused && gs === 'paused' ? ' · paused: ' + esc(m.goal.paused) : '') + '</p>' +
        '<div class="row2"><button class="btn" type="button" data-g="pause"' + (gs !== 'active' ? ' disabled' : '') + '>Pause the goal <kbd>esc</kbd></button><button class="btn" type="button" data-g="resume"' + (gs !== 'paused' ? ' disabled' : '') + '>Resume</button><button class="btn danger" type="button" data-g="clear">Clear the goal</button></div>' : '<div class="field-row"><input id="goalIn" type="text" placeholder="e.g. add pagination to /orders; go test ./... must pass" aria-label="Goal text"><button class="btn pri" type="button" data-g="set">Set the goal</button></div>'),
      onMount(b, sc, close) { sc.listen(b, 'click', e => { const x = e.target.closest('[data-g]'); if (!x) return; const k = x.dataset.g; let r; if (k === 'pause') r = SL.act.pauseGoal(); else if (k === 'resume') r = SL.act.resumeGoal(); else if (k === 'clear') r = SL.act.clearGoal(); else if (k === 'set') r = SL.act.setGoal($('#goalIn', b).value); refused(r); close(); }); } });
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
    ui.modal({ title: 'Help', kicker: '/help', desc: 'this text, and your custom commands and skills', wide: true, color: 'var(--fe)', body: '<div class="g2"><div><h3 class="lab">keys</h3><dl class="kv keys2">' + [['g then c r f h k a m b p s t e', 'Cockpit, Radio, Files, Changes, Checkpoints, Cache, Mail, Board, Replay, Sessions, Tools, Settings'], ['alt+t · alt+g', 'Cache · Cockpit (browsers reserve ctrl+t and ctrl+g)'], ['alt+1 … alt+9', 'the nth session tab']].concat(D.shortcuts).map(([k, d]) => '<dt>' + esc(k) + '</dt><dd>' + esc(d) + '</dd>').join('') + '</dl><p class="stubnote" style="margin-top:10px">Browsers reserve ctrl+t and ctrl+g; alt+t and alt+g do the same here.</p></div><div><h3 class="lab">commands</h3>' + SL.palette.listHtml(SL.palette.filter('', true), -1).replace(/<button class="cmd"/g, '<div class="cmd"').replace(/<\/button>/g, '</div>') + '</div></div>' });
  };
  sheets.history = function () {
    const S = S_(), h = S.hist.slice().reverse();
    ui.modal({ title: 'History', kicker: 'ctrl+r', desc: 'lines you sent from this prompt', color: 'var(--fe)', body: h.length ? '<ul class="cmds">' + h.map((x, i) => '<li><button class="cmd" type="button" data-h="' + i + '"><span class="cn">' + esc(x) + '</span></button></li>').join('') + '</ul>' : '<p class="stubnote">Nothing typed yet in this session.</p>', onMount(b, sc, close) { sc.listen(b, 'click', e => { const x = e.target.closest('[data-h]'); if (x) { close(); ui.setDraft(h[+x.dataset.h]); } }); } });
  };

  /* ---------- dialogs ---------- */
  dialogs.rename = function (S) {
    ui.modal({ title: 'Rename the session', kicker: 'rename', color: 'var(--mgr)', focus: '#rnIn', body: '<div class="field-row">' + inp('rnIn', S.name, 'aria-label="Session name"') + '<button class="btn pri" type="button" id="rnGo">Rename</button></div>', onMount(b, sc, close) { const go = () => { const v = $('#rnIn', b).value.trim(); if (!v) return; const r = SL.act.renameSession(S.id, v); close(); if (refused(r)) return; if (r && r.done) r.done.then(x => { if (x.ok) ui.toast('renamed to ' + v, 'ok'); }); }; sc.listen($('#rnGo', b), 'click', go); sc.listen($('#rnIn', b), 'keydown', e => { if (e.key === 'Enter') go(); }); } });
  };
  const ageTxt = s => s < 86400 ? Math.round(s / 3600) + 'h' : Math.round(s / 86400) + 'd';
  dialogs.resume = function () {
    function body() { const list = SL.sessions.recorded; return '<div class="row2" style="margin:0 0 10px"><button class="btn pri" type="button" data-r="latest">--continue: the latest session in this directory</button></div><table class="tbl"><thead><tr><th></th><th>session</th><th>first prompt</th><th class="r">agents</th><th class="r">cost</th><th>age</th></tr></thead><tbody>' + list.map(r => '<tr><td>' + (r.resumable ? '<span class="res" title="resumable">↺</span>' : '<span class="dim" title="too old to resume">·</span>') + '</td><td class="mono dim">' + esc(r.id) + '</td><td class="bright" title="' + esc(r.model ? 'model ' + r.model : '') + '">' + esc(r.first) + (r.interrupted ? ' <span class="warm">⚠ interrupted</span>' : '') + '</td><td class="r">' + (r.agents | 0) + '</td><td class="r">' + U.fmtUsd(r.cost || 0, 2) + '</td><td>' + ageTxt(r.ageS || 0) + '</td><td class="r nowrap"><button class="btn sm pri" type="button" data-r="' + esc(r.id) + '"' + (r.resumable ? '' : ' disabled') + '>↺ Resume</button> <button class="btn sm" type="button" data-rs="' + esc(r.id) + '"' + (r.resumable ? '' : ' disabled') + ' title="open New session with this session\'s model and team, then resume it">↺ with settings…</button></td></tr>').join('') + (list.length ? '' : '<tr><td colspan="7" class="dim">no recorded session yet</td></tr>') + '</tbody></table><p class="stubnote" style="margin-top:10px">Equivalent: <span class="mono">sleipnir chat --resume ID</span> or <span class="mono">--continue</span>. Sessions older than 30 days may have been pruned of their checkpoints.</p>'; }
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
  /** The seed of the New session dialog: the server's defaults (its flags over the configuration), else the mock's own values. */
  function seedOf() {
    const has = !!(SL.live && SL.live.defaults()), d = has ? SL.live.defaults() : {}, projects = (D.extra.projects || []), def = projects.find(p => p.default) || projects[0];
    const swarm = d.swarm != null ? d.swarm : 8, cwd = (def && def.dir) || d.cwd || '';
    /* with the server's defaults an absent field is the CLI's own default (no budget, no verify command); without them, the mock's seeds */
    const act = SL.sessions && SL.sessions.active, actModel = act && !act.placeholder && !act.recorded ? act.meta.model : '';
    return { name: '', cwd, model: d.model || actModel || (D.models[0] ? D.models[0].ref : ''), mode: d.mode || 'default', swarm, isolation: d.isolation || (swarm ? 'worktree' : 'none'), verify: d.verify != null ? d.verify : has ? '' : 'go test {dirs}', commit: !!d.commit, mailman: !!d.mailman, mailmanDefault: !!d.mailman, budget: d.budget ? String(d.budget) : has ? 'off' : '5', rules: (d.rules || []).slice(), trustProject: d.trustProject !== false, noMcp: !!d.noMcp, goalText: '', effort: d.effort || 'default', roleModels: Object.assign({}, d.roleModels || {}), maxWorkers: d.maxWorkers || 12, resume: '' };
  }
  dialogs.newSession = function (seed) {
    if (SL.data) { SL.data.load('projects'); SL.data.load('models'); }
    const st = Object.assign(seedOf(), seed || {}), MAXW = st.maxWorkers;
    const projects = () => { const l = (D.extra.projects || []).slice(); if (st.cwd && !l.some(p => p.dir === st.cwd)) l.unshift({ dir: st.cwd, trust: '' }); return l; };
    const models = () => { const l = D.models.map(m => m.ref); if (st.model && l.indexOf(st.model) < 0) l.unshift(st.model); return l; };
    const cli = () => { const f = ['sleipnir chat']; if (st.resume) f.push('--resume ' + st.resume); if (st.cwd && st.cwd !== '.') f.push('--cwd ' + st.cwd); f.push('--model ' + st.model); f.push('--mode ' + st.mode); f.push('--swarm ' + st.swarm); f.push('--isolation ' + st.isolation); if (st.verify) f.push('--verify "' + st.verify + '"'); if (st.commit) f.push('--commit'); if (st.mailman) f.push('--mailman'); else if (st.mailmanDefault) f.push('--mailman=false'); if (st.budget && st.budget !== 'off') f.push('--budget-usd ' + st.budget); Object.keys(st.roleModels).forEach(r => f.push('--role-model ' + r + '=' + st.roleModels[r])); st.rules.forEach(r => f.push('--allow ' + r)); if (st.trustProject) f.push('--trust-project'); if (st.noMcp) f.push('--no-mcp'); return f.join(' ') + (st.goalText ? '\n  # then: /goal ' + st.goalText : ''); };
    const chk = (k, label) => '<label class="chk"><input type="checkbox" data-k="' + k + '"' + (st[k] ? ' checked' : '') + '> ' + label + '</label>';
    ui.modal({ title: 'New session', kicker: '+ chat flags', desc: 'one tab = one sleipnir session: its own chat, team, log, directory and budget', wide: true, color: 'var(--ok)', focus: '#nsName', body:
      (st.resume ? '<p class="stubnote mono" style="margin:0 0 8px">resume ' + esc(st.resume) + '</p>' : '') +
      '<div class="g2 ns"><div>' + field('name', inp('nsName', st.name, 'placeholder="shop-2" aria-label="Session name"')) + field('directory (--cwd)', '<select id="nsCwd" aria-label="Directory">' + projects().map(p => '<option value="' + esc(p.dir) + '"' + (p.dir === st.cwd ? ' selected' : '') + '>' + esc(p.dir) + '</option>').join('') + '</select>') +
      field('model (--model)', '<select id="nsModel" aria-label="Model">' + models().map(r => '<option' + (r === st.model ? ' selected' : '') + '>' + esc(r) + '</option>').join('') + '</select>') +
      field('workers (--swarm)', '<input id="nsSwarm" type="number" min="0" max="' + MAXW + '" value="' + st.swarm + '" aria-label="worker count"><span class="dim" id="nsSw"></span>') + field('isolation', seg('isolation', ['none', 'worktree'], st.isolation)) + field('verify (--verify)', inp('nsVerify', st.verify, 'aria-label="verify command"')) + '</div><div>' +
      field('budget (--budget-usd)', inp('nsBudget', st.budget, 'inputmode="decimal" aria-label="Budget in dollars"'), 'a number, or off') + field('flags', chk('commit', '--commit') + chk('mailman', '--mailman') + chk('noMcp', '--no-mcp') + chk('trustProject', '--trust-project'), 'trust-project: the repository’s own instructions, skills and hooks are used (asked at the start, remembered by hash)') +
      field('allow rules (--allow)', '<div class="field-row" style="margin:0"><input id="nsRule" type="text" placeholder="tests, Bash(go test:*)" aria-label="Allow rule"><button class="btn sm" type="button" id="nsAddRule">Add</button></div><div id="nsRules" class="tags"></div>') + field('role models (--role-model)', '<div class="field-row" style="margin:0"><select id="nsRole" aria-label="Role">' + D.roleOrder.map(r => '<option>' + esc(r) + '</option>').join('') + '</select><select id="nsRoleM" aria-label="Model for the role">' + models().map(r => '<option>' + esc(r) + '</option>').join('') + '</select><button class="btn sm" type="button" id="nsAddRole">Add</button></div><div id="nsRoleTags" class="tags"></div>', 'a role without an entry runs on the worker model') + field('first goal (optional)', '<textarea id="nsGoal" rows="3" placeholder="/goal text: leave empty for a plain chat" aria-label="Goal">' + esc(st.goalText) + '</textarea>') + '</div></div><div class="nsmodefld">' + field('mode (--mode)', modePick(st.mode)) + '</div>' +
      '<h3 class="lab" style="margin:12px 0 4px">the same command line</h3><pre class="pre cli" id="nsCli"></pre><div class="row2"><button class="btn pri" type="button" id="nsStart">Start the session</button><button class="btn" type="button" id="nsCopy">Copy the command</button><button class="btn" type="button" data-close>Cancel</button></div>',
      onMount(b, sc, close) {
        const paint = () => { $('#nsCli', b).innerHTML = esc(cli()).replace(/--mode (bypass|yolo)/, '<span class="cli-danger">--mode $1</span>'); paintMode(b, st.mode); $('#nsSw', b).textContent = st.swarm === 0 ? ' single agent' : ' manager + ' + st.swarm + ' worker' + (st.swarm === 1 ? '' : 's') + (st.swarm > 8 ? ' (' + (st.swarm - 8) + ' share legs)' : ''); $('#nsRoleTags', b).innerHTML = Object.keys(st.roleModels).map(r => '<span class="tag">' + esc(r) + ' = ' + esc(st.roleModels[r]) + ' <button type="button" data-rmr="' + esc(r) + '" aria-label="remove ' + esc(r) + '">×</button></span>').join(''); $('#nsRules', b).innerHTML = st.rules.map((r, i) => '<span class="tag">' + esc(r) + ' <button type="button" data-rr="' + i + '" aria-label="remove ' + esc(r) + '">×</button></span>').join(''); }; paint();
        sc.listen(b, 'input', e => { const t = e.target; if (t.id === 'nsName') st.name = t.value; else if (t.id === 'nsVerify') st.verify = t.value; else if (t.id === 'nsBudget') st.budget = t.value.trim(); else if (t.id === 'nsSwarm') st.swarm = Math.max(0, Math.min(MAXW, parseInt(t.value, 10) || 0)); else if (t.id === 'nsGoal') st.goalText = t.value; paint(); });
        sc.listen(b, 'change', e => { const t = e.target; if (t.id === 'nsCwd') st.cwd = t.value; else if (t.id === 'nsModel') st.model = t.value; else if (t.dataset.k) st[t.dataset.k] = t.checked; paint(); });
        sc.listen(b, 'click', e => { const mr = e.target.closest('[data-nsmode]'); if (mr) { st.mode = mr.dataset.nsmode; paint(); return; } const x = e.target.closest('[data-seg]'); if (x) { st[x.dataset.seg] = x.dataset.v; $$('[data-seg="' + x.dataset.seg + '"]', b).forEach(y => y.setAttribute('aria-pressed', y === x)); paint(); return; } const rr = e.target.closest('[data-rr]'); if (rr) { st.rules.splice(+rr.dataset.rr, 1); paint(); } const rm = e.target.closest('[data-rmr]'); if (rm) { delete st.roleModels[rm.dataset.rmr]; paint(); } if (e.target.closest('#nsAddRole')) { st.roleModels[$('#nsRole', b).value] = $('#nsRoleM', b).value; paint(); } });
        sc.listen($('#nsModes', b), 'keydown', e => { const k = e.key; if (!/^(Arrow(Up|Down|Left|Right)|Home|End)$/.test(k)) return; e.preventDefault(); const rs = $$('[data-nsmode]', b), f = rs.indexOf(document.activeElement), i = f >= 0 ? f : rs.findIndex(r => r.dataset.nsmode === st.mode), j = k === 'Home' ? 0 : k === 'End' ? rs.length - 1 : (i + (/Down|Right/.test(k) ? 1 : rs.length - 1)) % rs.length; st.mode = rs[j].dataset.nsmode; paint(); rs[j].focus(); });
        const addRule = () => { const v = $('#nsRule', b).value.trim(); if (v && !st.rules.includes(v)) { st.rules.push(v); $('#nsRule', b).value = ''; paint(); } }; sc.listen($('#nsAddRule', b), 'click', addRule); sc.listen($('#nsRule', b), 'keydown', e => { if (e.key === 'Enter') { e.preventDefault(); addRule(); } });
        sc.listen($('#nsCopy', b), 'click', () => U.copy(cli()).then(ok => ui.toast(ok ? 'command copied' : 'copy is not available here', ok ? 'ok' : 'warm')));
        sc.listen($('#nsStart', b), 'click', () => { const bud = st.budget === 'off' || st.budget === '' ? 0 : parseFloat(st.budget); if (st.budget !== 'off' && st.budget !== '' && !(bud > 0)) { ui.toast('the budget is a number of dollars, or off', 'err'); return; }
          const spec = { name: st.name.trim() || undefined, cwd: st.cwd, model: st.model, mode: st.mode, swarm: st.swarm, isolation: st.isolation, verify: st.verify, commit: st.commit, mailman: st.mailman, rules: st.rules, trustProject: st.trustProject, noMcp: st.noMcp, goalText: st.goalText.trim() || undefined, effort: st.effort, roleModels: st.roleModels };
          if (bud > 0) spec.budget = bud; if (st.resume) spec.resume = st.resume;
          const proj = (D.extra.projects || []).find(p => p.dir === st.cwd), untrusted = st.trustProject && proj && proj.trust && proj.trust !== 'trusted';
          close(); if (untrusted) trustThen(st.cwd, null, spec); else start(spec, null);
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
    return new Promise(resolve => {
      let done = false; const finish = v => { if (!done) { done = true; resolve(v); } };
      const path = String(o.path || '').split('?')[0], fromNew = o.method === 'POST' && path === '/api/sessions';
      const dm = ((o.reasons || []).map(r => /^permission mode (bypass|yolo)$/.exec(r)).find(Boolean) || [])[1], typed = !!dm && !fromNew;
      const t = ASK_TITLE.find(x => x[0].test(path)), S = SL.sessions && SL.sessions.active, who = S && !S.placeholder && !fromNew ? '<b>' + esc(S.name) + '</b>' : 'the session';
      const list = (o.reasons || []).length ? '<ul class="plist">' + o.reasons.map(r => '<li><span class="pg">⚠</span><span class="pt">' + esc(r) + '</span></li>').join('') + '</ul>' : esc(o.message || 'this action needs a confirmation');
      ui.modal({ title: t ? t[1] : 'Confirm', kicker: 'confirm', desc: '', color: 'var(--err)', cls: 'confirm', focus: typed ? '#cfType' : '[data-no]', onClose: () => finish(false),
        body: '<p class="cf-t">This raises what ' + who + ' may do:</p><div class="cf-d">' + list + '</div>' + (typed ? '<div class="dangerbox"><span>' + esc(TYPE_TEXT[dm]) + '</span><input id="cfType" type="text" autocomplete="off" aria-label="Type the mode name to confirm"></div>' : '') + '<div class="row2"><button class="btn danger" type="button" data-ok' + (typed ? ' disabled' : '') + '>' + (typed ? 'Set it' : 'Yes, go ahead') + '</button><button class="btn" type="button" data-no>Cancel</button></div>',
        onMount(b, sc, close) {
          const ok = $('[data-ok]', b);
          if (typed) { const i = $('#cfType', b); sc.listen(i, 'input', () => { ok.disabled = i.value.trim() !== dm; }); sc.listen(i, 'keydown', e => { if (e.key === 'Enter' && !ok.disabled) { e.preventDefault(); ok.click(); } }); }
          sc.listen(ok, 'click', () => { if (ok.disabled) return; finish(true); close(); }); sc.listen($('[data-no]', b), 'click', () => close());
        } });
    });
  }
  ui.askConfirm = askConfirm;
  if (SL.api && SL.api.cfg) SL.api.cfg.askConfirm = askConfirm;

  /** Start the session; the server's trust challenge (409 trust_required) opens the trust confirm, whose yes repeats with its id. */
  function start(spec, confirmId) {
    const r = SL.act.newSession(spec, { confirmId, onTrust: ch => trustThen(spec.cwd, ch, spec) });
    if (refused(r)) return;
    r.done.then(res => { if (res.ok) ui.toast('started ' + ((res.data && res.data.tab && res.data.tab.name) || r.name) + ': it runs in the background too', 'ok'); });
  }
  /** The mock's "Trust this project?" confirm. With a challenge in hand the yes repeats at once; without one the request goes first
   *  and the server's challenge (which this yes already answered) is accepted straight away. */
  function trustThen(dir, ch, spec) {
    const files = ch && ch.files && ch.files.length ? ch.files.slice(0, 6).map(f => f.path).join(', ') + (ch.files.length > 6 ? ', …' : '') : '';
    /* the yes also confirms what else the session raises (one confirmation covers the session's settings): say it */
    const also = [].concat(spec.mode === 'bypass' || spec.mode === 'yolo' ? ['permission mode ' + spec.mode] : [], (spec.rules || []).length ? ['allow ' + spec.rules.join(', ')] : [], spec.verify ? ['run the verify command ' + spec.verify] : []);
    ui.confirm({ title: 'Trust this project?', text: '<b>' + esc(dir) + '</b> brings its own instructions, skills and hooks. Use them in this session?', detail: (ch && ch.changed ? 'Since your last yes: ' + esc(ch.changed) + '. ' : 'You have not said yes to these files before. ') + 'The yes holds until one of them changes.' + (files ? '<br><span class="mono dim">' + esc(files) + '</span>' : '') + (also.length ? '<br>It also confirms: ' + also.map(a => '<span class="mono">' + esc(a) + '</span>').join(' · ') : ''), ok: 'Yes, trust these files',
      run: () => { if (ch && ch.confirm) start(spec, ch.confirm); else { const r = SL.act.newSession(spec, { onTrust: c2 => start(spec, c2.confirm) }); if (refused(r)) return; r.done.then(res => { if (res.ok) ui.toast('started ' + ((res.data && res.data.tab && res.data.tab.name) || r.name) + ': it runs in the background too', 'ok'); }); } } });
  }
})(SL);
