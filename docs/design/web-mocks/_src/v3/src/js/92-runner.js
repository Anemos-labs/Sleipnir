/* 92-runner.js: SL.runner, the generic command runner. For every `sleipnir` command in the spec (cli-spec.json: generated from docs/CLI.md,
 * or the small built-in spec) it builds a FORM from the real flags (types, defaults, descriptions), shows the equivalent command line live,
 * runs it, and streams a terminal-styled output pane plus a result card. Outputs come from SLDATA.outputs[path](flags, ctx) when the data
 * pack is loaded; the built-in outputs below cover sim, sessions prune, trust, mcp, doctor (and read the live registry, so `--yes` really prunes).
 * Everything the runner starts (the streaming timers) belongs to its view scope. */
(function (SL) {
  'use strict';
  const U = SL.u, { $, $$, esc, mk, fmtUsd, fmtN } = U, D = SL.D, G = SL.G, ui = SL.ui = SL.ui || {};
  const spec = () => D.spec, cmds = () => spec().commands;
  const key = c => c.path.join(' ');
  const find = p => { const k = Array.isArray(p) ? p.join(' ') : String(p).replace(/^sleipnir\s+/, ''); return cmds().find(c => key(c) === k) || null; };

  /* ---------- the command line from the form state ---------- */
  function cmdline(c, st) {
    const parts = ['sleipnir'].concat(c.path);
    (c.positional || []).forEach(p => { const v = (st.pos || {})[p.name]; if (v) parts.push(/\s/.test(v) ? JSON.stringify(v) : v); });
    c.flags.forEach(f => { const v = (st.flags || {})[f.name]; if (v == null || v === '' || v === false) return; if (f.arg === 'bool') { parts.push('--' + f.name); return; } if (f.repeatable) { String(v).split(',').map(s => s.trim()).filter(Boolean).forEach(x => parts.push('--' + f.name + ' ' + (/\s/.test(x) ? JSON.stringify(x) : x))); return; } if (String(v) === String(f.default)) return; parts.push('--' + f.name + ' ' + (/\s/.test(String(v)) ? JSON.stringify(String(v)) : v)); });
    return parts.join(' ');
  }
  /** {flag: value} with the typed values (ints parsed, bools true), plus `_` = positional array. */
  function flagValues(c, st) { const o = { _: (c.positional || []).map(p => (st.pos || {})[p.name]).filter(Boolean) }; c.flags.forEach(f => { let v = (st.flags || {})[f.name]; if (v == null || v === '') v = f.default; if (f.arg === 'bool') o[f.name] = !!v && v !== 'false'; else if (f.arg === 'int' || f.arg === 'uint') o[f.name] = v == null ? null : parseInt(v, 10); else if (f.arg === 'float') o[f.name] = v == null ? null : parseFloat(v); else o[f.name] = v == null ? null : f.repeatable ? String(v).split(',').map(s => s.trim()).filter(Boolean) : v; }); return o; }

  /* ---------- built-in sample outputs (used when the data pack has none for a command) ---------- */
  const L = (k, t) => ({ k, t });
  const SAMPLE = L('dim', '# sample output (the data pack is not loaded)');
  const age = s => s < 86400 ? Math.max(1, Math.round(s / 3600)) + 'h' : Math.round(s / 86400) + 'd';
  const BUILTIN = {
    'sessions prune': f => {
      const older = f['older-than'] || '30d', keep = f.keep == null ? 20 : f.keep, r = SL.sessions.prune(older, keep, !!f.yes); if (f.yes && !r.error) D.packSync(r.list.map(s => s.id));
      if (r.error) return { lines: [L('bad', 'sleipnir: ' + r.error)], exit: 2, ms: 3 };
      const lines = [L('head', (f.yes ? 'deleted ' : 'would delete ') + r.list.length + ' session' + (r.list.length === 1 ? '' : 's') + ' (' + r.mb.toFixed(1) + ' MB), older than ' + older + ', keeping the newest ' + keep + ':')].concat(r.list.map(s => L(f.yes ? 'warn' : 'out', '  ' + s.id + '  ' + age(s.ageS) + '  ' + s.mb.toFixed(1) + ' MB  ' + s.first)));
      if (!r.list.length) lines.push(L('dim', '  nothing is old enough'));
      lines.push(L(f.yes ? 'ok' : 'dim', f.yes ? 'done: ' + SL.sessions.recorded.length + ' sessions remain (' + SL.sessions.recordedMb().toFixed(1) + ' MB)' : 'nothing was deleted: pass --yes to delete them'));
      return { lines, exit: 0, ms: 38, card: { title: f.yes ? 'pruned' : 'dry run', rows: [['sessions', r.list.length], ['space', r.mb.toFixed(1) + ' MB'], ['kept', SL.sessions.recorded.length + (f.yes ? '' : ' (unchanged)')]] } };
    },
    'trust': f => {
      const act = (f._[0] || 'list'), dir = f._[1] || '~/projects/shop';
      if (act === 'add') { const d = G.trustDirs.find(x => x.dir === dir) || (G.trustDirs.push({ dir, files: 3, state: 'trusted (today)' }), G.trustDirs[G.trustDirs.length - 1]); d.state = 'trusted (today)'; G.ver++; return { lines: [L('ok', 'trusted ' + dir + ' (' + d.files + ' files; the yes holds until one of them changes)')], exit: 0, ms: 12 }; }
      if (act === 'forget') { const d = G.trustDirs.find(x => x.dir === dir); if (!d) return { lines: [L('bad', 'sleipnir: ' + dir + ' was never trusted')], exit: 1, ms: 6 }; d.state = 'not trusted'; G.ver++; return { lines: [L('warn', 'forgot ' + dir + ': the next session asks again')], exit: 0, ms: 9 }; }
      return { lines: [L('head', 'trusted files:')].concat(G.trust.map(t => L('out', '  ' + t.hash + '  ' + t.file + '  ' + t.state))).concat([L('head', 'directories:')]).concat(G.trustDirs.map(d => L(/^trusted/.test(d.state) ? 'ok' : 'warn', '  ' + d.dir + '  ' + d.files + ' files  ' + d.state))), exit: 0, ms: 11 };
    },
    'mcp': f => {
      const act = f._[0] || 'list', name = f._[1];
      if (act === 'list' || !act) return { lines: [L('head', 'tool servers:')].concat(G.mcp.map(s => L(s.state === 'running' ? 'ok' : /fail/.test(s.state) ? 'bad' : 'warn', '  ' + s.name.padEnd(10) + ' ' + s.transport.padEnd(6) + ' ' + String(s.tools.length).padStart(2) + ' tools  ' + s.state + '   (' + s.origin + ')'))), exit: 0, ms: 14, card: { title: 'mcp', rows: [['servers', G.mcp.length], ['running', G.mcp.filter(s => s.state === 'running').length], ['tools', G.mcp.reduce((n, s) => n + s.tools.length, 0)]] } };
      const s = G.mcp.find(x => x.name === name); if (!s) return { lines: [L('bad', 'sleipnir: no tool server named ' + (name || '(none)'))], exit: 1, ms: 5 };
      if (act === 'approve') { SL.act.setMcp(name, { state: 'running', tools: s.tools.length ? s.tools : ['list_issues', 'get_issue'] }); return { lines: [L('ok', 'approved ' + name + ': its tools are in the next request')], exit: 0, ms: 16 }; }
      if (act === 'revoke') { SL.act.setMcp(name, { state: 'needs approval' }); return { lines: [L('warn', 'revoked ' + name)], exit: 0, ms: 8 }; }
      return /fail/.test(s.state) ? { lines: [L('bad', name + ': connection reset by peer'), L('bad', name + ': not reachable')], exit: 1, ms: 612 } : { lines: [L('ok', name + ': ' + (s.tools.length || 'no') + ' tools answered')], exit: 0, ms: 143 };
    },
    'doctor': f => ({ lines: [SAMPLE, L('head', 'sleipnir doctor: probing ' + (f.model || 'heimdall/demo-model') + ' at ' + (f['base-url'] || 'https://heimdall.sample/v1')), L('ok', '  streaming      ok    first token 412 ms, 38 tokens/s'), L('ok', '  tool calls     ok    1 call parsed, arguments are valid JSON'), L('ok', '  prefix cache   ok    request 2 read 1,664 of 1,792 prompt tokens (93%)'), L('out', '    granularity  128 tokens: reads come in blocks'), L('out', '    min prefix   1,024 tokens'), L('out', '    warm-up      none: the first request after 5 s already reads'), f.deep ? L('out', '    deep         6 more requests: no read below 1,024 tokens, 1 miss after 300 s idle (the lifetime)') : L('dim', '    (--deep also measures granularity, minimum prefix and warm-up needs)'), L('ok', 'verdict: this endpoint serves the cache; no warm-up request is needed.')], exit: 0, ms: 2840, card: { title: 'doctor', rows: [['streaming', 'ok'], ['tools', 'ok'], ['cache', '93% on request 2']] } }),
    'sim': f => {
      const a = f.agents || 8, t = f.turns || 12, mode = f.mode || 'compare', tot = a * t * 1500;
      return { lines: [SAMPLE, L('head', 'sleipnir sim --mode ' + mode + ': ' + a + ' agents × ' + t + ' turns (a model with printed assumptions, not a benchmark)'), L('dim', 'assumptions: 4.5k shared prefix, 1.5k new tokens per turn, cached reads at 10% of the input price, 300 s cache lifetime'), L('out', 'policy                 input tokens   cache read   cost (sample $)'), L('out', 'no cache               ' + fmtN(tot + a * t * 4500).padStart(12) + '   ' + '0%'.padStart(10) + '   ' + (((tot + a * t * 4500) * 1e-6).toFixed(3)).padStart(12)), L('out', 'naive (per agent)      ' + fmtN(tot + a * t * 4500).padStart(12) + '   ' + '71%'.padStart(10) + '   ' + (((tot + a * t * 4500) * 3.9e-7).toFixed(3)).padStart(12)), L('ok', 'layered, shared prefix ' + fmtN(tot + a * t * 4500).padStart(12) + '   ' + '88%'.padStart(10) + '   ' + (((tot + a * t * 4500) * 2.1e-7).toFixed(3)).padStart(12)), L('dim', 'savings are estimates at the printed prices; a model, not a benchmark')], exit: 0, ms: 61, card: { title: 'sim', rows: [['mode', mode], ['agents', a], ['turns', t]] } };
    },
  };
  function generic(c, f) { return { lines: [SAMPLE, L('head', '$ ' + cmdline(c, { flags: f })), L('out', c.summary || ''), L('dim', 'no sample output for `sleipnir ' + key(c) + '` is built in. With the data pack loaded this prints a captured or generated result.'), L('out', c.usage || '')], exit: 0, ms: 20 }; }

  /** Run a command: returns {lines, exit, ms, card?}. State-owning commands use the built-in (they change the registry); the rest use the data pack when it has one. */
  function exec(c, st) {
    const f = flagValues(c, st); let k = key(c); if ((c.path[0] === 'trust' || c.path[0] === 'mcp') && c.path.length > 1) { f._ = c.path.slice(1).concat(f._); k = c.path[0]; }
    const own = k === 'sessions prune' || k === 'trust' || k === 'mcp', pk = D.output(c.path);
    try { if (own && BUILTIN[k]) return BUILTIN[k](f); if (pk) { const r = pk(f, D.ctx()); if (r && r.lines) return r; } if (BUILTIN[k]) return BUILTIN[k](f); } catch (e) { console.error(e); return { lines: [L('bad', 'the sample output failed: ' + e.message)], exit: 70, ms: 1 }; }
    return generic(c, f);
  }

  /* ---------- the view ---------- */
  G.runs = G.runs || [];
  function mount(sc, root, params) {
    const st = { c: find(params.path || ['sessions', 'prune']) || cmds()[0], pos: {}, flags: {}, q: '', running: false, out: null };
    if (params.flags) Object.assign(st.flags, params.flags);
    root.innerHTML = '<div class="runv"><section class="panel rv-list"><div class="ph"><h2>Commands</h2><div class="r"><span class="rcnt"></span><button class="btn sm" type="button" data-tools>‹ Tools</button></div></div><div class="pb"><input type="search" class="rq" placeholder="filter: sessions, trust, rl rollout ..." aria-label="Filter commands"><div class="rcmds" role="listbox" aria-label="sleipnir commands"></div></div></section><section class="panel rv-form"><div class="ph"><h2 class="rtitle">Run</h2><div class="r"><span class="rsrc"></span></div></div><div class="rform"></div><div class="rcl"><pre class="clpre cli"></pre><div class="row2"><button class="btn pri rrun" type="button">Run</button><button class="btn rcopy" type="button">Copy</button><button class="btn rreset" type="button">Reset</button></div></div></section><section class="panel rv-out"><div class="ph"><h2>Output</h2><div class="r rstat"></div></div><div class="term" role="log" aria-label="Command output" tabindex="0"><div class="tl dim">Nothing run yet. The form on the left builds the command line; Run prints a sample result.</div></div><div class="rcard"></div><div class="rhist"></div></section></div>';
    const list = $('.rcmds', root), form = $('.rform', root), clp = $('.clpre', root), term = $('.term', root), card = $('.rcard', root);
    const GROUPS = c => c.path[0] === 'rl' ? 'rl lab' : ['chat', 'run', 'swarm', 'demo', 'mock'].includes(c.path[0]) ? 'sessions and runs' : ['sessions', 'config', 'init', 'trust', 'mcp', 'login', 'logout'].includes(c.path[0]) ? 'project and account' : ['inspect', 'watch', 'replay', 'doctor', 'models', 'recon', 'sim', 'friction', 'update', 'schedule', 'daemon'].includes(c.path[0]) ? 'tools' : 'other';
    function drawList() {
      const ORDER = ['sessions and runs', 'project and account', 'tools', 'rl lab', 'other'], q = st.q.toLowerCase().trim(), words = q.split(/\s+/).filter(Boolean);
      const shown = cmds().filter(c => words.every(w => (key(c) + ' ' + (c.summary || '')).toLowerCase().includes(w))).map((c, i) => ({ c, i, g: ORDER.indexOf(GROUPS(c)) })).sort((x, y) => x.g - y.g || x.i - y.i).map(x => x.c); let g = null, h = '';   /* grouped, spec order inside a group */
      shown.forEach(c => { const gr = GROUPS(c); if (gr !== g) { g = gr; h += '<div class="cgrp">' + gr + '</div>'; } h += '<button type="button" role="option" class="rcmd' + (c === st.c ? ' sel' : '') + '" data-k="' + esc(key(c)) + '" aria-selected="' + (c === st.c) + '"><span class="mono">' + esc(key(c)) + '</span><small>' + esc((c.summary || '').slice(0, 70)) + '</small></button>'; });
      list.innerHTML = h || '<div class="dim" style="padding:10px">no command matches</div>'; $('.rcnt', root).textContent = shown.length + ' of ' + cmds().length;
    }
    function inputFor(f) {
      const v = st.flags[f.name], idn = 'rf-' + f.name, ph = f.default != null ? 'default ' + esc(f.default) : (f.defaultNote ? esc(f.defaultNote).slice(0, 40) : '');
      if (f.arg === 'bool') return '<label class="tgl"><input type="checkbox" id="' + idn + '" data-f="' + esc(f.name) + '"' + (v ? ' checked' : '') + '><span class="trk" aria-hidden="true"></span><span class="sr">' + esc(f.name) + '</span></label>';
      if (f.arg === 'int' || f.arg === 'uint' || f.arg === 'float') return '<input type="number" id="' + idn + '" data-f="' + esc(f.name) + '" value="' + esc(v == null ? '' : v) + '" step="' + (f.arg === 'float' ? 'any' : '1') + '" placeholder="' + ph + '" aria-label="' + esc(f.name) + '">';
      return '<input type="text" id="' + idn + '" data-f="' + esc(f.name) + '" value="' + esc(v == null ? '' : v) + '" placeholder="' + (f.repeatable ? 'a, b, c (repeatable)' : ph) + '" aria-label="' + esc(f.name) + '" autocomplete="off" spellcheck="false">';
    }
    function drawForm() {
      const c = st.c; $('.rtitle', root).textContent = 'sleipnir ' + key(c); $('.rsrc', root).textContent = spec().generatedFrom || '';
      form.innerHTML = '<p class="rsum">' + esc(c.summary || '') + '</p><p class="rusage mono">' + esc(c.usage || '') + '</p>' + ((c.positional || []).length ? '<div class="rgrp">arguments</div>' + c.positional.map(p => '<div class="frow"><label for="rp-' + esc(p.name) + '" class="mono">' + esc(p.name) + (p.required ? ' <i class="req" title="required">*</i>' : '') + '</label><input type="text" id="rp-' + esc(p.name) + '" data-p="' + esc(p.name) + '" value="' + esc(st.pos[p.name] || '') + '" autocomplete="off" spellcheck="false"></div>').join('') : '') + (c.flags.length ? '<div class="rgrp">flags</div>' + c.flags.map(f => '<div class="frow' + (st.flags[f.name] ? ' set' : '') + '"><label for="rf-' + esc(f.name) + '" class="mono">--' + esc(f.name) + '<small class="ty">' + (f.arg === 'bool' ? 'on/off' : esc(f.arg)) + (f.repeatable ? ' · repeatable' : '') + '</small></label><div class="fin">' + inputFor(f) + '</div><p class="fdesc">' + esc(f.desc || '') + (f.default != null && f.arg !== 'bool' ? ' <span class="dim">(default ' + esc(f.default) + ')</span>' : f.defaultNote ? ' <span class="dim">(default: ' + esc(f.defaultNote) + ')</span>' : '') + '</p></div>').join('') : '<p class="dim" style="padding:0 12px">no flags</p>');
      drawCl();
    }
    function drawCl() { clp.textContent = cmdline(st.c, st); }
    /* streaming output: lines appear over ~1.6 s at most; the timers are the view's, so leaving the view stops them */
    let streamIds = [];
    function stop() { streamIds.forEach(id => sc.clear(id)); streamIds = []; st.running = false; $('.rrun', root).disabled = false; }
    function run() {
      stop(); const r = exec(st.c, st), lines = r.lines || [], per = Math.max(12, Math.min(90, 1600 / Math.max(1, lines.length))); st.running = true; $('.rrun', root).disabled = true; term.innerHTML = ''; card.innerHTML = ''; $('.rstat', root).innerHTML = '<span class="dim">running…</span>';
      const cmd = cmdline(st.c, st); term.appendChild(mk('div', { class: 'tl hd' }, '<span class="prompt">$</span> ' + esc(cmd)));
      lines.forEach((ln, i) => { streamIds.push(sc.timeout(() => { term.appendChild(mk('div', { class: 'tl ' + (ln.k || 'out') }, esc(ln.t))); term.scrollTop = term.scrollHeight; }, 60 + i * per)); });
      streamIds.push(sc.timeout(() => { st.running = false; $('.rrun', root).disabled = false; const ok = r.exit === 0; $('.rstat', root).innerHTML = '<span class="' + (ok ? 'ok' : 'err') + '">exit ' + r.exit + '</span><span>' + (r.ms < 1000 ? r.ms + ' ms' : (r.ms / 1000).toFixed(1) + ' s') + '</span>'; card.innerHTML = '<div class="rc ' + (ok ? 'ok' : 'bad') + '"><b>' + (ok ? '✓' : '✗') + ' ' + esc((r.card && r.card.title) || key(st.c)) + '</b><span>exit status ' + r.exit + '</span><span>elapsed ' + (r.ms < 1000 ? r.ms + ' ms' : (r.ms / 1000).toFixed(1) + ' s') + '</span>' + ((r.card && r.card.rows) || []).map(([a, b]) => '<span><i class="dim">' + esc(a) + '</i> ' + esc(b) + '</span>').join('') + '</div>'; G.runs.unshift({ cmd, exit: r.exit, ms: r.ms, path: st.c.path.slice(), flags: Object.assign({}, st.flags) }); G.runs = G.runs.slice(0, 12); hist(); }, 90 + lines.length * per));
    }
    function hist() { $('.rhist', root).innerHTML = G.runs.length ? '<div class="rgrp">recent runs</div>' + G.runs.map((h, i) => '<button type="button" class="hrow" data-h="' + i + '"><span class="' + (h.exit === 0 ? 'ok' : 'err') + '">' + (h.exit === 0 ? '✓' : '✗') + '</span><span class="mono">' + esc(h.cmd) + '</span><span class="dim">' + (h.ms < 1000 ? h.ms + 'ms' : (h.ms / 1000).toFixed(1) + 's') + '</span></button>').join('') : ''; }
    sc.listen($('[data-tools]', root), 'click', () => SL.views.show('tools'));
    sc.listen($('.rq', root), 'input', e => { st.q = e.target.value; drawList(); });
    sc.listen(list, 'click', e => { const b = e.target.closest('[data-k]'); if (!b) return; stop(); st.c = find(b.dataset.k); st.pos = {}; st.flags = {}; term.innerHTML = '<div class="tl dim">Nothing run yet.</div>'; card.innerHTML = ''; $('.rstat', root).innerHTML = ''; drawList(); drawForm(); });
    sc.listen(form, 'input', e => { const t = e.target; if (t.dataset.p) st.pos[t.dataset.p] = t.value.trim(); else if (t.dataset.f) { st.flags[t.dataset.f] = t.type === 'checkbox' ? t.checked : t.value; t.closest('.frow').classList.toggle('set', !!st.flags[t.dataset.f]); } drawCl(); });
    sc.listen($('.rrun', root), 'click', run);
    sc.listen($('.rcopy', root), 'click', () => U.copy(cmdline(st.c, st)).then(ok => ui.toast(ok ? 'command copied' : 'copy is not available here', ok ? 'ok' : 'warm')));
    sc.listen($('.rreset', root), 'click', () => { stop(); st.pos = {}; st.flags = {}; drawForm(); });
    sc.listen(root, 'click', e => { const h = e.target.closest('[data-h]'); if (h) { const r = G.runs[+h.dataset.h]; stop(); st.c = find(r.path) || st.c; st.flags = Object.assign({}, r.flags); st.pos = {}; drawList(); drawForm(); } });
    sc.listen(form, 'keydown', e => { if (e.key === 'Enter' && e.target.tagName === 'INPUT' && e.target.type !== 'checkbox') { e.preventDefault(); run(); } });
    drawList(); drawForm(); hist(); if (params.run) run();
  }
  SL.views.register({ name: 'runner', title: 'Run a command', nav: false, mount });
  SL.runner = { find, cmdline, exec, flagValues, spec, BUILTIN };
  ui.runCli = (path, flags, run) => SL.views.show('runner', { path, flags, run });
})(SL);
