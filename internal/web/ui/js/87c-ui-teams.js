/* 87c-ui-teams.js: SL.ui.teams, the team library: the saved teams (loadouts) of a folder's project and of the person (GET
 * /api/teams), a team saved under a name (PUT /api/teams/{name}) or removed (DELETE), and a team shared as the JSON file that
 * --team reads: copied or downloaded, and taken back in from pasted text or a file (POST /api/teams/check). The New session dialog
 * and Team & run open it with the team they show and with what loading a team does there. In a terminal: /team and --team NAME.
 * The helpers that turn a team into a file and back have no DOM and are tested alone. */
(function (SL) {
  'use strict';
  const U = SL.u, { $, $$, esc } = U, ui = SL.ui = SL.ui || {};
  const teams = ui.teams = {};
  const NAME = /^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$/;
  /** the roles that take a model but are never a slot of the team */
  const SERVICE = ['manager', 'mailman', 'compactor', 'judge', 'embedder', 'reranker'];
  teams.service = role => SERVICE.indexOf(role) >= 0;
  /** whether n can name a saved team (letters, digits, '-', '_', '.', at most 64, starting with a letter or a digit) */
  teams.validName = n => NAME.test(String(n || '')) && !/\.json$/.test(String(n));
  /** a name made of text: what a file was called, or a description, cut to what a name may hold ('' when nothing is left) */
  teams.nameOf = text => String(text || '').replace(/\.json$/i, '').trim().replace(/\s+/g, '-').replace(/[^A-Za-z0-9_.-]/g, '').replace(/^[^A-Za-z0-9]+/, '').slice(0, 64);

  /** The team file of a team: slots [{role, model}] in order (role=model, or the role alone when the slot names no model), the models
   *  of the roles that are no slot (a worker role's model goes in its slots), and a description. */
  teams.fileOf = (slots, models, description) => {
    const f = {}; const d = String(description || '').trim(); if (d) f.description = d;
    f.slots = (slots || []).map(s => s.model ? s.role + '=' + s.model : String(s.role));
    const m = {}; Object.keys(models || {}).sort().forEach(r => { if (teams.service(r) && models[r]) m[r] = String(models[r]); });
    if (Object.keys(m).length) f.models = m;
    return f;
  };
  /** A team file as a team: {slots: [{role, model}], models: {role: model}} (model '' for a bare role). */
  teams.teamOf = f => {
    const slots = ((f && f.slots) || []).map(s => { const i = String(s).indexOf('='); return i < 0 ? { role: String(s).trim(), model: '' } : { role: String(s).slice(0, i).trim(), model: String(s).slice(i + 1).trim() }; });
    return { slots, models: Object.assign({}, (f && f.models) || {}) };
  };
  /** The roles of a team file with their numbers, in the order they first appear: [[role, n], ...] */
  teams.counts = f => teams.teamOf(f).slots.reduce((o, s) => { const e = o.find(x => x[0] === s.role); if (e) e[1]++; else o.push([s.role, 1]); return o; }, []);
  /** The models a team file's slots and roles name, each once, in order. */
  teams.models = f => { const t = teams.teamOf(f), out = []; t.slots.map(s => s.model).concat(Object.keys(t.models).map(r => t.models[r])).forEach(m => { if (m && out.indexOf(m) < 0) out.push(m); }); return out; };
  /** One line of what a team file is: its size and its roles ("12 workers: 3 backend, 2 frontend, …"). */
  teams.summary = f => { const n = ((f && f.slots) || []).length; return n + ' worker' + (n === 1 ? '' : 's') + ': ' + teams.counts(f).map(([r, k]) => (k > 1 ? k + ' ' : '') + r).join(', '); };
  /** A team file as it is saved and shared: indented JSON with a final newline. */
  teams.text = f => JSON.stringify(f, null, 2) + '\n';

  /** Hand the person a file: the browser's download of text as name. */
  function download(name, text) {
    try { const a = document.createElement('a'), url = URL.createObjectURL(new Blob([text], { type: 'application/json' })); a.href = url; a.download = name; document.body.appendChild(a); a.click(); a.remove(); setTimeout(() => URL.revokeObjectURL(url), 1000); return true; } catch (e) { return false; }
  }
  teams.download = download;

  const toast = (t, k) => ui.toast(t, k);
  const SCOPE_TXT = { project: 'this project', user: 'just you' };

  /** The library for a running session S (the chat's /team): it saves the team S runs (GET /api/sessions/{id}/team), and a team
   *  loaded starts S again as it after a yes (/team load NAME, as in a terminal). o adds to or replaces what ui.teams.open takes;
   *  o.stageImported(file), when given, is what an imported team that was not saved does (else the person is asked to save it first). */
  /** The team cannot take over the workers at work (the server said why): offer to start again as it with an empty conversation. */
  function freshAsk(S, name, said) {
    ui.confirm({ title: 'Start again empty', text: esc(said.replace(/^team:\s*/, '').replace(/\.?\s*\/team load \S+ --fresh.*$/, '')) + '.<br><br>Start <b>' + esc(S.name) + '</b> again as <b>' + esc(name) + '</b> with an empty conversation and board? Its checkpoints stay.', ok: 'Start fresh as ' + name, danger: true, run: () => {
      const a = SL.act.command('/team load ' + name + ' --fresh', S.id); if (a && a.ok === false) { toast(a.why || 'that cannot be done now', 'warm'); return; }
      if (a && a.done) a.done.then(x => toast(x && x.ok ? 'the team starts again as ' + name + ', empty' : (x && x.message) || 'the team did not start again', x && x.ok ? 'ok' : 'err'));
    } });
  }
  teams.forSession = async function (S, o) {
    const r = await SL.api.get(SL.api.tab(S.id) + '/team'), live = r.ok && r.data ? r.data.team : null;
    return teams.open(Object.assign({ dir: (S.meta && S.meta.cwd) || '', currentWhat: 'the running team', current: () => live && JSON.parse(JSON.stringify(live)), noCurrent: r.message || 'A single agent has no team to save: start a team first.', applyLabel: 'Load', quietApply: true,
      apply: (f, name) => {
        if (!name) { if (o && o.stageImported) o.stageImported(f); else toast('save the imported team to the library first, then load it', 'warm'); return; }
        ui.confirm({ title: 'Start the team again', text: 'Restart <b>' + esc(S.name) + '</b> as the saved team <b>' + esc(name) + '</b> (' + esc(teams.summary(f)) + ')? The conversation and the board carry over; the work goes on.', ok: 'Restart as ' + name, danger: true, run: () => {
          const a = SL.act.command('/team load ' + name, S.id); if (a && a.ok === false) { toast(a.why || 'that cannot be done now', 'warm'); return; }
          if (a && a.done) a.done.then(x => { const said = x && x.ok && x.data && x.data.output ? x.data.output.trim() : ''; if (/^team:.*--fresh/.test(said)) { freshAsk(S, name, said); return; } toast(x && x.ok ? said || 'the team starts again as ' + name : (x && x.message) || 'the team did not start again', !x || !x.ok ? 'err' : /^team:/.test(said) ? 'warm' : 'ok'); });
        } });
      } }, o || {}));
  };
  /**
   * Open the team library. o = {
   *   dir: the folder whose project's teams are listed (the server's folder when empty),
   *   current: () => team file | null, the team the opener shows (Save saves it; null: nothing to save),
   *   currentWhat: what that team is, in a few words ("the team of the dialog"),
   *   apply: (file, name, entry) => void, what Load does with a saved team (its name and list entry) or an imported one (name '', entry
 *     null); null: no Load. applyLabel: its button's text,
   *   quietApply: true when apply says itself what it did,
 *   onClose: (applied) => void, after the library closes, whatever closed it }.
   */
  teams.open = function (o) {
    o = o || {};
    const st = { confirmRm: -1, list: null, project: '', user: '', err: '', busy: false, scope: 'user', name: '', desc: '', replace: '', imported: null, importErr: '', importName: '' };
    const cur = () => { try { return o.current ? o.current() : null; } catch (e) { return null; } };
    const dirQ = () => o.dir ? '?dir=' + encodeURIComponent(o.dir) : '';
    const scopeQ = (e) => 'scope=' + encodeURIComponent(e.scope) + (o.dir ? '&dir=' + encodeURIComponent(o.dir) : '');
    /* what Load does runs once the library has closed (it may open a sheet of its own), before onClose (which may open another) */
    let applied = false, then = null;
    const rec = ui.modal({ title: 'Saved teams', kicker: '--team · /team', desc: 'teams you reuse: save one under a name, load it, or share it as a file', wide: true, color: 'var(--ok)', cls: 'teamlib', focus: '#tlName',
      onClose: () => { if (then) { const f = then; then = null; f(); } if (o.onClose) o.onClose(applied); },
      body: '<div class="tl-list" id="tlList" aria-live="polite"><p class="dim">reading the saved teams…</p></div>' +
        '<div class="g2 tl-forms"><section class="tl-save" aria-labelledby="tlSaveH"><h3 class="lab" id="tlSaveH">Save ' + esc(o.currentWhat || 'this team') + '</h3><div id="tlSave"></div></section>' +
        '<section class="tl-import" aria-labelledby="tlImpH"><h3 class="lab" id="tlImpH">Import a shared team</h3><div id="tlImport"></div></section></div>' +
        '<p class="stubnote" style="margin-top:10px">A saved team is a file: <span class="mono">.sleipnir/teams/NAME.json</span> in a project, or <span class="mono">~/.sleipnir/teams/NAME.json</span> for you alone. In a terminal: <span class="mono">sleipnir chat --team NAME</span>, and <span class="mono">/team save NAME</span> or <span class="mono">/team load NAME</span> in the chat.</p>',
      onMount(b, sc, close) {
        const load = async () => {
          const r = await SL.api.get('/api/teams' + dirQ());
          if (!r.ok) { st.err = r.message || 'the saved teams could not be read'; st.list = []; paintList(); return; }
          st.list = r.data.teams || []; st.project = r.data.project || ''; st.user = r.data.user || ''; st.err = '';
          if (!st.project && st.scope === 'project') st.scope = 'user';
          paintList(); paintSave();
        };
        const roleChips = f => teams.counts(f).map(([r, k]) => { const R = (SL.D.roles || {})[r] || {}; return '<span class="tl-role" style="color:var(--c-' + esc(R.code || 'sc') + ')">' + (k > 1 ? k + '× ' : '') + esc(r) + '</span>'; }).join(' ');
        function paintList() {
          const el = $('#tlList', b), l = st.list || [];
          if (st.list == null) return;
          el.innerHTML = (st.err ? '<p class="err" role="alert">' + esc(st.err) + '</p>' : '') + (l.length ? '<table class="tbl tl-tbl"><thead><tr><th>team</th><th>kept for</th><th class="r">workers</th><th>roles and models</th><th></th></tr></thead><tbody>' + l.map((e, i) => {
            const f = e.team, bad = !f;
            const models = f ? teams.models(f) : [];
            return '<tr data-ti="' + i + '"' + (e.shadowed ? ' class="dim"' : '') + '><td><b class="bright">' + esc(e.name) + '</b>' + (f && f.description ? '<div class="dim tl-desc">' + esc(f.description) + '</div>' : '') + (e.shadowed ? '<div class="dim tl-desc">hidden by the project’s team of the same name</div>' : '') + '</td><td>' + esc(SCOPE_TXT[e.scope] || e.scope) + '</td><td class="r num">' + (f ? f.slots.length : '–') + '</td><td>' + (bad ? '<span class="warm">' + esc(e.error || 'unreadable') + '</span>' : roleChips(f) + (models.length ? '<div class="dim mono tl-models">' + esc(models.join(' · ')) + '</div>' : '')) + '</td>' +
              '<td class="r nowrap">' + (o.apply && !bad && !e.shadowed ? '<button class="btn sm pri" type="button" data-tl="load">' + esc(o.applyLabel || 'Load') + '</button> ' : '') + (bad ? '' : '<button class="btn sm" type="button" data-tl="copy" title="copy the team file, to paste into another Sleipnir">Copy</button> <button class="btn sm" type="button" data-tl="download" title="download ' + esc(e.name) + '.json">Download</button> ') + '<button class="btn sm danger" type="button" data-tl="rm" title="remove ' + esc(e.path) + '">' + (st.confirmRm === i ? 'Remove it?' : 'Remove') + '</button></td></tr>';
          }).join('') + '</tbody></table>' : '<p class="dim tl-empty">No saved team yet' + (st.project ? ' in this project or for you' : '') + '. Save the one you have below, or import one somebody shared.</p>');
        }
        function paintSave() {
          const el = $('#tlSave', b), f = cur();
          if (!f) { el.innerHTML = '<p class="dim">' + esc(o.noCurrent || 'There is no team here to save: a single agent has none.') + '</p>'; return; }
          const seg = '<div class="seg" role="group" aria-label="Kept for"><button type="button" data-tlscope="user" aria-pressed="' + (st.scope === 'user') + '" title="' + esc(st.user || '~/.sleipnir/teams') + '">Just me</button><button type="button" data-tlscope="project" aria-pressed="' + (st.scope === 'project') + '"' + (st.project ? ' title="' + esc(st.project) + '"' : ' disabled title="the folder is not in a project (no .git or .sleipnir above it)"') + '>This project</button></div>';
          el.innerHTML = '<p class="dim tl-cur">' + esc(teams.summary(f)) + '</p>' +
            '<div class="field-row"><input id="tlName" type="text" placeholder="name, e.g. web-crew" aria-label="Team name" spellcheck="false" autocomplete="off" value="' + esc(st.name) + '"></div>' +
            '<div class="field-row"><input id="tlDesc" type="text" placeholder="what it is for (optional)" aria-label="Description" value="' + esc(st.desc) + '"></div>' +
            '<div class="row2 tl-row">' + seg + '<button class="btn pri" type="button" data-tl="save">' + (st.replace && st.replace === st.name ? 'Replace ' + esc(st.name) : 'Save') + '</button></div><div class="nserr err" id="tlSaveErr" role="alert" hidden></div>';
        }
        function paintImport() {
          const el = $('#tlImport', b), f = st.imported;
          el.innerHTML = '<textarea id="tlText" rows="4" spellcheck="false" placeholder="paste a team: {&quot;slots&quot;: [&quot;backend=provider/model&quot;, …]}" aria-label="Team file text"></textarea>' +
            '<div class="row2 tl-row"><label class="btn sm tl-file">Choose a file…<input type="file" id="tlFile" accept=".json,application/json" hidden></label><button class="btn sm" type="button" data-tl="check">Read it</button></div>' +
            (st.importErr ? '<p class="err" role="alert">' + esc(st.importErr) + '</p>' : '') +
            (f ? '<div class="tl-imp boxed"><p>' + roleChips(f) + '</p><p class="dim">' + esc(teams.summary(f)) + (f.description ? ' · ' + esc(f.description) : '') + '</p><div class="row2 tl-row">' + (o.apply ? '<button class="btn sm pri" type="button" data-tl="imp-load">' + esc(o.applyLabel || 'Load') + '</button>' : '') + '<input id="tlImpName" type="text" placeholder="name to save it as" aria-label="Name to save the imported team as" spellcheck="false" value="' + esc(st.importName) + '"><button class="btn sm" type="button" data-tl="imp-save">Save to the library</button></div></div>' : '');
        }
        const readText = async text => {
          st.importErr = ''; st.imported = null;
          if (!String(text || '').trim()) { st.importErr = 'paste a team file, or choose one'; paintImport(); return; }
          const r = await SL.api.post('/api/teams/check', { text: String(text) });
          if (!r.ok) st.importErr = r.message || 'not a team'; else st.imported = r.data.team;
          paintImport();
        };
        const save = async (name, f, scope, overwrite) => {
          const r = await SL.api.put('/api/teams/' + encodeURIComponent(name), { dir: o.dir || '', scope, overwrite: !!overwrite, team: f });
          return r;
        };
        const entry = el => (st.list || [])[+el.closest('[data-ti]').dataset.ti];
        sc.listen(b, 'input', e => {
          const t = e.target;
          if (t.id === 'tlName') { st.name = t.value.trim(); if (st.replace && st.replace !== st.name) { st.replace = ''; const btn = $('[data-tl="save"]', b); if (btn) btn.textContent = 'Save'; } }
          else if (t.id === 'tlDesc') st.desc = t.value;
          else if (t.id === 'tlImpName') st.importName = t.value.trim();
        });
        sc.listen(b, 'change', e => {
          const t = e.target; if (t.id !== 'tlFile' || !t.files || !t.files[0]) return;
          const file = t.files[0]; if (file.size > 64 << 10) { st.importErr = 'a team file holds at most 64 KiB'; paintImport(); return; }
          file.text().then(text => { const area = $('#tlText', b); if (area) area.value = text; if (!st.importName) st.importName = teams.nameOf(file.name); return readText(text); }).then(() => { const n = $('#tlImpName', b); if (n && st.importName) n.value = st.importName; });
        });
        sc.listen(b, 'click', async e => {
          const sb = e.target.closest('[data-tlscope]'); if (sb) { if (sb.disabled) return; st.scope = sb.dataset.tlscope; st.replace = ''; paintSave(); return; }
          const btn = e.target.closest('[data-tl]'); if (!btn || btn.disabled || st.busy) return;
          const k = btn.dataset.tl;
          if (k === 'load') { const en = entry(btn); applied = true; then = () => o.apply(en.team, en.name, en); close(); if (!o.quietApply) toast('loaded the team ' + en.name + ': ' + teams.summary(en.team), 'ok'); return; }
          if (k === 'copy') { const en = entry(btn); U.copy(teams.text(en.team)).then(ok => toast(ok ? 'copied ' + en.name + ': paste it into Import a shared team, or save it as ' + en.name + '.json' : 'copy is not available here: use Download', ok ? 'ok' : 'warm')); return; }
          if (k === 'download') { const en = entry(btn); toast(download(en.name + '.json', teams.text(en.team)) ? 'downloaded ' + en.name + '.json' : 'the download did not start', 'ok'); return; }
          if (k === 'rm') {
            /* a second click removes: the first asks, in the button itself (a confirmation sheet would take the library's place) */
            const en = entry(btn), i = +btn.closest('[data-ti]').dataset.ti;
            if (st.confirmRm !== i) { st.confirmRm = i; paintList(); const again = $('[data-ti="' + i + '"] [data-tl="rm"]', b); if (again) again.focus(); return; }
            st.confirmRm = -1; st.busy = true;
            const r = await SL.api.del('/api/teams/' + encodeURIComponent(en.name) + '?' + scopeQ(en)); st.busy = false;
            if (!r.ok) toast(r.message || 'the team was not removed', 'err'); else toast('removed the team ' + en.name + ' (' + en.path + ')', 'warm');
            load(); return;
          }
          if (k === 'save') {
            const errEl = $('#tlSaveErr', b), say = m => { errEl.hidden = !m; errEl.textContent = m || ''; };
            const f = cur(); if (!f) return;
            if (!teams.validName(st.name)) { say('A name is letters, digits, “-”, “_” and “.”, starting with a letter or a digit.'); $('#tlName', b).focus(); return; }
            if (st.desc.trim()) f.description = st.desc.trim(); else delete f.description;
            st.busy = true; const r = await save(st.name, f, st.scope, st.replace === st.name); st.busy = false;
            if (!r.ok && r.code === 'exists') { st.replace = st.name; paintSave(); say(st.name + ' is saved there already: Replace it, or choose another name.'); return; }
            if (!r.ok) { say(r.message || 'the team was not saved'); return; }
            toast('saved the team ' + st.name + ' for ' + (SCOPE_TXT[st.scope] || st.scope) + ': --team ' + st.name, 'ok');
            st.replace = ''; st.name = ''; st.desc = ''; load(); return;
          }
          if (k === 'check') { readText(($('#tlText', b) || {}).value); return; }
          if (k === 'imp-load' && st.imported) { const f = st.imported; applied = true; then = () => o.apply(f, '', null); close(); if (!o.quietApply) toast('loaded the imported team: ' + teams.summary(st.imported), 'ok'); return; }
          if (k === 'imp-save' && st.imported) {
            if (!teams.validName(st.importName)) { st.importErr = 'give it a name first: letters, digits, “-”, “_” and “.”'; paintImport(); return; }
            st.busy = true; const r = await save(st.importName, st.imported, st.scope, false); st.busy = false;
            if (!r.ok) { st.importErr = r.code === 'exists' ? st.importName + ' is saved already: choose another name, or remove that one first' : (r.message || 'the team was not saved'); paintImport(); return; }
            toast('saved the imported team as ' + st.importName, 'ok'); st.imported = null; st.importName = ''; paintImport(); load();
          }
        });
        paintSave(); paintImport(); load();
      } });
    return rec;
  };
})(window.SL);
