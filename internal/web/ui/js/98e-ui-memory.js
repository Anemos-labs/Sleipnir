/* 98e-ui-memory.js: the Memory view. It shows what the agents carry from one session to the next, in three cards, and lets the person
 * change it (GET /api/sessions/ID/memory; docs/WEB-API.md):
 *   - Agent notes: the notes the agents saved with the memory tool (~/.sleipnir/MEMORY.md). A note is edited, removed or added in
 *     place; the whole list is sent (PUT .../memory/notes) under the tool's limits.
 *   - Instruction files: ~/.sleipnir/SLEIPNIR.md, the project's AGENTS.md, CLAUDE.md, SLEIPNIR.md and the local files, grouped by
 *     whose they are, each with whether a session started now reads it; a file opens to its text and is edited or created there
 *     (PUT .../memory/file). Imported files are shown, not edited.
 *   - Lessons: what earlier goals taught (~/.sleipnir/lessons.json), with their record; a lesson can be removed
 *     (DELETE .../memory/lessons/ID).
 * Every write names the version it was edited from: when the file changed in between (an agent saved a note), the server refuses and
 * the view reads it again. A change applies to the sessions started after it; a running team keeps the prompt it started with.
 *
 * Notes, file text, paths and lessons are data: they go through esc() or textContent. */
