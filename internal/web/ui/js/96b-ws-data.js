/* 96b-ws-data.js: SL.ws, the data layer of the Workspace (Files / Changes / Checkpoints / Merge) on the server's workspace API
 * (internal/web/wire/ws.go). It keeps the shape the screen draws from (a base, then one change set per checkpoint, turned into what the screen
 * needs at any point in time) and fills it from the API:
 *
 *   info(S)                      the checkpoints of the session (arrival order), the scrubber positions and the tree; null while loading
 *   setAt(I, k) / pointId(I, k)  the change sets applied at scrubber position k, and the API point ("base", a checkpoint id, "live") it names
 *   rows(S, I, steps)            the tree at a point in time with ownership, leases, protected paths and `ask` markers
 *   touches / statusOf / counts  from the index (no request)
 *   fileAt / diffAt              a file's text and per-line authorship, and a diff, fetched once and cached (an empty shape until they arrive)
 *   marks(S)                     the person's marks, as the server keeps them (S.ws mirrors the index; SL.act edits it ahead of the server): reviewed
 *                                files, reverted hunks, the latest restore
 *   ops                          what the Workspace asks of the server itself: restore and hunk revert (each confirmed with the scope the server issued for exactly
 *                                what the person saw), and accept (apply verified work)
 *   rangeOf / revertId / hasRestore / refresh   the hooks SL.act (60-actions.js) calls for the reviewed marks and the undo of a revert or a restore
 *   merge                        the worktrees, the merge queue and the verify output (the Merge tab)
 *
 * A "step" is the id of a change set (a checkpoint with files); the id a person sees is the server's. Values not fetched yet return the
 * empty shape and schedule the fetch; their arrival bumps SL.G.ver so that the screen draws again through its own update hook.
 * Nothing here changes the model: it reads S.m / S.wm and the server's answers. Text from a file or a path is data: the helpers at the
 * end (fmt) make it printable (control, zero-width and bidirectional characters become visible code points) and bounded. */
