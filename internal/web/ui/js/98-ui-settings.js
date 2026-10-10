/* 98-ui-settings.js: Settings, eleven real pages in three groups. A page reads the session or the shared state and changes it only through SL.act
 * (or the server's own routes where the answer is the point), so a change made here shows everywhere at once: the mode chip, the HUD, the
 * footer, the stalls, the Radio rail.
 *   this session   Models · Roles & effort · Budget · Permissions · Trust · Run settings
 *   every session  MCP servers · Skills, commands & hooks · Providers & login · Config layers
 *   this browser   Appearance & motion
 * Text from files, servers, mail and models is data: it goes through esc(). The browser never holds an API key: signing in is the
 * terminal's flow, and this page only learns which providers are connected. Nothing a page shows or keeps is a secret. */
(function (SL) {
  'use strict';
  const U = SL.u, { $, $$, esc, mk, fmtK, fmtUsd, fmtN, agCol } = U, D = SL.D, G = SL.G, X = D.extra, calc = SL.calc, ui = SL.ui = SL.ui || {}, TK = SL.toolkit = SL.toolkit || {};
  const net = TK.net, arr = x => Array.isArray(x) ? x : [];
  const PAGES = [
    ['models', 'Models', 'session', 'the catalogue: filter, star, choose for the manager or a role'],
    ['roles', 'Roles & effort', 'session', 'which model each role runs on, and how hard it thinks'],
    ['budget', 'Budget', 'session', 'dollars for the turns from now on'],
    ['permissions', 'Permissions', 'session', 'the mode and the rules in force'],
    ['trust', 'Trust', 'session', 'what this project’s own files may say to the harness'],
    ['run', 'Run settings', 'session', 'workers, isolation, verify, flags'],
    ['mcp', 'MCP servers', 'shared', 'tool servers: state, tools, approval'],
    ['skills', 'Skills, commands & hooks', 'shared', 'what the model can load and what runs around its tools'],
    ['providers', 'Providers & login', 'shared', 'where each model is reached, and whether a key is there'],
    ['config', 'Config layers', 'shared', 'which file supplied each value'],
    ['look', 'Appearance & motion', 'browser', 'hover, motion, density and the keys'],
  ];
  const GROUPS = { session: 'this session', shared: 'every session', browser: 'this browser' };
  const EFFORTS = ['default', 'none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'];
  const efforts = () => D.efforts && D.efforts.length ? D.efforts : EFFORTS;
  const tag = (t, c, title) => '<span class="tag ' + (c || '') + '"' + (title ? ' title="' + esc(title) + '"' : '') + '>' + esc(t) + '</span>';
  const seg = (name, list, cur, label) => '<div class="seg" role="group" aria-label="' + esc(label || name) + '">' + list.map(v => { const val = Array.isArray(v) ? v[0] : v, lab = Array.isArray(v) ? v[1] : v; return '<button type="button" data-seg="' + name + '" data-v="' + esc(val) + '" aria-pressed="' + (String(val) === String(cur)) + '">' + esc(lab) + '</button>'; }).join('') + '</div>';
  const row = (l, inner, hint, id) => '<div class="setrow"><div class="sl"><label' + (id ? ' for="' + id + '"' : '') + '>' + l + '</label>' + (hint ? '<small>' + hint + '</small>' : '') + '</div><div class="sc2">' + inner + '</div></div>';
  const card = (title, right, inner, cls) => '<section class="panel setpanel ' + (cls || '') + '"><div class="ph"><h2>' + title + '</h2><div class="r">' + (right || '') + '</div></div><div class="setcard">' + inner + '</div></section>';
  const cli = (path, label, attr) => '<button class="btn sm" type="button" data-cli="' + esc(path) + '"' + (attr || '') + ' title="run it in the command runner">Run as CLI <span class="mono">' + esc(label || path) + '</span></button>';
  const roleCol = r => esc('var(--c-' + (D.roles[r] ? D.roles[r].code : 'sc') + ')');
  const agColor = id => esc(agCol(id));
  /** the model a worker of the team runs on (a role without an entry of its own runs on it) */
  const workerModel = S => { const w = arr(S.roster).find(x => x.id !== 'mgr' && x.model); return w ? w.model : S.meta.model; };
  const curModel = (S, role) => role === 'manager' ? S.meta.model : ((S.meta.roleModels || {})[role] || D.roleModels[role] || workerModel(S));
  const ALL_ROLES = () => ['manager'].concat(D.roleOrder);
  const hello = () => TK.hello() || {};
  /** the server's defaults for a new session (hello.defaults), or {} */
  /** the tab title badge as it is now: the person's choice, else the server's default (off when SLEIPNIR_BELL=0) */
  const titleOn = () => { const v = SL.settings.title; return v === 'on' || (v !== 'off' && !(hello().ui && hello().ui.bell === false)); };
  const defaults = () => { const d = (SL.live && typeof SL.live.defaults === 'function' && SL.live.defaults()) || hello().defaults; return d || {}; };
  /** the most workers a team may have: the server's ceiling (config swarm.max_workers) when it says one */
  const maxWorkers = () => Math.max(1, parseInt(defaults().maxWorkers, 10) || 12);

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
    const d = defs || {}, flag = (k, name) => M[k] ? ' --' + name : (d[k] ? ' --' + name + '=false' : '');
    return 'sleipnir chat --model ' + M.model + ' --mode ' + M.mode + ' --swarm ' + M.swarm + ' --isolation ' + M.isolation + (M.verify ? ' --verify "' + M.verify + '"' : '') + (M.budget ? ' --budget-usd ' + M.budget : '') + flag('commit', 'commit') + flag('mailman', 'mailman') + flag('noMcp', 'no-mcp') + flag('trustProject', 'trust-project');
  }
  const clampWorkers = (v, max) => Math.max(0, Math.min(max || 12, parseInt(v, 10) || 0));
  const teamWord = n => n === 0 ? 'single agent' : 'manager + ' + n + ' worker' + (n === 1 ? '' : 's') + (n > 8 ? ' (' + (n - 8) + ' share leg' + (n - 8 === 1 ? '' : 's') + ')' : '');
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
  TK.settings = { trustFlow, trustPost, forgetSpec, projectState, TRUST_WHY, modelRows, modelsCli, rulesIn, dayTxt, keyInfo, runLine, clampWorkers, teamWord, ledgerRows, warningRow, issueRow, configWarnings, promptCmds, curModel, PAGES };

  /* ---------------- the pages: pure builders of markup from the session S and the page state ST ---------------- */
  const P = {};

    P.models = (S, ST) => {
      const st = ST.models, favs = G.favs || new Set(), all = st.view ? st.view.models : D.models, cur = curModel(S, st.role), rows = modelRows(all, st, favs);
      const users = ref => ALL_ROLES().filter(r => curModel(S, r) === ref), errs = arr(st.view ? st.view.errors : D.modelErrors), cl = modelsCli(st);
      const opt = (list, v) => list.map(o => '<option value="' + o[0] + '"' + (v === o[0] ? ' selected' : '') + '>' + o[1] + '</option>').join('');
      return '<div class="setfilter"><input id="mq" type="search" placeholder="filter: every word must appear, any case" aria-label="Filter models" value="' + esc(st.q) + '" autocomplete="off"><div class="seg" role="group" aria-label="Filters"><button type="button" data-flt="tools" aria-pressed="' + st.tools + '">tools</button><button type="button" data-flt="reasoning" aria-pressed="' + st.reasoning + '">reasoning</button><button type="button" data-flt="favs" aria-pressed="' + st.favs + '">★ favourites</button><button type="button" data-flt="all" aria-pressed="' + st.all + '" title="include models that do not chat (--all)">all</button></div>' +
        '<button class="btn sm" type="button" data-do="refresh-models"' + (st.busy ? ' disabled' : '') + ' title="asks every provider again; the list is kept 6 h">' + (st.busy ? 'Fetching…' : 'Refresh the catalogue') + '</button>' +
        '<span style="display:inline-flex;align-items:center;gap:8px;white-space:nowrap"><label class="lab" for="mxp">max input $/M</label><select id="mxp" data-sel="maxp">' + opt([['', 'any'], ['0.5', '0.5'], ['1', '1'], ['3', '3'], ['5', '5']], st.maxp) + '</select></span><span style="display:inline-flex;align-items:center;gap:8px;white-space:nowrap"><label class="lab" for="mxo">max output $/M</label><select id="mxo" data-sel="maxo" title="--max-price: the price of the output">' + opt([['', 'any'], ['2', '2'], ['5', '5'], ['10', '10'], ['25', '25']], st.maxo) + '</select></span><span style="display:inline-flex;align-items:center;gap:8px;white-space:nowrap"><label class="lab" for="mnc">min context</label><select id="mnc" data-sel="minc">' + opt([['', 'any'], ['64000', '64k'], ['128000', '128k'], ['400000', '400k']], st.minc) + '</select></span>' +
        '<span style="display:inline-flex;align-items:center;gap:8px;white-space:nowrap"><label class="lab" for="mrl">choose for</label><select id="mrl" data-sel="role">' + ALL_ROLES().map(r => '<option' + (st.role === r ? ' selected' : '') + '>' + esc(r) + '</option>').join('') + '</select></span></div>' +
        (D.modelsNote ? '<p class="stubnote warm" style="margin:0 0 8px">' + esc(D.modelsNote) + '</p>' : '') + (errs.length ? '<p class="stubnote warm" style="margin:0 0 8px">' + errs.length + ' source' + (errs.length === 1 ? '' : 's') + ' of the catalogue could not be read: ' + esc(errs.join(' · ')) + '</p>' : '') +
        '<div class="settable"><table class="tbl"><thead><tr><th></th><th>model</th><th class="r">context</th><th class="r">in $/M</th><th class="r">out $/M</th><th>tags</th><th>runs</th><th></th></tr></thead><tbody>' + rows.map(mm => '<tr class="' + (mm.ref === cur ? 'sel' : '') + '"><td><button type="button" class="star' + (favs.has(mm.ref) ? ' on' : '') + '" data-fav="' + esc(mm.ref) + '" aria-pressed="' + favs.has(mm.ref) + '" aria-label="' + (favs.has(mm.ref) ? 'unstar ' : 'star ') + esc(mm.ref) + '">' + (favs.has(mm.ref) ? '★' : '☆') + '</button></td><td class="bright">' + esc(mm.ref) + '</td><td class="r">' + fmtK(mm.ctx) + '</td><td class="r">' + (mm.in == null ? '<span class="dim">price unknown</span>' : mm.in.toFixed(2)) + '</td><td class="r">' + (mm.out == null ? '' : mm.out.toFixed(2)) + '</td><td>' + (mm.tools ? tag('tools', 'fe') : '') + (mm.reasoning ? tag('reasoning', 'fe') : '') + '</td><td>' + users(mm.ref).map(r => '<span class="rrc" style="color:' + roleCol(r) + '">' + esc(r) + '</span>').join(' ') + '</td><td class="r"><button type="button" class="btn sm' + (mm.ref === cur ? '' : ' pri') + '" data-use="' + esc(mm.ref) + '"' + (mm.ref === cur ? ' disabled' : '') + '>' + (mm.ref === cur ? 'current' : 'Use for ' + esc(st.role)) + '</button></td></tr>').join('') + (rows.length ? '' : '<tr><td colspan="8" class="dim">no model matches</td></tr>') + '</tbody></table></div>' +
        '<p class="stubnote">' + rows.length + ' of ' + arr(all).length + ' models. Choosing the manager’s model starts the team again on it; choosing for a role restarts that role’s workers. Prices are the catalogue’s; <b>price unknown</b> means the catalogue does not say. ' + cli('models', cl.label, ' data-cli-models') + '</p>';
    };
    P.roles = (S, ST) => {
      const m = S.m || S.wm, ef = ST.eff[S.id] || {}, rowsH = ALL_ROLES().map(r => { const ws = S.roster.filter(x => x.role === r && x.id !== 'mgr' || (r === 'manager' && x.id === 'mgr')), cur = curModel(S, r), unk = (D.model(cur) || {}).in == null;
        return '<tr><td style="color:' + roleCol(r) + ';font-weight:700;width:120px">' + esc(r) + '</td><td class="dim" style="width:150px">' + (ws.length ? ws.map(w => '<span style="color:' + agColor(w.id) + '">' + esc(w.id) + '</span>').join(' ') : '<span class="faint">none on the team</span>') + '</td><td><select data-role="' + esc(r) + '" aria-label="model for ' + esc(r) + '">' + (D.models.some(mm => mm.ref === cur) ? '' : '<option selected>' + esc(cur) + '</option>') + D.models.map(mm => '<option' + (mm.ref === cur ? ' selected' : '') + '>' + esc(mm.ref) + '</option>').join('') + '</select>' + (unk ? ' ' + tag('price unknown', 'warm') : '') + '</td></tr>'; }).join('');
      const mailman = '<tr><td style="color:var(--warm);font-weight:700">mailman</td><td class="dim">harness service</td><td><span class="dim">' + (S.meta.mailman ? 'runs on the worker model; digests worker mail (not on the roster, board or legs)' : 'off: worker mail is delivered at once (--mailman turns it on)') + '</span></td></tr>';
      const applied = ef.applied && ef.applied !== S.meta.effort ? ' · this model: <b>' + esc(ef.applied) + '</b>' : '';
      return card('Which model each role runs on', cli('models', 'models'), '<table class="tbl"><tbody>' + rowsH + mailman + '</tbody></table><p class="stubnote" style="margin:10px 0 0">A role without its own entry runs on the worker model; changing a role restarts its workers on it, keeping the manager’s conversation and the board. <span class="mono">--role-model ROLE=MODEL</span> at start does the same.</p>') +
        card('Reasoning effort', '<span class="mono">/effort</span>', row('Effort', seg('effort', efforts(), S.meta.effort, 'Reasoning effort'), 'the harness maps a level to the closest one each model supports') + '<p class="stubnote" style="margin:0">Current: <b id="effNow">' + esc(S.meta.effort) + '</b>' + applied + '. Effort applies to the turns from now on; it never changes the cached prefix.</p>');
    };
    P.budget = (S, ST) => {
      const c = calc.totals(S.wm); return card('This session’s budget', '<span class="mono">--budget-usd</span> · <span class="mono">/budget</span>', '<div class="bigrow"><div><div class="big num" id="budSpent">' + fmtUsd(c.cost, 4) + '</div><small>spent</small></div><div><div class="big num dimv">' + (S.meta.budget ? fmtUsd(S.meta.budget, 2) : 'off') + '</div><small>budget</small></div><div class="gaugecol"><div class="gauge wide"><i id="budBar" style="width:' + (S.meta.budget ? Math.min(100, c.cost / S.meta.budget * 100) : 0) + '%"></i></div><small id="budPct">' + (S.meta.budget ? Math.round(c.cost / S.meta.budget * 100) + '% used' : 'no limit') + '</small></div></div>' +
        row('Set the budget', '<input id="bIn" type="text" inputmode="decimal" value="' + (S.meta.budget ? S.meta.budget.toFixed(2) : 'off') + '" aria-label="Budget in dollars" style="width:110px"> <button class="btn pri" type="button" data-do="budget">Set</button> <button class="btn" type="button" data-do="budget-off">Off</button>', 'a number of dollars, or off; when the budget is reached the turn and the goal pause themselves', 'bIn')) +
        card('Cost by agent', '<span class="mono">/cost</span>', '<table class="tbl" id="budTbl"><thead><tr><th>agent</th><th class="r">prompt tok</th><th class="r qfull">hit</th><th class="r">out tok</th><th class="r">cost</th></tr></thead><tbody></tbody></table><p class="stubnote qfull" style="margin:8px 0 0"><span id="budSavedLbl">Saved est. at list price</span>: <b id="budSaved"></b> (an estimate).</p><p class="stubnote" id="budUnpriced" hidden style="margin:8px 0 0"></p><p class="stubnote" style="margin:8px 0 0">Every figure derives from one table of tokens per agent, so this page, the HUD, the stalls and the Cache view agree.</p>');
    };
    P.permissions = (S, ST) => {
      const PM = X.permissions, modes = PM ? PM.modes : D.modes.map(m => ({ id: m.id, danger: !!m.danger, cycle: !m.danger, text: m.desc })), rs = rulesIn(S), res = ST.perm.res, dg = ST.perm.danger;
      const modeRows = modes.filter(m => !m.danger).map(m => '<button type="button" class="mrow3" data-mode="' + esc(m.id) + '" aria-pressed="' + (S.meta.mode === m.id) + '"><b class="mono">' + esc(m.id) + '</b><span>' + esc(m.text) + '</span>' + (S.meta.mode === m.id ? tag('current', 'ok') : '') + '</button>').join('');
      const dng = modes.filter(m => m.danger).map(m => '<button type="button" class="mrow3 dng" data-danger="' + esc(m.id) + '" aria-pressed="' + (S.meta.mode === m.id) + '"><b class="mono">⚠ ' + esc(m.id) + '</b><span>' + esc(m.text) + '</span>' + (S.meta.mode === m.id ? tag('current', 'err') : '') + '</button>').join('');
      return card('Mode', '<span class="mono">/mode</span> · <span class="mono">shift+tab</span>', '<div class="modes3">' + modeRows + '</div><p class="stubnote" style="margin:8px 0 0"><kbd>shift</kbd> <kbd>tab</kbd> steps <b>default → accept-edits → plan → default</b> and never enters bypass or yolo; those two are set only by typing their name below (or <span class="mono">/mode bypass</span>, <span class="mono">--mode</span>). <span class="dim">--continue never brings them back.</span></p>') +
        '<section class="panel setpanel danger"><div class="ph"><h2>⚠ Dangerous modes</h2><div class="r"><span class="err">asks nothing</span></div></div><div class="setcard"><div class="modes3">' + dng + '</div>' + (dg ? '<div class="dangerbox"><span>' + (dg === 'yolo' ? 'yolo asks nothing and ignores the ask rules; for sandboxes and runs with nobody there. Type <b>yolo</b> to confirm.' : 'bypass asks nothing except about the very dangerous (sudo, a recursive delete, a forced push ...); deny rules still apply. Type <b>bypass</b> to confirm.') + '</span><input id="cfIn" type="text" autocomplete="off" aria-label="Type the mode name to confirm" placeholder="type ' + esc(dg) + '"><div class="row2"><button class="btn danger" type="button" id="cfGo" disabled>Set ' + esc(dg) + '</button><button class="btn" type="button" data-do="danger-no">Cancel</button></div></div>' : '') + '</div></section>' +
        card('The manager', '', '<p class="stubnote" style="margin:0">' + esc(PM ? PM.managerWrites.note : 'the manager edits no file') + '. A write or a writing shell command from the manager is refused at run time with: <i>“' + esc(PM ? PM.managerWrites.text : 'spawn a worker for the change') + '”</i> <button class="btn sm" type="button" data-do="mgr-refused">See the refused Edit</button></p>') +
        card('Rules in force', '<span>' + rs.length + ' rules · ' + rs.filter(r => !r.fixed).length + ' from this session</span>', '<table class="tbl"><thead><tr><th>effect</th><th>rule</th><th>from</th><th></th></tr></thead><tbody>' + rs.map(r => '<tr><td>' + tag(r.effect, r.effect === 'deny' ? 'err' : r.effect === 'ask' ? 'warm' : 'ok') + '</td><td class="bright mono">' + esc(r.rule) + (r.note ? '<small class="rn">' + esc(r.note) + '</small>' : '') + '</td><td>' + (r.fixed ? esc(r.origin) + (r.file ? ' <span class="dim mono">' + esc(r.file) + '</span>' : '') : '<b class="' + (r.origin === "don't ask again" ? 'warm' : 'fe') + '">' + esc(r.origin) + '</b> <span class="dim">· this session</span>') + '</td><td class="r">' + (r.fixed ? '<span class="dim" title="built in or from a file: change it there">locked</span>' : '<button class="btn sm" type="button" data-rm="' + esc(r.rule) + '">remove</button>') + '</td></tr>').join('') + '</tbody></table>' +
          '<div class="field-row" style="margin-top:12px"><select id="ruleEff" aria-label="Rule effect"><option>allow</option><option>deny</option><option>ask</option></select><input id="ruleIn" type="text" placeholder="Bash(go test:*) · Edit(./docs/**) · tests" aria-label="Rule"><button class="btn pri" type="button" data-do="rule-add">Add for this session</button><button class="btn" type="button" data-do="rule-tests" title="' + esc((PM ? PM.testsPreset.summary : '')) + '">+ the tests preset</button></div><p class="stubnote" style="margin:6px 0 0">An answer 2 (“don’t ask again”) adds an allow rule with the origin <b class="warm">don’t ask again</b>. <span class="mono">tests</span> expands to ' + (PM ? PM.testsPreset.rules.length : arr(D.testsPreset).length) + ' rules: build and test commands, never install or run. Deny rules from a project can add to yours, never remove from it.</p>') +
        card('Would it ask?', '<span class="mono">the order: ' + esc(PM ? PM.order.map(o => o.split(' (')[0]).join(' → ') : 'deny → ask → high-risk → allow → mode') + '</span>', '<div class="field-row"><select id="tryTool" aria-label="Tool"><option' + (ST.perm.tool === 'Bash' ? ' selected' : '') + '>Bash</option><option' + (ST.perm.tool === 'Edit' ? ' selected' : '') + '>Edit</option><option' + (ST.perm.tool === 'Read' ? ' selected' : '') + '>Read</option></select><input id="tryArg" type="text" value="' + esc(ST.perm.arg) + '" aria-label="Command or path" placeholder="npm install --save-dev vitest · api/server.go · ./.env"><button class="btn" type="button" data-do="try"' + (ST.perm.busy ? ' disabled' : '') + '>Check</button></div><div class="tryres" id="tryRes" aria-live="polite">' + (res ? '<span class="tag ' + esc(res.cls) + '">' + esc(res.d) + '</span> ' + esc(res.why) : '<span class="dim">Type a command or a path and see which rule decides, in the order above.</span>') + '</div>');
    };
    P.trust = (S, ST) => {
      const T = X.trust, pr = T && T.project, files = T ? arr(T.files) : [], used = !!S.meta.trustProject, st = pr ? pr.state : '';
      const stateHtml = !used ? tag('not used', 'warm') + ' <span class="dim">--trust-project is off for this session: the project’s own files are left out</span>' : st === 'trusted' || !pr ? tag('trusted', 'ok') + ' <span class="dim">you said yes' + (pr && pr.savedDay ? ' on ' + esc(dayTxt(pr.savedDay)) : '') + '; the hashes are unchanged</span>' : /^changed/.test(st) ? tag('changed', 'warm') + ' <span class="dim">a file changed since your yes: ' + esc(st.replace(/^changed:?\s*/, '')) + '; it is left out until you say yes again</span>' : tag(st || 'not trusted', 'warm') + ' <span class="dim">' + (TRUST_WHY[st] ? esc(TRUST_WHY[st]) : 'nobody said yes to these files: they are left out until you do') + '</span>';
      const dirs = ledgerRows(T, G.trustDirs, X.projects);
      return card('This project', cli('trust list', 'trust list'), '<dl class="kv"><dt>directory</dt><dd>' + esc(pr ? pr.dir : S.meta.cwd) + '</dd><dt>state</dt><dd>' + stateHtml + '</dd>' + (pr ? '<dt>digest</dt><dd class="mono">' + esc(pr.digest) + '</dd><dt>unlocks</dt><dd>' + esc(pr.unlocks) + '</dd>' : '') + '</dl>' +
        '<table class="tbl" style="margin-top:10px"><thead><tr><th>file</th><th>kind</th><th class="r">bytes</th><th>hash</th></tr></thead><tbody>' + files.map(f => '<tr><td class="bright mono">' + esc(f.path) + '</td><td>' + esc(f.kind || '') + '</td><td class="r num">' + esc(f.bytes) + '</td><td class="mono dim">' + esc(String(f.hash).slice(0, 16)) + '</td></tr>').join('') + '</tbody></table><p class="stubnote" style="margin:10px 0 0">' + esc(T ? T.covers : 'what the harness itself reads as text and settings') + '</p>') +
        card('Every directory you decided about', '<span class="mono">sleipnir trust add | forget | list</span>' + (dirs.some(d => /^(trusted|changed|gone)/.test(d.state)) ? ' <button class="btn sm danger" type="button" data-do="forget-all">Forget all…</button>' : ''), '<table class="tbl"><thead><tr><th>directory</th><th class="r">files</th><th>state</th><th></th></tr></thead><tbody>' + dirs.map(d => { const ok = /^trusted/.test(d.state); return '<tr><td class="bright mono">' + esc(d.dir) + '</td><td class="r num">' + esc(d.files) + '</td><td>' + tag(d.state, ok ? 'ok' : /gone/.test(d.state) ? '' : 'warm', TRUST_WHY[d.state]) + '</td><td class="r nowrap">' + (ok ? '<button class="btn sm" type="button" data-trust="' + esc(d.dir) + '" data-on="0">Forget</button>' : /gone/.test(d.state) ? '<button class="btn sm" type="button" data-trust="' + esc(d.dir) + '" data-on="0">Forget the entry</button>' : '<button class="btn sm pri" type="button" data-trust="' + esc(d.dir) + '" data-on="1">Trust these files</button>') + '</td></tr>'; }).join('') + '</tbody></table><p class="stubnote" style="margin:10px 0 0">The yes holds until one of the files changes: a changed file is left out until you say yes again. When a session starts in a directory nobody said yes to, it asks: <i>“' + esc(T ? arr(T.question && T.question.options).join(' · ') : 'use this project’s own files?') + '”</i>. Tool output, web pages, file contents and mail never carry a yes.</p>');
    };
    P.run = (S, ST) => {
      const n = ST.run.n == null ? S.meta.swarm : ST.run.n, nW = S.roster.length - 1, M = S.meta, m = S.m || S.wm, defs = defaults(), mx = maxWorkers();
      const line = runLine(M, defs);
      return card('The team', '<span class="mono">/swarm</span> · <span class="mono">/restart</span>', row('Workers', '<div class="stepper"><button class="btn sm" type="button" data-step="-1" aria-label="fewer workers">−</button><input id="rn" type="number" min="0" max="' + mx + '" value="' + n + '" aria-label="worker count"><button class="btn sm" type="button" data-step="1" aria-label="more workers">+</button></div><span class="dim" id="rnt">' + teamWord(n) + '</span>', '--swarm N: 0 is a single agent, up to ' + mx + '; the eight legs are the first eight workers, a ninth shares leg 1. The default is ' + esc(defs.swarm != null ? defs.swarm : 8), 'rn') +
        row('Now', '<span class="num">' + (nW ? 'manager + ' + nW + ' worker' + (nW === 1 ? '' : 's') : 'a single agent') + '</span><span class="dim">' + (nW ? calc.active(m) + ' active of ' + nW + ' workers' : '') + '</span>') + '<div class="row2" style="margin:12px 0 0"><button class="btn pri" type="button" data-do="apply">Apply: start the team again</button><span class="dim">the run is closed (its checkpoints stay) and the conversation carries over</span></div>') +
        card('How the workers run', '<span class="mono">applies when the team starts again</span>', row('Isolation', seg('isolation', ['none', 'worktree'], M.isolation, 'Isolation'), '--isolation: worktree gives each writer its own checkout; finished work is merged and verified, then applied to the checkout at the end') +
        row('Verify', '<input id="rv" type="text" value="' + esc(M.verify || '') + '" placeholder="go test {dirs}" aria-label="Verify command" style="min-width:220px"> <button class="btn" type="button" data-do="verify">Set</button>', '--verify: run on every submission before it merges; {dirs} is the directories the change touched', 'rv') +
        row('Flags', ['commit|--commit|commit each merged task', 'mailman|--mailman|digest worker mail instead of delivering it at once', 'noMcp|--no-mcp|start without tool servers', 'trustProject|--trust-project|use the repository’s own instructions, skills and hooks'].map(f => { const [k, fl, d] = f.split('|'); return '<label class="chk" title="' + esc(d) + '"><input type="checkbox" data-flag="' + k + '"' + (M[k] ? ' checked' : '') + '> ' + fl + '</label>'; }).join(''), 'each takes effect when the team starts again')) +
        card('The same command line', '', '<pre class="pre cli">' + esc(line) + '</pre><div class="row2" style="margin:0"><button class="btn" type="button" data-do="copycli">Copy the command</button>' + cli('chat', 'chat') + '</div>');
    };
    P.mcp = (S, ST) => {
      const det = n => (X.mcp && arr(X.mcp.servers).find(s => s.name === n)) || {};
      return card('Tool servers', cli('mcp list', 'mcp list'), G.mcp.map(s => { const d = det(s.name), st = /fail/.test(s.state) ? 'err' : s.state === 'running' ? 'ok' : s.state === 'off' || s.state === 'disabled' ? '' : 'warm', out = ST.mcp.out[s.name] || (G.mcpOut || {})[s.name], busy = ST.mcp.busy[s.name], dis = busy ? ' disabled' : '', pc = s.state === 'running' ? promptCmds(d, s.name) : [];
        return '<article class="mcpc" data-name="' + esc(s.name) + '"><div class="mcph"><b>' + esc(s.name) + '</b>' + tag(s.state, st) + '<span class="dim">' + esc(s.transport) + ' · ' + esc(s.origin) + '</span><span class="sp"></span>' + (s.state === 'needs approval' ? '<button class="btn sm pri" type="button" data-mcp="approve" data-n="' + esc(s.name) + '"' + dis + '>Approve</button>' : '') + '<button class="btn sm" type="button" data-mcp="test" data-n="' + esc(s.name) + '"' + dis + '>Test</button><button class="btn sm" type="button" data-mcp="reconnect" data-n="' + esc(s.name) + '"' + (s.state === 'off' || s.state === 'disabled' || busy ? ' disabled' : '') + '>Reconnect</button><button class="btn sm danger" type="button" data-mcp="revoke" data-n="' + esc(s.name) + '"' + (s.state === 'needs approval' || busy ? ' disabled' : '') + '>Revoke</button></div>' +
        (d.command ? '<div class="mcpl mono">' + esc(d.command + ' ' + arr(d.args).join(' ')) + (d.server ? ' <span class="dim">· ' + esc(d.server) + '</span>' : '') + '</div>' : d.server ? '<div class="mcpl mono">' + esc(d.server) + '</div>' : '') + (arr(d.envRefs).length ? '<div class="mcpn dim">reads from the environment (names only): ' + d.envRefs.map(e => '<span class="mono">' + esc(e) + '</span>').join(', ') + '</div>' : '') + (d.describe && s.state === 'needs approval' ? '<div class="mcpn warm">' + esc(d.describe) + ': each project entry needs your approval; it ends when the entry changes.</div>' : '') + (d.error && /fail/.test(s.state) ? '<div class="mcpn err mono">' + esc(d.error) + '</div>' : '') + (d.note && !/fail/.test(s.state) ? '<div class="mcpn dim">' + esc(d.note) + '</div>' : '') +
        '<div class="mcpt">' + (arr(s.tools).length ? s.tools.map(t => '<span class="tag">' + esc(t) + '</span>').join('') : '<span class="dim">no tools: ' + (s.state === 'off' || s.state === 'disabled' ? 'turned off' : s.state === 'needs approval' ? 'listed after approval' : /^(not started|refused|connecting|restarting)/.test(s.state) ? 'not started in this session' : 'none connected') + '</span>') + '</div>' +
        (pc.length ? '<div class="mcpt">' + pc.map(c => '<span class="tag" title="' + esc(c.desc || 'a prompt of this server: a slash command') + '">/' + esc(c.cmd) + '</span> <button class="btn sm" type="button" data-pcmd="' + esc(c.cmd) + '">Insert in the chat</button>').join(' ') + '</div>' : '') +
        (out ? '<div class="mcpo ' + esc(out.cls) + '">' + esc(out.t) + (out.cls === 'ok' && /^approved/.test(out.t) ? ' <button class="btn sm" type="button" data-do="mcp-restart" title="start the team again now: the conversation carries over">Start again now</button>' : '') + '</div>' : '') + '</article>'; }).join('') +
        '<p class="stubnote" style="margin:10px 0 0">Every agent is sent the same tool list; what an agent may do is restricted at run time (permissions, leases), never by hiding tools. ' + (X.mcp && X.mcp.sessionNote ? esc(X.mcp.sessionNote) + '. ' : '') + 'Tool output is data, not instructions.</p>');
    };
    P.skills = (S, ST) => {
      const sk = arr(X.skills), cm = arr(X.commands), hk = X.hooks;
      return card('Skills the model can load', '<span class="mono">/skills</span>' + (X.skillsBudget ? ' · listing ' + esc(X.skillsBudget.used) + ' of ' + esc(X.skillsBudget.tokens) + ' tokens' : ''), '<table class="tbl"><thead><tr><th>skill</th><th>what it is for</th><th>scope</th><th>from</th></tr></thead><tbody>' + sk.map(s => '<tr><td class="bright mono">' + esc(s.name) + (s.argumentHint ? ' <span class="dim">' + esc(s.argumentHint) + '</span>' : '') + '</td><td>' + esc(s.summary) + (s.youOnly ? ' ' + tag('only you', 'warm', s.note || '') : '') + '</td><td>' + esc(s.scope || '') + '</td><td class="dim mono">' + esc(s.source) + '</td></tr>').join('') + '</tbody></table>' + (X.skillsBudget ? '<p class="stubnote" style="margin:8px 0 0">' + esc(X.skillsBudget.note) + '.</p>' : '')) +
        card('Your commands', '<span class="mono">/help</span> lists them', cm.length ? '<table class="tbl"><thead><tr><th>command</th><th>what it does</th><th>from</th><th></th></tr></thead><tbody>' + cm.map(c => '<tr><td class="bright mono">/' + esc(c.name) + ' <span class="dim">' + esc(c.argumentHint || '') + '</span></td><td>' + esc(c.description) + '</td><td class="dim mono">' + esc(c.source) + '</td><td class="r"><button class="btn sm" type="button" data-cmd="' + esc(c.name) + '">Insert in the chat</button></td></tr>').join('') + '</tbody></table>' : '<p class="stubnote" style="margin:0">No custom command: a markdown file in ~/.sleipnir/commands or .sleipnir/commands becomes one.</p>') +
        card('Hooks', '<span>' + (hk ? arr(hk.configured).length : 0) + ' configured</span>', hk && arr(hk.configured).length ? '<table class="tbl"><thead><tr><th>event</th><th>matcher</th><th>command</th><th>why</th></tr></thead><tbody>' + hk.configured.map(h => '<tr><td class="bright mono">' + esc(h.event) + '</td><td class="mono">' + esc(h.matcher) + '</td><td class="mono dim">' + esc(h.command) + ' <span class="dim">· ' + esc(h.timeout) + 's</span></td><td>' + esc(h.purpose) + '<small class="rn">' + esc(h.origin) + '</small></td></tr>').join('') + '</tbody></table><p class="stubnote" style="margin:8px 0 0">Events a hook can attach to: ' + arr(hk.events).map(e => '<span class="mono">' + esc(e) + '</span>').join(' · ') + '. A hook from a project runs only when its file is trusted.</p>' : '<p class="stubnote" style="margin:0">No hooks.</p>');
    };
    P.providers = (S, ST) => {
      const provs = arr(G.providers);
      return card('Providers', cli('login', 'login') + ' ' + cli('models', 'models'), '<table class="tbl"><thead><tr><th>provider</th><th>key</th><th>base URL</th><th>used by</th><th></th></tr></thead><tbody>' + provs.map(p => { const ki = keyInfo(p), st = tag(ki.txt, ki.cls);
        return '<tr><td class="bright">' + esc(p.name) + (p.recommended ? ' ' + tag('default', 'fe') : '') + '</td><td>' + st + '<small class="rn">' + esc(p.keyWhere || (p.key === 'env' ? 'the environment variable ' + p.env : p.state)) + (p.who ? ' · ' + esc(p.who) : '') + '</small></td><td class="mono dim">' + esc(p.base) + '<small class="rn">' + esc(p.dialect || '') + '</small></td><td>' + esc(p.usedBy || '') + '</td><td class="r nowrap">' + (ki.btn === 'in' ? '<button class="btn sm pri" type="button" data-prov="' + esc(p.id) + '">Sign in…</button>' : ki.btn === 'out' ? '<button class="btn sm" type="button" data-prov="' + esc(p.id) + '"' + (ki.env ? ' disabled title="the key comes from the environment variable ' + esc(p.env) + ': unset it there"' : '') + '>Sign out</button>' : '') + '</td></tr>'; }).join('') + '</tbody></table><p class="stubnote" style="margin:10px 0 0">The browser never sees an API key. Signing in is the terminal’s flow (<span class="mono">sleipnir login PROVIDER</span>: a key prompt, or the ChatGPT sign-in); this page only learns which providers are connected. Keys are kept in <span class="mono">~/.sleipnir/auth.json</span> (mode 0600) or read from the environment. ' + esc(X.providerNote || '') + '</p>');
    };
    P.config = (S, ST) => {
      const C = X.config, eff = C ? arr(C.effective) : [], q = ST.cfg.q.trim().toLowerCase(), M = S.meta, warns = C ? configWarnings(C) : [];
      const over = { 'permissions.mode': [M.mode, '/mode'], 'models.default': [M.model, '/model'], 'swarm.isolation': [M.isolation, '--isolation'], 'swarm.budget_usd': [M.budget || 'off', '/budget'] };
      const rows = eff.filter(r => !q || r.key.toLowerCase().includes(q) || String(r.file).toLowerCase().includes(q) || String(r.layer).includes(q));
      const left = C ? arr(C.risks).map(issueRow).map(r => (r.where ? r.where + ': ' : '') + r.text).concat(arr(C.layers).filter(l => typeof l.trusted === 'string' && l.trusted).map(l => l.kind + ': ' + l.trusted)) : [];
      const closing = C && typeof C.valid === 'string' && C.valid ? C.valid : (warns.length ? 'configuration is valid, with ' + warns.length + ' warning' + (warns.length === 1 ? '' : 's') : 'configuration is valid');
      return card('Layers, lowest to highest', cli('config', 'config'), C ? '<ol class="layers">' + arr(C.layers).map((l, i) => '<li><b>' + esc(l.kind) + '</b><span class="mono dim">' + esc(l.source) + '</span>' + tag(l.state, l.state === 'active' || l.state === 'found' || l.state === 'applied' ? 'ok' : '') + (l.trusted && typeof l.trusted === 'string' ? '<small class="rn">' + esc(l.trusted) + '</small>' : '') + '</li>').join('') + '</ol><p class="stubnote" style="margin:8px 0 0">' + esc(C.precedence || 'defaults → user → project → local → environment → flags') + '. Security-sensitive keys from a project apply only once you said yes to its files.</p>' : '<p class="stubnote">No layer data.</p>') +
        (C ? card('Warnings', '<span>' + (warns.length ? warns.length + ' warning' + (warns.length === 1 ? '' : 's') : 'none') + '</span>', (warns.length ? '<table class="tbl"><thead><tr><th>where</th><th>message</th></tr></thead><tbody>' + warns.map(w => '<tr><td class="mono dim">' + esc(w.where) + '</td><td' + (w.sev === 'error' ? ' class="err"' : '') + '>' + esc(w.text) + '</td></tr>').join('') + '</tbody></table>' : '') + (left.length ? '<p class="stubnote" style="margin:' + (warns.length ? 10 : 0) + 'px 0 0">Left out until you trust the project’s files: ' + left.map(l => '<span class="mono">' + esc(l) + '</span>').join(' · ') + '</p>' : '') + '<p class="stubnote ' + (C.ok === false ? 'err' : warns.length ? 'warm' : 'ok') + '" style="margin:' + (warns.length || left.length ? 8 : 0) + 'px 0 0">' + esc(closing) + '</p>') : '') +
        card('Effective values and where each came from', '<span>' + rows.length + ' of ' + eff.length + ' keys</span>', '<div class="field-row"><input id="cq" type="search" placeholder="filter keys, files, layers" aria-label="Filter config keys" value="' + esc(ST.cfg.q) + '" autocomplete="off"></div><div class="settable"><table class="tbl"><thead><tr><th>key</th><th>value</th><th>from</th></tr></thead><tbody>' + rows.map(r => { const o = over[r.key]; return '<tr><td class="bright mono">' + esc(r.key) + '</td><td class="mono">' + esc(o ? o[0] : typeof r.value === 'object' ? JSON.stringify(r.value) : r.value) + (o && String(o[0]) !== String(r.value) ? ' <span class="dim">was ' + esc(r.value) + '</span>' : '') + '</td><td>' + (o && String(o[0]) !== String(r.value) ? '<b class="fe">this session</b> <span class="dim mono">' + esc(o[1]) + '</span>' : '<span class="lyr ' + esc(r.layer) + '">' + esc(r.layer) + '</span> <span class="dim mono">' + esc(r.file) + '</span>') + (r.note ? '<small class="rn">' + esc(r.note) + '</small>' : '') + (r.below ? '<small class="rn">below: ' + esc(Object.keys(r.below).map(k => k + ' ' + r.below[k]).join(' · ')) + '</small>' : '') + '</td></tr>'; }).join('') + '</tbody></table></div>');
    };
    P.look = (S, ST) => {
      const st = SL.settings, addr = (SL.live && SL.live.addr) || (hello().server && hello().server.addr) || location.host, reduceSrv = !!(hello().ui && hello().ui.reduceMotion);
      return card('Hover behaviour', '<span class="mono">saved in this browser</span>', row('Hover', seg('hover', [['both', 'hold on chat + slow on linked items'], ['chat', 'hold on chat only'], ['off', 'off']], st.hover, 'Hover behaviour'), 'over the chat the whole screen holds (a gradual stop, then a bounded catch-up on release); over a stall, leg, row or card it slows to 30%. Space over the chat pins the hold; Esc releases it') +
        row('Motion', seg('motion', [['auto', 'follow the system'], ['reduce', 'reduce'], ['full', 'full']], st.motion, 'Motion'), 'reduced: the horse stands, arcs and flashes are skipped; the data still flows' + (reduceSrv ? '. “Follow the system” also reduces here: the server runs with SLEIPNIR_ANIM=0 or REDUCE_MOTION=1' : '')) + row('Density', seg('density', ['comfortable', 'compact'], st.density, 'Density')) + row('Cache details', seg('cache', [['quiet', 'Quiet'], ['full', 'Full']], st.cache, 'Cache details'), 'Quiet keeps the cache out of the way: a small cache chip in the top bar, a quiet warm clock, no saved or hit figures on the stalls, one calm line in Team activity when a cache breaks. Full adds the shared-prefix bar, the saved estimate and the hit figures. The Cache view always has everything') +
        row('Tab title', seg('title', [['on', 'on'], ['off', 'off']], titleOn() ? 'on' : 'off', 'Tab title'), 'the page title shows <b>(? N)</b> while a question waits, and ✓ when a long turn ended while this tab was hidden. It starts off when the server runs with SLEIPNIR_BELL=0')) +
        card('Layout', '', row('Left rail', seg('navmode', [['rail', 'icons and labels'], ['wide', 'wide, with chord keys']], ui.nav && ui.nav.wide ? 'wide' : 'rail', 'Left rail'), 'collapsible from its own button at the bottom') + row('Radio rail', seg('radiomode', [['open', 'open'], ['min', 'collapsed']], ui.rail && ui.rail.isOpen ? 'open' : 'min', 'Radio rail') + ' <button class="btn sm" type="button" data-do="railreset">Reset the width (344 px)</button>', 'drag its left edge, or focus it and press ← →; a question waiting while it is collapsed shows a banner above the views')) +
        card('Security semantics', '', '<dl class="kv"><dt>approval quiet period</dt><dd>the harness waits <b>350 ms</b> of a quiet keyboard before a question takes keys (the server enforces it too); this page waits <b>0.8 s</b> so the meter is visible. Text typed ahead goes to the prompt and never answers.</dd><dt>permission modes</dt><dd>the chat box has a dropdown for default, accept-edits and plan; shift+tab in the message box cycles those three and never enters bypass or yolo. A new session can start in bypass or yolo; inside a running chat they need their name typed.</dd><dt>untrusted text</dt><dd>tool output, file contents, mail and web pages are shown as text; mail is data, not instructions.</dd><dt>connection</dt><dd class="mono">' + esc(addr) + ' · loopback · signed in with a session cookie this page cannot read</dd></dl>') +
        card('Keys', '<span class="mono">?</span> opens this anywhere', '<dl class="kv keys2">' + [['g then c r f h k a m b p s t e', 'Cockpit, Radio, Files, Changes, Checkpoints, Cache, Mail, Board, Replay, Sessions, Tools, Settings'], ['o c m b r s ,', 'the round-1 single keys for the same views'], ['ctrl+k · /', 'command palette · slash menu in the composer'], ['alt+t · alt+g', 'Cache · Cockpit (browsers reserve ctrl+t and ctrl+g)'], ['alt+1 … alt+9', 'the nth session tab']].concat(D.shortcuts.slice(0, 12)).map(([k, d]) => '<dt>' + esc(k) + '</dt><dd>' + esc(d) + '</dd>').join('') + '</dl>');
    };

  TK.settings.pages = P;
  /** the state of the page: the sub-page, the filters of Models, the permission tester, the Run settings stepper, the MCP lines */
  const newState = params => ({ page: (params && params.page) || ui.setPage || 'models', models: { q: '', tools: false, reasoning: false, favs: false, all: false, maxp: '', maxo: '', minc: '', role: 'manager', view: null, busy: false }, perm: { tool: 'Bash', arg: 'go test ./api/...', res: null, danger: null, busy: false, seq: 0 }, cfg: { q: '' }, run: { n: null, dirty: {} }, mcp: { out: {}, busy: {} }, eff: {}, cmdq: '' });
  TK.settings.newState = newState;

  function mount(sc, root, params, S0) {
    const ST = newState(params);
    root.innerHTML = '<div class="setv2"><nav class="setnav2" aria-label="Settings pages">' + ['session', 'shared', 'browser'].map(g => '<div class="sng"><span class="sngh" id="sg-' + g + '">' + GROUPS[g] + (g === 'session' ? ' <b class="sess"></b>' : '') + '</span><div role="group" aria-labelledby="sg-' + g + '">' + PAGES.filter(p => p[2] === g).map(p => '<button type="button" class="snb" data-page="' + p[0] + '"><b>' + p[1] + '</b><small>' + p[3] + '</small></button>').join('') + '</div></div>').join('') + '</nav><div class="setmain"><div class="setpage" tabindex="-1"></div></div></div>';
    const page = $('.setpage', root), nav = $('.setnav2', root);
    const toast = ui.toast;

    const budTick = S => {
      const c = calc.totals(S.wm), e = $('#budSpent', page); if (!e) return; e.textContent = fmtUsd(c.cost, 4); $('#budBar', page).style.width = (S.meta.budget ? Math.min(100, c.cost / S.meta.budget * 100) : 0) + '%'; $('#budPct', page).textContent = S.meta.budget ? Math.round(c.cost / S.meta.budget * 100) + '% used' : 'no limit'; $('#budSaved', page).textContent = fmtUsd(c.saved, 3);
      $('#budSavedLbl', page).textContent = c.savedPartial ? 'Saved at least' : 'Saved est. at list price'; const un = $('#budUnpriced', page), n = Number(c.unpriced) || 0; un.hidden = !n; un.textContent = n ? fmtN(n) + ' tokens at unknown prices are not counted in the cost.' : '';
      const h = S.wm.order.map(id => { const A = S.wm.ag[id], a = calc.agent(A); return '<tr><td class="bright" style="color:' + agColor(id) + '">' + esc(id) + '</td><td class="r num">' + fmtN(a.prompt) + '</td><td class="r num qfull ' + U.hitCls(a.pct) + '">' + (a.prompt ? a.pct + '%' : '–') + '</td><td class="r num">' + fmtN(a.out) + '</td><td class="r num">' + fmtUsd(a.cost, 4) + '</td></tr>'; }).join('') + '<tr class="tot"><td>all</td><td class="r num">' + fmtN(c.prompt) + '</td><td class="r num qfull">' + c.pct + '%</td><td class="r num">' + fmtN(c.out) + '</td><td class="r num">' + fmtUsd(c.cost, 4) + '</td></tr>';
      const tb = $('#budTbl tbody', page); if (tb._h !== h) { tb._h = h; tb.innerHTML = h; }
    };
    /* ---------------- paint ---------------- */
    const sess = $('.sess', root);
    /** with no session (the server hosts no tab) the pages of a session have nothing to show: Appearance does, the rest say what to do */
    function paintEmpty() {
      sess.textContent = ''; $$('.snb', nav).forEach(b => b.setAttribute('aria-current', String(b.dataset.page === ST.page)));
      const def = PAGES.find(p => p[0] === ST.page) || PAGES[0], h = '<header class="sethead"><h1>' + def[1] + '</h1><p>' + def[3] + '</p></header><div class="setbody2">' + (def[0] === 'look' ? P.look({ meta: {}, roster: [] }, ST) : '<p class="stubnote">These settings belong to a session, and none is open: start one with <b>+ New</b> (or ctrl+k).</p>') + '</div>';
      if (page._h !== h) { page._h = h; page.innerHTML = h; }
    }
    function paint(force) {
      const S = SL.sessions.active; if (!S || !S.m) { paintEmpty(); return; } sess.textContent = '· ' + S.name;
      $$('.snb', nav).forEach(b => b.setAttribute('aria-current', String(b.dataset.page === ST.page)));
      const def = PAGES.find(p => p[0] === ST.page) || PAGES[0], head = '<header class="sethead"><h1>' + def[1] + '</h1><p>' + def[3] + (def[2] === 'session' ? ' <b class="sesn">· ' + esc(S.name) + '</b>' : def[2] === 'shared' ? ' <span class="dim">· shared by every session</span>' : '') + '</p></header>';
      const active = document.activeElement, typing = active && page.contains(active) && /^(INPUT|TEXTAREA)$/.test(active.tagName) && active.type !== 'checkbox';
      const h = head + '<div class="setbody2">' + P[def[0]](S, ST) + '</div>';
      if (page._h !== h && !(typing && !force)) {
        const fid = active && page.contains(active) ? active.id || (active.dataset && (active.dataset.fav || active.dataset.use || active.dataset.seg + active.dataset.v || active.dataset.mcp + active.dataset.n)) : null, sel = active && page.contains(active) && active.selectionStart != null ? [active.selectionStart, active.selectionEnd] : null, top = page.scrollTop;
        page._h = h; page.innerHTML = h; page.scrollTop = top;
        if (fid) { const n = page.querySelector('#' + CSS.escape(fid)) || page.querySelector('[data-fav="' + CSS.escape(fid) + '"],[data-use="' + CSS.escape(fid) + '"]'); if (n) { n.focus({ preventScroll: true }); if (sel && n.setSelectionRange && n.type !== 'number' && n.type !== 'search') try { n.setSelectionRange(sel[0], sel[1]); } catch (e) { /* not a text field */ } } }
      }
      if (ST.page === 'budget') budTick(S);
    }
    const go = id => { ST.page = id; ui.setPage = id; ST.perm.res = null; if (SL.data) SL.data.forPage(id, { loud: true }); paint(true); page.scrollTop = 0; };

    /* ---------------- input ---------------- */
    /** the result of an action: a refusal before the request is said at once; otherwise the promise of the answer (the data layer says a failure itself) */
    const acted = (r, ok) => { if (r && r.ok === false) { toast(r.why || 'that cannot be done now', 'warm'); return Promise.resolve(null); } return r && r.done ? r.done.then(x => { if (x && x.ok && ok) ok(x.data); return x; }) : Promise.resolve(null); };
    sc.listen(nav, 'click', e => { const b = e.target.closest('[data-page]'); if (b) go(b.dataset.page); });
    sc.listen(nav, 'keydown', e => { if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return; const bs = $$('.snb', nav), i = bs.indexOf(document.activeElement); if (i < 0) return; e.preventDefault(); bs[(i + (e.key === 'ArrowDown' ? 1 : bs.length - 1)) % bs.length].focus(); });
    sc.listen(page, 'click', e => {
      const t = e.target, S = SL.sessions.active, A = SL.act;
      const sg = t.closest('[data-seg]'); if (sg) { const k = sg.dataset.seg, v = sg.dataset.v;
        if (k === 'effort') { acted(A.setEffort(v), d => { ST.eff[S.id] = d || {}; toast('effort: ' + v + (d && d.applied && d.applied !== v ? ' (this model: ' + d.applied + ')' : ''), 'ok'); paint(true); }); }
        else if (k === 'isolation') { ST.run.dirty[S.id] = true; acted(A.setIsolation(v)); } else if (k === 'hover') A.setHover(v); else if (k === 'motion') { A.setMotion(v); ui.applyMotion(); } else if (k === 'density') A.setDensity(v); else if (k === 'cache') A.setCache(v); else if (k === 'title') A.setTitleBadge(v); else if (k === 'navmode') { ui.nav.setWide(v === 'wide'); } else if (k === 'radiomode') { v === 'open' ? ui.rail.expand() : ui.rail.collapse(); } paint(true); return; }
      const fl = t.closest('[data-flt]'); if (fl) { const k = fl.dataset.flt; ST.models[k] = !ST.models[k]; if (k === 'all') loadModels({ all: ST.models.all, refresh: false }); paint(true); return; }
      const fv = t.closest('[data-fav]'); if (fv) { acted(A.favModel(fv.dataset.fav), d => { if (d && d.note) toast(String(d.note), 'warm'); }); paint(true); return; }
      const us = t.closest('[data-use]'); if (us) { useModel(us.dataset.use, ST.models.role, S); return; }
      const md = t.closest('[data-mode]'); if (md) { acted(A.setMode(md.dataset.mode), () => toast('mode: ' + md.dataset.mode)); return; }
      const dg = t.closest('[data-danger]'); if (dg) { ST.perm.danger = dg.dataset.danger; paint(true); const i = $('#cfIn', page); if (i) i.focus(); return; }
      const rm = t.closest('[data-rm]'); if (rm) { acted(A.removeRule(rm.dataset.rm), () => toast('rule removed: ' + rm.dataset.rm)); return; }
      const dofn = t.closest('[data-do]'); if (dofn) return doIt(dofn.dataset.do, S);
      const tr = t.closest('[data-trust]'); if (tr) { if (tr.dataset.on === '1') trustFlow(tr.dataset.trust); else acted(A.trustDir(tr.dataset.trust, false), () => toast('forgot ' + tr.dataset.trust, 'ok')); return; }
      const st = t.closest('[data-step]'); if (st) { const i = $('#rn', page); ST.run.n = clampWorkers((parseInt(i.value, 10) || 0) + +st.dataset.step, maxWorkers()); i.value = ST.run.n; $('#rnt', page).textContent = teamWord(ST.run.n); return; }
      const mc = t.closest('[data-mcp]'); if (mc) return mcpDo(mc.dataset.mcp, mc.dataset.n);
      const cmd = t.closest('[data-cmd]'); if (cmd) { ui.setDraft('/' + cmd.dataset.cmd + ' '); toast('inserted in the Radio composer'); return; }
      const pcm = t.closest('[data-pcmd]'); if (pcm) { ui.setDraft('/' + pcm.dataset.pcmd + ' '); toast('inserted in the Radio composer'); return; }
      const pv = t.closest('[data-prov]'); if (pv) { const p = arr(G.providers).find(x => x.id === pv.dataset.prov); if (!p) return; if (keyInfo(p).btn === 'in') signIn(p); else signOut(p); return; }
      const cl = t.closest('[data-cli]'); if (cl) { if (cl.hasAttribute('data-cli-models')) { const c = modelsCli(ST.models); ui.runCli(['models'], c.flags, false, c.pos); } else ui.runCli(cl.dataset.cli.split(' ')); return; }
    });
    sc.listen(page, 'change', e => {
      const t = e.target, S = SL.sessions.active, A = SL.act;
      if (t.dataset.sel) { ST.models[t.dataset.sel] = t.value; paint(true); } else if (t.dataset.role) useModel(t.value, t.dataset.role, S, true);
      else if (t.dataset.flag) { ST.run.dirty[S.id] = true; acted(A.setFlag(t.dataset.flag, t.checked)); } else if (t.id === 'tryTool') ST.perm.tool = t.value;
    });
    sc.listen(page, 'input', e => {
      const t = e.target;
      if (t.id === 'mq') { ST.models.q = t.value; paintKeep(t); } else if (t.id === 'cq') { ST.cfg.q = t.value; paintKeep(t); } else if (t.id === 'cfIn') { $('#cfGo', page).disabled = t.value.trim() !== ST.perm.danger; } else if (t.id === 'tryArg') ST.perm.arg = t.value; else if (t.id === 'rn') { ST.run.n = clampWorkers(t.value, maxWorkers()); $('#rnt', page).textContent = teamWord(ST.run.n); }
    });
    function paintKeep(inp) { const pos = inp.selectionStart, id = inp.id; paint(true); const n = $('#' + id, page); if (n) { n.focus(); try { n.setSelectionRange(pos, pos); } catch (e) { /* no caret in this type */ } } }
    sc.listen(page, 'keydown', e => { const t = e.target; SL.time.noteKey(); if (e.key === 'Enter') { if (t.id === 'bIn') { e.preventDefault(); doIt('budget', SL.sessions.active); } else if (t.id === 'ruleIn') { e.preventDefault(); doIt('rule-add', SL.sessions.active); } else if (t.id === 'tryArg') { e.preventDefault(); doIt('try', SL.sessions.active); } else if (t.id === 'rv') { e.preventDefault(); doIt('verify', SL.sessions.active); } else if (t.id === 'cfIn' && !$('#cfGo', page).disabled) { e.preventDefault(); $('#cfGo', page).click(); } } });
    sc.listen(page, 'click', e => { if (e.target.id === 'cfGo') { const m = ST.perm.danger; ST.perm.danger = null; acted(SL.act.setMode(m, { confirm: m }), () => toast('mode: ' + m + ' (shift+tab will not leave it unasked)', 'err')); paint(true); } });

    /** choose a model for the manager or a role; what the server did (a team that restarts, or a switch in place) is what the toast says */
    function useModel(ref, role, S, fromSelect) {
      const A = SL.act, r = role === 'manager' ? A.setModel(ref) : A.setRoleModel(role, ref), team = S.roster.length > 1;
      acted(r, d => {
        const restarted = d ? !!d.restarted : team;
        toast(role === 'manager' ? (restarted ? 'a team starts again on ' + ref : 'model: ' + ref) : role + ' runs on ' + ref + (restarted ? ' (its workers restart)' : ''), 'ok');
      }); if (!fromSelect) paint(true);
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
      else if (k === 'mgr-refused') ui.openDrawer('mgr');
      else if (k === 'verify') { const v = $('#rv', page).value.trim(); ST.run.dirty[S.id] = true; acted(A.setVerify(v), () => toast('verify: ' + (v || '(none)'), 'ok')); paint(true); }
      else if (k === 'apply') { const nn = ST.run.n == null ? S.meta.swarm : ST.run.n; if (nn === S.meta.swarm && !ST.run.dirty[S.id]) { toast('already ' + teamWord(nn), 'warm'); return; } ui.confirm({ title: 'Start the team again', text: 'Restart <b>' + esc(S.name) + '</b> as ' + (nn ? 'a manager and <b>' + nn + '</b> worker' + (nn === 1 ? '' : 's') + (nn > 8 ? ' (' + (nn - 8) + ' share legs)' : '') : 'a single agent') + '? The current run is closed (its checkpoints stay) and the conversation carries over.', ok: 'Restart', danger: true, run: () => { const r = A.restartTeam({ swarm: nn, force: true }); acted(r, () => { ST.run.n = null; delete ST.run.dirty[S.id]; toast('the team starts again: ' + teamWord(nn), 'ok'); }); } }); }
      else if (k === 'copycli') U.copy($('.pre.cli', page).textContent).then(ok => toast(ok ? 'command copied' : 'copy is not available here', ok ? 'ok' : 'warm'));
      else if (k === 'railreset') { ui.rail.setWidth(344); }
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
    /** Sign in: the page shows the command for the terminal and, when it was run, asks the server again which providers are connected */
    function signIn(p) {
      ui.modal({ title: 'Sign in to ' + p.name, kicker: 'terminal flow', desc: 'the browser never sees an API key', color: 'var(--ok)', body: '<p class="stubnote">Run this in a terminal; it asks for the key (or opens the ChatGPT sign-in) and stores it in <span class="mono">~/.sleipnir/auth.json</span> with mode 0600. This page then shows the provider as connected.</p><pre class="pre cli">sleipnir login ' + esc(p.id) + '</pre><div class="row2"><button class="btn" type="button" data-copy>Copy the command</button><button class="btn pri" type="button" data-done>I ran it: check again</button><button class="btn" type="button" data-close>Cancel</button></div>',
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
    sc.on('mcp-changed', () => paint(false)); sc.on('trust-changed', () => paint(false)); sc.on('models-changed', () => paint(false));
    if (SL.data) SL.data.forPage(ST.page, { loud: true });
    paint(true);
  }
  SL.views.register({ name: 'settings', title: 'Settings', mount });
})(SL);