(function (SL) {
  'use strict';
  const U = SL.u, { $, esc } = U, ui = SL.ui = SL.ui || {};
  const TK = SL.toolkit || {}, net = TK.net;
  const arr = x => (Array.isArray(x) ? x : []);
  const plural = (n, w) => n + ' ' + w + (n === 1 ? '' : 's');
  /** The groups of instruction files, in the order a session reads them: [scope, title, what they are]. */
  const SCOPES = [['user', 'Yours', 'in ~/.sleipnir: every session of every project reads them'], ['project', 'This project', 'at the project root, usually committed with it'],
    ['dir', 'Folders', 'in the folders between the root and where the session runs'], ['local', 'Local', 'this checkout only: keep them out of version control'],
    ['import', 'Imported', 'pulled in by an @path line of a file above']];
  const NEXT = 'A change applies to the sessions started after it; a running team keeps the prompt it started with.';

  /** The heading of a card: its title and what sits on the right. */
  const head = (t, r) => '<div class="ph"><h2>' + t + '</h2><div class="r">' + (r || '') + '</div></div>';
  const onChip = (on, off) => on ? '<span class="chip mem-on">on</span>' : '<span class="chip mem-off" title="' + esc(off) + '">off</span>';

  function notesHtml(st) {
    const n = st.v && st.v.notes; if (!n) return '<section class="panel">' + head('Agent notes') + '<p class="stubnote mem-pad">…</p></section>';
    const notes = arr(n.notes), ed = st.noteEdit;
    const rows = notes.map((t, i) => ed === i
      ? '<li class="mem-note editing"><span class="mem-n">' + (i + 1) + '</span><form class="mem-nf" data-note-save="' + i + '"><textarea rows="2" maxlength="' + (n.maxLen * 2) + '" aria-label="note ' + (i + 1) + '">' + esc(st.noteDraft) + '</textarea><div class="mem-btns"><button class="btn sm pri" type="submit">Save</button><button class="btn sm" type="button" data-note-cancel>Cancel</button></div></form></li>'
      : '<li class="mem-note"><span class="mem-n">' + (i + 1) + '</span><span class="mem-t">' + esc(t) + '</span><span class="mem-btns"><button class="btn sm" type="button" data-note-edit="' + i + '">Edit</button><button class="btn sm" type="button" data-note-del="' + i + '" aria-label="remove note ' + (i + 1) + '">Remove</button></span></li>').join('');
    return '<section class="panel mem-notes">' + head('Agent notes', onChip(n.on, 'the Memory notes plugin is off: sessions neither read nor save these notes') + '<span class="mono dim">' + esc(n.path) + '</span>') +
      '<p class="mem-about">What the agents chose to remember about you and your setup, with the memory tool. Every session starts with them, in any project. You approve each note an agent saves; here you change them yourself.</p>' +
      (notes.length ? '<ol class="mem-nl">' + rows + '</ol>' : '<p class="stubnote mem-pad">No notes yet. An agent adds one when it learns something lasting, or add one below.</p>') +
      '<form class="mem-add" data-note-add><input id="memNew" maxlength="' + (n.maxLen * 2) + '" value="' + esc(st.newDraft) + '" placeholder="one fact in one sentence, e.g. I prefer pnpm over npm" aria-label="a new note" autocomplete="off"><button class="btn sm pri" type="submit"' + (notes.length >= n.max ? ' disabled title="the memory is full: remove or merge notes first"' : '') + '>Add note</button></form>' +
      '<p class="mem-foot dim">' + notes.length + ' of ' + n.max + ' notes · at most ' + n.maxLen + ' characters each</p></section>';
  }

  function fileHtml(st, f) {
    const open = st.open[f.path], edit = st.edit[f.path] != null;
    const chips = (f.loaded ? '<span class="chip mem-on" title="a session started now has it in its shared prompt">read</span>' : f.exists ? '<span class="chip mem-off">not read</span>' : '<span class="chip">not created</span>') +
      (f.truncated ? '<span class="chip mem-warn" title="the prompt has only the start of it">cut</span>' : '') + (f.exists && !f.editable ? '<span class="chip">read-only</span>' : '');
    const lines = f.exists && f.text ? f.text.replace(/\n$/, '').split('\n').length : 0;
    let body = '';
    if (open || edit) {
      body = edit ? '<form class="mem-ff" data-file-save="' + esc(f.path) + '"><textarea class="mono" rows="' + Math.min(24, Math.max(8, lines + 2)) + '" spellcheck="false" aria-label="' + esc(f.path) + '">' + esc(st.edit[f.path]) + '</textarea>' +
        '<div class="mem-btns"><button class="btn sm pri" type="submit">' + (f.exists ? 'Save' : 'Create') + '</button><button class="btn sm" type="button" data-file-cancel="' + esc(f.path) + '">Cancel</button><span class="dim">' + esc(NEXT) + '</span></div></form>'
        : f.text ? '<pre class="pre mem-pre">' + esc(f.text) + '</pre>' : '<p class="stubnote mem-pad">The file is empty.</p>';
    }
    const act = edit ? '' : (f.exists ? '<button class="btn sm" type="button" data-file-open="' + esc(f.path) + '" aria-expanded="' + !!open + '">' + (open ? 'Hide' : 'View') + '</button>' : '') +
      (f.editable ? '<button class="btn sm' + (f.exists ? '' : ' pri') + '" type="button" data-file-edit="' + esc(f.path) + '">' + (f.exists ? 'Edit' : 'Create') + '</button>' : '');
    return '<li class="mem-file' + (f.exists ? '' : ' absent') + '" data-file="' + esc(f.path) + '"><div class="mem-fh"><span class="mono mem-p">' + esc(f.path) + '</span><span class="mem-chips">' + chips + '</span>' +
      (f.exists ? '<span class="dim mem-sz">' + plural(lines, 'line') + '</span>' : '') + '<span class="mem-btns">' + act + '</span></div>' +
      (f.why && f.exists ? '<p class="mem-why dim">' + esc(f.why) + '</p>' : '') + body + '</li>';
  }

  function filesHtml(st) {
    const v = st.v; if (!v) return '<section class="panel">' + head('Instruction files') + '<p class="stubnote mem-pad">…</p></section>';
    const files = arr(v.files), there = files.filter(f => f.exists).length;
    const groups = SCOPES.map(([sc, title, about]) => {
      const fs = files.filter(f => f.scope === sc && (f.exists || st.showAll || st.edit[f.path] != null)); if (!fs.length) return '';
      return '<div class="mem-grp"><h3 class="lab">' + esc(title) + ' <span class="dim">' + esc(about) + '</span></h3><ul class="mem-fl">' + fs.map(f => fileHtml(st, f)).join('') + '</ul></div>';
    }).join('');
    const hidden = files.filter(f => !f.exists).length;
    return '<section class="panel mem-files">' + head('Instruction files', '<span class="dim">' + plural(there, 'file') + '</span>' + (hidden ? '<button class="btn sm" type="button" data-files-all aria-pressed="' + !!st.showAll + '">' + (st.showAll ? 'Hide the places with no file' : 'New file…') + '</button>' : '')) +
      '<p class="mem-about">Standing instructions every agent reads at the start of a session: yours, then the project’s, then the local ones, the later winning. ' + (!v.root ? 'With no session open, only yours are shown. ' : v.trusted ? '' : '<b class="warm">This project is not trusted</b>, so its sessions read only yours: trust it in Settings › Permissions & trust. ') + esc(NEXT) + '</p>' +
      (groups || '<p class="stubnote mem-pad">No instruction file yet. <button class="linkbtn" type="button" data-files-all>Create one</button>: AGENTS.md at the project root is read by most agent tools.</p>') + '</section>';
  }

  function lessonHtml(l) {
    const pct = Math.round((l.utility || 0) * 100);
    return '<li class="mem-ls"><div class="mem-lh"><span class="chip ' + (l.kind === 'pitfall' ? 'mem-warn' : 'mem-on') + '">' + esc(l.kind) + '</span><span class="mem-t">' + esc(l.text) + '</span>' +
      '<button class="btn sm" type="button" data-lesson-del="' + esc(l.id) + '">Remove</button></div>' +
      '<div class="mem-lm dim">' + (l.task ? '<span title="the goal it came from">from: ' + esc(l.task) + '</span>' : '') +
      '<span class="mono" title="goals it was shown to: reached / not reached">' + plural(l.uses, 'use') + ' · ' + l.wins + ' won · ' + l.losses + ' lost</span>' +
      '<span class="mem-u" title="how often the goals it was shown to were reached (smoothed)"><i style="width:' + pct + '%"></i></span><span class="mono">' + pct + '%</span></div></li>';
  }

  function lessonsHtml(st) {
    const L = st.v && st.v.lessons; if (!L) return '<section class="panel">' + head('Lessons') + '<p class="stubnote mem-pad">…</p></section>';
    const ls = arr(L.lessons);
    return '<section class="panel mem-lessons">' + head('Lessons', onChip(L.on, 'the Lesson memory plugin is off') + '<span class="mono dim">' + esc(L.path) + '</span>') +
      '<p class="mem-about">What earlier goals taught: a strategy from a goal that was reached, a pitfall from one that was not. A goal is shown the few most like it when it starts; recall needs an embedder model (<span class="mono">--role-model embedder=MODEL</span>).</p>' +
      (L.error ? '<p class="stubnote mem-pad err">' + esc(L.error) + '</p>' : '') +
      (ls.length ? '<ul class="mem-ll">' + ls.map(lessonHtml).join('') + '</ul>' : '<p class="stubnote mem-pad">No lessons yet. They are distilled when a goal ends, while the lesson memory is on.</p>') + '</section>';
  }

  SL.views.register({ name: 'memory', title: 'Memory', mount(sc, root, params, S0) {
    const P = ui.pageHead(sc, root, S0 || SL.sessions.active, 'memory', params, { title: 'Memory', purpose: 'What the agents remember from one session to the next: the notes they saved, the instruction files they read and the lessons of earlier goals. Read it, correct it, remove what is wrong.' });
    const st = { v: null, seq: 0, sig: '', open: {}, edit: {}, noteEdit: -1, noteDraft: '', newDraft: '', noteBase: null, base: {}, showAll: false, busy: false };
    const S = () => SL.sessions.active;
    /** The routes of the active tab, or the server's own (the user's files only) while there is no tab. */
    const api = sfx => { const s = S(); return (s && s.id ? net.tab(s, '/memory') : '/api/memory') + (sfx || ''); };
    const editing = () => st.noteEdit >= 0 || Object.keys(st.edit).length > 0;
    function paint(force) {
      const sig = JSON.stringify([st.v, st.open, st.edit, st.noteEdit, st.showAll]); if (!force && sig === st.sig) return; st.sig = sig;
      const a = document.activeElement, keep = a && root.contains(a) && a.tagName === 'TEXTAREA' ? a.closest('[data-file-save],[data-note-save]') : null, inNew = a && a.id === 'memNew' && root.contains(a);
      P.fill({ 'memory.notes': notesHtml(st), 'memory.files': filesHtml(st), 'memory.lessons': lessonsHtml(st) });
      if (keep) { const k = keep.dataset.fileSave != null ? '[data-file-save="' + CSS.escape(keep.dataset.fileSave) + '"] textarea' : '[data-note-save] textarea', n = root.querySelector(k); if (n) n.focus({ preventScroll: true }); }
      if (inNew) { const n = $('#memNew', root); if (n) { n.focus({ preventScroll: true }); n.setSelectionRange(n.value.length, n.value.length); } }
    }
    async function load(force) {
      if (!net) return; const mine = ++st.seq;
      const r = await net.get(api()); if (!sc.alive || mine !== st.seq) return;
      if (!r.ok || !r.data) { if (force) net.fail(r, 'the memory could not be read'); return; }
      st.v = r.data; paint();
    }
    /** Take the answer of a write: the whole view as it is now, or a refusal (a file that changed meanwhile is read again). */
    function took(r, done, what) {
      st.busy = false; if (!sc.alive) return false;
      if (!r.ok) { if (r.status === 409 || r.code === 'changed') { ui.toast('it changed meanwhile and was not saved: Cancel your edit to see it as it is now, then edit again', 'warm'); load(true); } else net.fail(r, what); return false; }
      if (r.data) st.v = r.data; ui.toast(done, 'ok'); return true;
    }
    /** Send the whole list of notes with the version it was made from (base: the list and version an edit or a removal started from). */
    async function putNotes(notes, done, version) {
      if (st.busy || !st.v) return false; st.busy = true;
      const r = await net.put(api('/notes'), { notes, version: version != null ? version : st.v.notes.version });
      const ok = took(r, done, 'the notes were not saved'); paint(true); return ok;
    }
    const file = p => arr(st.v && st.v.files).find(f => f.path === p);
    sc.listen(root, 'click', e => {
      const t = e.target, notes = st.v ? arr(st.v.notes.notes) : [];
      let b;
      if ((b = t.closest('[data-note-edit]'))) { st.noteEdit = +b.dataset.noteEdit; st.noteDraft = notes[st.noteEdit] || ''; st.noteBase = { notes: notes.slice(), version: st.v.notes.version }; paint(); const x = $('[data-note-save] textarea', root); if (x) { x.focus(); x.setSelectionRange(x.value.length, x.value.length); } return; }
      if (t.closest('[data-note-cancel]')) { st.noteEdit = -1; paint(); return; }
      if ((b = t.closest('[data-note-del]'))) { const i = +b.dataset.noteDel, n = notes[i], ver = st.v.notes.version; if (n == null) return;
        ui.confirm({ title: 'Remove this note', text: 'The agents will no longer start their sessions with it:', detail: '<p>' + esc(n) + '</p>', ok: 'Remove', danger: true, run: () => putNotes(notes.filter((_, j) => j !== i), 'note removed', ver) }); return; }
      if ((b = t.closest('[data-file-open]'))) { const p = b.dataset.fileOpen; st.open[p] = !st.open[p]; paint(); return; }
      if ((b = t.closest('[data-file-edit]'))) { const p = b.dataset.fileEdit, f = file(p); if (!f) return; st.edit[p] = f.exists ? f.text : ''; st.base[p] = f.version || ''; paint(); const x = root.querySelector('[data-file-save="' + CSS.escape(p) + '"] textarea'); if (x) x.focus(); return; }
      if ((b = t.closest('[data-file-cancel]'))) { delete st.edit[b.dataset.fileCancel]; paint(); return; }
      if (t.closest('[data-files-all]')) { st.showAll = !st.showAll; paint(); return; }
      if ((b = t.closest('[data-lesson-del]'))) { const id = b.dataset.lessonDel, l = arr(st.v && st.v.lessons.lessons).find(x => x.id === id); if (!l) return;
        ui.confirm({ title: 'Remove this lesson', text: 'Later goals will no longer be shown it:', detail: '<p>' + esc(l.text) + '</p>', ok: 'Remove', danger: true,
          run: async () => { const r = await net.del(api('/lessons/' + encodeURIComponent(id))); took(r, 'lesson removed', 'the lesson was not removed'); paint(true); } }); }
    });
    sc.listen(root, 'input', e => {
      const t = e.target; if (t.id === 'memNew') { st.newDraft = t.value; return; } if (t.tagName !== 'TEXTAREA') return;
      const ff = t.closest('[data-file-save]'); if (ff) { st.edit[ff.dataset.fileSave] = t.value; st.sig = JSON.stringify([st.v, st.open, st.edit, st.noteEdit, st.showAll]); return; }
      if (t.closest('[data-note-save]')) st.noteDraft = t.value;
    });
    sc.listen(root, 'keydown', e => { if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') { const f = e.target.closest && e.target.closest('form.mem-ff,form.mem-nf'); if (f) { e.preventDefault(); f.requestSubmit(); } } });
    sc.listen(root, 'submit', async e => {
      const f = e.target; e.preventDefault(); if (!st.v) return;
      const notes = arr(st.v.notes.notes);
      if (f.matches('[data-note-add]')) { const inp = $('#memNew', root), v = inp.value.trim(); if (!v) { inp.focus(); return; } if (await putNotes(notes.concat([v]), 'note saved: the next session starts with it')) { st.newDraft = ''; const n = $('#memNew', root); if (n) { n.value = ''; n.focus(); } } return; }
      if (f.matches('[data-note-save]')) { const base = st.noteBase || { notes, version: st.v.notes.version }, i = +f.dataset.noteSave, v = st.noteDraft.trim(), all = base.notes, next = v ? all.map((n, j) => (j === i ? v : n)) : all.filter((_, j) => j !== i); if (await putNotes(next, v ? 'note saved' : 'note removed', base.version)) { st.noteEdit = -1; paint(true); } return; }
      if (f.matches('[data-file-save]')) {
        const p = f.dataset.fileSave, cur = file(p); if (!cur || st.busy) return; st.busy = true;
        const r = await net.put(api('/file'), { path: p, text: st.edit[p], version: st.base[p] != null ? st.base[p] : cur.version || '' });
        if (took(r, (cur.exists ? 'saved ' : 'created ') + p, 'the file was not saved')) { delete st.edit[p]; delete st.base[p]; st.open[p] = true; } paint(true);
      }
    });
    sc.interval(() => { if (!editing() && !st.busy) load(); }, 8000);
    sc.on('activated', () => { st.v = null; st.open = {}; st.edit = {}; st.noteEdit = -1; paint(); load(); });
    paint(); load();
  } });
})(SL);
