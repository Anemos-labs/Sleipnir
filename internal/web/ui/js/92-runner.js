/* 92-runner.js: SL.runner, the generic command runner. For every `sleipnir` command of the server's spec (GET /api/cli) it builds a FORM from
 * the real flags (types, choices, defaults, descriptions), shows the equivalent command line live, runs it on the server, and streams a
 * terminal-styled output pane plus a result card. A run is a child process of the server (CONTRACT.md 18): its stdout lines arrive as `out`,
 * its stderr as `err` (D-19), its end as a result.
 *
 * A run belongs to this view (D-05): leaving the view, Reset or choosing another command stops it, unless the person ticked "Keep running
 * when I leave" (A22). A command that changes something asks first: the server answers 428 with the exact scope, the page shows the
 * standard confirm, and only then asks for the single-use id and repeats the request. Output is text: every line goes through esc(). */
(function (SL) {
  'use strict';
  const U = SL.u, { $, $$, esc, mk } = U, D = SL.D, G = SL.G, ui = SL.ui = SL.ui || {}, C3 = SL.c3 = SL.c3 || {};
  const net = C3.net;
  const spec = () => D.spec, cmds = () => (spec() && spec().commands) || [];
  const key = c => c.path.join(' ');
  const find = p => { const k = Array.isArray(p) ? p.join(' ') : String(p).replace(/^sleipnir\s+/, ''); return cmds().find(c => key(c) === k) || null; };
  const KINDS = { out: 1, err: 1, dim: 1, head: 1, ok: 1, warn: 1, bad: 1, hd: 1 };
  const msTxt = ms => ms < 1000 ? Math.round(ms) + ' ms' : (ms / 1000).toFixed(1) + ' s';
  const arr = x => Array.isArray(x) ? x : [];

  /* ---------- the command line from the form state ---------- */
  function cmdline(c, st) {
    const parts = ['sleipnir'].concat(c.path);
    (c.positional || []).forEach(p => { const v = (st.pos || {})[p.name]; if (v) parts.push(/\s/.test(v) ? JSON.stringify(v) : v); });
    c.flags.forEach(f => { const v = (st.flags || {})[f.name]; if (f.arg === 'bool' && f.default === true) { if (v === false) parts.push('--' + f.name + '=false'); return; } if (v == null || v === '' || v === false) return; if (f.arg === 'bool') { parts.push('--' + f.name); return; } if (f.repeatable) { String(v).split(',').map(s => s.trim()).filter(Boolean).forEach(x => parts.push('--' + f.name + ' ' + (/\s/.test(x) ? JSON.stringify(x) : x))); return; } if (String(v) === String(f.default)) return; parts.push('--' + f.name + ' ' + (/\s/.test(String(v)) ? JSON.stringify(String(v)) : v)); });
    return parts.join(' ');
  }
  /** {flag: value} with the typed values (ints parsed, bools true), plus `_` = positional array. */
  function flagValues(c, st) { const o = { _: (c.positional || []).map(p => (st.pos || {})[p.name]).filter(Boolean) }; c.flags.forEach(f => { let v = (st.flags || {})[f.name]; if (v == null || v === '') v = f.default; if (f.arg === 'bool') o[f.name] = !!v && v !== 'false'; else if (f.arg === 'int' || f.arg === 'uint') o[f.name] = v == null ? null : parseInt(v, 10); else if (f.arg === 'float') o[f.name] = v == null ? null : parseFloat(v); else o[f.name] = v == null ? null : f.repeatable ? String(v).split(',').map(s => s.trim()).filter(Boolean) : v; }); return o; }
  /** The request that runs what the form shows: the flags of the command line (typed), the positionals by name, the tab whose directory it runs in. */
  function buildRequest(c, st, tab, keep) {
    const flags = {}, pos = {};
    c.flags.forEach(f => {
      const v = (st.flags || {})[f.name]; if (f.arg === 'bool' && f.default === true) { if (v === false) flags[f.name] = false; return; }   // a flag that is on by default can only be turned off
      if (v == null || v === '' || v === false) return;
      if (f.arg === 'bool') { flags[f.name] = true; return; }
      if (f.repeatable) { const a = String(v).split(',').map(s => s.trim()).filter(Boolean); if (a.length) flags[f.name] = a; return; }
      if (String(v) === String(f.default)) return;
      flags[f.name] = f.arg === 'int' || f.arg === 'uint' ? parseInt(v, 10) : f.arg === 'float' ? parseFloat(v) : String(v);
    });
    (c.positional || []).forEach(p => { const v = (st.pos || {})[p.name]; if (v) pos[p.name] = v; });
    const req = { path: c.path.slice() }; if (Object.keys(pos).length) req.pos = pos; if (Object.keys(flags).length) req.flags = flags; if (tab) req.tab = tab; if (keep) req.keep = true;
    return req;
  }
  /** What is missing or wrong before the form is sent: [{field, text}] (a required argument or flag, a number that is not one). */
  function validate(c, st) {
    const bad = [];
    (c.positional || []).forEach(p => { if (p.required && !((st.pos || {})[p.name] || '').trim()) bad.push({ field: 'rp-' + p.name, text: p.name + ' is required' }); });
    c.flags.forEach(f => {
      const v = (st.flags || {})[f.name], has = !(v == null || v === '' || v === false);
      if (f.required && !has) bad.push({ field: 'rf-' + f.name, text: '--' + f.name + ' is required' });
      if (has && (f.arg === 'int' || f.arg === 'uint') && !/^-?\d+$/.test(String(v).trim())) bad.push({ field: 'rf-' + f.name, text: '--' + f.name + ' needs a whole number' });
      if (has && f.arg === 'uint' && Number(v) < 0) bad.push({ field: 'rf-' + f.name, text: '--' + f.name + ' cannot be negative' });
      if (has && f.arg === 'float' && !isFinite(Number(v))) bad.push({ field: 'rf-' + f.name, text: '--' + f.name + ' needs a number' });
    });
    return bad;
  }
  /** The mode a command runs in with the flags typed so far: the command's own, or the one a flag changes it to (spec `when`). */
  function modeOf(c, st) {
    let m = c.mode || 'run';
    arr(c.when).forEach(w => { const v = (st.flags || {})[w.flag]; if (v == null || v === '' || v === false) return; if (!arr(w.values).length || arr(w.values).includes(String(v))) m = w.mode; });
    return m;
  }

  /* ---------- one run: the state machine ---------- */
  /**
   * The life of a run, apart from the screen. deps: {post(req, opts), cancel(id), onLine, onStep, onVerdict, onResult}. Phases:
   * idle -> starting -> (confirming ->) running -> stopping -> done. start() resolves what happened to the request:
   *   {state: 'started', id, cmdline}   the server runs it; lines and the result arrive as frames
   *   {state: 'confirm', scope, detail} a privileged command: ask the person, then confirmStart(scope)
   *   {state: 'refused', code, message} the server will not run it (a terminal-only command, bad flags, four runs already)
   *   {state: 'error', r}               the request did not go through
   */
  function createRun(deps) {
    const R = { phase: 'idle', id: null, cmdline: '', req: null, seq: 0, keep: false, dead: false };
    const tk = C3.tracker({
      line: l => deps.onLine && deps.onLine(l),
      step: s => deps.onStep && deps.onStep(s),
      verdict: v => deps.onVerdict && deps.onVerdict(v),
      result: r => { R.phase = 'done'; if (deps.onResult) deps.onResult(r); },
    });
    const open = () => R.phase === 'starting' || R.phase === 'confirming' || R.phase === 'running' || R.phase === 'stopping';
    /** the first request is a probe for the confirmation scope: api.js would toast the 428 as a page bug, and here it is the protocol */
    async function quiet(fn) { const cfg = SL.api && SL.api.cfg; if (!cfg) return fn(); const t = cfg.toast; cfg.toast = () => {}; try { return await fn(); } finally { cfg.toast = t; } }
    async function send(req, opts) {
      const my = ++R.seq; R.phase = 'starting'; R.req = req; R.keep = !!req.keep; tk.arm();
      const r = await (opts && opts.confirm ? deps.post(req, opts) : quiet(() => deps.post(req, opts)));
      if (my !== R.seq || R.dead) { if (r.ok && r.data && r.data.id && !(req.keep && !R.dead)) deps.cancel(r.data.id); return { state: 'stale' }; }   // nobody is looking any more: a run that started is stopped (D-05)
      if (r.ok && r.data && r.data.id) { R.id = r.data.id; R.cmdline = r.data.cmdline || ''; R.phase = 'running'; tk.begin(R.id); return { state: 'started', id: R.id, cmdline: R.cmdline }; }
      tk.abort();
      if (r.status === 428 && r.code === 'confirm_required') {
        const scope = r.scope || (r.detail && r.detail.scope) || '';
        if (scope) { R.phase = 'confirming'; return { state: 'confirm', scope, detail: r.detail || {} }; }
      }
      R.phase = 'idle';
      if (r.status === 403 && r.code === 'tty_only' || r.status === 400 || r.status === 409) return { state: 'refused', code: r.code, status: r.status, message: r.message };
      return { state: 'error', r };
    }
    return {
      get phase() { return R.phase; }, get id() { return R.id; }, get cmdline() { return R.cmdline; }, get keep() { return R.keep; }, get open() { return open(); },
      /** send the request; a privileged command answers {state:'confirm'} */
      start(req) { if (open()) return Promise.resolve({ state: 'busy' }); return send(req); },
      /** the person said yes: ask for the id of this scope and repeat the request */
      confirmStart(scope) { if (R.phase !== 'confirming' || !R.req) return Promise.resolve({ state: 'busy' }); return send(R.req, { confirm: scope }); },
      /** the person said no */
      declined() { if (R.phase === 'confirming') R.phase = 'idle'; },
      /** a "run" frame of the stream */
      frame(d) { tk.frame(d); },
      /** stop it: the server ends the process group; its result frame closes the run (canceled) */
      async cancel() {
        if (R.phase !== 'running' || !R.id) return false; R.phase = 'stopping';
        const r = await deps.cancel(R.id);
        if (!r.ok && r.status === 404) { R.phase = 'done'; if (deps.onResult) deps.onResult({ exit: 130, ms: 0, canceled: true }); }
        return true;
      },
      /** the view goes away: a run that is not to be kept is stopped (D-05); a kept one keeps going on the server */
      leave() { R.dead = true; if (R.phase === 'running' && !R.keep && R.id) { R.phase = 'stopping'; deps.cancel(R.id); } },
      /** show a run that is already going (a kept one): its retained output first, then what follows */
      attach(id, info) { R.id = id; R.phase = info && info.running === false ? 'done' : 'running'; R.keep = true; R.cmdline = (info && info.cmd) || ''; tk.arm(); tk.begin(id, { onlyResult: true }); },
      reset() { R.seq++; R.phase = 'idle'; R.id = null; tk.abort(); },
    };
  }

  /* ---------- the command list and the form: pure builders of markup ---------- */
  const GROUPS = c => c.path[0] === 'rl' ? 'rl lab' : ['chat', 'run', 'swarm', 'demo', 'mock'].includes(c.path[0]) ? 'sessions and runs' : ['sessions', 'config', 'init', 'trust', 'mcp', 'login', 'logout'].includes(c.path[0]) ? 'project and account' : ['inspect', 'watch', 'replay', 'doctor', 'models', 'recon', 'sim', 'friction', 'update', 'schedule', 'daemon'].includes(c.path[0]) ? 'tools' : 'other';
  const ORDER = ['sessions and runs', 'project and account', 'tools', 'rl lab', 'other'];
  /** the commands the filter keeps, grouped, spec order inside a group */
  function shownCmds(st) { const words = st.q.toLowerCase().trim().split(/\s+/).filter(Boolean); return cmds().filter(c => words.every(w => (key(c) + ' ' + (c.summary || '')).toLowerCase().includes(w))).map((c, i) => ({ c, i, g: ORDER.indexOf(GROUPS(c)) })).sort((x, y) => x.g - y.g || x.i - y.i).map(x => x.c); }
  function listHtml(st) {
    let g = null, h = '';
    shownCmds(st).forEach(c => { const gr = GROUPS(c); if (gr !== g) { g = gr; h += '<div class="cgrp">' + gr + '</div>'; } h += '<button type="button" role="option" class="rcmd' + (c === st.c ? ' sel' : '') + '" data-k="' + esc(key(c)) + '" aria-selected="' + (c === st.c) + '"><span class="mono">' + esc(key(c)) + '</span><small>' + esc((c.summary || '').slice(0, 70)) + '</small></button>'; });
    return h || '<div class="dim" style="padding:10px">no command matches</div>';
  }
  function inputFor(f, st) {
    const v = st.flags[f.name], idn = esc('rf-' + f.name), ph = f.default != null ? 'default ' + esc(f.default) : (f.defaultNote ? esc(f.defaultNote).slice(0, 40) : '');
    if (f.arg === 'bool') return '<label class="tgl"><input type="checkbox" id="' + idn + '" data-f="' + esc(f.name) + '"' + ((v === undefined ? f.default === true : v) ? ' checked' : '') + '><span class="trk" aria-hidden="true"></span><span class="sr">' + esc(f.name) + '</span></label>';
    if (arr(f.choices).length && !f.repeatable) return '<select id="' + idn + '" data-f="' + esc(f.name) + '" aria-label="' + esc(f.name) + '"><option value="">' + (f.default != null ? 'default ' + esc(f.default) : f.defaultNote ? esc(f.defaultNote).slice(0, 40) : '(not set)') + '</option>' + f.choices.map(o => '<option value="' + esc(o) + '"' + (String(v) === String(o) ? ' selected' : '') + '>' + esc(o) + '</option>').join('') + '</select>';
    if (f.arg === 'int' || f.arg === 'uint' || f.arg === 'float') return '<input type="number" id="' + idn + '" data-f="' + esc(f.name) + '" value="' + esc(v == null ? '' : v) + '" step="' + (f.arg === 'float' ? 'any' : '1') + '" placeholder="' + ph + '" aria-label="' + esc(f.name) + '">';
    return '<input type="text" id="' + idn + '" data-f="' + esc(f.name) + '" value="' + esc(v == null ? '' : v) + '" placeholder="' + (f.repeatable ? 'a, b, c (repeatable)' : ph) + '" aria-label="' + esc(f.name) + '" autocomplete="off" spellcheck="false">';
  }
  /** the form of one command: its summary, usage, arguments and flags (every string of the spec is data) */
  function formHtml(c, st) {
    const note = c.mode === 'tty_only' ? c.why : (c.why && c.mode !== 'priv' ? c.why : '');
    return '<p class="rsum">' + esc(c.summary || '') + '</p><p class="rusage mono">' + esc(c.usage || '') + '</p>' + (note ? '<p class="rn" style="margin:0 0 6px">' + esc(note) + '</p>' : '') + ((c.positional || []).length ? '<div class="rgrp">arguments</div>' + c.positional.map(p => '<div class="frow"><label for="rp-' + esc(p.name) + '" class="mono">' + esc(p.name) + (p.required ? ' <i class="req" title="required">*</i>' : '') + (p.variadic ? '<small class="ty">one or more</small>' : '') + '</label><input type="text" id="rp-' + esc(p.name) + '" data-p="' + esc(p.name) + '" value="' + esc(st.pos[p.name] || '') + '" autocomplete="off" spellcheck="false">' + (p.desc ? '<p class="fdesc">' + esc(p.desc) + '</p>' : '') + '</div>').join('') : '') + (c.flags.length ? '<div class="rgrp">flags</div>' + c.flags.map(f => '<div class="frow' + (f.arg === 'bool' && f.default === true ? (st.flags[f.name] === false ? ' set' : '') : (st.flags[f.name] ? ' set' : '')) + '"><label for="rf-' + esc(f.name) + '" class="mono">--' + esc(f.name) + (f.required ? ' <i class="req" title="required">*</i>' : '') + '<small class="ty">' + (f.arg === 'bool' ? 'on/off' : esc(f.arg)) + (f.repeatable ? ' · repeatable' : '') + '</small></label><div class="fin">' + inputFor(f, st) + '</div><p class="fdesc">' + esc(f.desc || '') + (f.default != null && f.arg !== 'bool' ? ' <span class="dim">(default ' + esc(f.default) + ')</span>' : f.defaultNote ? ' <span class="dim">(default: ' + esc(f.defaultNote) + ')</span>' : '') + '</p></div>').join('') : '<p class="dim" style="padding:0 12px">no flags</p>');
  }

  /* ---------- the view ---------- */
  G.runs = G.runs || [];
  /** the recent runs, newest first, as the data layer keeps them in G.runs: {id, cmd, exit, ms, path, flags, running} */
  const loadRuns = () => SL.data ? SL.data.load('runs', { force: true }) : Promise.resolve(false);

  function mount(sc, root, params) {
    if (!cmds().length) {   // the command list comes with the server's spec: until it is here there is nothing to build a form from
      root.innerHTML = '<div class="runv"><section class="panel rv-list"><div class="ph"><h2>Commands</h2></div><p class="stubnote" style="padding:10px 12px;margin:0">The command list is not loaded yet.</p></section></div>';
      sc.update(() => { if (cmds().length) SL.views.show('runner', params); }); return;
    }
    const st = { c: find(params.path || ['sessions', 'prune']) || cmds()[0], pos: {}, flags: {}, q: '', keep: false, out: null, t0: 0 };
    if (params.flags) Object.assign(st.flags, params.flags);
    if (params.pos) Object.assign(st.pos, params.pos);
    root.innerHTML = '<div class="runv"><section class="panel rv-list"><div class="ph"><h2>Commands</h2><div class="r"><span class="rcnt"></span><button class="btn sm" type="button" data-tools>‹ Tools</button></div></div><div class="pb"><input type="search" class="rq" placeholder="filter: sessions, trust, rl rollout ..." aria-label="Filter commands"><div class="rcmds" role="listbox" aria-label="sleipnir commands"></div></div></section><section class="panel rv-form"><div class="ph"><h2 class="rtitle">Run</h2><div class="r"><span class="rsrc"></span></div></div><div class="rform"></div><div class="rcl"><pre class="clpre cli"></pre><div class="row2"><button class="btn pri rrun" type="button">Run</button><button class="btn rcopy" type="button">Copy</button><button class="btn rreset" type="button">Reset</button><label class="chk rkeep" hidden title="the process keeps going on the server when you leave this view; it stays in the recent runs"><input type="checkbox" class="rkeepin"> Keep running when I leave</label></div></div></section><section class="panel rv-out"><div class="ph"><h2>Output</h2><div class="r rstat"></div></div><div class="term" role="log" aria-label="Command output" tabindex="0"><div class="tl dim">Nothing run yet. The form on the left builds the command line; Run runs it here.</div></div><div class="rcard"></div><div class="rhist"></div></section></div>';
    const list = $('.rcmds', root), form = $('.rform', root), clp = $('.clpre', root), term = $('.term', root), card = $('.rcard', root), stat = $('.rstat', root), runBtn = $('.rrun', root);
    function drawList() { list.innerHTML = listHtml(st); $('.rcnt', root).textContent = shownCmds(st).length + ' of ' + cmds().length; }
    function drawForm() { const c = st.c; $('.rtitle', root).textContent = 'sleipnir ' + key(c); $('.rsrc', root).textContent = (spec() && spec().generatedFrom) || ''; form.innerHTML = formHtml(c, st); drawCl(); }
    function drawCl() {
      clp.textContent = cmdline(st.c, st);
      const m = modeOf(st.c, st), kp = $('.rkeep', root); kp.hidden = !(m === 'net' || m === 'server'); if (kp.hidden) { st.keep = false; $('.rkeepin', root).checked = false; }
      clp.title = m === 'priv' ? 'a privileged command: it asks for a confirmation first' : '';
    }

    /* the output pane: lines are batched and the pane keeps the last 4,000 so that a long run does not weigh on the page */
    const MAXL = 4000; let queue = [], flushT = 0, shown = 0, dropped = 0;
    function flush() {
      flushT = 0; if (!queue.length) return; const q = queue; queue = []; const stick = term.scrollTop + term.clientHeight >= term.scrollHeight - 24;
      const frag = document.createDocumentFragment();
      q.forEach(l => { const k = KINDS[l.k] ? l.k : 'out'; const e = mk('div', { class: 'tl ' + (k === 'hd' ? 'hd' : k) }); e.textContent = String(l.t == null ? '' : l.t); frag.appendChild(e); shown++; });
      term.appendChild(frag);
      while (shown > MAXL) { const f = term.querySelector('.tl:not([data-gap])'); if (!f) break; f.remove(); shown--; dropped++; }
      if (dropped) { let g = term.querySelector('[data-gap]'); if (!g) { g = mk('div', { class: 'tl dim', 'data-gap': '1' }); term.insertBefore(g, term.firstChild); } g.textContent = '… ' + dropped + ' earlier lines are not shown (the server keeps them)'; }
      if (stick) term.scrollTop = term.scrollHeight;
    }
    const addLine = l => { if (l && /^\$ /.test(String(l.t)) && l.k === 'head' && hdLine && String(l.t).slice(2) === hdLine) return; queue.push(l); if (!flushT) flushT = sc.timeout(flush, 40); };
    let hdLine = '';
    function header(text) { hdLine = text; const e = mk('div', { class: 'tl hd' }); e.innerHTML = '<span class="prompt">$</span> '; e.appendChild(document.createTextNode(text)); term.appendChild(e); shown++; }
    function clearOut() { queue = []; clearTimeout(flushT); flushT = 0; shown = 0; dropped = 0; term.innerHTML = ''; card.innerHTML = ''; stat.innerHTML = ''; hdLine = ''; watch = null; }

    /* what the result looks like: the status, the card, the recent runs */
    function showResult(r) {
      flush(); const canceled = !!r.canceled, ok = r.exit === 0 && !canceled, ms = Number(r.ms) || 0;
      stat.innerHTML = canceled ? '<span class="warm">canceled</span><span>' + msTxt(ms) + '</span>' : '<span class="' + (ok ? 'ok' : 'err') + '">exit ' + esc(r.exit) + '</span><span>' + msTxt(ms) + '</span>';
      card.innerHTML = '<div class="rc ' + (ok ? 'ok' : 'bad') + '"><b>' + (ok ? '✓' : '✗') + ' ' + esc((r.card && r.card.title) || (canceled ? key(st.c) + ' (canceled)' : key(st.c))) + '</b><span>exit status ' + esc(r.exit) + '</span><span>elapsed ' + msTxt(ms) + '</span>' + arr(r.card && r.card.rows).map(row => '<span><i class="dim">' + esc(row[0]) + '</i> ' + esc(row[1]) + '</span>').join('') + '</div>' + watchHtml();
      runBtn.disabled = false; loadRuns().then(ok2 => { if (!ok2) { G.runs.unshift({ cmd: cmdline(st.c, st), exit: r.exit, ms, path: st.c.path.slice(), flags: Object.assign({}, st.flags) }); G.runs = G.runs.slice(0, 12); } hist(); }); findWatch();
    }
    /** a `run` or `swarm` writes a session of its own: when one shows up in the recorded list, Watch it opens it read-only (A7) */
    let watch = null;
    const watchHtml = () => watch ? '<div class="row2" style="margin:8px 0 0"><button class="btn sm" type="button" data-watchit title="a read-only tab: the run belongs to another process">Watch it</button> <span class="dim">' + esc(watch.id) + '</span></div>' : '';
    async function findWatch() {
      if (!['run', 'swarm'].includes(st.c.path[0]) || st.c.path.length > 1) return; await C3.refreshRecorded(); const hosted = C3.sessions.hostedSids();
      const cand = arr(SL.sessions.recorded).filter(r => r.lastWritten >= st.t0 - 5000 && !hosted.has(r.id) && (r.locked || C3.sessions.isLive(r))).sort((a, b) => b.lastWritten - a.lastWritten)[0];
      if (cand && sc.alive) { watch = cand; const h = $('[data-watchit]', card); if (!h) card.insertAdjacentHTML('beforeend', watchHtml()); }
    }
    function hist() { $('.rhist', root).innerHTML = G.runs.length ? '<div class="rgrp">recent runs</div>' + G.runs.map((h, i) => '<button type="button" class="hrow" data-h="' + i + '"><span class="' + (h.running ? 'warm' : h.exit === 0 ? 'ok' : 'err') + '">' + (h.running ? '●' : h.exit === 0 ? '✓' : '✗') + '</span><span class="mono">' + esc(h.cmd) + '</span><span class="dim">' + (h.running ? 'running' : h.ms < 1000 ? Math.round(h.ms) + 'ms' : (h.ms / 1000).toFixed(1) + 's') + '</span></button>').join('') : ''; }

    /* the run itself */
    const ctl = createRun({
      post: (req, opts) => net.post('/api/runs', req, opts),
      cancel: id => C3.cancelRun(id),
      onLine: addLine,
      onResult: r => { if (!sc.alive) return; showResult(r); },
    });
    sc.on('run', d => ctl.frame(d));
    sc.on('data', w => { if (w === 'runs') hist(); });
    sc.onUnmount(() => ctl.leave());   // D-05: the process is the view's, unless it was kept
    const setRunning = on => { runBtn.disabled = on; if (on) stat.innerHTML = '<span class="dim">starting…</span>'; };
    const setStopBtn = () => { stat.innerHTML = '<span class="dim">running…</span> <button class="btn sm" type="button" data-stop title="stop the process">Stop</button>'; };
    async function run() {
      if (ctl.open) return;
      const bad = validate(st.c, st); if (bad.length) { ui.toast(bad[0].text, 'warm'); const f = document.getElementById(bad[0].field); if (f) f.focus(); return; }
      const cmd = cmdline(st.c, st), tab = SL.sessions.active && SL.sessions.active.id, req = buildRequest(st.c, st, tab, st.keep && !$('.rkeep', root).hidden);
      clearOut(); st.t0 = Date.now(); header(cmd); setRunning(true);
      const r = await ctl.start(req); if (!sc.alive) return;
      await after(r, cmd);
    }
    async function after(r, cmd) {
      if (r.state === 'started') { setStopBtn(); if (['run', 'swarm'].includes(st.c.path[0]) && st.c.path.length === 1) { const iv = sc.interval(() => { if (!ctl.open) sc.clear(iv); else findWatch(); }, 3000); } if (r.cmdline && r.cmdline !== cmd) { const h = term.querySelector('.tl.hd'); if (h) { hdLine = r.cmdline; h.innerHTML = '<span class="prompt">$</span> '; h.appendChild(document.createTextNode(r.cmdline)); } } return; }
      if (r.state === 'confirm') {
        const shownCmd = (r.detail && r.detail.cmdline) || cmd;
        ui.confirm({ title: 'Run a privileged command', kicker: 'confirm', text: 'This command can change your configuration, trust, schedule or sessions, or run with fewer questions: <b class="mono">' + esc(shownCmd) + '</b>', detail: '<span class="dim">It runs on this machine, as you, in the directory of the active session.</span>', ok: 'Run it', danger: true,
          run: async () => { acting = true; setRunning(true); const r2 = await ctl.confirmStart(r.scope); if (sc.alive) await after(r2, cmd); } });
        // the confirm may be dismissed without an answer (Cancel, esc, a click outside): the form comes back
        let acting = false; const cur = ui.modalState && ui.modalState.cur;
        if (cur) { const prev = cur.onClose; cur.onClose = () => { if (prev) prev(); sc.timeout(() => { if (!acting && ctl.phase === 'confirming') { ctl.declined(); runBtn.disabled = false; stat.innerHTML = ''; term.querySelector('.tl.hd') && addLine({ k: 'dim', t: 'not run: the confirmation was declined' }); } }, 0); }; }
        return;
      }
      runBtn.disabled = false;
      if (r.state === 'refused') { addLine({ k: 'bad', t: r.message || 'refused' }); flush(); showResult({ exit: 2, ms: 0, card: { title: key(st.c), rows: [] } }); return; }
      if (r.state === 'error') { stat.innerHTML = ''; net.fail(r.r, 'the command could not be started'); return; }
      stat.innerHTML = '';
    }
    function reset() { ctl.leave(); ctl.reset(); clearOut(); term.innerHTML = '<div class="tl dim">Nothing run yet.</div>'; runBtn.disabled = false; }

    sc.listen($('[data-tools]', root), 'click', () => SL.views.show('tools'));
    sc.listen($('.rq', root), 'input', e => { st.q = e.target.value; drawList(); });
    sc.listen(list, 'click', e => { const b = e.target.closest('[data-k]'); if (!b) return; reset(); st.c = find(b.dataset.k); st.pos = {}; st.flags = {}; drawList(); drawForm(); });
    sc.listen(form, 'input', e => { const t = e.target; if (t.dataset.p) st.pos[t.dataset.p] = t.value.trim(); else if (t.dataset.f) { const fl = st.c.flags.find(x => x.name === t.dataset.f); st.flags[t.dataset.f] = t.type === 'checkbox' ? t.checked : t.value; t.closest('.frow').classList.toggle('set', fl && fl.arg === 'bool' && fl.default === true ? st.flags[t.dataset.f] === false : !!st.flags[t.dataset.f]); } drawCl(); });
    sc.listen(form, 'change', e => { const t = e.target; if (t.dataset.f && t.tagName === 'SELECT') { st.flags[t.dataset.f] = t.value; t.closest('.frow').classList.toggle('set', !!t.value); drawCl(); } });
    sc.listen(runBtn, 'click', run);
    sc.listen($('.rkeepin', root), 'change', e => { st.keep = e.target.checked; });
    sc.listen($('.rcopy', root), 'click', () => U.copy(cmdline(st.c, st)).then(ok => ui.toast(ok ? 'command copied' : 'copy is not available here', ok ? 'ok' : 'warm')));
    sc.listen($('.rreset', root), 'click', () => { reset(); st.pos = {}; st.flags = {}; drawForm(); });
    sc.listen(root, 'click', e => {
      const stop = e.target.closest('[data-stop]'); if (stop) { stop.disabled = true; ctl.cancel(); return; }
      const w = e.target.closest('[data-watchit]'); if (w && watch) { const open = (SL.act && SL.act.openRecorded) || (SL.sessions && SL.sessions.openRecorded); if (typeof open === 'function') { const x = open.call(SL.act && SL.act.openRecorded ? SL.act : SL.sessions, watch.id, { follow: true }); if (x && x.ok === false) ui.toast(x.why, 'warm'); } else ui.toast('watch it with sleipnir watch ' + watch.id + ' in a terminal', 'warm'); return; }
      const h = e.target.closest('[data-h]'); if (h) { const r = G.runs[+h.dataset.h]; if (!r) return;
        if (r.running && r.id) { reset(); st.c = find(r.path) || st.c; st.flags = Object.assign({}, r.flags); st.pos = {}; drawList(); drawForm(); reattach(r); return; }
        reset(); st.c = find(r.path) || st.c; st.flags = Object.assign({}, r.flags); st.pos = {}; drawList(); drawForm(); } });
    /** a kept run that is still going: show what it printed so far, then what follows (A22) */
    async function reattach(r) {
      clearOut(); header(r.cmd); setRunning(true); ctl.attach(r.id, r); setStopBtn();
      const o = await net.get('/api/runs/' + encodeURIComponent(r.id) + '/output'); if (!sc.alive) return;
      if (o.ok && o.data) { arr(o.data.lines).forEach(addLine); if (o.data.result) showResult(o.data.result); else if (o.data.running === false) showResult({ exit: o.data.exit == null ? 0 : o.data.exit, ms: o.data.ms || 0 }); }
      else net.fail(o, 'the output of that run is not available');
    }
    sc.listen(form, 'keydown', e => { if (e.key === 'Enter' && e.target.tagName === 'INPUT' && e.target.type !== 'checkbox') { e.preventDefault(); run(); } });
    drawList(); drawForm(); hist(); if (SL.data) SL.data.forPage('runner', { loud: true }); if (params.run) run();
  }
  SL.views.register({ name: 'runner', title: 'Run a command', nav: false, mount });
  SL.runner = { find, cmdline, flagValues, buildRequest, validate, modeOf, createRun, spec, loadRuns, formHtml, listHtml, shownCmds };
  C3.loadRuns = loadRuns;
  ui.runCli = (path, flags, run, pos) => SL.views.show('runner', { path, flags, run, pos });
})(SL);
