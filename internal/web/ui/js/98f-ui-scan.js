/* 98f-ui-scan.js: the Deep scan view. It lists the project's deep scans (GET /api/sessions/ID/scan), shows one in full
 * (GET .../scan/SLUG: its findings with their traces, verdicts and reproductions, its attack surface and the shards' maps),
 * starts a scan of the project (POST .../scan {action: start}) or of a repository it clones first (POST /api/scan/clone, then the
 * New session dialog, filled in, starts a sandboxed team there), and acts on a finding when the person asks: an issue written up
 * for them to read and edit before it is opened, or a goal that has the team fix it. A scan can be rerun (POST .../scan {action:
 * rerun}: the same code at HEAD, optionally pulled first), and the selected scan is compared with an earlier one (GET
 * .../scan/SLUG/compare?since=): its findings as new, fixed and unchanged.
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
  /** The views of a scan's leads the report's filter offers: which statuses each one shows, and what it means. */
  const VIEWS = [['findings', 'Findings', ['confirmed', 'unproven'], 'confirmed by reviewers that did not file them'], ['disputed', 'Disputed', ['disputed'], 'the reviewers did not agree'],
    ['open', 'Being verified', ['open'], 'filed, waiting for enough verdicts'], ['deferred', 'Not verified', ['deferred'], 'recorded after the scan reached its stopping point'],
    ['rejected', 'Rejected', ['rejected'], 'the reviewers showed why they are not real'], ['all', 'Every lead', null, 'every lead the scan recorded']];
  /** A finding's status in the report's words. */
  const statusLabel = f => ({ confirmed: f.reproduced ? 'Confirmed, reproduced' : 'Confirmed', unproven: 'Not reproduced', disputed: 'Disputed', open: 'Being verified', deferred: 'Not verified',
    rejected: 'Rejected' + (f.reject_reason ? ': ' + f.reject_reason : '') })[f.status] || f.status;
  const statusCls = s => (s === 'confirmed' ? 'done' : s === 'rejected' || s === 'deferred' ? 'off' : 'run');
  const sevRank = s => { const i = SEV.indexOf(s); return i < 0 ? SEV.length : i; };
  const leadNum = id => parseInt(String(id || '').replace(/^L/i, ''), 10) || 0;
  /** Where a finding does harm: its anchor on the change in a review, else the last step of its trace. */
  const locOf = f => (f.anchor ? f.anchor.file + ':' + f.anchor.line : sinkOf(f));
  /** The verdicts by role, in order: Defender, Reviewer 1, Reviewer 2. A report never names an agent. */
  const roleLabels = vs => { let n = 0; return arr(vs).map(v => (v.role === 'defender' ? 'Defender' : 'Reviewer ' + (++n))); };
  /** How a finding was checked, in a sentence. */
  function verification(f) {
    const c = f.confirms | 0, r = f.rejects | 0, p = arr(f.proofs);
    let s = !c && !r ? 'Not yet reviewed' : !r ? 'Confirmed by ' + plural(c, 'independent reviewer') : c + ' independent ' + (c === 1 ? 'reviewer' : 'reviewers') + ' confirmed it and ' + r + ' rejected it';
    s += (f.defended ? ', one of them a defender who looked for the reason it is not real' : '') + '.';
    if (f.reproduced) s += ' An independent reproducer showed the harm in the sandbox.';
    else if (p.length) s += ' ' + plural(p.length, 'reproduction') + ' did not show the harm in the sandbox.';
    else if (f.status === 'unproven') s += ' No reproduction was run.';
    return s;
  }
  const short = c => String(c || '').slice(0, 12);
  const sevPill = s => '<span class="sc-sev sc-' + esc(SEV.includes(s) ? s : 'low') + '">' + esc(s || '?') + '</span>';
  /** A finding's CVSS base score with one decimal, or '' for a finding filed without a vector. */
  const scoreOf = f => typeof f.score === 'number' ? f.score.toFixed(1) : '';
  /** A finding's severity pill with its CVSS score beside it. */
  const sevScore = f => {
    const sc = scoreOf(f), capped = sc && sevBand(f.score) !== f.severity;
    return '<span class="sc-sevc">' + sevPill(f.severity) + (sc ? '<b class="mono sc-score' + (capped ? ' sc-capped' : '') + '" title="' +
      (capped ? 'CVSS 3.1 base score ' + sc + ', capped at ' + esc(f.severity) + ': ' + (f.reach === 'unreachable' ? 'no path reaches the code' : 'no practical exploit was shown') : 'CVSS 3.1 base score') + '">' + sc + '</b>' : '') + '</span>';
  };
  /** The rating band of a CVSS base score, as the scan names its severities. */
  const sevBand = s => s >= 9 ? 'critical' : s >= 7 ? 'high' : s >= 4 ? 'medium' : 'low';
  /** Worst first: by severity, then by CVSS score (a scored finding before an unscored one), then in the order they were filed. */
  const worse = (a, b) => sevRank(a.severity) - sevRank(b.severity) || (typeof b.score === 'number' ? b.score : -1) - (typeof a.score === 'number' ? a.score : -1) || leadNum(a.id) - leadNum(b.id);
  /** Where a finding does harm: the last step of its trace. */
  const sinkOf = f => { const t = arr(f.trace); return t.length ? t[t.length - 1].file + ':' + t[t.length - 1].line : ''; };
  /** The kinds of change between two scans, in the order the server gives them, with what each one means. */
  const DELTAS = [['new', 'New', 'found by this scan, not by the earlier one'], ['fixed', 'Fixed', 'found by the earlier scan; this one, finished, did not find it again or its verifiers rejected it'],
    ['pending', 'Not confirmed again yet', 'found by the earlier scan; this one is still running and has not confirmed it'], ['unchanged', 'Unchanged', 'found by both scans']];
  const proofWord = f => { const p = arr(f.proofs); if (!p.length) return ''; return p.some(x => x.outcome === 'reproduced') ? 'reproduced' : p[p.length - 1].outcome; };

  /** How far each phase is, from 0 to 1, from the scan's own numbers: the phases overlap (a shard is hunted as soon as it is
   * mapped, a lead verified as soon as it is filed), so each bar fills as its work is done, not when the phase before ends.
   * The scan's phase is its least finished one; a phase before it is done, whatever its numbers say. */
  function phaseProgress(r) {
    const at = phaseAt(r.phase), done = r.phase === 'done', sh = r.shards | 0, leads = r.leads | 0, ruled = Math.max(0, leads - (r.open | 0));
    const frac = (n, of) => (of > 0 ? Math.min(1, n / of) : 0);
    const own = { map: [frac(r.mapped, sh), r.mapped + '/' + sh + ' shards mapped'], hunt: [frac(r.hunted, sh), r.hunted + '/' + sh + ' shards hunted'],
      verify: [leads ? frac(ruled, leads) : 0, leads ? ruled + '/' + leads + ' leads ruled on' + (r.unproven ? ', ' + r.unproven + ' unproven' : '') : 'no lead filed yet'],
      report: [0, ''], done: [0, ''] };
    return PHASES.map(([id], i) => {
      const [f, note] = own[id] || [0, ''], cur = !done && i === at;
      // the scan's own phase is not done, whatever its numbers say: a confirmed lead may still need its reproduction
      return { f: done || i < at ? 1 : cur ? Math.min(f, 0.99) : f, note, cur };
    });
  }
  /** The steps of a scan, each with its own bar. */
  function stepsHtml(r) {
    return phaseProgress(r).map((p, i) => {
      const [, label, about] = PHASES[i], pct = Math.round(p.f * 100);
      const cls = p.f >= 1 ? 'done' : p.cur || p.f > 0 ? 'cur' : '';
      return '<li class="' + cls + '" title="' + esc(label + (p.note ? ': ' + p.note : '') + ' · ' + about) + '"><i style="--sc-p:' + (p.f >= 1 ? 100 : pct) + '%"></i><span>' + esc(label) +
        (p.f > 0 && p.f < 1 ? ' <b class="sc-pct">' + pct + '%</b>' : '') + '</span></li>';
    }).join('');
  }
  /** The team of a scan: the one this session runs it with, and what the scan asks for when that differs. */
  function teamText(r) {
    if (!r.workers) return 'asks for a team of ' + r.team;
    return 'team of ' + r.workers + (r.workers !== r.team ? ' (the scan asks for ' + r.team + ')' : '');
  }

  function runHtml(r, sel) {
    const done = r.phase === 'done';
    const steps = stepsHtml(r);
    const sev = SEV.filter(s => r.bySeverity && r.bySeverity[s]).map(s => sevPill(s) + '<b class="mono">' + r.bySeverity[s] + '</b>').join(' ');
    return '<button type="button" class="sc-run' + (r.slug === sel ? ' sel' : '') + '" data-scan="' + esc(r.slug) + '" data-key="' + esc(r.slug) + '">' +
      '<span class="sc-rtop"><b class="mono">' + esc(r.slug) + '</b><span class="chip">' + esc(r.depth) + '</span>' + (r.active ? '<span class="chip rs-live">this session</span>' : '') + (r.previous ? '<span class="chip" title="a rerun of ' + esc(r.previous) + '">rerun</span>' : '') + (done ? '<span class="chip rs-ok">done</span>' : '') + '</span>' +
      '<span class="sc-rmeta mono dim">' + esc(short(r.commit)) + (r.focus ? ' · ' + esc(r.focus) : '') + ' · ' + plural(r.files, 'file') + ' · ' + plural(r.funcs, 'function') + ' · ' + plural(r.shards, 'shard') + ' · ' + esc(teamText(r)) + '</span>' +
      '<ol class="rs-steps sc-steps">' + steps + '</ol>' +
      '<span class="sc-rnums">' + r.mapped + '/' + r.shards + ' mapped · ' + r.hunted + '/' + r.shards + ' hunted · ' + plural(r.leads, 'lead') + (r.leads ? ' (' + Math.max(0, r.leads - (r.open | 0)) + ' ruled on)' : '') + ' · ' + r.confirmed + ' confirmed' + (r.unproven ? ' · ' + r.unproven + ' unproven' : '') + (r.rejected ? ' · ' + r.rejected + ' rejected' : '') + '</span>' +
      (sev ? '<span class="sc-rsev">' + sev + '</span>' : '') + '</button>';
  }

  /** One row of the report's index: severity, id, title with its location, category, status. */
  function findingRow(f) {
    return '<button type="button" class="sc-row" data-lead="' + esc(f.id) + '" data-key="' + esc(f.id) + '">' + sevScore(f) + '<span class="mono sc-fid">' + esc(f.id) + '</span>' +
      '<span class="sc-ft"><b>' + esc(f.title) + '</b><span class="mono dim">' + esc(locOf(f)) + '</span></span><span class="sc-rcat">' + esc(f.category) + '</span>' +
      '<span class="sc-st sc-st-' + statusCls(f.status) + '">' + esc(statusLabel(f)) + '</span></button>';
  }

  /** The comparison of the selected scan with an earlier one: a picker of the scan to compare with, the counts, and a row per
   * finding by kind. A new or unchanged finding opens as this scan has it; a fixed or pending one as the earlier scan had it. */
  function compareHtml(d, cmp, runs, since, err) {
    const others = arr(runs).filter(r => r.slug !== d.slug && r.created <= d.created);
    if (!others.length) return '';
    const sel = cmp ? cmp.old : since;
    const opts = '<option value=""' + (!sel ? ' selected' : '') + '>' + (cmp || !err ? 'the scan it reruns, or the last one of the same code' : 'choose a scan') + '</option>' +
      others.map(r => '<option value="' + esc(r.slug) + '"' + (r.slug === sel && since ? ' selected' : '') + '>' + esc(r.slug) + ' · ' + esc(short(r.commit)) + '</option>').join('');
    const pick = '<label class="sc-cmpwith"><span class="dim">compared with</span> <select class="sc-in sc-cmpsel" data-sc-since aria-label="the scan to compare with">' + opts + '</select></label>';
    if (!cmp) return '<section class="sc-cmp"><div class="sc-cmph"><h3>Changes</h3>' + pick + '</div>' + (err ? '<p class="dim">' + esc(err) + '</p>' : '<p class="dim">comparing…</p>') + '</section>';
    const c = cmp.counts || {}, ds = arr(cmp.deltas);
    const sum = DELTAS.filter(([k]) => c[k] || k !== 'pending').map(([k, label]) => '<span class="sc-dn sc-d-' + k + '"><b>' + (c[k] || 0) + '</b>' + esc(label.toLowerCase()) + '</span>').join('');
    const groups = DELTAS.map(([k, title, about]) => {
      const rows = ds.map((x, i) => [x, i]).filter(([x]) => x.kind === k);
      if (!rows.length) return '';
      const html = rows.map(([x, i]) => { const f = x.finding || {}; return '<button type="button" class="sc-f" data-sc-delta="' + i + '">' + sevPill(f.severity) + '<span class="mono sc-fid">' + esc(f.id) + '</span>' +
        '<span class="sc-ft"><b>' + esc(f.title) + '</b><span class="mono dim">' + esc(f.category) + ' · ' + esc(sinkOf(f)) + '</span></span>' + (x.note && x.kind !== 'pending' || /^filed again/.test(x.note || '') ? '<span class="chip">' + esc(x.note) + '</span>' : '') + '</button>'; }).join('');
      return k === 'unchanged' ? '<details class="sc-group"><summary><h3>' + esc(title) + ' <span class="dim">' + rows.length + '</span></h3> <span class="dim">' + esc(about) + '</span></summary>' + html + '</details>'
        : '<section class="sc-group"><h3>' + esc(title) + ' <span class="dim">' + rows.length + '</span></h3><p class="dim">' + esc(about) + '</p>' + html + '</section>';
    }).join('');
    return '<section class="sc-cmp"><div class="sc-cmph"><h3>Changes since <span class="mono">' + esc(cmp.old) + '</span></h3>' + pick + '</div>' +
      '<p class="dim sc-cmpc">commit <span class="mono">' + esc(short(cmp.oldCommit)) + '</span> → <span class="mono">' + esc(short(cmp.newCommit)) + '</span>' + (cmp.final ? '' : ' · this scan is still running: a finding it has not confirmed yet is not counted as fixed') + '</p>' +
      '<div class="sc-dnums">' + sum + '</div>' + (groups || '<p class="dim">Neither scan has a finding.</p>') + '</section>';
  }

  /** The leads the report's filter F lets through, worst first: its view (a set of statuses), the severities left on, a category
   *  and the words searched for (in the id, the title, the category and the location). */
  function filtered(fs, F) {
    const view = (VIEWS.find(v => v[0] === F.view) || VIEWS[0])[2], q = String(F.q || '').trim().toLowerCase();
    return fs.filter(f => (!view || view.includes(f.status)) && !F.off.includes(f.severity) && (!F.cat || f.category === F.cat) &&
      (!q || (f.id + ' ' + f.title + ' ' + f.category + ' ' + locOf(f)).toLowerCase().includes(q)))
      .sort(worse);
  }
  /** The summary of the report: the headline, a tile per severity, the distribution, the categories and what became of the leads. */
  function summaryHtml(d) {
    const sm = d.summary || {}, by = sm.bySeverity || {}, un = sm.unproven || {}, total = sm.findings | 0, c = d.counts || {};
    const tiles = SEV.map(s => '<button type="button" class="sc-tile sc-t-' + s + ((by[s] | 0) ? '' : ' zero') + '" data-sc-only="' + s + '" title="show only the ' + s + ' findings"><b>' + (by[s] | 0) + '</b><span>' + esc(s) + '</span>' +
      ((un[s] | 0) ? '<small>' + un[s] + ' not reproduced</small>' : '') + '</button>').join('') +
      '<div class="sc-tile sc-t-total"><b>' + total + '</b><span>findings</span>' + ((sm.reproduced | 0) ? '<small>' + sm.reproduced + ' reproduced</small>' : '') + '</div>';
    const dist = total ? '<div class="sc-dist" role="img" aria-label="' + esc('findings by severity: ' + SEV.map(s => (by[s] | 0) + ' ' + s).join(', ')) + '">' +
      SEV.filter(s => by[s]).map(s => '<i class="sc-b-' + s + '" style="flex:' + (by[s] | 0) + '" title="' + (by[s] | 0) + ' ' + s + '"></i>').join('') + '</div>' : '';
    const cats = arr(sm.categories), top = cats.length ? cats[0].total : 1;
    const catRows = cats.slice(0, 8).map(cc => '<button type="button" class="sc-crow" data-sc-cat="' + esc(cc.category) + '" title="show only ' + esc(cc.category) + '"><span>' + esc(cc.category) + '</span><span class="sc-cbar2" style="--w:' + Math.max(4, Math.round(cc.total * 100 / top)) + '%">' +
      SEV.filter(s => cc.bySeverity && cc.bySeverity[s]).map(s => '<i class="sc-b-' + s + '" style="flex:' + cc.bySeverity[s] + '"></i>').join('') + '</span><b class="mono">' + cc.total + '</b></button>').join('');
    const leads = [['Filed', c.leads | 0, 'all'], ['Confirmed by review', total, 'findings'], ['Disputed', sm.disputed | 0, 'disputed'], ['Being verified', sm.open | 0, 'open'], ['Not verified', sm.deferred | 0, 'deferred'], ['Rejected', sm.rejected | 0, 'rejected']]
      .filter(([, n], i) => n || i < 2).map(([label, n, v]) => '<button type="button" class="sc-crow" data-sc-view="' + v + '"><span>' + esc(label) + '</span><span class="sc-cbar2" style="--w:' + Math.max(2, Math.round(n * 100 / Math.max(1, c.leads | 0))) + '%"><i class="sc-b-lead"></i></span><b class="mono">' + n + '</b></button>').join('');
    return '<div class="sc-sum"><p class="sc-headline">' + esc(d.headline || '') + '</p><div class="sc-tiles">' + tiles + '</div>' + dist +
      '<div class="sc-cols">' + (catRows ? '<div class="sc-card"><h4>By category' + (cats.length > 8 ? ' <span class="dim">top 8 of ' + cats.length + '</span>' : '') + '</h4>' + catRows + '</div>' : '') +
      '<div class="sc-card"><h4>Leads</h4>' + leads + '</div></div></div>';
  }
  /** The report's filter bar: the words to find, a chip per severity, the view and the category, and how many leads it shows. */
  function toolsHtml(d, F, shown) {
    const fs = arr(d.findings), inView = st => fs.filter(f => !st || st.includes(f.status)).length;
    const cats = [...new Set(fs.map(f => f.category))].sort();
    return '<div class="sc-tools" role="search"><input class="sc-in sc-q" type="search" data-sc-q placeholder="find by id, title, file or category" aria-label="find a lead" spellcheck="false">' +
      '<span class="sc-chips">' + SEV.map(s => '<button type="button" class="sc-chip sc-' + s + (F.off.includes(s) ? ' off' : '') + '" data-sc-sev="' + s + '" aria-pressed="' + !F.off.includes(s) + '">' + esc(s) + '</button>').join('') + '</span>' +
      '<select class="sc-in sc-sel" data-sc-viewsel aria-label="which leads">' + VIEWS.map(([id, label, sts]) => '<option value="' + id + '"' + (F.view === id ? ' selected' : '') + '>' + esc(label) + ' (' + inView(sts) + ')</option>').join('') + '</select>' +
      '<select class="sc-in sc-sel" data-sc-catsel aria-label="category"><option value="">every category</option>' + cats.map(c => '<option value="' + esc(c) + '"' + (F.cat === c ? ' selected' : '') + '>' + esc(c) + '</option>').join('') + '</select>' +
      '<span class="sc-shown dim">' + shown + ' shown' + (F.q || F.off.length || F.cat ? ' · <button type="button" class="lnk" data-sc-clear>clear</button>' : '') + '</span></div>';
  }

  /** The detail of a scan in parts, [key, html] each, so that a part that comes or goes leaves the others in place (partsHtml): the
   *  scan's facts, the report's summary, its filter and index of leads, what the scan still needs and the comparison. */
  function detailParts(d, F) {
    const c = d.counts || {}, fs = arr(d.findings);
    const head = '<div class="sc-dhead"><span class="dim">commit <span class="mono">' + esc(short(d.commit)) + '</span>' + (d.focus ? ' · ' + esc(d.focus) : '') +
      ' · ' + esc(d.depth) + ': ' + plural(d.verifiers, 'reviewer') + ' per lead' + (d.defender ? ' with a defender' : '') + (d.proveFrom ? ', reproduced from ' + esc(d.proveFrom) + ' up' : '') +
      ' · ' + [[c.files, 'file'], [c.lines, 'line'], [c.points, 'point'], [c.edges, 'edge']].map(([n, w]) => plural(n | 0, w).replace(/^\d+/, m => Number(m).toLocaleString('en-US'))).join(' · ') + ' · ' + esc(teamText(d)) + '</span>' +
      (d.proveFrom && !d.sandbox ? '<p class="sc-warn">This session has no sandbox, so nothing can be reproduced here: start one with <span class="mono">--sandbox docker</span> (the <b>sandbox</b> field of New session).</p>' : '') + '</div>';
    const probs = arr(d.problems);
    const needs = d.phase === 'done' ? '' : '<details class="sc-needs"><summary>What the scan still needs <span class="dim">' + (probs.length + (d.moreProblems || 0)) + '</span>' + intakeText(d) + '</summary><ul>' +
      probs.map(p => '<li><span class="chip">' + esc(p.phase) + '</span> ' + esc(p.text) + '</li>').join('') + (d.moreProblems ? '<li class="dim">and ' + d.moreProblems + ' more</li>' : '') + '</ul></details>';
    if (!fs.length) return [['head', head], ['needs', needs], ['cmp', null], ['rows', '<p class="stubnote">No lead yet: the team maps the shards first, then hunts them. Findings appear here as soon as reviewers confirm them.</p>']];
    const list = filtered(fs, F), more = list.length - F.limit;
    const rows = list.length ? '<div class="sc-rows" role="list">' + list.slice(0, F.limit).map(findingRow).join('') + '</div>' + (more > 0 ? '<button type="button" class="btn sm sc-more2" data-sc-more>Show ' + Math.min(more, 100) + ' more of ' + more + '</button>' : '')
      : '<p class="stubnote">No lead matches the filter.</p>';
    return [['head', head], ['sum', summaryHtml(d)], ['needs', needs], ['cmp', null], ['tools', toolsHtml(d, F, list.length)], ['rows', rows],
      ['files', '<p class="dim sc-files">The report is rewritten as the scan goes: <span class="mono">' + esc(d.reportDir) + '</span> (REPORT.md, report.html, findings.json, findings.sarif, map.json).</p>']];
  }
  /** Where the lead intake stands, after what the scan still needs: the budget, and the late leads once every shard is hunted. */
  function intakeText(d) {
    const i = d.intake; if (!i || !i.budget) return '';
    return ' <span class="dim">· ' + i.verified + ' of a budget of ' + i.budget + ' leads verified' + (i.allowance ? (i.closed ? ' · no more late leads verified' : ' · ' + i.late + ' of ' + i.allowance + ' late leads') : '') + (i.deferred ? ' · ' + i.deferred + ' not verified' : '') + '</span>';
  }

  /** The attack surface and the map in parts: each lane and the shards scroll on their own and fold, so that a large codebase's
   * surface does not push its map out of reach. */
  function surfaceParts(d) {
    const pts = arr(d.points), shards = arr(d.shards);
    const lanes = LANES.map(([id, title]) => {
      const ps = pts.filter(p => p.lane === id);
      return '<div class="sc-lane"><h4>' + esc(title) + ' <span class="dim">' + ps.length + '</span></h4><div class="sc-lpts">' + (ps.length ? ps.map(p => '<div class="sc-pt" title="' + esc(p.note || '') + '"><span class="mono dim">' + esc(p.id) + '</span> ' + esc(p.title) +
        '<span class="mono dim sc-loc">' + esc(p.file) + ':' + (p.line | 0) + '</span></div>').join('') : '<div class="dim sc-none">none yet</div>') + '</div></div>';
    }).join('');
    const cells = shards.map(s => {
      const st = s.hunt ? 'hunted' : s.map ? 'mapped' : s.claimed ? 'claimed' : 'open';
      const tip = s.id + ' · ' + s.first + (s.files > 1 ? ' and ' + (s.files - 1) + ' more' : '') + ' · ' + s.funcs + ' functions, ' + s.lines + ' lines · ' + st + (s.claimed ? ' by ' + s.claimed : '') + (s.map ? '\n\n' + s.map : '');
      return '<span class="sc-cell sc-' + st + '" title="' + esc(tip) + '">' + esc(s.id) + '</span>';
    }).join('');
    const n = st => shards.filter(s => (s.hunt ? 'hunted' : s.map ? 'mapped' : s.claimed ? 'claimed' : 'open') === st).length;
    return [['lanes', '<details class="sc-fold" open><summary><h4 class="sc-sh">Attack surface <span class="dim">' + plural(pts.length, 'point') + '</span></h4></summary><div class="sc-lanes">' + lanes + '</div></details>'],
      ['shards', '<details class="sc-fold" open><summary><h4 class="sc-sh">Shards <span class="dim">' + n('open') + ' open · ' + n('claimed') + ' claimed · ' + n('mapped') + ' mapped · ' + n('hunted') + ' hunted</span></h4></summary><div class="sc-cells" >' + cells + '</div></details>']];
  }

  /** Parts as markup: each in a box of its own keyed by its name, so that a part that comes or goes does not shift the others (U.morph
   * matches them by key). A part whose html is null is filled by another painter (data-morph-own); an empty one is left out. */
  const partsHtml = parts => parts.filter(([, html]) => html !== '').map(([k, html]) => '<div class="sc-part" data-key="' + k + '">' +
    (html === null ? '<div data-sc-' + k + ' data-morph-own></div>' : html) + '</div>').join('');

  /** The scan's report, rendered by the server from the ledger as it is now: a download per format, and PDF through the browser's print. */
  const exportURL = (S, slug, fmt) => '/api/sessions/' + encodeURIComponent(String(S.id)) + '/scan/' + encodeURIComponent(slug) + '/export/' + fmt;
  function exportHtml(S, slug) {
    if (!S) return '';
    const a = (fmt, label, about) => '<a class="btn sm" href="' + esc(exportURL(S, slug, fmt)) + '" download title="' + esc(about) + '">' + esc(label) + '</a>';
    return '<span class="sc-exp"><a class="btn sm" href="' + esc(exportURL(S, slug, 'html')) + '" target="_blank" rel="noopener" title="the whole report as a document, in a new tab: summary, index, every finding, appendices">Full report ↗</a>' + a('csv', 'CSV', 'every lead, one row each, for a spreadsheet or a tracker') + a('json', 'JSON', 'findings.json: the findings with their traces, verdicts and reproductions') +
      '<button class="btn sm" type="button" data-sc-pdf title="the full report, laid out for print: choose Save as PDF in the print dialog">PDF</button>' +
      '<details class="sc-more"><summary class="btn sm" title="more formats">⋯</summary><div class="sc-menu">' + a('md', 'Markdown', 'REPORT.md') + a('sarif', 'SARIF', 'findings.sarif, for code scanning') + '</div></details></span>';
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
  function openFinding(S, d, f, reload, from) {
    const v = arr(f.verdicts), p = arr(f.proofs), actionable = !from && (f.status === 'confirmed' || f.status === 'unproven');
    const trace = arr(f.trace).map((s, i) => '<li><div class="sc-step"><span class="mono">' + (i + 1) + '. ' + esc(s.file) + ':' + (s.line | 0) + '</span> ' + esc(s.note) + '</div><pre class="pre sc-q">' + esc(s.quote) + '</pre></li>').join('');
    const labels = roleLabels(v);
    const verdicts = v.map((x, i) => '<li><b>' + esc(labels[i]) + '</b> ' + (x.confirm ? '<span class="chip rs-ok">confirms</span>' : '<span class="chip">rejects: ' + esc(x.reason || '') + '</span>') + ' ' + esc(x.note) + '</li>').join('');
    const proofs = p.map(x => '<details class="sc-proof"><summary><b>' + esc(x.outcome) + '</b> <span class="dim">' + (x.independent ? 'independent' : 'by the reviewer who filed it, so it does not count') + ', exit ' + (x.exit | 0) + ', ' + (x.seconds | 0) + 's' + (x.network ? ', with network' : '') + '</span></summary><h5>Script</h5><pre class="pre sc-q">' + esc(x.script) + '</pre><h5>Output</h5><pre class="pre sc-q">' + esc(x.output) + '</pre></details>').join('');
    ui.modal({ title: f.id + ' · ' + f.title, kicker: f.severity + ' · ' + statusLabel(f), wide: true, color: f.severity === 'critical' ? 'var(--err)' : f.severity === 'high' ? 'var(--c-rv)' : 'var(--warm)', body:
      '<div class="sc-dlg"><dl class="sc-facts"><div><dt>Severity</dt><dd>' + sevScore(f) + '</dd></div><div><dt>Status</dt><dd><span class="sc-st sc-st-' + statusCls(f.status) + '">' + esc(statusLabel(f)) + '</span></dd></div>' +
      '<div><dt>Category</dt><dd>' + esc(f.category) + '</dd></div><div><dt>Location</dt><dd class="mono">' + esc(locOf(f)) + '</dd></div>' +
      (f.cvss ? '<div class="sc-cvss"><dt>CVSS 3.1</dt><dd><a class="mono" target="_blank" rel="noopener noreferrer" href="' + esc('https://www.first.org/cvss/calculator/3.1#' + f.cvss) + '">' + esc(f.cvss) + '</a>' + (sevBand(f.score) !== f.severity ? ' <span class="dim">base score ' + scoreOf(f) + ', capped at ' + esc(f.severity) + ': ' + (f.reach === 'unreachable' ? 'no path reaches the code' : 'no practical exploit was shown') + '</span>' : '') + '</dd></div>' : '<div><dt>Impact</dt><dd>' + esc(f.impact) + '</dd></div>') +
      '<div><dt>Reachability</dt><dd>' + esc(f.reach) + '</dd></div><div><dt>Exploitability</dt><dd>' + esc(f.exploit) + '</dd></div></dl>' +
      '<h4>Description</h4><p class="sc-claim">' + esc(f.claim) + '</p>' +
      '<h4>Trace</h4><ol class="sc-trace">' + trace + '</ol><h4>Verification</h4><p class="sc-ver">' + esc(verification(f)) + '</p>' + (verdicts ? '<ul class="sc-verdicts">' + verdicts + '</ul>' : '') + (proofs ? '<h4>Reproductions</h4>' + proofs : '') +
      (f.deferred ? '<h4>Not verified</h4><p>' + esc(f.deferred) + '.</p>' : '') +
      (f.fix ? '<h4>Remediation</h4><p>' + esc(f.fix) + '</p>' : '') +
      (actionable ? '<div class="row2 sc-acts"><button class="btn" type="button" data-sc-issue>Open issue…</button><button class="btn" type="button" data-sc-fix>Fix…</button></div>' : from ? '<p class="stubnote">As the earlier scan <span class="mono">' + esc(from) + '</span> found it.</p>' : '<p class="stubnote">Only a confirmed finding can be written up or fixed.</p>') +
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
      'scan.findings': '<section class="panel"><div class="ph"><h2>Report</h2><div class="r"><span class="sc-phase"></span></div></div><div class="sc-detail"></div></section>',
      'scan.surface': '<section class="panel"><div class="ph"><h2>Attack surface and map</h2></div><div class="sc-surface"></div></section>',
      'scan.new': '<section class="panel"><div class="ph"><h2>New scan</h2><div class="r"><span class="mono dim">/scan DEPTH [DIR]</span></div></div><form class="rs-new sc-new" autocomplete="off">' +
        '<div class="sc-src" role="radiogroup" aria-label="what to scan"><label class="chk"><input type="radio" name="scSrc" value="here" checked> this project</label><label class="chk"><input type="radio" name="scSrc" value="url"> a repository, cloned for the scan</label></div>' +
        '<div data-sc-here><label class="lab" for="scFocus">Folder (optional)</label><input id="scFocus" class="sc-in mono" placeholder="the whole repository; or e.g. server/api" spellcheck="false"></div>' +
        '<div data-sc-url hidden><label class="lab" for="scUrl">Repository</label><input id="scUrl" class="sc-in mono" placeholder="https://github.com/owner/repo" spellcheck="false"><p class="dim sc-hint">Cloned into ~/sleipnir-scans with git’s hooks and symbolic links off. A new session opens there, its commands in a container with the toolchain of the repository’s language and no network (a Go module’s dependencies are fetched first).</p><p class="sc-warn" data-sc-nobox hidden></p></div>' +
        '<div class="rs-depths" role="radiogroup" aria-label="depth"></div><div class="row2"><button class="btn pri" type="submit" id="scGo">Start the scan</button></div>' +
        '<div class="sc-clone" data-sc-clone role="status" aria-live="polite" hidden><div class="sc-cbar"><i></i></div><div class="sc-cline"><b data-sc-cphase></b> <span class="mono dim" data-sc-cline></span> <span class="mono dim sc-ctime" data-sc-ctime></span></div></div>' +
        '<p class="stubnote">The work is sized by the code, not by a clock: the scan splits it into shards of so many functions and lines, and asks for one agent per two shards, from 8 to 32.</p></form></section>' });
    const st = { runs: [], depths: [], on: true, sandbox: false, box: null, depth: 'standard', sel: (params && params.scan) || '', detail: null, seq: 0, dseq: 0, sig: '', dsig: '', cmp: null, cmpErr: '', since: '', cseq: 0, csig: '',
      F: { q: '', off: [], view: 'findings', cat: '', limit: 60 } };
    const PAGE = 60, resetF = () => { st.F = { q: '', off: [], view: 'findings', cat: '', limit: PAGE }; const q = $('[data-sc-q]', root); if (q) q.value = ''; };
    const S = () => SL.sessions.active;
    const list = $('.sc-list', root), det = $('.sc-detail', root), surf = $('.sc-surface', root), depths = $('.rs-depths', root);
    function paint() {
      if (!list) return;
      const sig = JSON.stringify([st.runs, st.on, st.sel]); if (sig === st.sig) return; st.sig = sig;
      const cnt = $('.sc-count', root), count = st.runs.length ? plural(st.runs.length, 'scan') : ''; if (cnt && cnt.textContent !== count) cnt.textContent = count;
      U.morph(list, !st.on ? '<p class="stubnote" style="padding:10px 12px;margin:0">The deep scan plugin is off for this session: turn on <b>Deep scan</b> in <button class="lnk" type="button" data-sc-plugins>Plugins</button>, then start the session again, with <span class="mono">--sandbox docker</span> so that leads can be reproduced.</p>'
        : st.runs.length ? st.runs.map(r => runHtml(r, st.sel)).join('') : '<p class="stubnote" style="padding:10px 12px;margin:0">No scan yet. Start one from New scan: this project at its HEAD, or a repository cloned for it.</p>');
    }
    function paintDetail() {
      const d = st.detail, sig = JSON.stringify([d, st.F]); if (sig === st.dsig) return; st.dsig = sig;
      if (det) U.morph(det, d ? partsHtml(detailParts(d, st.F)) : '<p class="stubnote" style="padding:10px 12px;margin:0">' + (st.runs.length ? 'Pick a scan to see its findings.' : 'The findings of a scan appear here as verifiers confirm them.') + '</p>');
      if (surf) U.morph(surf, d ? partsHtml(surfaceParts(d)) : '');
      const ph = $('.sc-phase', root); if (ph) U.morph(ph, d ? '<span class="chip' + (d.phase === 'done' ? ' rs-ok' : ' rs-live') + '">' + esc(d.phase) + '</span>' +
        '<button class="btn sm sc-rerun" type="button" data-sc-rerun title="scan the same code again at HEAD, then compare the two">Rerun…</button>' + exportHtml(S(), d.slug) : '');
      st.csig = ''; paintCompare();
    }
    function paintCompare() {
      const box = $('[data-sc-cmp]', root), d = st.detail; if (!box || !d) return;
      const sig = JSON.stringify([st.cmp, st.cmpErr, st.since, st.runs.map(r => r.slug)]); if (sig === st.csig) return; st.csig = sig;
      U.morph(box, compareHtml(d, st.cmp, st.runs, st.since, st.cmpErr));
    }
    async function loadCompare() {
      const s = S(), d = st.detail; if (!s || !net || !d) return;
      const mine = ++st.cseq, slug = d.slug;
      if (!st.since && !d.previous && !st.runs.some(r => r.slug !== slug && r.created <= d.created && !r.review)) { st.cmp = null; st.cmpErr = ''; paintCompare(); return; }
      const r = await net.get(net.tab(s, '/scan/' + encodeURIComponent(slug) + '/compare' + (st.since ? '?since=' + encodeURIComponent(st.since) : '')));
      if (!sc.alive || mine !== st.cseq || !st.detail || st.detail.slug !== slug) return;
      st.cmp = r.ok && r.data ? r.data : null; st.cmpErr = r.ok ? '' : (r.message || 'no earlier scan to compare with');
      paintCompare();
    }
    /** Rerun the selected scan: the same code at HEAD, after a fast-forward from the remote when asked (a repository cloned for
     * a scan is pulled by default, the project's own checkout is not). */
    function rerunDialog(d) {
      const cloned = /[\\/]sleipnir-scans[\\/]/.test(String(d.reportDir || ''));
      ui.modal({ title: 'Rerun ' + d.slug, kicker: 'deep scan', body: '<div class="sc-dlg"><p>A new scan of the same code' + (d.focus ? ' (<span class="mono">' + esc(d.focus) + '</span>)' : '') + ' at ' + esc(d.depth) + ' depth, taken at the repository’s HEAD now' + (d.base ? ', reviewing the change from the same base' : '') + '. ' +
        'Its brief lists what this scan found so that the team checks each finding again, and the two are compared: new, fixed and unchanged findings.</p>' +
        '<label class="chk"><input type="checkbox" data-sc-pull' + (cloned ? ' checked' : '') + '> update the repository from its remote first (<span class="mono">git pull --ff-only</span>)</label>' +
        '<div class="row2"><button class="btn pri" type="button" data-sc-rergo>Rerun the scan</button><span class="dim" data-sc-rersaid></span></div></div>',
      onMount(body, msc) {
        msc.listen(body, 'click', async e => {
          const b = e.target.closest('[data-sc-rergo]'); if (!b) return;
          const said = $('[data-sc-rersaid]', body); b.disabled = true; said.textContent = 'taking the inventory…';
          const r = await net.post(net.tab(S(), '/scan'), { action: 'rerun', slug: d.slug, pull: !!$('[data-sc-pull]', body).checked }, { timeout: 15 * 60 * 1000 }); if (!msc.alive) return;
          b.disabled = false;
          if (!r.ok) { said.textContent = r.message || 'the scan was not rerun'; return; }
          ui.toast(r.data && r.data.queued ? 'the rerun is queued after the current goal' : 'the rerun started: the team checks the earlier findings again', 'ok'); ui.closeModal();
          st.sel = (r.data && r.data.slug) || ''; st.since = ''; st.sig = ''; load();
        });
      } });
    }
    function paintDepths() { if (depths) U.morph(depths, st.depths.map(x => depthHtml(x, st.depth)).join('')); }
    async function loadDetail() {
      const s = S(); if (!s || !net || !st.sel) { st.detail = null; paintDetail(); return; }
      const mine = ++st.dseq, r = await net.get(net.tab(s, '/scan/' + encodeURIComponent(st.sel))); if (!sc.alive || mine !== st.dseq) return;
      if (r.ok && r.data) { const fresh = !st.detail || st.detail.slug !== r.data.slug; st.detail = r.data; if (fresh) { st.cmp = null; st.cmpErr = ''; resetF(); } paintDetail(); loadCompare(); }
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
    /* whether this machine can give the scan's session its sandbox: said before the clone, not after the team is chosen */
    async function loadBox() {
      const r = await net.get('/api/sandbox'); if (!sc.alive || !r.ok || !r.data) return;
      st.box = r.data; const w = $('[data-sc-nobox]', root); if (!w) return;
      w.hidden = !!st.box.available;
      w.textContent = st.box.available ? '' : 'No sandbox can start on this machine: ' + (st.box.why || 'the docker command does not answer') + '. Install it to scan in a container, or start the session without the sandbox from the New session dialog: its commands then run on this machine, and no lead can be reproduced.';
    }
    /* the clone's progress under the form while the request runs: git's own lines when the server follows them, the time either way */
    function cloneStatus(url) {
      const box = $('[data-sc-clone]', root), bar = box && $('.sc-cbar i', box), id = 'c' + Date.now().toString(36) + Math.random().toString(36).slice(2, 8);
      const t0 = Date.now(); let stopped = false, timer = 0;
      const mmss = s => Math.floor(s / 60) + ':' + String(s % 60).padStart(2, '0');
      const show = p => {
        if (!box || stopped) return;
        const phase = p && p.phase === 'inventory' ? 'Taking the inventory' : 'Cloning ' + url;
        $('[data-sc-cphase]', box).textContent = phase + '…';
        $('[data-sc-cline]', box).textContent = p && p.phase !== 'inventory' && p.line ? p.line : '';
        $('[data-sc-ctime]', box).textContent = mmss(Math.round((Date.now() - t0) / 1000));
        const pct = p && p.percent >= 0 ? p.percent : -1;
        box.classList.toggle('sc-indet', pct < 0); if (bar) bar.style.width = pct < 0 ? '' : pct + '%';
      };
      let last = null;
      const tick = async () => {
        const r = await net.get('/api/scan/clone/progress?id=' + encodeURIComponent(id), { timeout: 5000 });
        if (stopped || !sc.alive) return;
        if (r.ok && r.data) last = r.data;
        show(last); timer = sc.timeout(tick, 800);
      };
      if (box) { box.hidden = false; show(null); timer = sc.timeout(tick, 400); }
      return { id, stop() { stopped = true; clearTimeout(timer); if (box) box.hidden = true; } };
    }
    async function start(form) {
      const src = (form.querySelector('[name=scSrc]:checked') || {}).value, b = $('#scGo', root); b.disabled = true;
      try {
        if (src === 'url') {
          const url = $('#scUrl', root).value.trim(); if (!url) { $('#scUrl', root).focus(); return; }
          const prog = cloneStatus(url); b.textContent = 'Cloning…';
          let r; try { r = await net.post('/api/scan/clone', { url, depth: st.depth, progress: prog.id }, { timeout: 20 * 60 * 1000 }); } finally { prog.stop(); b.textContent = 'Start the scan'; }
          if (!sc.alive) return;
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
      if (t.closest('[data-sc-rerun]') && st.detail) { rerunDialog(st.detail); return; }
      const dl = t.closest('[data-sc-delta]');
      if (dl && st.cmp && st.detail) {
        const x = arr(st.cmp.deltas)[dl.dataset.scDelta | 0]; if (!x) return;
        const mine = x.kind === 'new' || x.kind === 'unchanged', f = mine ? arr(st.detail.findings).find(y => y.id === x.finding.id) || x.finding : x.finding;
        openFinding(S(), st.detail, f, load, mine ? '' : st.cmp.old); return;
      }
      const run = t.closest('[data-scan]'); if (run) { st.sel = run.dataset.scan; st.sig = ''; paint(); st.detail = null; st.dsig = ''; st.since = ''; st.cmp = null; st.cmpErr = ''; loadDetail(); return; }
      const fl = t.closest('[data-lead]'); if (fl && st.detail) { const f = arr(st.detail.findings).find(x => x.id === fl.dataset.lead); if (f) openFinding(S(), st.detail, f, load); return; }
      /* the report's filter: a severity chip toggles, a tile shows that severity alone, a category or a row of the leads narrows the index */
      const F = st.F, sv = t.closest('[data-sc-sev]'), only = t.closest('[data-sc-only]'), cat = t.closest('[data-sc-cat]'), view = t.closest('[data-sc-view]');
      if (sv) { const s = sv.dataset.scSev; F.off = F.off.includes(s) ? F.off.filter(x => x !== s) : F.off.concat(s); }
      else if (only) { const s = only.dataset.scOnly; F.off = F.off.length === SEV.length - 1 && !F.off.includes(s) ? [] : SEV.filter(x => x !== s); F.view = 'findings'; }
      else if (cat) { F.cat = F.cat === cat.dataset.scCat ? '' : cat.dataset.scCat; F.view = 'findings'; }
      else if (view) F.view = view.dataset.scView;
      else if (t.closest('[data-sc-more]')) { F.limit += 100; paintDetail(); return; }
      else if (t.closest('[data-sc-clear]')) resetF();
      else return;
      F.limit = PAGE; paintDetail();
    });
    sc.listen(root, 'input', e => { if (!e.target.matches('[data-sc-q]')) return; st.F.q = e.target.value; st.F.limit = PAGE; paintDetail(); });
    sc.listen(root, 'change', e => {
      const t = e.target;
      if (t.name === 'scDepth') { st.depth = t.value; paintDepths(); }
      if (t.matches('[data-sc-viewsel]')) { st.F.view = t.value; st.F.limit = PAGE; paintDetail(); }
      if (t.matches('[data-sc-catsel]')) { st.F.cat = t.value; st.F.limit = PAGE; paintDetail(); }
      if (t.matches('[data-sc-since]')) { st.since = t.value; st.cmp = null; st.cmpErr = ''; st.csig = ''; paintCompare(); loadCompare(); }
      if (t.name === 'scSrc') { $('[data-sc-here]', root).hidden = t.value !== 'here'; $('[data-sc-url]', root).hidden = t.value !== 'url'; }
    });
    sc.listen(root, 'submit', e => { if (!e.target.closest('.sc-new')) return; e.preventDefault(); start(e.target.closest('.sc-new')); });
    sc.interval(load, 5000);
    sc.on('plugins-changed', load);
    paint(); paintDetail(); load(); loadBox();
  } });
})(SL);
