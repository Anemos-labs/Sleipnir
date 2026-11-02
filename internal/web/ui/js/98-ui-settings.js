/* 98-ui-settings.js: Settings, eight pages in three groups. A page reads the session or the shared state and changes it only through SL.act
 * (or the server's own routes where the answer is the point), so a change made here shows everywhere at once: the mode chip, the HUD, the
 * footer, the stalls, the Radio rail.
 *   this session   Models & roles · Team & run · Budget · Permissions & trust
 *   every session  Connections (providers and tool servers) · Skills & extensions · Configuration   (and a way to Plugins)
 *   this browser   Appearance
 * The pages of an earlier version (roles, trust, mcp) are parts of these: ui.settingsPage(<old id>) opens the page that has them and
 * brings their first card into view (ALIAS). A switch here that a plugin also owns (the mailman, docs on autopilot, manager approves,
 * push progress, built-in skills) says that it is the plugin and leads to it on the Plugins page.
 * Text from files, servers, mail and models is data: it goes through esc(). A provider key typed in the Providers page is sent
 * once and never comes back: the page only learns which providers are connected. Nothing a page shows or keeps is a secret. */
(function (SL) {
  'use strict';
  const U = SL.u, { $, $$, esc, mk, fmtK, fmtUsd, fmtN, agCol } = U, D = SL.D, G = SL.G, X = D.extra, calc = SL.calc, ui = SL.ui = SL.ui || {}, TK = SL.toolkit = SL.toolkit || {};
  const net = TK.net, arr = x => Array.isArray(x) ? x : [];
  /** The pages: [id, title, group, what it is for (one sentence), the parts it draws (the pages of an earlier version, which name the
   *  data it loads), the few words the list of pages shows under its title]. */
  const PAGES = [
    ['models', 'Models & roles', 'session', 'Choose the model the manager and each role run on, and how hard they think.', ['models', 'roles'], 'each role’s model, effort'],
    ['run', 'Team & run', 'session', 'How many workers the team has and how they work: separate checkouts, the check before a merge, the helpers.', ['run'], 'workers, checks, helpers'],
    ['budget', 'Budget', 'session', 'What this session has spent, by agent, and the most it may spend.', ['budget'], 'spend and its limit'],
    ['permissions', 'Permissions & trust', 'session', 'What the agents may do without asking you, and whether this project’s own files are used.', ['permissions', 'trust'], 'what runs without asking'],
    ['providers', 'Connections', 'shared', 'Where the models are reached and whether you are signed in, and the tool servers (MCP) the agents can call.', ['providers', 'mcp'], 'providers, tool servers'],
    ['skills', 'Skills & extensions', 'shared', 'The skills the model can load, your own commands and the hooks around the tools, read from files.', ['skills'], 'skills, commands, hooks'],
    ['config', 'Configuration', 'shared', 'Which configuration file set each value, and what the configuration warns about.', ['config'], 'where each value came from'],
    ['look', 'Appearance', 'browser', 'How this page moves and is laid out, its notifications and its keys.', ['look'], 'motion, layout, keys'],
  ];
  /** The page ids of an earlier version: old id -> [the page that has it now, the card to bring into view]. */
  const ALIAS = { roles: ['models', 'settings.roles.roles'], trust: ['permissions', 'settings.trust.project'], mcp: ['providers', 'settings.mcp.servers'] };
  /** The page an id opens (an old id opens the page it joined; an unknown id the first page). */
  const pageOf = id => (ALIAS[id] ? ALIAS[id][0] : PAGES.some(p => p[0] === id) ? id : 'models');
  const GROUPS = { session: 'this session', shared: 'every session', browser: 'this browser' };
  /** The plugins a control of Settings also is: the control says so and leads to it (plugin id, its title when there is no catalogue). */
  const PLUGIN_OF = { mailman: ['mailman', 'Mailman'], autoDocs: ['auto-docs', 'Docs on autopilot'], managerApproves: ['manager-approves', 'Manager approves'], pushProgress: ['push-progress', 'Push progress'], builtinSkills: ['builtin-skills', 'Built-in skills'] };
  /** "the Mailman plugin (on for every session)" and the button that opens it on the Plugins page. */
  function pluginNote(k) {
    const [id, t] = PLUGIN_OF[k], p = SL.plugins && SL.plugins.find ? SL.plugins.find(id) : null, title = (p && p.title) || t;
    const st = p ? (p.core || p.enabled ? 'on' : 'off') + ' by default' : '';
    return '<span class="plgref">' + tag('plugin', 'fe', 'this switch is the ' + title + ' plugin') + ' <span class="dim">the <b>' + esc(title) + '</b> plugin' + (st ? ', ' + st : '') + '</span> <button class="btn sm lnk" type="button" data-do="open-plugins" data-plg="' + esc(id) + '">Plugins ›</button></span>';
  }
  const EFFORTS = ['default', 'none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'];
  const efforts = () => D.efforts && D.efforts.length ? D.efforts : EFFORTS;
  const tag = (t, c, title) => '<span class="tag ' + (c || '') + '"' + (title ? ' title="' + esc(title) + '"' : '') + '>' + esc(t) + '</span>';
  const seg = (name, list, cur, label) => '<div class="seg" role="group" aria-label="' + esc(label || name) + '">' + list.map(v => { const val = Array.isArray(v) ? v[0] : v, lab = Array.isArray(v) ? v[1] : v; return '<button type="button" data-seg="' + name + '" data-v="' + esc(val) + '" aria-pressed="' + (String(val) === String(cur)) + '">' + esc(lab) + '</button>'; }).join('') + '</div>';
  /** A row of a card: its label (and the command-line flag it stands for, as secondary text), what it does in a line, and the control. */
  const row = (l, inner, hint, id, flag) => '<div class="setrow"><div class="sl"><label' + (id ? ' for="' + id + '"' : '') + '>' + l + '</label>' + (flag ? '<span class="setflag mono">' + esc(flag) + '</span>' : '') + (hint ? '<small>' + hint + '</small>' : '') + '</div><div class="sc2">' + inner + '</div></div>';
  /** An empty state: what will appear here, and what starts it. */
  const empty = (what, how) => '<p class="stubnote setempty">' + what + (how ? ' <span class="dim">' + how + '</span>' : '') + '</p>';
  const card = (id, title, right, inner, cls) => '<section class="panel setpanel ' + (cls || '') + '" data-slot="' + id + '"><div class="ph"><h2>' + title + '</h2><div class="r">' + (right || '') + '</div></div><div class="setcard">' + inner + '</div></section>';
  const cli = (path, label, attr) => '<button class="btn sm" type="button" data-cli="' + esc(path) + '"' + (attr || '') + ' title="run it in the command runner">Run as CLI <span class="mono">' + esc(label || path) + '</span></button>';
  const roleCol = r => esc('var(--c-' + (D.roles[r] ? D.roles[r].code : 'sc') + ')');
  const agColor = id => esc(agCol(id));
  /** an agent's model as the catalogue names it: the roster names a model without its provider (deepseek/v4 for heimdallm/deepseek/v4),
   *  so a model that is no ref of models (default: the catalogue) takes the one ref that ends with it; anything else is kept as it is */
  const refOf = (m, models) => { m = String(m || ''); const list = arr(models || D.models); if (!m || list.some(mm => mm.ref === m)) return m; const hit = list.filter(mm => mm.ref.endsWith('/' + m)); return hit.length === 1 ? hit[0].ref : m; };
  /** the model a worker of the team runs on (a role without an entry of its own runs on it) */
  const workerModel = S => { const w = arr(S.roster).find(x => x.id !== 'mgr' && x.model); return w ? refOf(w.model) : S.meta.model; };
  const curModel = (S, role) => role === 'manager' ? S.meta.model : ((S.meta.roleModels || {})[role] || D.roleModels[role] || workerModel(S));
  /** the model the mailman runs on when one is named for it (this session's, else the configuration's), else '' (the worker model) */
  const mailmanModel = S => (S.meta.roleModels || {}).mailman || D.roleModels.mailman || '';
  const ALL_ROLES = () => ['manager'].concat(D.roleOrder);
  /** the model changes chosen and not yet applied, by session id (role → model): Apply sends them in one request, so a team starts
   *  again once however many roles change, and editing goes on while it does */
  const drafts = {};
  /** the changes of a draft that differ from what S runs now, as [role, model] in the order of the roles */
  const pendingModels = (S, d) => ALL_ROLES().filter(r => d && d[r] && d[r] !== curModel(S, r)).map(r => [r, d[r]]);
  /** the model a role shows on the pages: the one chosen for it, else the one it runs on */
  const shownModel = (S, role) => (drafts[S.id] || {})[role] || curModel(S, role);
  /** choose a model for a role, to be applied with the others; choosing what it runs on now takes the change back */
  const stageModel = (S, role, ref) => { const d = drafts[S.id] = drafts[S.id] || {}; if (!ref || ref === curModel(S, role)) delete d[role]; else d[role] = ref; };
  /* The team, slot by slot. A team has one slot per worker ({role, model}, model '' for the role's model); the agents of a role take its
   * slots in the order of their numbers when the team starts again, so the k-th slot of a role is the k-th agent of it on the roster. */
  /** the teams chosen and not yet applied, by session id (one {role, model} per slot, in team order; model '' keeps what the slot runs
   *  on now, a model is one chosen for it): Apply
   *  sends one with the models chosen for roles, in one request */
  const tdrafts = {};
  /** the team S runs now: the server's, else for a team that names none the default team (one worker of each role); [] for a single agent */
  const teamNow = S => { const t = arr(S.meta.team); if (t.length) return t.map(x => ({ role: String(x.role), model: x.model || '' })); return arr(S.roster).length > 1 || S.meta.swarm > 0 ? D.roleOrder.map(r => ({ role: r, model: '' })) : []; };
  /** whether S is a team whose slots can be edited (a single agent has none) */
  const isTeam = S => teamNow(S).length > 0;
  /** the team the pages show for S: the one chosen, else the one it runs with every slot kept as it is */
  const teamShown = S => tdrafts[S.id] || teamNow(S).map(t => ({ role: t.role, model: '' }));
  /** the roles of team in the order they first appear */
  const teamRoles = team => team.reduce((o, t) => { if (o.indexOf(t.role) < 0) o.push(t.role); return o; }, []);
  /** the agents of role on the roster in the order they take the role's slots (by their number: be-2 before be-10) */
  const agentsOf = (S, role) => arr(S.roster).filter(x => x.id !== 'mgr' && x.role === role).sort((a, b) => String(a.id).length - String(b.id).length || (a.id < b.id ? -1 : a.id > b.id ? 1 : 0));
  /** the model a slot without one of its own runs on: the role's (this session's, else the configuration's), else the team's */
  const roleModelOf = (S, role) => (S.meta.roleModels || {})[role] || D.roleModels[role] || S.meta.model;
  /** the model the k-th slot of role runs on now: its agent's when it has started, else the slot's own, else the role's */
  const slotNow = (S, role, k) => { const a = agentsOf(S, role)[k], t = teamNow(S).filter(x => x.role === role)[k]; return (a && refOf(a.model)) || (t && t.model) || roleModelOf(S, role); };
  /** the model the k-th slot of role shows: the one chosen for it, else the one it runs on now */
  const slotShown = (S, role, k) => { const t = teamShown(S).filter(x => x.role === role)[k]; return (t && t.model) || slotNow(S, role, k); };
  /** keep team as the team chosen for S, or drop the choice when it is the team S runs (same roles, no slot on another model) */
  function setTeam(S, team) {
    tdrafts[S.id] = team;
    if (!teamChanges(S).length) delete tdrafts[S.id];
  }
  /** choose ref for the k-th slot of role (k < 0: every slot of the role) */
  function stageSlot(S, role, k, ref) {
    let i = -1; const now = teamNow(S).filter(x => x.role === role);
    setTeam(S, teamShown(S).map(t => { if (t.role !== role) return t; i++; if (k >= 0 && i !== k) return t; return { role, model: ref === slotNow(S, role, i) && i < now.length ? '' : ref }; }));
  }
  /** one worker more (d > 0) or less of role: a slot is added after the role's last, and the last is taken away; a role keeps a slot
   *  for each of its agents that has started (they come back when the team starts again). Returns why it cannot, or '' */
  function stepRole(S, role, d, max) {
    const t = teamShown(S).slice(), idx = t.map((x, i) => x.role === role ? i : -1).filter(i => i >= 0);
    if (d > 0) { if (max && t.length >= max) return 'a team has at most ' + max + ' workers here (swarm.max_workers)'; t.splice(idx.length ? idx[idx.length - 1] + 1 : t.length, 0, { role, model: '' }); }
    else { const held = agentsOf(S, role).length; if (!idx.length) return ''; if (idx.length <= held) return role + ' has ' + held + ' agent' + (held === 1 ? '' : 's') + ' at work: each keeps its slot'; t.splice(idx[idx.length - 1], 1); }
    setTeam(S, t); return '';
  }
  /** what the team chosen for S changes, in team order: {role, from, to} for a number of workers, {role, k, id, from, to} for a slot's model */
  function teamChanges(S) {
    const d = tdrafts[S.id]; if (!d) return [];
    const now = teamNow(S), out = [];
    teamRoles(now.concat(d)).forEach(r => {
      const a = now.filter(t => t.role === r), b = d.filter(t => t.role === r), ags = agentsOf(S, r);
      if (a.length !== b.length) out.push({ role: r, from: a.length, to: b.length });
      b.forEach((t, k) => { if (k < a.length && t.model && t.model !== slotNow(S, r, k)) out.push({ role: r, k, id: ags[k] ? ags[k].id : '', from: slotNow(S, r, k), to: t.model }); });
    });
    return out;
  }
  /** the team Apply sends: the chosen slots, each with its model ('' where neither the choice nor the team names one) */
  const teamSent = S => { const now = teamNow(S), seen = {}; return teamShown(S).map(t => { const k = seen[t.role] = (seen[t.role] || 0) + 1, n = now.filter(x => x.role === t.role)[k - 1]; return { role: t.role, model: t.model || (n ? n.model : '') }; }); };
  /** what "choose for" of the catalogue offers: the manager and the roles, and on a team each slot by the agent that holds it
   *  ("backend#1" is the second backend slot) */
  const targetsOf = S => {
    if (!isTeam(S)) return ALL_ROLES().map(r => [r, r]);
    const team = teamShown(S), out = [['manager', 'manager']];
    teamRoles(team).forEach(r => { const n = team.filter(t => t.role === r).length, ags = agentsOf(S, r); out.push([r, n > 1 ? r + ' (all ' + n + ')' : r]); if (n > 1) for (let k = 0; k < n; k++) out.push([r + '#' + k, '  ' + (ags[k] ? ags[k].id : r + ' slot ' + (k + 1))]); });
    return out;
  };
  /** the default a new session reads for a target of the catalogue (a slot's is its role's), as GET /api/models says where it is kept */
  const savedFor = tg => { const r = String(tg || 'manager').replace(/#\d+$/, ''), sv = D.saved || {}; return { role: r, s: sv[r] || (r === 'manager' ? sv.default : null) || { key: 'models.roles.' + r } }; };
  /** the line of the catalogue that says what a start reads for the target, from which file, and that a choice below is this session's only */
  const savedLine = (tg, name) => {
    const { s } = savedFor(tg), file = D.saveFile || '~/.sleipnir/config.json', from = s.layer === 'flag' ? 'the --model this server was started with' : s.layer === 'env' ? 'the environment (' + String(s.from || '').replace(/^env:/, '') + ')' : s.from || '';
    const over = s.ref && s.layer && s.layer !== 'user' && s.layer !== 'defaults' ? ' <span class="warm">It outranks your file <span class="mono">' + esc(file) + '</span>: a Default saved there applies here only once ' + esc(s.key) + ' is removed from ' + (s.layer === 'flag' ? 'the command that starts the server' : s.layer === 'env' ? 'the environment' : 'that file') + '.</span>' : '';
    return '<p class="stubnote" data-saved style="margin:0 0 8px">' + (s.ref ? 'Every start runs ' + esc(name) + ' on <b class="mono">' + esc(s.ref) + '</b>: <span class="mono">' + esc(s.key) + '</span> from <span class="mono">' + esc(from) + '</span>.' + over : 'No default is saved for ' + esc(name) + ' (<span class="mono">' + esc(s.key) + '</span>).') + ' <b>Use</b> changes this session only; <b>Default</b> keeps the model in <span class="mono">' + esc(file) + '</span>, so it survives a restart. ' + cli('models default', 'models default') + '</p>';
  };
  /** the model a target of the catalogue runs on now (cur) and shows (the one chosen for it); a role whose slots differ has none */
  function targetModel(S, tg) {
    const m = /^(.+)#(\d+)$/.exec(tg);
    if (m) return { cur: slotNow(S, m[1], +m[2]), shown: slotShown(S, m[1], +m[2]) };
    if (tg === 'manager' || !isTeam(S) || D.roleOrder.indexOf(tg) < 0 && !teamShown(S).some(t => t.role === tg)) return { cur: curModel(S, tg), shown: shownModel(S, tg) };
    const n = teamShown(S).filter(t => t.role === tg).length, one = f => { const v = Array.from({ length: Math.max(1, n) }, (x, k) => f(S, tg, k)); return v.every(x => x === v[0]) ? v[0] : ''; };
    return { cur: one(slotNow), shown: one(slotShown) };
  }
  /** choose ref for a target of the catalogue: a slot, every slot of a role of the team, or a role (the manager's is the team's model) */
  function stageTarget(S, tg, ref) {
    const m = /^(.+)#(\d+)$/.exec(tg);
    if (m) stageSlot(S, m[1], +m[2], ref);
    else if (tg !== 'manager' && isTeam(S) && teamShown(S).some(t => t.role === tg)) stageSlot(S, tg, -1, ref);
    else stageModel(S, tg, ref);
  }
  /** a change of the team as the bar says it (markup) */
  const changeTxt = c => c.k == null ? '<span class="rrc" style="color:' + roleCol(c.role) + '">' + esc(c.role) + '</span> ' + c.from + ' → ' + c.to + ' worker' + (c.to === 1 ? '' : 's')
    : '<span class="rrc" style="color:' + (c.id ? agColor(c.id) : roleCol(c.role)) + '">' + esc(c.id || c.role + ' slot ' + (c.k + 1)) + '</span> → <span class="mono">' + esc(c.to) + '</span>';
  /** the bar of the changes waiting to be applied, on Models & roles and on Team & run ('' when there is none) */
  const pendingBar = S => {
    const p = pendingModels(S, drafts[S.id]), tc = teamChanges(S), n = p.length + tc.length; if (!n) return '';
    const team = arr(S.roster).length > 1 || isTeam(S), size = teamShown(S).length, min = minWorkers(), short = tc.length && size < min;
    return '<div class="pendbar" role="status"><span class="pb-t"><b>' + n + ' change' + (n === 1 ? '' : 's') + '</b> to apply: ' + p.map(([r, m]) => '<span class="rrc" style="color:' + roleCol(r) + '">' + esc(r) + '</span> → <span class="mono">' + esc(m) + '</span>').concat(tc.map(changeTxt)).join(' · ') + (short ? '<br><span class="warm">A team has at least ' + min + ' workers: this one has ' + size + '.</span>' : '') + '</span>' +
      '<span class="pb-a"><button class="btn sm" type="button" data-do="models-discard">Discard</button> <button class="btn sm pri" type="button" data-do="models-apply"' + (short ? ' disabled' : '') + '>' + (team ? 'Apply: the team restarts once' : 'Apply') + '</button></span></div>';
  };
  const hello = () => TK.hello() || {};
  /** the server's defaults for a new session (hello.defaults), or {} */
  /** the tab title badge as it is now: the person's choice, else the server's default (off when SLEIPNIR_BELL=0) */
  const titleOn = () => { const v = SL.settings.title; return v === 'on' || (v !== 'off' && !(hello().ui && hello().ui.bell === false)); };
  const defaults = () => { const d = (SL.live && typeof SL.live.defaults === 'function' && SL.live.defaults()) || hello().defaults; return d || {}; };
  /** the most workers a team may have: the server's ceiling (config swarm.max_workers) when it says one, else 0 (none) */
  const maxWorkers = () => Math.max(0, parseInt(defaults().maxWorkers, 10) || 0);
  /** the fewest workers a team may have (one of each built-in role) */
  const minWorkers = () => Math.max(1, parseInt(defaults().minWorkers, 10) || 8);

  /* ---------------------------------------------------------------- pure parts (also read by the tests) */
  /** the models a filter keeps, favourites first: words (all must appear), tools, reasoning, favourites, max input and output price, min context */
  function modelRows(list, st, favs) {
    const words = String(st.q || '').toLowerCase().split(/\s+/).filter(Boolean), maxp = parseFloat(st.maxp), maxo = parseFloat(st.maxo), minc = parseInt(st.minc, 10);
    return arr(list).filter(mm => words.every(w => mm.ref.toLowerCase().includes(w)) && (!st.tools || mm.tools) && (!st.reasoning || mm.reasoning) && (!st.favs || favs.has(mm.ref)) && (isNaN(maxp) || (mm.in != null && mm.in <= maxp)) && (isNaN(maxo) || (mm.out != null && mm.out <= maxo)) && (isNaN(minc) || mm.ctx >= minc)).sort((a, b) => (favs.has(b.ref) ? 1 : 0) - (favs.has(a.ref) ? 1 : 0));
  }
  /** The `sleipnir models` command line and request for the filters on the page (the CLI's --max-price is the OUTPUT price; it has no input-price flag). */
  function modelsCli(st) {
    const flags = {}, pos = {}, parts = ['models'], words = String(st.q || '').trim();
    if (words) { pos.WORDS = words; parts.push(words); }
    if (st.tools) { flags.tools = true; parts.push('--tools'); } if (st.reasoning) { flags.reasoning = true; parts.push('--reasoning'); } if (st.favs) { flags.fav = true; parts.push('--fav'); } if (st.all) { flags.all = true; parts.push('--all'); }
    if (st.maxo) { flags['max-price'] = parseFloat(st.maxo); parts.push('--max-price ' + st.maxo); } if (st.minc) { flags['min-context'] = String(st.minc); parts.push('--min-context ' + st.minc); }
    return { flags, pos, label: parts.join(' ') };
  }
  /** the rules in force: the server's (deny, ask, allow; built in, from a file, from the flags) then this session's, each with its origin. A rule both lists carry shows once: from the file when it is a configuration rule, from the session when the person can remove it. */
  const CONFIG_ORIGINS = /^(user config|project config|local config|built-in protection|default)/;
  function rulesIn(S) {
    const P = X.permissions && X.permissions.rules, out = [];
    if (P) ['deny', 'ask', 'allow'].forEach(eff => arr(P[eff]).forEach(r => out.push({ effect: eff, rule: r.rule, origin: r.origin, file: r.file || '', note: r.note || r.why || '', fixed: true })));
    else arr(D.rules).forEach(r => out.push(Object.assign({ fixed: true, file: '' }, r)));
    arr(S.meta.rules).forEach(r => {
      const i = out.findIndex(x => x.fixed && x.effect === r.effect && x.rule === r.rule);
      if (i >= 0) { if (CONFIG_ORIGINS.test(String(r.origin || '')) || CONFIG_ORIGINS.test(String(out[i].origin || ''))) return; out.splice(i, 1); }   // the same rule twice: one line
      out.push({ effect: r.effect, rule: r.rule, origin: r.origin, file: r.file || '', note: r.note || '', fixed: !!r.fixed });
    });
    return out;
  }
  /** 2026-10-08 as Oct 8 (anything else as it is) */
  const dayTxt = d => { const m = /^\d{4}-(\d\d)-(\d\d)/.exec(d || ''); return m ? ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'][+m[1] - 1] + ' ' + (+m[2]) : String(d || ''); };
  /** how a provider's key is told on the page: the tag text and tone, and the button the row gets (in: Sign in…, out: Sign out, none) */
  function keyInfo(p) {
    if (p.key === 'none') return /no key needed/i.test(p.state || '') ? { txt: 'no key needed', cls: '', btn: '' } : { txt: 'no key', cls: '', btn: 'in' };
    if (p.key === 'env') return { txt: '✓ env ' + (p.env || ''), cls: 'ok', btn: 'out', env: true };
    if (p.key === 'signed in') return { txt: '✓ signed in', cls: 'ok', btn: 'out' };
    return { txt: '✓ stored', cls: 'ok', btn: 'out' };
  }
  /** `sleipnir chat ...` as the Run settings page shows it: what a restart would start (a flag that is on by default and is off here says --flag=false) */
  function runLine(M, defs) {
    // every value is quoted for the shell the line is pasted into: a model's name comes from a provider's catalogue
    const d = defs || {}, q = a => { a = String(a == null ? '' : a); return /^[A-Za-z0-9_@%+=:,./-]+$/.test(a) ? a : "'" + a.replace(/'/g, "'\\''") + "'"; }, flag = (k, name) => M[k] ? ' --' + name : (d[k] ? ' --' + name + '=false' : '');
    return 'sleipnir chat --model ' + q(M.model) + ' --mode ' + q(M.mode) + ' --swarm ' + q(M.swarm) + ' --isolation ' + q(M.isolation) + (M.verify ? ' --verify ' + q(M.verify) : '') + (M.budget ? ' --budget-usd ' + q(M.budget) : '') + flag('commit', 'commit') + flag('mailman', 'mailman') + (M.autoDocs === false ? ' --auto-docs=false' : '') + (M.managerApproves === false ? ' --manager-approves=false' : '') + flag('noMcp', 'no-mcp') + flag('noMemory', 'no-memory') + flag('trustProject', 'trust-project');
  }
  /** a team size the server takes: 0 (a single agent) or at least 8 (one worker of each role), at most max when there is one; a value
   *  in between goes to 8, or to 0 when dir says the person is counting down */
  const clampWorkers = (v, max, dir) => { let n = Math.max(0, parseInt(v, 10) || 0); if (n > 0 && n < 8) n = dir < 0 ? 0 : 8; return max ? Math.min(max, n) : n; };
  const teamWord = n => n === 0 ? 'single agent' : 'manager + ' + n + ' worker' + (n === 1 ? '' : 's');
  /** A project's trust as the server words it (GET /api/projects: trusted, untrusted, changed, partial, unreadable) as the table shows it, or '' when there is nothing to trust. */
  const projectState = t => { const v = String(t == null ? '' : t); return v === 'trusted' || v === 'none' || v === '' ? '' : v === 'untrusted' ? 'not trusted' : v === 'changed' ? 'changed since your yes' : v; };
  /** What the two words that mean "not every file could be read" say; a yes for such a project holds for the session only. */
  const TRUST_WHY = { partial: 'not every file of this project could be scanned: a yes holds for this session only and is not remembered', unreadable: 'its files could not be read: a yes holds for this session only and is not remembered' };
  /** the trust ledger as the table draws it: the server's rows, then the projects nobody said yes to (the one of this tab, and the host's list: GET /api/projects), so that each can be trusted from here */
  function ledgerRows(T, dirs, projects) {
    const rows = arr(dirs).slice(), pr = T && T.project, have = d => rows.some(r => r.dir === d);
    if (pr && pr.dir && pr.state !== 'trusted' && !have(pr.dir)) rows.push({ dir: pr.dir, files: arr(T.files).length, state: pr.state && pr.state !== 'not trusted' ? projectState(pr.state) || pr.state : 'not trusted' });
    arr(projects).forEach(p => { const st = projectState(p && p.trust), dir = p && (p.dir || p.root); if (st && dir && !have(dir)) rows.push({ dir, files: p.files == null ? '' : p.files, state: st }); });
    return rows;
  }
  /** a warning of the configuration as {where, text}: "file:line: message" splits at the position, anything else is all message */
  function warningRow(w) { const m = /^(\S+?:\d+(?::\d+)?):\s*(.*)$/.exec(String(w)); return m ? { where: m[1], text: m[2] } : { where: '', text: String(w) }; }
  /** a located issue of the configuration ({file, line, col, path, message, severity}) as {where, text, sev} */
  const issueRow = i => ({ where: (i.file || '') + (i.line ? ':' + i.line + (i.col ? ':' + i.col : '') : ''), text: (i.path ? i.path + ': ' : '') + String(i.message || ''), sev: i.severity || '' });
  /** the warnings of the configuration: the server's located issues, or its warning lines when it sends no issues */
  const configWarnings = C => Array.isArray(C.issues) ? C.issues.map(issueRow) : arr(C.warnings).map(warningRow);
  /** the prompts of an MCP server as the slash commands they are: /mcp__server__name, each with its description */
  const promptCmds = (d, name) => arr(d && d.prompts).map(p => { const s = String(typeof p === 'string' ? p : (p && (p.command || p.cmd || p.name)) || ''); return s ? { cmd: /^\/?mcp__/.test(s) ? s.replace(/^\//, '') : 'mcp__' + name + '__' + s, desc: typeof p === 'string' ? '' : String((p && p.description) || '') } : null; }).filter(Boolean);
  /** the yes itself: the id the trust step resolved (the one of the challenge it showed), not a new one */
  const trustPost = (dir, id) => net.post('/api/trust', { dir, on: true }, { confirmId: id });
  /**
   * "Trust these files": the server's challenge goes to the trust step (ui.trustStep), which lists every file it sent, each path it could
   * not read, and says that a yes for a partial or unreadable project holds for the session only; its yes stays asleep until the whole
   * list is on screen. Only the id that step resolves is sent. Without the step nothing is trusted. Resolves true when it was.
   */
  async function trustFlow(dir) {
    const c = await net.get('/api/trust/challenge?dir=' + encodeURIComponent(dir)); if (!c.ok) { net.fail(c, 'the files could not be read'); return false; }
    if (typeof ui.trustStep !== 'function') { ui.toast('the trust step is not available here: nothing was trusted', 'err'); return false; }
    const id = await ui.trustStep(c.data || {}, { dir }); if (!id) return false;
    const r = await trustPost(dir, id), reload = () => { if (SL.data) { SL.data.load('trust', { force: true }); SL.data.load('projects', { force: true }); } };
    if (!r.ok) { net.fail(r, 'the files were not trusted'); reload(); return false; }
    reload(); ui.toast('trusted ' + dir, 'ok'); return true;
  }
  /** the confirm of "Forget all…": the directories it forgets */
  const forgetSpec = dirs => ({ title: 'Forget every directory', text: 'Forget the <b>' + dirs.length + '</b> director' + (dirs.length === 1 ? 'y' : 'ies') + ' you said yes to? Their own files are left out of the next session until you say yes again.', detail: '<span class="mono">' + esc(dirs.map(d => d.dir).join('\n')) + '</span>', ok: 'Forget them', danger: true });
  /** The words under the spend of the Budget page: spent, or what a plan's figure is (text). */
  const budLabel = c => U.planOnly(c) ? 'plan: ' + U.PLAN_HELP : c.plan ? 'spent, with plan usage ' + U.PLAN_HELP : 'spent';
  /** The words under the budget gauge (text): how much of the budget the bill used, and for a session that is all a plan's, that the plan
   *  counts against no budget. A session that mixes the two is measured by its bill, as its budget is. */
  const budNote = (c, budget) => U.planOnly(c) ? (budget ? fmtUsd(0, 2) + ' billed: the plan counts against no budget' : 'no limit; the plan counts against no budget')
    : budget ? Math.round(U.budgetPct(c, budget)) + '% used' + (c.plan ? ' (billed; the plan\'s part is not counted)' : '') : 'no limit';
  TK.settings = { trustFlow, trustPost, forgetSpec, projectState, TRUST_WHY, budLabel, budNote, modelRows, modelsCli, rulesIn, dayTxt, keyInfo, runLine, clampWorkers, teamWord, ledgerRows, warningRow, issueRow, configWarnings, promptCmds, curModel, pendingModels, PAGES, ALIAS, pageOf, PLUGIN_OF, teamNow, teamShown, agentsOf, slotNow, slotShown, refOf, stageSlot, stepRole, teamChanges, teamSent, tdrafts, drafts, targetsOf, targetModel, stageTarget };

  /* ---------------- the pages: pure builders of markup from the session S and the page state ST ---------------- */
  /** The parts of the pages, by the id each had as a page of its own: B.trust(S, ST) is the markup of the trust cards. */
  const B = {};

    /** the options of a model select: every model of the catalogue, with cur first when the catalogue does not have it */
    const modelOpts = cur => (cur && !D.models.some(mm => mm.ref === cur) ? '<option selected>' + esc(cur) + '</option>' : '') + D.models.map(mm => '<option' + (mm.ref === cur ? ' selected' : '') + '>' + esc(mm.ref) + '</option>').join('');
    /**
     * The team, role by role: how many workers each has (a stepper), the model of all its slots, and each slot with the agent that holds
     * it and the model it runs on. A change waits in the bar with the other changes; a slot whose agent has not started says so.
     */
    const teamEditor = S => {
      const team = teamShown(S), now = teamNow(S), mx = maxWorkers(), roles = teamRoles(team).concat(D.roleOrder.filter(r => !team.some(t => t.role === r)));
      return '<div class="setteam">' + roles.map(r => {
        const R = D.roles[r] || { code: 'sc', desc: '' }, n = team.filter(t => t.role === r).length, had = now.filter(t => t.role === r).length, ags = agentsOf(S, r), held = ags.length;
        const shown = Array.from({ length: n }, (x, k) => slotShown(S, r, k)), mixed = shown.filter((m, i, a) => a.indexOf(m) === i).length > 1;
        const all = '<select data-tall="' + esc(r) + '" aria-label="model for ' + (n > 1 ? 'all ' + n + ' ' + esc(r) + ' slots' : esc(r)) + '"' + (n ? '' : ' disabled') + '>' + (mixed ? '<option value="" disabled selected>' + shown.filter((m, i, a) => a.indexOf(m) === i).length + ' models: set all ' + n + '</option>' + modelOpts(null) : modelOpts(shown[0] || roleModelOf(S, r))) + '</select>';
        const step = '<span class="nststep"><button type="button" class="btn sm" data-tcount="' + esc(r) + '" data-d="-1" aria-label="one ' + esc(r) + ' less"' + (n > held ? '' : ' disabled title="' + (held ? 'each of its ' + held + ' agents at work keeps its slot' : 'no slot') + '"') + '>−</button><span class="nstn mono" aria-label="' + esc(r) + ' workers">' + n + '</span><button type="button" class="btn sm" data-tcount="' + esc(r) + '" data-d="1" aria-label="one more ' + esc(r) + '"' + (mx && team.length >= mx ? ' disabled' : '') + '>+</button></span>';
        const slots = n > 1 || held ? '<div class="nstslots" role="group" aria-label="' + esc(r) + ' slots, one model each">' + Array.from({ length: n }, (x, k) => {
          const a = ags[k], was = k < had ? slotNow(S, r, k) : '', pend = was && shown[k] !== was;
          return '<label class="nstslot' + (pend ? ' pend' : '') + '"><span class="mono' + (a ? '' : ' dim') + '"' + (a ? ' style="color:' + agColor(a.id) + '"' : '') + ' title="' + (a ? 'the agent that holds this slot' : k < had ? 'no agent has started in this slot yet' : 'a slot added here, filled when the manager gives the role work') + '">' + (a ? esc(a.id) : k < had ? 'open' : 'new') + '</span><select data-tslot="' + esc(r) + '" data-ti="' + k + '" aria-label="model for ' + esc(a ? a.id : r + ' slot ' + (k + 1)) + '">' + modelOpts(shown[k]) + '</select>' + (pend ? tag('now ' + was, 'warm', 'runs on ' + was + ' until the changes are applied') : '') + '</label>';
        }).join('') + '</div>' : '';
        return '<div class="nstrow' + (n ? '' : ' zero') + '" data-trole="' + esc(r) + '"><span class="nstname" style="color:' + roleCol(r) + '">' + esc(r) + '</span>' + (ui.help ? ui.help(esc(R.desc || '') + '.' + (R.ro ? ' <b>Read-only.</b>' : ''), r) : '') + step + all + '</div>' + slots;
      }).join('') + '</div>';
    };
    B.models = (S, ST) => {
      const st = ST.models, favs = G.favs || new Set(), all = st.view ? st.view.models : D.models, tgs = targetsOf(S); if (!tgs.some(t => t[0] === st.role)) st.role = 'manager';
      const tm = targetModel(S, st.role), cur = tm.cur, shown = tm.shown, rows = modelRows(all, st, favs), tgName = (tgs.find(t => t[0] === st.role) || [0, st.role])[1].trim();
      /* who runs on a model now: on a team each agent at work (and the manager), else each role */
      const users = ref => isTeam(S) ? arr(S.roster).filter(x => refOf(x.model, all) === ref).map(x => [x.id, agColor(x.id)]).concat(ALL_ROLES().filter(r => r !== 'manager' && !teamNow(S).some(t => t.role === r) && curModel(S, r) === ref).map(r => [r, roleCol(r)])) : ALL_ROLES().filter(r => curModel(S, r) === ref).map(r => [r, roleCol(r)]);
      const errs = arr(st.view ? st.view.errors : D.modelErrors), cl = modelsCli(st);
      const opt = (list, v) => list.map(o => '<option value="' + o[0] + '"' + (v === o[0] ? ' selected' : '') + '>' + o[1] + '</option>').join('');
      return card('settings.models.catalogue', 'The model catalogue', '', pendingBar(S) + '<div class="setfilter"><input id="mq" type="search" placeholder="filter: every word must appear, any case" aria-label="Filter models" value="' + esc(st.q) + '" autocomplete="off"><div class="seg" role="group" aria-label="Filters"><button type="button" data-flt="tools" aria-pressed="' + st.tools + '">tools</button><button type="button" data-flt="reasoning" aria-pressed="' + st.reasoning + '">reasoning</button><button type="button" data-flt="favs" aria-pressed="' + st.favs + '">★ favourites</button><button type="button" data-flt="all" aria-pressed="' + st.all + '" title="include models that do not chat (--all)">all</button></div>' +
        '<button class="btn sm" type="button" data-do="refresh-models"' + (st.busy ? ' disabled' : '') + ' title="asks every provider again; the list is kept 6 h">' + (st.busy ? 'Fetching…' : 'Refresh the catalogue') + '</button>' +
        '<span style="display:inline-flex;align-items:center;gap:8px;white-space:nowrap"><label class="lab" for="mxp">max input $/M</label><select id="mxp" data-sel="maxp">' + opt([['', 'any'], ['0.5', '0.5'], ['1', '1'], ['3', '3'], ['5', '5']], st.maxp) + '</select></span><span style="display:inline-flex;align-items:center;gap:8px;white-space:nowrap"><label class="lab" for="mxo">max output $/M</label><select id="mxo" data-sel="maxo" title="--max-price: the price of the output">' + opt([['', 'any'], ['2', '2'], ['5', '5'], ['10', '10'], ['25', '25']], st.maxo) + '</select></span><span style="display:inline-flex;align-items:center;gap:8px;white-space:nowrap"><label class="lab" for="mnc">min context</label><select id="mnc" data-sel="minc">' + opt([['', 'any'], ['64000', '64k'], ['128000', '128k'], ['400000', '400k']], st.minc) + '</select></span>' +
        '<span style="display:inline-flex;align-items:center;gap:8px;white-space:nowrap"><label class="lab" for="mrl">choose for</label><select id="mrl" data-sel="role">' + tgs.map(t => '<option value="' + esc(t[0]) + '"' + (st.role === t[0] ? ' selected' : '') + '>' + esc(t[1]).replace(/^ +/, '&nbsp;&nbsp;') + '</option>').join('') + '</select></span></div>' +
        savedLine(st.role, tgName) + (D.modelsNote ? '<p class="stubnote warm" style="margin:0 0 8px">' + esc(D.modelsNote) + '</p>' : '') + (errs.length ? '<p class="stubnote warm" style="margin:0 0 8px">' + errs.length + ' source' + (errs.length === 1 ? '' : 's') + ' of the catalogue could not be read: ' + esc(errs.join(' · ')) + '</p>' : '') +
        '<div class="settable"><table class="tbl"><thead><tr><th></th><th>model</th><th class="r">context</th><th class="r">in $/M</th><th class="r">out $/M</th><th>tags</th><th>runs</th><th></th></tr></thead><tbody>' + rows.map(mm => '<tr class="' + (mm.ref === shown ? 'sel' : '') + (mm.ref === shown && shown !== cur ? ' pend' : '') + '"><td><button type="button" class="star' + (favs.has(mm.ref) ? ' on' : '') + '" data-fav="' + esc(mm.ref) + '" aria-pressed="' + favs.has(mm.ref) + '" aria-label="' + (favs.has(mm.ref) ? 'unstar ' : 'star ') + esc(mm.ref) + '">' + (favs.has(mm.ref) ? '★' : '☆') + '</button></td><td class="bright">' + esc(mm.ref) + '</td><td class="r">' + fmtK(mm.ctx) + '</td><td class="r">' + (mm.in == null ? '<span class="dim">price unknown</span>' : mm.in.toFixed(2)) + '</td><td class="r">' + (mm.out == null ? '' : mm.out.toFixed(2)) + '</td><td>' + (mm.plan ? tag('plan', 'ok', 'a ChatGPT plan covers its use: the prices are the API list prices of the same model, which the plan does not bill') : '') + (mm.tools ? tag('tools', 'fe') : '') + (mm.reasoning ? tag('reasoning', 'fe') : '') + '</td><td>' + users(mm.ref).map(([r, c]) => '<span class="rrc" style="color:' + c + '">' + esc(r) + '</span>').join(' ') + '</td><td class="r nowrap"><button type="button" class="btn sm' + (mm.ref === shown ? '' : ' pri') + '" data-use="' + esc(mm.ref) + '"' + (mm.ref === shown ? ' disabled' : '') + '>' + (mm.ref === shown ? (shown === cur ? 'current' : 'chosen') : (mm.ref === cur ? 'Keep for ' : 'Use for ') + esc(tgName)) + '</button>' + (savedFor(st.role).s.ref === mm.ref ? ' <button type="button" class="btn sm" disabled title="every start runs ' + esc(tgName) + ' on it">default ✓</button>' : ' <button type="button" class="btn sm" data-def="' + esc(mm.ref) + '" title="keep it as the default of ' + esc(tgName) + ' in ' + esc(D.saveFile || '~/.sleipnir/config.json') + ': every start reads it">Default</button>') + '</td></tr>').join('') + (rows.length ? '' : '<tr><td colspan="8" class="dim">' + (arr(all).length ? 'No model matches these filters: clear the filter words or a chip.' : 'The catalogue is empty: the models of every provider you are signed in to appear here. Refresh the catalogue asks them again.') + '</td></tr>') + '</tbody></table></div>' +
        '<p class="stubnote">' + rows.length + ' of ' + arr(all).length + ' models, prices in dollars per million tokens. A model you choose waits with the other changes until you apply them; the team then starts again once. <b>plan</b>: covered by your ChatGPT plan, shown at the API list price. ' + cli('models', cl.label, ' data-cli-models') + '</p>');
    };
    B.roles = (S, ST) => {
      const m = S.m || S.wm, ef = ST.eff[S.id] || {}, rowsH = ALL_ROLES().map(r => { const ws = S.roster.filter(x => x.role === r && x.id !== 'mgr' || (r === 'manager' && x.id === 'mgr')), now = curModel(S, r), cur = shownModel(S, r), pend = cur !== now, unk = (D.model(cur) || {}).in == null;
        return '<tr' + (pend ? ' class="pend"' : '') + '><td style="color:' + roleCol(r) + ';font-weight:700;width:120px">' + esc(r) + '</td><td class="dim" style="width:150px">' + (ws.length ? ws.map(w => '<span style="color:' + agColor(w.id) + '">' + esc(w.id) + '</span>').join(' ') : '<span class="faint">none on the team</span>') + '</td><td><select data-role="' + esc(r) + '" aria-label="model for ' + esc(r) + '">' + (D.models.some(mm => mm.ref === cur) ? '' : '<option selected>' + esc(cur) + '</option>') + D.models.map(mm => '<option' + (mm.ref === cur ? ' selected' : '') + '>' + esc(mm.ref) + '</option>').join('') + '</select>' + (pend ? ' ' + tag('now ' + now, 'warm', 'runs on ' + now + ' until the changes are applied') : '') + (unk ? ' ' + tag('price unknown', 'warm') : '') + '</td></tr>'; }).join('');
      const mailman = '<tr><td style="color:var(--warm);font-weight:700">mailman</td><td class="dim">a helper, not a worker</td><td><span class="dim">' + (S.meta.mailman ? (mailmanModel(S) ? 'runs on <span class="mono">' + esc(mailmanModel(S)) + '</span>' : 'runs on the worker model') + ' and turns worker mail into short digests' : 'off in this session: worker mail is delivered at once') + '</span> ' + pluginNote('mailman') + '</td></tr>';
      const applied = ef.applied && ef.applied !== S.meta.effort ? ' · this model: <b>' + esc(ef.applied) + '</b>' : '';
      const mgrRow = () => { const now = curModel(S, 'manager'), cur = shownModel(S, 'manager'), pend = cur !== now; return '<div class="nstrow setmgr' + (pend ? ' pend' : '') + '"><span class="nstname" style="color:' + roleCol('manager') + '">manager</span><span class="mono dim" style="color:' + agColor('mgr') + '">mgr</span><span></span><span><select data-role="manager" aria-label="model for manager">' + modelOpts(cur) + '</select>' + (pend ? ' ' + tag('now ' + now, 'warm', 'runs on ' + now + ' until the changes are applied') : '') + '</span></div>'; };
      const teamCard = () => card('settings.roles.roles', 'Who runs on which model', '<span class="dim">' + teamWord(teamShown(S).length) + '</span> ' + cli('models', 'models'), pendingBar(S) + mgrRow() + teamEditor(S) + '<table class="tbl" style="margin-top:8px"><tbody>' + mailman + '</tbody></table><p class="stubnote" style="margin:10px 0 0">Each agent is listed under its role with the model it runs on: choose a model for one agent, or for all the slots of a role, and change how many workers a role has. Changes wait until you apply them; the team then starts again once and keeps the conversation, the board and every agent’s work. <span class="dim mono">--role-model ROLE=MODEL</span>, given once per slot</p>');
      return (isTeam(S) ? teamCard() : card('settings.roles.roles', 'Which model each role runs on', cli('models', 'models'), pendingBar(S) + '<table class="tbl"><tbody>' + rowsH + mailman + '</tbody></table><p class="stubnote" style="margin:10px 0 0">A role without a model of its own runs on the worker model. Changes wait until you apply them; the team then starts again once and keeps the conversation and the board. <span class="dim mono">--role-model ROLE=MODEL</span></p>')) +
        card('settings.roles.effort', 'Reasoning effort', '', row('Effort', seg('effort', efforts(), S.meta.effort, 'Reasoning effort'), 'how long the models think before they answer; each model takes the closest level it supports', '', '/effort') + '<p class="stubnote" style="margin:0">Current: <b id="effNow">' + esc(S.meta.effort) + '</b>' + applied + '. Effort applies to the turns from now on; it never changes the cached prefix.</p>');
    };
    B.budget = (S, ST) => {
      const c = calc.totals(S.wm); return card('settings.budget.budget', 'This session’s budget', '', '<div class="bigrow"><div><div class="big num" id="budSpent">' + U.fmtSpend(c, 4) + '</div><small id="budSpentLbl">' + esc(budLabel(c)) + '</small></div><div><div class="big num dimv">' + (S.meta.budget ? fmtUsd(S.meta.budget, 2) : 'off') + '</div><small>budget</small></div><div class="gaugecol"><div class="gauge wide' + (c.plan ? ' plan' : '') + '"><i id="budBar" style="width:' + U.budgetPct(c, S.meta.budget) + '%"></i></div><small id="budPct">' + esc(budNote(c, S.meta.budget)) + '</small></div></div>' +
        row('Set the budget', '<input id="bIn" type="text" inputmode="decimal" value="' + (S.meta.budget ? S.meta.budget.toFixed(2) : 'off') + '" aria-label="Budget in dollars" style="width:110px"> <button class="btn pri" type="button" data-do="budget">Set</button> <button class="btn" type="button" data-do="budget-off">Off</button>', 'a number of dollars, or off; at the limit the turn and the goal pause', 'bIn', '--budget-usd · /budget')) +
        card('settings.budget.cost', 'Cost by agent', '<span class="dim mono">/cost</span>', '<table class="tbl" id="budTbl"><thead><tr><th>agent</th><th class="r">prompt tok</th><th class="r qfull">hit</th><th class="r">out tok</th><th class="r">cost</th></tr></thead><tbody></tbody></table><p class="stubnote qfull" style="margin:8px 0 0"><span id="budSavedLbl">Saved est. at list price</span>: <b id="budSaved"></b> (an estimate).</p><p class="stubnote" id="budUnpriced" hidden style="margin:8px 0 0"></p><p class="stubnote" style="margin:8px 0 0">Every figure derives from one table of tokens per agent, so this page, the HUD, the stalls and the Cache view agree.</p>');
    };
    B.permissions = (S, ST) => {
      const PM = X.permissions, modes = PM ? PM.modes : D.modes.map(m => ({ id: m.id, danger: !!m.danger, cycle: !m.danger, text: m.desc })), rs = rulesIn(S), res = ST.perm.res, dg = ST.perm.danger;
      const modeRows = modes.filter(m => !m.danger).map(m => '<button type="button" class="mrow3" data-mode="' + esc(m.id) + '" aria-pressed="' + (S.meta.mode === m.id) + '"><b class="mono">' + esc(m.id) + '</b><span>' + esc(m.text) + '</span>' + (S.meta.mode === m.id ? tag('current', 'ok') : '') + '</button>').join('');
      const dng = modes.filter(m => m.danger).map(m => '<button type="button" class="mrow3 dng" data-danger="' + esc(m.id) + '" aria-pressed="' + (S.meta.mode === m.id) + '"><b class="mono">⚠ ' + esc(m.id) + '</b><span>' + esc(m.text) + '</span>' + (S.meta.mode === m.id ? tag('current', 'err') : '') + '</button>').join('');
      return card('settings.permissions.mode', 'Mode', '<span class="mono">/mode</span> · <span class="mono">shift+tab</span>', '<div class="modes3">' + modeRows + '</div><p class="stubnote" style="margin:8px 0 0"><kbd>shift</kbd> <kbd>tab</kbd> steps <b>default → accept-edits → plan → default</b> and never enters bypass or yolo; those two are set only by typing their name below (or <span class="mono">/mode bypass</span>, <span class="mono">--mode</span>). <span class="dim">--continue never brings them back.</span></p>') +
        '<section class="panel setpanel danger" data-slot="settings.permissions.danger"><div class="ph"><h2>⚠ Dangerous modes</h2><div class="r"><span class="err">asks nothing</span></div></div><div class="setcard"><div class="modes3">' + dng + '</div>' + (dg ? '<div class="dangerbox"><span>' + (dg === 'yolo' ? 'yolo asks nothing and ignores the ask rules; for sandboxes and runs with nobody there. Type <b>yolo</b> to confirm.' : 'bypass asks nothing except about the very dangerous (sudo, a recursive delete, a forced push ...); deny rules still apply. Type <b>bypass</b> to confirm.') + '</span><input id="cfIn" type="text" autocomplete="off" aria-label="Type the mode name to confirm" placeholder="type ' + esc(dg) + '"><div class="row2"><button class="btn danger" type="button" id="cfGo" disabled>Set ' + esc(dg) + '</button><button class="btn" type="button" data-do="danger-no">Cancel</button></div></div>' : '') + '</div></section>' +
        card('settings.permissions.manager', 'The manager', '', '<p class="stubnote" style="margin:0">' + esc(PM ? PM.managerWrites.note : 'Substantial work uses persistent role pools; the manager handles direct requests and GitHub operations.') + '</p><p>' + esc(PM ? PM.managerWrites.text : "The manager can edit files and run commands under the session's permissions.") + '</p>') +
        card('settings.permissions.rules', 'Rules in force', '<span>' + rs.length + ' rules · ' + rs.filter(r => !r.fixed).length + ' from this session</span>', '<table class="tbl"><thead><tr><th>effect</th><th>rule</th><th>from</th><th></th></tr></thead><tbody>' + rs.map(r => '<tr><td>' + tag(r.effect, r.effect === 'deny' ? 'err' : r.effect === 'ask' ? 'warm' : 'ok') + '</td><td class="bright mono">' + esc(r.rule) + (r.note ? '<small class="rn">' + esc(r.note) + '</small>' : '') + '</td><td>' + (r.fixed ? esc(r.origin) + (r.file ? ' <span class="dim mono">' + esc(r.file) + '</span>' : '') : '<b class="' + (r.origin === "don't ask again" ? 'warm' : 'fe') + '">' + esc(r.origin) + '</b> <span class="dim">· this session</span>') + '</td><td class="r">' + (r.fixed ? '<span class="dim" title="built in or from a file: change it there">locked</span>' : '<button class="btn sm" type="button" data-rm="' + esc(r.rule) + '">remove</button>') + '</td></tr>').join('') + (rs.length ? '' : '<tr><td colspan="4" class="dim">No rule: the rules of your configuration files and the ones you add here appear in this list.</td></tr>') + '</tbody></table>' +
          '<div class="field-row" style="margin-top:12px"><select id="ruleEff" aria-label="Rule effect"><option>allow</option><option>deny</option><option>ask</option></select><input id="ruleIn" type="text" placeholder="Bash(go test:*) · Edit(./docs/**) · tests" aria-label="Rule"><button class="btn pri" type="button" data-do="rule-add">Add for this session</button><button class="btn" type="button" data-do="rule-tests" title="' + esc((PM ? PM.testsPreset.summary : '')) + '">+ the tests preset</button></div><p class="stubnote" style="margin:6px 0 0">An answer 2 (“don’t ask again”) adds an allow rule with the origin <b class="warm">don’t ask again</b>. <span class="mono">tests</span> expands to ' + (PM ? PM.testsPreset.rules.length : arr(D.testsPreset).length) + ' rules: build and test commands, and a Python virtual environment in the workspace (no other install). Deny rules from a project can add to yours, never remove from it.</p>') +
        card('settings.permissions.try', 'Would it ask?', '<span class="mono">the order: ' + esc(PM ? PM.order.map(o => o.split(' (')[0]).join(' → ') : 'deny → ask → high-risk → allow → mode') + '</span>', '<div class="field-row"><select id="tryTool" aria-label="Tool"><option' + (ST.perm.tool === 'Bash' ? ' selected' : '') + '>Bash</option><option' + (ST.perm.tool === 'Edit' ? ' selected' : '') + '>Edit</option><option' + (ST.perm.tool === 'Read' ? ' selected' : '') + '>Read</option></select><input id="tryArg" type="text" value="' + esc(ST.perm.arg) + '" aria-label="Command or path" placeholder="npm install --save-dev vitest · api/server.go · ./.env"><button class="btn" type="button" data-do="try"' + (ST.perm.busy ? ' disabled' : '') + '>Check</button></div><div class="tryres" id="tryRes" aria-live="polite">' + (res ? '<span class="tag ' + esc(res.cls) + '">' + esc(res.d) + '</span> ' + esc(res.why) : '<span class="dim">Type a command or a path and see which rule decides, in the order above.</span>') + '</div>');
    };
    B.trust = (S, ST) => {
      const T = X.trust, pr = T && T.project, files = T ? arr(T.files) : [], used = !!S.meta.trustProject, st = pr ? pr.state : '';
      const stateHtml = !used ? tag('not used', 'warm') + ' <span class="dim">“Use this project’s own files” is off for this session (Team &amp; run, <span class="mono">--trust-project</span>): they are left out</span>' : st === 'trusted' || !pr ? tag('trusted', 'ok') + ' <span class="dim">you said yes' + (pr && pr.savedDay ? ' on ' + esc(dayTxt(pr.savedDay)) : '') + '; the hashes are unchanged</span>' : /^changed/.test(st) ? tag('changed', 'warm') + ' <span class="dim">a file changed since your yes: ' + esc(st.replace(/^changed:?\s*/, '')) + '; it is left out until you say yes again</span>' : tag(st || 'not trusted', 'warm') + ' <span class="dim">' + (TRUST_WHY[st] ? esc(TRUST_WHY[st]) : 'nobody said yes to these files: they are left out until you do') + '</span>';
      const dirs = ledgerRows(T, G.trustDirs, X.projects);
      return card('settings.trust.project', 'This project’s own files', cli('trust list', 'trust list'), '<dl class="kv"><dt>directory</dt><dd>' + esc(pr ? pr.dir : S.meta.cwd) + '</dd><dt>state</dt><dd>' + stateHtml + '</dd>' + (pr ? '<dt>digest</dt><dd class="mono">' + esc(pr.digest) + '</dd><dt>unlocks</dt><dd>' + esc(pr.unlocks) + '</dd>' : '') + '</dl>' +
        (files.length ? '<table class="tbl" style="margin-top:10px"><thead><tr><th>file</th><th>kind</th><th class="r">bytes</th><th>hash</th></tr></thead><tbody>' : empty('This project has no file of its own that the harness reads.', 'Its AGENTS.md, skills, commands and hooks are listed here, with their hashes, once it has them.') + '<table class="tbl" hidden><tbody>') + files.map(f => '<tr><td class="bright mono">' + esc(f.path) + '</td><td>' + esc(f.kind || '') + '</td><td class="r num">' + esc(f.bytes) + '</td><td class="mono dim">' + esc(String(f.hash).slice(0, 16)) + '</td></tr>').join('') + '</tbody></table><p class="stubnote" style="margin:10px 0 0">' + esc(T ? T.covers : 'what the harness itself reads as text and settings') + '</p>') +
        card('settings.trust.dirs', 'Every directory you decided about', '<span class="mono">sleipnir trust add | forget | list</span>' + (dirs.some(d => /^(trusted|changed|gone)/.test(d.state)) ? ' <button class="btn sm danger" type="button" data-do="forget-all">Forget all…</button>' : ''), (dirs.length ? '' : empty('No directory yet.', 'A project appears here when you trust its files or a session asks you about them.')) + '<table class="tbl"' + (dirs.length ? '' : ' hidden') + '><thead><tr><th>directory</th><th class="r">files</th><th>state</th><th></th></tr></thead><tbody>' + dirs.map(d => { const ok = /^trusted/.test(d.state); return '<tr><td class="bright mono">' + esc(d.dir) + '</td><td class="r num">' + esc(d.files) + '</td><td>' + tag(d.state, ok ? 'ok' : /gone/.test(d.state) ? '' : 'warm', TRUST_WHY[d.state]) + '</td><td class="r nowrap">' + (ok ? '<button class="btn sm" type="button" data-trust="' + esc(d.dir) + '" data-on="0">Forget</button>' : /gone/.test(d.state) ? '<button class="btn sm" type="button" data-trust="' + esc(d.dir) + '" data-on="0">Forget the entry</button>' : '<button class="btn sm pri" type="button" data-trust="' + esc(d.dir) + '" data-on="1">Trust these files</button>') + '</td></tr>'; }).join('') + '</tbody></table><p class="stubnote" style="margin:10px 0 0">The yes holds until one of the files changes: a changed file is left out until you say yes again. When a session starts in a directory nobody said yes to, it asks: <i>“' + esc(T ? arr(T.question && T.question.options).join(' · ') : 'use this project’s own files?') + '”</i>. Tool output, web pages, file contents and mail never carry a yes.</p>');
    };
    /** The switches of Team & run: [meta key, label, flag, what it does]; the ones a plugin also owns say so (PLUGIN_OF). */
    const RUN_FLAGS = [
      ['commit', 'Commit each merged task', '--commit', 'one git commit for each task merged into your checkout'],
      ['mailman', 'Mailman', '--mailman', 'a small agent turns bursts of worker mail into short digests'],
      ['autoDocs', 'Docs on autopilot', '--auto-docs', 'the docs worker keeps README, docs/ and AGENTS.md current after every accepted task'],
      ['managerApproves', 'Manager approves', '--manager-approves', 'the manager answers the workers’ routine questions (builds, tests, edits in their scope); anything else still asks you'],
      ['noMcp', 'Start without tool servers', '--no-mcp', 'no MCP server starts with the team'],
      ['trustProject', 'Use this project’s own files', '--trust-project', 'its instructions, skills and hooks are read (see Permissions & trust)'],
    ];
    B.run = (S, ST) => {
      const n = ST.run.n == null ? S.meta.swarm : ST.run.n, nW = S.roster.length - 1, M = S.meta, m = S.m || S.wm, defs = defaults(), mx = maxWorkers();
      const line = runLine(M, defs);
      const sw = (k, on) => '<label class="tgl" title="applies when the team starts again"><input type="checkbox" role="switch" id="rf-' + k + '" data-flag="' + k + '"' + (on ? ' checked' : '') + '><span class="trk"></span></label>';
      const flags = RUN_FLAGS.map(([k, l, fl, d]) => { const on = k === 'managerApproves' ? M[k] !== false : M[k]; return row(l, sw(k, on), esc(d) + (PLUGIN_OF[k] ? '<br>' + pluginNote(k) : ''), 'rf-' + k, fl); }).join('');
      const running = row('Running now', '<span class="num">' + (nW ? 'manager + ' + nW + ' worker' + (nW === 1 ? '' : 's') : 'a single agent') + '</span><span class="dim">' + (nW ? calc.active(m) + ' active of ' + nW + ' workers' : '') + '</span>', 'the team of this session as it runs');
      const applyRow = '<div class="row2" style="margin:12px 0 0"><button class="btn pri" type="button" data-do="apply">Apply: start the team again</button><span class="dim">the run is closed (its checkpoints stay) and the conversation carries over</span></div>';
      const teamCard = () => card('settings.run.team', 'The team', '<span class="dim">' + teamWord(teamShown(S).length) + '</span> <span class="dim mono">/swarm · /restart</span>', pendingBar(S) + row('Workers', '<span class="num">' + esc(teamWord(teamShown(S).length)) + '</span>', 'role by role below, with the model of each slot; at least ' + minWorkers() + (mx ? ', at most ' + mx : '') + '. An agent at work keeps its slot', '', '--swarm N') + teamEditor(S) + running + '<div class="row2" style="margin:12px 0 0"><button class="btn" type="button" data-do="apply">Start the team again</button><button class="btn" type="button" data-do="teams" title="save this team under a name, start again as a saved one, or share one as a file (/team, --team NAME)">Saved teams…</button><span class="dim">with the switches below; a change of the team is applied from the bar above</span></div>');
      if (isTeam(S)) return teamCard() + howCards();
      return card('settings.run.team', 'The team', '<span class="dim mono">/swarm · /restart</span>', row('Workers', '<div class="stepper"><button class="btn sm" type="button" data-step="-1" aria-label="fewer workers">−</button><input id="rn" type="number" min="0"' + (mx ? ' max="' + mx + '"' : '') + ' value="' + n + '" aria-label="worker count"><button class="btn sm" type="button" data-step="1" aria-label="more workers">+</button></div><span class="dim" id="rnt">' + teamWord(n) + '</span>', 'how many workers the manager leads: 0 is a single agent; a team has at least 8, one for each role' + (mx ? ', and at most ' + mx : ''), 'rn', '--swarm N') +
        running + applyRow) + howCards();
      function howCards() { return card('settings.run.how', 'How the workers run', '<span class="dim">each change applies when the team starts again</span>', row('Separate checkouts', seg('isolation', [['none', 'one shared checkout'], ['worktree', 'a checkout per worker']], M.isolation, 'Separate checkouts'), 'with a checkout per worker, finished work is checked and merged, then applied to yours at the end', '', '--isolation') +
        row('Check before merging', '<input id="rv" type="text" value="' + esc(M.verify || '') + '" placeholder="go test {dirs}" aria-label="Check command" style="min-width:220px"> <button class="btn" type="button" data-do="verify">Set</button>', 'a command run on every finished task before it is merged; {dirs} is the folders the change touched', 'rv', '--verify') +
        flags + row('Push progress', pluginNote('pushProgress'), 'with a checkout per worker, the integration branch is pushed after every merge; a plugin only, switched on the Plugins page')) +
        card('settings.run.cli', 'The same command line', '', '<p class="stubnote" style="margin:0 0 8px">What starting the team again would run, as the terminal command.</p><pre class="pre cli">' + esc(line) + '</pre><div class="row2" style="margin:0"><button class="btn" type="button" data-do="copycli">Copy the command</button>' + cli('chat', 'chat') + '</div>'); }
    };
    B.mcp = (S, ST) => {
      const det = n => (X.mcp && arr(X.mcp.servers).find(s => s.name === n)) || {};
      return card('settings.mcp.servers', 'Tool servers (MCP)', cli('mcp list', 'mcp list'), (arr(G.mcp).length ? '' : empty('No tool server is configured.', 'A server you add under <span class="mono">"mcp"</span> in your configuration file (or a project’s) appears here with its tools, ready to test and approve.')) + arr(G.mcp).map(s => { const d = det(s.name), st = /fail/.test(s.state) ? 'err' : s.state === 'running' ? 'ok' : s.state === 'off' || s.state === 'disabled' ? '' : 'warm', out = ST.mcp.out[s.name] || (G.mcpOut || {})[s.name], busy = ST.mcp.busy[s.name], dis = busy ? ' disabled' : '', pc = s.state === 'running' ? promptCmds(d, s.name) : [];
        return '<article class="mcpc" data-name="' + esc(s.name) + '"><div class="mcph"><b>' + esc(s.name) + '</b>' + tag(s.state, st) + '<span class="dim">' + esc(s.transport) + ' · ' + esc(s.origin) + '</span><span class="sp"></span>' + (s.state === 'needs approval' ? '<button class="btn sm pri" type="button" data-mcp="approve" data-n="' + esc(s.name) + '"' + dis + '>Approve</button>' : '') + '<button class="btn sm" type="button" data-mcp="test" data-n="' + esc(s.name) + '"' + dis + '>Test</button><button class="btn sm" type="button" data-mcp="reconnect" data-n="' + esc(s.name) + '"' + (s.state === 'off' || s.state === 'disabled' || busy ? ' disabled' : '') + '>Reconnect</button><button class="btn sm danger" type="button" data-mcp="revoke" data-n="' + esc(s.name) + '"' + (s.state === 'needs approval' || busy ? ' disabled' : '') + '>Revoke</button></div>' +
        (d.command ? '<div class="mcpl mono">' + esc(d.command + ' ' + arr(d.args).join(' ')) + (d.server ? ' <span class="dim">· ' + esc(d.server) + '</span>' : '') + '</div>' : d.server ? '<div class="mcpl mono">' + esc(d.server) + '</div>' : '') + (arr(d.envRefs).length ? '<div class="mcpn dim">reads from the environment (names only): ' + d.envRefs.map(e => '<span class="mono">' + esc(e) + '</span>').join(', ') + '</div>' : '') + (d.describe && s.state === 'needs approval' ? '<div class="mcpn warm">' + esc(d.describe) + ': each project entry needs your approval; it ends when the entry changes.</div>' : '') + (d.error && /fail/.test(s.state) ? '<div class="mcpn err mono">' + esc(d.error) + '</div>' : '') + (d.note && !/fail/.test(s.state) ? '<div class="mcpn dim">' + esc(d.note) + '</div>' : '') +
        '<div class="mcpt">' + (arr(s.tools).length ? s.tools.map(t => '<span class="tag">' + esc(t) + '</span>').join('') : '<span class="dim">no tools: ' + (s.state === 'off' || s.state === 'disabled' ? 'turned off' : s.state === 'needs approval' ? 'listed after approval' : /^(not started|refused|connecting|restarting)/.test(s.state) ? 'not started in this session' : 'none connected') + '</span>') + '</div>' +
        (pc.length ? '<div class="mcpt">' + pc.map(c => '<span class="tag" title="' + esc(c.desc || 'a prompt of this server: a slash command') + '">/' + esc(c.cmd) + '</span> <button class="btn sm" type="button" data-pcmd="' + esc(c.cmd) + '">Insert in the chat</button>').join(' ') + '</div>' : '') +
        (out ? '<div class="mcpo ' + esc(out.cls) + '">' + esc(out.t) + (out.cls === 'ok' && /^approved/.test(out.t) ? ' <button class="btn sm" type="button" data-do="mcp-restart" title="start the team again now: the conversation carries over">Start again now</button>' : '') + '</div>' : '') + '</article>'; }).join('') +
        '<p class="stubnote" style="margin:10px 0 0">Every agent is sent the same tool list; what an agent may do is restricted at run time (permissions, leases), never by hiding tools. ' + (X.mcp && X.mcp.sessionNote ? esc(X.mcp.sessionNote) + '. ' : '') + 'Tool output is data, not instructions.</p>');
    };
    B.skills = (S, ST) => {
      const sk = arr(X.skills), cm = arr(X.commands), hk = X.hooks;
      return card('settings.skills.skills', 'Skills the model can load', '<span class="dim mono">/skills</span>' + (X.skillsBudget ? ' · listing ' + esc(X.skillsBudget.used) + ' of ' + esc(X.skillsBudget.tokens) + ' tokens' : ''), '<p class="stubnote" style="margin:0 0 8px">' + pluginNote('builtinSkills') + ' <span class="dim">the skills that ship with Sleipnir</span></p>' + (sk.length ? '' : empty('No skill yet.', 'A folder with a SKILL.md in ~/.sleipnir/skills or the project’s .sleipnir/skills appears here; the model loads it when a task needs it.')) + '<table class="tbl"' + (sk.length ? '' : ' hidden') + '><thead><tr><th>skill</th><th>what it is for</th><th>scope</th><th>from</th></tr></thead><tbody>' + sk.map(s => '<tr><td class="bright mono">' + esc(s.name) + (s.argumentHint ? ' <span class="dim">' + esc(s.argumentHint) + '</span>' : '') + '</td><td>' + esc(s.summary) + (s.youOnly ? ' ' + tag('only you', 'warm', s.note || '') : '') + '</td><td>' + esc(s.scope || '') + '</td><td class="dim mono">' + esc(s.source) + '</td></tr>').join('') + '</tbody></table>' + (X.skillsBudget ? '<p class="stubnote" style="margin:8px 0 0">' + esc(X.skillsBudget.note) + '.</p>' : '')) +
        card('settings.skills.commands', 'Your commands', '<span class="dim"><span class="mono">/help</span> lists them</span>', cm.length ? '<table class="tbl"><thead><tr><th>command</th><th>what it does</th><th>from</th><th></th></tr></thead><tbody>' + cm.map(c => '<tr><td class="bright mono">/' + esc(c.name) + ' <span class="dim">' + esc(c.argumentHint || '') + '</span></td><td>' + esc(c.description) + '</td><td class="dim mono">' + esc(c.source) + '</td><td class="r"><button class="btn sm" type="button" data-cmd="' + esc(c.name) + '">Insert in the chat</button></td></tr>').join('') + '</tbody></table>' : empty('No command of your own yet.', 'A markdown file in ~/.sleipnir/commands or the project’s .sleipnir/commands becomes a slash command and appears here.')) +
        card('settings.skills.hooks', 'Hooks', '<span>' + (hk ? arr(hk.configured).length : 0) + ' configured</span>', hk && arr(hk.configured).length ? '<table class="tbl"><thead><tr><th>event</th><th>matcher</th><th>command</th><th>why</th></tr></thead><tbody>' + hk.configured.map(h => '<tr><td class="bright mono">' + esc(h.event) + '</td><td class="mono">' + esc(h.matcher) + '</td><td class="mono dim">' + esc(h.command) + ' <span class="dim">· ' + esc(h.timeout) + 's</span></td><td>' + esc(h.purpose) + '<small class="rn">' + esc(h.origin) + '</small></td></tr>').join('') + '</tbody></table><p class="stubnote" style="margin:8px 0 0">Events a hook can attach to: ' + arr(hk.events).map(e => '<span class="mono">' + esc(e) + '</span>').join(' · ') + '. A hook from a project runs only when its file is trusted.</p>' : empty('No hook is configured.', 'A hook you add under <span class="mono">"hooks"</span> in a configuration file appears here with the event it runs on; a project’s hooks run once you trust its files.'));
    };
    B.providers = (S, ST) => {
      const provs = arr(G.providers);
      return card('settings.providers.providers', 'Providers', cli('login', 'login') + ' ' + cli('models', 'models'), (provs.length ? '' : empty('No provider yet.', 'The providers of your configuration appear here, each with whether a key is there; <span class="mono">sleipnir login</span> signs in to one.')) + '<table class="tbl"' + (provs.length ? '' : ' hidden') + '><thead><tr><th>provider</th><th>key</th><th>base URL</th><th>used by</th><th></th></tr></thead><tbody>' + provs.map(p => { const ki = keyInfo(p), st = tag(ki.txt, ki.cls);
        return '<tr><td class="bright">' + esc(p.name) + (p.recommended ? ' ' + tag('default', 'fe') : '') + '</td><td>' + st + '<small class="rn">' + esc(p.keyWhere || (p.key === 'env' ? 'the environment variable ' + p.env : p.state)) + (p.who ? ' · ' + esc(p.who) : '') + '</small></td><td class="mono dim">' + esc(p.base) + '<small class="rn">' + esc(p.dialect || '') + '</small></td><td>' + esc(p.usedBy || '') + '</td><td class="r nowrap">' + (ki.btn === 'in' ? '<button class="btn sm pri" type="button" data-prov="' + esc(p.id) + '">Sign in…</button>' : ki.btn === 'out' ? '<button class="btn sm" type="button" data-prov="' + esc(p.id) + '"' + (ki.env ? ' disabled title="the key comes from the environment variable ' + esc(p.env) + ': unset it there"' : '') + '>Sign out</button>' : '') + '</td></tr>'; }).join('') + '</tbody></table><p class="stubnote" style="margin:10px 0 0">Add a key with <b>Sign in…</b>, or <span class="mono">sleipnir login PROVIDER</span> in a terminal; a key is never shown again, and the ChatGPT plan signs in from a terminal. ' + esc(X.providerNote || '') + '</p>');
    };
    B.config = (S, ST) => {
      const C = X.config, eff = C ? arr(C.effective) : [], q = ST.cfg.q.trim().toLowerCase(), M = S.meta, warns = C ? configWarnings(C) : [];
      const over = M.mode ? { 'permissions.mode': [M.mode, '/mode'], 'models.default': [M.model, '/model'], 'swarm.isolation': [M.isolation, '--isolation'], 'swarm.budget_usd': [M.budget || 'off', '/budget'] } : {};
      const rows = eff.filter(r => !q || r.key.toLowerCase().includes(q) || String(r.file).toLowerCase().includes(q) || String(r.layer).includes(q));
      const left = C ? arr(C.risks).map(issueRow).map(r => (r.where ? r.where + ': ' : '') + r.text).concat(arr(C.layers).filter(l => typeof l.trusted === 'string' && l.trusted).map(l => l.kind + ': ' + l.trusted)) : [];
      const closing = C && typeof C.valid === 'string' && C.valid ? C.valid : (warns.length ? 'configuration is valid, with ' + warns.length + ' warning' + (warns.length === 1 ? '' : 's') : 'configuration is valid');
      return card('settings.config.layers', 'Layers, lowest to highest', cli('config', 'config'), C ? '<ol class="layers">' + arr(C.layers).map((l, i) => '<li><b>' + esc(l.kind) + '</b><span class="mono dim">' + esc(l.source) + '</span>' + tag(l.state, l.state === 'active' || l.state === 'found' || l.state === 'applied' ? 'ok' : '') + (l.trusted && typeof l.trusted === 'string' ? '<small class="rn">' + esc(l.trusted) + '</small>' : '') + '</li>').join('') + '</ol><p class="stubnote" style="margin:8px 0 0">' + esc(C.precedence || 'defaults → user → project → local → environment → flags') + '. Security-sensitive keys from a project apply only once you said yes to its files.</p>' : empty('The configuration has not arrived yet.', 'Its files appear here, lowest to highest, as soon as the server sends them; <span class="mono">sleipnir config</span> shows the same in a terminal.')) +
        (C ? card('settings.config.warnings', 'Warnings', '<span>' + (warns.length ? warns.length + ' warning' + (warns.length === 1 ? '' : 's') : 'none') + '</span>', (warns.length ? '<table class="tbl"><thead><tr><th>where</th><th>message</th></tr></thead><tbody>' + warns.map(w => '<tr><td class="mono dim">' + esc(w.where) + '</td><td' + (w.sev === 'error' ? ' class="err"' : '') + '>' + esc(w.text) + '</td></tr>').join('') + '</tbody></table>' : '') + (left.length ? '<p class="stubnote" style="margin:' + (warns.length ? 10 : 0) + 'px 0 0">Left out until you trust the project’s files: ' + left.map(l => '<span class="mono">' + esc(l) + '</span>').join(' · ') + '</p>' : '') + '<p class="stubnote ' + (C.ok === false ? 'err' : warns.length ? 'warm' : 'ok') + '" style="margin:' + (warns.length || left.length ? 8 : 0) + 'px 0 0">' + esc(closing) + '</p>') : card('settings.config.warnings', 'Warnings', '', empty('No configuration yet.', 'What the configuration warns about appears here once the server sends it.'))) +
        card('settings.config.values', 'Effective values and where each came from', '<span>' + rows.length + ' of ' + eff.length + ' keys</span>', '<div class="field-row"><input id="cq" type="search" placeholder="filter keys, files, layers" aria-label="Filter config keys" value="' + esc(ST.cfg.q) + '" autocomplete="off"></div><div class="settable"><table class="tbl"><thead><tr><th>key</th><th>value</th><th>from</th></tr></thead><tbody>' + rows.map(r => { const o = over[r.key]; return '<tr><td class="bright mono">' + esc(r.key) + '</td><td class="mono">' + esc(o ? o[0] : typeof r.value === 'object' ? JSON.stringify(r.value) : r.value) + (o && String(o[0]) !== String(r.value) ? ' <span class="dim">was ' + esc(r.value) + '</span>' : '') + '</td><td>' + (o && String(o[0]) !== String(r.value) ? '<b class="fe">this session</b> <span class="dim mono">' + esc(o[1]) + '</span>' : '<span class="lyr ' + esc(r.layer) + '">' + esc(r.layer) + '</span> <span class="dim mono">' + esc(r.file) + '</span>') + (r.note ? '<small class="rn">' + esc(r.note) + '</small>' : '') + (r.below ? '<small class="rn">below: ' + esc(Object.keys(r.below).map(k => k + ' ' + r.below[k]).join(' · ')) + '</small>' : '') + '</td></tr>'; }).join('') + (rows.length ? '' : '<tr><td colspan="3" class="dim">' + (eff.length ? 'No key matches the filter.' : 'Every configuration key, with the file that set it, appears here once the server sends them.') + '</td></tr>') + '</tbody></table></div>');
    };
    /** What the browser says about notifications, beside the setting: nothing while they work or are off. */
    const notifyState = st => {
      const N = typeof window !== 'undefined' && typeof window.Notification === 'function' ? window.Notification : null;
      if (!N) return ' <span class="warm">this browser cannot show notifications</span>';
      return st.notify === 'on' && N.permission !== 'granted' ? ' <span class="warm">the browser does not allow them for this page</span>' : '';
    };
    B.look = (S, ST) => {
      const st = SL.settings, addr = (SL.live && SL.live.addr) || (hello().server && hello().server.addr) || location.host, reduceSrv = !!(hello().ui && hello().ui.reduceMotion);
      return card('settings.look.motion', 'Motion and display', '<span class="dim">saved in this browser</span>', row('Motion', seg('motion', [['auto', 'follow the system'], ['reduce', 'reduce'], ['full', 'full']], st.motion, 'Motion'), 'reduce stills the horse and skips arcs and flashes; the data still flows' + (reduceSrv ? '; the server asks for less motion (SLEIPNIR_ANIM=0 or REDUCE_MOTION=1), so “follow the system” reduces too' : '')) + row('Density', seg('density', ['comfortable', 'compact'], st.density, 'Density'), 'how much room the lists and cards take') + row('Cache details', seg('cache', [['quiet', 'Quiet'], ['full', 'Full']], st.cache, 'Cache details'), 'Quiet shows keeps cache figures out of the top bar, with one calm line when the cache breaks; Full adds the cache reuse and warm rings to the top bar, the shared-prefix bar and the saved estimate. The Cache view always has everything') +
        row('Tab title', seg('title', [['on', 'on'], ['off', 'off']], titleOn() ? 'on' : 'off', 'Tab title'), 'the tab’s title shows <b>(? N)</b> while a question waits and ✓ when a long turn ended out of sight (off when the server runs with SLEIPNIR_BELL=0)') +
        row('Question notification', seg('notify', [['off', 'off'], ['on', 'on']], st.notify === 'on' ? 'on' : 'off', 'Question notification') + '<span class="dim nfafter">after <input id="notifySec" type="number" min="5" max="3600" step="5" value="' + esc(st.notifyAfter || '30') + '" aria-label="Seconds a question waits before the notification"> s</span>' + notifyState(st), 'a browser notification when a question waits this long while the page is out of sight (a click on it opens the question), and one when a session finishes a turn then', 'notifySec')) +
        card('settings.look.layout', 'Layout', '', row('Left rail', seg('navmode', [['rail', 'icons and labels'], ['wide', 'wide, with full labels']], ui.nav && ui.nav.wide ? 'wide' : 'rail', 'Left rail'), 'the list of views on the left; its own button at the bottom collapses it') + row('Radio rail', seg('radiomode', [['open', 'open'], ['min', 'collapsed']], ui.rail && ui.rail.isOpen ? 'open' : 'min', 'Radio rail') + ' <button class="btn sm" type="button" data-do="railreset">Reset the width (344 px)</button>', 'the manager’s conversation on the right; drag its left edge (or ← → on it) to resize')) +
        card('settings.look.tour', 'Cards and tour', '<span class="dim mono">~/.sleipnir/web-layout.json</span>', row('Cockpit layout', '<button class="btn sm" type="button" data-do="dash-edit">Customise the Cockpit</button> <button class="btn sm" type="button" data-do="dash-reset">Reset the layout</button>', 'move, resize, add and hide the Cockpit\'s cards; every page has a Customise button of its own') +
          row('Every page', '<button class="btn sm" type="button" data-do="layout-export">Export layout</button> <button class="btn sm" type="button" data-do="layout-import">Import layout</button><input type="file" id="layoutFile" accept="application/json,.json" hidden aria-label="Layout file to import"> <button class="btn sm danger" type="button" data-do="layout-reset-all">Reset all pages</button>', 'the cards of every page, kept for you by the server; a file of them moves them to another machine') +
          row('Header and rail', '<button class="btn sm" type="button" data-do="chrome-menu" aria-haspopup="menu">Customise header &amp; rail</button>', 'hide widgets of the header and the status bar, and parts of the Radio rail') +
          row('Guided tour', '<button class="btn sm" type="button" data-do="tour">Take the tour</button>', 'the parts of the page, one at a time; it runs by itself the first time')) +
        card('settings.look.security', 'Security semantics', '', '<dl class="kv"><dt>approval quiet period</dt><dd>the harness waits <b>350 ms</b> of a quiet keyboard before a question takes keys (the server enforces it too); this page waits <b>0.8 s</b> so the meter is visible. Text typed ahead goes to the prompt and never answers.</dd><dt>permission modes</dt><dd>the chat box has a dropdown for default, accept-edits and plan; shift+tab in the message box cycles those three and never enters bypass or yolo. A new session can start in bypass or yolo; inside a running chat they need their name typed.</dd><dt>untrusted text</dt><dd>tool output, file contents, mail and web pages are shown as text; mail is data, not instructions.</dd><dt>connection</dt><dd class="mono">' + esc(addr) + ' · loopback · signed in with a session cookie this page cannot read</dd></dl>') +
        card('settings.look.keys', 'Keys', '<span class="dim">the message box\'s keys: the page has no other shortcuts</span>', '<dl class="kv keys2">' + D.shortcuts.map(([k, d]) => '<dt>' + esc(k) + '</dt><dd>' + esc(d) + '</dd>').join('') + '</dl>');
    };

  /** The pages: each draws its parts, in order. */
  const P = {};
  PAGES.forEach(([id, , , , parts]) => { P[id] = (S, ST) => parts.map(p => B[p](S, ST)).join(''); });
  TK.settings.pages = P; TK.settings.parts = B;
  /** the state of the page: the sub-page, the filters of Models, the permission tester, the Run settings stepper, the MCP lines */
  const newState = params => { const id = (params && params.page) || ui.setPage || 'models'; return { page: pageOf(id), focus: ALIAS[id] ? ALIAS[id][1] : null, models: { q: '', tools: false, reasoning: false, favs: false, all: false, maxp: '', maxo: '', minc: '', role: 'manager', view: null, busy: false }, perm: { tool: 'Bash', arg: 'go test ./api/...', res: null, danger: null, busy: false, seq: 0 }, cfg: { q: '' }, run: { n: null, dirty: {} }, mcp: { out: {}, busy: {} }, eff: {}, cmdq: '' }; };
  TK.settings.newState = newState;
  /** Load what the parts of page id read (a page of an earlier version names the data it reads: SL.data.PAGES). */
  const loadFor = (id, o) => { if (!SL.data) return; const def = PAGES.find(p => p[0] === id); (def ? def[4] : [id]).forEach(p => SL.data.forPage(p, o)); };

  function mount(sc, root, params, S0) {
    /* the page is a page of cards (83b-ui-dash.js): one for each page of Settings, its cards drawn by the builders above. Drawn as one
     * card of another page (params.__only) it has no list of pages and no heading: only that card. */
    const one = ui.dash.modeOf(params) === 'only', ST = newState(params);
    root.innerHTML = one ? '<div class="setpage setone"><div class="setdash"></div></div>' : '<div class="setv2"><nav class="setnav2" aria-label="Settings pages">' + ['session', 'shared', 'browser'].map(g => '<div class="sng"><span class="sngh" id="sg-' + g + '">' + GROUPS[g] + (g === 'session' ? ' <b class="sess"></b>' : '') + '</span><div role="group" aria-labelledby="sg-' + g + '">' + PAGES.filter(p => p[2] === g).map(p => '<button type="button" class="snb" data-page="' + p[0] + '"><b>' + esc(p[1]) + '</b><small>' + esc(p[5]) + '</small></button>').join('') + (g === 'shared' ? '<button type="button" class="snb snb-ext" data-go="plugins" title="the Plugins page: turn parts of Sleipnir on and off"><b>Plugins ›</b><small>turn parts of Sleipnir on and off</small></button>' : '') + '</div></div>').join('') + '</nav><div class="setmain"><div class="setpage" tabindex="-1"><div class="sethost"></div><div class="setdash"></div></div></div></div>';
    const page = $('.setpage', root), nav = $('.setnav2', root), headEl = $('.sethost', root);
    const DP = ui.dash.page(sc, $('.setdash', root), S0, 'settings.' + ST.page, params, { sizeEl: page });
    const toast = ui.toast;
    let lastH = null;

    const budTick = S => {
      const c = calc.totals(S.wm), e = $('#budSpent', page);
      if (e) {
        e.textContent = U.fmtSpend(c, 4); $('#budSpentLbl', page).textContent = budLabel(c); $('#budBar', page).parentNode.classList.toggle('plan', !!c.plan);
        /* a plan bills nothing per token: the gauge fills with what was billed, and says the plan counts against no budget when that is nothing */
        $('#budBar', page).style.width = U.budgetPct(c, S.meta.budget) + '%'; $('#budPct', page).textContent = budNote(c, S.meta.budget); $('#budSaved', page).textContent = fmtUsd(c.saved, 3);
        $('#budSavedLbl', page).textContent = c.savedPartial ? 'Saved at least' : 'Saved est. at list price'; const un = $('#budUnpriced', page), n = Number(c.unpriced) || 0; un.hidden = !n; un.textContent = n ? fmtN(n) + ' tokens at unknown prices are not counted in the cost.' : '';
      }
      if (!$('#budTbl tbody', page)) return;
      const h = !S.wm.order.length ? '<tr><td colspan="5" class="dim">The tokens and cost of each agent appear here after the first request of the session.</td></tr>' : S.wm.order.map(id => { const A = S.wm.ag[id], a = calc.agent(A); return '<tr><td class="bright" style="color:' + agColor(id) + '">' + esc(id) + '</td><td class="r num">' + fmtN(a.prompt) + '</td><td class="r num qfull ' + U.hitCls(a.pct) + '">' + (a.prompt ? a.pct + '%' : '–') + '</td><td class="r num">' + fmtN(a.out) + '</td><td class="r num">' + U.fmtSpend(a, 4) + '</td></tr>'; }).join('') + '<tr class="tot"><td>all</td><td class="r num">' + fmtN(c.prompt) + '</td><td class="r num qfull">' + c.pct + '%</td><td class="r num">' + fmtN(c.out) + '</td><td class="r num">' + U.fmtSpend(c, 4) + U.planTag(c) + '</td></tr>';
      const tb = $('#budTbl tbody', page); if (tb._h !== h) { tb._h = h; tb.innerHTML = h; }
    };
    /* ---------------- paint ---------------- */
    const sess = $('.sess', root);
    /** the heading of the page (not drawn for one card) and its cards: each top-level data-slot section goes to its card */
    function draw(head, body) {
      if (headEl && headEl._h !== head) { headEl._h = head; headEl.innerHTML = head; }
      DP.put(body);
    }
    const markNav = () => { if (nav) $$('.snb', nav).forEach(b => b.setAttribute('aria-current', String(b.dataset.page === ST.page))); };
    /** with no session (the server hosts no tab) the pages of a session have nothing to show: Appearance does, the rest say what to do */
    function paintEmpty() {
      if (sess) sess.textContent = ''; markNav();
      const def = PAGES.find(p => p[0] === ST.page) || PAGES[0], cards = (SL.dash.PAGES['settings.' + def[0]] || { cards: [] }).cards;
      const head = '<header class="sethead"><h1>' + esc(def[1]) + '</h1><p>' + esc(def[3]) + '</p></header>';
      /* the pages shared by every session and this browser's draw without one; a page of a session says what it will show and how to open one */
      const body = def[2] !== 'session' ? P[def[0]]({ meta: {}, roster: [], wm: { order: [], ag: {} } }, ST) : cards.map((id, i) => '<section class="panel setpanel" data-slot="' + esc(id) + '"><div class="setcard">' + (i ? empty('Shown when a session is open.') : empty('These settings belong to a session, and none is open.', 'Start one with <b>+ New</b> (or ctrl+k): its models, team, budget and permissions appear here.')) + '</div></section>').join('');
      if (lastH !== head + body) { lastH = head + body; draw(head, body); }
    }
    function paint(force) {
      const S = SL.sessions.active; if (!S || !S.m) { paintEmpty(); return; } if (sess) sess.textContent = '· ' + S.name;
      markNav();
      const def = PAGES.find(p => p[0] === ST.page) || PAGES[0], head = '<header class="sethead"><h1>' + esc(def[1]) + '</h1><p>' + esc(def[3]) + (def[2] === 'session' ? ' <b class="sesn">· ' + esc(S.name) + '</b>' : def[2] === 'shared' ? ' <span class="dim">· shared by every session</span>' : '') + '</p></header>';
      const active = document.activeElement, typing = active && page.contains(active) && /^(INPUT|TEXTAREA)$/.test(active.tagName) && active.type !== 'checkbox';
      const body = P[def[0]](S, ST), h = head + body;
      if (lastH !== h && !(typing && !force)) {
        const fid = active && page.contains(active) ? active.id || (active.dataset && (active.dataset.fav || active.dataset.use || active.dataset.seg + active.dataset.v || active.dataset.mcp + active.dataset.n)) : null, sel = active && page.contains(active) && active.selectionStart != null ? [active.selectionStart, active.selectionEnd] : null, top = page.scrollTop;
        lastH = h; draw(head, body); page.scrollTop = top;
        if (fid) { const n = page.querySelector('#' + CSS.escape(fid)) || page.querySelector('[data-fav="' + CSS.escape(fid) + '"],[data-use="' + CSS.escape(fid) + '"]'); if (n) { n.focus({ preventScroll: true }); if (sel && n.setSelectionRange && n.type !== 'number' && n.type !== 'search') try { n.setSelectionRange(sel[0], sel[1]); } catch (e) { /* not a text field */ } } }
      }
      if (ST.page === 'budget') budTick(S);
    }
    /** Bring card id of the page into view (an old page id opened the page that has it now). */
    const reveal = id => setTimeout(() => { if (!sc.alive || !id) return; const el = root.querySelector('.dcard[data-card="' + id + '"]'); if (el && el.scrollIntoView) el.scrollIntoView({ block: 'start' }); }, 0);
    const go = (id0, keep) => { const id = pageOf(id0); ST.page = id; ui.setPage = id; ST.perm.res = null; lastH = null; DP.setPage('settings.' + id); loadFor(id, { loud: true }); paint(true); page.scrollTop = 0; if (ALIAS[id0]) reveal(ALIAS[id0][1]); };


    /* ---------------- input ---------------- */
    /** the result of an action: a refusal before the request is said at once; otherwise the promise of the answer (the data layer says a failure itself) */
    const acted = (r, ok) => { if (r && r.ok === false) { toast(r.why || 'that cannot be done now', 'warm'); return Promise.resolve(null); } return r && r.done ? r.done.then(x => { if (x && x.ok && ok) ok(x.data); return x; }) : Promise.resolve(null); };
    if (nav) sc.listen(nav, 'click', e => { const b = e.target.closest('[data-page]'); if (b) go(b.dataset.page); else if (e.target.closest('[data-go]')) SL.views.show('plugins'); });
    if (nav) sc.listen(nav, 'keydown', e => { if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return; const bs = $$('.snb', nav), i = bs.indexOf(document.activeElement); if (i < 0) return; e.preventDefault(); bs[(i + (e.key === 'ArrowDown' ? 1 : bs.length - 1)) % bs.length].focus(); });
    sc.listen(page, 'click', e => {
      const t = e.target, S = SL.sessions.active, A = SL.act;
      const sg = t.closest('[data-seg]'); if (sg) { const k = sg.dataset.seg, v = sg.dataset.v;
        if (k === 'effort') { acted(A.setEffort(v), d => { ST.eff[S.id] = d || {}; toast('effort: ' + v + (d && d.applied && d.applied !== v ? ' (this model: ' + d.applied + ')' : ''), 'ok'); paint(true); }); }
        else if (k === 'isolation') { ST.run.dirty[S.id] = true; acted(A.setIsolation(v)); } else if (k === 'motion') { A.setMotion(v); ui.applyMotion(); } else if (k === 'density') A.setDensity(v); else if (k === 'cache') A.setCache(v); else if (k === 'title') A.setTitleBadge(v); else if (k === 'notify') { ui.approvals.setNotify(v === 'on').then(() => paint(true)); } else if (k === 'navmode') { ui.nav.setWide(v === 'wide'); } else if (k === 'radiomode') { v === 'open' ? ui.rail.expand() : ui.rail.collapse(); } paint(true); return; }
      const fl = t.closest('[data-flt]'); if (fl) { const k = fl.dataset.flt; ST.models[k] = !ST.models[k]; if (k === 'all') loadModels({ all: ST.models.all, refresh: false }); paint(true); return; }
      const fv = t.closest('[data-fav]'); if (fv) { acted(A.favModel(fv.dataset.fav), d => { if (d && d.note) toast(String(d.note), 'warm'); }); paint(true); return; }
      const df = t.closest('[data-def]'); if (df) { const { role } = savedFor(ST.models.role); acted(A.saveDefaultModel(df.dataset.def, role === 'manager' && !isTeam(S) ? 'default' : role), d => { toast(d && d.over ? String(d.over) : df.dataset.def + ' is the default of ' + role + ', saved in ' + ((d && d.file) || D.saveFile || '~/.sleipnir/config.json'), d && d.over ? 'warm' : 'ok'); if (d && d.note) toast(String(d.note), 'warm'); paint(true); }); return; }
      const us = t.closest('[data-use]'); if (us) { stageTarget(S, ST.models.role, us.dataset.use); paint(true); return; }
      const tc = t.closest('[data-tcount]'); if (tc) { const why = stepRole(S, tc.dataset.tcount, +tc.dataset.d, maxWorkers()); if (why) toast(why, 'warm'); paint(true); return; }
      const md = t.closest('[data-mode]'); if (md) { acted(A.setMode(md.dataset.mode), () => toast('mode: ' + md.dataset.mode)); return; }
      const dg = t.closest('[data-danger]'); if (dg) { ST.perm.danger = dg.dataset.danger; paint(true); const i = $('#cfIn', page); if (i) i.focus(); return; }
      const rm = t.closest('[data-rm]'); if (rm) { acted(A.removeRule(rm.dataset.rm), () => toast('rule removed: ' + rm.dataset.rm)); return; }
      const dofn = t.closest('[data-do]'); if (dofn) { if (dofn.dataset.do === 'open-plugins') { SL.views.show('plugins', { focus: dofn.dataset.plg || '' }); return; } return doIt(dofn.dataset.do, S); }
      const tr = t.closest('[data-trust]'); if (tr) { if (tr.dataset.on === '1') trustFlow(tr.dataset.trust); else acted(A.trustDir(tr.dataset.trust, false), () => toast('forgot ' + tr.dataset.trust, 'ok')); return; }
      const st = t.closest('[data-step]'); if (st) { const i = $('#rn', page); ST.run.n = clampWorkers((parseInt(i.value, 10) || 0) + +st.dataset.step, maxWorkers(), +st.dataset.step); i.value = ST.run.n; $('#rnt', page).textContent = teamWord(ST.run.n); return; }
      const mc = t.closest('[data-mcp]'); if (mc) return mcpDo(mc.dataset.mcp, mc.dataset.n);
      const cmd = t.closest('[data-cmd]'); if (cmd) { ui.setDraft('/' + cmd.dataset.cmd + ' '); toast('inserted in the Radio composer'); return; }
      const pcm = t.closest('[data-pcmd]'); if (pcm) { ui.setDraft('/' + pcm.dataset.pcmd + ' '); toast('inserted in the Radio composer'); return; }
      const pv = t.closest('[data-prov]'); if (pv) { const p = arr(G.providers).find(x => x.id === pv.dataset.prov); if (!p) return; if (keyInfo(p).btn === 'in') signIn(p); else signOut(p); return; }
      const cl = t.closest('[data-cli]'); if (cl) { if (cl.hasAttribute('data-cli-models')) { const c = modelsCli(ST.models); ui.runCli(['models'], c.flags, false, c.pos); } else ui.runCli(cl.dataset.cli.split(' ')); return; }
    });
    sc.listen(page, 'change', e => {
      const t = e.target, S = SL.sessions.active, A = SL.act;
      if (t.dataset.sel) { ST.models[t.dataset.sel] = t.value; paint(true); } else if (t.dataset.role) { stageModel(S, t.dataset.role, t.value); paint(true); }
      else if (t.dataset.tall) { if (t.value) stageSlot(S, t.dataset.tall, -1, t.value); paint(true); } else if (t.dataset.tslot) { stageSlot(S, t.dataset.tslot, +t.dataset.ti, t.value); paint(true); }
      else if (t.dataset.flag) { ST.run.dirty[S.id] = true; acted(A.setFlag(t.dataset.flag, t.checked)); } else if (t.id === 'tryTool') ST.perm.tool = t.value;
      else if (t.id === 'notifySec') { A.setNotify(SL.settings.notify, t.value); t.value = SL.settings.notifyAfter; paint(true); }
      else if (t.id === 'layoutFile' && t.files && t.files[0]) importLayout(t.files[0]);
    });
    /** Export layout: the whole layout as a JSON file the browser saves. */
    function exportLayout() {
      const text = ui.dash.exportText();
      try { const a = document.createElement('a'), url = URL.createObjectURL(new Blob([text], { type: 'application/json' })); a.href = url; a.download = 'sleipnir-web-layout.json'; document.body.appendChild(a); a.click(); a.remove(); setTimeout(() => URL.revokeObjectURL(url), 1000); toast('the layout was exported: sleipnir-web-layout.json', 'ok'); }
      catch (e) { U.copy(text).then(ok => toast(ok ? 'this browser cannot save a file: the layout was copied instead' : 'the layout could not be exported here', ok ? 'warm' : 'err')); }
    }
    /** Import layout: a file exported before replaces the layout of every page (after one confirmation); a file that is not one is refused. */
    function importLayout(file) {
      if (file.size > 256 * 1024) { toast('that file is too large to be a layout', 'err'); return; }
      file.text().then(text => {
        if (!sc.alive) return; const d = SL.dash.importDoc(text); if (d instanceof Error) { toast('not imported: ' + d.message, 'err'); return; }
        const n = Object.keys(d.pages).length;
        ui.confirm({ title: 'Import a layout', kicker: 'layout', text: 'Replace the layout of every page with the one in <b>' + esc(file.name) + '</b> (' + n + ' page' + (n === 1 ? '' : 's') + ')? Pages it does not mention take their default layout.', ok: 'Import', run: () => { const r = ui.dash.importText(text); toast(r === true ? 'the layout was imported' : 'not imported: ' + r, r === true ? 'ok' : 'err'); } });
      }, () => toast('the file could not be read', 'err'));
    }
    sc.listen(page, 'input', e => {
      const t = e.target;
      if (t.id === 'mq') { ST.models.q = t.value; paintKeep(t); } else if (t.id === 'cq') { ST.cfg.q = t.value; paintKeep(t); } else if (t.id === 'cfIn') { $('#cfGo', page).disabled = t.value.trim() !== ST.perm.danger; } else if (t.id === 'tryArg') ST.perm.arg = t.value; else if (t.id === 'rn') { ST.run.n = clampWorkers(t.value, maxWorkers()); $('#rnt', page).textContent = teamWord(ST.run.n); }
    });
    function paintKeep(inp) { const pos = inp.selectionStart, id = inp.id; paint(true); const n = $('#' + id, page); if (n) { n.focus(); try { n.setSelectionRange(pos, pos); } catch (e) { /* no caret in this type */ } } }
    sc.listen(page, 'keydown', e => { const t = e.target; SL.time.noteKey(); if (e.key === 'Enter') { if (t.id === 'bIn') { e.preventDefault(); doIt('budget', SL.sessions.active); } else if (t.id === 'ruleIn') { e.preventDefault(); doIt('rule-add', SL.sessions.active); } else if (t.id === 'tryArg') { e.preventDefault(); doIt('try', SL.sessions.active); } else if (t.id === 'rv') { e.preventDefault(); doIt('verify', SL.sessions.active); } else if (t.id === 'cfIn' && !$('#cfGo', page).disabled) { e.preventDefault(); $('#cfGo', page).click(); } } });
    sc.listen(page, 'click', e => { if (e.target.id === 'cfGo') { const m = ST.perm.danger; ST.perm.danger = null; acted(SL.act.setMode(m, { confirm: m }), () => toast('mode: ' + m + ' (shift+tab will not leave it unasked)', 'err')); paint(true); } });

    /** the team library for S: it saves the team S runs now (GET /api/sessions/{id}/team); a saved team loaded starts the team again
     *  as that one, with every model it names (/team load NAME, as in a terminal), after a yes; an imported one becomes the team chosen
     *  for S, slot by slot with the manager's model, to apply from the bar like any other change */
    function openTeams(S) {
      ui.teams.forSession(S, { stageImported: f => {
        const t = ui.teams.teamOf(f); setTeam(S, t.slots.map(x => ({ role: x.role, model: x.model })));
        if (t.models.manager) stageModel(S, 'manager', t.models.manager);
        toast('the imported team is chosen: apply it from the bar above' + (Object.keys(t.models).some(x => x !== 'manager') ? ' (the models it names for its judge, compactor and the like come with it once it is saved and loaded)' : ''), 'ok'); paint(true);
      } });
    }
    /** apply the changes chosen for S in one request: what the server did (a team that restarts once, or a switch in place) is what the toast says */
    function applyModels(S) {
      const p = pendingModels(S, drafts[S.id]), tc = teamChanges(S); if (!p.length && !tc.length) { delete drafts[S.id]; delete tdrafts[S.id]; toast('no change to apply', 'warm'); paint(true); return; }
      const roles = {}; p.forEach(([r, m]) => { roles[r] = m; }); const n = p.length + tc.length, team = arr(S.roster).length > 1, sent = tc.length ? teamSent(S) : null, td = tdrafts[S.id];
      acted(SL.act.setModels(roles, undefined, sent), d => {
        const d0 = drafts[S.id] || {}; p.forEach(([r, m]) => { if (d0[r] === m) delete d0[r]; }); if (tdrafts[S.id] === td) delete tdrafts[S.id];
        const restarted = d ? !!d.restarted : team || !!sent;
        toast(restarted ? 'the team starts again once with ' + n + ' change' + (n === 1 ? '' : 's') + ', and goes on with its work' : n === 1 ? p[0][0] + ' runs on ' + p[0][1] : n + ' models changed', 'ok'); paint(true);
      });
    }
    /** fetch the catalogue again: all = include models that do not chat, refresh = ask every provider (network); the answer is this page's list */
    async function loadModels(o) {
      const st = ST.models; if (!o.all && !o.refresh) { st.view = null; paint(true); return; }
      st.busy = true; paint(true);
      const q = []; if (o.refresh) q.push('refresh=1'); if (o.all) q.push('all=1');
      const r = await net.get('/api/models?' + q.join('&')); st.busy = false;
      if (!sc.alive) return;
      if (r.ok && r.data) { if (o.refresh) { if (SL.data) await SL.data.load('models', { force: true }); toast('the catalogue was fetched again: ' + arr(r.data.models).length + ' models', 'ok'); } st.view = o.all ? r.data : null; }
      else { net.fail(r, 'the catalogue could not be fetched'); if (o.all) st.all = false; }
      paint(true);
    }
    async function doIt(k, S) {
      const A = SL.act;
      if (k === 'budget' || k === 'budget-off') { const v = k === 'budget-off' ? 'off' : $('#bIn', page).value.trim(), r = A.setBudget(v); if (r && r.ok === false) toast(r.why, 'err'); else acted(r, () => toast(v === 'off' ? 'budget: off' : 'budget: ' + fmtUsd(parseFloat(v), 2), 'ok')); paint(true); }
      else if (k === 'danger-no') { ST.perm.danger = null; paint(true); }
      else if (k === 'rule-add') { const eff = $('#ruleEff', page).value, text = $('#ruleIn', page).value, r = A[eff + 'Rule'](text); if (r && r.ok === false) toast(r.why, 'warm'); else acted(r, () => { toast(eff + ' this session: ' + text.trim() + ' (listed with the origin “this session”)', 'ok'); const i = $('#ruleIn', page); if (i) i.value = ''; }); paint(true); const i = $('#ruleIn', page); if (i) i.focus(); }
      else if (k === 'rule-tests') { const r = A.allowRule('tests'); acted(r, () => toast('allowed this session: tests (' + (r.added || (X.permissions ? X.permissions.testsPreset.rules.length : arr(D.testsPreset).length)) + ' rules)', 'ok')); paint(true); }
      else if (k === 'try') await checkPerm(S);
      else if (k === 'verify') { const v = $('#rv', page).value.trim(); ST.run.dirty[S.id] = true; acted(A.setVerify(v), () => toast('verify: ' + (v || '(none)'), 'ok')); paint(true); }
      else if (k === 'models-apply') applyModels(S);
      else if (k === 'teams') openTeams(S);
      else if (k === 'models-discard') { delete drafts[S.id]; delete tdrafts[S.id]; paint(true); }
      else if (k === 'apply') { const nn = ST.run.n == null ? S.meta.swarm : ST.run.n; if (nn === S.meta.swarm && !ST.run.dirty[S.id]) { toast('already ' + teamWord(nn), 'warm'); return; } ui.confirm({ title: 'Start the team again', text: 'Restart <b>' + esc(S.name) + '</b> as ' + (nn ? 'a manager and <b>' + nn + '</b> worker' + (nn === 1 ? '' : 's') : 'a single agent') + '? The current run is closed (its checkpoints stay) and the conversation carries over.', ok: 'Restart', danger: true, run: () => { const r = A.restartTeam({ swarm: nn, force: true }); acted(r, () => { ST.run.n = null; delete ST.run.dirty[S.id]; toast('the team starts again: ' + teamWord(nn), 'ok'); }); } }); }
      else if (k === 'copycli') U.copy($('.pre.cli', page).textContent).then(ok => toast(ok ? 'command copied' : 'copy is not available here', ok ? 'ok' : 'warm'));
      else if (k === 'railreset') { ui.rail.setWidth(344); }
      else if (k === 'dash-edit') { SL.views.show('cockpit'); ui.dash.setLocked(false); }
      else if (k === 'dash-reset') ui.confirm({ title: 'Reset the Cockpit', kicker: 'layout', text: 'Put back the default layout of the Cockpit? The cards you added leave and its own panels return to their places.', ok: 'Reset', run: () => { ui.dash.reset(); toast('the Cockpit has its default layout', 'ok'); } });
      else if (k === 'layout-export') exportLayout();
      else if (k === 'layout-import') { const f = $('#layoutFile', page); if (f) { f.value = ''; f.click(); } }
      else if (k === 'layout-reset-all') ui.confirm({ title: 'Reset every page', kicker: 'layout', text: 'Put back the default layout of every page, the header and the Radio rail? Hidden cards come back, cards added from other pages leave; the copy the server keeps for you is forgotten too.', ok: 'Reset all pages', danger: true, run: () => { ui.dash.resetAll(); toast('every page has its default layout', 'ok'); } });
      else if (k === 'chrome-menu') { const b = page.querySelector('[data-do="chrome-menu"]'); if (b) ui.chrome.menu(b); }
      else if (k === 'tour') ui.tour.start();
      else if (k === 'refresh-models') loadModels({ all: ST.models.all, refresh: true });
      else if (k === 'forget-all') forgetAll();
      else if (k === 'mcp-restart') { acted(A.restart([]), () => toast('the team starts again: the approved servers start with it', 'ok')); }
    }
    /** "Would it ask?": the server's own classifier answers (it never prompts); the line keeps its previous text until the answer arrives */
    async function checkPerm(S) {
      ST.perm.tool = $('#tryTool', page).value; ST.perm.arg = $('#tryArg', page).value.trim();
      if (!ST.perm.arg) { ST.perm.res = null; paint(true); return; }
      const my = ++ST.perm.seq; ST.perm.busy = true; paint(true);
      const r = await net.post(net.tab(S, '/permissions/check'), { tool: ST.perm.tool, arg: ST.perm.arg });
      if (my !== ST.perm.seq || !sc.alive) return; ST.perm.busy = false;
      if (r.ok && r.data) ST.perm.res = { d: String(r.data.d), why: String(r.data.why), cls: ['ok', 'warm', 'err'].includes(r.data.cls) ? r.data.cls : 'warm' }; else net.fail(r, 'the check could not be made');
      paint(true);
    }
    /** Forget every directory (trust forget --all) after one confirmation that lists them */
    function forgetAll() {
      const dirs = arr(G.trustDirs).filter(d => /^(trusted|changed|gone)/.test(d.state)); if (!dirs.length) return;
      ui.confirm(Object.assign({}, forgetSpec(dirs), { run: async () => { const r = await net.post('/api/trust', { all: true, on: false }); if (!r.ok) { net.fail(r, 'the directories were not forgotten'); return; } if (SL.data) { SL.data.load('trust', { force: true }); SL.data.load('projects', { force: true }); } const n = r.data && r.data.forgot != null ? r.data.forgot : dirs.length; toast('forgot ' + n + ' director' + (n === 1 ? 'y' : 'ies'), 'warm'); } }));
    }
    function signOut(p) { acted(SL.act.setProvider(p.id, { key: 'none' }), () => toast('signed out of ' + p.name, 'ok')); }
    /** Sign in. A provider that takes a key: the key is typed here, sent once (with a confirmation) and kept in auth.json as `sleipnir
     *  login` keeps it; the server tries it first and keeps it only when the provider accepts it; it is never shown again. The ChatGPT
     *  plan signs in in a browser window that the terminal opens: the page shows the command and, when it was run, asks again. */
    function signIn(p) {
      if (!p.env) return signInTerminal(p);
      ui.modal({ title: 'Add a key for ' + p.name, kicker: 'provider key', desc: 'kept in ~/.sleipnir/auth.json, never shown again', color: 'var(--ok)', focus: '#pkIn',
        body: '<form class="pkf" autocomplete="off"><div class="field"><label for="pkIn">' + esc(p.name) + ' API key <span class="dim mono">(' + esc(p.env) + ')</span></label><input id="pkIn" type="password" autocomplete="off" spellcheck="false" aria-label="' + esc(p.name) + ' API key"></div>' +
          '<p class="stubnote">The key goes to this machine\'s Sleipnir once and is kept in <span class="mono">~/.sleipnir/auth.json</span> (mode 0600), as <span class="mono">sleipnir login ' + esc(p.id) + '</span> keeps it. ' + esc(p.name) + ' is asked first whether it takes the key; a key it refuses is not kept. A variable ' + esc(p.env) + ' set in the environment still wins.</p>' +
          '<div class="row2"><button class="btn pri" type="submit" data-pk-save>Save the key</button><button class="btn" type="button" data-close>Cancel</button></div><p class="pk-out" aria-live="polite"></p></form>',
        onMount(b, scm, close) {
          const f = $('form', b), inp = $('#pkIn', b), out = $('.pk-out', b), btn = $('[data-pk-save]', b);
          scm.listen(f, 'submit', async e => {
            e.preventDefault(); const key = inp.value.trim(); if (!key) { inp.focus(); return; }
            if (/\s/.test(key)) { out.textContent = 'a key is one word, as the provider shows it'; out.className = 'pk-out err'; return; }
            btn.disabled = true; out.textContent = 'asking ' + p.name + ' whether it takes the key…'; out.className = 'pk-out dim';
            const r = await net.post('/api/providers/' + encodeURIComponent(p.id) + '/key', { provider: p.id, key, check: true }, { confirm: 'key:' + p.id, timeout: 60000 });
            btn.disabled = false;
            if (!r.ok) { out.textContent = r.message || 'the key was not saved'; out.className = 'pk-out err'; inp.select(); return; }
            inp.value = ''; close();
            if (SL.data) { SL.data.load('providers', { force: true }); SL.data.load('models', { force: true }); }
            toast(p.name + ' is connected: its models are in the list now', 'ok');
          });
        } });
    }
    /** The ChatGPT plan's sign-in, which the terminal does: the command, and a check when it was run */
    function signInTerminal(p) {
      ui.modal({ title: 'Sign in to ' + p.name, kicker: 'terminal flow', desc: 'the ChatGPT plan signs in in a browser window', color: 'var(--ok)', body: '<p class="stubnote">Run this in a terminal; it asks for the key (or opens the ChatGPT sign-in) and stores it in <span class="mono">~/.sleipnir/auth.json</span> with mode 0600. This page then shows the provider as connected.</p><pre class="pre cli">sleipnir login ' + esc(p.id) + '</pre><div class="row2"><button class="btn" type="button" data-copy>Copy the command</button><button class="btn pri" type="button" data-done>I ran it: check again</button><button class="btn" type="button" data-close>Cancel</button></div>',
        onMount(b, scm, close) {
          scm.listen($('[data-copy]', b), 'click', () => U.copy('sleipnir login ' + p.id).then(ok => toast(ok ? 'command copied' : 'copy is not available here', ok ? 'ok' : 'warm')));
          scm.listen($('[data-done]', b), 'click', () => { close(); acted(SL.act.setProvider(p.id, {}), () => { const now = arr(G.providers).find(x => x.id === p.id); if (now && keyInfo(now).btn !== 'in') toast(p.name + ' now shows as connected', 'ok'); else toast(p.name + ' still has no key: run the command in a terminal first', 'warm'); }); });
        } });
    }
    /** approve, revoke, test or reconnect a tool server: the server's one line for the card, and a toast for what changed (an approval or a revocation applies when the team starts again) */
    async function mcpDo(a, n) {
      const r = SL.act.setMcp(n, { action: a }); if (r && r.ok === false) { toast(r.why || 'that cannot be done now', 'warm'); return; }
      ST.mcp.busy[n] = a; paint(true);
      const res = await (r.done || Promise.resolve(null)); delete ST.mcp.busy[n]; if (!sc.alive) return;
      if (res && res.ok && res.data && res.data.t) { ST.mcp.out[n] = { t: String(res.data.t), cls: ['ok', 'warm', 'err'].includes(res.data.cls) ? res.data.cls : 'warm' };
        if (a === 'approve') toast('approved ' + n, 'ok'); else if (a === 'revoke') toast('revoked ' + n, 'warm'); else if (a === 'reconnect') { if (ST.mcp.out[n].cls === 'err') toast(n + ': reconnect failed again', 'err'); else toast('reconnected ' + n + ' ✓', 'ok'); } }
      paint(true);
    }
    sc.update(() => paint(false)); sc.frame(() => { if (ST.page === 'budget' && SL.loop.frameNo % 20 === 0) { const S = SL.sessions.active; if (S) budTick(S); } });
    sc.on('mcp-changed', () => paint(false)); sc.on('trust-changed', () => paint(false)); sc.on('plugins-changed', () => paint(false)); sc.on('models-changed', () => paint(false));
    loadFor(ST.page, { loud: true });
    paint(true); if (ST.focus && !one) reveal(ST.focus);
  }
  SL.views.register({ name: 'settings', title: 'Settings', mount });
})(SL);
