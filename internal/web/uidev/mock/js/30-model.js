/* 30-model.js: SL.model, the pure reducer from events to a session's model, and SL.calc, every number derived from it.
 *
 * An event is {t, k, ...fields} (t = session seconds). `reduce(m, ev, ctx)` is the ONLY thing that changes a model. It runs twice per
 * session: on the world model (events up to the world clock, no chat rows, drives the director and needs-you) and on the view model
 * (events up to the view clock, with chat rows). Both read the same events in the same order, so after a catch-up the two are equal.
 *
 * Every number on screen is derived from the per-agent token table in the model (read, uncached, out): hit, cost, savings, the HUD ring,
 * the Cache tab, the stalls and the drawer all call SL.calc, so they agree to the percent at every moment.
 *
 * Event kinds (see ARCHITECTURE.md for the full table): say, sys, tool, note, state, task, plan, verdict, req, use, warm, gov, mail, ckpt,
 * ask, answer, queue, merge, break, compact, stream, goal, final, local, steer, reply, interrupt, refuse, digest. */
(function (SL) {
  'use strict';
  const { upperBound } = SL.u;
  const D = SL.D;
  const CHAN_CAP = 4500;          // entries kept per channel in memory (the DOM keeps far fewer)
  const MARK_CAP = 800, SEG_CAP = 1200;
  const VISIBLE = new Set(['say', 'sys', 'tool', 'note', 'mail', 'break', 'compact', 'ask', 'merge', 'steer', 'reply', 'refuse', 'interrupt', 'local']);

  const roleOf = id => id === 'mgr' ? 'manager' : null;
  const priceOf = id => id === 'mgr' ? D.prices.mgr : D.prices.worker;

  /** Build an empty model for a session roster: [{id, role, code, nth, k, leg, scope, ro, spawn, model}]. */
  function newModel(S, opts) {
    const m = { sid: S.id, t: 0, ver: 0, nid: 0, chat: !(opts && opts.noChat), ag: {}, order: [], tasks: {}, torder: [], q: null, merged: [], conflicts: 0, bounced: 0,
      mail: [], marks: [], plan: D.plan.map(() => 'pending'), verdict: '', goal: { state: S.meta.goalText ? 'active' : 'none' }, qs: [], anomalies: [], compactions: [], ckpts: [],
      reqLog: [], flash: { t: -999, id: null }, lastReq: -9999, lastBreakT: -9999, streams: {}, diff: {}, rpm: 0, rpmHist: [], final: null, chan: { mgr: [], mail: [] }, folded: {}, steps: 0, refused: 0 };
    S.roster.forEach(r => {
      m.ag[r.id] = { id: r.id, role: r.role, code: r.code, nth: r.nth, k: r.k, leg: r.leg, scope: r.scope, ro: r.ro, model: r.model, state: 'idle', doing: r.id === 'mgr' ? 'waits for the first message' : 'not started',
        task: null, rd: 0, un: 0, out: 0, calls: 0, ratios: [], nreq: 0, lastReq: -999, segs: [], cur: null, spawned: r.id === 'mgr', spawnT: r.spawn, steered: null, stateT: 0 };
      m.order.push(r.id); if (r.id !== 'mgr') m.chan[r.id] = [];
      if (r.id === 'mgr') m.ag[r.id].cur = null;
    });
    return m;
  }

  /** Push a transcript entry. `animate`/`digest` come from ctx: collapsed rows are kept (state equality) but flagged and, for a digest, summarised. */
  function push(m, ctx, ch, entry, ev) {
    if (!m.chat) return;
    const list = m.chan[ch] || (m.chan[ch] = []);
    entry.id = ++m.nid; if (entry.t == null) entry.t = ev.t;
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
        else if (ev.who === 'sys') push(m, ctx, 'mgr', { k: 'sys', glyph: ev.glyph || '◇', text: ev.text, plan: !!ev.plan, task: ev.task, t: ev.t }, ev);
        else if (ev.who === 'scouts') push(m, ctx, 'mgr', { k: 'scouts', lines: ev.lines, t: ev.t }, ev);
        else if (ev.who === 'mgr') push(m, ctx, 'mgr', { k: 'say', ag: 'mgr', text: ev.text, stream: ev.stream !== false, rate: ev.rate || 70, t: ev.t }, ev);
        else if (ev.who === 'local') push(m, ctx, 'mgr', { k: 'local', title: ev.title, html: ev.html, t: ev.t }, ev);
        break;
      }
      case 'sys': push(m, ctx, ev.ch || 'mgr', { k: 'sys', glyph: ev.glyph || '◇', text: ev.text, ag: ev.ag, task: ev.task, t: ev.t }, ev); break;
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
        A.state = ev.s; A.doing = ev.doing != null ? ev.doing : A.doing; if (ev.task !== undefined) A.task = ev.task; A.spawned = true; A.stateT = ev.t;
        A.cur = { s: ev.s, t0: ev.t, t1: null, doing: A.doing }; A.segs.push(A.cur); if (A.segs.length > SEG_CAP) A.segs.splice(0, 200);
        delete m.streams[ev.id];
        if (prev !== ev.s && ['ask', 'done', 'stuck', 'idle', 'wait'].includes(ev.s) && ev.id !== 'mgr') push(m, ctx, chOf(ev.id), { k: 'st', ag: ev.id, s: ev.s, text: A.doing, task: A.task, t: ev.t }, ev);
        break;
      }
      case 'task': {
        let T = m.tasks[ev.id];
        if (!T) { T = m.tasks[ev.id] = { id: ev.id, title: ev.title || ev.id, owner: ev.owner || null, deps: ev.deps || [], scope: ev.scope || '', st: 'todo', t: ev.t, ms: 0 }; m.torder.push(ev.id); }
        if (ev.s) { T.st = ev.s; T.t = ev.t; if (ev.s === 'merged' && !m.merged.includes(ev.id)) m.merged.push(ev.id); }
        break;
      }
      case 'plan': m.plan[ev.n] = ev.s; break;
      case 'verdict': m.verdict = ev.text; break;
      case 'req': {
        if (!A) break;
        A.ratios.push(ev.ratio); A.nreq++; A.lastReq = ev.t;
        if (!ev.hist) {
          const p = ev.p || Math.round((A.rd + A.un) / Math.max(1, A.nreq - 1)) || 1200;
          const rd = Math.round(p * ev.ratio); A.rd += rd; A.un += Math.round(p) - rd; A.out += ev.o || 0;
          m.lastReq = ev.t; m.reqLog.push({ t: ev.t, id: ev.id, ratio: ev.ratio }); if (m.reqLog.length > 200) m.reqLog.shift();
          m.marks.push({ t: ev.t, id: ev.id, g: 'req' }); if (m.marks.length > MARK_CAP) m.marks.shift();
        }
        break;
      }
      case 'use': if (A) { A.rd = ev.rd; A.un = ev.un; A.out = ev.out; } break;
      case 'warm': m.lastReq = ev.t; break;
      case 'gov': m.rpm = ev.rpm; m.rpmHist.push({ t: ev.t, rpm: ev.rpm }); if (m.rpmHist.length > 60) m.rpmHist.shift(); break;
      case 'mail': {
        m.mail.push({ t: ev.t, from: ev.from, to: ev.to, text: ev.text }); if (m.mail.length > 400) m.mail.shift();
        m.marks.push({ t: ev.t, id: ev.from, g: 'mail' }); if (m.marks.length > MARK_CAP) m.marks.shift();
        const e = { k: 'mail', from: ev.from, to: ev.to, text: ev.text, t: ev.t };
        push(m, ctx, 'mail', e, ev); if (ev.from !== 'you') push(m, ctx, chOf(ev.from), Object.assign({}, e), ev); if (ev.to !== ev.from) push(m, ctx, chOf(ev.to), Object.assign({}, e), ev);
        push(m, ctx, 'mgr', { k: 'feed', ag: ev.from, g: 'mail', text: 'mail → ' + ev.to, task: A && A.task, to: ev.to, t: ev.t }, ev);
        if (d) d.mails++;
        break;
      }
      case 'ckpt': m.ckpts.unshift({ id: ev.step ? 'c' + String(m.ckpts.length + 1).padStart(2, '0') : (ev.cid || ev.id), step: ev.step || null, ts: ev.ts, files: ev.files, note: ev.note, skipped: !!ev.skipped, safety: !!ev.safety }); break;   /* `step` names the recorded change set this checkpoint holds; the id is numbered by arrival */
      case 'ask': {
        const q = Object.assign({}, ev.q, { t0: ev.t, answered: null, choice: null }); m.qs.push(q); m.q = m.qs.find(x => !x.answered) || null;
        m.marks.push({ t: ev.t, id: q.agent, g: 'ask' });
        push(m, ctx, 'mgr', { k: 'feed', ag: q.agent, g: 'ask', text: 'asks: ' + q.cmd, task: q.task, t: ev.t }, ev);
        push(m, ctx, chOf(q.agent), { k: 'ask', ag: q.agent, q, t: ev.t }, ev);
        if (d) d.asks++;
        break;
      }
      case 'answer': {
        const q = m.qs.find(x => x.id === ev.qid);
        if (q) { q.answered = ev.choice; q.tAns = ev.t; q.note = ev.note; }
        m.q = m.qs.find(x => !x.answered) || null;
        if (q) push(m, ctx, 'mgr', { k: 'sys', glyph: '❯', text: 'you answered ' + ev.choice + ' to ' + q.agent + ': ' + q.cmd, ag: q.agent, t: ev.t }, ev);
        break;
      }
      case 'queue': m.qHead = ev.head ? { task: ev.head, cmd: ev.cmd, step: ev.step, t0: (m.qHead && m.qHead.task === ev.head) ? m.qHead.t0 : ev.t, ms: ev.ms } : null; break;
      case 'merge': {
        const T = m.tasks[ev.id]; if (!T) break; T.st = 'merged'; T.ms = ev.ms; T.t = ev.t; if (!m.merged.includes(ev.id)) m.merged.push(ev.id);
        m.marks.push({ t: ev.t, id: T.owner, g: 'merge' }); m.lastMerge = ev;
        push(m, ctx, 'mgr', { k: 'sys', glyph: '✓', text: ev.id + ' merged: rebase ✓ · ' + ev.cmd + ' ✓ ' + (ev.ms < 1000 ? ev.ms + 'ms' : (ev.ms / 1000).toFixed(1) + 's'), task: ev.id, ag: T.owner, t: ev.t }, ev);
        if (T.owner) push(m, ctx, chOf(T.owner), { k: 'note', ag: T.owner, g: 'done', text: ev.id + ' merged ✓ ' + ev.cmd + ' ' + (ev.ms / 1000).toFixed(1) + 's', task: ev.id, t: ev.t }, ev);
        if (d) d.merged.push(ev.id);
        break;
      }
      case 'break': {
        m.anomalies.push({ t: ev.t, id: ev.id, kind: ev.kind, read: ev.read, expected: ev.expected, why: ev.why });
        m.flash = { t: ev.t, id: ev.id }; m.lastBreakT = ev.t; m.marks.push({ t: ev.t, id: ev.id, g: 'break' });
        push(m, ctx, 'mgr', { k: 'break', ag: ev.id, kind: ev.kind, read: ev.read, expected: ev.expected, why: ev.why, t: ev.t }, ev);
        push(m, ctx, chOf(ev.id), { k: 'break', ag: ev.id, kind: ev.kind, read: ev.read, expected: ev.expected, why: ev.why, t: ev.t }, ev);
        if (d) d.breaks++;
        break;
      }
      case 'compact': {
        m.compactions.push({ t: ev.t, id: ev.id, from: ev.from, to: ev.to, pct: ev.pct }); m.marks.push({ t: ev.t, id: ev.id, g: 'compact' });
        push(m, ctx, 'mgr', { k: 'compact', ag: ev.id, from: ev.from, to: ev.to, pct: ev.pct, t: ev.t }, ev);
        if (ev.id !== 'mgr') push(m, ctx, chOf(ev.id), { k: 'compact', ag: ev.id, from: ev.from, to: ev.to, pct: ev.pct, t: ev.t }, ev);
        if (d) d.compacts++;
        break;
      }
      case 'stream': {
        m.streams[ev.id] = { t0: ev.t, text: ev.text, rate: ev.rate, code: !!ev.code };
        const e = { k: 'stream', ag: ev.id, text: ev.text, rate: ev.rate, code: !!ev.code, t: ev.t };
        push(m, ctx, chOf(ev.id), e, ev); if (!ev.code && ev.id !== 'mgr') push(m, ctx, 'mgr', Object.assign({}, e), ev);
        break;
      }
      case 'diff': m.diff[ev.file] = ev; break;
      case 'goal': m.goal = Object.assign({}, m.goal, { state: ev.s }); break;
      case 'final': m.final = { t: ev.t, steps: m.steps }; push(m, ctx, 'mgr', { k: 'final', t: ev.t }, ev); break;
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
        m.marks.push({ t: ev.t, id: ev.id, g: 'refuse' });
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
      return { prompt, read: a.rd, un: a.un, out: a.out, hit, pct: Math.round(hit * 100), cost: (a.un * pr.in + a.rd * pr.cached + a.out * pr.out) / 1e6, saved: a.rd * (pr.in - pr.cached) / 1e6 };
    },
    /** Totals over every agent of a model (manager included). */
    totals(m) {
      const t = { prompt: 0, read: 0, un: 0, out: 0, cost: 0, saved: 0, calls: 0 };
      m.order.forEach(id => { const c = calc.agent(m.ag[id]); t.prompt += c.prompt; t.read += c.read; t.un += c.un; t.out += c.out; t.cost += c.cost; t.saved += c.saved; t.calls += m.ag[id].calls; });
      t.hit = t.prompt > 0 ? t.read / t.prompt : 0; t.pct = Math.round(t.hit * 100); t.hit1 = Math.round(t.hit * 1000) / 10;
      return t;
    },
    /** Seconds of warm cache left on the shared prefix at view time vt (negative = cold). */
    warmLeft(m, vt) { return m.lastReq < -9000 ? 0 : 25 - (vt - m.lastReq); },
    /** Workers that are neither idle nor done ("4 active of 8 workers"). */
    active(m) { return m.order.filter(id => id !== 'mgr' && m.ag[id].spawned && !['idle', 'done'].includes(m.ag[id].state)).length; },
    workers(m) { return m.order.filter(id => id !== 'mgr'); },
    started(m) { return m.order.filter(id => id !== 'mgr' && m.ag[id].spawned).length; },
    /** Is a turn running (the manager is working or the goal is active with unfinished work)? */
    turnRunning(m) { return !m.final && (m.goal.state === 'active' && (m.order.some(id => ['think', 'tool', 'edit', 'ask'].includes(m.ag[id].state)) || m.torder.some(t => m.tasks[t].st !== 'merged' && m.tasks[t].st !== 'todo'))); },
    openQuestion(m) { return m.qs.find(q => !q.answered) || null; },
    waiting(m) { return m.qs.filter(q => !q.answered).length; },
    mergedCount(m) { return m.merged.length; },
    allMerged(m) { return m.torder.length > 0 && m.torder.every(t => m.tasks[t].st === 'merged'); },
  };

  SL.model = { newModel, reduce, reduceRange, isVisible, VISIBLE, CHAN_CAP };
  SL.calc = calc;
})(SL);
