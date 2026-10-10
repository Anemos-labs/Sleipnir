/* 98b-ui-tools.js: Tools. A catalogue of every `sleipnir` command as a card, in four groups; a card opens its own panel where there is one
 * (Sessions, Doctor, Schedule, and the Settings pages for models, trust, mcp, config and login) or the generic runner (a form from the real flags,
 * the command line, a streaming output pane and a result card). Doctor and Schedule are purpose-built views registered here.
 * Output text from a probe, a job log or a file is data: it is escaped and never read as markup. */
(function (SL) {
  'use strict';
  const U = SL.u, { $, $$, esc, mk, fmtUsd, fmtN } = U, D = SL.D, G = SL.G, X = D.extra, ui = SL.ui = SL.ui || {};
  const reg = SL.views.register;
  ui.settingsPage = id => { ui.setPage = id; SL.views.show('settings', { page: id }); };
  const GROUPS = [
    ['Run and chat', 'start work, in a terminal-like session or headless', ['chat', 'run', 'swarm', 'demo', 'mock']],
    ['Observe', 'what happened, what it cost, what slowed it down', ['sessions', 'inspect', 'watch', 'replay', 'friction', 'recon']],
    ['Evaluate', 'probe an endpoint, simulate a policy, measure a model', ['doctor', 'sim', 'models', 'rl']],
    ['Set up', 'projects, accounts, trust, tool servers, schedules', ['init', 'login', 'logout', 'trust', 'mcp', 'schedule', 'daemon', 'config', 'update', 'version', 'help']],
  ];
  const BUILT = {
    sessions: ['Sessions', () => SL.views.show('sessions')], doctor: ['Doctor', () => SL.views.show('doctor')], schedule: ['Schedule', () => SL.views.show('schedule')], daemon: ['Schedule', () => SL.views.show('schedule')],
    models: ['Settings › Models', () => ui.settingsPage('models')], trust: ['Settings › Trust', () => ui.settingsPage('trust')], mcp: ['Settings › MCP servers', () => ui.settingsPage('mcp')], config: ['Settings › Config layers', () => ui.settingsPage('config')],
    login: ['Settings › Providers', () => ui.settingsPage('providers')], logout: ['Settings › Providers', () => ui.settingsPage('providers')], replay: ['Replay', () => SL.views.show('replay')], watch: ['Cockpit', () => SL.views.show('cockpit')], inspect: ['Cache', () => SL.views.show('cache')],
  };
  /** a command from the palette or the catalogue: its own panel when it has one, the generic runner otherwise */
  ui.openCommand = path => { const b = path.length === 1 ? BUILT[path[0]] : null; if (b) b[1](); else ui.runCli(path); };
  const top = () => D.spec.commands.filter(c => c.path.length === 1), subsOf = n => D.spec.commands.filter(c => c.path.length > 1 && c.path[0] === n);

  /* ---------------------------------------------------------------- the catalogue */
  reg({ name: 'tools', title: 'Tools', mount(sc, root) {
    const st = { q: '' };
    root.innerHTML = '<div class="toolsv"><header class="sethead"><h1>Tools</h1><p>Every <span class="mono">sleipnir</span> command, ' + D.spec.commands.length + ' in all. Open a purpose-built panel where there is one; the runner builds a form from the real flags for the rest, shows the command line and streams the output.</p></header>' +
      '<div class="toolbar"><input id="tq" type="search" placeholder="filter commands: doctor, prune, rl rollout, trust ..." aria-label="Filter commands" autocomplete="off"><button class="btn" type="button" data-run>Open the runner <span class="dim">· all commands</span></button></div><div class="tgroups"></div></div>';
    const host = $('.tgroups', root);
    function draw() {
      const words = st.q.toLowerCase().split(/\s+/).filter(Boolean), all = top(), seen = new Set(); let h = '';
      const fits = c => !words.length || words.every(w => (c.path.join(' ') + ' ' + (c.summary || '') + ' ' + subsOf(c.path[0]).map(s => s.path.slice(1).join(' ')).join(' ')).toLowerCase().includes(w));
      GROUPS.forEach(([title, sub, names]) => {
        const cards = names.map(n => all.find(c => c.path[0] === n)).filter(c => c && fits(c)); names.forEach(n => seen.add(n)); if (!cards.length) return;
        h += '<section class="tg"><div class="tgh"><h2>' + title + '</h2><span class="dim">' + sub + '</span></div><div class="tcards">' + cards.map(c => card(c)).join('') + '</div></section>';
      });
      const rest = all.filter(c => !seen.has(c.path[0]) && fits(c)); if (rest.length) h += '<section class="tg"><div class="tgh"><h2>Other</h2></div><div class="tcards">' + rest.map(c => card(c)).join('') + '</div></section>';
      host.innerHTML = h || '<p class="stubnote" style="padding:14px 4px">No command matches “' + esc(st.q) + '”.</p>';
    }
    function card(c) {
      const n = c.path[0], b = BUILT[n], subs = subsOf(n), many = subs.length > 10;
      return '<article class="tcx" data-cmd="' + esc(n) + '"><header><b class="mono">sleipnir ' + esc(n) + '</b>' + (b ? '<span class="tag fe" title="a purpose-built screen for this command">' + esc(b[0]) + '</span>' : '') + '</header><p>' + esc(c.summary || '') + '</p>' +
        (subs.length ? '<div class="subs" aria-label="subcommands">' + (many ? subs.slice(0, 8) : subs).map(s => '<button type="button" class="chip sub" data-sub="' + esc(s.path.join(' ')) + '" title="' + esc(s.summary || '') + '">' + esc(s.path.slice(1).join(' ')) + '</button>').join('') + (many ? '<span class="dim">+' + (subs.length - 8) + ' more in the runner</span>' : '') + '</div>' : '') +
        '<footer>' + (b ? '<button type="button" class="btn sm pri" data-open="' + esc(n) + '">Open ' + esc(b[0]) + '</button><button type="button" class="btn sm" data-cli="' + esc(n) + '">Run as CLI</button>' : '<button type="button" class="btn sm pri" data-cli="' + esc(n) + '">Open in the runner</button>') + '</footer></article>';
    }
    sc.listen($('#tq', root), 'input', e => { st.q = e.target.value; draw(); });
    sc.listen(root, 'click', e => {
      const o = e.target.closest('[data-open]'); if (o) { BUILT[o.dataset.open][1](); return; }
      const c = e.target.closest('[data-cli]'); if (c) { ui.runCli([c.dataset.cli]); return; }
      const s = e.target.closest('[data-sub]'); if (s) { ui.runCli(s.dataset.sub.split(' ')); return; }
      if (e.target.closest('[data-run]')) ui.runCli(['sessions', 'prune']);
    });
    draw();
  } });

  /* ---------------------------------------------------------------- doctor */
  const DOC = () => X.doctor || { endpoints: [], mock: { command: 'sleipnir doctor --base-url http://127.0.0.1:8089/v1 --model mock-1 --deep' } };
  reg({ name: 'doctor', title: 'Doctor', mount(sc, root) {
    const eps = DOC().endpoints || [], presets = eps.map((e, i) => ({ id: 'e' + i, label: e.ref, sub: e.where, flags: { model: e.ref } })).concat([{ id: 'mock', label: 'mock-1 (a loopback test server)', sub: 'sleipnir mock on 127.0.0.1:8089: a real probe', flags: { 'base-url': 'http://127.0.0.1:8089/v1', model: 'mock-1', deep: true } }, { id: 'custom', label: 'another endpoint …', sub: 'a base URL and a model', flags: {} }]);
    const st = { p: presets[0], base: '', model: '', deep: true, run: 0, steps: [], findings: [], lines: [], card: null, exit: null, ms: 0, running: false, ids: [] };
    root.innerHTML = '<div class="docv"><section class="panel dv-form"><div class="ph"><h2>Probe an endpoint</h2><div class="r"><span class="mono">sleipnir doctor</span></div></div><div class="setcard"><p class="stubnote" style="margin:0 0 8px">Sends a few small requests and reports what the endpoint really does: streaming, tool calls, usage and cost, <b>prefix-cache behaviour</b> and whether it needs warming up. It costs a few cents.</p><div class="fld"><label for="dEp">endpoint</label><div class="fc"><select id="dEp">' + presets.map(p => '<option value="' + p.id + '">' + esc(p.label) + '</option>').join('') + '</select><small class="hint dsub"></small></div></div><div class="fld" data-c hidden><label for="dBase">--base-url</label><div class="fc"><input id="dBase" type="text" placeholder="http://127.0.0.1:8000/v1" autocomplete="off"></div></div><div class="fld" data-c hidden><label for="dModel">--model</label><div class="fc"><input id="dModel" type="text" placeholder="a model name" autocomplete="off"><small class="hint">required with --base-url</small></div></div><div class="fld"><label>--deep</label><div class="fc"><label class="tgl"><input id="dDeep" type="checkbox" checked><span class="trk" aria-hidden="true"></span><span class="sr">--deep</span></label><small class="hint">the longer probe: cache growth, minimum cached prefix, warm-up</small></div></div><pre class="pre cli" id="dCl"></pre><div class="row2"><button class="btn pri" type="button" id="dRun">Run the probe</button><button class="btn" type="button" id="dCli">Open in the runner</button></div></div></section>' +
      '<section class="panel dv-out"><div class="ph"><h2>Probe</h2><div class="r"><span class="dstat dim">not run yet</span></div></div><div class="dmeter" hidden><i></i></div><div class="dbody"><div class="dsteps" aria-live="polite"><p class="ws-none">Choose an endpoint and run the probe: each step appears as the endpoint answers.</p></div><div class="dres"></div></div></section></div>';
    const q = s => $(s, root), sel = q('#dEp'), cl = q('#dCl');
    const flags = () => st.p.id === 'custom' ? Object.assign({}, st.base ? { 'base-url': st.base } : {}, st.model ? { model: st.model } : {}, st.deep ? { deep: true } : {}) : Object.assign({}, st.p.flags, st.p.id === 'mock' ? {} : (st.deep ? { deep: true } : {}));
    const line = () => 'sleipnir doctor' + Object.keys(flags()).map(k => flags()[k] === true ? ' --' + k : ' --' + k + ' ' + flags()[k]).join('');
    function form() { q('.dsub').textContent = st.p.sub || ''; $$('[data-c]', root).forEach(e => { e.hidden = st.p.id !== 'custom'; }); cl.textContent = line(); }
    function parse(lines) {
      const steps = [], kv = []; let grp = ''; const warn = [];
      lines.forEach(l => { const t = l.t; let m;
        if (l.k === 'dim' && (m = /^\s+([✓✗])\s+(\S+)\s+(\d+)ms\s*(.*)$/.exec(t))) { const o = {}; (m[4] || '').split(/\s+/).forEach(p => { const e = p.split('='); if (e.length === 2) o[e[0]] = e[1]; }); steps.push({ ok: m[1] === '✓', name: m[2], ms: +m[3], grp, in: +o.in || 0, cached: +o.cached || 0, out: +o.out || 0 }); }
        else if (l.k === 'dim' && /^\s+!/.test(t)) warn.push(t.replace(/^\s+!\s*/, ''));
        else if (l.k === 'dim' && /^\S/.test(t)) grp = t.trim();
        else if (l.k === 'out' && (m = /^\s{2}(\S.*?)\s{2,}(\S.*)$/.exec(t))) kv.push([m[1], m[2]]); });
      return { steps, kv, warn };
    }
    function stop() { st.ids.forEach(id => sc.clear(id)); st.ids = []; st.running = false; q('#dRun').disabled = false; }
    function run() {
      stop(); const f = flags(); if (st.p.id === 'custom' && (!f['base-url'] || !f.model)) { ui.toast('a custom endpoint needs --base-url and --model', 'warm'); return; }
      const fn = D.output(['doctor']); let r; try { r = fn ? fn(f, D.ctx()) : null; } catch (e) { r = null; }
      if (!r) r = { lines: [{ k: 'bad', t: 'sleipnir: doctor: no sample probe is built in without the data pack' }], exit: 1, ms: 5 };
      const P = parse(r.lines), steps = q('.dsteps'), body = q('.dbody'), res = q('.dres'), meter = q('.dmeter'); st.running = true; q('#dRun').disabled = true; steps.innerHTML = ''; res.innerHTML = ''; meter.hidden = false; $('i', meter).style.width = '0%'; q('.dstat').innerHTML = '<span class="warm">probing …</span>';
      const head = r.lines.find(l => l.k === 'err' || l.k === 'bad'); if (head) steps.appendChild(mk('div', { class: 'dline ' + (r.exit ? 'bad' : 'dim') }, esc(head.t)));
      const per = Math.max(35, Math.min(110, 2300 / Math.max(1, P.steps.length))); let lastGrp = '';
      P.steps.forEach((s, i) => st.ids.push(sc.timeout(() => {
        if (s.grp !== lastGrp) { lastGrp = s.grp; steps.appendChild(mk('div', { class: 'dgrp' }, esc(s.grp))); }
        const hit = s.in ? Math.round(s.cached / s.in * 100) : 0, isCache = /cache|warm|minp/.test(s.name);
        steps.appendChild(mk('div', { class: 'dstep ' + (s.ok ? 'ok' : 'bad') }, '<span class="g">' + (s.ok ? '✓' : '✗') + '</span><b class="mono">' + esc(s.name) + '</b><span class="num ms">' + s.ms + ' ms</span><span class="num io">in ' + fmtN(s.in) + (s.cached ? ' · cached ' + fmtN(s.cached) : '') + ' · out ' + s.out + '</span>' + (isCache ? '<span class="hbar2" title="' + hit + '% of the prompt read from the cache"><i style="width:' + hit + '%"></i></span><span class="num hp">' + hit + '%</span>' : '')));
        body.scrollTop = body.scrollHeight; $('i', meter).style.width = Math.round((i + 1) / P.steps.length * 100) + '%';
      }, 80 + i * per)));
      st.ids.push(sc.timeout(() => {
        st.running = false; q('#dRun').disabled = false; meter.hidden = true; const ok = r.exit === 0;
        P.warn.forEach(w => steps.appendChild(mk('div', { class: 'dline warm' }, '! ' + esc(w))));
        if (!ok) { r.lines.filter(l => l.k === 'bad').forEach(l => steps.appendChild(mk('div', { class: 'dline bad' }, esc(l.t)))); }
        q('.dstat').innerHTML = '<span class="' + (ok ? 'ok' : 'err') + '">' + (ok ? '✓ done' : '✗ exit ' + r.exit) + '</span><span>' + (r.ms < 1000 ? r.ms + ' ms' : (r.ms / 1000).toFixed(1) + ' s') + '</span>';
        const good = v => /^yes/i.test(v), tone = v => good(v) ? 'ok' : /^NO/.test(v) ? 'err' : '';
        body.scrollTop = body.scrollHeight;
        res.innerHTML = ok && P.kv.length ? '<div class="dverd"><h3>What this endpoint does</h3><dl class="dkv">' + P.kv.map(([k, v]) => '<dt>' + esc(k) + '</dt><dd class="' + tone(v) + '">' + (good(v) ? '✓ ' : /^NO/.test(v) ? '✗ ' : '') + esc(v) + '</dd>').join('') + '</dl>' + (r.card ? '<div class="dsum">' + r.card.rows.map(rw => '<span><small>' + esc(rw[0]) + '</small><b>' + esc(rw[1]) + '</b></span>').join('') + '</div>' : '') + '</div>' : (ok ? '' : '<div class="dverd bad"><h3>The probe could not run</h3><p>' + esc((r.lines.find(l => l.k === 'bad') || { t: '' }).t) + '</p></div>');
        G.runs = G.runs || []; G.runs.unshift({ path: ['doctor'], flags: f, cmd: line(), exit: r.exit, ms: r.ms }); G.runs.length = Math.min(G.runs.length, 8);
      }, 160 + P.steps.length * per));
    }
    sc.listen(sel, 'change', () => { st.p = presets.find(p => p.id === sel.value); form(); });
    sc.listen(root, 'input', e => { const t = e.target; if (t.id === 'dBase') st.base = t.value.trim(); else if (t.id === 'dModel') st.model = t.value.trim(); cl.textContent = line(); });
    sc.listen(root, 'change', e => { if (e.target.id === 'dDeep') { st.deep = e.target.checked; cl.textContent = line(); } });
    sc.listen(q('#dRun'), 'click', run); sc.listen(q('#dCli'), 'click', () => ui.runCli(['doctor'], flags(), false));
    form();
  } });

  /* ---------------------------------------------------------------- schedule */
  const SCH = () => G.sched || (G.sched = (function () {
    const S0 = X.schedule; if (S0 && S0.jobs) return U.deepCopy({ jobs: S0.jobs, daemon: S0.daemon, logs: S0.logs, n: S0.jobs.length });
    return { jobs: D.schedule.map((j, i) => ({ id: 'j' + (i + 1), cron: j.cron, goal: j.goal, dir: j.cwd, model: j.model, mode: j.mode, budgetUsd: j.budget, lastRun: '', lastExit: j.status, next: '' })), daemon: { running: true, pid: 20417, every: '30s', line: 'sleipnir daemon: looking for due jobs every 30s' }, logs: [], n: D.schedule.length };
  })());
  const NOW = '2026-01-02T03:04:43';
  const hm = iso => iso ? iso.slice(5, 10).replace('-', '/') + ' ' + iso.slice(11, 16) : '–';
  reg({ name: 'schedule', title: 'Schedule', mount(sc, root) {
    const st = { log: null, run: null, ids: [] }, S = SCH(), projects = (X.projects || [{ root: '~/projects/shop' }, { root: '~/projects/orders-api' }]).map(p => p.root);
    root.innerHTML = '<div class="schv"><header class="sethead"><h1>Schedule</h1><p>Cron jobs the daemon starts as headless runs: nobody can answer, so an action that needs approval is refused. <span class="mono">sleipnir schedule add | rm</span> · <span class="mono">sleipnir daemon</span></p></header>' +
      '<section class="panel"><div class="ph"><h2>Daemon</h2><div class="r dmn"></div></div></section><section class="panel"><div class="ph"><h2>Jobs</h2><div class="r jn"></div></div><div class="setcard jobs"></div></section>' +
      '<div class="g2s"><section class="panel"><div class="ph"><h2>Add a job</h2><div class="r"><span class="mono">schedule add</span></div></div><div class="setcard"><div class="fld"><label for="sCron">cron</label><div class="fc"><input id="sCron" type="text" value="0 9 * * 1-5" autocomplete="off" spellcheck="false"><small class="hint" id="sNext"></small></div></div><div class="fld"><label for="sGoal">goal</label><div class="fc"><textarea id="sGoal" rows="2" placeholder="what the run should do, judged on evidence"></textarea></div></div><div class="fld"><label for="sDir">--cwd</label><div class="fc"><select id="sDir">' + projects.map(p => '<option>' + esc(p) + '</option>').join('') + '</select></div></div><div class="fld"><label for="sMode">--mode</label><div class="fc"><select id="sMode"><option value="">default</option><option>accept-edits</option><option>plan</option></select><small class="hint">bypass and yolo cannot be scheduled from here</small></div></div><div class="fld"><label for="sBud">--budget-usd</label><div class="fc"><input id="sBud" type="text" value="1" inputmode="decimal"></div></div><div class="row2"><button class="btn pri" type="button" id="sAdd">Add the job</button></div></div></section>' +
      '<section class="panel"><div class="ph"><h2>Log</h2><div class="r lgh"></div></div><div class="slog"><div class="term" tabindex="0"><div class="tl dim">Pick a job’s log, or run one now.</div></div></div></section></div></div>';
    const q = s => $(s, root);
    function nextOf(j) { const n = X.cronNext ? X.cronNext(j.cron, NOW) : null; return n ? hm(n) : '–'; }
    function draw() {
      q('.dmn').innerHTML = '<span class="' + (S.daemon.running ? 'ok' : 'warm') + '">' + (S.daemon.running ? '● running' : '◌ stopped') + '</span>' + (S.daemon.running ? '<span class="dim">pid ' + S.daemon.pid + ' · every ' + esc(S.daemon.every) + ' · ' + esc(S.daemon.timeout || '1h0m0s per run') + '</span>' : '') + '<button class="btn sm" type="button" data-dm>' + (S.daemon.running ? 'Stop the daemon' : 'Start the daemon') + '</button><button class="btn sm" type="button" data-once title="sleipnir daemon --once: start what is due now, wait, exit">Run what is due (--once)</button>';
      q('.jn').textContent = S.jobs.length + ' job' + (S.jobs.length === 1 ? '' : 's');
      q('.jobs').innerHTML = S.jobs.length ? '<table class="tbl"><thead><tr><th>id</th><th>cron</th><th>goal</th><th>where</th><th>last run</th><th>next</th><th></th></tr></thead><tbody>' + S.jobs.map(j => { const bad = j.lastExit && j.lastExit !== 'ok' && j.lastExit !== '–' && j.lastExit !== ''; return '<tr class="' + (st.log === j.id ? 'sel' : '') + '"><td class="mono bright">' + esc(j.id) + '</td><td class="mono">' + esc(j.cron) + '</td><td class="bright jg">' + esc(j.goal) + '</td><td class="dim">' + esc(j.dir || j.cwd) + '<small class="rn">' + esc((j.model || 'default model') + ' · ' + (j.mode || 'default') + ' · $' + j.budgetUsd) + '</small></td><td>' + (j.lastRun ? esc(hm(j.lastRun)) : '–') + ' <span class="' + (bad ? 'err' : 'ok') + '">' + (j.lastExit ? (bad ? '✗ ' : '✓ ') + esc(String(j.lastExit).slice(0, 30)) : '') + '</span></td><td class="num">' + nextOf(j) + '</td><td class="r nowrap"><button class="btn sm" type="button" data-run="' + esc(j.id) + '">Run now</button><button class="btn sm" type="button" data-lg="' + esc(j.id) + '">Log</button><button class="btn sm danger" type="button" data-rm="' + esc(j.id) + '">Remove</button></td></tr>'; }).join('') + '</tbody></table>' : '<p class="stubnote" style="margin:12px 0">No job yet: add one on the left of the log.</p>';
      q('.lgh').innerHTML = st.log ? '<span class="mono">' + esc(st.log) + '</span>' : '';
    }
    function showLog(id) {
      st.log = id; const term = $('.term', root), logs = S.logs.filter(l => l.job === id), j = S.jobs.find(x => x.id === id);
      term.innerHTML = '<div class="tl hd"><span class="prompt">$</span> ' + esc(j ? j.log || '~/.sleipnir/schedule-logs/' + id + '-….log' : id) + '</div>' + (logs.length ? logs.slice(0, 2).map(l => '<div class="tl dim">' + esc(l.file) + ' · exit ' + esc(l.exit) + '</div><div class="tl out">' + esc(l.text) + '</div>').join('') : '<div class="tl dim">no log yet for this job</div>'); draw();
    }
    function runNow(id) {
      const j = S.jobs.find(x => x.id === id); if (!j) return; st.ids.forEach(i => sc.clear(i)); st.ids = []; const term = $('.term', root); st.log = id; q('.lgh').innerHTML = '<span class="mono">' + esc(id) + ' · running</span>';
      const cmd = 'sleipnir run --quiet' + (j.model ? ' --model ' + j.model : '') + ' --cwd ' + (j.dir || j.cwd) + (j.mode ? ' --mode ' + j.mode : '') + ' --budget-usd ' + j.budgetUsd + ' -- ' + JSON.stringify(j.goal);
      const lines = [['hd', cmd], ['dim', 'headless: an action that needs approval is refused (no --ask-timeout set: the daemon passes none)'], ['dim', 'swarm: single agent, isolation none'], ['out', 'reading the repository …'], ['out', 'checking: ' + j.goal], ['ok', 'done · 6 steps · $0.0' + (310 + id.length * 7) + ' · cache hit 86% · no file changed']];
      term.innerHTML = ''; lines.forEach((l, i) => st.ids.push(sc.timeout(() => { term.appendChild(mk('div', { class: 'tl ' + l[0] }, (l[0] === 'hd' ? '<span class="prompt">$</span> ' : '') + esc(l[1]))); term.scrollTop = term.scrollHeight; }, 100 + i * 380)));
      st.ids.push(sc.timeout(() => { j.lastRun = NOW; j.lastExit = 'ok'; S.logs.unshift({ job: id, file: '~/.sleipnir/schedule-logs/' + id + '-20260102-030443.log', exit: 'ok', text: 'Checked: ' + j.goal + '\n── 6 steps · $0.0' + (310 + id.length * 7) + ' · cache hit 86% · 0 compactions · no file was changed' }); draw(); ui.toast('job ' + id + ' ran: exit ok', 'ok'); }, 140 + lines.length * 380));
    }
    function nextHint() { const v = $('#sCron', root).value, n = X.cronNext ? X.cronNext(v, NOW) : null; $('#sNext', root).innerHTML = n ? '<span class="ok">✓ next run ' + esc(hm(n)) + '</span>' : '<span class="err">✗ not a cron expression: five fields (minute hour day month weekday) or @hourly @daily @weekly</span>'; return !!n; }
    sc.listen(root, 'click', e => {
      const t = e.target;
      if (t.closest('[data-dm]')) { S.daemon.running = !S.daemon.running; ui.toast(S.daemon.running ? 'daemon started' : 'daemon stopped: no job starts until it runs again', S.daemon.running ? 'ok' : 'warm'); draw(); return; }
      if (t.closest('[data-once]')) { const due = S.jobs.filter(j => X.cronNext && X.cronNext(j.cron, '2026-01-02T03:04:00') && X.cronNext(j.cron, '2026-01-02T03:04:00').slice(0, 16) <= '2026-01-02T03:04'); ui.toast(due.length ? 'ran ' + due.length + ' due job' + (due.length === 1 ? '' : 's') : 'nothing is due now (the next job starts at ' + (S.jobs.map(nextOf).sort()[0] || '–') + ')', due.length ? 'ok' : 'quiet'); return; }
      const r = t.closest('[data-run]'); if (r) { runNow(r.dataset.run); return; } const l = t.closest('[data-lg]'); if (l) { showLog(l.dataset.lg); return; }
      const rm = t.closest('[data-rm]'); if (rm) { const j = S.jobs.find(x => x.id === rm.dataset.rm); ui.confirm({ title: 'Remove the job', text: 'Remove <b>' + esc(j.id) + '</b> (' + esc(j.goal) + ')? Its past logs stay on disk.', ok: 'Remove it', danger: true, run: () => { S.jobs = S.jobs.filter(x => x.id !== j.id); if (st.log === j.id) st.log = null; draw(); ui.toast('removed ' + j.id); } }); return; }
      if (t.closest('#sAdd')) { const cron = $('#sCron', root).value.trim(), goal = $('#sGoal', root).value.trim(), bud = parseFloat($('#sBud', root).value); if (!nextHint()) { ui.toast('the cron expression is not valid', 'err'); return; } if (!goal) { ui.toast('a goal is required', 'warm'); $('#sGoal', root).focus(); return; } if (!(bud > 0)) { ui.toast('the budget is a number of dollars', 'err'); return; }
        S.n++; const j = { id: 'j' + S.n, cron, goal, dir: $('#sDir', root).value, model: '', mode: $('#sMode', root).value, budgetUsd: bud, created: NOW, lastRun: '', lastExit: '' }; S.jobs.push(j); $('#sGoal', root).value = ''; draw(); ui.toast('added ' + j.id + ': next run ' + nextOf(j), 'ok'); }
    });
    sc.listen(root, 'input', e => { if (e.target.id === 'sCron') nextHint(); });
    draw(); nextHint(); if (S.logs.length) showLog(S.logs[0].job);
  } });
})(SL);
