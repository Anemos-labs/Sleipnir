/* 87b-ui-folder.js: SL.ui.folder, the folder field of the New session dialog. The field takes any path, ~ for the home folder included,
 * and completes the names of folders as it is typed, the way a shell does: the server lists the subfolders that start with what was
 * typed and says what the path is (GET /api/fs/dirs: a folder that exists, one to make, a git repository, empty or not). Tab completes
 * the start every name shares (or the one name there is), the arrow keys choose a name and Enter takes it; with the field empty the
 * projects the server knows are offered. The words that say what a path is come from describe(), which has no DOM and is tested alone.
 * Nothing is decided here: the server refuses what it cannot start, and asks the person to confirm a folder it does not know. */
(function (SL) {
  'use strict';
  const U = SL.u, { $, $$, esc } = U, ui = SL.ui = SL.ui || {};
  const folder = ui.folder = {};
  /** The element ids of one field (the New session dialog's by default; the Schedule form has its own). */
  const IDS = { input: 'nsCwd', list: 'nsDirList', state: 'nsDirState', projects: 'nsProjects', row: 'nsDir' };
  const DEBOUNCE_MS = 140, MAX_PROJECTS = 8, ABSOLUTE = /^([\\/]|[A-Za-z]:[\\/])/;   // a typed path that is written in full needs no repeating

  /** A typed path as its head (up to and including the last separator) and the unfinished name after it. */
  /** A path written in full (a root, or a drive): the server can take it as it is. */
  folder.absolute = text => ABSOLUTE.test(String(text).trim());
  /** What the ? beside a folder field says. */
  folder.helpText = 'Any folder on this machine; <span class="mono">~</span> is your home folder. Folder names complete as you type: <kbd>Tab</kbd> completes, <kbd>↑</kbd> <kbd>↓</kbd> choose, <kbd>Enter</kbd> takes one.';

  folder.split = text => { text = String(text == null ? '' : text); const i = Math.max(text.lastIndexOf('/'), text.lastIndexOf('\\')); return { head: text.slice(0, i + 1), tail: text.slice(i + 1) }; };

  /** The text that takes a listed folder: its path written the way the person writes theirs (the home folder as ~ when they began with
   *  ~), ending in a separator so that the next names are listed. listing: the server's answer ({home, sep}). */
  folder.accepted = (path, typed, listing) => {
    const home = listing && listing.home, sep = (listing && listing.sep) || '/';
    let p = String(path);
    if (String(typed).trim().charAt(0) === '~' && home && (p === home || p.startsWith(home + sep))) p = '~' + p.slice(home.length);
    return p.endsWith(sep) || p.endsWith('/') ? p : p + sep;
  };

  /** What Tab completes: the longest start that every listed name shares, when it goes beyond what is typed (or fixes its case); else ''. */
  folder.common = (entries, tail) => {
    if (!entries || !entries.length) return '';
    let c = entries[0].name;
    entries.forEach(e => { let i = 0; while (i < c.length && i < e.name.length && c[i] === e.name[i]) i++; c = c.slice(0, i); });
    return c.length > tail.length || (c.length === tail.length && c !== tail && c.toLowerCase() === tail.toLowerCase()) ? c : '';
  };

  /**
   * What a path is, in words (state: the server's DirState, or null when it could not be asked). level: ok (a folder that exists), new (a
   * folder to make), bad (nothing can start there), '' (nothing known). git: repo, inside (root names the repository above), bare (a
   * bare repository), offer (a new or empty folder: a repository can be made), files (a folder with files and no repository), '' (no
   * folder).
   */
  folder.describe = (state, opts) => {
    const s = state, make = !(opts && opts.make === false);   // make: a folder that does not exist may be made (a session), else it is refused (a job)
    if (!s) return { level: '', lead: '', git: '', root: '' };
    if (s.inaccessible) return { level: 'bad', lead: 'The server cannot look at this path.', git: '', root: '' };
    if (s.exists && !s.dir) return { level: 'bad', lead: 'That is a file, not a folder.', git: '', root: '' };
    if (!s.exists && !make) return { level: 'bad', lead: 'This folder does not exist.', git: '', root: '' };
    if (!s.exists) {
      if (!s.parentOk) return { level: 'bad', lead: 'This folder does not exist, and neither does the one it would be made in.', git: '', root: '' };
      return { level: 'new', lead: 'New folder: made when the session starts.', git: s.git === 'inside' ? 'inside' : 'offer', root: s.gitRoot || '' };
    }
    if (!s.readable) return { level: 'bad', lead: 'The server cannot read this folder.', git: '', root: '' };
    return {
      level: 'ok', root: s.gitRoot || '',
      lead: s.known ? 'A project of this server.' : 'New to this server: you confirm it when you start.',
      git: s.git === 'repo' ? 'repo' : s.git === 'inside' ? 'inside' : s.git === 'bare' ? 'bare' : s.empty ? 'offer' : 'files',
    };
  };

  /** The line about git under the path (empty when a repository is only offered: that is a checkbox). */
  folder.gitNote = d => ({
    repo: 'A git repository.',
    bare: 'A bare git repository: it has no work tree to work in.',
    inside: 'Inside the git repository at ' + d.root + '.',
    files: 'Not a git repository.',
  })[d.git] || '';

  /** What lies behind a git line, for its ? dot (empty when there is nothing to add). */
  folder.gitHelp = d => (d.git === 'files' ? 'Checkpoints and rewind still work, and a team works in private copies of the folder. Run <span class="mono">git init</span> there to get commits and real history.' : '');

  /** The checks a folder's files suggest for --verify: [{cmd, why}], from the server's state. */
  folder.verifyHints = state => (state && Array.isArray(state.verify) ? state.verify : []).filter(v => v && typeof v.cmd === 'string');

  /** The words for a project's trust in the list of known projects (nothing when it is trusted or has nothing to trust). */
  const TRUST_WORD = { untrusted: 'not trusted', changed: 'changed since trusted', partial: 'partial', unreadable: 'unreadable' };
  const trustWord = t => t && t !== 'trusted' && t !== 'none' ? U.own(TRUST_WORD, t) || String(t) : '';

  /** The markup of the field: the text box that completes, its list, and what the path is. */
  folder.html = (value, ids) => { ids = Object.assign({}, IDS, ids); return '<div class="dirbox"><div class="dirrow"><input id="' + ids.input + '" type="text" value="' + esc(value == null ? '' : value) + '" role="combobox" aria-label="Folder" aria-autocomplete="list" aria-expanded="false" aria-controls="' + ids.list + '" autocomplete="off" autocapitalize="off" spellcheck="false" placeholder="~/code/my-project"><button type="button" class="btn sm" id="' + ids.projects + '" aria-label="Projects this server knows" title="Projects this server knows">Projects ▾</button></div>' +
    '<ul id="' + ids.list + '" class="dirlist" role="listbox" aria-label="Folders" hidden></ul></div><div id="' + ids.state + '" class="dirstate" role="status" aria-live="polite"></div>'; };

  /**
   * Make the field work. b: the dialog's body, sc: its scope, st: the dialog's state (st.cwd follows the text), o.projects(): the projects
   * the server knows ([{dir, name, trust}]), o.onState(state): called when the answer for the text in the box arrives. Returns
   * {state(), ensure()}: the answer for the text now in the box (null when none could be had), and a promise of it, asking when needed.
   */
  folder.mount = function (b, sc, st, o) {
    const ids = Object.assign({}, IDS, o.ids), input = $('#' + ids.input, b), list = $('#' + ids.list, b), box = $('#' + ids.state, b), make = o.make !== false;
    let listing = null, answered = null, seq = 0, timer = 0, rows = [], at = -1, pick = false;   // pick: the list is the known projects, whatever is typed
    const text = () => input.value;
    const isOpen = () => !list.hidden;

    function paintState() {
      const state = answered && answered.text === text() ? answered.state : null, d = folder.describe(state, { make }), note = folder.gitNote(d);
      box.className = 'dirstate' + (d.level ? ' ' + d.level : '');
      box.innerHTML = d.lead ? '<span class="dirlead">' + esc(d.lead) + '</span>' + (state && state.path && !ABSOLUTE.test(text().trim()) ? ' <span class="dirabs mono" title="the folder, in full">' + esc(state.path) + '</span>' : '') + (note ? '<span class="dirnote">' + esc(note) + (folder.gitHelp(d) ? ui.help(folder.gitHelp(d), 'a folder that is not a repository') : '') + '</span>' : '') : '';
    }

    function paintList() {
      const t = text().trim(), asked = answered && answered.text === text() ? listing : null;
      if (!t || pick) {
        const ps = (o.projects() || []).slice(0, MAX_PROJECTS);
        rows = ps.map(p => ({ path: p.dir, label: p.name || p.dir, sub: p.dir, tag: trustWord(p.trust), repo: false }));
      } else if (asked) {
        rows = asked.entries.map(e => ({ path: e.path, label: e.name, sub: '', tag: e.link ? 'link' : '', repo: !!e.repo }));
      } else rows = [];
      if (at >= rows.length) at = -1;
      list.innerHTML = rows.map((r, i) => '<li id="' + ids.row + i + '" role="option" class="dir' + (i === at ? ' on' : '') + '" data-i="' + i + '" aria-selected="' + (i === at) + '"><span class="dn">' + esc(r.label) + '</span>' + (r.sub ? '<span class="dp mono">' + esc(r.sub) + '</span>' : '') + (r.repo ? '<span class="tag">git</span>' : '') + (r.tag ? '<span class="tag warm">' + esc(r.tag) + '</span>' : '') + '</li>').join('') +
        ((!t || pick) && rows.length ? '<li class="dirmore" role="presentation">projects this server knows; or type a path</li>' : '') + (!pick && asked && asked.more ? '<li class="dirmore" role="presentation">more folders: keep typing</li>' : '');
      setOpen(rows.length > 0 && document.activeElement === input);
    }

    function setOpen(on) {
      list.hidden = !on; input.setAttribute('aria-expanded', String(on));
      if (on) input.setAttribute('data-popup-open', ''); else input.removeAttribute('data-popup-open');   // 94-keys.js leaves Escape to a field with its list open
      if (on && at >= 0) input.setAttribute('aria-activedescendant', ids.row + at); else input.removeAttribute('aria-activedescendant');
    }

    function ask(t) {
      const mine = ++seq;
      if (!t.trim()) { answered = null; listing = null; paintState(); paintList(); o.onState(null); return Promise.resolve(null); }
      return SL.api.get('/api/fs/dirs?path=' + encodeURIComponent(t)).then(r => {
        if (mine !== seq) return null;   // another text was asked for since
        if (r && r.ok && r.data && r.data.state) { listing = r.data; answered = { text: t, state: r.data.state }; }
        else { listing = null; answered = null; }
        paintState(); paintList(); o.onState(answered ? answered.state : null);
        return answered ? answered.state : null;
      });
    }

    /** Take row i: its folder is the text, and its subfolders are listed next. */
    function take(i) {
      const r = rows[i]; if (!r) return;
      input.value = st.cwd = folder.accepted(r.path, text(), listing); at = -1; pick = false; input.focus();
      ask(text()); setOpen(false);
    }

    /** Tab: the start that the listed names share, or the only one. */
    function complete() {
      if (!rows.length || !text().trim()) return false;
      if (at >= 0) { take(at); return true; }
      const { head, tail } = folder.split(text()), c = folder.common(listing && listing.entries, tail);
      if (listing && listing.entries.length === 1) { take(0); return true; }
      if (!c) return false;
      input.value = st.cwd = head + c; ask(text()); return true;
    }

    sc.listen(input, 'input', () => { st.cwd = text(); at = -1; pick = false; sc.clear(timer); timer = sc.timeout(() => ask(text()), DEBOUNCE_MS); paintState(); });
    sc.listen(input, 'focus', () => { paintList(); });
    sc.listen(input, 'blur', () => { sc.timeout(() => { if (document.activeElement !== input) setOpen(false); }, 120); });
    sc.listen(input, 'click', () => { if (!isOpen()) paintList(); });
    sc.listen(input, 'keydown', e => {
      const k = e.key;
      if (k === 'ArrowDown' || k === 'ArrowUp') {
        if (!rows.length) return; e.preventDefault();
        if (!isOpen()) setOpen(true);
        at = k === 'ArrowDown' ? (at + 1) % rows.length : (at <= 0 ? rows.length - 1 : at - 1);
        $$('li.dir', list).forEach((li, i) => { li.classList.toggle('on', i === at); li.setAttribute('aria-selected', String(i === at)); });
        input.setAttribute('aria-activedescendant', ids.row + at); const li = $('#' + ids.row + at, b); if (li && li.scrollIntoView) li.scrollIntoView({ block: 'nearest' });
      } else if (k === 'Enter' && isOpen() && at >= 0) { e.preventDefault(); take(at); }
      else if (k === 'Tab' && !e.shiftKey && isOpen() && complete()) e.preventDefault();
      else if (k === 'Escape' && isOpen()) { e.preventDefault(); e.stopPropagation(); at = -1; setOpen(false); }
    });
    const btn = $('#' + ids.projects, b);
    sc.listen(btn, 'click', () => { const was = pick && isOpen(); pick = !was; at = -1; input.focus(); paintList(); if (was) setOpen(false); });
    sc.listen(list, 'mousedown', e => { e.preventDefault(); });   // the box keeps the focus: a press on a name is a choice, not a blur
    sc.listen(list, 'click', e => { const li = e.target.closest && e.target.closest('[data-i]'); if (li) take(+li.dataset.i); });

    if (text().trim()) ask(text());
    return {
      state: () => (answered && answered.text === text() ? answered.state : null),
      ensure: () => (answered && answered.text === text() ? Promise.resolve(answered.state) : ask(text())),
    };
  };
})(SL);
