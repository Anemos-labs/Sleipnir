/* 98b-ui-tools.js: Tools. A catalogue of every `sleipnir` command as a card, in four groups; a card opens its own panel where there is one
 * (Sessions, Doctor, Schedule, and the Settings pages for models, trust, mcp, config and login) or the generic runner (a form from the real flags,
 * the command line, a streaming output pane and a result card). Doctor and Schedule are purpose-built views registered here.
 *
 * Doctor probes a real endpoint and Schedule edits the job list of this machine: both talk to the server, show what it answers, and ask for the
 * confirmation the server asks for. Output text from a probe, a job log or a file is data: it is escaped and never read as markup. */
(function (SL) {
  'use strict';
  const U = SL.u, { $, $$, esc, mk, fmtN } = U, D = SL.D, G = SL.G, ui = SL.ui = SL.ui || {}, TK = SL.toolkit = SL.toolkit || {};
  const X = () => D.extra || (D.extra = {}), net = TK.net;
  const reg = SL.views.register, arr = x => Array.isArray(x) ? x : [];
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
  const cmdsAll = () => (D.spec && D.spec.commands) || [];
  const top = () => cmdsAll().filter(c => c.path.length === 1), subsOf = n => cmdsAll().filter(c => c.path.length > 1 && c.path[0] === n);

  /* ---------------------------------------------------------------- the catalogue */
  reg({ name: 'tools', title: 'Tools', mount(sc, root) {
    const st = { q: '' };
    root.innerHTML = '<div class="toolsv"><header class="sethead"><h1>Tools</h1><p>Every <span class="mono">sleipnir</span> command, ' + cmdsAll().length + ' in all. Open a purpose-built panel where there is one; the runner builds a form from the real flags for the rest, shows the command line and streams the output.</p></header>' +
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
      host.innerHTML = h || (cmdsAll().length ? '<p class="stubnote" style="padding:14px 4px">No command matches “' + esc(st.q) + '”.</p>' : '<p class="stubnote" style="padding:14px 4px">The command list is not loaded yet.</p>');
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
    sc.update(() => { const n = cmdsAll().length; if (n !== st.n) { st.n = n; draw(); } });
    draw(); if (SL.data) SL.data.forPage('tools', { loud: true });
  } });

  /* ---------------------------------------------------------------- doctor: pure parts */
  /** One step of the probe as a row of the steps pane. */
  function stepHtml(s, showGroup) {
    const hit = s.in ? Math.round(s.cached / s.in * 100) : 0, isCache = /cache|warm|minp/.test(String(s.name));
    return (showGroup ? '<div class="dgrp">' + esc(s.grp) + '</div>' : '') + '<div class="dstep ' + (s.ok ? 'ok' : 'bad') + '"><span class="g">' + (s.ok ? '✓' : '✗') + '</span><b class="mono">' + esc(s.name) + '</b><span class="num ms">' + esc(s.ms) + ' ms</span><span class="num io">in ' + fmtN(Number(s.in) || 0) + (s.cached ? ' · cached ' + fmtN(Number(s.cached) || 0) : '') + ' · out ' + esc(s.out) + '</span>' + (isCache ? '<span class="hbar2" title="' + hit + '% of the prompt read from the cache"><i style="width:' + hit + '%"></i></span><span class="num hp">' + hit + '%</span>' : '') + '</div>';
  }
  /** "What this endpoint does": the verdict's pairs with their tone, and the summary card. */
  function verdictHtml(v) {
    const good = x => /^yes/i.test(x), tone = x => good(x) ? 'ok' : /^NO/.test(x) ? 'err' : '';
    return '<div class="dverd"><h3>What this endpoint does</h3><dl class="dkv">' + arr(v.kv).map(([k, x]) => '<dt>' + esc(k) + '</dt><dd class="' + tone(String(x)) + '">' + (good(String(x)) ? '✓ ' : /^NO/.test(String(x)) ? '✗ ' : '') + esc(x) + '</dd>').join('') + '</dl>' + (v.card ? '<div class="dsum">' + arr(v.card.rows).map(rw => '<span><small>' + esc(rw[0]) + '</small><b>' + esc(rw[1]) + '</b></span>').join('') + '</div>' : '') + '</div>';
  }
  const lineHtml = l => { const k = l.k === 'err' || l.k === 'bad' ? 'bad' : l.k === 'warn' ? 'warm' : 'dim'; return '<div class="dline ' + k + '">' + esc(l.t) + '</div>'; };
  /** The request a preset or a custom endpoint makes: the model, the base URL (custom, or the loopback test server) and --deep. */
  function doctorRequest(p, f) {
    const body = {}; const flags = f || {};
    if (flags.model) body.model = flags.model; if (flags['base-url']) body.baseUrl = flags['base-url']; body.deep = !!flags.deep;
    return body;
  }
  TK.doctor = { stepHtml, verdictHtml, lineHtml, doctorRequest };

  /* ---------------------------------------------------------------- doctor */
  reg({ name: 'doctor', title: 'Doctor', mount(sc, root) {
    const presetsOf = eps => eps.map((e, i) => ({ id: 'e' + i, label: e.ref, sub: e.where, flags: { model: e.ref } })).concat([{ id: 'mock', label: 'mock-1 (a loopback test server)', sub: 'sleipnir mock on 127.0.0.1:8089: a real probe', flags: { 'base-url': 'http://127.0.0.1:8089/v1', model: 'mock-1', deep: true } }, { id: 'custom', label: 'another endpoint …', sub: 'a base URL and a model', flags: {} }]);
    let presets = presetsOf(arr(X().doctor && X().doctor.endpoints));
    const st = { p: presets[0], base: '', model: '', deep: true, steps: 0, lastGrp: '' };
    root.innerHTML = '<div class="docv"><section class="panel dv-form"><div class="ph"><h2>Probe an endpoint</h2><div class="r"><span class="mono">sleipnir doctor</span></div></div><div class="setcard"><p class="stubnote" style="margin:0 0 8px">Sends a few small requests and reports what the endpoint really does: streaming, tool calls, usage and cost, <b>prefix-cache behaviour</b> and whether it needs warming up. It costs a few cents.</p><div class="fld"><label for="dEp">endpoint</label><div class="fc"><select id="dEp"></select><small class="hint dsub"></small></div></div><div class="fld" data-c hidden><label for="dBase">--base-url</label><div class="fc"><input id="dBase" type="text" placeholder="http://127.0.0.1:8000/v1" autocomplete="off"></div></div><div class="fld" data-c hidden><label for="dModel">--model</label><div class="fc"><input id="dModel" type="text" placeholder="a model name" autocomplete="off"><small class="hint">required with --base-url</small></div></div><div class="fld"><label>--deep</label><div class="fc"><label class="tgl"><input id="dDeep" type="checkbox" checked><span class="trk" aria-hidden="true"></span><span class="sr">--deep</span></label><small class="hint">the longer probe: cache growth, minimum cached prefix, warm-up</small></div></div><pre class="pre cli" id="dCl"></pre><div class="row2"><button class="btn pri" type="button" id="dRun">Run the probe</button><button class="btn" type="button" id="dCli">Open in the runner</button></div></div></section>' +
      '<section class="panel dv-out"><div class="ph"><h2>Probe</h2><div class="r"><span class="dstat dim">not run yet</span></div></div><div class="dmeter" hidden><i></i></div><div class="dbody"><div class="dsteps" aria-live="polite"><p class="ws-none">Choose an endpoint and run the probe: each step appears as the endpoint answers.</p></div><div class="dres"></div></div></section></div>';
    const q = s => $(s, root), sel = q('#dEp'), cl = q('#dCl');
    const flags = () => st.p.id === 'custom' ? Object.assign({}, st.base ? { 'base-url': st.base } : {}, st.model ? { model: st.model } : {}, st.deep ? { deep: true } : {}) : Object.assign({}, st.p.flags, st.p.id === 'mock' ? {} : (st.deep ? { deep: true } : {}));
    const line = () => 'sleipnir doctor' + Object.keys(flags()).map(k => flags()[k] === true ? ' --' + k : ' --' + k + ' ' + flags()[k]).join('');
    function drawEps() { const cur = st.p && st.p.id; sel.innerHTML = presets.map(p => '<option value="' + p.id + '">' + esc(p.label) + '</option>').join(''); st.p = presets.find(p => p.id === cur) || presets[0]; sel.value = st.p.id; form(); }
    function form() { q('.dsub').textContent = st.p.sub || ''; $$('[data-c]', root).forEach(e => { e.hidden = st.p.id !== 'custom'; }); cl.textContent = line(); }
    const steps = q('.dsteps'), body = q('.dbody'), res = q('.dres'), meter = q('.dmeter'), stat = q('.dstat'), runBtn = q('#dRun');
    let verdict = null;
    const ctl = SL.runner.createRun({
      post: (req, opts) => net.post('/api/doctor', req, opts), cancel: id => TK.cancelRun(id),
      onLine: l => { steps.insertAdjacentHTML('beforeend', lineHtml(l)); body.scrollTop = body.scrollHeight; },
      onStep: s => {
        const grp = s.grp !== st.lastGrp; if (grp) st.lastGrp = s.grp; st.steps++;
        steps.insertAdjacentHTML('beforeend', stepHtml(s, grp && s.grp)); body.scrollTop = body.scrollHeight; $('i', meter).style.width = Math.round(100 * (1 - 1 / (1 + st.steps * 0.35))) + '%';
      },
      onVerdict: v => { verdict = v; arr(v.warn).forEach(w => steps.insertAdjacentHTML('beforeend', '<div class="dline warm">! ' + esc(w) + '</div>')); },
      onResult: r => {
        if (!sc.alive) return; runBtn.disabled = false; meter.hidden = true; const ok = r.exit === 0 && !r.canceled, ms = Number(r.ms) || 0;
        stat.innerHTML = r.canceled ? '<span class="warm">canceled</span>' : '<span class="' + (ok ? 'ok' : 'err') + '">' + (ok ? '✓ done' : '✗ exit ' + esc(r.exit)) + '</span><span>' + (ms < 1000 ? ms + ' ms' : (ms / 1000).toFixed(1) + ' s') + '</span>';
        if (verdict && ok) res.innerHTML = verdictHtml(Object.assign({}, verdict, { card: verdict.card || r.card }));
        else if (!ok && !r.canceled) res.innerHTML = '<div class="dverd bad"><h3>The probe could not run</h3><p>' + esc(lastBad || 'the probe failed; see the lines above') + '</p></div>';
        body.scrollTop = body.scrollHeight; if (TK.loadRuns) TK.loadRuns();
      },
    });
    let lastBad = '';   // the failure message is the last stderr line the probe printed
    sc.on('run', d => { if (d && d.id && d.id === ctl.id && d.lines) d.lines.forEach(l => { if (l.k === 'err' || l.k === 'bad') lastBad = String(l.t); }); ctl.frame(d); });
    sc.onUnmount(() => ctl.leave());   // the probe is stopped when the view goes away
    async function run() {
      if (ctl.open) return; const f = flags();
      if (st.p.id === 'custom' && (!f['base-url'] || !f.model)) { ui.toast('a custom endpoint needs --base-url and --model', 'warm'); return; }
      steps.innerHTML = ''; res.innerHTML = ''; verdict = null; lastBad = ''; st.steps = 0; st.lastGrp = ''; meter.hidden = false; $('i', meter).style.width = '0%'; runBtn.disabled = true; stat.innerHTML = '<span class="warm">probing …</span>';
      const req = doctorRequest(st.p, f), r = await ctl.start(req); if (!sc.alive) return; await after(r, req);
    }
    async function after(r, req) {
      if (r.state === 'started') return;
      if (r.state === 'confirm') {
        let acting = false;
        ui.confirm({ title: 'Probe the endpoint', kicker: 'confirm', text: 'The probe sends a few real requests to <b class="mono">' + esc(req.baseUrl || req.model || 'the endpoint') + '</b> with your key. It costs a few cents.', ok: 'Run the probe', danger: false,
          run: async () => { acting = true; runBtn.disabled = true; stat.innerHTML = '<span class="warm">probing …</span>'; const r2 = await ctl.confirmStart(r.scope); if (sc.alive) await after(r2, req); } });
        const cur = ui.modalState && ui.modalState.cur;
        if (cur) { const prev = cur.onClose; cur.onClose = () => { if (prev) prev(); sc.timeout(() => { if (!acting && ctl.phase === 'confirming') { ctl.declined(); runBtn.disabled = false; meter.hidden = true; stat.innerHTML = '<span class="dim">not run yet</span>'; } }, 0); }; }
        return;
      }
      runBtn.disabled = false; meter.hidden = true;
      if (r.state === 'refused') { stat.innerHTML = '<span class="err">✗ refused</span>'; ui.toast(r.message || 'the probe was refused', 'warm'); return; }
      stat.innerHTML = '<span class="dim">not run yet</span>'; net.fail(r.r, 'the probe could not be started');
    }
    sc.listen(sel, 'change', () => { st.p = presets.find(p => p.id === sel.value); form(); });
    sc.listen(root, 'input', e => { const t = e.target; if (t.id === 'dBase') st.base = t.value.trim(); else if (t.id === 'dModel') st.model = t.value.trim(); cl.textContent = line(); });
    sc.listen(root, 'change', e => { if (e.target.id === 'dDeep') { st.deep = e.target.checked; cl.textContent = line(); } });
    sc.listen(runBtn, 'click', run); sc.listen(q('#dCli'), 'click', () => ui.runCli(['doctor'], flags(), false));
    drawEps(); if (SL.data) SL.data.forPage('doctor', { loud: true });
    // the endpoints are the server's (configured models first); the data layer fills them when the view opens
    sc.on('data', w => { if (w === 'doctor' && st.p && !ctl.open) { presets = presetsOf(arr(X().doctor && X().doctor.endpoints)); drawEps(); } });
  } });

  /* ---------------------------------------------------------------- schedule: pure parts */
  const MON = /^\d{4}-(\d\d)-(\d\d)[T ](\d\d:\d\d)/;
  /** "10/12 07:00" from a server time ("2026-10-12 07:00"); a word like "never" stays as it is, nothing is a dash */
  const hm = iso => { if (!iso) return '–'; const m = MON.exec(String(iso)); return m ? m[1] + '/' + m[2] + ' ' + m[3] : String(iso); };
  /** how a job's last exit reads: {txt, bad, none, run} ("ok" and 0 are a success, "running" a run that is going, "-" and nothing mean it never ran) */
  const exitInfo = x => { const s = x == null ? '' : String(x).trim(); if (s === '' || s === '-') return { none: true, txt: '', bad: false }; if (s === '0' || s === 'ok') return { txt: 'ok', bad: false }; if (s === 'running') return { txt: 'running', bad: false, run: true }; return { txt: /^\d+$/.test(s) ? 'exit ' + s : s.slice(0, 40), bad: true }; };
  /** The daemon line: who runs it and the buttons that make sense. */
  function daemonHtml(d, o) {
    const ext = d.running && d.owner === 'external', every = (o && o.every) || '30s';
    return '<span class="' + (d.running ? 'ok' : 'warm') + '">' + (d.running ? '● running' : '◌ stopped') + '</span>' + (d.running ? '<span class="dim">' + (d.pid ? 'pid ' + esc(d.pid) + ' · ' : '') + 'every ' + esc(d.every) + ' · ' + esc(d.timeout || '1h0m0s per run') + (ext ? ' · another process holds it' : '') + '</span>' : '<label class="mono" for="dmEvery" style="margin-right:4px">every</label><input id="dmEvery" type="text" value="' + esc(every) + '" aria-label="how often the daemon looks for due jobs" style="max-width:70px" autocomplete="off">') +
      '<button class="btn sm" type="button" data-dm' + (ext ? ' disabled title="a daemon outside this page holds the lock: stop it there"' : '') + '>' + (d.running ? 'Stop the daemon' : 'Start the daemon') + '</button><button class="btn sm" type="button" data-once title="sleipnir daemon --once: start what is due now, wait, exit">Run what is due (--once)</button>';
  }
  /** One row of the jobs table. */
  function jobRow(j, o, nextTxt) {
    const ex = exitInfo(j.lastExit), when = j.lastRun && j.lastRun !== 'never' ? esc(hm(j.lastRun)) : (j.lastRun === 'never' ? '<span class="dim">never</span>' : '–');
    return '<tr class="' + (o.log === j.id ? 'sel' : '') + (j.paused ? ' paused' : '') + '"><td class="mono bright">' + esc(j.id) + '</td><td class="mono">' + esc(j.cron) + '</td><td class="bright jg">' + esc(j.goal) + '</td><td class="dim">' + esc(j.dir || j.cwd) + '<small class="rn">' + esc((j.model || 'default model') + ' · ' + (j.mode || 'default') + ' · $' + j.budgetUsd) + '</small></td><td>' + when + ' <span class="' + (ex.bad ? 'err' : ex.run ? 'warm' : 'ok') + '">' + (ex.none ? '' : ex.run ? '● running' : (ex.bad ? '✗ ' : '✓ ') + esc(ex.txt)) + '</span></td><td class="num">' + (j.paused ? '<span class="tag warm" title="the daemon skips it until you resume it">paused</span>' : esc(nextTxt)) + '</td><td class="r nowrap"><button class="btn sm" type="button" data-run="' + esc(j.id) + '"' + (ex.run ? ' disabled title="this job is running now"' : '') + '>Run now</button><button class="btn sm" type="button" data-edit="' + esc(j.id) + '">Edit</button><button class="btn sm" type="button" data-pause="' + esc(j.id) + '" data-to="' + (j.paused ? '0' : '1') + '">' + (j.paused ? 'Resume' : 'Pause') + '</button><button class="btn sm" type="button" data-lg="' + esc(j.id) + '">Log</button><button class="btn sm danger" type="button" data-rm="' + esc(j.id) + '">Remove</button></td></tr>';
  }
  /** What the form asks for, checked the way the CLI checks it: the problems as [{field, text, kind}] (the server checks again). */
  function validateJob(f, cronOk) {
    const bad = [];
    if (!cronOk) bad.push({ field: 'sCron', text: 'the cron expression is not valid', kind: 'err' });
    if (!String(f.goal || '').trim()) bad.push({ field: 'sGoal', text: 'a goal is required', kind: 'warm' });
    if (!(parseFloat(f.budget) >= 0)) bad.push({ field: 'sBudget', text: 'the budget is a number of dollars, 0 for no limit', kind: 'err' });
    return bad;
  }
  /** The body of an add or an edit (bypass and yolo are not offered here, and the server refuses them). */
  const jobRequest = f => ({ cron: String(f.cron).trim(), goal: String(f.goal).trim(), dir: f.dir, model: f.model || '', mode: f.mode || '', budgetUsd: parseFloat(f.budget) });
  TK.schedule = { hm, exitInfo, daemonHtml, jobRow, validateJob, jobRequest };

  /* ---------------------------------------------------------------- schedule */
  const SCH = () => G.sched || (G.sched = { jobs: [], daemon: { running: false, owner: 'none', every: '30s', timeout: '', line: '' }, logs: [], n: 0 });
  const loadSchedule = () => SL.data ? SL.data.load('schedule', { force: true }) : Promise.resolve(false);
  reg({ name: 'schedule', title: 'Schedule', mount(sc, root) {
    const st = { log: null, edit: null, every: '30s', next: new Map(), seq: 0 };
    const projects = () => { const p = arr(X().projects).map(x => x.root || x.dir).filter(Boolean); return p.length ? p : []; };
    const models = () => arr(D.models).map(m => m.ref);
    root.innerHTML = '<div class="schv"><header class="sethead"><h1>Schedule</h1><p>Cron jobs the daemon starts as headless runs: nobody can answer, so an action that needs approval is refused. <span class="mono">sleipnir schedule add | rm</span> · <span class="mono">sleipnir daemon</span></p></header>' +
      '<section class="panel"><div class="ph"><h2>Daemon</h2><div class="r dmn"></div></div></section><section class="panel"><div class="ph"><h2>Jobs</h2><div class="r jn"></div></div><div class="setcard jobs"></div></section>' +
      '<div class="g2s"><section class="panel"><div class="ph"><h2 class="addh">Add a job</h2><div class="r"><span class="mono addm">schedule add</span></div></div><div class="setcard"><div class="fld"><label for="sCron">cron</label><div class="fc"><input id="sCron" type="text" value="0 9 * * 1-5" autocomplete="off" spellcheck="false"><small class="hint" id="sNext"></small></div></div><div class="fld"><label for="sGoal">goal</label><div class="fc"><textarea id="sGoal" rows="2" placeholder="what the run should do, judged on evidence"></textarea></div></div><div class="fld"><label for="sDir">--cwd</label><div class="fc"><select id="sDir"></select></div></div><div class="fld"><label for="sMode">--mode</label><div class="fc"><select id="sMode"><option value="">default</option><option>accept-edits</option><option>plan</option></select><small class="hint">bypass and yolo cannot be scheduled from here</small></div></div><div class="fld"><label for="sModel">--model</label><div class="fc"><select id="sModel"></select></div></div><div class="fld"><label for="sBudget">--budget-usd</label><div class="fc"><input id="sBudget" type="text" value="1" inputmode="decimal"></div></div><div class="row2"><button class="btn pri" type="button" id="sAdd">Add the job</button><button class="btn" type="button" id="sCancel" hidden>Cancel</button></div></div></section>' +
      '<section class="panel"><div class="ph"><h2>Log</h2><div class="r lgh"></div></div><div class="slog"><div class="term" tabindex="0"><div class="tl dim">Pick a job’s log, or run one now.</div></div></div></section></div></div>';
    const q = s => $(s, root), S = () => SCH();
    const nextOf = j => { const n = st.next.get(j.cron); return j.next ? hm(j.next) : n && n.ok ? hm(n.next) : '–'; };
    /** the directory and model lists (the server's projects and catalogue); what is chosen stays chosen, or prefer = {dir, model} */
    function fillSelects(prefer) {
      const dir = $('#sDir', root), want = prefer ? prefer.dir : dir.value, list = projects(); if (want && !list.includes(want)) list.unshift(want);
      const word = d => { const p = arr(X().projects).find(x => (x.dir || x.root) === d), w = p ? (TK.settings && TK.settings.projectState ? TK.settings.projectState(p.trust) : '') : ''; return w ? ' · ' + w : ''; };   // a scheduled run in a project nobody trusted or that could not be read says so
      dir.innerHTML = list.map(p => '<option value="' + esc(p) + '"' + (p === want ? ' selected' : '') + '>' + esc(p) + esc(word(p)) + '</option>').join('') || '<option value="">(no project)</option>';
      const md = $('#sModel', root), mcur = prefer ? prefer.model : md.value, ms = models(); if (mcur && !ms.includes(mcur)) ms.unshift(mcur);
      md.innerHTML = '<option value="">the default model</option>' + ms.map(m => '<option value="' + esc(m) + '"' + (m === mcur ? ' selected' : '') + '>' + esc(m) + '</option>').join('');
    }
    const put = (el, h) => { if (el._h !== h) { el._h = h; el.innerHTML = h; } };   // a redraw of the same markup must not drop the focus or a half-typed value
    function draw() {
      const s = S(); put(q('.dmn'), daemonHtml(s.daemon, { every: st.every }));
      q('.jn').textContent = s.jobs.length + ' job' + (s.jobs.length === 1 ? '' : 's');
      put(q('.jobs'), s.jobs.length ? '<table class="tbl"><thead><tr><th>id</th><th>cron</th><th>goal</th><th>where</th><th>last run</th><th>next</th><th></th></tr></thead><tbody>' + s.jobs.map(j => jobRow(j, st, nextOf(j))).join('') + '</tbody></table>' : '<p class="stubnote" style="margin:12px 0">No job yet: add one on the left of the log.</p>');
      q('.lgh').innerHTML = st.log ? '<span class="mono">' + esc(st.log) + '</span>' : '';
    }
    function setForm(j) {
      st.edit = j || null; const id = j ? j.id : null;
      q('.addh').textContent = j ? 'edit ' + id : 'Add a job'; q('.addm').textContent = j ? 'schedule edit' : 'schedule add'; q('#sAdd').textContent = j ? 'Save ' + id : 'Add the job'; q('#sCancel').hidden = !j;
      $('#sCron', root).value = j ? j.cron : $('#sCron', root).value; if (j) { $('#sGoal', root).value = j.goal; $('#sBudget', root).value = String(j.budgetUsd); $('#sMode', root).value = j.mode || ''; }
      fillSelects(j ? { dir: j.dir || '', model: j.model || '' } : null);
      nextHint();
    }
    function showLogText(j, l) {
      const term = $('.term', root); term.innerHTML = '';
      const hd = mk('div', { class: 'tl hd' }); hd.innerHTML = '<span class="prompt">$</span> '; hd.appendChild(document.createTextNode((l && l.file) || (j && j.log) || id0(j))); term.appendChild(hd);
      if (l && (l.text || l.file)) { const m = mk('div', { class: 'tl dim' }); m.textContent = (l.file || '') + ' · exit ' + (l.exit == null ? '' : l.exit); term.appendChild(m); const t = mk('div', { class: 'tl out' }); t.textContent = l.text || ''; term.appendChild(t); }
      else { const m = mk('div', { class: 'tl dim' }); m.textContent = 'no log yet for this job'; term.appendChild(m); }
    }
    const id0 = j => j ? '~/.sleipnir/schedule-logs/' + j.id + '-….log' : '';
    async function showLog(id) {
      st.log = id; const j = S().jobs.find(x => x.id === id), cached = S().logs.find(l => l.job === id); showLogText(j, cached); draw();
      const r = await net.get('/api/schedule/jobs/' + encodeURIComponent(id) + '/log'); if (!sc.alive || st.log !== id) return; if (r.ok && r.data) showLogText(j, r.data); else if (!net.quiet(r)) net.fail(r, 'the log could not be read');
    }
    /* run now: the job's own output as it prints; the view owns the process */
    let runId = '';
    const term = () => $('.term', root);
    const tracker = TK.tracker({
      line: l => { const t = term(), e = mk('div', { class: 'tl ' + (['ok', 'warn', 'bad', 'err', 'dim', 'head'].includes(l.k) ? l.k : 'out') }); e.textContent = String(l.t); t.appendChild(e); t.scrollTop = t.scrollHeight; },
      result: r => { if (!sc.alive) return; const j = S().jobs.find(x => x.id === st.log); runId = ''; q('.lgh').innerHTML = st.log ? '<span class="mono">' + esc(st.log) + '</span>' : ''; ui.toast(r.canceled ? 'job ' + (j ? j.id : '') + ' canceled' : 'job ' + (j ? j.id : '') + ' ran: exit ' + (r.exit === 0 ? 'ok' : r.exit), r.exit === 0 && !r.canceled ? 'ok' : 'warm'); loadSchedule().then(() => { if (sc.alive) draw(); }); },
    });
    sc.on('run', d => tracker.frame(d));
    sc.onUnmount(() => { if (runId) TK.cancelRun(runId); });
    async function runNow(id) {
      const j = S().jobs.find(x => x.id === id); if (!j) return; if (runId) { ui.toast('a job is running: wait for it or leave this view', 'warm'); return; }
      st.log = id; q('.lgh').innerHTML = '<span class="mono">' + esc(id) + ' · running</span>'; const t = term(); t.innerHTML = ''; draw();
      tracker.arm(); const r = await net.post('/api/schedule/jobs/' + encodeURIComponent(id) + '/run');
      if (!sc.alive) { if (r.ok && r.data && r.data.id) TK.cancelRun(r.data.id); return; }
      if (!r.ok) { tracker.abort(); q('.lgh').innerHTML = '<span class="mono">' + esc(id) + '</span>'; net.fail(r, 'the job could not be started'); return; }
      runId = r.data.id; const hd = mk('div', { class: 'tl hd' }); hd.innerHTML = '<span class="prompt">$</span> '; hd.appendChild(document.createTextNode(r.data.cmdline || id)); t.insertBefore(hd, t.firstChild); tracker.begin(runId);
    }
    /* the cron hint: the server parses it (debounced), the answer is kept per expression */
    let hintT = 0;
    function hintHtml(c) { return c && c.ok ? '<span class="ok">✓ next run ' + esc(hm(c.next)) + '</span>' : '<span class="err">✗ ' + (c && c.error ? esc(c.error) + ': ' : 'not a cron expression: ') + 'five fields (minute hour day month weekday) or @hourly @daily @weekly</span>'; }
    function nextHint() {
      const v = $('#sCron', root).value.trim(), hint = $('#sNext', root), have = st.next.get(v);
      if (have) { hint.innerHTML = hintHtml(have); return Promise.resolve(have.ok); }
      if (!v) { hint.innerHTML = hintHtml({ ok: false }); return Promise.resolve(false); }
      clearTimeout(hintT); const mine = ++st.seq;
      return new Promise(res => { hintT = sc.timeout(async () => { const r = await net.get('/api/schedule/next?cron=' + encodeURIComponent(v)); if (r.ok && r.data) { st.next.set(v, r.data); if (mine === st.seq && $('#sCron', root).value.trim() === v) hint.innerHTML = hintHtml(r.data); res(!!r.data.ok); } else { res(false); } }, 150); });
    }
    async function submit() {
      const f = { cron: $('#sCron', root).value, goal: $('#sGoal', root).value, dir: $('#sDir', root).value, model: $('#sModel', root).value, mode: $('#sMode', root).value, budget: $('#sBudget', root).value };
      const ok = await nextHint(), bad = validateJob(f, ok); if (bad.length) { ui.toast(bad[0].text, bad[0].kind); const e = document.getElementById(bad[0].field); if (e) e.focus(); return; }
      const body = jobRequest(f), editing = st.edit, btn = q('#sAdd'); btn.disabled = true;
      const r = editing ? await net.put('/api/schedule/jobs/' + encodeURIComponent(editing.id), body, { confirm: 'job.edit:' + editing.id }) : await net.post('/api/schedule/jobs', body, { confirm: 'job.add' });
      btn.disabled = false; if (!sc.alive) return;
      if (!r.ok) { net.fail(r, 'the job could not be saved'); return; }
      const j = r.data || {}; $('#sGoal', root).value = ''; if (editing) setForm(null);
      await loadSchedule(); draw(); ui.toast(editing ? 'saved ' + editing.id + ': next run ' + hm(j.next) : 'added ' + (j.id || 'the job') + ': next run ' + hm(j.next), 'ok');
    }
    async function daemon(action) {
      const body = { action }; if (action === 'start') { const e = $('#dmEvery', root); body.every = (e && e.value.trim()) || st.every; st.every = body.every; }
      const r = await net.post('/api/schedule/daemon', body); if (!sc.alive) return; if (!r.ok) { net.fail(r, 'the daemon did not answer'); await loadSchedule(); draw(); return; }
      if (r.data) { SCH().daemon = r.data; }
      ui.toast(action === 'start' ? 'daemon started' : action === 'stop' ? 'daemon stopped: no job starts until it runs again' : (r.data && /due|ran|nothing/i.test(r.data.line || '') ? r.data.line : 'ran what is due'), action === 'stop' ? 'warm' : 'ok'); await loadSchedule(); draw();
    }
    sc.listen(root, 'click', e => {
      const t = e.target;
      if (t.closest('[data-dm]')) { daemon(S().daemon.running ? 'stop' : 'start'); return; }
      if (t.closest('[data-once]')) { daemon('once'); return; }
      const r = t.closest('[data-run]'); if (r) { runNow(r.dataset.run); return; } const l = t.closest('[data-lg]'); if (l) { showLog(l.dataset.lg); return; }
      const ed = t.closest('[data-edit]'); if (ed) { const j = S().jobs.find(x => x.id === ed.dataset.edit); if (j) { setForm(j); $('#sGoal', root).focus(); } return; }
      const pz = t.closest('[data-pause]'); if (pz) { (async () => { const id = pz.dataset.pause, on = pz.dataset.to === '1', rr = await net.post('/api/schedule/jobs/' + encodeURIComponent(id) + '/pause', { paused: on }); if (!sc.alive) return; if (!rr.ok) { net.fail(rr, 'the job could not be changed'); return; } ui.toast((on ? 'paused ' : 'resumed ') + id + (on ? ': the daemon skips it' : ''), on ? 'warm' : 'ok'); await loadSchedule(); draw(); })(); return; }
      const rm = t.closest('[data-rm]'); if (rm) { const j = S().jobs.find(x => x.id === rm.dataset.rm); if (!j) return; ui.confirm({ title: 'Remove the job', text: 'Remove <b>' + esc(j.id) + '</b> (' + esc(j.goal) + ')? Its past logs stay on disk.', ok: 'Remove it', danger: true, run: async () => { const rr = await net.del('/api/schedule/jobs/' + encodeURIComponent(j.id)); if (!sc.alive) return; if (!rr.ok) { net.fail(rr, 'the job could not be removed'); return; } if (st.log === j.id) st.log = null; if (st.edit && st.edit.id === j.id) setForm(null); await loadSchedule(); draw(); ui.toast('removed ' + j.id); } }); return; }
      if (t.closest('#sCancel')) { setForm(null); return; }
      if (t.closest('#sAdd')) submit();
    });
    sc.listen(root, 'input', e => { if (e.target.id === 'sCron') nextHint(); else if (e.target.id === 'dmEvery') st.every = e.target.value.trim(); });
    sc.on('models-changed', () => fillSelects());
    sc.on('data', w => { if (w === 'schedule') draw(); else if (w === 'projects' || w === 'models') fillSelects(); });
    fillSelects(); draw(); nextHint(); if (S().logs.length) showLog(S().logs[0].job); if (SL.data) SL.data.forPage('schedule', { loud: true });
  } });
})(SL);
