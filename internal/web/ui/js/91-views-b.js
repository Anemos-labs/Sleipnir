/* 91-views-b.js: the Sessions view (live sessions, recorded sessions, prune, delete selected), and SL.c3, the small toolkit the pages of Settings,
 * Tools, the Runner and Sessions share (the API calls with their confirmations, the page caches, one honest way to say a request failed).
 *
 * Every number and name on this page comes from the server through SL.sessions (live tabs, the recorded list) or through the calls below; a
 * string from a session, a prompt, a branch name or a server message is data and goes through esc() or textContent. */
(function (SL) {
  'use strict';
  const U = SL.u, { $, $$, esc, fmtUsd, hitCls, agCol } = U, D = SL.D, calc = SL.calc, ui = SL.ui = SL.ui || {}, G = SL.G;
  const reg = SL.views.register, GL = { run: '●', ask: '?', idle: '◌', done: '✓', paused: '⏸' }, WORD = { run: 'running', ask: 'needs you', idle: 'at the prompt', done: 'done', paused: 'paused' };
  const age = s => s < 86400 ? Math.max(1, Math.round(s / 3600)) + 'h' : Math.round(s / 86400) + 'd';
  const plural = (n, w) => n + ' ' + w + (n === 1 ? '' : 's');
  const mb1 = n => (Number(n) || 0).toFixed(1);

  /* ======================================================================== SL.c3: the shared toolkit */
  const C3 = SL.c3 = SL.c3 || {};
  const NOAPI = { ok: false, status: 0, code: 'no_api', message: 'the server is not reachable' };
  const api = () => SL.api || null;
  /** The API calls of the pages: the same shape as SL.api (never throws), or a failure when this page has no server behind it. */
  const net = C3.net = {
    get: (path, opts) => api() ? api().get(path, opts) : Promise.resolve(NOAPI),
    post: (path, body, opts) => api() ? api().post(path, body, opts) : Promise.resolve(NOAPI),
    put: (path, body, opts) => api() ? api().put(path, body, opts) : Promise.resolve(NOAPI),
    patch: (path, body, opts) => api() ? api().patch(path, body, opts) : Promise.resolve(NOAPI),
    del: (path, body, opts) => api() ? api().del(path, body, opts) : Promise.resolve(NOAPI),
    /** a path of the active tab (or of S): /api/sessions/ID + suffix */
    tab: (S, suffix) => '/api/sessions/' + encodeURIComponent(String((S || SL.sessions.active || {}).id || '')) + (suffix || ''),
    /** the first 16 hex of SHA-256 over the canonical JSON of v (the digest of a confirmation scope) */
    d16: v => api() && api().d16 ? api().d16(v) : Promise.resolve(''),
    /** a request that failed: nothing to say when the page has no server (the static pages of development) or the server is gone (the connection chip says it) */
    quiet: r => !r || r.ok || r.status === 0 || r.status === 404 || r.code === 'aborted',
    /** say why a request failed, in the server's own sentence: a toast (warm for a state that does not allow it now, err otherwise) */
    fail(r, fallback) {
      if (!r || r.ok || r.code === 'aborted') return;
      ui.toast((r.message || fallback || 'the request failed'), r.status === 409 || r.status === 403 || r.status === 400 ? 'warm' : 'err');
    },
  };
  /** Tell every view that a shared cache changed, so that the pages draw it again. */
  C3.changed = topic => { G.ver++; if (SL.loop) SL.loop.dirty = true; if (topic) SL.bus.emit(topic); };
  /** The server's idea of this page, from the hello when the live core keeps it. */
  C3.hello = () => (SL.live && SL.live.hello) || null;
  /** Fetch the recorded sessions again (after a prune, a delete, a tab closing): the data layer owns the cache. */
  C3.refreshRecorded = function () { return SL.data ? SL.data.load('recorded', { force: true }) : Promise.resolve(false); };

  /**
   * The frames of one started run. A run's frames travel on the page's stream and can arrive before the answer that names its id, so a
   * tracker is armed before the request (it keeps every frame of every run), then begin(id) hands it the frames of its own run in order;
   * after that only frames of that id count. h = {line(l), step(s), verdict(v), result(r)}.
   */
  C3.tracker = function (h) {
    let id = null, buf = null, done = false;
    const apply = d => {
      if (done) return;
      if (d.lines) d.lines.forEach(l => h.line && h.line(l));
      if (d.step && h.step) h.step(d.step);
      if (d.verdict && h.verdict) h.verdict(d.verdict);
      if (d.result) { done = true; if (h.result) h.result(d.result); }
    };
    return {
      get id() { return id; }, get done() { return done; },
      /** call before the request that starts the run */
      arm() { id = null; done = false; buf = []; },
      /** the run has its id: replay the frames that came first (o.onlyResult: only its end, for a run whose output was fetched separately) */
      begin(i, o) { id = i; const q = buf || []; buf = null; q.forEach(d => { if (d && d.id === id && (!(o && o.onlyResult) || d.result)) apply(d); }); },
      /** the request failed: drop what was kept */
      abort() { id = null; buf = null; done = true; },
      /** a "run" frame from the stream */
      frame(d) { if (!d || typeof d !== 'object' || done) return; if (id == null) { if (buf && buf.length < 5000) buf.push(d); return; } if (d.id === id) apply(d); },
    };
  };
  /** Ask the server to stop a run (D-05); the answer is the run's own result frame. */
  C3.cancelRun = id => id ? net.del('/api/runs/' + encodeURIComponent(id)) : Promise.resolve(NOAPI);

  /* ======================================================================== SESSIONS: pure parts */
  /** The ids of the sessions that tabs of this page host: a recorded row of one of them cannot be deleted from here. */
  const hostedSids = () => new Set(SL.sessions.list.map(S => S.sid || (S.meta && S.meta.sid)).filter(Boolean));
  /** A recorded session another program is writing right now (locked, or written within the last ten seconds). */
  const isLive = (r, now) => !!(r.live || r.locked || (r.lastWritten && (now == null ? Date.now() : now) - r.lastWritten < 10000));
  /** Why a recorded row cannot be deleted from here, or '' */
  const whyNotDeletable = (r, hosted) => hosted.has(r.id) ? 'open in a tab: close it first' : r.locked ? 'another process is writing it' : '';

  /** One row of the recorded table. */
  function recRow(r, o) {
    const why = whyNotDeletable(r, o.hosted), live = isLive(r, o.now), ig = r.integration && r.integration.applied === false ? r.integration : null;
    return '<tr data-rid="' + esc(r.id) + '"><td><label class="chk" style="margin:0" title="' + esc(why || 'select it to delete it') + '"><input type="checkbox" data-pick="' + esc(r.id) + '"' + (o.sel.has(r.id) ? ' checked' : '') + (why ? ' disabled' : '') + '><span class="sr">select ' + esc(r.id) + '</span></label></td>' +
      '<td>' + (r.resumable ? '<span class="res">↺</span>' : '<span class="dim">·</span>') + '</td><td class="mono dim"' + (r.name ? ' title="' + esc(r.name) + '"' : '') + '>' + esc(r.id) + '</td>' +
      '<td class="bright" title="' + esc((r.model || '') + (r.cwd ? ' · ' + r.cwd : '')) + '">' + (r.name ? '<span class="dim">' + esc(r.name) + ' · </span>' : '') + esc(r.first) + (r.interrupted ? ' <span class="warm">⚠</span>' : '') + (ig ? ' <span class="warm" title="' + esc(ig.message || 'its verified work was not applied') + '">⚠ not applied</span>' : '') + '</td>' +
      '<td class="r">' + esc(r.agents) + '</td><td class="r">' + fmtUsd(Number(r.cost) || 0, 2) + '</td><td class="r">' + mb1(r.mb) + '</td><td>' + age(Number(r.ageS) || 0) + '</td>' +
      '<td class="r nowrap"><button class="btn sm pri" type="button" data-res="' + esc(r.id) + '"' + (r.resumable ? '' : ' disabled') + '>↺ Resume</button> ' +
      (live ? '<button class="btn sm" type="button" data-watch="' + esc(r.id) + '" title="the run belongs to another process: read-only">Watch</button>' : '<button class="btn sm" type="button" data-rep="' + esc(r.id) + '">Replay</button>') +
      (ig && ig.hint ? ' <button class="btn sm" type="button" data-hint="' + esc(r.id) + '" title="' + esc(ig.hint) + '">Copy the command</button>' : '') + '</td></tr>';
  }
  /** The recorded table; o = {sel: Set of ids, hosted: Set of sids, now}. */
  function recHtml(list, o) {
    const sel = o.sel, can = list.filter(r => !whyNotDeletable(r, o.hosted)), all = can.length > 0 && can.every(r => sel.has(r.id));
    return '<table class="tbl"><thead><tr><th><label class="chk" style="margin:0" title="select every session that can be deleted"><input type="checkbox" data-pickall' + (all ? ' checked' : '') + (can.length ? '' : ' disabled') + '><span class="sr">select every session that can be deleted</span></label></th><th></th><th>session</th><th>first prompt</th><th class="r">agents</th><th class="r">cost</th><th class="r">MB</th><th>age</th><th></th></tr></thead><tbody>' + list.map(r => recRow(r, o)).join('') + '</tbody></table>';
  }
  /** The text of the prune preview (the CLI's dry run); plan = {list, mb} or {error}. */
  function pruneText(plan) {
    if (plan.error) return plan.error;
    return 'would delete ' + plural(plan.list.length, 'session') + ' (' + mb1(plan.mb) + ' MB):\n' + (plan.list.map(s => '  ' + s.id + '  ' + age(Number(s.ageS) || 0) + '  ' + mb1(s.mb) + ' MB').join('\n') || '  none') + '\n\nnothing is deleted without --yes';
  }
  /** The lines under the prune panel after a prune or a delete: what went, how much space came back, what could not be deleted, and the Git branches kept for the edits in worker trees (the server words them: "kept Git branch X: reason"). */
  function doneNote(verb, plan) {
    const kept = (plan.branchesKept || plan.kept || []).map(x => String(x)).map(k => /^kept Git branch/.test(k) ? k : 'kept Git branch ' + k);
    return { head: verb + ' ' + plural(plan.list.length, 'session') + ', freed ' + mb1(plan.mb) + ' MB', kept: kept.concat(plan.error ? [String(plan.error)] : []), error: plan.error ? String(plan.error) : '' };
  }
  /** The note under the prune panel as markup: the first line, then one line per kept branch (all text from the server is escaped). */
  const noteHtml = n => esc(n.head) + n.kept.map(k => '<br>' + esc(k)).join('');
  /** The confirm of a deletion: one dialog for prune and for delete selected; ids are sorted, as the confirmation scope is. */
  function deleteSpec(list, kind) {
    const mb = list.reduce((s, r) => s + (Number(r.mb) || 0), 0);
    return kind === 'delete'
      ? { title: 'Delete ' + plural(list.length, 'recorded session'), text: 'The event log, its blobs and its checkpoints of each. Edits left in worker trees are kept as Git branches.', detail: '<span class="mono">' + esc(list.map(s => s.id + '  ' + mb1(s.mb) + ' MB').join('\n')) + '</span>', ok: 'Delete them' }
      : { title: 'Prune with --yes', text: 'Delete <b>' + list.length + '</b> recorded session' + (list.length === 1 ? '' : 's') + ' (' + mb1(mb) + ' MB): the event log, its blobs and its checkpoints?', detail: '<span class="mono">' + esc(list.map(s => s.id).join('\n')) + '</span>', ok: 'Delete them' };
  }
  /** Delete recorded sessions through the server; resolves the PrunePlan or the failure. */
  async function deleteIds(ids) {
    const sorted = ids.slice().sort();
    return net.post('/api/recorded/delete', { ids: sorted }, { confirm: 'delete:' + await net.d16(sorted) });
  }
  /** Prune through the server with the confirmation of the ids it will delete. */
  async function pruneApply(older, keep, ids) {
    const sorted = ids.slice().sort();
    return net.post('/api/recorded/prune', { olderThan: older, keep, apply: true }, { confirm: 'prune:' + await net.d16(sorted) });
  }
  /** The card of one live (or read-only) session. */
  function cardHtml(S) { const w = S.wm, c = calc.totals(w), st = S.state(), cur = S === SL.sessions.active, q = calc.openQuestion(w), nW = S.roster.length - 1, merged = w.merged.length, tot = w.torder.length;
        return '<article class="scard' + (cur ? ' cur' : '') + '" data-sid="' + esc(S.id) + '" data-state="' + st + '"><div class="sc-h"><span class="sg">' + GL[st] + '</span><b class="sn">' + esc(S.name) + '</b>' + (cur ? '<span class="tag ok">active</span>' : '') + (S.recorded ? '<span class="tag" title="a recorded session opened read-only: nothing here changes it">' + (S.follow ? 'watching' : 'replay') + '</span>' : '') + (S.meta.headless ? '<span class="tag warm" title="nobody can answer: an action that needs approval is refused (--ask-timeout ' + esc(S.meta.askTimeout || '10m') + ')">headless</span>' : '') + (q ? '<span class="tag warm">? needs you</span>' : '') + '<span class="sp"></span><span class="dim">' + WORD[st] + '</span></div><div class="sc-m"><span class="mono">' + esc(S.meta.cwd) + '</span><span>' + esc(S.meta.model.split('/')[1] || S.meta.model) + '</span><span>' + esc(S.meta.mode) + '</span><span>' + (nW ? 'manager + ' + nW + ' workers' : 'single agent') + '</span><span>started ' + esc(S.meta.started || '') + '</span></div><div class="sc-n"><span class="num" data-c="cost">' + fmtUsd(c.cost, 3) + '</span><span class="dim">of ' + (S.meta.budget ? fmtUsd(S.meta.budget, 2) : 'no budget') + '</span><span class="' + hitCls(c.pct) + ' num qfull" data-c="hit">' + c.pct + '% hit</span>' + (tot ? '<span class="num">' + merged + '/' + tot + ' merged</span>' : '') + '</div><div class="gauge wide"><i style="width:' + (S.meta.budget ? Math.min(100, c.cost / S.meta.budget * 100) : 0) + '%"></i></div><pre class="launch mono">' + esc(S.meta.launch || 'sleipnir chat') + '</pre><div class="sc-a"><button class="btn sm pri" type="button" data-open>Open</button><button class="btn sm" type="button" data-ren>Rename</button>' + (S.recorded ? '' : '<button class="btn sm" type="button" data-stop>Stop run</button>') + '<button class="btn sm danger" type="button" data-close>Close</button></div></article>'; }
  C3.sessions = { cardHtml, hostedSids, isLive, whyNotDeletable, recRow, recHtml, pruneText, doneNote, noteHtml, deleteSpec, deleteIds, pruneApply };

  /* ======================================================================== SESSIONS */
  reg({ name: 'sessions', title: 'Sessions', mount(sc, root) {
    root.innerHTML = '<div class="sessv"><section class="panel"><div class="ph"><h2>Live sessions</h2><div class="r"><span class="lcount"></span><button class="btn sm pri" type="button" data-new>+ New session</button></div></div><div class="livecards"></div><div class="ph" style="margin-top:6px"><h2>Recorded</h2><div class="r"><span class="rcount"></span><button class="btn sm danger" type="button" data-del disabled>Delete selected…</button><button class="btn sm" type="button" data-cont>↺ --continue</button></div></div><div class="reclist"></div></section><div class="sess-side"><section class="panel"><div class="ph"><h2>Prune</h2><div class="r"><span class="mono">sessions prune</span></div></div><div class="prune"><p style="margin:0 0 8px">Deletes recorded sessions that are old: the event log, the blobs it points at and the checkpoints of each. The newest are kept whatever their age, and so is any session written to in the last ten minutes. Live sessions are never touched.</p><div class="field-row"><label class="mono" for="pOlder">--older-than</label><input id="pOlder" value="30d" style="max-width:90px"><label class="mono" for="pKeep">--keep</label><input id="pKeep" value="20" style="max-width:70px"></div><pre id="pPre" class="pre"></pre><div class="row2"><button class="btn danger" type="button" id="pYes">Prune with --yes…</button></div><p id="pNote" class="stubnote" style="margin:8px 0 0"></p></div></section><section class="panel"><div class="ph"><h2>Cross-session inbox</h2><div class="r"><span class="nneed"></span></div></div><div class="needs-list"></div></section></div></div>';
    const live = $('.livecards', root), rec = $('.reclist', root), sel = new Set(), P = { plan: null, seq: 0, timer: 0, busy: false };
    /** the tabs the cards show: every session but the empty placeholder of a page with no tab (D-12) */
    const sessionsShown = () => SL.sessions.list.filter(S => !S.placeholder);
    const liveHtml = () => sessionsShown().map(cardHtml).join('');
    const recorded = () => SL.sessions.recorded || [];
    const drawRec = () => { const ids = new Set(recorded().map(r => r.id)); sel.forEach(id => { if (!ids.has(id)) sel.delete(id); }); hosted().forEach(id => sel.delete(id)); rec.innerHTML = recHtml(recorded(), { sel, hosted: hosted(), now: Date.now() }); picked(); };
    const hosted = hostedSids;
    function picked() { const b = $('[data-del]', root); if (!b) return; b.disabled = P.busy || sel.size === 0; b.title = sel.size ? 'delete ' + plural(sel.size, 'selected session') : 'select the recorded sessions to delete'; const all = $('[data-pickall]', rec); if (all) { const can = recorded().filter(r => !whyNotDeletable(r, hosted())); all.checked = can.length > 0 && can.every(r => sel.has(r.id)); } }

    /* the prune preview: the client's own arithmetic at once, then the server's plan (it knows which sessions another process holds) */
    const pruneArgs = () => { const o = $('#pOlder', root).value.trim(), k = parseInt($('#pKeep', root).value, 10); return { o, k: isNaN(k) ? 20 : k }; };
    function paintPlan(plan) { P.plan = plan; $('#pPre', root).textContent = pruneText(plan); $('#pYes', root).disabled = P.busy || !!plan.error || !plan.list.length; }
    function pruneTxt() {
      const { o, k } = pruneArgs(); paintPlan(SL.sessions.prune(o, k, false));
      const mine = ++P.seq; clearTimeout(P.timer);
      P.timer = sc.timeout(async () => {
        const r = await net.post('/api/recorded/prune', { olderThan: o, keep: k, apply: false }); if (mine !== P.seq || !sc.alive) return;
        if (r.ok && r.data && Array.isArray(r.data.list)) paintPlan(Object.assign({ server: true }, r.data));
        else if (!r.ok && r.code === 'bad_age') paintPlan({ error: r.message });
      }, 250);
    }
    let sig = '';
    function render() {
      const nowLive = recorded().filter(r => isLive(r)).length;
      const s2 = sessionsShown().map(S => S.id + S.name + S.state() + S.meta.mode + S.meta.model + S.meta.swarm + (S === SL.sessions.active) + (S.sid || '')).join('|') + '#' + recorded().length + '#' + recorded().map(r => r.id + (r.locked ? 'L' : '') + (r.integration ? 'I' : '')).join(',') + '#' + nowLive + '#' + SL.sessions.needs().length;
      if (s2 !== sig) { sig = s2; live.innerHTML = liveHtml(); drawRec(); pruneTxt(); const nd = SL.sessions.needs(); $('.nneed', root).textContent = nd.length + ' open'; $('.needs-list', root).innerHTML = nd.length ? nd.map(({ S, q }) => '<div class="nrow"><b style="color:var(--c-mgr)">' + esc(S.name) + '</b> › <span style="color:' + esc(agCol(q.agent)) + '">' + esc(q.agent) + '</span> <span class="mono">' + esc(q.cmd) + '</span><button class="btn sm" type="button" data-inb>Answer…</button></div>').join('') : '<p class="stubnote" style="padding:10px 12px;margin:0">Nothing waits for you in any session.</p>'; $('.lcount', root).textContent = sessionsShown().filter(x => !x.recorded).length + ' running'; $('.rcount', root).textContent = recorded().length + ' · ' + SL.sessions.recordedMb().toFixed(1) + ' MB on disk'; }
      $$('.scard', live).forEach(c => { const S = SL.sessions.get(c.dataset.sid); if (!S) return; const t = calc.totals(S.wm), e = $('[data-c="cost"]', c), h = $('[data-c="hit"]', c); if (e) { const x = fmtUsd(t.cost, 3); if (e.textContent !== x) e.textContent = x; } if (h) { const x = t.pct + '% hit'; if (h.textContent !== x) h.textContent = x; } });
    }

    /* the recorded session of a tab: Replay and Watch open a read-only tab through the live core (D-10, A7) */
    function openRecorded(id, follow) {
      const open = (SL.act && SL.act.openRecorded) || (SL.sessions && SL.sessions.openRecorded);
      if (typeof open !== 'function') { ui.toast('replay it with sleipnir replay ' + id + ' in a terminal', 'warm'); return; }
      const r = open.call(SL.act && SL.act.openRecorded ? SL.act : SL.sessions, id, { follow: !!follow });
      if (r && r.ok === false) ui.toast(r.why || 'that session cannot be opened', 'warm');
    }
    /** Stop the run of a session: the server answers 409 idle when nothing was running, and that is said as it is. */
    async function stopRun(S) {
      const r = await net.post('/api/sessions/' + encodeURIComponent(S.id) + '/stop');
      if (r.ok) ui.toast('stopped: the goal is paused', 'warm');
      else if (r.code === 'idle') ui.toast(r.message || 'nothing was running', 'warm');
      else if (r.code === 'no_api') { const x = SL.act.interrupt('turn', S.id); ui.toast(x.ok ? 'stopped: the goal is paused' : (x.why || 'nothing was running'), 'warm'); }
      else net.fail(r, 'the run could not be stopped');
    }
    function closeAsk(S) {
      if (typeof ui.closeSessionAsk === 'function') return ui.closeSessionAsk(S);
      ui.confirm({ title: 'Close the session', text: 'Close <b>' + esc(S.name) + '</b>? Its team stops and the tab goes away. The recorded log stays on disk and can be resumed with ↺.', ok: 'Close it', danger: true, run: () => { const r = SL.act.closeSession(S.id); if (r && r.ok === false) ui.toast(r.why || 'the last session cannot be closed: start another first', 'warm'); else ui.toast('closed ' + S.name); } });
    }
    /** Delete the selected sessions after one confirmation. */
    function deleteSelected() {
      const rows = recorded().filter(r => sel.has(r.id) && !whyNotDeletable(r, hosted())); if (!rows.length) { picked(); return; }
      const sp = deleteSpec(rows, 'delete');
      ui.confirm({ title: sp.title, text: sp.text, detail: sp.detail, ok: sp.ok, danger: true, run: async () => {
        P.busy = true; picked();
        const r = await deleteIds(rows.map(x => x.id)); P.busy = false;
        if (!r.ok) { net.fail(r, 'the sessions could not be deleted'); picked(); C3.refreshRecorded(); return; }
        const n = doneNote('deleted', r.data), note = $('#pNote', root);
        rows.forEach(x => sel.delete(x.id));
        if (note) note.innerHTML = noteHtml(n);
        if (n.error) ui.toast(n.error, 'warm'); ui.toast('deleted ' + plural(r.data.list.length, 'session') + ', ' + mb1(r.data.mb) + ' MB freed', n.error ? 'warm' : 'ok'); await C3.refreshRecorded(); sig = ''; render();
      } });
    }

    sc.listen(root, 'click', e => { const t = e.target, card = t.closest('.scard'), S = card && SL.sessions.get(card.dataset.sid);
      if (t.closest('[data-new]')) ui.dialogs.newSession(); else if (t.closest('[data-cont]')) { const r = SL.act.resumeSession('latest'); if (r && r.ok === false) ui.toast(r.why, 'warm'); }
      else if (t.closest('[data-del]')) deleteSelected();
      else if (S && t.closest('[data-open]')) { SL.act.switchSession(S.id); } else if (S && t.closest('[data-ren]')) ui.dialogs.rename(S);
      else if (S && t.closest('[data-stop]')) { SL.act.switchSession(S.id); ui.confirm({ title: 'Stop the run', text: 'Stop <b>' + esc(S.name) + '</b>? Every agent is interrupted and the goal is paused.', ok: 'Stop the run', danger: true, run: () => stopRun(S) }); }
      else if (S && t.closest('[data-close]')) closeAsk(S);
      else if (t.closest('[data-res]')) { const id = t.closest('[data-res]').dataset.res, r = SL.act.resumeSession(id); if (r && r.ok === false) ui.toast(r.why, 'warm'); else ui.toast('resuming ' + id, 'quiet'); }
      else if (t.closest('[data-rep]')) openRecorded(t.closest('[data-rep]').dataset.rep, false);
      else if (t.closest('[data-watch]')) openRecorded(t.closest('[data-watch]').dataset.watch, true);
      else if (t.closest('[data-hint]')) { const r = recorded().find(x => x.id === t.closest('[data-hint]').dataset.hint); if (r && r.integration) U.copy(r.integration.hint || '').then(ok => ui.toast(ok ? 'command copied' : 'copy is not available here', ok ? 'ok' : 'warm')); }
      else if (t.closest('[data-inb]')) ui.inbox.toggle($('#sInbox'));
      else if (t.closest('#pYes')) {
        const plan = P.plan; if (!plan || plan.error || !plan.list.length) return; const { o, k } = pruneArgs(), list = plan.list, sp = deleteSpec(list, 'prune');
        ui.confirm({ title: sp.title, text: sp.text, detail: sp.detail, ok: sp.ok, danger: true, run: async () => {
          P.busy = true; $('#pYes', root).disabled = true;
          const r = await pruneApply(o, k, list.map(x => x.id)); P.busy = false;
          if (!r.ok) { net.fail(r, 'the sessions could not be pruned'); await C3.refreshRecorded(); sig = ''; render(); return; }
          const n = doneNote('deleted', r.data); $('#pNote', root).innerHTML = noteHtml(n);
          if (n.error) ui.toast(n.error, 'warm'); ui.toast('pruned ' + plural(r.data.list.length, 'session') + ', ' + mb1(r.data.mb) + ' MB freed', n.error ? 'warm' : 'ok'); await C3.refreshRecorded(); sig = ''; render();
        } });
      } });
    sc.listen(root, 'change', e => {
      const t = e.target; if (t.dataset.pick) { if (t.checked) sel.add(t.dataset.pick); else sel.delete(t.dataset.pick); picked(); }
      else if (t.matches && t.matches('[data-pickall]')) { recorded().forEach(r => { if (!whyNotDeletable(r, hosted())) { if (t.checked) sel.add(r.id); else sel.delete(r.id); } }); $$('[data-pick]', rec).forEach(b => { if (!b.disabled) b.checked = t.checked; }); picked(); }
    });
    sc.listen(root, 'input', e => { if (e.target.id === 'pOlder' || e.target.id === 'pKeep') pruneTxt(); });
    sc.on('recorded-changed', () => { sig = ''; render(); }); sc.on('sessions-changed', () => { sig = ''; render(); }); sc.on('needs-changed', () => { sig = ''; });
    sc.update(render); sc.frame(() => { if (SL.loop.frameNo % 30 === 0) render(); }); render();
    if (SL.data) SL.data.forPage('sessions', { loud: true });   // a server that cannot answer is said, not drawn as an empty list
  } });

})(SL);
