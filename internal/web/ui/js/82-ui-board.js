/* 82-ui-board.js: SL.ui.board, the task board used by the cockpit band and by the Board view (todo / running / verify / merged).
 * Every task is drawn once, in the column of its own status, and the count in a column's head is the number of tasks of that column.
 * A subtask in its parent's column is drawn inside the parent's card (open in the columns of unfinished work, folded in merged; the
 * parent's toggle turns it); a subtask in another column is a card of that column marked ↳ with its parent, and the parent's card says
 * how many of its subtasks are in which column. Past COMPACT_AT merged tasks the merged column is a list of one-line rows (id, owner,
 * title, time of the merge), newest first; an empty column is drawn narrower.
 * Cards are created once per task and moved between columns; a move is a Web Animation owned by the caller's scope (FLIP), so it dies
 * with the view. Drag is disabled on purpose: the manager moves tasks, a person steers agents. */
(function (SL) {
  'use strict';
  const U = SL.u, { $, esc, mk, fmtMs, agCol, tod } = U, ui = SL.ui = SL.ui || {};
  const COLS = [['todo', 'todo'], ['running', 'running'], ['verify', 'verify'], ['merged', 'merged']];
  /** More merged tasks than this and the merged column is drawn as one-line rows, newest first. */
  const COMPACT_AT = 8;
  /** How a parent's card names its subtasks in another column. */
  const AWAY = { todo: n => n + ' in todo', running: n => n + ' running', verify: n => n + ' in verify', merged: n => n + ' merged' };

  /** Natural order of ids: T2 before T10 and be-2 before be-10, where a string sort puts T10 and T100 between T1 and T2. */
  function idCmp(a, b) {
    const x = String(a).match(/\d+|\D+/g) || [], y = String(b).match(/\d+|\D+/g) || [];
    for (let i = 0; i < Math.min(x.length, y.length); i++) {
      if (x[i] === y[i]) continue;
      const d = /^\d/.test(x[i]) && /^\d/.test(y[i]) ? Number(x[i]) - Number(y[i]) : 0;
      return d || (x[i] < y[i] ? -1 : 1);
    }
    return x.length - y.length;
  }
  /** Newest merge first: a merged task's time is that of its last change of status, the merge (a task merged, reopened and merged
   *  again counts from its second merge); a task of m.merged that was reopened since goes last; equal times keep the order of m.merged. */
  function byNewestMerge(m) {
    const rank = new Map(); m.merged.forEach((id, i) => rank.set(id, i));
    const when = id => { const t = m.tasks[id]; return t && t.st === 'merged' && typeof t.t === 'number' ? t.t : -Infinity; }, r = id => rank.has(id) ? rank.get(id) : -1;
    return (a, b) => (when(b) - when(a)) || (r(b) - r(a)) || idCmp(b, a);
  }
  /** The tasks of m.merged, newest merge first. */
  const newestMerged = m => m.merged.slice().sort(byNewestMerge(m));
  /** A task the manager resolved as no longer needed: done, in the merged column, but nothing of it was merged. */
  const resolved = t => !!t && t.st === 'merged' && /^unneeded/.test(t.closure || '');

  /** Create the board in `host`. big = the roomy variant (Board view). */
  function make(scope, host, big) {
    host.innerHTML = COLS.map(([k, n]) => '<div class="bcol" data-col="' + k + '"><h4>' + n + '<b class="cnt">0</b></h4><div class="cl"></div></div>').join('');
    const B = { host, big, nodes: {}, cols: {}, scope, open: new Map(), isOpen: () => false };
    COLS.forEach(([k]) => { B.cols[k] = $('[data-col="' + k + '"] .cl', host); });
    scope.listen(host, 'click', e => {
      const toggle = e.target.closest('[data-pool-toggle]');
      if (toggle) { const id = toggle.dataset.poolToggle; B.open.set(id, !B.isOpen(id)); if (B.model) update(B, B.model, B.S); return; }
      const c = e.target.closest('.tcard'); if (c && c.dataset.owner) ui.focusAgent(c.dataset.owner, e);
    });
    return B;
  }

  /** Where each task goes. top[k]: the tasks drawn directly in column k, in the column's order; kids[p]: the subtasks drawn inside
   *  p's card; away[p][k]: how many of p's subtasks are in column k when p is not. counts[k]: every task whose status is k. */
  function layout(m) {
    const T = m.tasks, colOf = id => COLS.some(c => c[0] === T[id].st) ? T[id].st : 'todo';
    const counts = { todo: 0, running: 0, verify: 0, merged: 0, resolved: 0 }, top = { todo: [], running: [], verify: [], merged: [] }, kids = Object.create(null), away = Object.create(null);
    /* a subtask nests when its parents up the chain share its column; a loop of parents is drawn flat */
    const nests = id => { const k = colOf(id), seen = new Set([id]); let p = T[id].parent; if (!p || !T[p] || colOf(p) !== k) return false; while (p && T[p] && colOf(p) === k) { if (seen.has(p)) return false; seen.add(p); p = T[p].parent; } return true; };
    m.torder.forEach(id => {
      if (!T[id]) return; const k = colOf(id), p = T[id].parent; counts[k]++; if (resolved(T[id])) counts.resolved++;
      if (nests(id)) (kids[p] || (kids[p] = [])).push(id);
      else { top[k].push(id); if (p && T[p]) { const a = away[p] || (away[p] = {}); a[k] = (a[k] || 0) + 1; } }
    });
    const newest = byNewestMerge(m);
    top.merged.sort(newest); Object.keys(kids).forEach(p => { if (colOf(p) === 'merged') kids[p].sort(newest); });
    return { colOf, counts, top, kids, away, compact: counts.merged > COMPACT_AT };
  }

  /** The line of a parent's card about its subtasks: the toggle of the ones inside it, and where the others are. */
  function subLine(id, n, open, away, row) {
    const parts = COLS.filter(([k]) => away && away[k]).map(([k]) => AWAY[k](away[k])), word = n + ' subtask' + (n === 1 ? '' : 's');
    const toggle = n ? '<button type="button" class="btn sm" data-pool-toggle="' + esc(id) + '" aria-expanded="' + open + '"' + (row ? ' title="' + word + '"' : '') + '>' + (open ? '▾ ' : '▸ ') + (row ? n : word) + '</button>' : '';
    if (row) return toggle;
    const where = parts.length ? '<span class="dim">' + (n ? '+ ' : 'subtasks: ') + parts.join(' · ') + '</span>' : '';
    return toggle || where ? '<div class="tsubl">' + toggle + where + '</div>' : '';
  }

  /** Re-render from the model; cards that changed column slide from their old place. S (the session, for the time of a merge) defaults
   *  to the one the board drew last, then the active one. Returns the counts of the columns. */
  function update(B, m, S) {
    B.model = m; if (S) B.S = S; S = S || B.S || (SL.sessions && SL.sessions.active);
    const vis = B.host.offsetParent !== null, before = {}, moved = [], T = m.tasks;
    if (vis) Object.keys(B.nodes).forEach(id => { const n = B.nodes[id]; if (n.isConnected) before[id] = n.getBoundingClientRect(); });
    Object.keys(B.nodes).forEach(id => { if (!T[id]) { B.nodes[id].remove(); delete B.nodes[id]; } });   /* a replay sought back before the task */
    const L = layout(m), { colOf, counts, top, kids, away, compact } = L;
    const isOpen = B.isOpen = id => B.open.has(id) ? B.open.get(id) : colOf(id) !== 'merged';
    const at = t => S && S.meta && S.meta.t0 != null ? tod(S.meta.t0, t) : '';
    const shown = new Set(), show = id => { shown.add(id); if (kids[id] && isOpen(id)) kids[id].forEach(show); };
    COLS.forEach(([k]) => top[k].forEach(show));
    Object.keys(B.nodes).forEach(id => { if (!shown.has(id) && B.nodes[id].parentNode) B.nodes[id].remove(); });
    shown.forEach(id => {
      const t = T[id], k = colOf(id), row = compact && k === 'merged', owner = t.owner || '', n0 = B.nodes[id], n = n0 || (B.nodes[id] = mk('div', { class: 'tcard', 'data-task': id }));
      n.setAttribute('data-ag', owner); n.dataset.owner = owner; n.style.setProperty('--c', agCol(owner || 'mgr')); n.classList.toggle('trow', row);
      const own = owner && m.ag[owner], blocked = t.st === 'running' && own && own.state === 'ask', unmet = t.st !== 'merged' ? t.deps.filter(d => T[d] && T[d].st !== 'merged') : [];
      const nk = kids[id] ? kids[id].length : 0, open = nk > 0 && isOpen(id), nested = !!(t.parent && T[t.parent] && kids[t.parent] && kids[t.parent].includes(id));
      const ownerHtml = '<span style="color:' + agCol(owner || 'mgr') + '">' + esc(owner) + '</span>';
      let html, title = id + ' · ' + t.title + (owner ? ' · ' + owner : '');
      if (row) {
        const when = at(t.t), rz = resolved(t); title += rz ? ' · resolved: no longer needed, nothing merged' : (when ? ' · merged ' + when : '') + (t.ms ? ' · verified in ' + fmtMs(t.ms) : '') + (away[id] ? ' · subtasks: ' + COLS.filter(([c]) => away[id][c]).map(([c]) => AWAY[c](away[id][c])).join(', ') : '');
        html = '<div class="tcr"><span class="tid">' + esc(id) + '</span><span class="tow">' + ownerHtml + '</span><span class="tti">' + esc(t.title) + '</span>' + (t.parent && !nested ? '<span class="dim tpar">↳ ' + esc(t.parent) + '</span>' : '') + subLine(id, nk, open, null, true) + (rz ? '<span class="dim">resolved</span>' : when ? '<time class="tat">' + when.slice(0, 5) + '</time>' : '') + '</div>' + (open ? '<div class="subtasks"></div>' : '');
      } else {
        const need = (B.big && t.deps.length && t.st !== 'merged') ? '<span class="dep">after ' + t.deps.map(esc).join(' ') + '</span>' : '';
        /* the members by name in their colours; a model is on the member's card and in the tooltip, not here, where three long ids wrapped a card to five lines */
        const pool = t.poolPhase ? '<div class="dim">' + esc(t.poolPhase) + ' · ' + (t.poolMembers || []).map(x => '<span style="color:' + agCol(x) + '" title="' + esc(x + (m.ag[x] && m.ag[x].model ? ' · ' + m.ag[x].model : '')) + '">' + esc(x) + '</span>').join(', ') + '</div>' : '';
        html = pool + subLine(id, nk, open, away[id], false) + '<div class="tcr"><span class="tid">' + esc(id) + '</span>' + (t.parent ? '<span class="dim tpar">↳ ' + esc(t.parent) + '</span>' : '') + (!B.big && t.st !== 'merged' && unmet.length ? '<span class="dep" title="waits on ' + esc(unmet.join(' ')) + '">← ' + esc(unmet.join(' ')) + '</span>' : '') + '<span class="tow">' + ownerHtml + (blocked ? '<span class="ask" title="waiting for your answer">?</span>' : '') + (resolved(t) ? '<span class="dim" title="resolved: no longer needed, nothing merged">resolved</span>' : t.st === 'merged' ? '<span class="ok">✓' + (t.ms ? ' ' + fmtMs(t.ms) : '') + '</span>' : '') + '</span></div><span class="tti">' + esc(t.title) + '</span>' + (B.big ? '<span class="tow">' + need + (t.st === 'verify' ? '<span class="warm">▸ verifying</span>' : '') + (blocked ? '<span class="warm">waiting for your answer</span>' : '') + (t.scope && t.scope !== '-' ? '<span class="dim">' + esc(t.scope) + '</span>' : '') + '</span>' : '') + (open ? '<div class="subtasks"></div>' : '');
      }
      if (n._h !== html) { n._h = html; n.innerHTML = html; }
      n.classList.toggle('blocked', !!blocked); n.title = title;
    });
    /* put each box's cards in its order: a column's own cards, then the subtasks inside each open parent */
    const place = (box, ids) => {
      ids.forEach((id, i) => { const n = B.nodes[id], here = box.children[i]; if (here === n) return; const from = n.parentNode; box.insertBefore(n, here || null); if (from !== box && before[id]) moved.push(id); });
      Array.from(box.children).slice(ids.length).forEach(x => x.remove());
    };
    COLS.forEach(([k]) => {
      const cl = B.cols[k], em = cl.querySelector('.bempty'); if (em) em.remove(); place(cl, top[k]); if (!top[k].length) cl.appendChild(mk('div', { class: 'bempty' }, 'nothing'));
      const cnt = $('[data-col="' + k + '"] .cnt', B.host); cnt.textContent = counts[k]; cnt.removeAttribute('title');
      B.host.style.setProperty('--bw-' + k, !counts[k] ? '.5fr' : k === 'merged' && compact ? '1.6fr' : '1fr');
    });
    /* a board with no task at all says what will come and what starts it, once, above its columns */
    const total = COLS.reduce((n, [k]) => n + (counts[k] || 0), 0); let none = Array.from(B.host.children).find(e => e.classList && e.classList.contains('bnone'));
    if (!total && !none) { none = mk('p', { class: 'bnone' }, B.big ? 'No tasks yet. When the manager has a goal, it splits the work into tasks; each one moves here from to do to merged.' : 'No tasks yet: they appear when the manager splits a goal into work.'); B.host.insertBefore(none, B.host.firstChild); }
    else if (total && none) none.remove();
    shown.forEach(id => { if (kids[id] && isOpen(id)) { const box = B.nodes[id].querySelector('.subtasks'); if (box) place(box, kids[id]); } });
    B.host.classList.toggle('compact-merged', compact);
    /* no slide in a paused replay: animations follow the view clock there, and a slide would stand still half-way */
    if (vis && !ui.still() && !(S && S.replay && !S.replay.playing)) moved.forEach(id => { const n = B.nodes[id], a = before[id]; if (!n || !n.isConnected) return; const b = n.getBoundingClientRect(), dx = a.left - b.left, dy = a.top - b.top; if (Math.abs(dx) + Math.abs(dy) > 2) B.scope.animate(n, [{ transform: 'translate(' + dx + 'px,' + dy + 'px)', background: 'var(--warm-b)' }, { transform: 'none', background: 'var(--panel)' }], { duration: 620, easing: 'cubic-bezier(.2,.7,.2,1)' }); });
    return counts;
  }
  ui.board = { make, update, layout, COLS, COMPACT_AT, idCmp, newestMerged, resolved };
})(SL);
