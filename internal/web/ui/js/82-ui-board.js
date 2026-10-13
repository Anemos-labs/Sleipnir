/* 82-ui-board.js: SL.ui.board, the task board used by the cockpit band and by the Board view (todo / running / verify / merged).
 * Cards are created once per task and moved between columns; a move is a Web Animation owned by the caller's scope (FLIP), so it dies
 * with the view. Drag is disabled on purpose: the manager moves tasks, a person steers agents. */
(function (SL) {
  'use strict';
  const U = SL.u, { $, esc, mk, fmtMs, agCol } = U, ui = SL.ui = SL.ui || {};
  const COLS = [['todo', 'todo'], ['running', 'running'], ['verify', 'verify'], ['merged', 'merged']];

  /** Create the board in `host`. big = the roomy variant (Board view). */
  function make(scope, host, big) {
    host.innerHTML = COLS.map(([k, n]) => '<div class="bcol" data-col="' + k + '"><h4>' + n + '<b class="cnt">0</b></h4><div class="cl"></div></div>').join('');
    const B = { host, big, nodes: {}, cols: {}, scope, expanded: new Set() };
    COLS.forEach(([k]) => { B.cols[k] = $('[data-col="' + k + '"] .cl', host); });
    scope.listen(host, 'click', e => { const toggle = e.target.closest('[data-pool-toggle]'); if (toggle) { const id = toggle.dataset.poolToggle; if (B.expanded.has(id)) B.expanded.delete(id); else B.expanded.add(id); if (B.model) update(B, B.model); return; } const c = e.target.closest('.tcard'); if (c && c.dataset.owner) ui.focusAgent(c.dataset.owner, e); });
    return B;
  }
  /** Re-render from the model; cards that changed column slide from their old place. */
  function update(B, m) {
    B.model = m;
    const vis = B.host.offsetParent !== null, before = {};
    if (vis) Object.keys(B.nodes).forEach(id => { const n = B.nodes[id]; if (n.parentNode) before[id] = n.getBoundingClientRect(); });
    const counts = { todo: 0, running: 0, verify: 0, merged: 0 }, moved = [];
    const childMap = {}; m.torder.forEach(id => { const p = m.tasks[id].parent; if (p) (childMap[p] || (childMap[p] = [])).push(id); });
    m.torder.forEach(id => {
      const t = m.tasks[id]; counts[t.st] = (counts[t.st] || 0) + 1; let n = B.nodes[id]; if (t.parent && !B.expanded.has(t.parent)) { if (n) n.remove(); return; }
      if (!n) { n = B.nodes[id] = mk('div', { class: 'tcard', 'data-task': id }); }
      n.setAttribute('data-ag', t.owner || ''); n.dataset.owner = t.owner || ''; n.style.setProperty('--c', agCol(t.owner || 'mgr'));
      const own = t.owner && m.ag[t.owner], blocked = t.st === 'running' && own && own.state === 'ask', unmet = t.st !== 'merged' ? t.deps.filter(d => m.tasks[d] && m.tasks[d].st !== 'merged') : [];
      const need = (B.big && t.deps.length && t.st !== 'merged') ? '<span class="dep">after ' + t.deps.join(' ') + '</span>' : '';
      const children = childMap[id] || [];
      const pool = t.poolPhase ? '<div class="dim">' + esc(t.poolPhase) + ' · ' + (t.poolMembers || []).map(x => esc(x + (m.ag[x] ? ' ' + m.ag[x].model : ''))).join(', ') + '</div>' : '';
      const nested = children.length ? '<button type="button" class="btn sm" data-pool-toggle="' + esc(id) + '" aria-expanded="' + B.expanded.has(id) + '">' + (B.expanded.has(id) ? '▾ ' : '▸ ') + children.length + ' subtasks</button>' : '';
      const html = pool + nested + (t.parent ? '<span class="dim">↳ ' + esc(t.parent) + '</span>' : '') + '<div class="tcr"><span class="tid">' + id + '</span>' + (!B.big && t.st !== 'merged' && unmet.length ? '<span class="dep" title="waits on ' + unmet.join(' ') + '">← ' + unmet.join(' ') + '</span>' : '') + '<span class="tow"><span style="color:' + agCol(t.owner || 'mgr') + '">' + esc(t.owner || '') + '</span>' + (blocked ? '<span class="ask" title="waiting for your answer">?</span>' : '') + (t.st === 'merged' ? '<span class="ok">✓' + (t.ms ? ' ' + fmtMs(t.ms) : '') + '</span>' : '') + '</span></div><span class="tti">' + esc(t.title) + '</span>' + (B.big ? '<span class="tow">' + need + (t.st === 'verify' ? '<span class="warm">▸ verifying</span>' : '') + (blocked ? '<span class="warm">waiting for your answer</span>' : '') + (t.scope && t.scope !== '-' ? '<span class="dim">' + esc(t.scope) + '</span>' : '') + '</span>' : '') + (children.length ? '<div class="subtasks"></div>' : '');
      if (n._h !== html) { n._h = html; n.innerHTML = html; }
      n.classList.toggle('blocked', !!blocked); n.title = id + ' · ' + t.title + ' · ' + (t.owner || '');
      const col = t.parent && B.nodes[t.parent] ? B.nodes[t.parent].querySelector('.subtasks') : B.cols[t.st] || B.cols.todo;
      if (col && n.parentNode !== col) { col.appendChild(n); if (vis && before[id]) moved.push(id); }
    });
    COLS.forEach(([k]) => { $('[data-col="' + k + '"] .cnt', B.host).textContent = counts[k] || 0; const cl = B.cols[k], em = cl.querySelector('.bempty'); if (!cl.querySelector('.tcard')) { if (!em) cl.appendChild(mk('div', { class: 'bempty' }, 'nothing')); } else if (em) em.remove(); });
    if (!ui.still()) moved.forEach(id => { const n = B.nodes[id], a = before[id], b = n.getBoundingClientRect(), dx = a.left - b.left, dy = a.top - b.top; if (Math.abs(dx) + Math.abs(dy) > 2) B.scope.animate(n, [{ transform: 'translate(' + dx + 'px,' + dy + 'px)', background: 'var(--warm-b)' }, { transform: 'none', background: 'var(--panel)' }], { duration: 620, easing: 'cubic-bezier(.2,.7,.2,1)' }); });
    return counts;
  }
  ui.board = { make, update, COLS };
})(SL);
