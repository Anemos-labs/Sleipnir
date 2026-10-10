/* 60-actions.js: SL.act (every action a person can take) and SL.settings (per-viewer settings). SL.G, the state shared by every
 * session, is built by 11-data-live.js.
 *
 * Rule: panels render from state; they never change it. Every action has the name, arguments and synchronous result of the same
 * action of the reference page: it runs the same checks (same `why` texts) and returns {ok: false, why} when one fails; otherwise
 * it sends the request and returns {ok: true, done} at once, where `done` is the promise of the API result. The effect on the
 * screen comes from the events and the meta the server sends back, never from a local insertion; a refused request becomes a
 * toast with the server's sentence ('warm' for a 409, 'err' otherwise). The only exceptions are UI-local state (settings, the
 * favourite star, the reviewed mark) and the line the composer shows as queued while the request is on its way. */
(function (SL) {
  'use strict';
  const U = SL.u, D = SL.D, G = SL.G, { fmtUsd } = U;
  const ACT = {};
  const api = () => SL.api;

  /* ---------------- per-viewer settings (localStorage is a convenience: the page works without it) ---------------- */
  const settings = SL.settings = {
    hover: 'both',            // 'both' = hold on chat + slow on linked items | 'chat' = hold on chat only | 'off'
    motion: 'auto',           // 'auto' (follow the system) | 'reduce' | 'full'
    density: 'comfortable',   // 'comfortable' | 'compact'
    cache: 'quiet',           // 'quiet' (the default: no savings figures, no hit-% on cards, a small warm clock) | 'full' (every cache figure)
    title: 'auto',            // the tab title badge: 'auto' (the server's default), 'on' or 'off'
    ver: 0,
    load() { try { const o = JSON.parse(localStorage.getItem('sleipnir.web.settings') || '{}'); ['hover', 'motion', 'density', 'cache', 'title'].forEach(k => { if (typeof o[k] === 'string') settings[k] = o[k]; }); } catch (e) { /* no storage: defaults */ } },
    save() { try { localStorage.setItem('sleipnir.web.settings', JSON.stringify({ hover: settings.hover, motion: settings.motion, density: settings.density, cache: settings.cache, title: settings.title })); } catch (e) { /* ignore */ } },
  };
  const OK = (v, list) => list.includes(v);

  const act = (name, fn) => { ACT[name] = function () { const r = fn.apply(null, arguments); SL.bus.emit('action', { name, args: Array.prototype.slice.call(arguments) }); return r; }; };
  const ses = sid => sid ? SL.sessions.get(sid) : SL.sessions.active;
  const toast = (t, k) => { if (SL.ui && SL.ui.toast) SL.ui.toast(t, k); };
  /** The failure of a request, shown as the server said it; a 409 is the state not allowing it now. */
  const fail = r => { if (!r || r.ok || r.code === 'aborted' || r.code === 'confirm_required') return; toast(r.message, r.status === 409 ? 'warm' : 'err'); };
  /** Send a request; failures toast. onOk(data) runs on success. Returns {ok: true, done}. */
  const send = (p, onOk, onFail) => ({ ok: true, done: p.then(r => { if (r.ok) { if (onOk) onOk(r.data, r); } else if (!onFail || onFail(r) !== true) fail(r); return r; }) });
  const tab = S => api().tab(S.id);
  /** A session that takes actions: a live tab (not the empty placeholder, not a recorded session opened read-only). */
  const why = S => !S ? 'no session' : S.placeholder ? 'start a session first: + New' : S.recorded ? 'a recorded session is read-only: resume it to act on it' : '';
  const refuse = S => ({ ok: false, why: why(S) });

  /* ---------------- settings actions ---------------- */
  act('setHover', v => { if (OK(v, ['both', 'chat', 'off'])) { settings.hover = v; settings.ver++; SL.time.setMode(v); settings.save(); } });
  act('setMotion', v => { if (OK(v, ['auto', 'reduce', 'full'])) { settings.motion = v; settings.ver++; SL.bus.emit('motion'); settings.save(); } });
  act('setCache', v => { if (OK(v, ['quiet', 'full'])) { settings.cache = v; settings.ver++; document.body.classList.toggle('cq', v === 'quiet'); settings.save(); SL.bus.emit('cache-detail', v); } });
  act('setDensity', v => { if (OK(v, ['comfortable', 'compact'])) { settings.density = v; settings.ver++; document.body.classList.toggle('compact', v === 'compact'); settings.save(); } });
  act('setTitleBadge', v => { if (OK(v, ['auto', 'on', 'off'])) { settings.title = v; settings.ver++; settings.save(); SL.bus.emit('title-badge'); } });

  /* ---------------- per-session actions ---------------- */
  const CYCLE = ['default', 'accept-edits', 'plan'];
  act('setMode', (mode, opt, sid) => {
    const S = ses(sid), m = D.modes.find(x => x.id === mode); if (!S || !m) return { ok: false, why: 'unknown mode ' + mode }; if (why(S)) return refuse(S);
    if (m.danger && !(opt && opt.confirm === mode)) return { ok: false, why: mode + ' is set only by typing its name to confirm' };
    return send(api().post(tab(S) + '/mode', { mode }, m.danger ? { confirm: 'mode:' + mode + ':' + S.id } : undefined));
  });
  /** shift+tab: default → accept-edits → plan → default. Never bypass, never yolo; from either of those it returns to default. */
  act('cycleMode', sid => { const S = ses(sid); if (!S) return; const i = CYCLE.indexOf(S.meta.mode), nx = CYCLE[(i + 1) % CYCLE.length]; return ACT.setMode(i < 0 ? 'default' : nx, null, sid); });
  act('setModel', (ref, sid) => { const S = ses(sid); if (!S) return { ok: false }; if (why(S)) return refuse(S); return send(api().post(tab(S) + '/model', { ref })); });
  act('setRoleModel', (role, ref, sid) => { const S = ses(sid); if (!S) return { ok: false }; if (role === 'manager') return ACT.setModel(ref, sid); if (why(S)) return refuse(S); return send(api().post(tab(S) + '/model', { ref, role })); });
  act('setEffort', (lv, sid) => { const S = ses(sid); if (!S) return { ok: false }; if (why(S)) return refuse(S); return send(api().post(tab(S) + '/effort', { level: lv })); });
  act('setBudget', (usd, sid) => {
    const S = ses(sid); if (!S) return { ok: false }; const v = usd === 'off' || usd === null || usd === 0 ? 0 : parseFloat(usd); if (isNaN(v) || v < 0) return { ok: false, why: 'a budget is a number of dollars, or off' };
    if (why(S)) return refuse(S);
    return send(api().post(tab(S) + '/budget', v ? { usd: v } : { off: true }));
  });
  const launch = (S, patch) => why(S) ? refuse(S) : send(api().patch(tab(S) + '/launch', patch));
  act('setIsolation', (v, sid) => { const S = ses(sid); if (S && OK(v, ['none', 'worktree'])) return launch(S, { isolation: v }); });
  act('setVerify', (v, sid) => { const S = ses(sid); if (S) return launch(S, { verify: String(v == null ? '' : v) }); });
  act('setFlag', (flag, on, sid) => { const S = ses(sid); if (S && OK(flag, ['commit', 'mailman', 'noMcp', 'trustProject'])) return launch(S, { [flag]: !!on }); });
  /* rules: effect allow | deny | ask; the origin is shown on the Permissions page */
  act('allowRule', (rule, origin, sid) => addRule(ses(sid), 'allow', rule, origin || 'this session'));
  act('denyRule', (rule, origin, sid) => addRule(ses(sid), 'deny', rule, origin || 'this session'));
  act('askRule', (rule, origin, sid) => addRule(ses(sid), 'ask', rule, origin || 'this session'));
  function addRule(S, effect, rule, origin) {
    if (!S) return { ok: false }; rule = String(rule || '').trim(); if (!rule) return { ok: false, why: 'a rule needs text, e.g. tests or Bash(go test:*)' };
    if (why(S)) return refuse(S);
    const r = send(api().post(tab(S) + '/rules', { effect, rule, origin })); r.added = rule === 'tests' ? (D.testsPreset.length || 1) : 1; return r;
  }
  act('removeRule', (rule, sid) => { const S = ses(sid); if (!S) return { ok: false }; if (why(S)) return refuse(S); return send(api().post(tab(S) + '/rules/remove', { rule })); });

  /* goals */
  /** A goal action; the server says when it waits to run (queued: set and resume as the tab's next item, pause and clear while a turn
   *  stops), and the page says so. */
  const goal = (S, body) => send(api().post(tab(S) + '/goal', body), d => { if (d && d.queued) toast(body.action === 'set' || body.action === 'resume' ? 'the goal runs as the next item of ' + S.name : 'the goal is ' + (body.action === 'pause' ? 'paused' : 'cleared') + ' once the running turn stops', 'quiet'); });
  act('setGoal', (text, sid) => {
    const S = ses(sid); if (!S) return { ok: false }; text = String(text || '').trim(); if (!text) return { ok: false, why: '/goal needs text' };
    if (why(S)) return refuse(S); return goal(S, { action: 'set', text });
  });
  act('pauseGoal', sid => { const S = ses(sid); if (!S || why(S)) return S ? refuse(S) : { ok: false }; if (S.wm.goal.state !== 'active') return { ok: false, why: 'no active goal to pause' }; return goal(S, { action: 'pause' }); });
  act('resumeGoal', sid => { const S = ses(sid); if (!S || why(S)) return S ? refuse(S) : { ok: false }; if (S.wm.goal.state !== 'paused') return { ok: false, why: 'the goal is not paused' }; return goal(S, { action: 'resume' }); });
  act('clearGoal', sid => { const S = ses(sid); if (!S || why(S)) return S ? refuse(S) : { ok: false }; return goal(S, { action: 'clear' }); });

  /* the question: the quiet-period rule lives in 85-ui-approvals.js; the server enforces its own floor (409 too_soon) */
  act('answerQuestion', (qid, choice, note, sid) => {
    const S = ses(sid) || SL.sessions.active; if (!S) return { ok: false };
    const q = S.wm.qs.find(x => x.id === qid && !x.answered); if (!q) return { ok: false, why: 'that question is not open' };
    if (S.recorded) return refuse(S);
    const body = { choice }; if (choice === 3 && note) body.note = String(note).slice(0, 2000);
    return send(api().post('/api/questions/' + api().seg(qid) + '/answer', body), null, r => {
      if (r.code === 'answered') return true;                 // another page answered it first: the answer event closes it here too
      if (r.code === 'no_question') return true;              // it was closed meanwhile (canceled, timed out, the session closed): its answer event says how
      if (r.code === 'too_soon') { if (SL.ui && SL.ui.approvals) SL.ui.approvals.shown[qid] = SL.time.T.wall; toast('the buttons wake up when the keyboard has been quiet for a moment', 'warm'); return true; }
      return false;
    });
  });
  /** Interrupt the running turn (Esc): the manager's turn stops, the workers with it, and the goal is paused. The person talks to the manager only, so no single worker can be interrupted. */
  act('interrupt', (target, sid) => {
    const S = ses(sid); if (!S) return { ok: false };
    if (target !== 'turn' && target !== 'mgr') return { ok: false, why: 'you talk to the manager only: interrupt the turn, and the manager stops the team' };
    if (why(S)) return refuse(S);
    if (!SL.calc.turnRunning(S.wm) && !S.meta.running) return { ok: false, why: 'no turn is running' };
    return send(api().post(tab(S) + '/interrupt', { target: 'turn' }));
  });
  /** Steer (`/steer TEXT`): guidance for the running turn of the MANAGER, without stopping it. The manager's answer is its next message. */
  act('steer', (agent, text, sid) => {
    const S = ses(sid); if (!S) return { ok: false }; text = String(text || '').trim(); if (!text) return { ok: false, why: 'write what to tell the manager' };
    if (agent !== 'mgr') return { ok: false, why: 'you talk to the manager only: tell it, and it tells the worker' };
    if (why(S)) return refuse(S);
    return send(api().post(tab(S) + '/steer', { text }));
  });
  act('compact', (focus, sid) => { const S = ses(sid); if (!S) return { ok: false }; if (why(S)) return refuse(S); return send(api().post(tab(S) + '/compact', focus ? { focus } : {})); });

  /* the workspace: reviewed marks, hunk reverts and restores are kept by the server (WsIndex); the Workspace cache refreshes after each.
     Undoing a hunk revert and undoing a restore are here; the reverts, restores and applies themselves are SL.ws.ops (scopes the server issues). */
  const WS = S => S.ws || (S.ws = { reviewed: {}, reverted: {}, restore: null });
  const wsRefresh = S => { if (SL.ws && typeof SL.ws.refresh === 'function') SL.ws.refresh(S); SL.bus.emit('ws-changed', S); };
  act('markReviewed', (path, cpid, on, sid) => {
    const S = ses(sid); if (!S) return { ok: false }; if (why(S)) return refuse(S); const w = WS(S); if (on) w.reviewed[path] = cpid; else delete w.reviewed[path]; S.touch();   /* optimistic: the mark is the person's own */
    return send(api().put(tab(S) + '/ws/reviewed', { path, cp: cpid || '', on: !!on }), () => wsRefresh(S), () => { if (on) delete w.reviewed[path]; else w.reviewed[path] = cpid; S.touch(); return false; });
  });
  /** The id of a hunk revert the server recorded (the Workspace cache's reverted list), or one kept in the session's marks. */
  function revertId(S, path, key) {
    if (SL.ws && typeof SL.ws.revertId === 'function') { const id = SL.ws.revertId(S, path, key); if (id) return id; }
    const v = WS(S).reverted[path + '#' + key]; return typeof v === 'string' ? v : null;
  }
  act('unrevertHunk', (path, key, sid, opt) => {
    const S = ses(sid); if (!S) return { ok: false }; if (why(S)) return refuse(S); const rid = (opt && opt.rid) || revertId(S, path, key); if (!rid) return { ok: false, why: 'nothing to undo' };
    return send(api().post(tab(S) + '/ws/revert/' + api().seg(rid) + '/undo'), () => { delete WS(S).reverted[path + '#' + key]; wsRefresh(S); }, r => { if (r.code === 'changed') wsRefresh(S); return false; });
  });
  /* a hunk revert, a restore (/rewind ID) and applying verified work are the Workspace's (SL.ws.ops, 96b-ws-data.js): each previews, and
     confirms the scope the server gave that preview; the two undos below need no preview */
  act('undoRewind', sid => {
    const S = ses(sid); if (!S) return { ok: false }; if (why(S)) return refuse(S); if (!WS(S).restore && !(SL.ws && SL.ws.hasRestore && SL.ws.hasRestore(S))) return { ok: false, why: 'no restore to undo' };
    return send(api().post(tab(S) + '/ws/restore/undo', undefined, { confirm: 'restore.undo:' + S.id }), () => { WS(S).restore = null; S.touch(); wsRefresh(S); });
  });

  /** Expand the composer's paste chips into the text that reaches the agent; the chips stay in what the transcript shows. */
  act('send', (text, sid, opt) => {
    const S = ses(sid); if (!S) return { ok: false }; const display = String(text || '').trim(); if (!display) return { ok: false };
    if (why(S)) return refuse(S);
    const full = opt && opt.text ? String(opt.text) : display;
    if (S.hist[S.hist.length - 1] !== display) S.hist.push(display);
    const busy = SL.calc.turnRunning(S.wm) || S.meta.running === true, local = busy ? { id: '', text: display, local: true } : null;
    if (local) { S.ui.queued.push(local); S.touch(); }
    const body = { text: full, clientId: api().cid() }; if (full !== display) body.display = display;
    return send(api().post(tab(S) + '/messages', body), res => { if (local && !(res && res.queued)) { const i = S.ui.queued.indexOf(local); if (i >= 0) { S.ui.queued.splice(i, 1); S.touch(); } } },
      () => { if (local) { const i = S.ui.queued.indexOf(local); if (i >= 0) { S.ui.queued.splice(i, 1); S.touch(); } } return false; });
  });
  /** A slash line the page has no handler for (custom commands, skills, MCP prompts, /mcp reconnect): the server runs it. */
  act('command', (line, sid) => { const S = ses(sid); if (!S) return { ok: false }; if (why(S)) return refuse(S); return send(api().post(tab(S) + '/command', { line: String(line) }), null, () => false); });

  /* sessions */
  act('switchSession', id => { const S = SL.sessions.activate(id); if (S && SL.data) SL.data.onSwitch(); return S; });
  const want = { id: null };
  /** Activate the tab the person just created once the page has it (the tab frame and the response can come in either order). */
  function activateWhenReady(id) { if (!id) return; if (SL.sessions.get(id)) { want.id = null; ACT.switchSession(id); } else want.id = id; }
  SL.bus.on('sessions-changed', () => { if (want.id && SL.sessions.get(want.id)) { const id = want.id; want.id = null; ACT.switchSession(id); } });
  const slug = s => String(s || 'session').toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '') || 'session';
  /**
   * The trust step of a request that starts a session (a new one or a resumed one). The page never decides that a project needs trust:
   * the request goes first, and the server's 409 trust_required carries the challenge (every file it would trust, what could not be read,
   * and a confirmation id). SL.ui.trustStep shows that challenge whole and resolves the id when the person says yes, or null; a yes
   * repeats the request with the id. A challenge that comes again (the files changed meanwhile) is shown again, at most three times.
   * post(confirmId) makes the request; also: what else the yes confirms, as the page knows it. Resolves the final API result
   * (declined: true when the person said no).
   */
  function trusting(post, firstId, also) {
    const go = (id, n) => post(id).then(r => {
      if (r.ok || r.code !== 'trust_required' || n >= 3 || !SL.ui || typeof SL.ui.trustStep !== 'function') return r;
      return SL.ui.trustStep(r.detail || {}, { message: r.message, also }).then(cid => cid ? go(cid, n + 1) : Object.assign(r, { declined: true }));
    });
    return go(firstId || '', 0);
  }
  /** What a new session raises besides trust, in the words of the trust step (one confirmation covers the session's settings). */
  const raisesOf = spec => [].concat(spec.mode === 'bypass' || spec.mode === 'yolo' ? ['permission mode ' + spec.mode] : [], (spec.rules || []).length ? ['allow ' + spec.rules.join(', ')] : [], spec.verify ? ['run the verify command ' + spec.verify] : []);
  /**
   * Start a session from the New session dialog. spec: NewSessionRequest fields. opt: {confirmId}. The trust step, when the server asks
   * for it, runs here (trusting). Returns {ok, name, done} at once.
   */
  act('newSession', (spec, opt) => {
    opt = opt || {}; const body = Object.assign({}, spec); delete body.launch; delete body.resumedFrom;
    if (!body.clientId) body.clientId = api().cid();
    const name = spec.name || slug((spec.cwd || '').split('/').filter(Boolean).pop());
    const p = trusting(id => api().post('/api/sessions', body, id ? { confirmId: id } : undefined), opt.confirmId, raisesOf(spec)).then(r => {
      if (r.ok) { activateWhenReady(r.data && r.data.tab && r.data.tab.id); return r; }
      if (!r.declined) fail(r); return r;
    });
    return { ok: true, name, done: p };
  });
  act('closeSession', id => {
    const S = SL.sessions.get(id); if (!S) return { ok: false, why: 'no such session' };
    if (S.recorded && SL.live.isWatch(id)) return send(api().del('/api/recorded/' + api().seg(S.sid) + '/watch'), () => SL.sessions.drop(id), r => { if (r.status === 404) { SL.sessions.drop(id); return true; } return false; });   /* the server stops following; its tab frame removes it everywhere */
    if (S.recorded) { SL.sessions.drop(id); return { ok: true, done: Promise.resolve({ ok: true, data: {} }) }; }
    if (SL.sessions.list.length <= 1) return { ok: false, why: 'the last session cannot be closed: start another first' };
    return send(api().del(tab(S)));
  });
  act('renameSession', (id, name) => { const S = SL.sessions.get(id); name = String(name || '').trim(); if (!S || !name) return { ok: false }; if (why(S)) return refuse(S); return send(api().patch(tab(S), { name })); });
  act('resumeSession', (recId, opt) => {
    const rec = recId === 'latest' ? SL.sessions.recorded.find(r => r.resumable) : SL.sessions.recorded.find(r => r.id === recId);
    if (!rec) return { ok: false, why: 'no recorded session to resume' }; if (!rec.resumable) return { ok: false, why: rec.id + ' cannot be resumed (older than the checkpoint window)' };
    const body = Object.assign({ from: recId === 'latest' ? 'latest' : rec.id }, opt && opt.name ? { name: opt.name } : {});
    /* a resume asks for trust as a new session does (the same challenge): the same trust step */
    return send(trusting(id => api().post('/api/sessions/resume', body, id ? { confirmId: id } : undefined)), d => activateWhenReady(d && d.tab && d.tab.id), r => { if (r.declined) return true; if (r.code === 'hosted' && r.detail && r.detail.tab) { ACT.switchSession(r.detail.tab.id || r.detail.tab); toast(r.message, 'warm'); return true; } return false; });
  });
  /** /swarm N, Run settings Apply, "run it again": the team starts again and the manager's conversation carries over (/new and /clear start empty). */
  act('restartTeam', (patch, sid) => {
    const S = ses(sid); if (!S) return { ok: false }; patch = Object.assign({}, patch); const force = patch.force; delete patch.force; const n = patch.swarm == null ? S.meta.swarm : Math.max(0, parseInt(patch.swarm, 10) || 0);
    if (!force && n === S.meta.swarm && !Object.keys(patch).some(k => k !== 'swarm')) return { ok: false, why: 'already manager + ' + n + ' workers' };
    if (why(S)) return refuse(S);
    return send(api().post(tab(S) + '/restart', { kind: 'swarm', swarm: n, fresh: false }));
  });
  /** /restart [flags]: start again with other flags, the conversation carried. */
  /** A restart that the server refuses because another tab hosts that session (409 hosted, detail.tab) brings that tab forward. */
  const hostedTab = r => { if (r.code === 'hosted' && r.detail && r.detail.tab) { ACT.switchSession(r.detail.tab.id || r.detail.tab); toast(r.message, 'warm'); return true; } return false; };
  act('restart', (flags, sid) => { const S = ses(sid); if (!S) return { ok: false }; if (why(S)) return refuse(S); return send(api().post(tab(S) + '/restart', { kind: 'restart', fresh: false, flags: flags || [] }), null, hostedTab); });
  act('newChat', sid => { const S = ses(sid); if (!S) return { ok: false }; if (why(S)) return refuse(S); return send(api().post(tab(S) + '/restart', { kind: 'new', fresh: true })); });
  /** The prune preview is the client's arithmetic over the recorded list (the server's rule); apply confirms the exact ids. */
  act('pruneSessions', (older, keep, apply) => {
    const age = SL.sessions.parseAge(older); if (isNaN(age)) return { error: 'bad --older-than: ' + older };
    const list = SL.sessions.pruneCandidates(age, keep), mb = list.reduce((s, r) => s + r.mb, 0), out = { list, mb, applied: false };
    if (!apply) return out;
    const ids = list.map(r => r.id).sort();
    out.done = api().d16(ids).then(d => api().post('/api/recorded/prune', { olderThan: String(older), keep: +keep || 0, apply: true }, { confirm: 'prune:' + d })).then(r => { if (!r.ok) fail(r); else if (SL.data) SL.data.load('recorded', { force: true }); return r; });
    return out;
  });
  act('favModel', ref => {
    const on = !G.favs.has(ref); if (on) G.favs.add(ref); else G.favs.delete(ref); const m = D.model(ref); if (m) m.fav = on; G.ver++; SL.bus.emit('models-changed');
    return send(api().post('/api/models/fav', { ref, on }), null, () => { if (on) G.favs.delete(ref); else G.favs.add(ref); if (m) m.fav = !on; G.ver++; SL.bus.emit('models-changed'); return false; });
  });
  act('trustDir', (dir, on) => {
    const after = () => { if (SL.data) { SL.data.load('trust', { force: true }); SL.data.load('projects', { force: true }); } };
    if (!on) return send(api().post('/api/trust', { dir, on: false }), after);
    return { ok: true, done: api().get('/api/trust/challenge?dir=' + encodeURIComponent(dir)).then(c => {
      if (!c.ok) { fail(c); return c; }
      return api().post('/api/trust', { dir, on: true }, { confirmId: c.data.confirm }).then(r => { if (r.ok) after(); else fail(r); return r; });
    }) };
  });
  /** Providers: sign out, or check again after a sign-in in a terminal (the browser never holds a provider key). */
  act('setProvider', (id, patch) => {
    const after = d => { if (d && Array.isArray(d.providers) && SL.data) { SL.data.map.providers(d); G.ver++; } else if (SL.data) SL.data.load('providers', { force: true }); };
    if (patch && patch.key === 'none') return send(api().post('/api/providers/' + api().seg(id) + '/signout'), () => after(null));
    return send(api().post('/api/providers/recheck'), after);
  });
  /** MCP servers: patch.action approve | revoke | test | reconnect (or the reference page's state patch). The result line lands in G.mcpOut. */
  act('setMcp', (name, patch) => {
    const S = SL.sessions.active; if (!S) return { ok: false }; if (why(S)) return refuse(S); patch = patch || {};
    const s = G.mcp.find(x => x.name === name), a = patch.action || (patch.state === 'running' ? 'approve' : patch.state === 'needs approval' ? 'revoke' : patch.state ? 'reconnect' : 'test');
    const path = tab(S) + '/mcp/' + api().seg(name) + '/' + a, out = res => { G.mcpOut = G.mcpOut || {}; G.mcpOut[name] = res; G.ver++; SL.bus.emit('mcp-changed'); if (SL.data) SL.data.load('mcp', { force: true }); };
    if (a !== 'approve') return send(api().post(path), out);
    /* the server names the exact scope with the view (confirmScope); else mcp.approve:<digest of {root, name, fingerprint}> */
    const raw = (s && s.raw) || {}, mv = SL.D.extra.mcp || {}, root = mv.root || (SL.D.extra.trust && SL.D.extra.trust.project && SL.D.extra.trust.project.dir) || S.meta.cwd;
    const scope = raw.confirmScope ? Promise.resolve(raw.confirmScope) : api().d16({ root, name, fingerprint: raw.fingerprint || '' }).then(d => 'mcp.approve:' + d);
    /* the confirmation covers exactly the entry the card showed: when the server's entry is another one now (.mcp.json was edited), nothing
       is confirmed; the card is read again and shows the entry as it is, for a fresh approve */
    return { ok: true, done: scope.then(sc => api().post(path, undefined, { confirm: sc, exact: true })).then(r => {
      if (r.ok) out(r.data);
      else if (r.code === 'scope_changed') { r.message = name + ' changed since its card was shown: look at it again, then approve it again'; G.mcpOut = G.mcpOut || {}; G.mcpOut[name] = { t: r.message, cls: 'warm' }; if (SL.data) SL.data.load('mcp', { force: true }); G.ver++; SL.bus.emit('mcp-changed'); fail(r); }
      else fail(r);
      return r; }) };
  });

  /** A recorded session opened read-only in its own tab, or followed while another process writes it. */
  act('openRecorded', (sid, opt) => { if (!sid) return { ok: false, why: 'no recorded session' }; return { ok: true, done: SL.live.openRecorded(String(sid), opt || {}) }; });

  SL.act = ACT;
  /** There is nothing to pump: the queue of typed-ahead lines is the server's (meta.queued). */
  SL.actions = { pump() {} };
})(SL);
