/* 83b-ui-dash.js: every page of the web UI as a dashboard of cards (SL.dash, the catalogue and the layout; ui.dash, the grid that shows
 * a page; ui.chrome, the header widgets and the Radio rail's parts).
 *
 * A page (a view, a tab of the Workspace or a page of Settings) is a grid of COLS columns and rows of one unit each (ROWS_FIT units fill
 * the height of the view, never less than ROW_MIN pixels each). A card is {id, x, y, w, h, hidden?, opts?} in those units. Every panel of
 * every page is a card of the catalogue (PAGES, BY_ID) with a stable id; a card also has a home page, whose view draws it. The default
 * layout of a page is the arrangement its panels had before they were cards. A card of one page can be added to any other page: the
 * card then mounts its home view in the card's own scope, drawing only that card (`params.__only`), so a card renders apart from its
 * home page. The views of the rail are cards too (`v:<view>`): the whole view, drawn by its own code inside the card (`params.__flat`).
 *
 * Locked (the default), the cards are the page. Unlocked (the page's Customise button), a card moves when it is dragged, resizes from its
 * right edge, bottom edge and corner, and has a menu (⋯): Hide, Reset size, and options where they help (the density of a list). A hidden
 * card keeps its place and waits in the Hidden tray of the toolbar, which restores it; `+ Add card` lists the page's hidden cards, the
 * cards of every other page and the views. With the keyboard, a focused card moves with the arrows, resizes with shift and the arrows and
 * hides with Delete. A card that lands on others pushes them down; nothing else moves, except under a card whose height follows its
 * content (most cards outside the Cockpit), which close up when it gets shorter. On a narrow screen (900 px or less) the cards stack in
 * one column in their order, and customising hides, restores and reorders them (up and down) only.
 *
 * The layout of every page is one document ({version, pages: {page: [cards]}}). It is kept in this browser (localStorage, KEY) and on
 * the server (GET/PUT/DELETE /api/layout, in ~/.sleipnir/web-layout.json), so it follows the person across browsers: the page reads the
 * server's copy at boot, saves every change in the browser at once and on the server shortly after. The Cockpit's own layout of the
 * first version (OLD_KEY) is carried over, its removed cards becoming hidden ones. The layout functions are pure: they take a layout and
 * return a new one, and the tests call them without a page. */
