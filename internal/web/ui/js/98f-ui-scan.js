/* 98f-ui-scan.js: the Deep scan view. It lists the project's deep scans (GET /api/sessions/ID/scan), shows one in full
 * (GET .../scan/SLUG: its findings with their traces, verdicts and reproductions, its attack surface and the shards' maps),
 * starts a scan of the project (POST .../scan {action: start}) or of a repository it clones first (POST /api/scan/clone, then the
 * New session dialog, filled in, starts a sandboxed team there), and acts on a finding when the person asks: an issue written up
 * for them to read and edit before it is opened, or a goal that has the team fix it.
 *
 * Everything a scan holds (titles, claims, quotes, notes, scripts, their output) was written by agents reading untrusted code:
 * it goes through esc() or textContent, never into a URL or an attribute unescaped. */
(function (SL) {
  'use strict';
  const U = SL.u, { $, esc } = U, ui = SL.ui = SL.ui || {};
  const TK = SL.toolkit || {}, net = TK.net;
  const arr = x => (Array.isArray(x) ? x : []);
  const plural = (n, w) => n + ' ' + w + (n === 1 ? '' : 's');
  /** The phases of a scan, in order, with what each one is for. */
  const PHASES = [['map', 'Map', 'every shard read: what it does, its attack surface, how its parts connect'], ['hunt', 'Hunt', 'every shard followed from its entry points to its sinks, leads filed with a cited trace'],
    ['verify', 'Verify', 'every lead ruled on by agents that did not file it, and reproduced in the sandbox where the depth asks'], ['report', 'Report', 'the report written from the ledger'], ['done', 'Done', 'every check passed']];
  const phaseAt = p => Math.max(0, PHASES.findIndex(x => x[0] === p));
  const SEV = ['critical', 'high', 'medium', 'low'];
  const LANES = [['entry', 'Entry points'], ['boundary', 'Trust boundaries'], ['access', 'Access control'], ['sink', 'Sinks'], ['config', 'Configuration']];
  const STATUS = [['confirmed', 'Findings', 'confirmed by agents that did not file them, and reproduced where the depth asks'], ['unproven', 'Unproven', 'confirmed, not yet reproduced in the sandbox: likely, not shown'],
    ['open', 'Being verified', 'filed, waiting for enough verdicts'], ['rejected', 'Rejected', 'the verifiers showed why they are not real']];
  const short = c => String(c || '').slice(0, 12);
  const sevPill = s => '<span class="sc-sev sc-' + esc(SEV.includes(s) ? s : 'low') + '">' + esc(s || '?') + '</span>';
  /** Where a finding does harm: the last step of its trace. */
  const sinkOf = f => { const t = arr(f.trace); return t.length ? t[t.length - 1].file + ':' + t[t.length - 1].line : ''; };
  const proofWord = f => { const p = arr(f.proofs); if (!p.length) return ''; return p.some(x => x.outcome === 'reproduced') ? 'reproduced' : p[p.length - 1].outcome; };

  function runHtml(r, sel) {
    const at = phaseAt(r.phase), done = r.phase === 'done';
    const steps = PHASES.map(([id, label, about], i) => '<li class="' + (i < at || done ? 'done' : i === at ? 'cur' : '') + '" title="' + esc(about) + '"><i></i><span>' + esc(label) + '</span></li>').join('');
    const sev = SEV.filter(s => r.bySeverity && r.bySeverity[s]).map(s => sevPill(s) + '<b class="mono">' + r.bySeverity[s] + '</b>').join(' ');
    return '<button type="button" class="sc-run' + (r.slug === sel ? ' sel' : '') + '" data-scan="' + esc(r.slug) + '">' +
      '<span class="sc-rtop"><b class="mono">' + esc(r.slug) + '</b><span class="chip">' + esc(r.depth) + '</span>' + (r.active ? '<span class="chip rs-live">this session</span>' : '') + (done ? '<span class="chip rs-ok">done</span>' : '') + '</span>' +
      '<span class="sc-rmeta mono dim">' + esc(short(r.commit)) + (r.focus ? ' · ' + esc(r.focus) : '') + ' · ' + plural(r.files, 'file') + ' · ' + plural(r.funcs, 'function') + ' · ' + plural(r.shards, 'shard') + ' · team of ' + r.team + '</span>' +
      '<ol class="rs-steps sc-steps">' + steps + '</ol>' +
      '<span class="sc-rnums">' + r.mapped + '/' + r.shards + ' mapped · ' + r.hunted + '/' + r.shards + ' hunted · ' + plural(r.leads, 'lead') + ' · ' + r.confirmed + ' confirmed' + (r.unproven ? ' · ' + r.unproven + ' unproven' : '') + (r.rejected ? ' · ' + r.rejected + ' rejected' : '') + '</span>' +
      (sev ? '<span class="sc-rsev">' + sev + '</span>' : '') + '</button>';
  }

  function findingRow(f) {
    const v = arr(f.verdicts), yes = v.filter(x => x.confirm).length, no = v.length - yes, pw = proofWord(f);
    return '<button type="button" class="sc-f" data-lead="' + esc(f.id) + '">' + sevPill(f.severity) + '<span class="mono sc-fid">' + esc(f.id) + '</span>' +
      '<span class="sc-ft"><b>' + esc(f.title) + '</b><span class="mono dim">' + esc(f.category) + ' · ' + esc(sinkOf(f)) + '</span></span>' +
      '<span class="sc-fv mono" title="verdicts: confirm / reject">' + yes + '✓ ' + no + '✗</span>' +
      (pw ? '<span class="chip ' + (pw === 'reproduced' ? 'rs-ok' : '') + '">' + esc(pw) + '</span>' : '') + (f.reject_reason ? '<span class="chip">' + esc(f.reject_reason) + '</span>' : '') + '</button>';
  }

  function detailHtml(d) {
    const c = d.counts || {}, fs = arr(d.findings);
    const head = '<div class="sc-dhead"><span class="mono">' + esc(d.slug) + '</span><span class="dim"> · commit <span class="mono">' + esc(short(d.commit)) + '</span>' + (d.focus ? ' · ' + esc(d.focus) : '') +
      ' · ' + esc(d.depth) + ': ' + plural(d.verifiers, 'verifier') + ' per lead' + (d.defender ? ' with a defender' : '') + (d.proveFrom ? ', reproduced from ' + esc(d.proveFrom) + ' up' : '') + '</span>' +
      (d.proveFrom && !d.sandbox ? '<p class="sc-warn">This session has no sandbox, so nothing can be reproduced here: start one with <span class="mono">--sandbox docker</span> (the <b>sandbox</b> field of New session).</p>' : '') + '</div>';
    const nums = '<div class="sc-nums">' + [[c.files, 'files'], [c.funcs, 'functions'], [c.lines, 'lines'], [c.points, 'points'], [c.edges, 'edges'], [c.leads, 'leads'], [c.confirmed, 'confirmed'], [c.reproduced, 'reproduced']]
      .map(([n, w]) => '<span><b>' + (n || 0) + '</b>' + esc(w) + '</span>').join('') + '</div>';
    const groups = STATUS.map(([st, title, about]) => {
      const list = fs.filter(f => f.status === st); if (!list.length) return '';
      const rows = list.map(findingRow).join('');
      return st === 'rejected' ? '<details class="sc-group"><summary><h3>' + esc(title) + ' <span class="dim">' + list.length + '</span></h3><span class="dim">' + esc(about) + '</span></summary>' + rows + '</details>'
        : '<section class="sc-group"><h3>' + esc(title) + ' <span class="dim">' + list.length + '</span></h3><p class="dim">' + esc(about) + '</p>' + rows + '</section>';
    }).join('') || '<p class="stubnote">No lead yet: the team maps the shards first, then hunts them. Findings appear here as soon as verifiers confirm them.</p>';
    const probs = arr(d.problems);
    const needs = d.phase === 'done' ? '' : '<details class="sc-needs"><summary>What the scan still needs <span class="dim">' + (probs.length + (d.moreProblems || 0)) + '</span></summary><ul>' +
      probs.map(p => '<li><span class="chip">' + esc(p.phase) + '</span> ' + esc(p.text) + '</li>').join('') + (d.moreProblems ? '<li class="dim">and ' + d.moreProblems + ' more</li>' : '') + '</ul></details>';
    return head + nums + needs + groups + '<p class="dim sc-files">The report is rewritten as the scan goes: <span class="mono">' + esc(d.reportDir) + '</span> (REPORT.md, findings.json, findings.sarif, map.json).</p>';
  }

  function surfaceHtml(d) {
    const pts = arr(d.points), shards = arr(d.shards);
    const lanes = LANES.map(([id, title]) => {
      const ps = pts.filter(p => p.lane === id);
      return '<div class="sc-lane"><h4>' + esc(title) + ' <span class="dim">' + ps.length + '</span></h4>' + (ps.length ? ps.map(p => '<div class="sc-pt" title="' + esc(p.note || '') + '"><span class="mono dim">' + esc(p.id) + '</span> ' + esc(p.title) +
        '<span class="mono dim sc-loc">' + esc(p.file) + ':' + (p.line | 0) + '</span></div>').join('') : '<div class="dim sc-none">none yet</div>') + '</div>';
    }).join('');
    const cells = shards.map(s => {
      const st = s.hunt ? 'hunted' : s.map ? 'mapped' : s.claimed ? 'claimed' : 'open';
      const tip = s.id + ' · ' + s.first + (s.files > 1 ? ' and ' + (s.files - 1) + ' more' : '') + ' · ' + s.funcs + ' functions, ' + s.lines + ' lines · ' + st + (s.claimed ? ' by ' + s.claimed : '') + (s.map ? '\n\n' + s.map : '');
      return '<span class="sc-cell sc-' + st + '" title="' + esc(tip) + '">' + esc(s.id) + '</span>';
    }).join('');
    return '<div class="sc-lanes">' + lanes + '</div><h4 class="sc-sh">Shards <span class="dim">open · claimed · mapped · hunted</span></h4><div class="sc-cells">' + cells + '</div>';
  }

  /** The scan's report, rendered by the server from the ledger as it is now: a download per format, and PDF through the browser's print. */
  const exportURL = (S, slug, fmt) => '/api/sessions/' + encodeURIComponent(String(S.id)) + '/scan/' + encodeURIComponent(slug) + '/export/' + fmt;
  function exportHtml(S, slug) {
    if (!S) return '';
    const a = (fmt, label, about) => '<a class="btn sm" href="' + esc(exportURL(S, slug, fmt)) + '" download title="' + esc(about) + '">' + esc(label) + '</a>';
    return '<span class="sc-exp">' + a('csv', 'CSV', 'every lead, one row each, for a spreadsheet or a tracker') + a('json', 'JSON', 'findings.json: the findings with their traces, verdicts and reproductions') +
      '<button class="btn sm" type="button" data-sc-pdf title="the full report, laid out for print: choose Save as PDF in the print dialog">PDF</button>' +
      '<details class="sc-more"><summary class="btn sm" title="more formats">⋯</summary><div class="sc-menu">' + a('md', 'Markdown', 'REPORT.md') + a('sarif', 'SARIF', 'findings.sarif, for code scanning') +
      '<a class="btn sm" href="' + esc(exportURL(S, slug, 'html')) + '" target="_blank" rel="noopener" title="the printable report in a new tab">Open the report ↗</a></div></details></span>';
  }
  /** Print the report page in a frame of this page: the browser's dialog saves it as a PDF. */
  function printReport(S, slug) {
    const old = document.getElementById('scPrint'); if (old) old.remove();
    const f = document.createElement('iframe'); f.id = 'scPrint'; f.title = 'the report, to print'; f.style.cssText = 'position:fixed;width:0;height:0;border:0;right:0;bottom:0';
    f.addEventListener('load', () => { try { f.contentWindow.focus(); f.contentWindow.print(); } catch (err) { window.open(exportURL(S, slug, 'html'), '_blank', 'noopener'); } });
    f.src = exportURL(S, slug, 'html'); document.body.appendChild(f);
  }

  function depthHtml(d, sel) {
    return '<label class="rs-depth' + (d.id === sel ? ' sel' : '') + '"><input type="radio" name="scDepth" value="' + esc(d.id) + '"' + (d.id === sel ? ' checked' : '') + '>' +
      '<b>' + esc(d.title || d.id) + '</b><span>' + esc(d.about) + '</span><small class="mono">shards ≤ ' + d.shardFuncs + ' fn / ' + d.shardLines + ' lines · ' + plural(d.verifiers, 'verifier') + (d.defender ? ' + defender' : '') + (d.proveFrom ? ' · proof ≥ ' + esc(d.proveFrom) : '') + '</small></label>';
  }

  /** The finding in full, with what the person can do with it: open an issue (written up, edited, then opened) or have the team fix it. */
  function openFinding(S, d, f, reload) {
    const v = arr(f.verdicts), p = arr(f.proofs), actionable = f.status === 'confirmed' || f.status === 'unproven';
    const trace = arr(f.trace).map((s, i) => '<li><div class="sc-step"><span class="mono">' + (i + 1) + '. ' + esc(s.file) + ':' + (s.line | 0) + '</span> ' + esc(s.note) + '</div><pre class="pre sc-q">' + esc(s.quote) + '</pre></li>').join('');
    const verdicts = v.map(x => '<li><b>' + esc(x.by) + '</b> <span class="dim">' + esc(x.role) + '</span> ' + (x.confirm ? '<span class="chip rs-ok">confirms</span>' : '<span class="chip">rejects: ' + esc(x.reason || '') + '</span>') + ' ' + esc(x.note) + '</li>').join('');
    const proofs = p.map(x => '<details class="sc-proof"><summary><b>' + esc(x.outcome) + '</b> <span class="dim">by ' + esc(x.by) + ', exit ' + (x.exit | 0) + ', ' + (x.seconds | 0) + 's' + (x.network ? ', with network' : '') + '</span></summary><h5>Script</h5><pre class="pre sc-q">' + esc(x.script) + '</pre><h5>Output</h5><pre class="pre sc-q">' + esc(x.output) + '</pre></details>').join('');
    ui.modal({ title: f.id + ' · ' + f.title, kicker: f.severity + ' · ' + f.status, wide: true, color: f.severity === 'critical' || f.severity === 'high' ? 'var(--err)' : 'var(--warm)', body:
      '<div class="sc-dlg"><p class="sc-claim">' + esc(f.claim) + '</p>' +
      '<p class="sc-meta">' + sevPill(f.severity) + ' <span class="mono">' + esc(f.category) + '</span> · reachability ' + esc(f.reach) + ' · exploitability ' + esc(f.exploit) + ' · impact ' + esc(f.impact) + ' · filed by ' + esc(f.by) + '</p>' +
      '<h4>Trace</h4><ol class="sc-trace">' + trace + '</ol>' + (verdicts ? '<h4>Verdicts</h4><ul class="sc-verdicts">' + verdicts + '</ul>' : '') + (proofs ? '<h4>Reproductions</h4>' + proofs : '') +
      (f.fix ? '<h4>Suggested fix</h4><p>' + esc(f.fix) + '</p>' : '') +
      (actionable ? '<div class="row2 sc-acts"><button class="btn" type="button" data-sc-issue>Open issue…</button><button class="btn" type="button" data-sc-fix>Fix…</button></div>' : '<p class="stubnote">Only a confirmed finding can be written up or fixed.</p>') +
      '<div class="sc-panel" data-sc-panel hidden></div></div>',
    onMount(body, sc) {
      const panel = $('[data-sc-panel]', body);
      sc.listen(body, 'click', async e => {
        const t = e.target;
        if (t.closest('[data-sc-issue]')) {
          panel.hidden = false; panel.textContent = 'writing the issue up…';
          const r = await net.post(net.tab(S, '/scan'), { action: 'issue-draft', slug: d.slug, lead: f.id }); if (!sc.alive) return;
          if (!r.ok || !r.data) { panel.textContent = r.message || 'the issue could not be written up'; return; }
          panel.innerHTML = '<label class="lab" for="scIT">Title</label><input id="scIT" class="sc-in" maxlength="256"><label class="lab" for="scIB">Body (Markdown): read it, change what you want, then open it</label><textarea id="scIB" class="sc-in mono" rows="14"></textarea>' +
            '<div class="row2"><button class="btn pri" type="button" data-sc-open title="gh issue create on this machine, in the project’s repository">Open on GitHub</button><button class="btn" type="button" data-sc-copy>Copy</button><span class="dim" data-sc-said></span></div>';
          $('#scIT', panel).value = r.data.title || ''; $('#scIB', panel).value = r.data.body || '';
          return;
        }
        if (t.closest('[data-sc-copy]')) { const txt = '# ' + $('#scIT', panel).value + '\n\n' + $('#scIB', panel).value; try { await navigator.clipboard.writeText(txt); ui.toast('copied', 'ok'); } catch (err) { ui.toast('the clipboard is not available here', 'warm'); } return; }
        if (t.closest('[data-sc-open]')) {
          const b = t.closest('[data-sc-open]'), said = $('[data-sc-said]', panel); b.disabled = true; said.textContent = 'opening…';
          const r = await net.post(net.tab(S, '/scan'), { action: 'issue', slug: d.slug, lead: f.id, title: $('#scIT', panel).value, body: $('#scIB', panel).value }, { timeout: 90000 }); if (!sc.alive) return;
          b.disabled = false;
          if (!r.ok) { said.textContent = r.message || 'the issue was not opened'; return; }
          const u = String((r.data && r.data.url) || '');
          said.innerHTML = 'opened: ' + (/^https:\/\//.test(u) ? '<a href="' + esc(u) + '" target="_blank" rel="noopener">' + esc(u) + '</a>' : esc(u));
          return;
        }
        if (t.closest('[data-sc-fix]')) {
          panel.hidden = false;
          panel.innerHTML = '<p>The team gets a goal: fix the cause on the trace, add a regression test that fails without the fix, and run the reproducer again to show it no longer reproduces.</p>' +
            '<label class="chk"><input type="checkbox" data-sc-pr> open a pull request for it, and fix its CI and reviews (needs push access from the session)</label><label class="chk"><input type="checkbox" data-sc-merge> merge it once it is green</label>' +
            '<div class="row2"><button class="btn pri" type="button" data-sc-go>Start the fix</button></div>';
          return;
        }
        if (t.closest('[data-sc-go]')) {
          const b = t.closest('[data-sc-go]'); b.disabled = true;
          const r = await net.post(net.tab(S, '/scan'), { action: 'fix', slug: d.slug, lead: f.id, autoFix: !!$('[data-sc-pr]', panel).checked, autoMerge: !!$('[data-sc-merge]', panel).checked }); if (!sc.alive) return;
          b.disabled = false;
          if (!r.ok) { net.fail(r, 'the fix did not start'); return; }
          ui.toast(r.data && r.data.queued ? 'the fix is queued after the current goal' : 'the fix is the team’s goal now', 'ok'); ui.closeModal(); reload();
        }
      });
    } });
  }

  SL.views.register({ name: 'scan', title: 'Deep scan', mount(sc, root, params, S0) {
    const P = ui.pageHead(sc, root, S0 || SL.sessions.active, 'scan', params, { title: 'Deep scan', purpose: 'A team reads a whole repository at one commit: it maps every shard, records the attack surface and how the parts connect, follows the input to the sinks, and reports only what agents that did not file a lead confirmed and, from the depth’s bar up, a script reproduced in the sandbox.', action: { label: '+ New scan', title: 'scan this project, or a repository cloned for it' }, onAction: () => { const u = $('#scUrl', root); if (u) u.focus(); } });
    P.fill({ 'scan.runs': '<section class="panel"><div class="ph"><h2>Scans</h2><div class="r"><span class="sc-count dim"></span></div></div><div class="sc-list"></div></section>',
      'scan.findings': '<section class="panel"><div class="ph"><h2>Findings</h2><div class="r"><span class="sc-phase"></span></div></div><div class="sc-detail"></div></section>',
      'scan.surface': '<section class="panel"><div class="ph"><h2>Attack surface and map</h2></div><div class="sc-surface"></div></section>',
      'scan.new': '<section class="panel"><div class="ph"><h2>New scan</h2><div class="r"><span class="mono dim">/scan DEPTH [DIR]</span></div></div><form class="rs-new sc-new" autocomplete="off">' +
        '<div class="sc-src" role="radiogroup" aria-label="what to scan"><label class="chk"><input type="radio" name="scSrc" value="here" checked> this project</label><label class="chk"><input type="radio" name="scSrc" value="url"> a repository, cloned for the scan</label></div>' +
        '<div data-sc-here><label class="lab" for="scFocus">Folder (optional)</label><input id="scFocus" class="sc-in mono" placeholder="the whole repository; or e.g. server/api" spellcheck="false"></div>' +
        '<div data-sc-url hidden><label class="lab" for="scUrl">Repository</label><input id="scUrl" class="sc-in mono" placeholder="https://github.com/owner/repo" spellcheck="false"><p class="dim sc-hint">Cloned into ~/sleipnir-scans with git’s hooks and symbolic links off. A new session opens there, its commands in a container with no network.</p></div>' +
        '<div class="rs-depths" role="radiogroup" aria-label="depth"></div><div class="row2"><button class="btn pri" type="submit" id="scGo">Start the scan</button></div>' +
        '<p class="stubnote">The work is sized by the code, not by a clock: the scan splits it into shards of so many functions and lines, and asks for one agent per two shards, from 8 to 32.</p></form></section>' });
    const st = { runs: [], depths: [], on: true, sandbox: false, depth: 'standard', sel: (params && params.scan) || '', detail: null, seq: 0, dseq: 0, sig: '', dsig: '' };
    const S = () => SL.sessions.active;
    const list = $('.sc-list', root), det = $('.sc-detail', root), surf = $('.sc-surface', root), depths = $('.rs-depths', root);
    function paint() {
      if (!list) return;
      const sig = JSON.stringify([st.runs, st.on, st.sel]); if (sig === st.sig) return; st.sig = sig;
      const cnt = $('.sc-count', root); if (cnt) cnt.textContent = st.runs.length ? plural(st.runs.length, 'scan') : '';
      list.innerHTML = !st.on ? '<p class="stubnote" style="padding:10px 12px;margin:0">The deep scan plugin is off for this session: turn on <b>Deep scan</b> in <button class="lnk" type="button" data-sc-plugins>Plugins</button>, then start the session again, with <span class="mono">--sandbox docker</span> so that leads can be reproduced.</p>'
        : st.runs.length ? st.runs.map(r => runHtml(r, st.sel)).join('') : '<p class="stubnote" style="padding:10px 12px;margin:0">No scan yet. Start one from New scan: this project at its HEAD, or a repository cloned for it.</p>';
    }
    function paintDetail() {
      const d = st.detail, sig = JSON.stringify(d); if (sig === st.dsig) return; st.dsig = sig;
      if (det) det.innerHTML = d ? detailHtml(d) : '<p class="stubnote" style="padding:10px 12px;margin:0">' + (st.runs.length ? 'Pick a scan to see its findings.' : 'The findings of a scan appear here as verifiers confirm them.') + '</p>';
      if (surf) surf.innerHTML = d ? surfaceHtml(d) : '';
      const ph = $('.sc-phase', root); if (ph) ph.innerHTML = d ? '<span class="chip' + (d.phase === 'done' ? ' rs-ok' : ' rs-live') + '">' + esc(d.phase) + '</span>' + exportHtml(S(), d.slug) : '';
    }
    function paintDepths() { if (depths) depths.innerHTML = st.depths.map(x => depthHtml(x, st.depth)).join(''); }
    async function loadDetail() {
      const s = S(); if (!s || !net || !st.sel) { st.detail = null; paintDetail(); return; }
      const mine = ++st.dseq, r = await net.get(net.tab(s, '/scan/' + encodeURIComponent(st.sel))); if (!sc.alive || mine !== st.dseq) return;
      if (r.ok && r.data) { st.detail = r.data; paintDetail(); }
    }
    async function load() {
      const s = S(); if (!s || !net) return; const mine = ++st.seq;
      const r = await net.get(net.tab(s, '/scan')); if (!sc.alive || mine !== st.seq) return;
      if (!r.ok || !r.data) return;
      st.runs = arr(r.data.runs); st.on = r.data.on !== false; st.sandbox = !!r.data.sandbox;
      if (!st.depths.length && arr(r.data.depths).length) { st.depths = arr(r.data.depths); paintDepths(); }
      if (!st.sel || !st.runs.some(x => x.slug === st.sel)) { const a = st.runs.find(x => x.active) || st.runs[0]; st.sel = a ? a.slug : ''; }
      paint(); loadDetail();
    }
    async function start(form) {
      const src = (form.querySelector('[name=scSrc]:checked') || {}).value, b = $('#scGo', root); b.disabled = true;
      try {
        if (src === 'url') {
          const url = $('#scUrl', root).value.trim(); if (!url) { $('#scUrl', root).focus(); return; }
          ui.toast('cloning ' + url + '…', 'ok');
          const r = await net.post('/api/scan/clone', { url, depth: st.depth }, { timeout: 20 * 60 * 1000 }); if (!sc.alive) return;
          if (!r.ok || !r.data) { net.fail(r, 'the repository was not cloned'); return; }
          const name = String(r.data.dir || '').split(/[\\/]/).pop();
          ui.dialogs.newSession({ name: 'scan ' + name, cwd: r.data.dir, sandbox: 'docker', swarm: r.data.team, goalText: r.data.goal });
          return;
        }
        const r = await net.post(net.tab(S(), '/scan'), { action: 'start', depth: st.depth, focus: $('#scFocus', root).value.trim() }); if (!sc.alive) return;
        if (!r.ok) { net.fail(r, 'the scan did not start'); return; }
        ui.toast(r.data && r.data.queued ? 'the scan is queued after the current goal' : 'the scan started: the team maps the shards now', 'ok'); st.sel = ''; load();
      } finally { b.disabled = false; }
    }
    sc.listen(root, 'click', e => {
      const t = e.target;
      if (t.closest('[data-sc-plugins]')) { SL.views.show('plugins'); return; }
      if (t.closest('[data-sc-pdf]') && st.detail) { printReport(S(), st.detail.slug); return; }
      const run = t.closest('[data-scan]'); if (run) { st.sel = run.dataset.scan; st.sig = ''; paint(); st.detail = null; st.dsig = ''; loadDetail(); return; }
      const fl = t.closest('[data-lead]'); if (fl && st.detail) { const f = arr(st.detail.findings).find(x => x.id === fl.dataset.lead); if (f) openFinding(S(), st.detail, f, load); }
    });
    sc.listen(root, 'change', e => {
      const t = e.target;
      if (t.name === 'scDepth') { st.depth = t.value; paintDepths(); }
      if (t.name === 'scSrc') { $('[data-sc-here]', root).hidden = t.value !== 'here'; $('[data-sc-url]', root).hidden = t.value !== 'url'; }
    });
    sc.listen(root, 'submit', e => { if (!e.target.closest('.sc-new')) return; e.preventDefault(); start(e.target.closest('.sc-new')); });
    sc.interval(load, 5000);
    sc.on('plugins-changed', load);
    paint(); paintDetail(); load();
  } });
})(SL);
