/* 98e-ui-memory.js: the Memory view. It shows what the agents carry from one session to the next, in five cards, and lets the person
 * change it (GET /api/sessions/ID/memory; docs/WEB-API.md):
 *   - Agent notes: the notes the agents saved with the memory tool (~/.sleipnir/MEMORY.md). A note is edited, removed or added in
 *     place; the whole list is sent (PUT .../memory/notes) under the tool's limits. Agents save notes without asking: the Memory
 *     item of the left bar counts each change (96-ui-nav.js) until this view is opened.
 *   - Project notes: the same for the project's own notes (.sleipnir/MEMORY.local.md, scope project; PUT .../memory/notes with
 *     scope project).
 *   - Team lore: what each role of a team learned in this project (the lore files under the state directory), read-only.
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

  /** The two lists of notes: [scope, card title, the input's id, what they are]. */
  const NOTES = {
    user: ['Agent notes', 'memNew', 'What the agents chose to remember about you and your setup, with the memory tool. Every session starts with them, in any project. An agent saves a note without asking you: the Memory item of the left bar counts the new ones, and here you keep, change or remove them.', 'one fact in one sentence, e.g. I prefer pnpm over npm'],
    project: ['Project notes', 'memNewP', 'What the agents chose to remember about this project only (scope project): a fact about the code base, a mistake not to repeat here. Sessions of this project start with them while it is trusted; the file stays out of git.', 'one fact about this project, e.g. make gen regenerates the client'],
  };
  function notesHtml(st, scope) {
    const [title, inputId, about, hint] = NOTES[scope], n = st.v && (scope === 'project' ? st.v.project : st.v.notes);
    if (!st.v) return '<section class="panel">' + head(title) + '<p class="stubnote mem-pad">…</p></section>';
    if (!n) return '<section class="panel mem-notes">' + head(title) + '<p class="stubnote mem-pad">Open a session of a project to see the notes the agents saved for it.</p></section>';
    const notes = arr(n.notes), ed = st.noteScope === scope ? st.noteEdit : -1, sc = ' data-scope="' + scope + '"';
    const rows = notes.map((t, i) => ed === i
      ? '<li class="mem-note editing"><span class="mem-n">' + (i + 1) + '</span><form class="mem-nf" data-note-save="' + i + '"' + sc + '><textarea rows="2" maxlength="' + (n.maxLen * 2) + '" aria-label="note ' + (i + 1) + '">' + esc(st.noteDraft) + '</textarea><div class="mem-btns"><button class="btn sm pri" type="submit">Save</button><button class="btn sm" type="button" data-note-cancel>Cancel</button></div></form></li>'
      : '<li class="mem-note"><span class="mem-n">' + (i + 1) + '</span><span class="mem-t">' + esc(t) + '</span><span class="mem-btns"><button class="btn sm" type="button" data-note-edit="' + i + '"' + sc + '>Edit</button><button class="btn sm" type="button" data-note-del="' + i + '"' + sc + ' aria-label="remove note ' + (i + 1) + '">Remove</button></span></li>').join('');
    return '<section class="panel mem-notes">' + head(title, onChip(n.on, 'the Memory notes plugin is off: sessions neither read nor save these notes') + '<span class="mono dim">' + esc(n.path) + '</span>') +
      '<p class="mem-about">' + about + (scope === 'project' && !st.v.trusted ? ' <b class="warm">This project is not trusted</b>, so its sessions neither read nor save them.' : '') + '</p>' +
      (notes.length ? '<ol class="mem-nl">' + rows + '</ol>' : '<p class="stubnote mem-pad">No notes yet. An agent adds one when it learns something lasting, or add one below.</p>') +
      '<form class="mem-add" data-note-add' + sc + '><input id="' + inputId + '" maxlength="' + (n.maxLen * 2) + '" value="' + esc(st.newDraft[scope]) + '" placeholder="' + esc(hint) + '" aria-label="a new note" autocomplete="off"><button class="btn sm pri" type="submit"' + (notes.length >= n.max ? ' disabled title="the memory is full: remove or merge notes first"' : '') + '>Add note</button></form>' +
      '<p class="mem-foot dim">' + notes.length + ' of ' + n.max + ' notes · at most ' + n.maxLen + ' characters each</p></section>';
  }

  /** What each role of a team learned in this project: read-only here, the files are for a person to edit (mark one [canon] to keep it). */
  function loreHtml(st) {
    const L = st.v && st.v.lore; if (!st.v) return '<section class="panel">' + head('Team lore') + '<p class="stubnote mem-pad">…</p></section>';
    if (!L) return '<section class="panel mem-lore">' + head('Team lore') + '<p class="stubnote mem-pad">Open a session of a project to see what its team roles learned there.</p></section>';
    const roles = arr(L.roles).filter(r => arr(r.items).length || r.error);
    const role = r => '<div class="mem-grp"><h3 class="lab">' + esc(r.role) + ' <span class="mono dim">' + esc(r.path) + '</span></h3>' + (r.error ? '<p class="stubnote mem-pad err">' + esc(r.error) + '</p>' : '') +
      '<ul class="mem-ll">' + arr(r.items).map(it => '<li class="mem-ls"><div class="mem-lh"><span class="chip ' + (it.stage === 'canon' ? 'mem-on' : '') + '" title="' + (it.stage === 'canon' ? 'marked canon by a person: kept first and for good' : 'saved when a task was accepted') + '">' + esc(it.stage) + (it.confirmed > 1 ? ' ×' + it.confirmed : '') + '</span><span class="mem-t">' + esc(it.text) + '</span></div></li>').join('') + '</ul></div>';
    return '<section class="panel mem-lore">' + head('Team lore', onChip(L.on, 'the Memory notes plugin is off: team sessions neither read nor save lore') + '<span class="mono dim">' + esc(L.dir) + '</span>') +
      '<p class="mem-about">What the workers of each role learned in this project: the role notes of a worker whose task the manager accepted, saved for the agents of that role in later team sessions. Edit a file by hand to delete a wrong line, or mark one <span class="mono">[canon]</span> to keep it first and for good.</p>' +
      (L.error ? '<p class="stubnote mem-pad err">' + esc(L.error) + '</p>' : '') +
      (roles.length ? roles.map(role).join('') : '<p class="stubnote mem-pad">No lore yet. It is saved when the manager accepts a task whose worker kept role notes.</p>') + '</section>';
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
      '<span class="mem-u" title="how often the goals it was shown to were reached (smoothed)"><i style="width:' + pct + '%"></i></span><span class="mono">' + pct + '%</span></div>' +
      (l.replaced ? '<div class="mem-lm dim mem-lr" title="an earlier lesson that the outcome of the goal this one came from showed wrong; goals are no longer shown it">replaced: <s>' + esc(l.replaced) + '</s></div>' : '') + '</li>';
  }

  function lessonsHtml(st) {
    const L = st.v && st.v.lessons; if (!L) return '<section class="panel">' + head('Lessons') + '<p class="stubnote mem-pad">…</p></section>';
    const ls = arr(L.lessons);
    return '<section class="panel mem-lessons">' + head('Lessons', onChip(L.on, 'the Lesson memory plugin is off') + '<span class="mono dim">' + esc(L.path) + '</span>') +
      '<p class="mem-about">What earlier goals taught: a strategy from a goal that was reached, a pitfall from one that was not. A goal is shown the few most like it when it starts; they are found by keyword, and by meaning too with an embedder model (<span class="mono">--role-model embedder=MODEL</span>).</p>' +
      (L.error ? '<p class="stubnote mem-pad err">' + esc(L.error) + '</p>' : '') +
      (ls.length ? '<ul class="mem-ll">' + ls.map(lessonHtml).join('') + '</ul>' : '<p class="stubnote mem-pad">No lessons yet. They are distilled when a goal ends, while the lesson memory is on.</p>') + '</section>';
  }

  SL.views.register({ name: 'memory', title: 'Memory', mount(sc, root, params, S0) {
    const P = ui.pageHead(sc, root, S0 || SL.sessions.active, 'memory', params, { title: 'Memory', purpose: 'What the agents remember from one session to the next: the notes they saved, the instruction files they read and the lessons of earlier goals. Read it, correct it, remove what is wrong.' });
    const st = { v: null, seq: 0, sig: '', open: {}, edit: {}, noteEdit: -1, noteScope: 'user', noteDraft: '', newDraft: { user: '', project: '' }, noteBase: null, base: {}, showAll: false, busy: false };
    const S = () => SL.sessions.active;
    /** The routes of the active tab, or the server's own (the user's files only) while there is no tab. */
    const api = sfx => { const s = S(); return (s && s.id ? net.tab(s, '/memory') : '/api/memory') + (sfx || ''); };
    const editing = () => st.noteEdit >= 0 || Object.keys(st.edit).length > 0;
    function paint(force) {
      const sig = JSON.stringify([st.v, st.open, st.edit, st.noteEdit, st.noteScope, st.showAll]); if (!force && sig === st.sig) return; st.sig = sig;
      const a = document.activeElement, keep = a && root.contains(a) && a.tagName === 'TEXTAREA' ? a.closest('[data-file-save],[data-note-save]') : null, inNew = a && (a.id === 'memNew' || a.id === 'memNewP') && root.contains(a) ? a.id : '';
      P.fill({ 'memory.notes': notesHtml(st, 'user'), 'memory.project': notesHtml(st, 'project'), 'memory.lore': loreHtml(st), 'memory.files': filesHtml(st), 'memory.lessons': lessonsHtml(st) });
      if (keep) { const k = keep.dataset.fileSave != null ? '[data-file-save="' + CSS.escape(keep.dataset.fileSave) + '"] textarea' : '[data-note-save] textarea', n = root.querySelector(k); if (n) n.focus({ preventScroll: true }); }
      if (inNew) { const n = $('#' + inNew, root); if (n) { n.focus({ preventScroll: true }); n.setSelectionRange(n.value.length, n.value.length); } }
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
    /** the index of the note of notes, other than skip, that says v (the server keeps one of notes that differ only in case or spacing), or -1 */
    const sameAs = (notes, v, skip) => { const k = s => String(s).trim().replace(/\s+/g, ' ').toLowerCase(), want = k(v); return notes.findIndex((n, j) => j !== skip && k(n) === want); };
    /** the notes of scope (user or project) as the view last read them */
    const listOf = scope => (st.v && (scope === 'project' ? st.v.project : st.v.notes)) || { notes: [], version: '' };
    async function putNotes(scope, notes, done, version) {
      if (st.busy || !st.v) return false; st.busy = true;
      const was = S() && S().id, r = await net.put(api('/notes'), { notes, version: version != null ? version : listOf(scope).version, scope });
      if ((S() && S().id) !== was) { st.busy = false; load(true); return false; }   // another session's project is shown now: its answer is not this view's
      const ok = took(r, done, 'the notes were not saved'); paint(true); return ok;
    }
    const file = p => arr(st.v && st.v.files).find(f => f.path === p);
    sc.listen(root, 'click', e => {
      const t = e.target, el = t.closest('[data-scope]'), scope = el ? el.dataset.scope : 'user', L = listOf(scope), notes = arr(L.notes);
      let b;
      if ((b = t.closest('[data-note-edit]'))) { st.noteEdit = +b.dataset.noteEdit; st.noteScope = scope; st.noteDraft = notes[st.noteEdit] || ''; st.noteBase = { notes: notes.slice(), version: L.version }; paint(); const x = $('[data-note-save] textarea', root); if (x) { x.focus(); x.setSelectionRange(x.value.length, x.value.length); } return; }
      if (t.closest('[data-note-cancel]')) { st.noteEdit = -1; paint(); return; }
      if ((b = t.closest('[data-note-del]'))) { const i = +b.dataset.noteDel, n = notes[i], ver = L.version; if (n == null) return;
        ui.confirm({ title: 'Remove this note', text: 'The agents will no longer start their sessions with it:', detail: '<p>' + esc(n) + '</p>', ok: 'Remove', danger: true, run: () => putNotes(scope, notes.filter((_, j) => j !== i), 'note removed', ver) }); return; }
      if ((b = t.closest('[data-file-open]'))) { const p = b.dataset.fileOpen; st.open[p] = !st.open[p]; paint(); return; }
      if ((b = t.closest('[data-file-edit]'))) { const p = b.dataset.fileEdit, f = file(p); if (!f) return; st.edit[p] = f.exists ? f.text : ''; st.base[p] = f.version || ''; paint(); const x = root.querySelector('[data-file-save="' + CSS.escape(p) + '"] textarea'); if (x) x.focus(); return; }
      if ((b = t.closest('[data-file-cancel]'))) { delete st.edit[b.dataset.fileCancel]; paint(); return; }
      if (t.closest('[data-files-all]')) { st.showAll = !st.showAll; paint(); return; }
      if ((b = t.closest('[data-lesson-del]'))) { const id = b.dataset.lessonDel, l = arr(st.v && st.v.lessons.lessons).find(x => x.id === id); if (!l) return;
        ui.confirm({ title: 'Remove this lesson', text: 'Later goals will no longer be shown it:', detail: '<p>' + esc(l.text) + '</p>', ok: 'Remove', danger: true,
          run: async () => { const r = await net.del(api('/lessons/' + encodeURIComponent(id))); took(r, 'lesson removed', 'the lesson was not removed'); paint(true); } }); }
    });
    sc.listen(root, 'input', e => {
      const t = e.target; if (t.id === 'memNew' || t.id === 'memNewP') { st.newDraft[t.id === 'memNew' ? 'user' : 'project'] = t.value; return; } if (t.tagName !== 'TEXTAREA') return;
      const ff = t.closest('[data-file-save]'); if (ff) { st.edit[ff.dataset.fileSave] = t.value; st.sig = JSON.stringify([st.v, st.open, st.edit, st.noteEdit, st.noteScope, st.showAll]); return; }
      if (t.closest('[data-note-save]')) st.noteDraft = t.value;
    });
    sc.listen(root, 'keydown', e => { if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') { const f = e.target.closest && e.target.closest('form.mem-ff,form.mem-nf'); if (f) { e.preventDefault(); f.requestSubmit(); } } });
    sc.listen(root, 'submit', async e => {
      const f = e.target; e.preventDefault(); if (!st.v) return;
      const scope = f.dataset.scope || 'user', L = listOf(scope), notes = arr(L.notes), inputId = NOTES[scope][1];
      if (f.matches('[data-note-add]')) { const inp = $('#' + inputId, root), v = inp.value.trim(); if (!v) { inp.focus(); return; } if (sameAs(notes, v, -1) >= 0) { ui.toast('that note is saved already (note ' + (sameAs(notes, v, -1) + 1) + ')', 'warm'); inp.focus(); return; } if (await putNotes(scope, notes.concat([v]), 'note saved: the next session starts with it')) { st.newDraft[scope] = ''; const n = $('#' + inputId, root); if (n) { n.value = ''; n.focus(); } } return; }
      if (f.matches('[data-note-save]')) { const base = st.noteBase || { notes, version: L.version }, i = +f.dataset.noteSave, v = st.noteDraft.trim(), all = base.notes, next = v ? all.map((n, j) => (j === i ? v : n)) : all.filter((_, j) => j !== i); if (v && sameAs(all, v, i) >= 0) { ui.toast('note ' + (sameAs(all, v, i) + 1) + ' says that already: change it there, or remove this one', 'warm'); return; } if (await putNotes(scope, next, v ? 'note saved' : 'note removed', base.version)) { st.noteEdit = -1; paint(true); } return; }
      if (f.matches('[data-file-save]')) {
        const p = f.dataset.fileSave, cur = file(p); if (!cur || st.busy) return; st.busy = true;
        const r = await net.put(api('/file'), { path: p, text: st.edit[p], version: st.base[p] != null ? st.base[p] : cur.version || '' });
        if (took(r, (cur.exists ? 'saved ' : 'created ') + p, 'the file was not saved')) { delete st.edit[p]; delete st.base[p]; st.open[p] = true; } paint(true);
      }
    });
    sc.interval(() => { if (!editing() && !st.busy) load(); }, 8000);
    sc.on('activated', () => { st.v = null; st.open = {}; st.edit = {}; st.noteEdit = -1; st.newDraft.project = ''; paint(); load(); });   // a project note not sent is that project's, not the next one's
    paint(); load();
  } });
})(SL);
