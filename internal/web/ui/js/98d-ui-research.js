/* 98d-ui-research.js: the Research view. It lists the project's deep research runs (GET /api/sessions/ID/research) with where each
 * stands, opens a run's wiki in a new tab (/research/ID/SLUG/, rendered by the server from the run's ledger), exports it into the
 * project, and starts a run (POST .../research {action: start}): the run is a goal, set as /research sets it, so the team drives it
 * and the judge rules on its milestones.
 *
 * A question, a slug and a server message are data: they go through esc() or textContent. */
(function (SL) {
  'use strict';
  const U = SL.u, { $, esc } = U, ui = SL.ui = SL.ui || {};
  const TK = SL.toolkit || {}, net = TK.net;
  const arr = x => (Array.isArray(x) ? x : []);
  const plural = (n, w) => n + ' ' + w + (n === 1 ? '' : 's');
  /** The phases of a run, in order, with what each one is for. */
  const PHASES = [['plan', 'Plan', 'perspectives, tracks, questions and the outline'], ['gather', 'Gather', 'sources with checked quotes, and findings'],
    ['write', 'Write', 'the pages, each citing its sources'], ['verify', 'Fact-check', 'every page passed by an agent that did not write it'],
    ['publish', 'Publish', 'the wiki built from the ledger'], ['done', 'Done', 'published and checked']];
  const phaseAt = p => Math.max(0, PHASES.findIndex(x => x[0] === p));
  /** The wiki of a run on this server. */
  const wikiURL = (S, slug) => '/research/' + encodeURIComponent(String(S.id)) + '/' + encodeURIComponent(slug) + '/';

  function runHtml(S, r) {
    const at = phaseAt(r.phase), done = r.phase === 'done';
    const steps = PHASES.map(([id, label, about], i) => r.noFactCheck && id === 'verify' ? '' : '<li class="' + (i < at || done ? 'done' : i === at ? 'cur' : '') + '" title="' + esc(about) + '"><i></i><span>' + esc(label) + '</span></li>').join('');
    const nums = [plural(r.sources, 'source') + ' on ' + plural(r.domains, 'site'), plural(r.findings, 'finding'), r.pages + ' of ' + Math.max(r.planned, r.pages) + ' pages']
      .concat(r.snippets ? [plural(r.snippets, 'snippet')] : []).concat(r.openGaps ? ['<span class="warm">' + plural(r.openGaps, 'open gap') + '</span>'] : []);
    const state = done ? '<span class="chip rs-ok">published</span>' : r.active ? '<span class="chip rs-live">running</span>' : r.built ? '<span class="chip">built</span>' : '';
    return '<article class="rs-run" data-slug="' + esc(r.slug) + '">' +
      '<div class="rs-top"><div class="rs-q"><b>' + esc(r.question) + '</b><span class="rs-meta"><span class="chip">' + esc(r.depth) + '</span>' +
      (r.mode === 'kickoff' ? '<span class="chip" title="researched before a goal: its build context is in every agent’s shared prompt once published">before a goal</span>' : '') + state +
      '<span class="mono dim">' + esc(r.slug) + '</span></span></div>' +
      '<div class="rs-act"><a class="btn sm" href="' + esc(wikiURL(S, r.slug)) + '" target="_blank" rel="noopener" title="the wiki as the run stands now, in a new tab">Open wiki ↗</a>' +
      '<button class="btn sm" type="button" data-rs-status title="/research status: what the run has against its bars, and what is next">Status</button>' +
      '<button class="btn sm" type="button" data-rs-export title="/research export: render the wiki into a folder of the project">Export…</button></div></div>' +
      '<ol class="rs-steps">' + steps + '</ol><div class="rs-nums">' + nums.join(' · ') + (r.problems && !done ? ' · <span class="dim">' + plural(r.problems, 'check') + ' to pass</span>' : '') + '</div>' +
      '<form class="rs-exp" data-rs-expf hidden><label class="lab" for="rsx-' + esc(r.slug) + '">Render the wiki into</label><input id="rsx-' + esc(r.slug) + '" class="mono" value="docs/research" spellcheck="false"><button class="btn sm pri" type="submit">Export</button><span class="dim">a folder of the project; an earlier export there is replaced</span></form>' +
      '<pre class="pre rs-out" hidden></pre></article>';
  }

  function depthHtml(d, sel) {
    return '<label class="rs-depth' + (d.id === sel ? ' sel' : '') + '"><input type="radio" name="rsDepth" value="' + esc(d.id) + '"' + (d.id === sel ? ' checked' : '') + '>' +
      '<b>' + esc(d.title || d.id) + '</b><span>' + esc(d.about) + '</span><small class="mono">' + d.tracks + ' researchers · ' + d.sources + '+ sources · ' + esc(d.pages) + ' pages</small></label>';
  }

  SL.views.register({ name: 'research', title: 'Research', mount(sc, root, params, S0) {
    const P = ui.pageHead(sc, root, S0 || SL.sessions.active, 'research', params, { title: 'Research', purpose: 'Deep research runs: a team plans the question from several points of view, researches it on the web in parallel, keeps only quotes it found on their pages, writes a wiki with every claim cited and fact-checks each page before it is published.', action: { label: '+ New research', title: 'ask a question for the team to research' }, onAction: () => { const q = $('#rsQ', root); if (q) q.focus(); } });
    P.fill({ 'research.runs': '<section class="panel"><div class="ph"><h2>Runs</h2><div class="r"><span class="rs-count"></span></div></div><div class="rs-list"></div></section>',
      'research.new': '<section class="panel"><div class="ph"><h2>New research</h2><div class="r"><span class="mono dim">/research DEPTH QUESTION</span></div></div><form class="rs-new" autocomplete="off"><label class="lab" for="rsQ">Question</label><textarea id="rsQ" rows="3" maxlength="2000" placeholder="How do mature sync engines resolve conflicts offline?"></textarea><div class="rs-depths" role="radiogroup" aria-label="depth"></div><div class="row2"><button class="btn pri" type="submit" id="rsGo">Start research</button></div><p class="stubnote">The run is a goal: the manager plans it and the team works it until every check passes. To research before building, pick <b>research first</b> beside the first goal of New session, or <b>Research first</b> in the Goal sheet, or type <span class="mono">/goal --research=standard GOAL</span>.</p></form></section>' });
    const list = $('.rs-list', root), depths = $('.rs-depths', root), st = { runs: [], depths: [], on: true, depth: 'standard', out: {}, exp: {}, seq: 0, sig: '' };
    const S = () => SL.sessions.active;
    function paint() {
      const s = S(); if (!s) return;
      const sig = JSON.stringify([st.runs, st.on]); if (sig === st.sig) return; st.sig = sig;
      $('.rs-count', root).textContent = st.runs.length ? plural(st.runs.length, 'run') : '';
      list.innerHTML = !st.on ? '<p class="stubnote" style="padding:10px 12px;margin:0">The research plugin is off for this session: turn on <b>Deep research</b> in Plugins, then start the team again.</p>'
        : st.runs.length ? st.runs.map(r => runHtml(s, r)).join('') : '<p class="stubnote" style="padding:10px 12px;margin:0">No research yet. Ask a question on the right: a quick run takes a few researchers and a handful of pages; a deep one ten researchers, three rounds and a wiki of twenty pages or more.</p>';
      list.querySelectorAll('[data-slug]').forEach(el => { const k = el.dataset.slug, o = $('.rs-out', el); if (st.out[k] != null) { o.hidden = false; o.textContent = st.out[k]; } if (st.exp[k]) $('[data-rs-expf]', el).hidden = false; });
    }
    function paintDepths() { depths.innerHTML = st.depths.map(d => depthHtml(d, st.depth)).join(''); }
    async function load() {
      const s = S(); if (!s || !net) return; const mine = ++st.seq;
      const r = await net.get(net.tab(s, '/research')); if (!sc.alive || mine !== st.seq) return;
      if (!r.ok || !r.data) return;
      st.runs = arr(r.data.runs); st.on = r.data.on !== false;
      if (!st.depths.length && arr(r.data.depths).length) { st.depths = arr(r.data.depths); paintDepths(); }
      paint();
    }
    async function status(slug, el) {
      const s = S(); const out = $('.rs-out', el); out.hidden = false; out.textContent = '…';
      const r = await net.post(net.tab(s, '/command'), { line: '/research status ' + slug }); if (!sc.alive) return;
      st.out[slug] = r.ok && r.data ? String(r.data.output || '') : (r.message || 'the status could not be read');
      const cur = list.querySelector('[data-slug="' + CSS.escape(slug) + '"] .rs-out'); if (cur) cur.textContent = st.out[slug];
    }
    function exportRun(el) { const f = $('[data-rs-expf]', el); f.hidden = !f.hidden; st.exp[el.dataset.slug] = !f.hidden; if (!f.hidden) $('input', f).focus(); }
    async function doExport(slug, dir) {
      dir = String(dir || '').trim(); if (!dir) return;
      const r = await net.post(net.tab(S(), '/research'), { action: 'export', slug, dir });
      if (r.ok) ui.toast('the wiki is in ' + ((r.data && r.data.dir) || dir), 'ok'); else net.fail(r, 'the wiki was not exported');
    }
    sc.listen(root, 'click', e => {
      const t = e.target, run = t.closest('[data-slug]'); if (!run) return;
      if (t.closest('[data-rs-status]')) { status(run.dataset.slug, run); return; }
      if (t.closest('[data-rs-export]')) exportRun(run);
    });
    sc.listen(root, 'change', e => { const t = e.target; if (t.name === 'rsDepth') { st.depth = t.value; paintDepths(); } });
    sc.listen(root, 'submit', async e => {
      const xf = e.target.closest('[data-rs-expf]'); if (xf) { e.preventDefault(); doExport(xf.closest('[data-slug]').dataset.slug, $('input', xf).value); return; }
      if (!e.target.closest('.rs-new')) return; e.preventDefault();
      const q = $('#rsQ', root), question = q.value.trim(); if (!question) { q.focus(); return; }
      const b = $('#rsGo', root); b.disabled = true;
      const r = await net.post(net.tab(S(), '/research'), { action: 'start', question, depth: st.depth }); b.disabled = false; if (!sc.alive) return;
      if (!r.ok) { net.fail(r, 'the research did not start'); return; }
      q.value = ''; ui.toast('research started: the team plans it now', 'ok'); load();
    });
    sc.interval(load, 5000);
    sc.on('plugins-changed', load);
    paint(); load();
  } });
})(SL);
