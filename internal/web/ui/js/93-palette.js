/* 93-palette.js: SL.palette, the command registry (slash commands, views, sessions, `sleipnir ...` commands), the palette dialog and the `/` menu's
 * source. Every slash command dispatches its real action on the active session; the long tail opens the runner. */
(function (SL) {
  'use strict';
  const U = SL.u, { $, $$, esc, mk, fmtUsd, fmtK } = U, D = SL.D, calc = SL.calc, ui = SL.ui = SL.ui || {};
  const S_ = () => SL.sessions.active;
  const local = (title, html) => { const S = S_(); if (S.replay) { ui.toast('go live first: a replay never changes the session', 'warm'); return; } S.add({ k: 'local', title, html }); S.touch(); };
  const rowsT = rows => '<table>' + rows.map(r => '<tr><td>' + r[0] + '</td><td>' + r[1] + '</td></tr>').join('') + '</table>';

  /* ---------- slash commands: name -> handler(arg) ---------- */
  const H = {
    '/goal': a => { if (a && a !== 'status') { const w = a.split(/\s+/)[0]; if (w === 'pause') return SL.act.pauseGoal(); if (w === 'resume') return SL.act.resumeGoal(); if (w === 'clear') return SL.act.clearGoal(); const r = SL.act.setGoal(a); if (r && r.ok === false && r.why && !r.fixture) ui.toast(r.why, 'warm'); } ui.sheets.goal(); },
    '/new': () => newChat(), '/clear': () => newChat(),
    '/resume': a => { if (a) { const r = SL.act.resumeSession(a); if (r && r.ok === false) ui.toast(r.why, 'warm'); } else ui.dialogs.resume(); },
    '/sessions': () => SL.views.show('sessions'),
    '/compact': a => { ui.compactDialog(); if (a) { const i = $('#cpIn'); if (i) i.value = a; } },
    '/rewind': a => { if (a) ui.ws.restore(a); else ui.ws.setCp(null, 'checkpoints'); },
    '/diff': a => ui.ws.diff(a),
    '/exit': () => ui.closeSessionAsk(S_()),
    '/model': a => { const m = a && D.models.find(x => x.ref === a); if (m) { SL.act.setModel(m.ref); ui.toast('a team starts again on ' + m.ref, 'ok'); } else ui.settingsPage('models'); },
    '/effort': a => { if (a && ['default', 'none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'].includes(a)) { SL.act.setEffort(a); ui.toast('effort: ' + a); } else ui.settingsPage('roles'); },
    '/fav': a => { const m = a && D.models.find(x => x.ref === a); if (m) { SL.act.favModel(m.ref); ui.toast((SL.G.favs.has(m.ref) ? 'starred ' : 'unstarred ') + m.ref); } else ui.settingsPage('models'); },
    '/login': () => ui.settingsPage('providers'),
    '/budget': a => { if (a) { const r = SL.act.setBudget(a); if (r && r.ok === false) ui.toast(r.why, 'err'); else ui.toast(a === 'off' ? 'budget: off' : 'budget: $' + parseFloat(a).toFixed(2), 'ok'); } else ui.settingsPage('budget'); },
    '/cost': () => { const S = S_(), c = calc.totals(S.wm); local('/cost', rowsT([['input, uncached', fmtK(c.un)], ['input, read from the cache', fmtK(c.read)], ['cache write', '0'], ['output', fmtK(c.out)], ['cache hit ratio', c.pct + '%'], ['cost', fmtUsd(c.cost, 4) + ' of ' + (S.meta.budget ? fmtUsd(S.meta.budget, 2) : 'no budget')]].concat(SL.settings.cache === 'full' ? [['saved, est. at list price', fmtUsd(c.saved, 3)]] : []))); },
    '/stats': () => SL.views.show('cache'), '/context': () => ui.sheets.context(),
    '/status': () => { const S = S_(), c = calc.totals(S.wm); local('/status', rowsT([['model', esc(S.meta.model)], ['mode', esc(S.meta.mode)], ['session', S.sid], ['budget', S.meta.budget ? fmtUsd(S.meta.budget, 2) : 'off'], ['cost', fmtUsd(c.cost, 4)], ['team', S.roster.length > 1 ? 'manager + ' + (S.roster.length - 1) + ' workers · ' + calc.active(S.wm) + ' active' : 'single agent']])); },
    '/mode': a => { if (a) { if (a === 'bypass' || a === 'yolo') ui.sheets.mode(a); else { const r = SL.act.setMode(a); if (r && r.ok === false) ui.toast(r.why, 'err'); else ui.toast('mode: ' + a); } } else ui.settingsPage('permissions'); },
    '/plan': a => { SL.act.setMode('plan'); ui.toast('plan mode: read-only'); if (a) SL.act.send(a); },
    '/allow': a => { if (!a) return ui.toast('/allow needs a rule, e.g. tests or Bash(go test:*)', 'warm'); const r = SL.act.allowRule(a, '/allow'); if (r && r.ok === false) ui.toast(r.why, 'warm'); else ui.toast('allowed this session: ' + a, 'ok'); },
    '/permissions': () => ui.settingsPage('permissions'), '/trust': () => ui.settingsPage('trust'),
    '/roles': a => { const m = /^(\w+)=(\S+)$/.exec(a || ''); if (m) { SL.act.setRoleModel(m[1], m[2]); ui.toast(m[1] + ' runs on ' + m[2], 'ok'); } else ui.settingsPage('roles'); },
    '/swarm': a => { const n = parseInt(a, 10); if (isNaN(n)) return ui.settingsPage('run'); restart(n); },
    '/restart': () => ui.settingsPage('run'), '/agents': () => SL.views.show('cockpit'),
    '/steer': a => { if (!a) return ui.toast('/steer needs text: tell the running turn something', 'warm'); const r = SL.act.steer('mgr', a); if (r && r.ok === false) ui.toast(r.why, 'warm'); else ui.toast('steer sent to the manager'); },
    '/verbose': a => { const S = S_(), on = a ? a !== 'off' : S.ui.verbose === false; S.ui.verbose = on; S.touch(); ui.toast(on ? 'notices on: Team activity shows every tool call' : 'notices off: Team activity keeps questions, mail, merges and cache breaks, and hides tool calls', on ? 'ok' : 'quiet'); },
    '/anim': a => { const on = a ? a === 'on' : ui.still(); SL.act.setMotion(on ? 'full' : 'reduce'); ui.applyMotion(); ui.toast('motion ' + (on ? 'on' : 'off') + (on ? '' : ': the horse stands')); },
    '/cwd': () => { const S = S_(); local('/cwd', '<code>' + esc(S.meta.cwd) + '</code><br><span class="dim">--isolation ' + esc(S.meta.isolation) + (S.meta.isolation === 'worktree' ? ': each worker has its own checkout' : '') + '</span>'); },
    '/recon': () => ui.runCli(['recon'], null, true), '/skills': () => ui.settingsPage('skills'), '/mcp': () => ui.settingsPage('mcp'), '/help': () => ui.sheets.help(),
  };
  function newChat() { const S = S_(); ui.confirm({ title: 'Start again, empty', text: 'Clear <b>' + esc(S.name) + '</b>: the chat and the team start again with the same model and mode. Checkpoints stay.', ok: 'Start again', danger: true, run: () => { SL.act.newChat(); ui.toast('started again, empty', 'ok'); } }); }
  function restart(n) {
    const S = S_(), N = Math.max(0, Math.min(12, n)); if (N === S.meta.swarm) return ui.toast('already ' + (N ? 'manager + ' + N + ' workers' : 'a single agent'), 'warm');
    ui.confirm({ title: 'Start the team again', text: 'Restart <b>' + esc(S.name) + '</b> as ' + (N ? 'a manager and <b>' + N + '</b> worker' + (N === 1 ? '' : 's') + (N > 8 ? ' (' + (N - 8) + ' share legs)' : '') : 'a single agent') + '? The current run is closed (its checkpoints stay).', ok: 'Restart', danger: true, run: () => { SL.act.restartTeam({ swarm: N }); ui.toast('the team starts again: ' + (N ? 'manager + ' + N + ' workers' : 'a single agent'), 'ok'); SL.views.show(SL.views.current ? SL.views.current.name : 'cockpit'); } });
  }

  /* ---------- the registry ---------- */
  let items = [];
  function build() {
    items = [];
    D.slash.forEach(c => items.push({ g: c.group || 'commands', name: c.cmd, args: c.args, d: c.desc, slash: true, live: !!H[c.cmd] }));
    const GO = { cockpit: 'the horse, the nine stalls, the gantt, the board', cache: SL.settings.cache === 'full' ? 'cost, cache, savings, the six layers' : 'the prompt by layer, requests, anomalies', mail: 'worker mail: data, not instructions', board: 'the task board and its dependencies', replay: 'the session as a recording: scrub, speed', sessions: 'live and recorded sessions, prune', tools: 'every sleipnir command', settings: 'models, roles, budget, permissions, trust, MCP ...', doctor: 'probe an endpoint', schedule: 'cron jobs and the daemon', runner: 'a form from the real flags, the command line, the output' };
    SL.views.V.order.concat(['runner']).forEach(n => { const def = SL.views.V.reg[n]; if (!def || n === 'workspace') return; items.push({ g: 'go to', name: n === 'runner' ? 'Run a command' : def.title, d: GO[n] || 'open the ' + def.title + ' view', view: n, live: true }); });
    [['files', 'Files', 'the tree: ownership, leases, protected paths'], ['changes', 'Changes', 'by task or by agent, with the diff and its attribution gutter'], ['checkpoints', 'Checkpoints', '/rewind: diff and restore, with a preview']].forEach(([id, t, d]) => items.push({ g: 'go to', name: t, d, fn: () => ui.nav.go(id), live: true }));
    [['models', 'Models'], ['roles', 'Roles & effort'], ['budget', 'Budget'], ['permissions', 'Permissions'], ['trust', 'Trust'], ['run', 'Run settings'], ['mcp', 'MCP servers'], ['skills', 'Skills, commands & hooks'], ['providers', 'Providers & login'], ['config', 'Config layers'], ['look', 'Appearance & motion']].forEach(([id, t]) => items.push({ g: 'settings', name: 'Settings › ' + t, d: 'a real page, not a help text', fn: () => ui.settingsPage(id), live: true }));
    items.push({ g: 'go to', name: 'Radio', d: 'open the Radio rail and focus the composer', fn: () => ui.rail.focus(), live: true });
    items.push({ g: 'sessions', name: 'New session…', d: 'start another sleipnir session (all the chat flags)', fn: () => ui.dialogs.newSession(), live: true }, { g: 'sessions', name: 'Resume a session…', d: 'a recorded session, with ↺', fn: () => ui.dialogs.resume(), live: true }, { g: 'sessions', name: 'Needs you (inbox)', d: 'open questions in every session', fn: () => ui.inbox.toggle(), live: true }, { g: 'sessions', name: 'Rename this session…', d: 'a name for the tab', fn: () => ui.dialogs.rename(S_()), live: true }, { g: 'sessions', name: 'Close this session…', d: 'stops its team; the log stays on disk', fn: () => ui.closeSessionAsk(S_()), live: true }, { g: 'sessions', name: 'Team…', d: 'workers, role models, isolation, verify', fn: () => ui.settingsPage('run'), live: true });
    SL.sessions.list.forEach(S => items.push({ g: 'sessions', name: 'Switch to ' + S.name, d: S.meta.cwd + ' · ' + S.state(), fn: () => SL.act.switchSession(S.id), live: true, dyn: true }));
    items.push({ g: 'time', name: 'Pin the hold', d: 'freeze the view while you read (Space over the chat; Esc releases)', fn: () => ui.togglePin(), live: true });
    D.spec.commands.forEach(c => items.push({ g: 'the program (sleipnir ...)', name: 'sleipnir ' + c.path.join(' '), d: c.summary || '', cli: c.path, live: true }));
  }
  function filter(q, slashOnly) {
    if (!items.length || items.some(x => x.dyn)) build(); q = (q || '').toLowerCase().replace(/^\//, '').replace(/^sleipnir\s+/, '').trim(); const pool = slashOnly ? items.filter(c => c.slash) : items;
    if (!q) return pool.slice();
    const sc = c => { const n = c.name.toLowerCase().replace(/^\/|^sleipnir /, ''); if (n === q) return 0; if (n.indexOf(q) === 0) return 1; if (n.indexOf(q) >= 0) return 2; if (q.length >= 3 && (c.d || '').toLowerCase().indexOf(q) >= 0) return 3; return 9; };
    return pool.map(c => [sc(c), c]).filter(x => x[0] < 9).sort((a, b) => a[0] - b[0]).map(x => x[1]);
  }
  function listHtml(list, idx) {
    if (!list.length) return '<ul class="cmds"><li class="none">No command matches.</li></ul>'; let g = null, out = '<ul class="cmds" role="presentation">';
    list.forEach((c, i) => { if (c.g !== g) { g = c.g; out += '<li class="grp" role="presentation">' + esc(g) + '</li>'; } out += '<li role="presentation"><button class="cmd" type="button" role="option" aria-selected="' + (i === idx) + '" data-i="' + i + '"><span class="cn">' + esc(c.name) + '</span>' + (c.args ? '<span class="ca">' + esc(c.args) + '</span>' : '') + '<span class="cd">' + esc(c.d || '') + '</span>' + (c.live ? '<span class="live" title="does something in this mock" aria-label="works in this mock">●</span>' : '') + '</button></li>'; });
    return out + '</ul>';
  }
  function run(c, arg) {
    if (c.view) return SL.views.show(c.view); if (c.fn) return c.fn(arg); if (c.cli) return ui.openCommand(c.cli);
    const f = H[c.name]; if (f) f(arg || ''); else ui.toast(c.name + ': no handler in this mock', 'warm');
  }
  function runLine(line) {
    const parts = line.trim().split(/\s+/), n = parts[0].toLowerCase(), arg = line.trim().slice(parts[0].length).trim();
    if (H[n]) return H[n](arg); const c = items.find(x => x.name === n) || (build(), items.find(x => x.name === n)); if (c) return run(c, arg); ui.toast('unknown command ' + parts[0] + ': / lists them', 'err');
  }
  /** the project's paths for @ completion */
  function files() { const S = S_(); return (S && S.kind === 'orders' ? D.treeOrders : D.treeShop).map(r => r[0]).filter(f => !/\/$/.test(f) && !/^\.env$/.test(f)); }

  /* ---------- the dialog (ctrl+k) ---------- */
  function open(q) {
    ui.closeModal(true); build(); const st = { items: [], idx: 0 };
    ui.modal({ title: 'Command palette', cls: 'pal', color: 'var(--fe)', focus: '#palIn', body: '<input id="palIn" type="text" role="combobox" aria-expanded="true" aria-controls="palList" placeholder="Type a command or a view: model, mode, stats, runner, prune, doctor ..." aria-label="Filter commands" autocomplete="off" spellcheck="false"><div id="palList" role="listbox"></div><div class="pf"><span><kbd>↑</kbd> <kbd>↓</kbd> choose</span><span><kbd>⏎</kbd> run</span><span><kbd>esc</kbd> close</span><span style="margin-left:auto"><span class="ok">●</span> acts in this mock</span></div>',
      onMount(b, sc, close) {
        const inp = $('#palIn', b); inp.value = q || '';
        const upd = () => { st.items = filter(inp.value, false); st.idx = Math.min(st.idx, Math.max(0, st.items.length - 1)); const l = $('#palList', b); l.innerHTML = listHtml(st.items, st.idx); const s = l.querySelector('[aria-selected="true"]'); if (s) s.scrollIntoView({ block: 'nearest' }); };
        sc.listen(inp, 'input', () => { st.idx = 0; upd(); }); sc.listen($('#palList', b), 'click', e => { const x = e.target.closest('.cmd'); if (x) { const c = st.items[+x.dataset.i]; close(); run(c, ''); } });
        sc.listen(inp, 'keydown', e => { if (e.key === 'ArrowDown') { e.preventDefault(); st.idx = Math.min(st.items.length - 1, st.idx + 1); upd(); } else if (e.key === 'ArrowUp') { e.preventDefault(); st.idx = Math.max(0, st.idx - 1); upd(); } else if (e.key === 'Enter') { e.preventDefault(); const raw = inp.value.trim(), c = st.items[st.idx]; close(); if (c) run(c, raw.charAt(0) === '/' && raw.indexOf(c.name) === 0 ? raw.slice(c.name.length).trim() : ''); else if (raw.charAt(0) === '/') runLine(raw); } });
        upd(); inp.focus(); inp.select();
      } });
  }
  SL.palette = { open, filter, listHtml, run, runLine, files, build, H, local };
})(SL);
