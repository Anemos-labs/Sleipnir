/* 98c-ui-plugins.js: SL.plugins (the plugin catalogue the page is gated by) and the Plugins view.
 *
 * The catalogue comes from the server (GET /api/sessions/{id}/plugins for the active tab, else GET /api/plugins; docs/WEB-API.md). It is
 * loaded at boot, when the active tab changes and after every change made here. SL.plugins.on(id) is true while there is no catalogue,
 * so a server older than the plugin registry leaves every part of the page on. A view whose plugin is off (view:<name>; the Workspace's
 * four tabs are view:workspace; the runner, Doctor and Schedule are view:tools) leaves the rail, the palette, the `g` chords and the
 * Cockpit's card picker, and SL.views.show of it draws a short note with the way back (SL.plugins.placeholder).
 *
 * The view is a page of cards (83b-ui-dash.js, page plugins): the profile and the filter, then one card for each category, each plugin a
 * row with a switch, its state, one line of what it does, what it adds, which layer decided it and a settings form generated from its
 * config schema. A change is sent at once (POST /api/plugins/{id}); what waits for the team to start again is said by the Apply bar
 * above the cards, whose button restarts the session once. SL.views.show('plugins', {focus: id}) brings a plugin's row into view (the
 * Settings switches that are plugins lead here). Under a plugin that is on, "Programs it uses" lists its needs (Blender, gh, a tool
 * server): each found one with where, each missing one with Locate (type the program's path; POST /api/needs/{id}/locate) and one
 * Install button per method for this machine, which shows the exact command in a confirm dialog and runs it only on Run (POST
 * /api/needs/{id}/install); the output shows under the need until it ends (GET /api/needs, polled while one runs). Text from the
 * server is data: it goes through esc(). */