(function (SL) {
  'use strict';
  const U = SL.u, { esc } = U, ui = SL.ui = SL.ui || {};
  const COLS = 12, ROWS_FIT = 18, ROW_MIN = 30, MAX_H = 48, FIT_MAX = 160, VERSION = 2, KEY = 'sleipnir.web.layout', OLD_KEY = 'sleipnir.web.cockpit', OLD_VERSION = 1;

  /* ------------------------------------------------------------------------------------------------------------ the catalogue */
  /** The pages: key -> {key, title, group, view, params, wrap, cards: [ids], layout: default [{id, x, y, w, h}]}. */
  const PAGES = {};
  /** The cards: id -> {id, page (its home), kind (part | panel | view | chrome), title, desc, w, h, minW, minH, fit, list, view?}. A fit
   *  card's height follows its content until the person sizes it; a list card has a density option. */
  const BY_ID = {};
  /** Declare a page and its cards: spec rows are [id, title, desc, x, y, w, h, minW, minH, flags] (flags: 'fill' a fixed box, 'list' a
   *  list with a density option); a row that is a string ([id, x, y, w, h]) reuses a card another page declared. */
  function page(key, o, rows) {
    const P = PAGES[key] = { key, title: o.title, group: o.group || o.title, view: o.view, params: o.params || {}, wrap: o.wrap || '', cards: [], layout: [] };
    rows.forEach(r => {
      if (typeof r[0] === 'string' && r.length === 5) { const [id, x, y, w, h] = r; P.cards.push(id); P.layout.push({ id, x, y, w, h }); return; }
      const [id, title, desc, x, y, w, h, minW, minH, flags] = r, f = flags || '';
      BY_ID[id] = { id, page: key, kind: o.kind || 'panel', title, desc, w, h, minW: minW || 2, minH: minH || 2, fit: o.kind !== 'part' && o.kind !== 'chrome' && !/fill/.test(f), list: /list/.test(f) };
      P.cards.push(id); P.layout.push({ id, x, y, w, h });
    });
    return P;
  }
  /* The Cockpit: the horse and the stalls on top, the gantt and the board, then the queue, mail and the governor in a short band. */
  page('cockpit', { title: 'Cockpit', view: 'cockpit', kind: 'part' }, [
    ['hero', 'The horse', 'the team as a horse: each of the eight legs is one role of the team', 0, 0, 4, 8, 2, 4],
    ['team', 'Manager and stalls', 'the manager\'s card and one stall per worker; a free slot names its role', 4, 0, 8, 8, 3, 4],
    ['gantt', 'Timeline', 'what each agent did in the last 60 seconds, with the replay controls', 0, 8, 6, 7, 3, 4],
    ['tasks', 'Task board', 'the tasks by status: todo, running, verify, merged', 6, 8, 6, 7, 3, 4, 'list'],
    ['queue', 'Merge queue', 'what is being verified and merged, conflicts and bounces', 0, 15, 4, 3, 2, 2],
    ['mail', 'Mail, latest', 'the last 30 mails between agents', 4, 15, 5, 3, 2, 2, 'list'],
    ['gov', 'Rate limits', 'requests per minute, refused requests (429) and retries', 9, 15, 3, 3, 2, 2],
  ]);
  page('cache', { title: 'Cache', view: 'cache', wrap: 'cache' }, [
    ['cache.head', 'The latest prompt, layer by layer', 'the prompt of the agent answered last, by layer, and what was read from the cache', 0, 0, 12, 5, 4, 3],
    ['cache.hit', 'How much of each request came from the cache', 'the share of each request read from the cache, with breaks and compactions', 0, 5, 12, 6, 4, 3],
    ['cache.fold', 'Folding the conversation', 'the latest summary of earlier work: when it happened and what it cost', 0, 11, 6, 6, 3, 3],
    ['cache.anom', 'Unexpected cache misses', 'requests that read less from the cache than expected, and why', 6, 11, 6, 6, 3, 3, 'list'],
    ['cache.table', 'Every agent\'s cache', 'read, uncached and written tokens of every agent, and the savings', 0, 17, 12, 7, 4, 3, 'list'],
    ['cache.sum', 'The whole session', 'the session\'s hit ratio, savings, spend, compactions and anomalies', 0, 24, 12, 2, 3, 2],
  ]);
  page('mail', { title: 'Mail', view: 'mail', wrap: 'mailv' }, [
    ['mail.flow', 'Who writes to whom', 'an arc per mail between the agents of the team', 0, 0, 12, 6, 4, 3, 'fill'],
    ['mail.list', 'Envelopes', 'every mail, newest first', 0, 6, 5, 12, 3, 3, 'fill list'],
    ['mail.msg', 'Message', 'the selected mail, its thread and its delivery', 5, 6, 7, 12, 3, 3, 'fill'],
  ]);
  page('board', { title: 'Board', view: 'board', wrap: 'boardv' }, [
    ['board.tasks', 'Task board', 'the tasks in their columns: todo, running, verify, merged', 0, 0, 12, 10, 4, 4, 'fill list'],
    ['board.deps', 'Dependencies', 'which task waits for which, up to the goal', 0, 10, 12, 8, 4, 4, 'fill'],
  ]);
  page('replay', { title: 'Replay', view: 'replay', wrap: 'replayv' }, [
    ['replay.tape', 'The recorded session', 'the tape of every agent, with play, seek and speed', 0, 0, 12, 7, 4, 3],
    ['replay.frame', 'Frame', 'what the cockpit shows at the playhead', 0, 7, 4, 10, 2, 3, 'fill'],
    ['replay.events', 'Notable events', 'mail, breaks, merges and questions; a click seeks', 4, 7, 4, 10, 2, 3, 'fill list'],
    ['replay.keys', 'Keys', 'the keys of a replay', 8, 7, 4, 10, 2, 3, 'fill'],
  ]);
  page('sessions', { title: 'Sessions', view: 'sessions', wrap: 'sessv' }, [
    ['sessions.live', 'Live sessions', 'the sessions this server runs, with their cost and state', 0, 0, 8, 6, 3, 3, 'list'],
    ['sessions.recorded', 'Recorded', 'the recorded sessions on disk: resume, replay, watch, delete', 0, 6, 8, 9, 3, 3, 'list'],
    ['sessions.prune', 'Prune', 'delete old recorded sessions, with a preview', 8, 0, 4, 9, 3, 3],
    ['sessions.inbox', 'Cross-session inbox', 'the questions waiting in any session', 8, 9, 4, 4, 3, 2, 'list'],
  ]);
  page('tools', { title: 'Tools', view: 'tools', wrap: 'toolsv' }, [
    ['tools.find', 'Find a command', 'filter the commands and open the runner', 0, 0, 12, 2, 3, 2],
    ['tools.run', 'Run work', 'start a session, run a goal without a terminal, or put one on a schedule', 0, 2, 12, 5, 3, 2],
    ['tools.observe', 'Sessions', 'find, replay, watch and clean up recorded sessions', 0, 7, 12, 5, 3, 2],
    ['tools.evaluate', 'Models & providers', 'keys, the model catalogue, and what an endpoint really does', 0, 12, 12, 5, 3, 2],
    ['tools.setup', 'Skills, plugins & maintenance', 'plugins, tool servers, trust, the configuration and updates', 0, 17, 12, 6, 3, 2],
    ['tools.other', 'Data', 'the RL environment, and any command of no group', 0, 23, 12, 4, 3, 2],
  ]);
  page('runner', { title: 'Run a command', view: 'runner', wrap: 'runv' }, [
    ['runner.list', 'Commands', 'every sleipnir command, filtered', 0, 0, 3, 18, 2, 4, 'fill list'],
    ['runner.form', 'Run', 'the form of the chosen command, from its real flags, and its command line', 3, 0, 5, 18, 3, 4, 'fill'],
    ['runner.out', 'Output', 'the output of the run, its result and the recent runs', 8, 0, 4, 18, 3, 4, 'fill'],
  ]);
  page('doctor', { title: 'Doctor', view: 'doctor', wrap: 'docv' }, [
    ['doctor.form', 'Probe an endpoint', 'choose an endpoint and run the probe', 0, 0, 4, 18, 3, 4, 'fill'],
    ['doctor.out', 'Probe', 'each step of the probe and the verdict', 4, 0, 8, 18, 3, 4, 'fill'],
  ]);
  page('schedule', { title: 'Schedule', view: 'schedule', wrap: 'schv' }, [
    ['schedule.daemon', 'Daemon', 'whether the daemon that starts the jobs runs', 0, 0, 12, 2, 3, 2],
    ['schedule.jobs', 'Jobs', 'the cron jobs of this machine', 0, 2, 12, 5, 3, 2, 'list'],
    ['schedule.add', 'Add a job', 'a cron line, a goal, a folder, a mode, a model and a budget', 0, 7, 5, 11, 3, 4],
    ['schedule.log', 'Log', 'the log of a job, or of a run started now', 5, 7, 7, 11, 3, 4, 'fill'],
  ]);
  /* the Workspace: one set of panels, a layout for each of its four tabs */
  page('files', { title: 'Files', group: 'Workspace', view: 'workspace', params: { tab: 'files' }, wrap: 'wsv' }, [
    ['ws.bar', 'Workspace controls', 'the tabs, the time-travel scrubber and the summary', 0, 0, 12, 2, 4, 2],
    ['ws.left', 'Files, changes and checkpoints', 'the tree, the changes, the checkpoints or the worktrees', 0, 2, 4, 14, 2, 4, 'fill list'],
    ['ws.main', 'The selected file', 'the diff or the whole file, with the per-line gutter', 4, 2, 8, 14, 3, 4, 'fill'],
    ['ws.strip', 'Verify and merge', 'what is verified and merged, and applying the verified work', 0, 16, 12, 2, 4, 2],
  ]);
  ['changes', 'checkpoints', 'merge'].forEach(t => page(t, { title: t[0].toUpperCase() + t.slice(1), group: 'Workspace', view: 'workspace', params: { tab: t }, wrap: 'wsv' },
    PAGES.files.layout.map(c => [c.id, c.x, c.y, c.w, c.h])));
  /* Settings: a page of cards for each page of Settings (98-ui-settings.js draws them). A card keeps the id it had when its page was a
   * page of its own (`roles.roles` is settings.roles.roles, now on Models & roles); MOVED says where such a page went. */
  const SET = [
    ['models', 'Models & roles', [['catalogue', 'The model catalogue', 'filter, star, and choose a model for the manager or a role', 14, 'list'], ['roles.roles', 'Which model each role runs on', 'the model of each role, waiting to be applied', 8, 'list'], ['roles.effort', 'Reasoning effort', 'how hard the models think', 4]]],
    ['run', 'Team & run', [['team', 'The team', 'workers and starting the team again', 5], ['how', 'How the workers run', 'isolation, the check command and the team\'s helpers', 7], ['cli', 'The same command line', 'this session as a command line', 3]]],
    ['budget', 'Budget', [['budget', 'This session\'s budget', 'what was spent and the limit', 6], ['cost', 'Cost by agent', 'tokens and cost of every agent', 6, 'list']]],
    ['permissions', 'Permissions & trust', [['mode', 'Mode', 'the permission mode', 5], ['danger', 'Dangerous modes', 'bypass and yolo: they ask nothing', 4], ['manager', 'The manager', 'what the manager may write', 3], ['rules', 'Rules in force', 'the allow, ask and deny rules and their origin', 7, 'list'], ['try', 'Would it ask?', 'test a tool call against the rules', 4], ['trust.project', 'This project\'s own files', 'whether the harness reads this project\'s instructions, skills and hooks', 6], ['trust.dirs', 'Every directory you decided about', 'the trust ledger', 5, 'list']]],
    ['providers', 'Connections', [['providers', 'Providers', 'where each model is reached, and whether a key is there', 6, 'list'], ['mcp.servers', 'Tool servers (MCP)', 'state, tools and approval of each server', 8, 'list']]],
    ['skills', 'Skills & extensions', [['skills', 'Skills the model can load', 'the skills and where each came from', 6, 'list'], ['commands', 'Your commands', 'custom slash commands', 4, 'list'], ['hooks', 'Hooks', 'what runs around the tools', 4, 'list']]],
    ['config', 'Configuration', [['layers', 'Layers, lowest to highest', 'the configuration files, in order', 5], ['warnings', 'Warnings', 'what sleipnir config warns about', 3, 'list'], ['values', 'Effective values and where each came from', 'every key with the layer that set it', 10, 'list']]],
    ['look', 'Appearance', [['motion', 'Motion and display', 'motion, density, tab title and notifications', 7], ['layout', 'Layout', 'the rails, and the layout of the cards', 7], ['tour', 'Cards and tour', 'customise every page, export and import the layout, the guided tour', 6], ['security', 'Security semantics', 'how questions and approvals are kept safe', 5], ['keys', 'Keys', 'the keys of the page', 6, 'list']]],
  ];
  SET.forEach(([p, title, cards]) => { let y = 0; page('settings.' + p, { title, group: 'Settings', view: 'settings', params: { page: p }, wrap: 'setbody2' }, cards.map(([id, t, d, h, f]) => { const r = ['settings.' + (id.includes('.') ? id : p + '.' + id), t, d, 0, y, 12, h, 4, 2, f]; y += h; return r; })); });
  /** Pages of an earlier version merged into another page: old key -> the page that has its cards now. */
  const MOVED = { 'settings.roles': 'settings.models', 'settings.trust': 'settings.permissions', 'settings.mcp': 'settings.providers' };
  /* Plugins: the profile and the filter, then one card for each category of plugins (98c-ui-plugins.js draws them) */
  page('plugins', { title: 'Plugins', view: 'plugins', wrap: 'plugv' }, [
    ['plugins.profile', 'Profile and filter', 'the profile, where a change is saved, and the filter of the list', 0, 0, 12, 3, 4, 2],
    ['plugins.tools', 'Tools', 'what the agents can call', 0, 3, 12, 8, 4, 2, 'list'],
    ['plugins.extensions', 'Extensions', 'skills, commands, agents, hooks and instructions read from files', 0, 11, 12, 6, 4, 2, 'list'],
    ['plugins.team', 'Team', 'how a team of agents works together', 0, 17, 12, 6, 4, 2, 'list'],
    ['plugins.interface', 'Interface', 'the views of this page', 0, 23, 12, 6, 4, 2, 'list'],
    ['plugins.external', 'External', 'plugins installed from outside Sleipnir', 0, 29, 12, 3, 4, 2, 'list'],
    ['plugins.core', 'Core', 'always on: Sleipnir needs them to run', 0, 32, 12, 4, 4, 2, 'list'],
  ]);
  /* the header and status bar, and the Radio rail: parts that stay in place and can be hidden (and, in the rail, collapsed) */
  page('hud', { title: 'Header', view: null, kind: 'chrome' }, [
    ['hud.goal', 'Goal', 'the goal, its state and the elapsed time', 0, 0, 1, 1, 1, 1],
    ['hud.budget', 'Budget', 'what was spent and the budget', 0, 0, 1, 1, 1, 1],
    ['hud.hit', 'Cache reuse', 'the share of the prompt read from the cache (shown with full cache detail)', 0, 0, 1, 1, 1, 1],
    ['hud.warm', 'Cache stays warm', 'how long the shared instructions stay in the cache (shown with full cache detail)', 0, 0, 1, 1, 1, 1],
    ['hud.team', 'Team', 'the team and how many are working', 0, 0, 1, 1, 1, 1],
    ['hud.status', 'Status line', 'the status line at the foot of the page', 0, 0, 1, 1, 1, 1],
    ['hud.model', 'Model', 'the model at the foot of the page', 0, 0, 1, 1, 1, 1],
  ]);
  page('rail', { title: 'Radio rail', view: null, kind: 'chrome' }, [
    ['rail.talk', 'Manager', 'the conversation with the manager and its plan', 0, 0, 1, 1, 1, 1],
    ['rail.feed', 'Team activity', 'what the workers do, read-only', 0, 0, 1, 1, 1, 1],
    ['rail.composer', 'Composer', 'where you write to the manager', 0, 0, 1, 1, 1, 1],
  ]);
  /** Where the chrome parts are on the page (ui.chrome): a selector for each. */
  const CHROME_EL = { 'hud.goal': '#goalBtn', 'hud.budget': '#hud .hm.budget', 'hud.hit': '#ringHit', 'hud.warm': '#ringWarm', 'hud.team': '#teamBox', 'hud.status': '#statusLine', 'hud.model': '#footModel',
    'rail.talk': '#rail .talkwrap', 'rail.feed': '#feedWrap', 'rail.composer': '#composer' };
  /** The views of the rail a card can hold whole: [view, title, description]. The Workspace card has its four tabs, as the view has. */
  const VIEWS = [
    ['workspace', 'Workspace', 'files, changes, checkpoints and merge, with the diff'],
    ['cache', 'Cache', 'the prompt by layer, the requests, cache breaks'],
    ['mail', 'Mail', 'every mail between agents, with its text'],
    ['board', 'Board', 'the task board with its dependencies'],
    ['replay', 'Replay', 'the session as a recording: scrub and speed'],
    ['sessions', 'Sessions', 'live and recorded sessions'],
    ['tools', 'Tools', 'every sleipnir command, with a form'],
    ['doctor', 'Doctor', 'probe an endpoint'],
    ['schedule', 'Schedule', 'cron jobs and the daemon'],
    ['plugins', 'Plugins', 'turn parts of Sleipnir on and off'],
    ['settings', 'Settings', 'models, roles, budget, permissions and the rest'],
  ];
  VIEWS.forEach(([v, t, d]) => { BY_ID['v:' + v] = { id: 'v:' + v, page: null, kind: 'view', view: v, title: t, desc: d, minW: 3, minH: 4, w: 6, h: 9, fit: false, list: false }; });
  /** The cards a Cockpit could hold in the first version: its panels (kind part) and the views of the rail (kind view). */
  const CATALOG = PAGES.cockpit.cards.map(id => BY_ID[id]).concat(VIEWS.map(([v]) => BY_ID['v:' + v]));
  /** The Cockpit's default layout. */
  const DEFAULT = PAGES.cockpit.layout;

  /* ------------------------------------------------------------------------------------------------------------ the layout functions */
  const int = (v, d) => (typeof v === 'number' && isFinite(v) ? Math.round(v) : d);
  /** A plain copy of a card: {id, x, y, w, h}, with hidden only when it is hidden and opts only when it has some. */
  const copy1 = c => { const o = { id: c.id, x: c.x, y: c.y, w: c.w, h: c.h }; if (c.hidden) o.hidden = true; if (c.opts && Object.keys(c.opts).length) o.opts = Object.assign({}, c.opts); return o; };
  const copy = L => L.map(copy1);
  /** The cards that show. */
  const visible = L => L.filter(c => !c.hidden);
  /** The tallest a card may be: a card whose height follows its content may grow further than one sized by hand. */
  const maxH = d => (d && d.fit ? FIT_MAX : MAX_H);
  /** A card kept inside the grid and inside its own limits; its hidden flag and its options (strings, numbers, booleans) are kept. */
  function clampCard(c) {
    const d = BY_ID[c.id]; const w = Math.max(d.minW, Math.min(COLS, int(c.w, d.w))), h = Math.max(d.minH, Math.min(maxH(d), int(c.h, d.h)));
    const o = { id: c.id, w, h, x: Math.max(0, Math.min(COLS - w, int(c.x, 0))), y: Math.max(0, Math.min(1000 - h, int(c.y, 0))) };
    if (c.hidden === true) o.hidden = true;
    if (c.opts && typeof c.opts === 'object') { const op = {}; Object.keys(c.opts).slice(0, 16).forEach(k => { const v = c.opts[k]; if (/^[A-Za-z0-9][A-Za-z0-9._:-]{0,95}$/.test(k) && (typeof v === 'boolean' || (typeof v === 'number' && isFinite(v)) || (typeof v === 'string' && v.length <= 200))) op[k] = v; }); if (Object.keys(op).length) o.opts = op; }
    return { id: o.id, x: o.x, y: o.y, w: o.w, h: o.h, ...(o.hidden ? { hidden: true } : {}), ...(o.opts ? { opts: o.opts } : {}) };
  }
  const overlaps = (a, b) => a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h;
  /** The layout with no two showing cards on the same cell: the card `fixed` keeps its place, every other card that lands on a placed one
   *  goes down below it (in the order of their rows, then columns), and nothing moves up. Hidden cards keep their place and block nothing. */
  function resolve(L, fixed) {
    const placed = [], out = copy(L), vis = out.filter(c => !c.hidden), first = vis.find(c => c.id === fixed);
    if (first) placed.push(first);
    vis.filter(c => c !== first).sort((a, b) => a.y - b.y || a.x - b.x).forEach(c => {
      for (let hit = placed.find(p => overlaps(p, c)); hit; hit = placed.find(p => overlaps(p, c))) c.y = hit.y + hit.h;
      placed.push(c);
    });
    return out;
  }
  /** A list of cards read from storage: unknown and repeated cards are left out, sizes and places are clamped, overlaps resolved. */
  function cleanList(list) {
    const seen = new Set(), cards = [];
    (Array.isArray(list) ? list : []).forEach(c => { if (c && typeof c === 'object' && typeof c.id === 'string' && BY_ID[c.id] && BY_ID[c.id].kind !== 'chrome' && !seen.has(c.id)) { seen.add(c.id); cards.push(clampCard(c)); } });
    return resolve(cards);
  }
  /** The layout of the first version (the Cockpit only: {v: 1, cards}), or null when raw is not one. */
  function normalize(raw) {
    if (!raw || typeof raw !== 'object' || raw.v !== OLD_VERSION || !Array.isArray(raw.cards)) return null;
    return cleanList(raw.cards.filter(c => !(c && BY_ID[c.id] && BY_ID[c.id].kind === 'chrome')));
  }
  /** A page's layout from what is stored for it (a list, or nothing): the stored cards, then any card of the page the list does not name
   *  (a card new in this version of the page) where it fits; nothing stored gives the default. */
  function normalizeList(list, key) {
    const P = PAGES[key]; if (!Array.isArray(list)) return defaults(key);
    let L = cleanList(list); if (!P) return L;
    P.cards.forEach(id => { if (!L.some(c => c.id === id)) L = add(L, id); });
    return L;
  }
  /** The default layout of a page (a copy). */
  const defaults = key => (PAGES[key] ? copy(PAGES[key].layout) : []);
  /** The layout with card id moved to (x, y). */
  function move(L, id, x, y) { const c = L.find(k => k.id === id); if (!c) return copy(L); return resolve(L.map(k => (k === c ? clampCard(Object.assign({}, c, { x, y })) : k)), id); }
  /** The layout with card id resized to w by h, from its top-left corner. */
  function resize(L, id, w, h) { const c = L.find(k => k.id === id); if (!c) return copy(L); return resolve(L.map(k => (k === c ? clampCard(Object.assign({}, c, { w, h })) : k)), id); }
  /** The first place, row by row, where a card of w by h touches no showing card. */
  function firstFit(L, w, h) {
    const vis = visible(L); for (let y = 0; ; y++) for (let x = 0; x + w <= COLS; x++) { const c = { x, y, w, h }; if (!vis.some(k => overlaps(k, c))) return { x, y }; }
  }
  /** The layout with card id showing: a hidden card comes back at its own place when that is free, else where it first fits; a card not
   *  on the page is added at its own size where it first fits (unchanged when it shows already or is unknown). */
  function add(L, id) {
    const d = BY_ID[id]; if (!d || d.kind === 'chrome') return copy(L); const cur = L.find(k => k.id === id);
    if (cur && !cur.hidden) return copy(L);
    if (cur) { const back = Object.assign({}, cur); delete back.hidden; const others = visible(L); const p = others.some(k => overlaps(k, back)) ? firstFit(L, back.w, back.h) : back; return L.map(k => (k === cur ? clampCard(Object.assign(back, { x: p.x, y: p.y })) : copy1(k))); }
    const w = Math.min(COLS, d.w), p = firstFit(L, w, d.h); return copy(L).concat([clampCard({ id, x: p.x, y: p.y, w, h: d.h })]);
  }
  /** The layout without card id (a card of another page leaves the page). */
  const remove = (L, id) => copy(L.filter(k => k.id !== id));
  /** The layout with card id hidden: it keeps its place, for when it comes back. */
  const hide = (L, id) => L.map(k => (k.id === id ? Object.assign(copy1(k), { hidden: true }) : copy1(k)));
  /** The layout with card id at its own default size again (a card whose height follows its content follows it again). */
  function resetSize(L, id) {
    const d = BY_ID[id], c = L.find(k => k.id === id); if (!d || !c) return copy(L); const P = PAGES[d.page], dl = P && P.layout.find(k => k.id === id);
    const next = L.map(k => { if (k !== c) return k; const o = copy1(k); if (o.opts) { delete o.opts.size; if (!Object.keys(o.opts).length) delete o.opts; } return o; });
    return resize(next, id, dl ? dl.w : d.w, dl ? dl.h : d.h);
  }
  /** The layout with option k of card id set to v (v null removes it). */
  const setOpt = (L, id, k, v) => L.map(c => { if (c.id !== id) return copy1(c); const o = copy1(c), op = Object.assign({}, o.opts || {}); if (v == null) delete op[k]; else op[k] = v; if (Object.keys(op).length) o.opts = op; else delete o.opts; return o; });
  /** The order a single column shows the cards in: by row, then column. */
  const order = L => visible(L).slice().sort((a, b) => a.y - b.y || a.x - b.x);
  /** The layout with card id one place earlier (d = -1) or later (d = 1) in the order of a single column: it trades places with its
   *  neighbour, and the one of the two that goes up keeps its new place (the other is pushed under it when they are of different sizes). */
  function swap(L, id, d) {
    const o = order(L), i = o.findIndex(c => c.id === id), j = i + d; if (i < 0 || j < 0 || j >= o.length) return copy(L);
    const a = o[i], b = o[j], up = d > 0 ? b : a;
    return resolve(L.map(k => (k.id === a.id ? clampCard(Object.assign({}, k, { x: b.x, y: b.y })) : k.id === b.id ? clampCard(Object.assign({}, k, { x: a.x, y: a.y })) : copy1(k))), up.id);
  }
  /** The layout with card id h rows tall because its content is (the person did not size it): taller pushes what is under it down,
   *  shorter lets what was under it come up as far as it can, by the same amount. */
  function fitHeight(L, id, h) {
    const c = L.find(k => k.id === id); if (!c || c.hidden) return copy(L); const d = BY_ID[id]; h = Math.max(d.minH, Math.min(maxH(d), Math.round(h)));
    if (h === c.h) return copy(L);
    if (h > c.h) return resolve(L.map(k => (k === c ? Object.assign(copy1(k), { h }) : k)), id);
    const bottom = c.y + c.h, dy = c.h - h, out = L.map(k => (k === c ? Object.assign(copy1(k), { h }) : copy1(k)));
    out.filter(k => !k.hidden && k.id !== id && k.y >= bottom).sort((a, b) => a.y - b.y || a.x - b.x).forEach(k => {
      const others = out.filter(o => !o.hidden && o !== k);
      for (let up = dy; up > 0; up--) { const t = Object.assign({}, k, { y: k.y - up }); if (!others.some(o => overlaps(o, t))) { k.y = t.y; break; } }
    });
    return out;
  }
  /** The number of rows the showing cards take. */
  const rows = L => visible(L).reduce((m, c) => Math.max(m, c.y + c.h), 0);
  /** What a key does to a focused card when the page is unlocked: the arrows move it one cell, shift and the arrows resize it by one cell;
   *  in a single column (stack) only up and down, which reorder. Returns the new layout, or null for a key that does nothing. */
  function keyOp(L, id, key, shift, stack) {
    const c = L.find(k => k.id === id), d = { ArrowLeft: [-1, 0], ArrowRight: [1, 0], ArrowUp: [0, -1], ArrowDown: [0, 1] }[key]; if (!c || !d) return null;
    if (stack) return shift || !d[1] ? null : swap(L, id, d[1]);
    return shift ? resize(L, id, c.w + d[0], c.h + d[1]) : move(L, id, c.x + d[0], c.y + d[1]);
  }
  /** Whether two layouts put the same cards in the same places, hidden or not, with the same options (the order of the list does not count). */
  const same = (A, B) => A.length === B.length && A.every(a => { const b = B.find(k => k.id === a.id); return b && b.x === a.x && b.y === a.y && b.w === a.w && b.h === a.h && !!b.hidden === !!a.hidden && JSON.stringify(b.opts || {}) === JSON.stringify(a.opts || {}); });
  /** The height in pixels of one row for a view of height H (ROWS_FIT rows fill it, never under ROW_MIN). */
  const rowHeight = (H, gap) => Math.max(ROW_MIN, Math.floor((H - gap * (ROWS_FIT - 1)) / ROWS_FIT));
  /** Whether card id belongs to page key (one of its own panels) rather than to another page. */
  const isHome = (key, id) => !!(PAGES[key] && PAGES[key].cards.includes(id));
  /** The cards the Add picker offers on page key with layout L: the page's hidden cards, then the cards of every other page and the views
   *  (each with its page), none that shows already. */
  function addable(key, L) {
    const shown = new Set(visible(L).map(c => c.id)), out = [];
    L.filter(c => c.hidden).forEach(c => out.push({ id: c.id, group: 'Hidden on this page', card: BY_ID[c.id] }));
    Object.keys(PAGES).forEach(k => { const P = PAGES[k]; if (k === key || !P.view) return; P.cards.forEach(id => { if (!shown.has(id) && !out.some(o => o.id === id) && !isHome(key, id) && BY_ID[id].page === k) out.push({ id, group: P.group === P.title ? P.title : P.group + ' › ' + P.title, card: BY_ID[id] }); }); });
    VIEWS.forEach(([v]) => { const id = 'v:' + v; if (!shown.has(id) && !L.some(c => c.id === id && !c.hidden)) out.push({ id, group: 'Whole views', card: BY_ID[id] }); });
    const on = v => !SL.plugins || SL.plugins.viewOn(v);   // a card of a view whose plugin is off is not offered
    return out.filter(o => !o.card || (o.card.kind === 'view' ? on(o.card.view) : !PAGES[o.card.page] || !PAGES[o.card.page].view || on(PAGES[o.card.page].view)));
  }

  /* ------------------------------------------------------------------------------------------------------------ the document */
  /** A stored document ({version: VERSION, pages: {key: [cards]}}), or null when raw is not one. Pages this version does not know are kept
   *  as they are (another version of the page may know them); a page's cards are read again when it is shown. */
  function normalizeDoc(raw) {
    if (!raw || typeof raw !== 'object' || raw.version !== VERSION || !raw.pages || typeof raw.pages !== 'object' || Array.isArray(raw.pages)) return null;
    raw = { version: VERSION, pages: mergeMoved(raw.pages) };
    const pages = {}; Object.keys(raw.pages).forEach(k => { const l = raw.pages[k]; if (/^[A-Za-z0-9][A-Za-z0-9._:-]{0,95}$/.test(k) && Array.isArray(l)) pages[k] = PAGES[k] ? copy(PAGES[k].view ? cleanList(l) : cleanChrome(l, k)) : l.filter(c => c && typeof c.id === 'string').slice(0, 200); });
    return { version: VERSION, pages };
  }
  /** Pages with the layouts of merged pages (MOVED) carried over: the cards of the old page go under those of the page it joined (its
   *  stored layout, else its default without them), each where it was on its own page; the old key is dropped. */
  function mergeMoved(pages) {
    const out = Object.assign({}, pages);
    Object.keys(MOVED).forEach(old => {
      const L = out[old], to = MOVED[old]; delete out[old]; if (!Array.isArray(L)) return;
      const mine = L.filter(c => c && typeof c.id === 'string'), ids = new Set(mine.map(c => c.id));
      const base = (Array.isArray(out[to]) ? out[to] : defaults(to)).filter(c => c && !ids.has(c.id)), off = base.reduce((m, c) => Math.max(m, (+c.y || 0) + (+c.h || 0)), 0);
      out[to] = base.concat(mine.map(c => Object.assign({}, c, { y: (+c.y || 0) + off })));
    });
    return out;
  }
  /** The parts of the header or the rail as stored: only their own ids, each with hidden and opts. */
  function cleanChrome(list, key) { const seen = new Set(); return list.filter(c => c && PAGES[key].cards.includes(c.id) && !seen.has(c.id) && seen.add(c.id)).map(c => clampCard({ id: c.id, x: 0, y: 0, w: 1, h: 1, hidden: c.hidden, opts: c.opts })); }
  /** The document the first version leaves: its Cockpit layout, the cards it removed hidden (they were taken off, not forgotten). */
  function migrate(store) {
    try {
      const s = store && store.getItem(OLD_KEY); if (!s) return null; const L = normalize(JSON.parse(s)); if (!L) return null;
      PAGES.cockpit.cards.forEach(id => { if (!L.some(c => c.id === id)) { const d = DEFAULT.find(c => c.id === id); L.push(Object.assign(copy1(d), { hidden: true })); } });
      return { version: VERSION, pages: { cockpit: L } };
    } catch (e) { return null; }
  }
  /** The stored document (try/catch: storage may be missing or blocked): this version's, else the first version's carried over, else an
   *  empty one. */
  function readDoc(store) {
    try { const s = store && store.getItem(KEY); if (s) { const d = normalizeDoc(JSON.parse(s)); if (d) return d; } } catch (e) { /* unreadable: the next */ }
    return migrate(store) || { version: VERSION, pages: {} };
  }
  /** Keep the document in storage; false when storage refused it. */
  function writeDoc(store, doc) { try { store.setItem(KEY, JSON.stringify(doc)); return true; } catch (e) { return false; } }
  /** The stored layout of page key (the Cockpit by default), else its default. */
  function load(store, key) { key = key || 'cockpit'; return normalizeList(readDoc(store).pages[key], key); }
  /** Keep the layout of page key in storage (the Cockpit by default); false when storage refused it. */
  function save(store, L, key) { const d = readDoc(store); d.pages[key || 'cockpit'] = copy(L); return writeDoc(store, d); }
  /** Forget the stored layout of page key, or of every page (and the first version's Cockpit) without a key. */
  function forget(store, key) { try { if (!key) { store.removeItem(KEY); store.removeItem(OLD_KEY); return; } const d = readDoc(store); delete d.pages[key]; writeDoc(store, d); } catch (e) { /* nothing stored */ } }
  /** The document as the JSON a person downloads. */
  const exportDoc = doc => JSON.stringify(doc, null, 2);
  /** The document in text a person chose to import, or an Error saying why it is not one. */
  function importDoc(text) {
    let raw; try { raw = JSON.parse(text); } catch (e) { return new Error('the file is not JSON'); }
    if (raw && raw.version !== VERSION) return new Error(raw.version == null ? 'the file is not a layout of Sleipnir' : 'the file is a layout of version ' + raw.version + '; this page reads version ' + VERSION);
    const d = normalizeDoc(raw); return d || new Error('the file is not a layout of Sleipnir');
  }
  SL.dash = { COLS, ROWS_FIT, ROW_MIN, MAX_H, FIT_MAX, VERSION, KEY, OLD_KEY, PAGES, BY_ID, VIEWS, CATALOG, DEFAULT, CHROME_EL, clampCard, overlaps, resolve, normalize, normalizeList, defaults, load, save, forget, move, resize, add, remove, hide, resetSize, setOpt, swap, fitHeight, order, rows, keyOp, firstFit, same, rowHeight, visible, isHome, addable, normalizeDoc, readDoc, writeDoc, migrate, exportDoc, importDoc, MOVED, mergeMoved };

  /* ------------------------------------------------------------------------------------------------------------ keeping it: browser and server */
  const store = () => { try { return window.localStorage; } catch (e) { return null; } };
  /** The document in memory, what is waiting to be sent, and whether the person changed something before the server's copy arrived. */
  const DOCS = { doc: null, timer: 0, edited: false, synced: false, saving: null };
  const PUT_MS = 700;
  const doc = () => DOCS.doc || (DOCS.doc = readDoc(store()));
  const api = () => SL.api;
  /** Send the document to the server a moment after the last change (one request for a burst of changes). */
  function schedulePut() { clearTimeout(DOCS.timer); DOCS.timer = setTimeout(putNow, PUT_MS); }
  function putNow() { DOCS.timer = 0; if (!api() || !api().put) return Promise.resolve(null); const p = api().put('/api/layout', doc()).then(r => { if (r && !r.ok && r.status && r.status !== 404) SL.bus.emit('layout-save-failed', r); return r; }); DOCS.saving = p; return p; }
  /** The layout of page key as kept now. */
  const pageLayout = key => (PAGES[key] && !PAGES[key].view ? cleanChrome(doc().pages[key] || [], key) : normalizeList(doc().pages[key], key));
  /** Keep the layout of page key: in this browser now, on the server shortly. */
  function keepPage(key, L) { doc().pages[key] = copy(L); DOCS.edited = true; writeDoc(store(), doc()); schedulePut(); SL.bus.emit('layout-saved', key); }
  /** Forget the layout of page key (its default comes back), here and on the server. */
  function resetPage(key) { delete doc().pages[key]; DOCS.edited = true; writeDoc(store(), doc()); if (api() && api().del) api().del('/api/layout?page=' + encodeURIComponent(key)); SL.bus.emit('layout-changed', [key]); }
  /** Forget the layout of every page, here and on the server. */
  function resetAll() { const keys = Object.keys(doc().pages); DOCS.doc = { version: VERSION, pages: {} }; DOCS.edited = true; clearTimeout(DOCS.timer); forget(store()); writeDoc(store(), DOCS.doc); if (api() && api().del) api().del('/api/layout'); SL.bus.emit('layout-changed', keys.concat(Object.keys(PAGES))); }
  /** Replace the whole document (an import): kept here and sent to the server at once. */
  function replaceDoc(d) { DOCS.doc = d; DOCS.edited = true; writeDoc(store(), d); clearTimeout(DOCS.timer); putNow(); SL.bus.emit('layout-changed', Object.keys(PAGES)); }
  /** Read the server's copy at boot: when the server has one, it is the person's layout (it follows them across browsers) unless they
   *  changed something here before it arrived; when it has none, this browser's copy is sent to it. */
  function sync() {
    if (!api() || !api().get) return Promise.resolve(false);
    return api().get('/api/layout').then(r => {
      DOCS.synced = true; if (!r || !r.ok || !r.data) return false;
      const srv = normalizeDoc(r.data), has = srv && Object.keys(srv.pages).length;
      if (!has) { if (Object.keys(doc().pages).length) putNow(); return false; }
      if (DOCS.edited) { putNow(); return false; }
      const before = JSON.stringify(doc().pages), changed = Object.keys(PAGES).concat(Object.keys(srv.pages)).filter(k => JSON.stringify(doc().pages[k]) !== JSON.stringify(srv.pages[k]));
      DOCS.doc = srv; writeDoc(store(), srv); if (before !== JSON.stringify(srv.pages)) SL.bus.emit('layout-changed', changed);
      return true;
    });
  }
  SL.dash.docs = { doc, pageLayout, keepPage, resetPage, resetAll, replaceDoc, sync, putNow, flush: () => (DOCS.timer ? (clearTimeout(DOCS.timer), putNow()) : DOCS.saving || Promise.resolve(null)), state: DOCS };
  SL.dash.sync = sync;

  /* ------------------------------------------------------------------------------------------------------------ a small menu */
  const MENU = { cur: null };
  /** Close the open menu (and give the focus back to its button when back is true). */
  function closeMenu(back) { const c = MENU.cur; if (!c) return false; MENU.cur = null; c.p.remove(); document.removeEventListener('pointerdown', c.away, true); document.removeEventListener('keydown', c.keys, true); if (back && c.btn && document.contains(c.btn)) c.btn.focus(); return true; }
  /** A menu under btn: items are {label, sub?, check? (a ticked or unticked choice), act(), off?} or {sep: 'heading'}. The arrows move
   *  between the items, Escape closes it; a click on an item closes it and does what the item says. */
  function menu(btn, items, label) {
    if (MENU.cur && MENU.cur.btn === btn) { closeMenu(false); return null; }
    closeMenu(false);
    const app = document.getElementById('app') || document.body, p = U.mk('div', { class: 'popover dmenu', role: 'menu', 'aria-label': label || 'Menu' });
    items.forEach((it, i) => {
      if (it.sep != null) { p.appendChild(U.mk('div', { class: 'dmh', role: 'presentation' }, esc(it.sep))); return; }
      const b = U.mk('button', { type: 'button', role: it.check != null ? 'menuitemcheckbox' : 'menuitem', class: 'dmi', 'data-i': String(i) }, (it.check != null ? '<span class="dmc" aria-hidden="true">' + (it.check ? '✓' : '') + '</span>' : '') + '<span class="dml">' + esc(it.label) + (it.sub ? '<small>' + esc(it.sub) + '</small>' : '') + '</span>');
      if (it.check != null) b.setAttribute('aria-checked', String(!!it.check)); if (it.off) b.disabled = true;
      p.appendChild(b);
    });
    app.appendChild(p);
    const r = btn.getBoundingClientRect(), ar = app.getBoundingClientRect(), w = window.innerWidth || 1024;
    p.style.top = (r.bottom - ar.top + 4) + 'px'; if (r.left - ar.left > w / 2) p.style.right = Math.max(8, ar.right - r.right) + 'px'; else p.style.left = Math.max(8, r.left - ar.left) + 'px';
    const btns = () => Array.from(p.querySelectorAll('.dmi')).filter(b => !b.disabled), first = btns()[0]; if (first) first.focus();
    const away = e => { if (!p.contains(e.target) && !(btn.contains && btn.contains(e.target))) closeMenu(false); };
    const keys = e => {
      if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); closeMenu(true); return; }
      if (e.key === 'Tab') { closeMenu(false); return; }
      if (e.key === 'ArrowDown' || e.key === 'ArrowUp' || e.key === 'Home' || e.key === 'End') { const bs = btns(), i = bs.indexOf(document.activeElement); if (!bs.length) return; e.preventDefault(); e.stopPropagation(); const n = e.key === 'Home' ? 0 : e.key === 'End' ? bs.length - 1 : (i + (e.key === 'ArrowDown' ? 1 : bs.length - 1)) % bs.length; bs[n].focus(); }
    };
    p.addEventListener('click', e => { const b = e.target.closest('.dmi'); if (!b || b.disabled) return; const it = items[+b.dataset.i]; closeMenu(true); if (it && it.act) it.act(); });
    MENU.cur = { p, btn, away, keys };
    document.addEventListener('pointerdown', away, true); document.addEventListener('keydown', keys, true);
    return p;
  }

  /* ------------------------------------------------------------------------------------------------------------ the grid of a page */
  const LOCK = '<svg viewBox="0 0 16 16" width="15" height="15" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="7" width="10" height="7" rx="1.3"/><path d="M5.2 7V5a2.8 2.8 0 0 1 5.6 0v2"/></svg>';
  const UNLOCK = '<svg viewBox="0 0 16 16" width="15" height="15" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="7" width="10" height="7" rx="1.3"/><path d="M5.2 7V5a2.8 2.8 0 0 1 5.5-.8"/></svg>';
  /** The lock of the page: locked until the person unlocks it (it is not kept: every page load starts locked); one lock for every page. */
  const D = { locked: true };
  /** The mounted page ({root, key, layout, setLocked, add, remove, hide, reset, picker}), or null. */
  let M = null;
  /** Say a line to a screen reader (the page's polite live region). */
  const say = t => { const a = document.getElementById('announcer'); if (a) a.textContent = t; };
  /** A card's class: dc-<its id> with letters, digits and - only. */
  const cls = id => 'dc-' + String(id).replace(/[^A-Za-z0-9-]/g, '-');
  const narrowQ = () => { try { return !!(window.matchMedia && window.matchMedia('(max-width: 900px)').matches); } catch (e) { return false; } };
  /** How a view is mounted: home (its page, with the grid), only (one card of it inside a card of another page) or flat (the whole view
   *  inside a card, its cards in their order). */
  const modeOf = params => (params && params.__only ? 'only' : params && params.__flat ? 'flat' : 'home');

  /**
   * The cards of page key, in host: sc is the view's scope, S the session, params the view's (they say the mode). Returns the page P:
   *   P.slot(id)       the persistent body of card id (in its card when it shows, else kept aside), for the view to draw into;
   *   P.fill(map)      set the markup of several bodies at once ({id: html}); P.put(html) gives each top-level element with a
   *                    data-slot="id" to the body of its card (an element that did not change is left alone);
   *   P.onCard(fn)     fn(id, cardScope, body) for each card of the page that shows, now and when one comes back; the card's scope dies
   *                    when it is hidden (for panels that start timers and hooks of their own);
   *   P.setPage(key)   the same view showing another page of cards (a tab of the Workspace, a page of Settings);
   *   P.home, P.mode, P.key, P.root.
   * opts.sizeEl is the element whose height the rows fill (the view's root by default).
   */
  function mountPage(sc, host, S, key, params, opts) {
    opts = opts || {};
    const mode = modeOf(params), only = mode === 'only' ? params.__only : null;
    const inst = { sc, host, S, key, mode, slots: {}, cards: {}, L: null, mounter: null, P: null };
    let dash = null, grid = null, bar = null, storeEl = null, lockB = null, addB = null, resetB = null, trayB = null, hint = null, wrapEl = null;
    const drag = { on: false };
    const pdef = () => PAGES[inst.key] || { cards: [], wrap: '', title: inst.key };
    if (mode === 'home') {
      host.innerHTML = '<div class="dash" data-arrange="0" data-page="' + esc(key) + '"><div class="dash-bar" role="toolbar" aria-label="The cards of this page"><span class="dash-hint" hidden>Drag a card to move it, its edges to resize it; ⋯ has its options. On a focused card: arrows move, shift+arrows resize, Delete hides. Kept for you on this machine\'s server and in this browser.</span><span class="sp"></span>' +
        '<button class="btn sm dash-tray" type="button" hidden aria-haspopup="menu"></button><button class="btn sm dash-add" type="button" hidden>+ Add card</button><button class="btn sm dash-reset" type="button" hidden>Reset layout</button><button class="btn sm dash-chrome" type="button" hidden aria-haspopup="menu">Header &amp; rail</button>' +
        '<button class="dash-lock" type="button" aria-pressed="false"></button></div><div class="dash-grid" aria-label="Cards"></div><div class="dash-store" hidden></div></div>';
      dash = host.querySelector('.dash'); grid = host.querySelector('.dash-grid'); bar = host.querySelector('.dash-bar'); storeEl = host.querySelector('.dash-store');
      lockB = host.querySelector('.dash-lock'); addB = host.querySelector('.dash-add'); resetB = host.querySelector('.dash-reset'); trayB = host.querySelector('.dash-tray'); hint = host.querySelector('.dash-hint');
      wrapEl = grid;
    } else {
      host.innerHTML = '<div class="dash-x' + (mode === 'flat' ? ' dash-flat' : ' dash-one') + '"><div class="dash-store" hidden></div></div>';
      wrapEl = host.querySelector('.dash-x'); storeEl = host.querySelector('.dash-store');
    }
    function setWrap() { const w = pdef().wrap; wrapEl.className = (mode === 'home' ? 'dash-grid' : 'dash-x' + (mode === 'flat' ? ' dash-flat' : ' dash-one')) + (w ? ' ' + w : ''); if (dash) dash.dataset.page = inst.key; }
    setWrap();
    /** The body of card id: made once, kept aside while the card does not show. */
    function slot(id) { let el = inst.slots[id]; if (!el) { el = U.mk('div', { class: 'dc-body', 'data-slot-of': id }); inst.slots[id] = el; storeEl.appendChild(el); } return el; }
    /** Whether card id shows on this page now. */
    const shows = id => (mode === 'only' ? id === only : visible(inst.L).some(c => c.id === id));
    const gap = () => { const g = parseFloat(window.getComputedStyle && grid ? getComputedStyle(grid).rowGap || getComputedStyle(grid).gap : ''); return isFinite(g) ? g : 8; };
    const rowh = () => (grid ? parseFloat(grid.style.getPropertyValue('--rowh')) || ROW_MIN : ROW_MIN);
    const stacked = () => !!dash && dash.dataset.stack === '1';
    function fit() { if (!grid) return; const el = opts.sizeEl || host, H = el.clientHeight - (bar ? bar.offsetHeight : 0) - 2 * gap(); grid.style.setProperty('--rowh', rowHeight(H > 0 ? H : 0, gap()) + 'px'); }
    /** Whether card id's height follows its content now: a card of this page made so, not sized by hand (a card of another page draws
     *  its view in a scrolled box and keeps its rows). */
    const auto = c => !!(BY_ID[c.id] && BY_ID[c.id].fit && isHome(inst.key, c.id) && !(c.opts && c.opts.size === 'fixed'));
    function place(c) {
      const k = inst.cards[c.id]; if (!k) return; const el = k.el;
      if (grid) { el.style.gridColumn = (c.x + 1) + ' / span ' + c.w; el.style.gridRow = (c.y + 1) + ' / span ' + c.h; el.style.setProperty('--h', c.h); }
      el.style.order = String(c.y * COLS + c.x); const sz = el.querySelector('.dc-size'); if (sz) sz.textContent = c.w + ' × ' + c.h;
      const fitOn = auto(c) ? (c.h >= FIT_MAX ? 'cap' : '1') : '0'; if (el.dataset.fit !== fitOn) el.dataset.fit = fitOn;
      const dn = (c.opts && c.opts.density) || ''; if ((el.dataset.density || '') !== dn) { if (dn) el.dataset.density = dn; else el.removeAttribute('data-density'); }
    }
    function paintAll() { visible(inst.L).forEach(place); if (grid) grid.style.setProperty('--rows', Math.max(rows(inst.L), 1)); paintTray(); }
    function commit(next, keep) { inst.L = next; if (keep !== false && mode === 'home') keepPage(inst.key, inst.L); paintAll(); }

    /* ---- the cards: a card of the page holds its persistent body; a card of another page or a view mounts its home view inside ---- */
    function chrome(d, foreign) {
      const t = esc(d.title);
      return '<div class="dc-edit"><span class="dc-grip" aria-hidden="true">⠿</span><b>' + t + '</b>' + (foreign && d.page ? '<span class="dc-from">' + esc(PAGES[d.page].title) + '</span>' : '') + '<span class="dc-size num"></span>' +
        '<button class="dc-menu" type="button" aria-haspopup="menu" aria-label="Options of the ' + t + ' card" title="Options of this card">⋯</button><button class="dc-x" type="button" aria-label="' + (foreign ? 'Remove' : 'Hide') + ' the ' + t + ' card" title="' + (foreign ? 'Remove this card from this page' : 'Hide this card: the Hidden tray brings it back') + '">×</button></div>' +
        (d.kind === 'view' ? '<div class="dc-h"><h2>' + t + '</h2><button class="dc-open" type="button" title="Open the ' + t + ' view" aria-label="Open the ' + t + ' view">open ↗</button></div>' : '');
    }
    const RS = '<i class="dc-rs" data-rs="e" aria-hidden="true"></i><i class="dc-rs" data-rs="s" aria-hidden="true"></i><i class="dc-rs" data-rs="se" aria-hidden="true"></i>';
    function mountCard(c) {
      const d = BY_ID[c.id]; if (!d) return; const home = isHome(inst.key, c.id);
      if (mode !== 'home') {   // a card of this view outside its page: its body in the flow, in order
        const body = slot(c.id); wrapEl.appendChild(body); const k = { el: body, body, scope: null, home: true }; inst.cards[c.id] = k;
        if (inst.mounter) { k.scope = SL.makeScope(body, 'card:' + c.id); try { inst.mounter(c.id, k.scope, body); } catch (e) { console.error(e); } }
        return;
      }
      const el = U.mk('section', { class: 'dcard ' + (d.kind === 'view' ? 'dc-view' : cls(c.id)) + (home ? '' : ' dc-foreign'), 'data-card': c.id, 'aria-label': d.title }, chrome(d, !home) + RS);
      grid.appendChild(el); const k = { el, body: null, scope: null, home, cancelDrag: null }; inst.cards[c.id] = k;
      if (home) {
        k.body = slot(c.id); el.insertBefore(k.body, el.querySelector('.dc-rs'));
        if (inst.mounter) { k.scope = SL.makeScope(el, 'card:' + c.id); k.scope.onUnmount(() => { if (k.cancelDrag) k.cancelDrag(); }); try { inst.mounter(c.id, k.scope, k.body); } catch (e) { console.error(e); } }
      } else {
        k.body = U.mk('div', { class: 'dc-body' }); el.insertBefore(k.body, el.querySelector('.dc-rs'));
        k.scope = SL.makeScope(el, 'card:' + c.id); k.scope.onUnmount(() => { if (k.cancelDrag) k.cancelDrag(); });
        try { mountForeign(k.scope, k.body, d, inst.S); } catch (e) { console.error(e); }
      }
      if (k.body && window.ResizeObserver) { const ro = new ResizeObserver(() => measureSoon(c.id)); ro.observe(k.body); k.ro = ro; }
      paintEdit(el); place(c);
    }
    function unmountCard(id) {
      const k = inst.cards[id]; if (!k) return; delete inst.cards[id]; if (k.ro) k.ro.disconnect(); if (k.cancelDrag) k.cancelDrag();
      if (k.scope) k.scope.dispose();
      if (k.home) { if (inst.mounter) k.body.innerHTML = ''; storeEl.appendChild(k.body); if (k.el !== k.body) k.el.remove(); }
      else k.el.remove();
    }
    /** Show what L says: cards that left are put away, cards that came are mounted, every card is placed. */
    function apply(next, keep) {
      const vis = new Set((mode === 'only' ? [{ id: only }] : visible(next)).map(c => c.id));
      Object.keys(inst.cards).forEach(id => { if (!vis.has(id)) unmountCard(id); });
      inst.L = next;
      const list = mode === 'only' ? [next.find(c => c.id === only) || { id: only, x: 0, y: 0, w: 12, h: 4 }] : mode === 'flat' ? order(next).filter(c => isHome(inst.key, c.id)) : visible(next);
      list.forEach(c => { if (!inst.cards[c.id]) mountCard(c); });
      if (mode === 'flat') list.forEach(c => { const k = inst.cards[c.id]; if (k) wrapEl.appendChild(k.el); });
      if (keep !== false && mode === 'home') keepPage(inst.key, inst.L);
      paintAll();
    }

    /* ---- a card whose height follows its content: measured when its body changes size ---- */
    const pend = new Set(); let measT = 0;
    function measureSoon(id) { if (mode !== 'home') return; pend.add(id); if (!measT) measT = sc.raf(() => { measT = 0; const ids = Array.from(pend); pend.clear(); ids.forEach(measure); }); }
    function measure(id) {
      const k = inst.cards[id], c = inst.L.find(x => x.id === id); if (!k || !c || c.hidden || stacked() || !auto(c) || drag.on) return;
      const bh = k.body.offsetHeight; if (!bh) return; const rh = rowh(), g = gap(); const h = Math.max(BY_ID[id].minH, Math.min(FIT_MAX, Math.ceil((bh + g) / (rh + g))));
      if (h !== c.h) commit(fitHeight(inst.L, id, h), false);
    }

    /* ---- the edit chrome ---- */
    function paintEdit(el) { if (!el.classList.contains('dcard')) return; const on = !D.locked; if (on) { el.setAttribute('tabindex', '0'); el.setAttribute('aria-roledescription', 'movable card'); } else { el.removeAttribute('tabindex'); el.removeAttribute('aria-roledescription'); } }
    function paintTray() {
      if (!trayB) return; const hid = inst.L.filter(c => c.hidden && BY_ID[c.id]); trayB.hidden = D.locked || !hid.length;
      trayB.innerHTML = 'Hidden <b class="num">' + hid.length + '</b> ▾'; trayB.setAttribute('aria-label', hid.length + ' hidden card' + (hid.length === 1 ? '' : 's') + ': restore one');
    }
    function paintLock() {
      if (!dash) return; const on = !D.locked; dash.dataset.arrange = on ? '1' : '0'; lockB.innerHTML = (on ? UNLOCK : LOCK) + '<span class="dl-t">' + (on ? 'Done' : 'Customise') + '</span>';
      const pt = pdef().title || 'this page';
      lockB.setAttribute('aria-pressed', String(on)); lockB.setAttribute('aria-label', on ? 'Lock the ' + pt + ' page: stop arranging its cards' : 'Customise the ' + pt + ' page: move, resize, add and hide its cards');
      lockB.title = on ? 'Lock the cards in place' : 'Customise: move, resize, add and hide cards';
      addB.hidden = resetB.hidden = hint.hidden = !on; host.querySelector('.dash-chrome').hidden = !on; if (on) grid.setAttribute('data-owns-keys', ''); else grid.removeAttribute('data-owns-keys');
      Object.keys(inst.cards).forEach(id => paintEdit(inst.cards[id].el)); paintTray();
    }
    function setLocked(v) { D.locked = !!v; paintLock(); SL.bus.emit('dash-lock', D.locked); }

    /* ---- what a person does to the cards ---- */
    const title = id => (BY_ID[id] ? BY_ID[id].title : id);
    function focusCard(id) { const k = inst.cards[id]; if (k && k.el.focus) { k.el.focus({ preventScroll: true }); if (k.el.scrollIntoView) k.el.scrollIntoView({ block: 'nearest' }); } }
    function addCard(id) { const next = add(inst.L, id); if (same(next, inst.L)) return; const was = inst.L.find(c => c.id === id); apply(next); focusCard(id); ui.toast((was && was.hidden ? 'restored ' : 'added ') + title(id), 'ok'); }
    function hideCard(id) {
      const i = order(inst.L).findIndex(c => c.id === id); if (i < 0) return; const home = isHome(inst.key, id);
      apply(home ? hide(inst.L, id) : remove(inst.L, id)); const o = order(inst.L), nx = o[Math.min(i, o.length - 1)];
      if (nx && inst.cards[nx.id]) focusCard(nx.id); else if (lockB) lockB.focus();
      ui.toast(home ? 'hid ' + title(id) + ': the Hidden tray brings it back' : 'removed ' + title(id) + ' from this page: Add card brings it back');
    }
    function reset() { resetPage(inst.key); apply(defaults(inst.key), false); ui.toast('this page has its default layout', 'ok'); }
    function openPicker() {
      const list = addable(inst.key, inst.L), groups = [];
      list.forEach(o => { let g = groups.find(x => x.t === o.group); if (!g) groups.push(g = { t: o.group, items: [] }); g.items.push(o); });
      const row = o => '<li data-q="' + esc((o.card.title + ' ' + o.card.desc + ' ' + o.group).toLowerCase()) + '"><button class="btn more dpick" type="button" data-add="' + esc(o.id) + '"><b>' + esc(o.card.title) + '</b><span>' + esc(o.card.desc) + '</span></button></li>';
      ui.modal({ title: 'Add a card', kicker: pdef().title || 'this page', desc: 'a hidden card of this page, any card of another page, or a whole view', color: 'var(--fe)', cls: 'dashpick', focus: '#dpq',
        body: '<div class="field-row"><input id="dpq" type="search" placeholder="filter: governor, budget, rules ..." aria-label="Filter the cards" autocomplete="off"></div>' +
          (groups.length ? groups.map(g => '<section class="dpg"><h3 class="lab">' + esc(g.t) + '</h3><ul class="dpicks">' + g.items.map(row).join('') + '</ul></section>').join('') : '<p class="stubnote">Every card is on this page.</p>') +
          '<div class="row2" style="margin-top:12px"><button class="btn sm" type="button" data-reset>Reset this page to its default layout</button></div>',
        onMount(b, scm, close) {
          scm.listen(b, 'click', e => { const a = e.target.closest('[data-add]'); if (a) { close(); addCard(a.dataset.add); return; } if (e.target.closest('[data-reset]')) { close(); reset(); } });
          const q = b.querySelector('#dpq'); if (q) scm.listen(q, 'input', () => { const w = q.value.trim().toLowerCase().split(/\s+/).filter(Boolean); Array.from(b.querySelectorAll('.dpicks li')).forEach(li => { li.hidden = !w.every(x => (li.dataset.q || '').includes(x)); }); Array.from(b.querySelectorAll('.dpg')).forEach(s => { s.hidden = !Array.from(s.querySelectorAll('li')).some(li => !li.hidden); }); });
        } });
    }
    function trayMenu() {
      const hid = inst.L.filter(c => c.hidden && BY_ID[c.id]);
      menu(trayB, [{ sep: 'Hidden cards' }].concat(hid.map(c => ({ label: title(c.id), sub: BY_ID[c.id].desc, act: () => addCard(c.id) }))).concat(hid.length > 1 ? [{ label: 'Restore all', act: () => { let L = inst.L; hid.forEach(c => { L = add(L, c.id); }); apply(L); ui.toast('restored ' + hid.length + ' cards', 'ok'); } }] : []), 'Hidden cards');
    }
    function cardMenu(btn, id) {
      const c = inst.L.find(k => k.id === id), d = BY_ID[id], home = isHome(inst.key, id); if (!c || !d) return;
      const items = [{ label: home ? 'Hide' : 'Remove from this page', sub: home ? 'the Hidden tray brings it back' : 'Add card brings it back', act: () => hideCard(id) },
        { label: 'Reset size', sub: d.fit ? 'its height follows its content again' : 'its own size: ' + (PAGES[d.page] ? (PAGES[d.page].layout.find(k => k.id === id) || d).w : d.w) + ' columns', act: () => { commit(resetSize(inst.L, id)); say(d.title + ': its own size'); } }];
      if (d.fit) items.push({ label: 'Height follows the content', check: auto(c), act: () => commit(setOpt(inst.L, id, 'size', auto(c) ? 'fixed' : null)) });
      if (d.list) { const dn = (c.opts && c.opts.density) || 'comfortable'; items.push({ sep: 'Density' }, { label: 'Comfortable', check: dn !== 'compact', act: () => commit(setOpt(inst.L, id, 'density', null)) }, { label: 'Compact', sub: 'more rows in the same room', check: dn === 'compact', act: () => commit(setOpt(inst.L, id, 'density', 'compact')) }); }
      if (stacked()) items.push({ sep: 'Order' }, { label: 'Move up', act: () => commit(swap(inst.L, id, -1)) }, { label: 'Move down', act: () => commit(swap(inst.L, id, 1)) });
      if (!home && d.page && PAGES[d.page].view) items.push({ label: 'Open its page', sub: PAGES[d.page].title, act: () => openPage(d.page) });
      menu(btn, items, 'Options of the ' + d.title + ' card');
    }

    /* ---- mounting ---- */
    inst.L = mode === 'home' || mode === 'flat' ? pageLayout(key) : [];
    const me = { root: host, key };
    const P = inst.P = {
      get key() { return inst.key; }, mode, home: mode === 'home', root: host, slot,
      fill(map) { Object.keys(map).forEach(id => { const el = slot(id); if (el._h !== map[id]) { el._h = map[id]; el.innerHTML = map[id]; } }); },
      put(html) {
        const t = document.createElement('template'); t.innerHTML = html; const seen = new Set();
        Array.from(t.content.childNodes).forEach(n => { if (n.nodeType !== 1) return; const id = n.getAttribute('data-slot'); if (!id) return; seen.add(id); const el = slot(id), h = n.outerHTML; if (el._h !== h) { el._h = h; el.innerHTML = ''; el.appendChild(n); } });
        pdef().cards.forEach(id => { if (!seen.has(id)) { const el = slot(id); if (el._h !== '') { el._h = ''; el.innerHTML = '<section class="panel"><p class="stubnote" style="padding:10px 12px;margin:0">Nothing to show here now.</p></section>'; } } });
      },
      onCard(fn) { inst.mounter = fn; Object.keys(inst.cards).forEach(id => { const k = inst.cards[id]; if (k.home && !k.scope) { k.scope = SL.makeScope(k.el, 'card:' + id); try { fn(id, k.scope, k.body); } catch (e) { console.error(e); } } }); },
      setPage(k2) {
        if (k2 === inst.key || !PAGES[k2]) return; const oldHome = pdef().cards.slice(); inst.key = k2; me.key = k2; setWrap();
        if (mode === 'only') return;
        const next = pageLayout(k2); Object.keys(inst.cards).forEach(id => unmountCard(id));
        oldHome.filter(id => !PAGES[k2].cards.includes(id)).forEach(id => { const el = inst.slots[id]; if (el) { el.remove(); delete inst.slots[id]; } });
        apply(next, false); if (dash) paintLock();
      },
      shows,
    };
    if (mode === 'only') { slot(only); apply([{ id: only, x: 0, y: 0, w: 12, h: 4 }], false); }
    else { pdef().cards.forEach(slot); apply(inst.L, false); }
    if (mode === 'home') {
      paintLock(); fit();
      const stackQ = () => { const w = host.clientWidth, narrow = narrowQ() || (stackQ.ro && w > 0 && w <= 620); dash.dataset.stack = narrow ? '1' : '0'; };
      sc.observe(opts.sizeEl || host, () => { stackQ.ro = true; fit(); stackQ(); }); sc.listen(window, 'resize', () => { fit(); stackQ(); }); stackQ();
      wire();
    }
    sc.on('layout-changed', keys => { if (mode === 'only' || !keys || !keys.includes(inst.key) || drag.on) return; apply(pageLayout(inst.key), false); });
    sc.onUnmount(() => { Object.keys(inst.cards).forEach(id => { const k = inst.cards[id]; delete inst.cards[id]; if (k.ro) k.ro.disconnect(); if (k.cancelDrag) k.cancelDrag(); if (k.scope) k.scope.dispose(); }); if (M === me) M = null; closeMenu(false); });

    /* ---- input: the lock, the toolbar, the cards' buttons, the keys and the pointer ---- */
    function wire() {
      sc.listen(lockB, 'click', () => setLocked(!D.locked));
      sc.listen(addB, 'click', () => openPicker());
      sc.listen(trayB, 'click', () => trayMenu());
      sc.listen(host.querySelector('.dash-chrome'), 'click', e => ui.chrome.menu(e.currentTarget || host.querySelector('.dash-chrome')));
      sc.listen(resetB, 'click', () => ui.confirm({ title: 'Reset this page', kicker: 'layout', text: 'Put back the default layout of ' + esc(pdef().title || 'this page') + '? Hidden cards come back, cards of other pages leave, and every card returns to its place and size.', ok: 'Reset', run: () => reset() }));
      sc.listen(grid, 'click', e => {
        const x = e.target.closest('.dc-x'); if (x) { hideCard(x.closest('.dcard').dataset.card); return; }
        const mb = e.target.closest('.dc-menu'); if (mb) { cardMenu(mb, mb.closest('.dcard').dataset.card); return; }
        const o = e.target.closest('.dc-open'); if (o) { const v = BY_ID[o.closest('.dcard').dataset.card].view; if (v === 'workspace' && ui.nav) ui.nav.go((S.ui.ws && S.ui.ws.tab) || 'files'); else SL.views.show(v); }
      });
      sc.listen(grid, 'keydown', e => {
        if (D.locked) return; const el = e.target; if (!el.classList || !el.classList.contains('dcard')) return; const id = el.dataset.card;
        if (e.key === 'Delete') { e.preventDefault(); hideCard(id); return; }
        const next = keyOp(inst.L, id, e.key, e.shiftKey, stacked());
        if (!next) { if (/^Arrow/.test(e.key)) { e.preventDefault(); e.stopPropagation(); } return; }   /* an arrow on a card is the card's, also where it does nothing (a single column) */
        e.preventDefault(); e.stopPropagation();
        if (!same(next, inst.L)) { const was = inst.L.find(k => k.id === id); commit(was && auto(was) && e.shiftKey && e.key.match(/Up|Down/) ? setOpt(next, id, 'size', 'fixed') : next); const c = inst.L.find(k => k.id === id); say(title(id) + ': column ' + (c.x + 1) + ', row ' + (c.y + 1) + ', ' + c.w + ' by ' + c.h); if (stacked()) focusCard(id); }
      });
      /* pointer: drag to move, the edges to resize; the grid follows while the pointer moves */
      sc.listen(grid, 'pointerdown', e => {
        if (D.locked || e.button !== 0 || e.target.closest('.dc-x,.dc-menu')) return; const el = e.target.closest('.dcard'); if (!el) return;
        const id = el.dataset.card, card = inst.cards[id]; if (!card) return; if (card.cancelDrag) card.cancelDrag();
        const c0 = inst.L.find(k => k.id === id); if (!c0) return; const rs = e.target.closest('[data-rs]'), how = rs ? rs.dataset.rs : 'move';
        if (stacked()) return;   // a single column: the keyboard and the card's menu reorder
        e.preventDefault(); const r = grid.getBoundingClientRect(), g = gap(), colP = (r.width + g) / COLS, rowP = rowh() + g;
        const L0 = inst.L, x0 = e.clientX, y0 = e.clientY; el.classList.add('dragging'); document.body.classList.add('resizing'); drag.on = true; try { el.setPointerCapture(e.pointerId); } catch (er) { /* synthetic event */ }
        let cur = L0;
        const mv = ev => {
          const dx = Math.round((ev.clientX - x0) / colP), dy = Math.round((ev.clientY - y0) / rowP);
          let next = how === 'move' ? move(L0, id, c0.x + dx, c0.y + dy) : resize(L0, id, c0.w + (how !== 's' ? dx : 0), c0.h + (how !== 'e' ? dy : 0));
          if (how !== 'move' && how !== 'e' && auto(c0)) next = setOpt(next, id, 'size', 'fixed');   /* a card made taller or shorter by hand keeps its height */
          if (!same(next, cur)) { cur = next; inst.L = next; paintAll(); }
        };
        const clean = () => { el.removeEventListener('pointermove', mv); el.removeEventListener('pointerup', up); el.removeEventListener('pointercancel', cancel); el.classList.remove('dragging'); document.body.classList.remove('resizing'); drag.on = false; card.cancelDrag = null; try { el.releasePointerCapture(e.pointerId); } catch (er) { /* already released */ } };
        const up = () => { clean(); if (!same(cur, L0)) commit(cur); };
        const cancel = () => { clean(); inst.L = L0; paintAll(); }; card.cancelDrag = cancel;
        el.addEventListener('pointermove', mv); el.addEventListener('pointerup', up); el.addEventListener('pointercancel', cancel);
      });
    }
    if (mode === 'home') { Object.assign(me, { layout: () => copy(visible(inst.L)), all: () => copy(inst.L), setLocked, add: addCard, remove: hideCard, hide: hideCard, reset, picker: openPicker, menu: id => { const k = inst.cards[id]; if (k) cardMenu(k.el.querySelector('.dc-menu'), id); }, tray: () => trayMenu() }); M = me; }
    return P;
  }
  /** Draw card d (of another page, or a whole view) into body, in the card's scope: its home view mounts there with only that card. */
  function mountForeign(cs, body, d, S) {
    if (d.kind === 'view') {
      const def = SL.views.V.reg[d.view], inner = U.mk('section', { class: 'cview v-' + d.view, 'data-view': d.view, 'aria-label': d.title }); body.appendChild(inner);
      if (def && SL.plugins && !SL.plugins.viewOn(d.view)) SL.plugins.placeholder(cs, inner, d.view);   // its plugin is off: the note, not the view
      else if (def) def.mount(cs, inner, { __flat: true }, S); else inner.innerHTML = '<p class="stubnote">this view is not available here</p>';
      if (SL.data && SL.data.PAGES && SL.data.PAGES[d.view]) SL.data.forPage(d.view);
      return;
    }
    const pg = PAGES[d.page], def = pg && SL.views.V.reg[pg.view], inner = U.mk('section', { class: 'cview xone v-' + (pg ? pg.view : ''), 'data-view': pg ? pg.view : '', 'aria-label': d.title }); body.appendChild(inner);
    if (!def) { inner.innerHTML = '<p class="stubnote">this card is not available here</p>'; return; }
    if (SL.plugins && !SL.plugins.viewOn(pg.view)) { SL.plugins.placeholder(cs, inner, pg.view); return; }   // its page's plugin is off
    def.mount(cs, inner, Object.assign({}, pg.params, { __only: d.id }), S);
    const dp = pg.view === 'settings' ? pg.params.page : pg.view; if (SL.data && SL.data.PAGES && SL.data.PAGES[dp]) SL.data.forPage(dp);
  }
  /** Open page key: its view (and its tab or Settings page). */
  function openPage(key) { const P = PAGES[key]; if (!P || !P.view) return; if (P.view === 'workspace' && ui.nav) ui.nav.go(P.params.tab); else if (P.view === 'settings' && ui.settingsPage) ui.settingsPage(P.params.page); else SL.views.show(P.view); }

  /** The mounted page's controls (for Settings, the palette and the tour): its layout (the cards that show), the lock, add, hide, reset,
   *  the picker. Without a mounted page they act on what is stored (the Cockpit's) and on the lock of the page. */
  ui.dash = {
    page: mountPage, modeOf, menu, closeMenu, openPage,
    /** A page with a heading above its cards (Tools, Schedule): the heading is left out when the view draws one card of another page. */
    withHead(sc, root, S, key, params, headHtml) {
      if (modeOf(params) === 'only') return mountPage(sc, root, S, key, params);
      root.innerHTML = headHtml + '<div class="dash-host"></div>'; return mountPage(sc, root.querySelector('.dash-host'), S, key, params, { sizeEl: root });
    },
    /** Mount the Cockpit's grid: the first version's call (parts maps a card id to fn(cardScope, body, S)). */
    mount(sc, root, S, parts) { const P = mountPage(sc, root, S, 'cockpit', {}); P.onCard((id, cs, body) => { if (parts[id]) parts[id](cs, body, S); }); return P; },
    get root() { return M ? M.root : null; }, get key() { return M ? M.key : null; }, locked: () => D.locked,
    layout: key => (M && (!key || key === M.key) ? M.layout() : visible(pageLayout(key || 'cockpit'))),
    all: key => (M && (!key || key === M.key) ? M.all() : pageLayout(key || 'cockpit')),
    setLocked(v) { if (M) M.setLocked(v); else D.locked = !!v; },
    add(id) { if (M) M.add(id); }, remove(id) { if (M) M.remove(id); }, hide(id) { if (M) M.hide(id); }, menu(id) { if (M) M.menu(id); }, tray() { if (M) M.tray(); },
    reset(key) { if (M && (!key || key === M.key)) M.reset(); else resetPage(key || 'cockpit'); }, resetAll, picker() { if (M) M.picker(); },
    /** The whole layout as JSON, the file a person downloads. */
    exportText: () => exportDoc(doc()),
    /** Take a layout a person chose (the text of the file): true when it was taken, else the reason it was not. */
    importText(text) { const d = importDoc(text); if (d instanceof Error) return d.message; replaceDoc(d); return true; },
  };

  /* ------------------------------------------------------------------------------------------------------------ the header and the rail */
  /** The parts of the header and the Radio rail that a person hides (and, in the rail, collapses) where they are. */
  ui.chrome = {
    /** The state of part id: 'shown', 'collapsed' or 'hidden'. */
    state(id) { const p = BY_ID[id] && BY_ID[id].page, c = p ? pageLayout(p).find(k => k.id === id) : null; return !c ? 'shown' : c.hidden ? 'hidden' : (c.opts && c.opts.state === 'collapsed' ? 'collapsed' : 'shown'); },
    /** Set the state of part id and keep it. */
    set(id, st) {
      const p = BY_ID[id].page; let L = pageLayout(p).filter(c => c.id !== id);
      if (st !== 'shown') L = L.concat([clampCard({ id, x: 0, y: 0, w: 1, h: 1, hidden: st === 'hidden', opts: st === 'collapsed' ? { state: 'collapsed' } : undefined })]);
      keepPage(p, L); ui.chrome.apply();
    },
    /** Put every part in its state on the page: a hidden one is not drawn; a collapsed one shows as a line that opens it. */
    apply() {
      Object.keys(CHROME_EL).forEach(id => {
        const el = document.querySelector(CHROME_EL[id]); if (!el) return; const st = ui.chrome.state(id), off = st !== 'shown' ? st : '';
        if ((el.getAttribute('data-cust') || '') !== off) { if (off) el.setAttribute('data-cust', off); else el.removeAttribute('data-cust'); }
        if (id.indexOf('rail.') === 0) {
          let stub = document.querySelector('.railstub[data-part="' + id + '"]');
          if (st === 'collapsed') { if (!stub) { stub = U.mk('button', { class: 'railstub', type: 'button', 'data-part': id }); el.parentNode.insertBefore(stub, el); } stub.innerHTML = '<span>' + esc(BY_ID[id].title) + '</span><span class="dim">collapsed · show</span><span aria-hidden="true">▸</span>'; stub.setAttribute('aria-label', 'Show ' + BY_ID[id].title); }
          else if (stub) stub.remove();
        }
      });
    },
    /** The menu of the header's widgets and the rail's parts, under btn. */
    menu(btn, only) {
      const items = [];
      if (only !== 'rail') { items.push({ sep: 'Header and status bar' }); PAGES.hud.cards.forEach(id => { const on = ui.chrome.state(id) === 'shown'; items.push({ label: BY_ID[id].title, sub: BY_ID[id].desc, check: on, act: () => { ui.chrome.set(id, on ? 'hidden' : 'shown'); ui.toast((on ? 'hid ' : 'showing ') + BY_ID[id].title + (on ? ': Customise › Header & rail brings it back' : ''), on ? 'quiet' : 'ok'); } }); }); }
      if (only !== 'hud') {
        items.push({ sep: 'Radio rail' });
        PAGES.rail.cards.forEach(id => { const st = ui.chrome.state(id), can = id !== 'rail.composer';
          items.push({ label: BY_ID[id].title + (st === 'shown' ? '' : ' · ' + st), sub: st === 'shown' ? (can ? 'collapse or hide it' : 'hide it') : 'show it again', check: st === 'shown', act: () => ui.chrome.set(id, st === 'shown' ? (can ? 'collapsed' : 'hidden') : 'shown') });
          if (can && st !== 'hidden') items.push({ label: 'Hide ' + BY_ID[id].title.toLowerCase(), act: () => ui.chrome.set(id, 'hidden') }); });
      }
      items.push({ label: 'Show everything', act: () => { PAGES.hud.cards.concat(PAGES.rail.cards).forEach(id => { if (ui.chrome.state(id) !== 'shown') ui.chrome.set(id, 'shown'); }); ui.toast('the header and the rail show every part', 'ok'); } });
      return menu(btn, items, only === 'rail' ? 'Customise the Radio rail' : 'Customise the header and the rail');
    },
    /** Mount under the shell scope: the rail's own ⋯ button, the collapsed lines, the state kept. */
    mount(sh) {
      const head = document.querySelector('#rail .rail-h');
      if (head && !head.querySelector('.railcust')) { const b = U.mk('button', { class: 'railcust', type: 'button', 'aria-haspopup': 'menu', 'aria-label': 'Customise the Radio rail: collapse or hide its parts', title: 'Collapse or hide the parts of the rail' }, '⋯'); const tog = head.querySelector('.railtog'); head.insertBefore(b, tog || null); sh.listen(b, 'click', () => ui.chrome.menu(b, 'rail')); }
      const rail = document.getElementById('rail'); if (rail) sh.listen(rail, 'click', e => { const s = e.target.closest && e.target.closest('.railstub'); if (s) ui.chrome.set(s.dataset.part, 'shown'); });
      sh.on('layout-changed', () => ui.chrome.apply()); ui.chrome.apply();
    },
  };
})(SL);
