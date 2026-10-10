/* 30-model.js: SL.model, the pure reducer from events to a session's model, and SL.calc, every number derived from it.
 *
 * An event is {t, k, ...fields} (t = session seconds). `reduce(m, ev, ctx)` is the ONLY thing that changes a model. It runs twice per
 * session: on the world model (events up to the world clock, no chat rows, drives the director and needs-you) and on the view model
 * (events up to the view clock, with chat rows). Both read the same events in the same order, so after a catch-up the two are equal.
 *
 * Every number on screen is derived from the per-agent token table in the model (read, uncached, out): hit, cost, savings, the HUD ring,
 * the Cache tab, the stalls and the drawer all call SL.calc, so they agree to the percent at every moment.
 *
 * Event kinds (their fields and caps are the types of internal/web/wire/events.go): say, sys, tool, note, state, task, plan, verdict,
 * req, use, warm, gov, mail, ckpt, ask, answer, queue, merge, break, compact, stream, goal, final, local, steer, reply, interrupt,
 * refuse, digest, and more, turn, stall, handover, layers (plus alert, mailstat and svc when the server sends them). Fields that real data
 * carries beyond what a screen draws are kept in the model and change no rendering path. */
(function (SL) {
  'use strict';
  const { upperBound } = SL.u;
  const D = SL.D;
  const CHAN_CAP = 4500;          // entries kept per channel in memory (the DOM keeps far fewer)
  const MARK_CAP = 800, SEG_CAP = 1200;
  /* every list a long stream grows is bounded: the ratios of an agent's requests (the count stays in nreq), the streamed messages still
     open, the answered questions, the anomalies and the compactions */
  const RATIO_CAP = 400, MID_CAP = 1000, QS_CAP = 300, ANOM_CAP = 500, COMPACT_CAP = 500;
  /** Push a mark of the gantt (the cap holds for every kind). */
  const addMark = (m, x) => { m.marks.push(x); if (m.marks.length > MARK_CAP) m.marks.splice(0, m.marks.length - MARK_CAP); };
  const capList = (l, n) => { if (l.length > n) l.splice(0, l.length - n); };
  const VISIBLE = new Set(['say', 'sys', 'tool', 'note', 'mail', 'break', 'compact', 'ask', 'merge', 'steer', 'reply', 'refuse', 'interrupt', 'local']);

  const priceOf = id => id === 'mgr' ? D.prices.mgr : D.prices.worker;
  const MAIL_CAP = 400, HANDOVER_CAP = 100;

  /* ---------------- what comes from the server, made plain once (it is data: a log can name anything) ---------------- */
  const { safeId, okId } = SL.u, BAD = SL.u.BAD_ID;
  const ID_FIELDS = ['id', 'cid', 'qid', 'ag', 'from', 'to', 'owner', 'head', 'task', 'ch', 'mid'], ID_LISTS = ['deps', 'left', 'agents'];
  const NUM_FIELDS = ['rd', 'un', 'out', 'wr', 'ratio', 'p', 'o', 'rpm', 'r429', 'retries', 'inflight', 'queued', 'conflicts', 'bounced', 'read', 'expected', 'pct', 'ms', 'add', 'del', 'tok', 'ttl', 'turns', 'max', 'attempts', 'rate', 'reqSince', 'n'];
  const STATES = new Set(['think', 'tool', 'edit', 'wait', 'ask', 'idle', 'done', 'stuck']);
  const num = x => typeof x === 'number' && isFinite(x) ? x : (isFinite(+x) ? +x : 0);
  /** An id field: a finite number stays (a count such as a compaction's from and to shares the name), a string is made plain, any other value is BAD_ID. */
  const plainId = x => typeof x === 'number' ? (isFinite(x) ? x : BAD) : typeof x === 'string' ? safeId(x) : BAD;
  /**
   * Make one event from the server safe to keep (in place; running it again changes nothing). Every id (agent, task, question,
   * message, checkpoint) is a plain id or SL.u.BAD_ID, so no id reaches markup, an attribute, a CSS variable or an object key as anything
   * but a plain word; t, seq and every count are numbers; an agent's state is one the page draws (else idle); a page-local row (`local`, which
   * carries markup the page made itself) is never accepted from the server: it becomes a plain system row. Returns ev.
   */
  function clean(ev) {
    if (!ev || typeof ev !== 'object') return ev;
    ev.t = num(ev.t); if (ev.seq != null) ev.seq = num(ev.seq);
    NUM_FIELDS.forEach(k => { if (ev[k] != null && typeof ev[k] !== 'number') ev[k] = num(ev[k]); });
    ID_FIELDS.forEach(k => { if (ev[k] != null && ev[k] !== '') ev[k] = plainId(ev[k]); });
    ID_LISTS.forEach(k => { if (Array.isArray(ev[k])) ev[k] = ev[k].map(plainId); });
    if (Array.isArray(ev.lines)) ev.lines = ev.lines.map(l => Array.isArray(l) ? [safeId(l[0]), l[1]] : ['', String(l)]);
    if (ev.q && typeof ev.q === 'object') { const q = ev.q; ['id', 'agent', 'task'].forEach(k => { if (q[k] != null && q[k] !== '') q[k] = safeId(q[k]); }); }
    if (ev.k === 'state' && !STATES.has(ev.s)) ev.s = 'idle';
    if (ev.k === 'task' && ev.s != null && !okId(ev.s)) delete ev.s;
    if (ev.k === 'local' || ev.who === 'local' || ev.html != null) { if (ev.k === 'local') ev.k = 'sys'; if (ev.who === 'local') ev.who = 'sys'; ev.text = String(ev.text != null ? ev.text : ev.title || ''); delete ev.html; }
    return ev;
  }
  /** One roster entry from the server, made plain: its id, role and code are plain ids (the code names a colour variable). */
  const cleanAgent = r => Object.assign({}, r, { id: safeId(String(r && r.id != null ? r.id : '')) || BAD, role: safeId(r && r.role) || 'agent', code: r && /^[A-Za-z0-9_-]{1,32}$/.test(String(r.code || '')) ? r.code : 'dim' });
  /** A roster from the server, made plain, without a second entry for an id it already has. */
  function cleanRoster(list) { const seen = {}; return (Array.isArray(list) ? list : []).filter(r => r && typeof r === 'object').map(cleanAgent).filter(r => seen[r.id] ? false : (seen[r.id] = true)); }

  /** Build an empty model for a session roster: [{id, role, code, nth, k, leg, scope, ro, spawn, model}]. The plan is sized by the
   *  first `plan` event (its steps); the counters of the additive vocabulary start at zero. */
  function newModel(S, opts) {
    const m = { sid: S.id, t: 0, ver: 0, nid: 0, chat: !(opts && opts.noChat), ag: {}, order: [], tasks: {}, torder: [], q: null, merged: [], conflicts: 0, bounced: 0,
      mail: [], marks: [], plan: [], planText: null, verdict: '', verdictKind: '', left: [], goal: { state: S.meta.goalText ? 'active' : 'none' }, qs: [], anomalies: [], compactions: [], ckpts: [],
      reqLog: [], flash: { t: -999, id: null }, lastReq: -9999, lastBreakT: -9999, streams: {}, diff: {}, rpm: 0, rpmHist: [], final: null, chan: { mgr: [], mail: [] }, folded: {}, steps: 0, refused: 0,
      mids: {}, midOrder: [], stalls: {}, handovers: [], alerts: {}, mailstat: null, svc: null, ttl: 0, r429: 0, retries: 0, inflight: 0, queued: 0, turn: false };
    (S.roster || []).forEach(r => addAgent(m, r));
    return m;
  }
  /** Add one roster entry to a model (the per-agent part of newModel): used when a team grows while the page watches. A known id is
   *  left as it is. */
  function addAgent(m, r) {
    if (!okId(r.id) || !okId(r.role)) r = cleanAgent(r);
    if (m.ag[r.id]) { Object.assign(m.ag[r.id], { role: r.role, code: r.code, nth: r.nth, k: r.k, leg: r.leg, scope: r.scope, ro: r.ro, model: r.model }); return m.ag[r.id]; }
    m.ag[r.id] = { id: r.id, role: r.role, code: r.code, nth: r.nth, k: r.k, leg: r.leg, scope: r.scope, ro: r.ro, model: r.model, state: 'idle', doing: r.id === 'mgr' ? 'waits for the first message' : 'not started',
      task: null, rd: 0, un: 0, out: 0, wr: 0, cost: null, saved: null, layers: null, reqSince: null, calls: 0, ratios: [], rmarks: [], nreq: 0, lastReq: -999, segs: [], cur: null, spawned: r.id === 'mgr', spawnT: r.spawn, steered: null, stateT: 0 };
    m.order.push(r.id); if (r.id !== 'mgr' && !m.chan[r.id]) m.chan[r.id] = [];
    return m.ag[r.id];
  }

  /** Push a transcript entry. `animate`/`digest` come from ctx: collapsed rows are kept (state equality) but flagged and, for a digest, summarised. */
  function push(m, ctx, ch, entry, ev) {
    if (!m.chat) return;
    const list = m.chan[ch] || (m.chan[ch] = []);
    entry.id = ++m.nid; if (entry.t == null) entry.t = ev.t; if (ev && ev.at && entry.at == null) entry.at = ev.at;
    if (ev && ev.mid && (entry.k === 'say' || entry.k === 'stream')) { entry.mid = ev.mid; if (!m.mids[ev.mid]) { m.mids[ev.mid] = []; m.midOrder.push(ev.mid); if (m.midOrder.length > MID_CAP) delete m.mids[m.midOrder.shift()]; } m.mids[ev.mid].push(entry); }
    if (ctx && ctx.digest) { entry.collapsed = true; if (ctx.digest.rows.length < 160) ctx.digest.rows.push({ ch, k: entry.k, t: entry.t, text: entry.text || entry.arg || (entry.name ? entry.name : '') || (entry.id2 || '') , who: entry.who || entry.ag || '' }); }
    list.push(entry);
    if (list.length > CHAN_CAP) { list.splice(0, 500); m.folded[ch] = (m.folded[ch] || 0) + 500; }
  }
  const chOf = id => id === 'mgr' ? 'mgr' : id;

  function closeSeg(a, t) { if (a.cur) { a.cur.t1 = t; a.cur = null; } }

  /** Apply one event to a model. ctx: {digest?} (collapsed replay). Returns nothing; bumps m.ver. */
  function reduce(m, ev, ctx) {
    ctx = ctx || {};
    m.t = ev.t; m.ver++;
    const A = ev.id ? m.ag[ev.id] : null;
    const d = ctx.digest;
    switch (ev.k) {
      case 'say': {
        if (ev.who === 'you') push(m, ctx, 'mgr', { k: 'you', text: ev.text, t: ev.t }, ev);
        else if (ev.who === 'sys') push(m, ctx, 'mgr', { k: 'sys', glyph: ev.glyph || '◇', text: ev.text, plan: !!ev.plan, task: ev.task, open: ev.open, t: ev.t }, ev);
        else if (ev.who === 'scouts') push(m, ctx, 'mgr', { k: 'scouts', lines: ev.lines, t: ev.t }, ev);
        else if (ev.who === 'mgr') push(m, ctx, 'mgr', { k: 'say', ag: 'mgr', text: ev.text, stream: ev.stream !== false, rate: ev.rate || 70, t: ev.t, done: !ev.mid }, ev);
        else if (ev.who === 'local') push(m, ctx, 'mgr', { k: 'local', title: ev.title, html: ev.html, t: ev.t }, ev);
        break;
      }
      case 'sys': push(m, ctx, ev.ch || 'mgr', { k: 'sys', glyph: ev.glyph || '◇', text: ev.text, ag: ev.ag, task: ev.task, open: ev.open, t: ev.t }, ev); break;
      case 'local': push(m, ctx, 'mgr', { k: 'local', title: ev.title, html: ev.html, t: ev.t }, ev); break;
      case 'tool': {
        if (A) A.calls++; m.steps++; if (d) d.tools++;
        const e = { k: 'tool', ag: ev.id, name: ev.name, arg: ev.arg, out: ev.out, ok: ev.ok !== false, refused: !!ev.refused, reason: ev.reason, file: ev.file, add: ev.add, del: ev.del, task: ev.task || (A && A.task), t: ev.t };
        if (ev.refused) { m.refused++; e.ok = false; }
        push(m, ctx, chOf(ev.id), e, ev);
        if (ev.id !== 'mgr') push(m, ctx, 'mgr', { k: 'feed', ag: ev.id, g: ev.refused ? 'x' : /^(Write|Edit)/.test(ev.name) ? 'edit' : ev.name === 'Bash' ? 'tool' : 'tool', text: ev.name + ' ' + (ev.arg || '') + (ev.add ? ' +' + ev.add : ''), task: e.task, t: ev.t }, ev);
        break;
      }
      case 'note': {
        const e = { k: 'note', ag: ev.id, g: ev.g || 'done', text: ev.text, task: ev.task || (A && A.task), t: ev.t };
        push(m, ctx, chOf(ev.id), e, ev);
        if (ev.id !== 'mgr') push(m, ctx, 'mgr', { k: 'feed', ag: ev.id, g: ev.g || 'done', text: ev.text, task: e.task, t: ev.t }, ev);
        if (d && /^submitted/.test(ev.text)) d.submitted.push(e.task);
        break;
      }
      case 'state': {
        if (!A) break;
        closeSeg(A, ev.t);
        const prev = A.state;
        A.state = ev.s; A.doing = ev.doing != null ? ev.doing : A.doing; if (ev.task !== undefined) A.task = ev.task; A.spawned = true; A.stateT = ev.t; A.reqSince = typeof ev.reqSince === 'number' ? ev.reqSince : null;
        A.cur = { s: ev.s, t0: ev.t, t1: null, doing: A.doing }; A.segs.push(A.cur); if (A.segs.length > SEG_CAP) A.segs.splice(0, 200);
        delete m.streams[ev.id];
        if (prev !== ev.s && ['ask', 'done', 'stuck', 'idle', 'wait'].includes(ev.s) && ev.id !== 'mgr') push(m, ctx, chOf(ev.id), { k: 'st', ag: ev.id, s: ev.s, text: A.doing, task: A.task, t: ev.t }, ev);
        break;
      }
      case 'task': {   /* each task event is the whole task (the server's TaskX): a field it omits is empty, never "as before" */
        let T = m.tasks[ev.id];
        if (!T) { T = m.tasks[ev.id] = { id: ev.id, title: ev.title || ev.id, owner: null, deps: [], scope: '', st: 'todo', t: ev.t, ms: 0 }; m.torder.push(ev.id); }
        if (ev.s) { T.st = ev.s; T.t = ev.t; if (ev.s === 'merged' && !m.merged.includes(ev.id)) m.merged.push(ev.id); }
        T.owner = ev.owner || null;   /* a handover moves the card's owner; a released task has none */
        if (ev.title) T.title = ev.title; T.deps = Array.isArray(ev.deps) ? ev.deps.slice() : []; T.scope = ev.scope || '';
        T.closure = ev.closure || ''; T.failed = !!ev.failed; T.attempts = typeof ev.attempts === 'number' ? ev.attempts : 0;
        break;
      }
      case 'plan': {
        if (Array.isArray(ev.steps)) { m.planText = ev.steps.map(String); m.plan = ev.steps.map((_, i) => SL.u.own(PLAN_ST, (ev.st || [])[i]) || 'pending'); }
        else if (Number.isInteger(ev.n) && ev.n >= 0 && ev.n < 1000) m.plan[ev.n] = SL.u.own(PLAN_ST, ev.s) || 'pending';
        break;
      }
      case 'verdict': m.verdict = ev.text || ''; m.verdictKind = ev.kind || ''; m.left = Array.isArray(ev.left) ? ev.left.slice() : []; break;   /* the whole verdict: what it omits is empty */
      case 'req': {
        if (!A) break;
        A.ratios.push(ev.ratio); capList(A.ratios, RATIO_CAP); A.nreq++; A.lastReq = ev.t;
        A.rmarks.push(ev.mark === 'epoch' || ev.mark === 'rebase' ? ev.mark : ''); capList(A.rmarks, RATIO_CAP);   /* what came before the request: a new shared prefix, thinking dropped */
        if (!ev.hist) {
          const p = ev.p || Math.round((A.rd + A.un) / Math.max(1, A.nreq - 1)) || 1200;
          const rd = Math.round(p * ev.ratio); A.rd += rd; A.un += Math.round(p) - rd; A.out += ev.o || 0;
          m.lastReq = ev.t; m.reqLog.push({ t: ev.t, id: ev.id, ratio: ev.ratio }); if (m.reqLog.length > 200) m.reqLog.shift();
          addMark(m, { t: ev.t, id: ev.id, g: 'req' });
        }
        break;
      }
      case 'use': if (A) { A.rd = ev.rd; A.un = ev.un; A.out = ev.out; if (ev.wr != null) A.wr = ev.wr; if (typeof ev.cost === 'number') A.cost = ev.cost; if (typeof ev.saved === 'number') A.saved = ev.saved; A.savedPartial = !!ev.savedPartial; A.unpriced = typeof ev.unpriced === 'number' ? ev.unpriced : 0; } break;
      case 'warm': m.lastReq = ev.t; m.ttl = ev.ttl || 0; break;
      case 'gov': m.rpm = ev.rpm; m.rpmHist.push({ t: ev.t, rpm: ev.rpm }); if (m.rpmHist.length > 60) m.rpmHist.shift(); if (ev.r429 != null) m.r429 = ev.r429; if (ev.retries != null) m.retries = ev.retries; m.inflight = ev.inflight || 0; m.queued = ev.queued || 0; break;
      case 'mail': {
        m.mail.push({ t: ev.t, from: ev.from, to: ev.to, text: ev.text, at: ev.at, tok: ev.tok }); if (m.mail.length > MAIL_CAP) m.mail.shift();
        addMark(m, { t: ev.t, id: ev.from, g: 'mail' });
        const e = { k: 'mail', from: ev.from, to: ev.to, text: ev.text, t: ev.t };
        push(m, ctx, 'mail', e, ev); if (ev.from !== 'you') push(m, ctx, chOf(ev.from), Object.assign({}, e), ev); if (ev.to !== ev.from) push(m, ctx, chOf(ev.to), Object.assign({}, e), ev);
        push(m, ctx, 'mgr', { k: 'feed', ag: ev.from, g: 'mail', text: 'mail → ' + ev.to, task: A && A.task, to: ev.to, t: ev.t }, ev);
        if (d) d.mails++;
        break;
      }
      case 'ckpt': {   /* the same id updates in place (its file set grows while it is current) */
        const id = ev.step ? 'c' + String(m.ckpts.length + 1).padStart(2, '0') : (ev.cid || ev.id), old = m.ckpts.find(c => c.id === id);
        const c = { id, step: ev.step || null, ts: ev.ts, files: ev.files, note: ev.note, skipped: !!ev.skipped, safety: !!ev.safety, at: ev.at, agents: ev.agents || [], add: ev.add || 0, del: ev.del || 0 };
        if (old) Object.assign(old, c); else m.ckpts.unshift(c);
        break;
      }
      case 'ask': {
        const q = Object.assign({}, ev.q, { t0: ev.t, answered: null, choice: null }); m.qs.push(q); m.q = m.qs.find(x => !x.answered) || null;
        addMark(m, { t: ev.t, id: q.agent, g: 'ask' });
        push(m, ctx, 'mgr', { k: 'feed', ag: q.agent, g: 'ask', text: 'asks: ' + SL.u.clip1(q.cmd, 200), task: q.task, t: ev.t }, ev);   /* the question itself shows the command whole */
        push(m, ctx, chOf(q.agent), { k: 'ask', ag: q.agent, q, t: ev.t }, ev);
        if (d) d.asks++;
        break;
      }
      case 'answer': {
        const q = m.qs.find(x => x.id === ev.qid);
        if (q) { q.answered = ev.choice; q.tAns = ev.t; q.note = ev.note; q.by = ev.by; }
        if (m.qs.length > QS_CAP) { let drop = m.qs.length - QS_CAP; m.qs = m.qs.filter(x => !(x.answered && drop > 0 && drop-- > 0)); }   /* the oldest answered ones go; an open one never */
        m.q = m.qs.find(x => !x.answered) || null;
        /* a question the person answered, or one that closed without an answer (canceled, timed out, the session closed, no page open): a quiet row */
        if (q) push(m, ctx, 'mgr', ev.by && ev.by !== 'you' ? { k: 'sys', glyph: '⊘', text: 'refused without an answer (' + (SL.u.own(BY_WORD, ev.by) || 'closed') + '): ' + q.agent + ': ' + SL.u.clip1(q.cmd, 200), ag: q.agent, t: ev.t }
          : { k: 'sys', glyph: '❯', text: 'you answered ' + ev.choice + ' to ' + q.agent + ': ' + SL.u.clip1(q.cmd, 200), ag: q.agent, t: ev.t }, ev);
        break;
      }
      case 'queue': m.qHead = ev.head ? { task: ev.head, cmd: ev.cmd || '', step: ev.step || '', t0: (m.qHead && m.qHead.task === ev.head) ? m.qHead.t0 : ev.t, ms: ev.ms || 0 } : null; m.conflicts = ev.conflicts || 0; m.bounced = ev.bounced || 0; break;
      case 'merge': {
        const T = m.tasks[ev.id]; if (!T) break; T.st = 'merged'; T.ms = ev.ms; T.t = ev.t; if (!m.merged.includes(ev.id)) m.merged.push(ev.id);
        addMark(m, { t: ev.t, id: T.owner, g: 'merge' }); m.lastMerge = ev;
        push(m, ctx, 'mgr', { k: 'sys', glyph: '✓', text: ev.id + ' merged: rebase ✓ · ' + ev.cmd + ' ✓ ' + (ev.ms < 1000 ? ev.ms + 'ms' : (ev.ms / 1000).toFixed(1) + 's'), task: ev.id, ag: T.owner, t: ev.t }, ev);
        if (T.owner) push(m, ctx, chOf(T.owner), { k: 'note', ag: T.owner, g: 'done', text: ev.id + ' merged ✓ ' + ev.cmd + ' ' + (ev.ms / 1000).toFixed(1) + 's', task: ev.id, t: ev.t }, ev);
        if (d) d.merged.push(ev.id);
        break;
      }
      case 'break': {
        m.anomalies.push({ t: ev.t, id: ev.id, kind: ev.kind, read: ev.read, expected: ev.expected, why: ev.why }); capList(m.anomalies, ANOM_CAP);
        m.flash = { t: ev.t, id: ev.id }; m.lastBreakT = ev.t; addMark(m, { t: ev.t, id: ev.id, g: 'break' });
        push(m, ctx, 'mgr', { k: 'break', ag: ev.id, kind: ev.kind, read: ev.read, expected: ev.expected, why: ev.why, t: ev.t }, ev);
        push(m, ctx, chOf(ev.id), { k: 'break', ag: ev.id, kind: ev.kind, read: ev.read, expected: ev.expected, why: ev.why, t: ev.t }, ev);
        if (d) d.breaks++;
        break;
      }
      case 'compact': {
        /* when the planner decided (a cold cache makes the rewrite free) and how the thread was folded; both are '' when the server does not say,
           and a compaction nobody planned (an emergency one, one a person asked for) has no moment: the page then states no price */
        const moment = ev.moment === 'cold' || ev.moment === 'warm' ? ev.moment : '', mode = ev.mode === 'fork' || ev.mode === 'mask' || ev.mode === 'emergency' ? ev.mode : '';
        m.compactions.push({ t: ev.t, id: ev.id, from: ev.from, to: ev.to, pct: ev.pct, moment, mode }); capList(m.compactions, COMPACT_CAP); addMark(m, { t: ev.t, id: ev.id, g: 'compact' });
        push(m, ctx, 'mgr', { k: 'compact', ag: ev.id, from: ev.from, to: ev.to, pct: ev.pct, moment, mode, t: ev.t }, ev);
        if (ev.id !== 'mgr') push(m, ctx, chOf(ev.id), { k: 'compact', ag: ev.id, from: ev.from, to: ev.to, pct: ev.pct, moment, mode, t: ev.t }, ev);
        if (d) d.compacts++;
        break;
      }
      case 'stream': {
        m.streams[ev.id] = { t0: ev.t, text: ev.text, rate: ev.rate, code: !!ev.code, mid: ev.mid, file: ev.file, done: !ev.mid };
        const e = { k: 'stream', ag: ev.id, text: ev.text, rate: ev.rate, code: !!ev.code, t: ev.t };
        push(m, ctx, chOf(ev.id), e, ev); if (!ev.code && ev.id !== 'mgr') push(m, ctx, 'mgr', Object.assign({}, e), ev);
        break;
      }
      case 'diff': if (typeof ev.file === 'string' && !(ev.file in Object.prototype)) m.diff[ev.file] = ev; break;
      case 'goal': m.goal = { state: ev.s, objective: ev.objective || '', turns: ev.turns || 0, max: ev.max || 0, paused: ev.paused || '', reason: ev.reason || '' }; break;   /* the whole goal: what it omits is empty */
      case 'final': m.final = { t: ev.t, steps: m.steps }; m.turn = false; push(m, ctx, 'mgr', { k: 'final', t: ev.t }, ev); break;
      case 'steer': {
        const e = { k: 'steer', to: ev.to, text: ev.text, t: ev.t };
        push(m, ctx, ev.to === 'mgr' ? 'mgr' : ev.to, e, ev); if (ev.to !== 'mgr' && !ev.quiet) push(m, ctx, 'mgr', { k: 'feed', ag: ev.to, g: 'steer', text: 'your answer: ' + ev.text, t: ev.t }, ev);
        if (m.ag[ev.to]) m.ag[ev.to].steered = ev.t;
        break;
      }
      case 'reply': push(m, ctx, chOf(ev.id), { k: 'say', ag: ev.id, text: ev.text, stream: true, rate: 80, t: ev.t }, ev); break;
      case 'interrupt': {
        push(m, ctx, 'mgr', { k: 'sys', glyph: '⏹', text: ev.id === 'turn' ? 'you interrupted the turn; the goal is paused' : 'you interrupted ' + ev.id, ag: ev.id === 'turn' ? null : ev.id, t: ev.t }, ev);
        if (ev.id !== 'turn' && ev.id !== 'mgr') push(m, ctx, chOf(ev.id), { k: 'sys', glyph: '⏹', text: 'interrupted by you', ag: ev.id, t: ev.t }, ev);
        break;
      }
      case 'refuse': {
        m.refused++;
        const e = { k: 'tool', ag: ev.id, name: ev.name, arg: ev.arg, out: '', ok: false, refused: true, reason: ev.reason, t: ev.t, task: A && A.task };
        push(m, ctx, chOf(ev.id), e, ev);
        push(m, ctx, 'mgr', { k: 'feed', ag: ev.id, g: 'x', text: 'refused: ' + ev.name + ' ' + (ev.arg || ''), t: ev.t }, ev);
        addMark(m, { t: ev.t, id: ev.id, g: 'refuse' });
        break;
      }
      case 'more': {   /* a streaming message grows: every copy of it (chat and feed) and the agent's stream record */
        const list = m.mids[ev.mid];
        if (list) list.forEach(e => { if (ev.text) e.text += ev.text; if (ev.end) e.done = true; });
        Object.keys(m.streams).forEach(id => { const s = m.streams[id]; if (s.mid === ev.mid) { if (ev.text) s.text += ev.text; if (ev.end) s.done = true; } });
        if (ev.end) delete m.mids[ev.mid];   /* an ended message takes no more text: its index is let go */
        break;
      }
      case 'turn': if (ev.s === 'start') { m.final = null; m.turn = true; m.turnStart = ev.t; } else if (ev.s === 'end') m.turn = false; break;
      case 'stall': { const k = (ev.id || '') + '|' + ev.kind + '|' + (ev.task || ''); if (ev.s === 'clear') delete m.stalls[k]; else m.stalls[k] = ev; break; }
      case 'handover': m.handovers.push(ev); if (m.handovers.length > HANDOVER_CAP) m.handovers.shift(); break;
      case 'layers': if (A && Array.isArray(ev.toks)) A.layers = ev.toks.slice(0, 6); break;
      case 'alert': { const k = String(ev.key || ev.kind || ''); if (k in Object.prototype) break; if (ev.s === 'clear') delete m.alerts[k]; else m.alerts[k] = { kind: ev.kind, text: ev.text, t: ev.t, at: ev.at }; break; }
      case 'mailstat': m.mailstat = ev; break;
      case 'svc': {   /* what the harness's own service agents (the mailman) have used, in all: no row of their own, but part of the run's totals */
        const n = x => typeof x === 'number' && isFinite(x) ? x : 0;
        m.svc = { rd: n(ev.rd), un: n(ev.un), out: n(ev.out), wr: n(ev.wr), cost: n(ev.cost), saved: n(ev.saved), savedPartial: !!ev.savedPartial, unpriced: n(ev.unpriced) };
        break;
      }
      case 'digest': break;
      default: break;
    }
  }

  /** Reduce events [from, to) of a log into m (silent, optional digest context). */
  function reduceRange(m, log, from, to, ctx) { for (let i = from; i < to; i++) reduce(m, log[i], ctx); }
  const isVisible = ev => VISIBLE.has(ev.k);

  /* ---------------- derived numbers: the only place tokens become percentages and dollars ---------------- */
  const calc = {
    /** {prompt, read, un, out, hit (0..1), pct (rounded %), cost, saved} for one agent. */
    agent(a) {
      const pr = priceOf(a.id), prompt = a.rd + a.un, hit = prompt > 0 ? a.rd / prompt : 0;
      const cost = typeof a.cost === 'number' ? a.cost : (a.un * pr.in + a.rd * pr.cached + a.out * pr.out) / 1e6, saved = typeof a.saved === 'number' ? a.saved : a.rd * (pr.in - pr.cached) / 1e6;
      return { prompt, read: a.rd, un: a.un, out: a.out, wr: a.wr || 0, hit, pct: Math.round(hit * 100), cost, saved, savedPartial: !!a.savedPartial, unpriced: a.unpriced || 0 };
    },
    /** Totals over every agent of a model (manager included) and what the harness's service agents used (they have no row of their own). */
    totals(m) {
      const t = { prompt: 0, read: 0, un: 0, out: 0, wr: 0, cost: 0, saved: 0, calls: 0, savedPartial: false, unpriced: 0 };
      m.order.forEach(id => { const c = calc.agent(m.ag[id]); t.prompt += c.prompt; t.read += c.read; t.un += c.un; t.out += c.out; t.wr += c.wr; t.cost += c.cost; t.saved += c.saved; t.calls += m.ag[id].calls; if (c.savedPartial) t.savedPartial = true; t.unpriced += c.unpriced; });
      const v = m.svc;
      if (v) { t.prompt += v.rd + v.un; t.read += v.rd; t.un += v.un; t.out += v.out; t.wr += v.wr; t.cost += v.cost; t.saved += v.saved; if (v.savedPartial) t.savedPartial = true; t.unpriced += v.unpriced; }
      t.hit = t.prompt > 0 ? t.read / t.prompt : 0; t.pct = Math.round(t.hit * 100); t.hit1 = Math.round(t.hit * 1000) / 10;
      return t;
    },
    /** What a compaction (a row of the conversation, an entry of m.compactions) says about its moment, in the terminal's words: the
     *  cache rewritten while cold costs nothing extra (short: the conversation's row; long: the Cache view's note), at a warm moment it is a
     *  declared, priced rebase. A compaction that no plan preceded has no moment and so no price: it says how it was made ("emergency
     *  compaction", as the terminal's feed does), or nothing when the server did not say. Only the words of this table are ever returned. */
    compactNote(c, long) {
      if (c && c.moment === 'cold') return long ? 'the cache was cold, so the rewrite cost nothing extra' : 'cache rewritten while cold: free';
      if (c && c.moment === 'warm') return long ? 'the cache was warm: a declared, priced rebase' : 'a declared, priced rebase';
      return c && (c.mode === 'fork' || c.mode === 'mask' || c.mode === 'emergency') ? c.mode + ' compaction' : '';
    },
    /** Seconds of warm cache left on the shared prefix at view time vt (negative = cold). */
    warmLeft(m, vt) { return m.lastReq < -9000 ? 0 : (m.ttl || 25) - (vt - m.lastReq); },
    /** Workers that are neither idle nor done ("4 active of 8 workers"). */
    active(m) { return m.order.filter(id => id !== 'mgr' && m.ag[id].spawned && !['idle', 'done'].includes(m.ag[id].state)).length; },
    workers(m) { return m.order.filter(id => id !== 'mgr'); },
    started(m) { return m.order.filter(id => id !== 'mgr' && m.ag[id].spawned).length; },
    /** Is a turn running (the manager is working or the goal is active with unfinished work)? */
    turnRunning(m) { return !!m.turn || (!m.final && (m.goal.state === 'active' && (m.order.some(id => ['think', 'tool', 'edit', 'ask'].includes(m.ag[id].state)) || m.torder.some(t => m.tasks[t].st !== 'merged' && m.tasks[t].st !== 'todo')))); },
    openQuestion(m) { return m.qs.find(q => !q.answered) || null; },
    waiting(m) { return m.qs.filter(q => !q.answered).length; },
    mergedCount(m) { return m.merged.length; },
    allMerged(m) { return m.torder.length > 0 && m.torder.every(t => m.tasks[t].st === 'merged'); },
  };

  /** Plan step states of the server (internal/plan) as the plan box draws them. */
  /** Why a question closed without the person's answer (VOCAB 5.17 `by`), in the words of its transcript row. */
  const BY_WORD = { timeout: 'nobody answered in time', canceled: 'canceled', closed: 'the session closed', nobody: 'no page was open' };
  const PLAN_ST = { pending: 'pending', doing: 'act', act: 'act', done: 'done', verify: 'verify', edit: 'edit', ask: 'ask', queued: 'queued' };
  SL.model = { newModel, addAgent, reduce, reduceRange, isVisible, VISIBLE, CHAN_CAP, clean, cleanRoster };
  SL.calc = calc;
})(SL);