(function (SL) {
  'use strict';
  const U = SL.u, { $, $$, esc } = U, ui = SL.ui = SL.ui || {};
  const arr = x => Array.isArray(x) ? x : [];

  /** The categories in the order the page shows them: [id, title, what is in it]. */
  const CATS = [
    ['tools', 'Tools', 'what the agents can call'],
    ['extensions', 'Extensions', 'skills, commands, agents, hooks and instructions read from files'],
    ['team', 'Team', 'how a team of agents works together'],
    ['interface', 'Interface', 'the views of this page'],
    ['external', 'External', 'installed from outside Sleipnir: off until you turn them on'],
    ['core', 'Core', 'always on: Sleipnir needs them to run'],
  ];
  /** What a category with no plugin at all says: what would appear there and what puts it there. */
  const CAT_EMPTY = {
    external: 'Plugins installed from outside Sleipnir appear here, off until you turn them on: <span class="mono">sleipnir plugins add DIR</span> installs one into ~/.sleipnir/plugins.',
  };
  /** The card of each category (83b-ui-dash.js, page plugins). */
  const cardOf = cat => 'plugins.' + cat;
  /** The plugin of a view that is not view:<its name> ('' for one no plugin owns). */
  const VIEW_PLUGIN = { research: 'research', workspace: 'view:workspace', runner: 'view:tools', doctor: 'view:tools', schedule: 'view:tools', kit: '' };
  /** What a contribution kind is called on the page: [one, many]. */
  const KIND = {
    tool: ['tool', 'tools'], slash: ['command', 'commands'], cli: ['CLI command', 'CLI commands'], skill: ['skill', 'skills'], role: ['role', 'roles'],
    hook: ['hook', 'hooks'], mcp: ['MCP server', 'MCP servers'], prompt: ['prompt', 'prompts'], provider: ['provider', 'providers'],
    'web.view': ['view', 'views'], 'web.card': ['card', 'cards'], 'web.setting': ['setting', 'settings'], 'web.nav': ['menu entry', 'menu entries'],
    'tui.panel': ['terminal panel', 'terminal panels'], event: ['event', 'events'],
  };
  /** Who decided whether a plugin is on, in plain words. */
  const LAYER = { default: 'the built-in default', profile: 'the profile', user: 'your settings', project: 'this project', local: 'local settings', env: 'an environment variable', flag: 'a command-line flag' };
  const FILTERS = [['all', 'All'], ['on', 'On'], ['off', 'Off'], ['experimental', 'Experimental'], ['external', 'External']];
  const SCOPES = [['user', 'Just me'], ['project', 'This project']];

  /* ---------------------------------------------------------------- the catalogue */
  const P = SL.plugins = { cat: null, ver: 0, absent: false, loaded: false };
  let seq = 0;
  /** The plugin that owns a view ('' when none does). */
  P.viewPlugin = name => (VIEW_PLUGIN[name] != null ? VIEW_PLUGIN[name] : 'view:' + name);
  /** The catalogue's entry of a plugin, or null. */
  P.find = id => (P.cat ? arr(P.cat.plugins).find(p => p && p.id === id) || null : null);
  /** Whether a plugin is on: true while there is no catalogue (an older server) and for an id the catalogue does not list. */
  P.on = id => { const p = P.find(id); return !p || !!(p.core || p.enabled); };
  /** Whether a view is on (its plugin is). */
  P.viewOn = name => { const id = P.viewPlugin(name); return !id || P.on(id); };
  /** The plugins whose change waits for the team to start again. */
  P.waiting = () => (P.cat ? arr(P.cat.plugins).filter(p => p && p.state === 'restart') : []);
  /** The session the catalogue is read for: the active tab when it hosts a live session, else none (the configuration's). */
  const liveTab = () => { const S = SL.sessions && SL.sessions.active; return S && !S.readOnly && S.id ? S : null; };
  function changed() { P.ver++; SL.bus.emit('plugins-changed', P.cat); if (SL.loop) SL.loop.dirty = true; }
  /** Take a catalogue (or null: the server has none). */
  function adopt(c) { P.cat = c && Array.isArray(c.plugins) ? c : null; P.absent = !P.cat; P.loaded = true; changed(); }
  /**
   * Fetch the catalogue: the live session's, else the configuration's; a server that has neither (404) has no plugins, and every part
   * of the page stays on. A failure that is not a 404 keeps what the page had. Resolves true when a catalogue arrived.
   */
  P.load = async function () {
    const my = ++seq, S = liveTab();
    let r = await SL.api.plugins(S ? S.id : null);
    if (!r.ok && S && r.status === 404) r = await SL.api.plugins(null);
    if (my !== seq) return false;
    if (r.ok && r.data && Array.isArray(r.data.plugins)) { adopt(r.data); return true; }
    if (r.status === 404) adopt(null);
    return false;
  };
  /** Put a changed entry in the catalogue and note whether something now waits for a restart. */
  function merge(d) {
    if (!P.cat || !d) return; const e = d.plugin, list = arr(P.cat.plugins);
    if (e && e.id) { const i = list.findIndex(p => p.id === e.id); if (i >= 0) list[i] = e; else list.push(e); }
    if (d.restart != null) P.cat.restart = !!d.restart;
    changed();
  }
  /** Change a plugin: body {enabled?, config?, scope?}. The answer's entry replaces the old one, then the catalogue is read again. */
  P.set = async function (id, body) {
    const r = await SL.api.setPlugin(id, body); if (r.ok) { merge(r.data); P.load(); } return r;
  };
  /** Choose a profile for a scope; the answer is the whole catalogue. */
  P.setProfile = async function (profile, scope) {
    const r = await SL.api.setProfile(profile, scope); if (r.ok) { if (r.data && Array.isArray(r.data.plugins)) adopt(r.data); P.load(); } return r;
  };
  /** Take a plugin back to its profile's default in a scope. */
  P.reset = async function (id, scope) {
    const r = await SL.api.resetPlugin(id, scope); if (r.ok) { merge(r.data); P.load(); } return r;
  };

  /** The needs of a plugin, from the catalogue. */
  P.needsOf = id => (P.cat ? arr(P.cat.needs).filter(n => n && n.plugin === id) : []);
  let pollT = 0;
  /** Read the needs again while an install runs, then the whole catalogue once it ends (a required need found changes a row). */
  P.pollNeeds = function () {
    if (pollT || !P.cat || !arr(P.cat.needs).some(n => n.install && n.install.running)) return;
    pollT = setTimeout(async () => {
      pollT = 0; const r = await SL.api.needs(); if (!r.ok || !r.data || !P.cat) return;
      const was = arr(P.cat.needs).filter(n => n.install && n.install.running).map(n => n.id);
      P.cat.needs = arr(r.data.needs); changed();
      const ended = P.cat.needs.filter(n => was.includes(n.id) && n.install && !n.install.running);
      ended.forEach(n => ui.toast(n.install.error ? (n.title || n.id) + ': ' + n.install.error : (n.title || n.id) + (n.found ? ' is ready' : ': the install ended'), n.install.error ? 'err' : 'ok'));
      if (ended.length) P.load(); P.pollNeeds();
    }, 1500);
  };
  /** Put a need's new row in the catalogue. */
  function mergeNeed(row) { if (!P.cat || !row || !row.id) return; const l = P.cat.needs = arr(P.cat.needs), i = l.findIndex(n => n.id === row.id); if (i >= 0) l[i] = row; else l.push(row); changed(); P.pollNeeds(); }
  P.locateNeed = async (id, path) => { const r = await SL.api.locateNeed(id, path); if (r.ok) { mergeNeed(r.data); P.load(); } return r; };
  P.forgetNeed = async id => { const r = await SL.api.forgetNeed(id); if (r.ok) { mergeNeed(r.data); P.load(); } return r; };
  P.installNeed = async (id, method, scope) => { const r = await SL.api.installNeed(id, method, scope); if (r.ok) mergeNeed(r.data); else if (r.code === 'scope_changed') P.load(); return r; };
  SL.bus.on('plugins-changed', () => P.pollNeeds());

  /** The note a view whose plugin is off shows in its place: what is off, a button that turns it on, and the way to the Plugins page. */
  P.placeholder = function (sc, root, name) {
    const id = P.viewPlugin(name), p = P.find(id), def = SL.views.V.reg[name], title = (p && p.title) || (def && def.title) || name;
    root.innerHTML = '<div class="plgoff"><section class="panel"><div class="ph"><h2>' + esc(title) + '</h2><div class="r"><span class="tag">off</span></div></div><div class="setcard"><p class="plgoff-t">This view is turned off. Turn it on in Plugins.</p>' +
      '<p class="stubnote">Its plugin is <span class="mono">' + esc(id) + '</span>' + (p && p.layer ? ', turned off by ' + esc(LAYER[p.layer] || p.layer) : '') + '.</p><div class="row2"><button class="btn sm pri" type="button" data-plg-on>Turn it on</button>' + (name !== 'plugins' ? '<button class="btn sm" type="button" data-plg-go>Open Plugins</button>' : '') + '</div></div></section></div>';
    sc.listen(root, 'click', e => {
      if (e.target.closest('[data-plg-go]')) { SL.views.show('plugins'); return; }
      if (e.target.closest('[data-plg-on]')) P.set(id, { enabled: true, scope: ST.scope }).then(r => { if (!r.ok) fail(r, 'it could not be turned on'); else ui.toast(title + ' is on', 'ok'); });
    });
  };
  /* the view on screen follows its plugin: a view turned off becomes the note, a note whose view came back on becomes the view */
  SL.bus.on('plugins-changed', () => { const c = SL.views.current; if (c && !!c.off !== !P.viewOn(c.name)) SL.views.show(c.name, c.params); });

  /* ---------------------------------------------------------------- pure parts (also read by the tests) */
  /** The category a plugin is listed under: core and external first, else its own. */
  const catOf = p => (p.core ? 'core' : p.external ? 'external' : CATS.some(c => c[0] === p.category) ? p.category : 'external');
  /** "adds: 2 tools fs_read, fs_write · view Files", from contributes ('' when it adds nothing). */
  function addsText(list) {
    const by = {}, order = [];
    arr(list).forEach(c => { if (!c || !c.kind) return; if (!by[c.kind]) { by[c.kind] = []; order.push(c.kind); } if (c.name) by[c.kind].push(String(c.name)); });
    if (!order.length) return '';
    return 'adds: ' + order.map(k => { const n = by[k], w = KIND[k] || [k, k]; const names = n.map(x => k === 'slash' ? (x.charAt(0) === '/' ? x : '/' + x) : k === 'cli' ? 'sleipnir ' + x : x); return (n.length > 1 ? w[1] : w[0]) + (names.length ? ' ' + names.join(', ') : ''); }).join(' · ');
  }
  /** The state badge: [text, tone, explanation]. */
  function badge(p) {
    if (p.core) return ['always on', 'ok', p.reason || 'Sleipnir needs it'];
    switch (p.state) {
      case 'on': return ['on', 'ok', ''];
      case 'pending': return ['pending: ' + (p.reason || 'waits for another plugin'), 'warm', p.reason || ''];
      case 'error': return ['error', 'err', p.reason || 'it failed to load'];
      case 'restart': return ['applies on restart', 'warm', p.reason || 'applies when the team starts again'];
      default: return [p.enabled ? 'on' : 'off', p.enabled ? 'ok' : '', ''];
    }
  }
  /** Whether a plugin passes the filter chip and every word of the search (title, id, description, category, what it adds). */
  function fits(p, st) {
    const f = st.f || 'all';
    if (f === 'on' && !(p.enabled || p.core)) return false; if (f === 'off' && (p.enabled || p.core)) return false;
    if (f === 'experimental' && !p.experimental) return false; if (f === 'external' && !p.external) return false;
    const words = String(st.q || '').toLowerCase().split(/\s+/).filter(Boolean); if (!words.length) return true;
    const hay = [p.id, p.title, p.description, p.category, catOf(p), addsText(p.contributes), p.state, p.reason].join(' ').toLowerCase();
    return words.every(w => hay.includes(w));
  }
  /** The groups the page draws: [{id, title, desc, rows, on, all}] for each category that has a row after the filters. */
  function groups(cat, st, every) {
    const all = arr(cat && cat.plugins).filter(p => p && p.id);
    return CATS.map(([id, title, desc]) => { const mine = all.filter(p => catOf(p) === id); return { id, title, desc, rows: mine.filter(p => fits(p, st)), on: mine.filter(p => p.enabled || p.core).length, all: mine.length }; }).filter(g => every || g.rows.length);
  }
  /** An id as a part of an element id. */
  const key = id => String(id).replace(/[^A-Za-z0-9_-]/g, '_');
  /** The fields of a plugin's config schema: [{name, type, title, desc, enum, min, max, def}] for string, number, integer, boolean and enum. */
  function fields(p) {
    const props = p && p.config_schema && p.config_schema.properties; if (!props || typeof props !== 'object') return [];
    return Object.keys(props).map(n => { const s = props[n] || {}, en = Array.isArray(s.enum) ? s.enum : null, t = en ? 'enum' : s.type === 'integer' ? 'number' : s.type; return { name: n, type: t, int: s.type === 'integer', title: s.title || n, desc: s.description || '', enum: en, min: s.minimum, max: s.maximum, def: s.default }; }).filter(f => f.type === 'string' || f.type === 'number' || f.type === 'boolean' || f.type === 'enum');
  }
  /** The value a field shows: what is being typed, else the plugin's setting, else the schema's default. */
  const valueOf = (p, f, st) => { const d = (st.draft || {})[p.id]; if (d && f.name in d) return d[f.name]; const c = p.config || {}; return f.name in c ? c[f.name] : f.def; };
  function formHtml(p, st) {
    const fs = fields(p), k = key(p.id), where = st.scope === 'project' ? 'this project' : 'just you';
    return '<form class="plg-form" id="pform-' + k + '" data-form="' + esc(p.id) + '" aria-label="' + esc((p.title || p.id) + ' settings') + '">' + fs.map(f => {
      const id = 'pf-' + k + '-' + key(f.name), v = valueOf(p, f, st), hint = f.desc ? '<small class="hint">' + esc(f.desc) + '</small>' : '';
      let c;
      if (f.type === 'boolean') c = '<label class="tgl"><input type="checkbox" role="switch" id="' + id + '" data-fk="' + esc(f.name) + '"' + (v ? ' checked' : '') + '><span class="trk"></span></label>';
      else if (f.type === 'enum') c = '<select id="' + id + '" data-fk="' + esc(f.name) + '">' + (v == null ? '<option value="">(not set)</option>' : '') + f.enum.map(o => '<option value="' + esc(o) + '"' + (String(o) === String(v) ? ' selected' : '') + '>' + esc(o) + '</option>').join('') + '</select>';
      else c = '<input type="' + (f.type === 'number' ? 'number' : 'text') + '" id="' + id + '" data-fk="' + esc(f.name) + '" value="' + esc(v == null ? '' : v) + '"' + (f.min != null ? ' min="' + esc(f.min) + '"' : '') + (f.max != null ? ' max="' + esc(f.max) + '"' : '') + (f.int ? ' step="1"' : '') + ' autocomplete="off">';
      return '<div class="fld"><label for="' + id + '">' + esc(f.title) + '</label><div class="fc">' + c + hint + '</div></div>';
    }).join('') + '<div class="row2"><button class="btn sm pri" type="submit">Save settings</button><span class="dim">saved for ' + where + '</span></div></form>';
  }
  /** How a found need was found, in words. */
  const VIA = { located: 'located by you', path: 'on PATH', default: 'in its usual place', config: 'in your tool servers' };
  /** One command as one line, quoted as a shell would need it. */
  const cmdText = argv => arr(argv).map(a => /[\s"'$`\\]/.test(a) ? "'" + String(a).replace(/'/g, "'\\''") + "'" : String(a)).join(' ');
  /** A method's commands, one line each: the variables it works out (NAME=$(command)), its setup steps, then its own. */
  const argvText = m => Object.keys(m.env || {}).sort().map(k => k + '=$(' + cmdText(m.env[k]) + ')').concat(arr(m.setup).concat([m.argv]).map(cmdText)).filter(Boolean).join('\n');
  /** "Programs it uses": one line per need of a plugin that is on, with Locate and Install for a missing one ('' when it has none). */
  function needsHtml(p, st) {
    const ns = P.needsOf(p.id); if (!ns.length || !(p.enabled || p.core)) return '';
    return '<div class="plg-needs"><p class="plg-nh">Programs it uses</p><ul>' + ns.map(n => {
      const k = key(n.id), job = n.install, run = !!(job && job.running), loc = !!(st.locate || {})[n.id];
      const what = n.found ? '<span class="tag ok">found</span> <span class="dim">' + esc(VIA[n.via] || n.via || '') + (n.path ? ': </span><span class="mono">' + esc(n.path) + '</span>' : '</span>') + (n.version ? ' <span class="dim">(' + esc(n.version) + ')</span>' : '')
        : '<span class="tag ' + (n.required ? 'warm' : '') + '">' + (n.required ? 'missing: needed' : 'not found') + '</span>';
      const acts = run ? '<span class="dim">installing…</span>' : (n.found ? (n.via === 'located' ? '<button class="btn sm" type="button" data-nforget="' + esc(n.id) + '">Forget</button>' : '')
        : (n.server ? '' : '<button class="btn sm" type="button" data-nloc="' + esc(n.id) + '" aria-expanded="' + loc + '">Locate…</button>') +
          arr(n.methods).map(m => '<button class="btn sm' + (m.available ? ' pri' : '') + '" type="button" data-ninst="' + esc(n.id) + '" data-m="' + esc(m.id) + '"' + (m.available ? '' : ' disabled title="' + esc(m.reason || '') + '"') + '>' + esc(n.server ? m.title : 'Install with ' + m.title) + '</button>').join(''));
      const form = loc && !n.found && !n.server ? '<form class="plg-loc" data-nlocf="' + esc(n.id) + '"><label for="nl-' + k + '">Full path of ' + esc(n.title || n.id) + '</label><input id="nl-' + k + '" type="text" autocomplete="off" spellcheck="false" placeholder="' + esc('/path/to/' + n.id) + '"><button class="btn sm pri" type="submit">Use this file</button></form>' : '';
      const out = job && (job.output || job.error) ? '<pre class="plg-out" aria-live="polite">' + esc(String(job.output || '').slice(-4000)) + (job.error ? '\n' + esc(job.error) : '') + '</pre>' : '';
      const help = !n.found && arr(n.methods).every(m => !m.available) && !run ? '<small class="hint">' + (arr(n.methods).length ? esc(arr(n.methods).map(m => m.reason).filter(Boolean)[0] || '') + '. ' : '') + (n.homepage ? 'Get it from <span class="mono">' + esc(n.homepage) + '</span>' + (n.server ? '' : ', then Locate it') + '.' : '') + '</small>' : '';
      return '<li class="plg-need" data-need="' + esc(n.id) + '"><div><b>' + esc(n.title || n.id) + '</b> ' + what + '<p class="plg-nd">' + esc(n.description || '') + '</p>' + help + '</div><div class="plg-a">' + acts + '</div>' + form + out + '</li>';
    }).join('') + '</ul></div>';
  }
  /** One plugin's row. */
  function rowHtml(p, st) {
    const id = String(p.id), k = key(id), title = p.title || id, b = badge(p), busy = !!(st.busy || {})[id], fs = fields(p), open = !!(st.open || {})[id];
    const on = !!(p.enabled || p.core), prof = P.cat && arr(P.cat.profiles).find(x => x.id === P.cat.profile);
    const tip = p.core ? 'Always on: ' + b[2] : (on ? 'Turn ' + title + ' off' : 'Turn ' + title + ' on');
    const sw = '<label class="tgl plg-sw" title="' + esc(tip) + '"><input type="checkbox" role="switch" id="psw-' + k + '" data-sw="' + esc(id) + '" aria-label="' + esc(title) + '"' + (on ? ' checked' : '') + (p.core || busy ? ' disabled' : '') + ' aria-describedby="pd-' + k + '"><span class="trk"></span></label>';
    const tags = '<span class="tag ' + b[1] + '"' + (b[2] ? ' title="' + esc(b[2]) + '"' : '') + '>' + esc(b[0]) + '</span>' + (p.experimental ? '<span class="tag fe">experimental</span>' : '') + (p.external ? '<span class="tag">external</span>' : '');
    const adds = addsText(p.contributes), by = p.layer && !p.core ? 'set by ' + (LAYER[p.layer] || p.layer) + (p.layer === 'profile' && prof ? ' (' + prof.title + ')' : '') : '', needs = arr(p.requires).length ? 'needs ' + arr(p.requires).join(', ') : '';
    const meta = [adds, needs, by].filter(Boolean).map(esc).join(' <span class="sep">·</span> ');
    /* the badge says on, off, pending (with what it waits for) and restart; an error's message and a core plugin's reason need a line */
    const why = p.core ? ['', 'always on: ' + b[2]] : p.state === 'error' ? ['err', p.reason || 'it failed to load'] : p.unavailable ? ['warm', p.unavailable] : null;
    const acts = (fs.length ? '<button class="btn sm" type="button" data-open="' + esc(id) + '" aria-expanded="' + open + '" aria-controls="pform-' + k + '">Settings</button>' : '') + (!p.core && (p.layer === 'user' || p.layer === 'project') ? '<button class="btn sm" type="button" data-reset="' + esc(id) + '" title="back to the profile\'s default">Reset</button>' : '');
    return '<li class="plg' + (on ? '' : ' off') + (p.core ? ' core' : '') + '" data-plg="' + esc(id) + '"><div class="plg-c">' + sw + '</div><div class="plg-main"><div class="plg-h"><b>' + esc(title) + '</b><span class="pid">' + esc(id) + '</span>' + tags + '</div>' +
      '<p class="plg-d" id="pd-' + k + '">' + esc(p.description || '') + '</p>' + (meta ? '<p class="plg-m">' + meta + '</p>' : '') + (why ? '<p class="plg-why ' + why[0] + '">' + esc(why[1]) + '</p>' : '') + needsHtml(p, st) + '</div>' +
      (acts ? '<div class="plg-a">' + acts + '</div>' : '') + (fs.length && open ? formHtml(p, st) : '') + '</li>';
  }
  /** What the page says while there is no catalogue: it is loading, or the server has none. */
  const noCatalogue = () => '<p class="stubnote">' + (P.absent ? 'This server has no plugin list: it is older than Sleipnir 0.3, and every part of it is on.' : 'Loading the plugins… Each one appears here with a switch, grouped by what it is for.') + '</p>';
  /** One category's card: its plugins after the filter, or what will appear there. */
  function groupHtml(g, st) {
    const body = g.rows.length ? '<ul class="plg-list">' + g.rows.map(p => rowHtml(p, st)).join('') + '</ul>'
      : '<p class="stubnote plg-none">' + (g.all ? 'No plugin of this group matches' + (st.q ? ' “' + esc(st.q) + '”' : ' this filter') + ': ' + g.all + ' hidden by the filter above.' : CAT_EMPTY[g.id] || 'No plugin in this group.') + '</p>';
    return '<section class="panel plg-grp" data-cat="' + g.id + '" aria-labelledby="pg-' + g.id + '"><div class="ph"><h2 id="pg-' + g.id + '">' + esc(g.title) + '</h2><div class="r"><span class="plg-gd">' + esc(g.desc) + '</span>' + (g.all ? '<span><b>' + g.on + '</b> of ' + g.all + ' on</span>' : '') + '</div></div>' + body + '</section>';
  }
  /** The markup of each category's card, by card id. */
  function cardsHtml(cat, st) {
    const out = {};
    if (!cat) { CATS.forEach(([id, title]) => { out[cardOf(id)] = '<section class="panel plg-grp plg-empty" data-cat="' + id + '"><div class="ph"><h2>' + esc(title) + '</h2></div>' + (id === CATS[0][0] ? noCatalogue() : '<p class="stubnote plg-none">' + (P.absent ? 'No plugin list on this server.' : 'Loading…') + '</p>') + '</section>'; }); return out; }
    groups(cat, st, true).forEach(g => { out[cardOf(g.id)] = groupHtml(g, st); });
    return out;
  }
  /** Every group that has a row after the filter, or what to say when there is nothing to list (one string; the tests read it). */
  function listHtml(cat, st) {
    if (!cat) return '<section class="panel"><div class="setcard plg-empty">' + noCatalogue() + '</div></section>';
    const gs = groups(cat, st);
    if (!gs.length) return '<p class="stubnote plg-none">No plugin matches' + (st.q ? ' “' + esc(st.q) + '”' : ' this filter') + '.</p>';
    return gs.map(g => groupHtml(g, st)).join('');
  }
  /** The Apply bar: how many changes wait for the team to start again, and the button that starts it again ('' when none wait). */
  function applyHtml(cat, S) {
    const w = P.waiting(); if (!cat || (!cat.restart && !w.length)) return '';
    const n = Math.max(1, w.length), names = w.map(p => esc(p.title || p.id)).join(', ');
    return '<div class="pendbar" role="status"><span class="pb-t"><b>' + n + ' plugin change' + (n === 1 ? '' : 's') + '</b> ' + (n === 1 ? 'waits' : 'wait') + ' for the team to start again' + (names ? ': ' + names : '') + '.' + (S ? ' The conversation carries over.' : '') + '</span>' +
      '<span class="pb-a">' + (S ? '<button class="btn sm pri" type="button" data-do="apply">Apply: start the team again</button>' : '<span class="dim">the next session you start has them</span>') + '</span></div>';
  }
  /** The view's state, kept while the page is open: search, filter chip, scope, open forms, values being typed, requests in flight. */
  const ST = { q: '', f: 'all', scope: 'user', open: {}, draft: {}, busy: {}, locate: {} };
  const fail = (r, what) => { if (r && r.declined) return; ui.toast((r && r.message) || what, 'err'); };
  Object.assign(P, { addsText, badge, fits, groups, fields, needsHtml, argvText, rowHtml, formHtml, listHtml, groupHtml, cardsHtml, applyHtml, CATS, CAT_EMPTY, LAYER, KIND, state: ST });

  /* ---------------------------------------------------------------- the view */
  /** The profile-and-filter card: the profile (what it means in a line), where a change is saved, the search and the filter chips. */
  const toolsHtml = () => '<section class="panel plg-tools" aria-label="Profile and where changes are saved"><div class="ph"><h2>Profile and filter</h2></div>' +
    '<div class="plg-row"><label class="plg-ctl" for="pprof"><span class="lab">Profile</span><select id="pprof"></select></label><span class="plg-pd"></span></div>' +
    '<p class="plg-help">A profile is a starting set of plugins (standard, minimal, team, headless); a switch below changes one plugin on top of it.</p>' +
    '<div class="plg-row"><span class="plg-ctl"><span class="lab" id="pscl">Save changes for</span><span class="seg" role="group" aria-labelledby="pscl">' + SCOPES.map(([v, t]) => '<button type="button" data-scope="' + v + '" aria-pressed="false">' + t + '</button>').join('') + '</span></span>' +
    '<input id="pq" type="search" placeholder="filter: a name or what it adds" aria-label="Filter plugins" autocomplete="off"><span class="seg" role="group" aria-label="Show">' + FILTERS.map(([v, t]) => '<button type="button" data-f="' + v + '" aria-pressed="false">' + t + '</button>').join('') + '</span></div></section>';
  function mount(sc, root, params, S0) {
    const st = ST;
    /* a page of cards: the heading and the Apply bar above them; drawn as one card of another page (params.__only), only that card */
    const DP = ui.dash.withHead(sc, root, S0 || (SL.sessions && SL.sessions.active), 'plugins', params, '<div class="plghead"><header class="sethead"><h1>Plugins</h1><p>Sleipnir is made of plugins: its tools, the team’s helpers and the views of this page. Turn one on or off, or change its settings: a change is saved at once, and a running team takes it when it starts again. <span class="dim">In a terminal: <span class="mono">sleipnir plugins</span> or <span class="mono">/plugins</span>.</span></p></header><div class="plg-apply"></div></div>');
    DP.fill({ 'plugins.profile': toolsHtml() });
    const bar = $('.plg-apply', root), groupsEl = root, sel = $('#pprof', root), pd = $('.plg-pd', root), q = $('#pq', root);
    if (q) q.value = st.q;
    function paint() {
      const cat = P.cat, S = liveTab();
      if (bar) { const bh = applyHtml(cat, S); if (bar._h !== bh) { bar._h = bh; bar.innerHTML = bh; } }
      if (sel) {
        const profs = arr(cat && cat.profiles), oh = profs.length ? profs.map(p => '<option value="' + esc(p.id) + '"' + (p.id === cat.profile ? ' selected' : '') + '>' + esc(p.title || p.id) + '</option>').join('') : '<option value="">' + (cat ? esc(cat.profile || 'standard') : '(none)') + '</option>';
        if (sel._h !== oh) { sel._h = oh; sel.innerHTML = oh; } sel.disabled = !profs.length; if (cat && cat.profile) sel.value = cat.profile;
        const cur = profs.find(p => p.id === (cat && cat.profile)); pd.textContent = cur && cur.description ? cur.description : '';
      }
      $$('[data-scope]', root).forEach(b => b.setAttribute('aria-pressed', String(b.dataset.scope === st.scope)));
      $$('[data-f]', root).forEach(b => b.setAttribute('aria-pressed', String(b.dataset.f === st.f)));
      const map = cardsHtml(cat, st), a = document.activeElement, inCard = a && root.contains(a) && a.closest && a.closest('.plg-grp');
      const fid = inCard ? (a.id ? '#' + CSS.escape(a.id) : a.dataset && a.dataset.open ? '[data-open="' + CSS.escape(a.dataset.open) + '"]' : a.dataset && a.dataset.reset ? '[data-reset="' + CSS.escape(a.dataset.reset) + '"]' : '') : '';
      DP.fill(map);
      if (fid && !root.contains(a)) { const n = root.querySelector(fid); if (n) n.focus({ preventScroll: true }); }
      if (st.focus && cat) { const id = st.focus; st.focus = ''; setTimeout(() => { if (!sc.alive) return; const r = root.querySelector('[data-plg="' + CSS.escape(id) + '"]'); if (r) { r.classList.add('plg-hl'); if (r.scrollIntoView) r.scrollIntoView({ block: 'center' }); const sw = r.querySelector('[data-sw]'); if (sw) sw.focus({ preventScroll: true }); } }, 0); }
    }
    sc.on('plugins-changed', paint);
    sc.on('activated', paint);
    if (q) sc.listen(q, 'input', () => { st.q = q.value; paint(); });
    if (sel) sc.listen(sel, 'change', () => {
      const v = sel.value; if (!v || (P.cat && v === P.cat.profile)) return;
      P.setProfile(v, st.scope).then(r => { if (!r.ok) { fail(r, 'the profile was not changed'); paint(); return; } const pr = arr(P.cat && P.cat.profiles).find(x => x.id === v); ui.toast('profile: ' + ((pr && pr.title) || v) + (st.scope === 'project' ? ' for this project' : ' for you'), 'ok'); });
    });
    sc.listen(root, 'click', e => {
      const t = e.target;
      const sp = t.closest('[data-scope]'); if (sp) { st.scope = sp.dataset.scope; paint(); return; }
      const fb = t.closest('[data-f]'); if (fb) { st.f = fb.dataset.f; paint(); return; }
      const op = t.closest('[data-open]'); if (op) { const id = op.dataset.open; st.open[id] = !st.open[id]; paint(); if (st.open[id]) { const f = root.querySelector('#pform-' + key(id) + ' [data-fk]'); if (f) f.focus(); } return; }
      const rs = t.closest('[data-reset]'); if (rs) { const id = rs.dataset.reset, p = P.find(id); resetIt(id, p && p.layer === 'project' ? 'project' : 'user'); return; }
      const nl = t.closest('[data-nloc]'); if (nl) { const id = nl.dataset.nloc; st.locate[id] = !st.locate[id]; paint(); const f = root.querySelector('#nl-' + key(id)); if (f) f.focus(); return; }
      const nf = t.closest('[data-nforget]'); if (nf) { const id = nf.dataset.nforget; P.forgetNeed(id).then(r => { if (!r.ok) fail(r, 'it was not forgotten'); }); return; }
      const ni = t.closest('[data-ninst]'); if (ni) { install(ni.dataset.ninst, ni.dataset.m); return; }
      if (t.closest('[data-do="apply"]')) apply();
    });
    sc.listen(root, 'change', e => { const t = e.target; if (t.dataset && t.dataset.sw) toggle(t.dataset.sw, !!t.checked); });
    sc.listen(root, 'input', e => { const t = e.target, f = t.closest && t.closest('[data-form]'); if (f && t.dataset.fk) { const d = st.draft[f.dataset.form] = st.draft[f.dataset.form] || {}; d[t.dataset.fk] = t.type === 'checkbox' ? !!t.checked : t.value; } });
    sc.listen(root, 'submit', e => {
      const lf = e.target.closest && e.target.closest('[data-nlocf]');
      if (lf) { e.preventDefault(); const id = lf.dataset.nlocf, v = String(($('input', lf) || {}).value || '').trim(); if (!v) return; P.locateNeed(id, v).then(r => { if (!r.ok) { fail(r, 'that file was not taken'); return; } delete st.locate[id]; ui.toast(((r.data && r.data.title) || id) + ' found', 'ok'); }); return; }
      const f = e.target.closest && e.target.closest('[data-form]'); if (!f) return; e.preventDefault(); save(f.dataset.form, f);
    });

    /** Turn a plugin on or off in the chosen scope. */
    function toggle(id, on) {
      const p = P.find(id); if (!p || p.core || st.busy[id]) { paint(); return; }
      st.busy[id] = true; paint();
      P.set(id, { enabled: on, scope: st.scope }).then(r => {
        delete st.busy[id];
        if (!r.ok) { fail(r, (p.title || id) + ' was not changed'); const c = DP.slot(cardOf(catOf(p))); if (c) c._h = ''; paint(); return; }
        const now = (r.data && r.data.plugin) || {}, wait = now.state === 'restart' || (r.data && r.data.restart);
        ui.toast((p.title || id) + ': ' + (on ? 'on' : 'off') + (st.scope === 'project' ? ' for this project' : ' for you') + (wait ? '. It applies when the team starts again' : ''), on ? 'ok' : 'warm');
        paint();
      });
    }
    /** Install a need with one of its methods, after the person has read the exact command and pressed Run. */
    function install(id, mid) {
      const n = arr(P.cat && P.cat.needs).find(x => x.id === id), m = n && arr(n.methods).find(x => x.id === mid); if (!m || !m.available) return;
      const cmd = argvText(m), detail = (cmd ? '<pre class="mono plg-cmd">' + esc(cmd) + '</pre>' : '') + (m.connect ? '<p>It then ' + esc(m.connect) + '; tool servers start with the next session.</p>' : '') + (m.note ? '<p class="dim">' + esc(m.note) + '</p>' : '');
      ui.confirm({ title: (n.server ? '' : 'Install ') + (n.title || id), kicker: 'runs on this machine', text: cmd ? 'Sleipnir will run this command on this machine, as you:' : 'Sleipnir will change your configuration:', detail, ok: cmd ? 'Run it' : 'Connect',
        run: () => P.installNeed(id, mid, m.scope).then(r => { if (!r.ok) fail(r, 'the install did not start'); }) });
    }
    /** Send a plugin's settings form. */
    function save(id, form) {
      const p = P.find(id); if (!p) return; const out = {};
      for (const f of fields(p)) {
        const el = form.querySelector('[data-fk="' + CSS.escape(f.name) + '"]'); if (!el) continue;
        if (f.type === 'boolean') out[f.name] = !!el.checked;
        else if (f.type === 'number') { const s = String(el.value).trim(); if (s === '') continue; const n = Number(s); if (!isFinite(n) || (f.int && !Number.isInteger(n))) { ui.toast(f.title + ' needs ' + (f.int ? 'a whole number' : 'a number'), 'warm'); el.focus(); return; } out[f.name] = n; }
        else if (f.type === 'enum') { if (el.value !== '') out[f.name] = el.value; }
        else out[f.name] = el.value;
      }
      P.set(id, { config: out, scope: st.scope }).then(r => { if (!r.ok) { fail(r, 'the settings were not saved'); return; } delete st.draft[id]; ui.toast((p.title || id) + ': settings saved' + (st.scope === 'project' ? ' for this project' : ' for you'), 'ok'); });
    }
    function resetIt(id, scope) {
      const p = P.find(id);
      P.reset(id, scope).then(r => { if (!r.ok) { fail(r, 'it was not reset'); return; } delete st.draft[id]; ui.toast((p ? p.title || id : id) + ': back to the profile\'s default', 'ok'); });
    }
    /** Start the team again once, so that it runs with the plugins as they are now. */
    function apply() {
      const S = liveTab(); if (!S) return; const n = Math.max(1, P.waiting().length);
      ui.confirm({ title: 'Start the team again', text: 'Restart <b>' + esc(S.name) + '</b> so that ' + (n === 1 ? 'the plugin change applies' : 'the ' + n + ' plugin changes apply') + '? The current run is closed (its checkpoints stay) and the conversation carries over.', ok: 'Restart', danger: true,
        run: () => {
          const r = SL.act.restart ? SL.act.restart([]) : SL.act.restartTeam({ force: true });
          if (r && r.ok === false) { ui.toast(r.why || 'that cannot be done now', 'warm'); return; }
          (r && r.done ? r.done : Promise.resolve({ ok: true })).then(x => { if (x && x.ok) { ui.toast('the team starts again with the plugins as they are now', 'ok'); P.load(); } });
        } });
    }
    st.focus = (params && params.focus) || '';
    paint(); P.load();
  }
  SL.views.register({ name: 'plugins', title: 'Plugins', mount });
})(SL);
