/* 60-actions.js: SL.act (the store's explicit actions), SL.settings (per-viewer settings) and SL.G (state shared by every session).
 *
 * Rule: panels render from state; they never change it. Everything a person can do is one action here, and an action does exactly three
 * things: change meta/state, insert events at the world time (so the change shows up in the chat, the log and the replay), emit 'action'.
 * A setting changed in one place shows everywhere because every panel re-renders from the same meta (mode chip, HUD, drawer, footer ...). */
(function (SL) {
  'use strict';
  const U = SL.u, D = SL.D, { deepCopy, fmtUsd } = U;
  const ACT = {};

  /* ---------------- per-viewer settings (localStorage is a convenience: the page works without it) ---------------- */
  const settings = SL.settings = {
    hover: 'both',            // 'both' = hold on chat + slow on linked items | 'chat' = hold on chat only | 'off'
    motion: 'auto',           // 'auto' (follow the system) | 'reduce' | 'full'
    density: 'comfortable',   // 'comfortable' | 'compact'
    cache: 'quiet',           // 'quiet' (the default: no savings figures, no hit-% on cards, a small warm clock) | 'full' (the Dock v2 amount of cache detail)
    ver: 0,
    load() { try { const o = JSON.parse(localStorage.getItem('sleipnir.web.settings') || '{}'); ['hover', 'motion', 'density', 'cache'].forEach(k => { if (typeof o[k] === 'string') settings[k] = o[k]; }); } catch (e) { /* no storage: defaults */ } },
    save() { try { localStorage.setItem('sleipnir.web.settings', JSON.stringify({ hover: settings.hover, motion: settings.motion, density: settings.density, cache: settings.cache })); } catch (e) { /* ignore */ } },
  };
  const OK = (v, list) => list.includes(v);

  /* ---------------- state shared by every session (SL.G) ---------------- */
  /** the trust ledger: the pack's (the shop's entry is real) plus a directory nobody said yes to */
  function ledger() {
    const L = D.extra.trust && D.extra.trust.ledger; if (!L) return [{ dir: '~/projects/shop', files: 3, state: 'trusted (Jan 1)' }, { dir: '~/projects/orders-api', files: 2, state: 'trusted (Jan 1)' }, { dir: '~/scratch/untrusted-demo', files: 4, state: 'not trusted' }];
    const day = d => { const m = /^\d{4}-(\d\d)-(\d\d)$/.exec(d || ''); return m ? ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'][+m[1] - 1] + ' ' + (+m[2]) : d; };
    return L.map(l => ({ dir: l.dir, files: l.files, saved: l.saved, now: l.now, state: l.state === 'trusted' ? 'trusted (' + day(l.saved) + ')' : l.state === 'changed' ? 'changed since your yes: ' + l.now : 'gone: ' + l.now })).concat([{ dir: '~/scratch/untrusted-demo', files: 4, saved: '', now: 'no yes given', state: 'not trusted' }]);
  }
  const G = SL.G = {
    ver: 0, mcp: deepCopy(D.mcp), favs: new Set(D.models.filter(m => m.fav).map(m => m.ref)),
    trust: D.trustFiles.map(([f, h]) => ({ file: f, hash: h, state: 'trusted' })), trustDirs: ledger(),
    providers: deepCopy(D.providers), schedule: deepCopy(D.schedule), roleModels: Object.assign({}, D.roleModels), history: [],
  };

  const act = (name, fn) => { ACT[name] = function () { const r = fn.apply(null, arguments); SL.bus.emit('action', { name, args: Array.prototype.slice.call(arguments) }); return r; }; };
  const ses = sid => sid ? SL.sessions.get(sid) : SL.sessions.active;
  const say = (S, text, glyph, extra) => S.add({ k: 'say', who: 'sys', glyph: glyph || '⚙', text, ...(extra || {}) });
  const ownerOf = (S, ev) => ev.own || ev.id || (ev.k === 'mail' ? ev.from : null) || (ev.q && ev.q.agent) || (ev.k === 'task' || ev.k === 'merge' ? ((S.wm.tasks[ev.id] || {}).owner) : null) || null;
  /** Remove not-yet-applied events (index >= widx) of one agent (or all when id is null). Returns how many. */
  function cancelFuture(S, id) {
    const keep = [], drop = []; S.log.forEach((e, i) => { if (i >= S.widx && (id ? ownerOf(S, e) === id : ownerOf(S, e) && ownerOf(S, e) !== 'you')) drop.push(e); else keep.push(e); });
    S.log = keep; return drop;
  }

  /* ---------------- settings actions ---------------- */
  act('setHover', v => { if (OK(v, ['both', 'chat', 'off'])) { settings.hover = v; settings.ver++; SL.time.setMode(v); settings.save(); } });
  act('setMotion', v => { if (OK(v, ['auto', 'reduce', 'full'])) { settings.motion = v; settings.ver++; SL.bus.emit('motion'); settings.save(); } });
  act('setCache', v => { if (OK(v, ['quiet', 'full'])) { settings.cache = v; settings.ver++; document.body.classList.toggle('cq', v === 'quiet'); settings.save(); SL.bus.emit('cache-detail', v); } });
  act('setDensity', v => { if (OK(v, ['comfortable', 'compact'])) { settings.density = v; settings.ver++; document.body.classList.toggle('compact', v === 'compact'); settings.save(); } });

  /* ---------------- per-session actions ---------------- */
  const CYCLE = ['default', 'accept-edits', 'plan'];
  act('setMode', (mode, opt, sid) => {
    const S = ses(sid), m = D.modes.find(x => x.id === mode); if (!S || !m) return { ok: false, why: 'unknown mode ' + mode };
    if (m.danger && !(opt && opt.confirm === mode)) return { ok: false, why: mode + ' is set only by typing its name to confirm' };
    S.setMeta({ mode }); say(S, 'mode: ' + mode + (mode === 'plan' ? ' (read-only)' : m.danger ? ' (dangerous: ' + m.desc + ')' : ''), m.danger ? '⚠' : '⚙'); return { ok: true };
  });
  /** shift+tab: default → accept-edits → plan → default. Never bypass, never yolo; from either of those it returns to default. */
  act('cycleMode', sid => { const S = ses(sid); if (!S) return; const i = CYCLE.indexOf(S.meta.mode), nx = CYCLE[(i + 1) % CYCLE.length]; return ACT.setMode(i < 0 ? 'default' : nx, null, sid); });
  act('setModel', (ref, sid) => { const S = ses(sid), mm = D.model(ref); if (!S) return; S.meta.model = ref; S.roster[0].model = ref; S.touch(); say(S, 'model: ' + ref + (mm && mm.in == null ? ' (price unknown)' : '') + '; a team starts again on it'); return { ok: true }; });
  act('setRoleModel', (role, ref, sid) => { const S = ses(sid); if (!S) return; if (role === 'manager') return ACT.setModel(ref, sid); S.meta.roleModels = Object.assign({}, S.meta.roleModels, { [role]: ref }); S.roster.forEach(r => { if (r.role === role) r.model = ref; }); S.touch(); say(S, 'role ' + role + ' runs on ' + ref + ' (restarts the workers of that role)'); return { ok: true }; });
  act('setEffort', (lv, sid) => { const S = ses(sid); if (!S) return; S.setMeta({ effort: lv }); say(S, 'reasoning effort: ' + lv + ' (closest supported level)'); });
  act('setBudget', (usd, sid) => {
    const S = ses(sid); if (!S) return { ok: false }; const v = usd === 'off' || usd === null || usd === 0 ? 0 : parseFloat(usd); if (isNaN(v) || v < 0) return { ok: false, why: 'a budget is a number of dollars, or off' };
    S.setMeta({ budget: v }); say(S, v ? 'budget: ' + fmtUsd(v, 2) + ' for the turns from now on' : 'budget: off', '⚙');
    if (v && SL.calc.totals(S.wm).cost >= v && S.wm.goal.state === 'active') { S.add({ k: 'goal', s: 'paused' }); say(S, 'budget reached: the goal is paused', '⚠'); }
    return { ok: true };
  });
  act('setIsolation', (v, sid) => { const S = ses(sid); if (S && OK(v, ['none', 'worktree'])) { S.setMeta({ isolation: v }); say(S, '--isolation ' + v + ' (applies when the team starts again)'); } });
  act('setVerify', (v, sid) => { const S = ses(sid); if (S) { S.setMeta({ verify: v }); say(S, '--verify "' + v + '"'); } });
  act('setFlag', (flag, on, sid) => { const S = ses(sid); if (S && OK(flag, ['commit', 'mailman', 'noMcp', 'trustProject'])) { S.setMeta({ [flag]: !!on }); say(S, ({ commit: '--commit', mailman: '--mailman', noMcp: '--no-mcp', trustProject: '--trust-project' })[flag] + ' ' + (on ? 'on' : 'off') + ' (applies when the team starts again)'); } });
  /* rules: effect allow | deny | ask; the origin is shown on the Permissions page */
  act('allowRule', (rule, origin, sid) => addRule(ses(sid), 'allow', rule, origin || 'this session'));
  act('denyRule', (rule, origin, sid) => addRule(ses(sid), 'deny', rule, origin || 'this session'));
  act('askRule', (rule, origin, sid) => addRule(ses(sid), 'ask', rule, origin || 'this session'));
  function addRule(S, effect, rule, origin) {
    if (!S) return { ok: false }; rule = String(rule || '').trim(); if (!rule) return { ok: false, why: 'a rule needs text, e.g. tests or Bash(go test:*)' };
    const rules = rule === 'tests' ? D.testsPreset : [rule];
    rules.forEach(r => { if (!S.meta.rules.some(x => x.effect === effect && x.rule === r)) S.meta.rules.push({ effect, rule: r, origin: rule === 'tests' ? 'the tests preset' : origin }); });
    S.touch(); say(S, effect + ' this session: ' + (rule === 'tests' ? 'tests (' + rules.length + ' rules)' : rule) + ' · from ' + origin, effect === 'deny' ? '⚠' : '⚙'); return { ok: true, added: rules.length };
  }
  act('removeRule', (rule, sid) => { const S = ses(sid); if (!S) return; const n = S.meta.rules.length; S.meta.rules = S.meta.rules.filter(r => r.rule !== rule); S.touch(); if (S.meta.rules.length < n) say(S, 'rule removed: ' + rule); });

  /* goals */
  act('setGoal', (text, sid) => {
    const S = ses(sid); if (!S) return { ok: false }; text = String(text || '').trim(); if (!text) return { ok: false, why: '/goal needs text' };
    if (S.kind === 'shop' || S.kind === 'docs') { say(S, 'mock: the fixture keeps its goal; a new session takes a new one (New session)', '⚠'); return { ok: false, fixture: true }; }
    S.setMeta({ goalText: text }); S.add([{ k: 'say', who: 'you', text: '/goal ' + text }, { k: 'goal', s: 'active' }, { k: 'say', who: 'sys', glyph: '◇', text: 'goal set (mock: the scripted run for a new session continues)' }]); return { ok: true };
  });
  act('pauseGoal', sid => { const S = ses(sid); if (!S) return; if (S.wm.goal.state === 'active') { S.add({ k: 'goal', s: 'paused' }); say(S, 'goal paused', '⏸'); } });
  act('resumeGoal', sid => {
    const S = ses(sid); if (!S) return; if (S.wm.goal.state !== 'paused') return; S.add({ k: 'goal', s: 'active' }); say(S, 'goal resumed', '▶');
    Object.keys(S.interrupted || {}).forEach(id => resumeAgent(S, id));
  });
  act('clearGoal', sid => { const S = ses(sid); if (!S) return; S.add({ k: 'goal', s: 'cleared' }); S.endgamed = true; say(S, 'goal cleared', '◇'); S.setMeta({ goalText: '' }); });

  /* the question: the quiet-period rule lives in approvals.js, not here */
  act('answerQuestion', (qid, choice, note, sid) => {
    const S = ses(sid) || SL.sessions.active; if (!S) return { ok: false };
    const q = S.wm.qs.find(x => x.id === qid && !x.answered); if (!q) return { ok: false, why: 'that question is not open' };
    const a = S.wt; S.add({ k: 'answer', qid, choice, note, t: a });
    if (choice === 2) addRule(S, 'allow', q.rule || 'Bash(' + q.cmd + ')', "don't ask again");   /* real: a command that is not a build or test one is remembered as the exact request */
    if (q.cont) S.add(SL.scripts.afterAnswer(S, q, choice, note, a));
    S.touch(); return { ok: true };
  });
  function resumeAgent(S, id) {
    const rec = S.interrupted && S.interrupted[id]; if (!rec) return; delete S.interrupted[id];
    const b = new SL.scripts.SB(); b.vqFree = Math.max(S.vqFree, S.wt); const a = S.wt + 0.3, task = rec.task;
    b.state(a, id, 'think', 'picking up ' + (task || 'its task') + ' again', task); b.req(a + 0.2, id, SL.scripts.liveRatio(id, 31), { p: SL.scripts.promptPer(id), o: SL.scripts.outPer(id) });
    if (task && S.wm.tasks[task] && S.wm.tasks[task].st !== 'merged') { b.step(a + 2, id, 'edit', 'Edit', rec.file || (S.wm.ag[id].scope.split(',')[0].replace('**', 'main.go')), 'resumed work', task, { add: 3, del: 1 }); const end = b.submit(a + 5, id, task, 'go test ./...', 1.2, null, { keepPlan: true }); (rec.planDone || []).forEach(n => b.e(end, 'plan', { n, s: 'done' })); }
    else b.state(a + 1, id, 'idle', 'waits for work', task);
    S.vqFree = b.vqFree; S.add(b.ev);
  }
  /** Interrupt the running turn (Esc): the manager's turn stops, the workers with it, and the goal is paused. The person talks to the manager only, so no single worker can be interrupted. */
  act('interrupt', (target, sid) => {
    const S = ses(sid); if (!S) return { ok: false }; S.interrupted = S.interrupted || {};
    if (target !== 'turn' && target !== 'mgr') return { ok: false, why: 'you talk to the manager only: interrupt the turn, and the manager stops the team' };
    const stop = id => { const A = S.wm.ag[id]; if (!A || !A.spawned || ['idle', 'done'].includes(A.state)) return false; const dropped = cancelFuture(S, id); S.interrupted[id] = { task: A.task, planDone: dropped.filter(e => e.k === 'plan' && e.s === 'done').map(e => e.n) }; S.add([{ k: 'interrupt', id }, { k: 'state', id, s: 'idle', doing: 'interrupted by you (esc)', task: A.task }]); return true; };
    if (target === 'turn' || target === 'mgr') {
      if (!SL.calc.turnRunning(S.wm)) return { ok: false, why: 'no turn is running' };
      S.wm.order.forEach(id => { if (id !== 'mgr') stop(id); }); cancelFuture(S, 'mgr');
      S.add([{ k: 'interrupt', id: 'turn' }, { k: 'goal', s: 'paused' }, { k: 'state', id: 'mgr', s: 'wait', doing: 'turn interrupted; the goal is paused (/goal resume)', task: null }]); S.endgamed = false; S.touch(); return { ok: true };
    }
    return { ok: false };
  });
  /** Steer (`/steer TEXT`): guidance for the running turn of the MANAGER, without stopping it. The manager answers and notes it in its next step. */
  act('steer', (agent, text, sid) => {
    const S = ses(sid); if (!S) return { ok: false }; text = String(text || '').trim(); if (!text) return { ok: false, why: 'write what to tell the manager' };
    if (agent !== 'mgr') return { ok: false, why: 'you talk to the manager only: tell it, and it tells the worker' };
    const A = S.wm.ag[agent]; if (!A) return { ok: false, why: 'no such agent' };
    const r = SL.scripts.steerReply(S, agent, text), a = S.wt;
    S.add([{ k: 'steer', to: agent, text }, { k: 'reply', id: agent, text: r.say, t: a + 1.1 }, { k: 'tool', id: agent, name: r.tool.name, arg: r.tool.arg, out: r.tool.out, t: a + 1.6, task: A.task }]);
    S.touch(); return { ok: true };
  });
  act('compact', (focus, sid) => {
    const S = ses(sid); if (!S) return { ok: false }; const g5 = D.g5.mgr || 1400, to = Math.round(g5 * 0.45 / 5) * 5, pct = -Math.round((1 - to / g5) * 100);
    S.add([{ k: 'compact', id: 'mgr', from: g5, to, pct }, { k: 'req', id: 'mgr', ratio: SL.scripts.liveRatio('mgr', 40), p: SL.scripts.promptPer('mgr'), o: 200 }, { k: 'say', who: 'sys', glyph: '◆', text: 'folded the older thread' + (focus ? '; keeping in view: ' + focus : '') + ' · a declared, priced rebase' }]); S.touch(); return { ok: true, from: g5, to };
  });
  /* the workspace: the person's marks live in S.ws (reviewed files, reverted hunks, a restore); the history itself is the recorded log */
  const WS = S => S.ws || (S.ws = { reviewed: {}, reverted: {}, restore: null });
  act('markReviewed', (path, cpid, on, sid) => { const S = ses(sid); if (!S) return; const w = WS(S); if (on) w.reviewed[path] = cpid; else delete w.reviewed[path]; S.touch(); });
  act('revertHunk', (path, key, sid) => { const S = ses(sid); if (!S) return { ok: false }; WS(S).reverted[path + '#' + key] = S.wt; say(S, 'reverted a hunk of ' + path + ' (mock: nothing is written; the agent that wrote it is told to read the file again)', '↺'); S.touch(); return { ok: true }; });
  act('unrevertHunk', (path, key, sid) => { const S = ses(sid); if (!S) return; delete WS(S).reverted[path + '#' + key]; S.touch(); });
  /** /rewind ID: a safety checkpoint first, then every file touched in ID or later is put back as it was when ID began (the mock records it, writes nothing) */
  act('rewind', (id, sid) => {
    const S = ses(sid); if (!S) return { ok: false }; const c = S.wm.ckpts.find(x => x.id === id); if (!c || c.skipped || c.safety) return { ok: false, why: 'nothing to put back' };
    const I = SL.ws && SL.ws.info(S), idx = I ? I.pos.findIndex(x => x.id === id) : -1, files = idx >= 0 ? SL.ws.filesFrom(I, idx) : [];
    const safety = id + 's', n = files.length || c.files;
    S.add([{ k: 'ckpt', cid: safety, ts: SL.u.tod(S.meta.t0, S.wt), files: n, note: 'before restoring ' + id, skipped: false, safety: true }]);
    WS(S).restore = { to: id, files, at: S.wt, safety };
    say(S, 'restored the files of ' + id + ' (' + n + ' file' + (n === 1 ? '' : 's') + '): ' + c.note + ' · safety checkpoint ' + safety + ' taken first (mock: nothing is written)', '↺'); S.touch(); return { ok: true };
  });
  act('undoRewind', sid => { const S = ses(sid); if (!S || !WS(S).restore) return { ok: false }; const r = WS(S).restore; WS(S).restore = null; say(S, 'restore of ' + r.to + ' undone: the files are as the safety checkpoint ' + r.safety + ' had them (mock)', '↺'); S.touch(); return { ok: true }; });
  act('send', (text, sid) => {
    const S = ses(sid); if (!S) return { ok: false }; text = String(text || '').trim(); if (!text) return { ok: false };
    S.hist.push(text);
    const busy = SL.calc.turnRunning(S.wm);
    if (busy) { S.ui.queued.push({ text, at: SL.time.wall + 1400 }); S.touch(); return { ok: true, queued: true }; }
    deliver(S, text); return { ok: true };
  });
  function deliver(S, text) {
    const a = S.wt, reply = SL.scripts.chatReply(S, text);
    S.add([{ k: 'say', who: 'you', text, t: a }, { k: 'say', who: 'mgr', text: reply, t: a + 1.2 }]);
    if (S.kind === 'orders' && /commit|push/i.test(text) && !S.openQuestion()) {
      S.add([{ k: 'state', id: 'mgr', s: 'ask', doing: 'wants to run `git commit -am "orders: fix off-by-one in List"`', task: null, t: a + 3 }, { k: 'ask', t: a + 3, q: { id: 'q-orders-' + a.toFixed(0), agent: 'mgr', task: null, cmd: 'git commit -am "orders: fix off-by-one in List"', cwd: '.', why: 'writes a commit to the repository; it is not a build or test command', scope: 'cwd . (the single agent)', what: 'this command', cont: 'generic' } }]);
    }
  }
  /** Deliver queued typed-ahead lines (called each frame with wall time). */
  function pump() { SL.sessions.list.forEach(S => { const q = S.ui.queued; if (q.length && q[0].at <= SL.time.wall) { deliver(S, q.shift().text); S.touch(); } }); }

  /* sessions */
  act('switchSession', id => SL.sessions.activate(id));
  act('newSession', spec => { const S = SL.sessions.create(spec, true); return S; });
  act('closeSession', id => SL.sessions.close(id));
  act('renameSession', (id, name) => SL.sessions.rename(id, name));
  act('resumeSession', (recId, opt) => {
    const rec = recId === 'latest' ? SL.sessions.recorded.find(r => r.resumable) : SL.sessions.recorded.find(r => r.id === recId);
    if (!rec) return { ok: false, why: 'no recorded session to resume' }; if (!rec.resumable) return { ok: false, why: rec.id + ' cannot be resumed (older than the checkpoint window)' };
    const S = SL.sessions.create({ name: 'resume-' + rec.id.slice(9, 15), cwd: '~/projects/shop', model: rec.model, swarm: 8, resumedFrom: rec, launch: 'sleipnir chat --resume ' + rec.id }, true); return { ok: true, S };
  });
  act('restartTeam', (patch, sid) => {
    const S = ses(sid); if (!S) return { ok: false }; patch = Object.assign({}, patch); const force = patch.force; delete patch.force; const n = patch.swarm == null ? S.meta.swarm : Math.max(0, Math.min(12, parseInt(patch.swarm, 10) || 0));
    if (!force && n === S.meta.swarm && !Object.keys(patch).some(k => k !== 'swarm')) return { ok: false, why: 'already manager + ' + n + ' workers' };
    Object.assign(S.meta, patch, { swarm: n }); if (S.kind !== 'new') S.kind = 'new';
    S.plan = SL.sessions.PLANS.generic; S.meta.launch = 'sleipnir chat --swarm ' + n; S.initRun(); S.ui.chan = 'mgr';
    if (SL.sessions.active === S) S.rebuild(S.wt); S.touch(); SL.bus.emit('sessions-changed'); return { ok: true };
  });
  act('newChat', sid => { const S = ses(sid); if (!S) return; S.meta.goalText = ''; return ACT.restartTeam({ swarm: S.meta.swarm, force: true }, sid); });
  act('pruneSessions', (older, keep, apply) => SL.sessions.prune(older, keep, apply));
  act('favModel', ref => { if (G.favs.has(ref)) G.favs.delete(ref); else G.favs.add(ref); const m = D.model(ref); if (m) m.fav = G.favs.has(ref); SL.bus.emit('models-changed'); });
  act('trustDir', (dir, on) => { let d = G.trustDirs.find(x => x.dir === dir); if (!d) { d = { dir, files: 3, saved: '', now: '', state: 'not trusted' }; G.trustDirs.push(d); } d.state = on ? 'trusted (today)' : 'not trusted'; d.now = on ? 'unchanged' : d.now; G.ver++; SL.bus.emit('trust-changed'); });
  act('setProvider', (id, patch) => { const p = G.providers.find(x => x.id === id); if (p) { Object.assign(p, patch); G.ver++; } });
  act('setMcp', (name, patch) => { const s = G.mcp.find(x => x.name === name); if (s) { Object.assign(s, patch); SL.bus.emit('mcp-changed'); } });

  SL.act = ACT; SL.actions = { cancelFuture, ownerOf, pump, deliver, resumeAgent };
})(SL);