(function (SL) {
  'use strict';
  const U = SL.u, esc = U.esc, ws = SL.ws = {};
  const cfg = ws.cfg = {
    debounceMs: 350, maxWaitMs: 1500,           // the index is fetched again this long after the model said a checkpoint or merge moved
    cacheChars: 24e6, cacheEntries: 96,          // the file and diff cache: characters of text kept, entries kept
    retryMs: 8000,                               // a failed file or diff is asked again after this long, when something asks
    maxLine: 2000,                               // characters of a line the page draws (the rest is counted)
    mergeTtlMs: 1500                             // the Merge tab's answers are asked again after this long when the model moved
  };
  const NOBODY = Object.freeze({ ag: '-', id: '', task: '' });
  const bump = () => { try { if (SL.G) SL.G.ver = (SL.G.ver || 0) + 1; if (SL.loop) SL.loop.dirty = true; } catch (e) { /* no page */ } };
  const toast = (m, k) => { try { if (SL.ui && SL.ui.toast) SL.ui.toast(m, k); } catch (e) { /* no page */ } };
  const tabUrl = S => SL.api.tab(S.id);
  /** A session that has no workspace to ask: the placeholder, a recorded session opened read-only, a watch tab (they have no live tab on the server). */
  const noTab = S => !S || S.id == null || S.id === '' || !!(S.placeholder || S.recorded || S.follow || S.readOnly);
  /** Why such a session has no workspace, as a sentence. */
  const noTabWhy = S => (S && S.placeholder ? 'no session is open: start one with + New' : S && S.follow ? 'a session that another process writes keeps no workspace here' : 'a recorded session keeps no workspace here: resume it to see its files');
  const sidOf = S => String(S.sid || (S.meta && S.meta.sid) || '');
  const num = x => (typeof x === 'number' && isFinite(x) ? x : 0);
  const arr = x => (Array.isArray(x) ? x : []);
  const str = x => (typeof x === 'string' ? x : x == null ? '' : String(x));
  /** A map keyed by strings from the server (paths, ids): no prototype, so a directory called constructor or __proto__ is a key like any other. */
  const dict = () => Object.create(null);
  /** A copy of a plain object's own keys into such a map. */
  const dictFrom = o => { const d = dict(); if (o && typeof o === 'object') Object.keys(o).forEach(k => { d[k] = o[k]; }); return d; };
  /** The own value of a key of an object that may have a prototype (the model's tables), else undefined. */
  const own = (o, k) => (o && Object.prototype.hasOwnProperty.call(o, k) ? o[k] : undefined);

  /* ================================================================ text: printable, bounded, windowed ================================== */

  /** Characters a person cannot see, or that change how text reads: C0 and C1 controls (not tab or newline), soft hyphen, Arabic letter mark,
   *  zero-width and bidirectional controls, line and paragraph separators, invisible operators, the byte-order mark and the tag characters. */
  const ODD = /[\u{0}-\u{8}\u{b}-\u{1f}\u{7f}-\u{9f}\u{ad}\u{61c}\u{180e}\u{200b}-\u{200f}\u{2028}-\u{202e}\u{2060}-\u{206f}\u{feff}\u{fff9}-\u{fffb}\u{e0000}-\u{e007f}]/gu;
  const mark = c => '⟨U+' + c.codePointAt(0).toString(16).toUpperCase().padStart(4, '0') + '⟩';
  /** Plain text made printable: each odd character becomes its code point in angle brackets. */
  const vis = t => str(t).replace(ODD, mark);
  /** Escaped HTML in which each odd character is its code point, dimmed (the comment colour of the diff). */
  const escv = t => esc(t).replace(ODD, c => '<em class="c">' + mark(c) + '</em>');
  /** The first `n` characters of a line, not cutting a surrogate pair; `more` counts what was left out. */
  function clip(t, n) {
    t = str(t); n = n || cfg.maxLine; if (t.length <= n) return { t, more: 0 };
    let e = n; const c = t.charCodeAt(e - 1); if (c >= 0xd800 && c <= 0xdbff) e--;
    return { t: t.slice(0, e), more: t.length - e };
  }
  /** Lines of a text: a trailing newline ends the last line instead of starting another; a CR before a newline is not part of the line. */
  const lines = t => (t == null || t === '' ? [] : str(t).replace(/\n$/, '').split('\n').map(l => (l.charCodeAt(l.length - 1) === 13 ? l.slice(0, -1) : l)));
  const KW = /^(func|return|type|package|import|const|var|for|range|if|else|struct|interface|map|chan|go|defer|switch|case|default|break|continue|nil|true|false|let|function|async|await|new|null)$/;
  /** A line the page draws whole up to `n` characters; a longer one keeps its start and its last `tail` characters and says how many it left out
   *  between them (never cutting a surrogate pair). */
  function elide(t, n, tail) {
    t = str(t); n = n || cfg.maxLine; tail = tail == null ? 200 : tail; if (t.length <= n) return { head: t, tail: '', hidden: 0 };
    let he = n - tail; const hc = t.charCodeAt(he - 1); if (hc >= 0xd800 && hc <= 0xdbff) he--;
    let ts = t.length - tail; const tc = t.charCodeAt(ts); if (tc >= 0xdc00 && tc <= 0xdfff) ts++;
    return { head: t.slice(0, he), tail: t.slice(ts), hidden: ts - he };
  }
  /** What stands where the characters of an overlong line were left out. */
  const more = n => '\u2026 ' + n.toLocaleString('en-US') + ' more characters \u2026';
  /** A little syntax colour for Go, JS, CSS, HTML and JSON: comments dim, strings green, keywords violet. Input is data: every piece is escaped, an
   *  overlong line (over maxLine) keeps its first and last characters and says how many are between them, and a line over 400 is not coloured. */
  function hl(t) {
    const k = elide(t); let out;
    if (k.hidden) return escv(k.head) + '<em class="c">' + more(k.hidden) + '</em>' + escv(k.tail);
    t = k.head;
    if (t.length > 400) out = escv(t);
    else {
      const re = /(\/\/.*$)|("(?:[^"\\]|\\.)*"|`[^`]*`|'(?:[^'\\]|\\.)*')|([A-Za-z_][A-Za-z0-9_]*)/g; let i = 0, m; out = '';
      while ((m = re.exec(t))) { out += escv(t.slice(i, m.index)); i = m.index + m[0].length; if (m[1]) out += '<em class="c">' + escv(m[1]) + '</em>'; else if (m[2]) out += '<em class="s">' + escv(m[2]) + '</em>'; else out += KW.test(m[3]) ? '<em class="k">' + m[3] + '</em>' : m[3]; }
      out += escv(t.slice(i));
    }
    return out;
  }
  /** Row offsets: o[i] is the top of row i, o[n] the total height; hOf(i) the height of row i. */
  function offsets(n, hOf) { const o = new Float64Array(n + 1); for (let i = 0; i < n; i++) o[i + 1] = o[i] + hOf(i); return o; }
  /** The row whose box holds y (clamped to the first and last row). */
  function rowAt(o, y) { let lo = 0, hi = o.length - 2; if (hi < 0) return 0; while (lo < hi) { const mid = (lo + hi + 1) >> 1; if (o[mid] <= y) lo = mid; else hi = mid - 1; } return lo; }
  /** The rows to draw for a scroller at `top` showing `view` pixels: [a, b) with `over` rows more on each side. */
  function windowOf(o, top, view, over) {
    const n = o.length - 1; if (n <= 0) return { a: 0, b: 0 };
    const a = Math.max(0, rowAt(o, Math.max(0, top)) - over), b = Math.min(n, rowAt(o, Math.max(0, top) + Math.max(1, view)) + 1 + over); return { a, b };
  }
  /** The rows of a diff, flat (so that 50,000 of them cost typed arrays, not elements): kind 0 hunk head, 1 context, 2 added, 3 deleted,
   *  4 elided run, 5 a reverted hunk's head; hunk[i] the hunk, line[i] the line in it, oldNo/newNo the numbers drawn, head[i] the row of
   *  the head that covers row i. `reverted` lists {key, rid} of hunks reverted by the person that the diff no longer holds. */
  function flatDiff(d, reverted) {
    const hunks = arr(d && d.hunks), rv = arr(reverted).slice().sort((a, b) => num(a.newStart) - num(b.newStart));
    let n = rv.length; hunks.forEach(h => { n += 1 + arr(h.lines).length; });
    const kind = new Uint8Array(n), hunk = new Int32Array(n), line = new Int32Array(n), oldNo = new Int32Array(n), newNo = new Int32Array(n), head = new Int32Array(n);
    let i = 0, r = 0;
    const putRev = () => { kind[i] = 5; hunk[i] = r; line[i] = -1; head[i] = i; i++; r++; };
    hunks.forEach((h, hi) => {
      while (r < rv.length && num(rv[r].newStart) <= num(h.newStart)) putRev();
      const top = i; kind[i] = 0; hunk[i] = hi; line[i] = -1; head[i] = top; i++;
      let on = num(h.oldStart), nn = num(h.newStart);
      arr(h.lines).forEach((l, li) => {
        hunk[i] = hi; line[i] = li; head[i] = top;
        if (l.t === '…') kind[i] = 4;
        else if (l.t === '-') { kind[i] = 3; oldNo[i] = on++; }
        else if (l.t === '+') { kind[i] = 2; newNo[i] = nn++; }
        else { kind[i] = 1; newNo[i] = nn++; oldNo[i] = on++; }
        i++;
      });
    });
    while (r < rv.length) putRev();
    return { n, kind, hunk, line, oldNo, newNo, head, hunks, reverted: rv };
  }
  /** A reverted hunk's key is "oldStart:newStart"; this gives the numbers back. */
  function keyParts(key) { const m = /^(\d+):(\d+)$/.exec(str(key)); return m ? { oldStart: +m[1], newStart: +m[2] } : { oldStart: 0, newStart: 0 }; }
  ws.fmt = { ODD, vis, escv, clip, elide, more, lines, hl, dict, dictFrom, own, offsets, rowAt, windowOf, flatDiff, keyParts, NOBODY };

  /* ================================================================ the per-session cache ================================================ */

  const STATUS = { added: 'A', add: 'A', a: 'A', new: 'A', created: 'A', modified: 'M', modify: 'M', m: 'M', changed: 'M', renamed: 'M', deleted: 'D', removed: 'D', d: 'D', unchanged: '-', '-': '-', '': '-' };
  /** A status word of the server as the screen's letter: A, M, D or - (nothing against the base). */
  const stat = s => own(STATUS, str(s).toLowerCase()) || '-';

  /** The data of one session: the index, what was asked for, and the person's marks that the server has not confirmed yet. */
  function dataOf(S) {
    let D = S._wsd;
    if (D && D.sid !== sidOf(S)) { dropTimers(D); D = null; }
    if (!D) {
      D = S._wsd = { tab: S.id, sid: sidOf(S), state: 'idle', I: null, error: null, stamp: '', mver: -1, mref: null, wantStamp: '', timer: 0, firstDirty: 0, inflight: false, again: false,
        lru: new Map(), chars: 0, merge: {}, epoch: 0, toasted: false, acceptedLanded: 0 };
      D.epoch = (dataOf.n = (dataOf.n || 0) + 1);
    }
    return D;
  }
  function dropTimers(D) { if (D.timer) { clearTimeout(D.timer); D.timer = 0; } D.epoch = -1; }

  /** The model's own signal that the history moved: checkpoints (count of files, skipped, safety), the diffs and merges seen. Cheap, memoised on the model. */
  function stampOf(m) {
    if (!m) return '';
    let s = ''; arr(m.ckpts).forEach(c => { s += c.id + ':' + c.files + (c.skipped ? 's' : '') + (c.safety ? 'x' : '') + ','; });
    let dn = 0, dt = 0; const df = m.diff || {}; for (const k in df) { dn++; if (df[k] && df[k].t > dt) dt = df[k].t; }
    return s + '|' + dn + ':' + dt + '|' + arr(m.merged).length;
  }

  /** The workspace index of a session in the screen's shape, or null while it loads (or could not be read: ws.status says which). It asks the
   *  server the first time and again, debounced, whenever the model's checkpoints, diffs or merges moved. */
  function info(S) {
    if (noTab(S) || !SL.api) return null;   // no live tab: nothing to ask
    const D = dataOf(S), m = S.m || S.wm;
    if (m && (D.mref !== m || D.mver !== m.ver)) { D.mref = m; D.mver = m.ver; D.wantStamp = stampOf(m); }
    if (D.state === 'idle') fetchIndex(S, D);
    else if (D.wantStamp !== D.stamp && D.state !== 'loading') schedule(S, D);
    return D.I;
  }
  /** Load state of the session's index: {state: 'loading' | 'ready' | 'error' | 'none' (a recorded session has none), message, code}. */
  function status(S) {
    if (noTab(S)) return { state: 'none', message: noTabWhy(S) };
    const D = S && S._wsd; if (!D) return { state: 'loading' }; return { state: D.I ? 'ready' : D.state === 'error' ? 'error' : 'loading', message: D.error && D.error.message, code: D.error && D.error.code }; }
  function schedule(S, D, now) {
    if (D.timer || D.inflight) { if (D.inflight) D.again = true; return; }
    const t = Date.now(); if (!D.firstDirty) D.firstDirty = t;
    const wait = now ? 0 : Math.max(0, Math.min(cfg.debounceMs, D.firstDirty + cfg.maxWaitMs - t));
    D.timer = setTimeout(() => { D.timer = 0; D.firstDirty = 0; if (D.epoch >= 0) fetchIndex(S, D); }, wait);
  }
  /** Fetch the index now (an action did something, a retry): coalesced with a fetch in flight. */
  function refresh(S) { const D = dataOf(S); if (D.timer) { clearTimeout(D.timer); D.timer = 0; } if (D.inflight) { D.again = true; return; } fetchIndex(S, D); }
  function fetchIndex(S, D) {
    if (D.inflight) { D.again = true; return; }
    D.inflight = true; if (!D.I) D.state = 'loading';
    const stamp = D.wantStamp, epoch = D.epoch;
    SL.api.get(tabUrl(S) + '/ws/index').then(r => {
      D.inflight = false; if (D.epoch !== epoch) return;
      if (r.ok && r.data) { D.error = null; D.toasted = false; D.state = 'ready'; D.stamp = stamp; apply(S, D, r.data); }
      else {
        D.error = { message: r.message || 'the workspace could not be read', code: r.code, status: r.status }; D.state = D.I ? 'ready' : 'error'; D.stamp = stamp;
        if (r.code !== 'network' && !D.toasted) { D.toasted = true; toast(D.error.message, 'err'); }
      }
      bump();
      if (D.again) { D.again = false; schedule(S, D); }
    });
  }
  /** Take an index from the server: a new version replaces the screen's shape, marks the cached live answers stale (kept until their fresh ones arrive)
   *  and makes S.ws (the marks SL.act edits ahead of the server) the server's again. */
  function apply(S, D, data) {
    if (D.I && D.I.version === data.version) return;
    const I = build(S, D, data);
    D.lru.forEach(e => { if (e.live) e.stale = true; });
    D.I = I; const w = wsMarks(S);
    w.reviewed = dictFrom(I.reviewed); w.reverted = dict(); I.reverted.forEach(r => { w.reverted[r.path + '#' + r.key] = r.id; }); w.restore = I.restore ? Object.assign({}, I.restore) : null;
  }
  /** The session's marks object: {reviewed: {path: cp}, reverted: {"path#key": id}, restore}. */
  const wsMarks = S => S.ws || (S.ws = { reviewed: {}, reverted: {}, restore: null });

  /* ================================================================ the index in the screen's shape ====================================== */

  function normCp(c) {
    const changes = arr(c.changes).map(x => ({ path: str(x.path), status: stat(x.status), added: num(x.added), removed: num(x.removed), agents: arr(x.agents).map(str), task: str(x.task), binary: !!x.binary }));
    const files = arr(c.files).map(str);
    return { id: str(c.id), time: str(c.time), at: num(c.at), label: str(c.label), skipped: !!c.skipped, safety: !!c.safety, files, agents: arr(c.agents).map(str), tasks: arr(c.tasks).map(str),
      added: num(c.added), removed: num(c.removed), nfiles: typeof c.nfiles === 'number' ? c.nfiles : files.length, unsaved: arr(c.unsaved).map(str), changes };
  }
  function build(S, D, data) {
    const cps = arr(data.cps).map(normCp);                         // arrival order: I.cps keeps it (the list on screen shows the newest first)
    const pos = cps.filter(c => !c.skipped && !c.safety);          // the scrubber positions: checkpoints with files that were not taken by a restore
    pos.forEach(c => { c.step = c.id; });
    const posOf = dict(); pos.forEach((c, i) => { posOf[c.id] = i + 1; });
    const tl = Object.create(null);                                // path -> its touches, in arrival order, with the scrubber position of the checkpoint (0: not one)
    cps.forEach(c => c.changes.forEach(x => { (tl[x.path] || (tl[x.path] = [])).push({ pi: posOf[c.id] || 0, step: c.id, id: c.id, ag: x.agents[0] || '', agents: x.agents, task: x.task, status: x.status, added: x.added, removed: x.removed, binary: x.binary }); }));
    const tree = arr(data.tree).map(f => normFile(f)), byPath = new Map(); tree.forEach(f => byPath.set(f.path, f));
    const base = data.base || {};
    const I = { D, tab: S.id, version: str(data.version), root: str(data.root), isolation: str(data.isolation), key: S.id + '|' + sidOf(S) + '|' + str(data.version),
      cps, pos, stepOfId: dict(), posOf, tl, tree, byPath, raw: { tree }, cache: {},
      base: { id: str(base.id) || 'start', label: str(base.label) || 'before the session', time: str(base.time) },
      reviewed: dictFrom(data.reviewed), reverted: arr(data.reverted).map(r => ({ id: str(r.id), path: str(r.path), key: str(r.key), t: num(r.t) })),
      restore: data.restore ? { to: str(data.restore.to), files: arr(data.restore.files).map(str), at: num(data.restore.at), safety: str(data.restore.safety) } : null };
    pos.forEach(c => { I.stepOfId[c.id] = c.id; });
    return I;
  }
  function normFile(f) {
    const path = str(f.path), cut = path.lastIndexOf('/');
    return { path, dir: typeof f.dir === 'string' ? f.dir : cut < 0 ? '' : path.slice(0, cut), name: typeof f.name === 'string' && f.name ? f.name : path.slice(cut + 1), kind: str(f.kind) || 'text',
      status: str(f.status), owner: f.owner ? str(f.owner) : null, task: f.task ? str(f.task) : null, cp: f.cp ? str(f.cp) : null, lease: f.lease || null, protected: f.protected || null, ask: f.ask || null,
      add: num(f.add), del: num(f.del), size: num(f.size), exists: f.exists !== false, ignored: !!f.ignored };
  }

  /* ================================================================ points in time ====================================================== */

  /** The change sets applied at scrubber position k (0 = the base, n = now): the first k checkpoints with files. */
  const setAt = (I, k) => I.pos.slice(0, Math.max(0, Math.min(I.pos.length, k))).map(c => c.step);
  /** The API's name for scrubber position k: "base", the id of the checkpoint that begins there (the content when it began), or "live". */
  const pointId = (I, k) => (k <= 0 ? 'base' : k >= I.pos.length ? 'live' : I.pos[k].id);
  /** The position a list of applied change sets (a prefix of the positions) stands for. */
  const pointOf = (I, steps) => Math.max(0, Math.min(I.pos.length, steps.length));
  /** Touches of a file by the applied change sets, in order: [{step, id, ag, task, status, added, removed}]. */
  function touches(I, path, steps) {
    const tl = I.tl[path]; if (!tl) return []; const k = pointOf(I, steps);
    return tl.filter(t => t.pi >= 1 && t.pi <= k);
  }
  /** Touches of a file by the change sets after position ka up to kb (what a range such as "only c07" holds). */
  function touchesIn(I, path, ka, kb) { const tl = I.tl[path]; return tl ? tl.filter(t => t.pi > ka && t.pi <= kb) : []; }
  /** Does the file exist after the first k change sets? */
  function existsAt(I, path, k) {
    const f = I.byPath.get(path), tl = I.tl[path];
    if (!tl || !tl.length) return f ? f.exists : false;
    const first = tl[0]; let ex = first.status !== 'A';          // before the first change: there unless that change created it
    tl.forEach(t => { if (t.pi >= 1 && t.pi <= k) ex = t.status !== 'D'; });
    return ex;
  }
  /** A, M or D against the base after the first k change sets; "-" when nothing differs. At the live edge the server's own status wins. */
  function statusOf(I, path, steps) {
    const k = pointOf(I, steps), f = I.byPath.get(path);
    if (k >= I.pos.length && f && f.status !== '') return stat(f.status);
    const tl = I.tl[path]; if (!tl) return '-';
    const t = tl.filter(x => x.pi >= 1 && x.pi <= k); if (!t.length) return '-';
    const had = tl[0].status !== 'A', has = t[t.length - 1].status !== 'D';
    if (!had && has) return 'A'; if (had && !has) return 'D'; if (!had && !has) return '-'; return 'M';
  }
  /** +/- of a file between two states: the exact diff when it was fetched, else what the change sets in b and not in a add up to. */
  function counts(I, path, a, b) {
    const e = I.D.lru.get(dkey(path, pointId(I, pointOf(I, a)), pointId(I, pointOf(I, b))));
    if (e && e.c && e.state === 'ready') return { added: num(e.c.added), removed: num(e.c.removed) };
    const ka = pointOf(I, a), kb = pointOf(I, b), tl = I.tl[path]; let add = 0, del = 0;
    if (tl) tl.forEach(t => { if (t.pi > ka && t.pi <= kb) { add += t.added; del += t.removed; } });
    return { added: add, removed: del };
  }
  const dirOf = p => (p.indexOf('/') < 0 ? '' : p.slice(0, p.lastIndexOf('/')));

  /** The tree at a point in time: one row per file that exists (or was deleted by then), with ownership (last writer), lease (until its task
   *  merges), protection and ask markers. Memoised per point and per set of merged tasks. */
  function rows(S, I, steps) {
    const m = S.m || S.wm, k = pointOf(I, steps), n = I.pos.length;
    const lease = m ? arr(m.merged).length + ':' + Object.keys(m.tasks || {}).length : '';
    const ck = 'rows|' + k + '|' + lease; if (I.cache[ck]) return I.cache[ck];
    const out = [];
    I.tree.forEach(f => {
      const st = statusOf(I, f.path, steps), ex = existsAt(I, f.path, k); if (!ex && st === '-') return;
      const tch = touches(I, f.path, steps), last = tch[tch.length - 1], live = k >= n;
      const T = f.lease && m && m.tasks ? own(m.tasks, f.lease.task) : null, leased = !!(f.lease && !(T && T.st === 'merged'));
      let add = 0, del = 0; if (st !== '-') { if (live && (f.add || f.del)) { add = f.add; del = f.del; } else { tch.forEach(t => { add += t.added; del += t.removed; }); } }
      out.push({ path: f.path, dir: f.dir == null ? dirOf(f.path) : f.dir, name: f.name, status: st, owner: last ? last.ag || null : live ? f.owner : null, task: last ? last.task || null : live ? f.task : null, cp: last ? last.id : live ? f.cp : null,
        lease: leased ? f.lease : null, protected: f.protected, ask: f.ask, size: f.size, add, del, kind: f.kind, ignored: f.ignored });
    });
    return (I.cache[ck] = out);
  }
  /** Files touched in change set `idx` (a position) and later: what /rewind would put back. */
  function filesFrom(I, idx) { const seen = [], has = new Set(); I.pos.slice(idx).forEach(c => c.files.forEach(f => { if (!has.has(f)) { has.add(f); seen.push(f); } })); return seen; }
  /** The id of the last checkpoint that changed a file (the one a reviewed mark is made at). */
  function lastId(I, path) { const tl = I.tl[path]; return tl && tl.length ? tl[tl.length - 1].id : null; }

  /* ================================================================ files and diffs (fetched once, cached) ============================== */

  const fkey = (path, at) => 'f\u0000' + path + '\u0000' + at;
  const dkey = (path, from, to) => 'd\u0000' + path + '\u0000' + from + '\u0000' + to;
  const sizeOf = c => (c ? str(c.text).length + 64 * arr(c.blame).length : 0);
  const diffSize = d => { let s = 0; if (d) arr(d.hunks).forEach(h => { s += 48; arr(h.lines).forEach(l => { s += 8 + str(l.s).length; }); }); return s; };
  /** Keep the cache within its budget: the oldest used entries go first, never the entry just asked for. */
  function trim(D, keep) {
    for (const [k, e] of D.lru) {
      if (D.chars <= cfg.cacheChars && D.lru.size <= cfg.cacheEntries) break;
      if (e === keep || e.state === 'loading') continue; D.lru.delete(k); D.chars -= e.size || 0;
    }
  }
  /** A cache entry: {state: 'loading' | 'ready' | 'error', c (the answer), code, message, stale}. An answer for "live" is kept when the index moves (stale)
   *  and asked for again in the background, so the screen never goes blank under a person reading it. */
  function entry(I, key, url, size, live) {
    const D = I.D; let e = D.lru.get(key);
    if (e) {
      D.lru.delete(key); D.lru.set(key, e);
      if (e.state === 'error' && Date.now() - e.at > cfg.retryMs) { D.lru.delete(key); e = null; }
      else { if (e.stale && !e.busy) load(D, e, url, size); return e; }
    }
    e = { key, state: 'loading', c: null, at: Date.now(), live: !!live, stale: false, busy: false, size: 0 }; D.lru.set(key, e); load(D, e, url, size); return e;
  }
  function load(D, e, url, size) {
    e.busy = true; const epoch = D.epoch;
    SL.api.get(url).then(r => {
      e.busy = false; if (D.epoch !== epoch || D.lru.get(e.key) !== e) return;
      D.chars -= e.size || 0; e.at = Date.now();
      if (r.ok && r.data) { e.state = 'ready'; e.c = r.data; e.stale = false; e.size = size(r.data); delete e.bl; delete e.ls; } else if (e.state === 'ready' && e.c) { e.stale = false; e.size = size(e.c); } else { e.state = 'error'; e.code = r.code; e.message = r.message; e.status = r.status; e.size = 0; }
      D.chars += e.size; trim(D, e); bump();
    });
  }
  /** A file's text and authorship at a point ("base", a checkpoint id, "live"): the cache entry (see entry). */
  function fileEntry(I, path, at) { return entry(I, fkey(path, at), SL.api.tab(I.tab) + '/ws/file?path=' + encodeURIComponent(path) + '&at=' + encodeURIComponent(at), sizeOf, at === 'live'); }
  /** The diff of a file between two points: the cache entry. */
  function diffEntry(I, path, from, to) { return entry(I, dkey(path, from, to), SL.api.tab(I.tab) + '/ws/diff?path=' + encodeURIComponent(path) + '&from=' + encodeURIComponent(from) + '&to=' + encodeURIComponent(to), diffSize, to === 'live' || from === 'live'); }
  /** The entry of a file after the change sets `steps`. */
  const fileAt = (I, path, steps) => fileEntry(I, path, pointId(I, pointOf(I, steps)));
  /** The entry of the diff of a file between the states after `a` and after `b`. */
  const diffAt = (I, path, a, b) => diffEntry(I, path, pointId(I, pointOf(I, a)), pointId(I, pointOf(I, b)));
  /** A file's text after the change sets `steps`; null while it loads, when it does not exist there, or when it has no text. */
  function textAt(I, path, steps) { const e = fileAt(I, path, steps); return e.state === 'ready' && e.c.exists && !e.c.binary ? str(e.c.text) : null; }
  /** Who wrote each line of a file's content: [{ag, id, task}] (the runs of the server expanded; '-' = not known or there before the session). */
  function blameOf(e) {
    if (!e || e.state !== 'ready') return [];
    if (e.bl) return e.bl; const c = e.c, n = c.exists && !c.binary ? lines(c.text).length : 0, out = new Array(n).fill(NOBODY);
    arr(c.blame).forEach(r => { const o = { ag: str(r.ag) || '-', id: str(r.id), task: str(r.task) }, a = Math.max(1, num(r.line)), b = Math.min(n, a + num(r.count) - 1); for (let i = a; i <= b; i++) out[i - 1] = o; });
    return (e.bl = out);
  }
  /** blame of the file after the change sets `steps` */
  const blame = (I, path, steps) => blameOf(fileAt(I, path, steps));
  /** The lines of an entry's text, split once. */
  function linesOf(e) { if (!e || e.state !== 'ready' || !e.c.exists || e.c.binary) return []; return e.ls || (e.ls = lines(e.c.text)); }
  /** The diff between two states in the hunk format of the screen ({added, removed, hunks}); an empty one until it arrives. */
  function hunks(I, path, a, b) { const e = diffAt(I, path, a, b); return e.state === 'ready' ? e.c : { added: 0, removed: 0, hunks: [] }; }
  /** Forget a file's cached answers (an action changed it). */
  function forget(I, path) { const D = I.D; for (const [k, e] of D.lru) { if (k.split('\u0000')[1] === path) { D.chars -= e.size || 0; D.lru.delete(k); } } }

  /* ================================================================ the file being written now ========================================== */

  /** The file being written right now: the agent in state `edit` whose `doing` names a path of the tree. */
  function liveFile(S, I) {
    const m = S.m; if (!m || !I) return null; let out = null;
    m.order.forEach(id => {
      const A = own(m.ag, id); if (!A || id === 'mgr' || A.state !== 'edit' || !A.doing) return;
      String(A.doing).split(/[\s`'"]+/).forEach(w => { if (w && I.byPath.has(w)) out = { path: w, ag: id, task: A.task, since: A.stateT }; });
    });
    return out;
  }
  /** Is the edit of `path` still going on (no diff event after the agent began it)? */
  function editing(m, live, path) { if (!live || live.path !== path) return false; const d = m.diff && m.diff[path]; return !(d && d.done && (live.since == null || d.t >= live.since)); }
  /** The not-yet-recorded content of the files workers are writing: typed over each stream's own time from the content the write call carries.
   *  [{path, text, done, ag, task}] */
  function pendingAll(S, I, vt) {
    const m = S.m, out = []; if (!m || !m.streams) return out;
    Object.keys(m.streams).forEach(id => {
      const st = m.streams[id]; if (!st || !st.code || !st.file || !st.rate) return;
      const full = str(st.text), dur = full.length / st.rate, n = Math.max(0, Math.floor(full.length * Math.min(1, (vt - st.t0) / dur)));
      if (vt - st.t0 > dur + 1.5) return; const A = own(m.ag, id);
      out.push({ path: st.file, text: full.slice(0, n), done: n >= full.length, ag: id, task: A ? A.task : null });
    });
    return out;
  }
  /** The content being written to one path, or null. */
  const pending = (S, I, path, vt) => pendingAll(S, I, vt).find(p => p.path === path) || null;

  /* ================================================================ the person's marks ================================================== */

  /** The person's marks: {reviewed: {path: cp}, reverted: {"path#key": id}, revs: {path: [{id, key}]}, restore: {to, files, at, safety} | null, stamp}. They are S.ws,
   *  which follows the server's index and which SL.act edits ahead of the server's answer. */
  function marks(S) {
    const w = wsMarks(S), revs = dict(); let nr = 0, nv = 0;
    for (const k in w.reviewed) nr++;
    for (const k in w.reverted) { nv++; if (typeof w.reverted[k] === 'string') { const i = k.lastIndexOf('#'); (revs[k.slice(0, i)] || (revs[k.slice(0, i)] = [])).push({ id: w.reverted[k], key: k.slice(i + 1) }); } }
    return { reviewed: w.reviewed, reverted: w.reverted, revs, restore: w.restore || null, stamp: nr + '/' + nv + '/' + (w.restore ? w.restore.to + w.restore.files.length + w.restore.safety : '') };
  }
  /** The id the server gave a hunk revert of `path` at `key` ("oldStart:newStart"), or null. */
  function revertId(S, path, key) { const v = wsMarks(S).reverted[path + '#' + key]; if (typeof v === 'string') return v; const I = S._wsd && S._wsd.I, r = I && I.reverted.find(x => x.path === path && x.key === key); return r ? r.id : null; }
  /** Is there a restore that can be undone? */
  const hasRestore = S => !!wsMarks(S).restore;

  /* ================================================================ actions ============================================================= */

  /** What the Workspace asks of the server itself (the reviewed, revert and restore actions are SL.act's): each takes the session and resolves {ok, ...}. */
  const ops = ws.ops = {
    /** The dry run of /rewind ID: what each file would do, and the scope that confirms exactly that plan. Resolves {ok, plan} or {ok:false, code, message}
     *  ("nothing": c04 has nothing to put back). */
    async previewRestore(S, id) {
      if (noTab(S)) return { ok: false, code: 'no_session', message: noTabWhy(S) };
      const r = await SL.api.post(tabUrl(S) + '/ws/restore', { id, dryRun: true });
      return r.ok ? { ok: true, plan: normPlan(r.data) } : { ok: false, code: r.code, message: r.message };
    },
    /** Put the files back to before checkpoint `id`, as the plan the person saw says: the server's scope of that plan is confirmed and sent back, and when the
     *  plan changed since (409 changed) nothing was written and the new plan comes back to be looked at and confirmed again. Resolves {ok, plan}, or
     *  {ok: false, code, message, plan?} (plan: the new one after `changed`). A server that sends no scope is asked with the scope `restore:<session>:<id>`. */
    async restore(S, id, plan) {
      if (noTab(S)) return { ok: false, code: 'no_session', message: noTabWhy(S) };
      const scope = plan && plan.scope, body = { id, dryRun: false }; if (scope) body.scope = scope;
      const r = await SL.api.post(tabUrl(S) + '/ws/restore', body, { confirm: scope || 'restore:' + S.id + ':' + id });
      if (r.ok) { const done = normPlan(r.data); done.files.forEach(f => { if (dataOf(S).I) forget(dataOf(S).I, f.path); }); refresh(S); changed(S); return { ok: true, plan: done }; }
      if (r.code === 'changed' && r.detail) return { ok: false, code: 'changed', message: r.message, plan: normPlan(r.detail) };
      toast(r.message, r.status === 409 ? 'warm' : 'err'); return { ok: false, code: r.code, message: r.message };
    },
    /** Revert one hunk (a hunk of the diff of `path` between `from` and `to`, as the diff returned it, with its own scope). 409 changed: nothing was written,
     *  the diff as it is now was put in the cache (its hunks carry their new scopes) and `diff` says so. Resolves {ok} or {ok: false, code, message, diff?}. */
    async revert(S, path, hunk, from, to) {
      if (noTab(S)) return { ok: false, code: 'no_session', message: noTabWhy(S) };
      const body = { path, key: hunk.oldStart + ':' + hunk.newStart, from, to }, scope = hunk.scope; if (scope) body.scope = scope;
      const r = await SL.api.post(tabUrl(S) + '/ws/revert', body, { confirm: scope || 'revert:' + S.id + ':' + await SL.api.d16({ path, key: body.key, from, to }) });
      const I = dataOf(S).I;
      if (r.ok) { if (I) forget(I, path); refresh(S); changed(S); return { ok: true, revert: r.data }; }
      if (r.code === 'changed') {
        if (r.detail && r.detail.hunks && I) putDiff(I, path, from, to, r.detail); else if (I) forget(I, path);
        refresh(S); return { ok: false, code: 'changed', message: r.message, diff: r.detail && r.detail.hunks ? r.detail : null };
      }
      toast(r.message, r.status === 409 ? 'warm' : 'err'); return { ok: false, code: r.code, message: r.message };
    },
    /** Apply the verified, merged work of an isolated team to the person's checkout now. o = {mode: 'commits' (default) | 'edits', message, dryRun, scope}:
     *  a dry run says what would happen and gives the scope that confirms exactly that (nothing is written, no confirmation); the real call confirms the
     *  scope and sends it back, and when what would be applied changed since (409 changed) nothing was written and the new dry run comes back as `result`.
     *  Resolves {ok, result} (see normAccept) or {ok: false, code, message, detail, result?}; the caller shows the sentence (it may carry a hint). */
    async accept(S, o) {
      if (noTab(S)) return { ok: false, code: 'no_session', message: noTabWhy(S) };
      o = typeof o === 'string' ? { message: o } : (o || {}); const D = dataOf(S), body = {};
      if (o.mode) body.mode = o.mode; if (o.message) body.message = str(o.message).slice(0, 200); if (o.dryRun) body.dryRun = true; if (o.scope && !o.dryRun) body.scope = o.scope;
      const r = await SL.api.post(tabUrl(S) + '/ws/accept', body, o.dryRun ? undefined : { confirm: o.scope || 'accept:' + S.id });
      if (!r.ok) return { ok: false, code: r.code, message: r.message, detail: r.detail, result: r.code === 'changed' && r.detail ? normAccept(r.detail) : undefined };
      const result = normAccept(r.data);
      if (!o.dryRun) { const q = D.merge.queue; D.acceptedLanded = q && q.data ? arr(q.data.landed).length : 0; delete D.merge.queue; delete D.merge.worktrees; refresh(S); changed(S); }
      return { ok: true, result };
    }
  };
  /** The workspace changed under an action of the person: the page's other readers (the rail's badges, the drawer) look again. */
  function changed(S) { bump(); try { SL.bus.emit('ws-changed', S); } catch (e) { /* no bus */ } }
  /** Put a diff the server sent (the new one that came with a 409 changed) in the cache in place of what was there. */
  function putDiff(I, path, from, to, d) {
    const D = I.D, key = dkey(path, from, to), old = D.lru.get(key); if (old) D.chars -= old.size || 0;
    const e = { key, state: 'ready', c: d, at: Date.now(), live: from === 'live' || to === 'live', stale: false, busy: false, size: diffSize(d) }; D.lru.set(key, e); D.chars += e.size; bump(); return e;
  }
  /** The answer of the accept route: what was applied (or, for a dry run, would be), as optional fields with their empty values. canCommit is false only when the server says so. */
  function normAccept(d) {
    d = d || {};
    return { commit: str(d.commit), files: arr(d.files).map(str), branch: str(d.branch), applied: !!d.applied, committed: !!d.committed, waiting: !!d.waiting, dryRun: !!d.dryRun,
      commitBlocked: str(d.commitBlocked), canCommit: d.canCommit !== false && !d.commitBlocked, message: str(d.message), tasks: arr(d.tasks).map(str), scope: str(d.scope) };
  }
  /** Is verified work waiting to be applied? The merge queue landed more submissions than the last apply covered. */
  function acceptWaiting(S) { const D = dataOf(S), q = D.merge.queue; if (!q || !q.data) return false; return arr(q.data.landed).length > (D.acceptedLanded || 0); }
  function normPlan(p) {
    p = p || {};
    return { id: str(p.id), label: str(p.label), time: str(p.time), applied: !!p.applied, safety: str(p.safety), summary: str(p.summary),
      files: arr(p.files).map(f => ({ path: str(f.path), action: str(f.action), outcome: str(f.outcome), added: num(f.added), removed: num(f.removed), to: str(f.to), reason: str(f.reason) })), scope: str(p.scope) };
  }

  /* ================================================================ the Merge tab: worktrees, queue, verify output ======================== */

  /** One answer of the Merge tab: {state: 'loading' | 'ready' | 'none' | 'error', data, message}. `none` is the 409 not_isolated answer
   *  (the team writes to the checkout directly). Asked again, in the background, when the model's merges moved or the answer is old. */
  function remote(S, key, path) {
    if (noTab(S)) return { state: 'none', data: null, message: noTabWhy(S), at: 0 };
    const D = dataOf(S), m = S.m || S.wm, st = m ? arr(m.merged).length + ':' + (m.qHead ? m.qHead.task + m.qHead.step : '') + ':' + m.conflicts + ':' + m.bounced : '', now = Date.now();
    let e = D.merge[key]; if (!e) e = D.merge[key] = { state: 'loading', data: null, message: '', st: null, at: 0, busy: false };
    const due = e.at === 0 || (e.state === 'error' ? now - e.at > cfg.retryMs : (e.st !== st && now - e.at > cfg.mergeTtlMs) || now - e.at > 30000);
    if (!e.busy && due) {
      e.busy = true; e.st = st; const epoch = D.epoch;
      SL.api.get(tabUrl(S) + path).then(r => {
        e.busy = false; if (D.epoch !== epoch) return; e.at = Date.now();
        if (r.ok) { e.state = 'ready'; e.data = r.data; e.message = ''; }
        else if (r.code === 'not_isolated' || r.status === 404) { e.state = 'none'; e.message = r.message; e.data = null; }
        else { e.state = e.data ? 'ready' : 'error'; e.message = r.message; }
        bump();
      });
    }
    return e;
  }
  const merge = ws.merge = {
    /** [{agent, path, branch, base, head, dirty, files}] */
    worktrees: S => remote(S, 'worktrees', '/ws/worktrees'),
    /** {branch, tip, healthy, active, phase, waiting, landed, verify} */
    queue: S => remote(S, 'queue', '/ws/queue'),
    /** The output of the last verification of a task: {task, cmd, runs: [{attempt, exit, ms, at, out, truncated, timedOut}]}. A server that sends one
     *  run (cmd, exitCode, output) is read as a list of one. */
    verify(S, task) { const e = remote(S, 'verify:' + task, '/ws/verify/' + SL.api.seg(task)); return Object.assign({}, e, { data: e.data ? normVerify(e.data) : null }); }
  };
  function normVerify(v) {
    const runs = Array.isArray(v.runs) ? v.runs.map((r, i) => ({ attempt: num(r.attempt) || i + 1, exit: num(r.exit != null ? r.exit : r.exitCode), ms: num(r.ms), at: str(r.at), out: str(r.out != null ? r.out : r.output), truncated: !!r.truncated, timedOut: !!r.timedOut }))
      : [{ attempt: 1, exit: num(v.exitCode), ms: num(v.ms), at: str(v.at), out: str(v.output), truncated: !!v.truncated, timedOut: !!v.timedOut }];
    return { task: str(v.task), cmd: str(v.cmd), runs };
  }
  merge.normVerify = normVerify;

  /* ================================================================ exports ============================================================= */

  /** The point of a scrubber position for a Workspace view state W: the index of the shown state (0..n). */
  function kOf(I, cp) { const n = I.pos.length; if (cp === 'base') return 0; if (cp) { const i = I.pos.findIndex(c => c.id === cp); if (i >= 0) return i + 1; } return n; }
  /** The files an agent owns at the live edge (the drawer's "files owned"): [{path, st, add, del}]. */
  function filesOf(S, id) { const I = info(S); if (!I) return []; return rows(S, I, setAt(I, I.pos.length)).filter(r => r.owner === id).map(r => ({ path: r.path, st: r.status, add: r.add, del: r.del })); }
  Object.assign(ws, { info, status, refresh, setAt, pointId, pointOf, kOf, touches, touchesIn, existsAt, statusOf, counts, rows, filesFrom, lastId, fileAt, diffAt, fileEntry, diffEntry, textAt, blame, blameOf, linesOf, hunks, liveFile, editing, pending, pendingAll,
    marks, revertId, hasRestore, filesOf, lines, stat, dataOf, normPlan, normAccept, acceptWaiting, stampOf, forget });
  /** hover linking: the owner of a file at the live edge (the engine asks through SL.fileOwner) */
  SL.fileOwner = f => { const S = SL.sessions && SL.sessions.active, I = S && S._wsd && S._wsd.I; if (!I) return null; const t = touches(I, f, setAt(I, I.pos.length)); if (t.length) return t[t.length - 1].ag || null; const r = I.byPath.get(f); return r ? r.owner : null; };
})(SL);
