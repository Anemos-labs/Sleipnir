// inventory.mjs: lists what the shipped page (internal/web/ui) offers a person, from the page's own data structures, and writes it to
// internal/parity/webui.json, where the Go tests of the parity guard (internal/parity, cmd/sleipnir) read it without Node.
//
//   node internal/web/uidev/inventory.mjs            rewrite internal/parity/webui.json
//   node internal/web/uidev/inventory.mjs --check    change nothing; exit 1 when the file is out of date
//   sh scripts/gen-webui-inventory.sh [--check]      the same, from the repository root
//
// The page is loaded the way the browser loads it (the scripts of index.html, in its order) into a Node vm context whose DOM is a
// permissive stand-in: every property is another stand-in, innerHTML keeps what was assigned. Nothing is drawn; the inventory reads
//   - the view registry (SL.views), the rail (ui.nav.items) and the palette (SL.palette) with what each entry does when it is run,
//   - the slash handlers of the palette (SL.palette.H) with what each can reach,
//   - the Settings pages (TK.settings.PAGES, and the markup of every page) with each control classified,
//   - the New session dialog and the Resume dialog (their markup, and the command line the dialog builds),
//   - the command runner (the form it builds for every command of the CLI spec, and the mode it runs the command in).
// The CLI spec it reads (internal/web/clispec/clispec.json) is the one the server serves.
//
// Where a fact cannot be read from a data structure (which config key a Settings control changes) the classification is a table in
// this file (SETTING_MAP). A control the table does not know makes the generator fail, so that a new control is classified when it
// is added.
import fs from 'node:fs';
import path from 'node:path';
import vm from 'node:vm';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { webcrypto } from 'node:crypto';

export const REPO = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..', '..');
export const UI = path.join(REPO, 'internal', 'web', 'ui');
export const SPEC_FILE = path.join(REPO, 'internal', 'web', 'clispec', 'clispec.json');
export const OUT_FILE = path.join(REPO, 'internal', 'parity', 'webui.json');
export const REGENERATE = 'sh scripts/gen-webui-inventory.sh';

/* ------------------------------------------------------------------------------------------------------------ the stand-in DOM */

/** A stand-in for any DOM object: reading an unknown property gives another stand-in, calling it gives one, and what is assigned is kept. */
function stub(name) {
  const store = Object.create(null);
  const target = function () {};
  return new Proxy(target, {
    get(t, k) {
      if (k === Symbol.toPrimitive) return () => '';
      if (k === 'then' || typeof k === 'symbol') return undefined;
      if (k in store) return store[k];
      switch (k) {
        case 'length': return 0;
        case 'dataset': return (store[k] = {});
        case 'classList': return { add() {}, remove() {}, toggle() {}, contains: () => false };
        case 'children': case 'childNodes': return [];
        case 'innerHTML': case 'textContent': case 'value': case 'className': case 'id': return '';
        case 'querySelectorAll': return () => [];
        case 'getAttribute': return () => null;
        case 'hasAttribute': return () => false;
        case 'closest': return () => null;
        case 'contains': return () => false;
        case 'getClientRects': return () => [];
        case 'isConnected': return true;
        default: return (store[k] = stub(name + '.' + String(k)));
      }
    },
    set(t, k, v) { store[k] = v; return true; },
    apply() { return stub(name + '()'); },
    has() { return false; },
  });
}

/** The context the page's scripts run in. */
function makeContext() {
  const document = stub('document');
  document.readyState = 'loading';   // 99-app.js waits for DOMContentLoaded, which never comes: the page does not boot
  document.title = 'Sleipnir Web';
  document.hidden = false;
  document.createElement = () => stub('element');
  document.createElementNS = () => stub('element');
  const ctx = {
    console, setTimeout: () => 0, clearTimeout() {}, setInterval: () => 0, clearInterval() {}, Promise, TextEncoder, URLSearchParams, URL, Date, Math,
    performance, crypto: webcrypto, innerWidth: 1400, innerHeight: 900, AbortController,
    location: { search: '', host: '127.0.0.1:6969', href: 'http://127.0.0.1:6969/', reload() {} },
    localStorage: { getItem: () => null, setItem() {}, removeItem() {} },
    navigator: { clipboard: null },
    requestAnimationFrame: () => 0, cancelAnimationFrame() {},
    CSS: { escape: s => s },
    document,
    fetch: async () => { throw new TypeError('no network in the inventory'); },
    EventSource: class { addEventListener() {} close() {} },
    Event: class {}, DOMException,
    addEventListener() {}, removeEventListener() {},
  };
  ctx.window = ctx;
  ctx.globalThis = ctx;
  vm.createContext(ctx);
  return ctx;
}

/** The scripts index.html loads, in its order. */
export function pageScripts() {
  const html = fs.readFileSync(path.join(UI, 'index.html'), 'utf8');
  return [...html.matchAll(/<script src="(js\/[^"]+)"><\/script>/g)].map(m => m[1]);
}

/** Loads the page into a fresh context and returns it with the spec in SL.D.spec. */
function loadPage(spec) {
  const ctx = makeContext();
  for (const s of pageScripts()) {
    try { vm.runInContext(fs.readFileSync(path.join(UI, s), 'utf8'), ctx, { filename: s }); }
    catch (e) { throw new Error('the page script ' + s + ' did not load in the inventory: ' + e.message); }
  }
  const SL = ctx.SL;
  SL.D.spec = { commands: spec.commands, chatSlash: spec.chatSlash, exitCodes: spec.exitCodes };
  return { ctx, SL };
}

/* ------------------------------------------------------------------------------------------------------------ helpers */

const uniq = a => [...new Set(a)];
const sortedUniq = a => uniq(a).sort();
const flagsIn = text => sortedUniq([...String(text).matchAll(/(?<![\w-])--([a-z][a-z0-9-]*)/g)].map(m => m[1]));
const settle = async () => { for (let i = 0; i < 4; i++) await Promise.resolve(); };

/** A session on the page, with a manager and a worker, as the settings pages and the slash handlers read it. */
function seedSession(SL) {
  SL.sessions.make({
    id: 't1', name: 'shop', cwd: '/p',
    meta: { model: 'a/m', mode: 'default', swarm: 1, isolation: 'worktree', trustProject: true, rules: [{ effect: 'allow', rule: 'Bash(go test:*)', origin: 'this session' }] },
    roster: [{ id: 'mgr', role: 'manager', code: 'mgr', model: 'a/m' }, { id: 'be-1', role: 'backend', code: 'be', model: 'a/w' }],
  });
  SL.sessions.activate('t1');
  return SL.sessions.active;
}

/** The data the pages show rows for, so that the controls of a row appear in the markup. */
function seedCatalogues(SL) {
  const D = SL.D, G = SL.G, X = D.extra;
  D.models = [{ ref: 'a/m', provider: 'a', ctx: 100000, in: 1, out: 2, tools: true, reasoning: true }, { ref: 'a/w', provider: 'a', ctx: 100000, in: 1, out: 2, tools: true, reasoning: false }];
  G.favs = new Set(['a/w']);
  G.mcp = [{ name: 'fs', origin: 'project', transport: 'stdio', state: 'running', tools: ['read'] }, { name: 'gh', origin: 'project', transport: 'http', state: 'needs approval', tools: [] }];
  X.mcp = { servers: [{ name: 'fs', prompts: ['sum'] }, { name: 'gh' }] };
  G.providers = [{ id: 'p1', name: 'p1', key: 'none', state: 'no key' }, { id: 'p2', name: 'p2', key: 'stored', state: 'stored' }];
  G.trustDirs = [{ dir: '/p', files: 2, state: 'trusted (Oct 8)' }, { dir: '/q', files: 1, state: 'not trusted' }];
  X.trust = { project: { dir: '/p', state: 'trusted' }, files: [] };
  X.commands = [{ name: 'review', description: 'review', source: 'user' }];
  X.skills = [{ name: 'tdd', summary: 's', source: 'user' }];
  X.config = { layers: [], effective: [], issues: [] };
  SL.sessions.reg.recorded = [{ id: '20260101-000000-abcdef', resumable: true, first: 'a first prompt', agents: 2, cost: 0.5, ageS: 3600 }];
}

/** Replaces what the page does when it acts (open a view, a page, a sheet, a request) with a recorder, and returns the log. Run after
 *  the dialogs were captured: it replaces them. */
function instrument(SL) {
  const log = [];
  const ui = SL.ui;
  const rec = s => { log.push(s); };
  const wrapAll = (obj, prefix, keep) => { for (const k of Object.keys(obj)) if (typeof obj[k] === 'function' && !(keep || []).includes(k)) obj[k] = () => { rec(prefix + k); }; };
  wrapAll(ui.sheets, 'sheet:');
  wrapAll(ui.dialogs, 'dialog:', ['cliLine']);
  wrapAll(ui.ws, 'ws:', ['counts']);
  wrapAll(SL.act, 'act:');
  Object.keys(SL.act).forEach(k => { const f = SL.act[k]; SL.act[k] = function () { f(); return { ok: true, done: Promise.resolve({ ok: true, data: {} }) }; }; });
  SL.views.show = name => { rec('view:' + name); return true; };
  ui.settingsPage = id => rec('settings:' + id);
  ui.runCli = path_ => rec('runner:' + path_.join(' '));
  ui.nav.go = id => { rec('nav:' + id); return true; };
  ui.rail.focus = () => rec('rail:focus');
  ui.confirm = () => rec('confirm');
  ui.toast = () => {};
  ui.closeSessionAsk = () => rec('closeSession');
  ui.compactDialog = () => rec('dialog:compact');
  ui.togglePin = () => rec('pin');
  ui.setDraft = () => rec('draft');
  ui.applyMotion = () => {};
  ui.inbox = { toggle: () => rec('inbox:toggle') };
  /* a command that answers on the page itself (a card in the chat, a switch of the page's own state) */
  const S = SL.sessions.active;
  S.add = ev => { rec('card:' + (ev && ev.k)); };
  S.touch = () => { rec('page-state'); };
  return log;
}

/* ------------------------------------------------------------------------------------------------------------ settings controls */

/** The control a start tag stands for, as one id (null for an element that is not one). Rows of data (a model, a rule, a server) are one id with a star. */
const ATTRS = [['data-seg', 'seg'], ['data-do', 'do'], ['data-flag', 'flag'], ['data-mode', 'mode'], ['data-danger', 'danger'], ['data-role', 'role*'], ['data-fav', 'fav*'],
  ['data-use', 'use*'], ['data-rm', 'rm*'], ['data-trust', 'trust*'], ['data-step', 'step*'], ['data-mcp', 'mcp'], ['data-prov', 'prov*'], ['data-cmd', 'cmd*'],
  ['data-pcmd', 'pcmd*'], ['data-cli', 'cli*'], ['data-flt', 'flt'], ['data-sel', 'sel'], ['id', 'id']];
export function controlsOf(html) {
  const out = [];
  for (const m of String(html).matchAll(/<(button|input|select|textarea)\b([^>]*)>/g)) {
    const attrs = m[2], get = n => { const r = new RegExp('\\s' + n + '="([^"]*)"').exec(attrs); return r ? r[1] : null; };
    for (const [attr, kind] of ATTRS) {
      const v = get(attr);
      if (v == null) continue;
      if (kind.endsWith('*')) out.push(kind); else out.push(kind + ':' + v);
      break;
    }
  }
  return sortedUniq(out);
}

/**
 * What each Settings control is. The page has no registry of which configuration key a control changes, so this table is the audit of
 * it: scope is
 *   config   changes the configuration key(s) in `keys` for the session (or, for the favourites, in the user's file),
 *   flag     changes a flag of `sleipnir chat` for the session (named in `flag`), which no configuration key stands for,
 *   state    changes state that is not configuration (the trust ledger, the stored provider keys, the effort of the session),
 *   view     only filters, tests or opens something,
 *   browser  a setting of this browser, kept in localStorage.
 * Entries are tried in order; the first whose pattern matches the control id of the page wins.
 */
export const SETTING_MAP = [
  // Models
  [/^id:mq$|^flt:|^sel:|^do:refresh-models$|^cli\*$/, { scope: 'view' }],
  [/^fav\*$/, { scope: 'config', keys: ['models.favorites'] }],
  [/^use\*$/, { scope: 'config', keys: ['models.default', 'models.roles.*'] }],
  // Roles & effort
  [/^role\*$/, { scope: 'config', keys: ['models.default', 'models.roles.*'] }],
  [/^seg:effort$/, { scope: 'state', note: 'reasoning effort of the session (/effort); no configuration key' }],
  // Budget
  [/^id:bIn$|^do:budget(-off)?$/, { scope: 'config', keys: ['swarm.budget_usd'] }],
  // Permissions
  [/^mode:|^danger:/, { scope: 'config', keys: ['permissions.mode'] }],
  [/^do:rule-(add|tests)$|^id:rule(In|Eff)$|^rm\*$/, { scope: 'config', keys: ['permissions.allow', 'permissions.ask', 'permissions.deny'] }],
  [/^do:(try|mgr-refused)$|^id:try(Tool|Arg)$|^id:cf(In|Go)$|^do:danger-no$/, { scope: 'view' }],
  // Trust
  [/^trust\*$|^do:forget-all$/, { scope: 'state', note: 'the trust ledger (a file of its own), not configuration' }],
  // Run settings
  [/^step\*$|^id:rn$|^do:apply$/, { scope: 'flag', flag: 'swarm' }],
  [/^seg:isolation$/, { scope: 'config', keys: ['swarm.isolation'], flag: 'isolation' }],
  [/^id:rv$|^do:verify$/, { scope: 'flag', flag: 'verify' }],
  [/^flag:commit$/, { scope: 'flag', flag: 'commit' }],
  [/^flag:mailman$/, { scope: 'config', keys: ['swarm.mailman'], flag: 'mailman' }],
  [/^flag:noMcp$/, { scope: 'flag', flag: 'no-mcp' }],
  [/^flag:trustProject$/, { scope: 'flag', flag: 'trust-project' }],
  [/^do:copycli$/, { scope: 'view' }],
  // MCP servers
  [/^mcp:(approve|revoke)$/, { scope: 'config', keys: ['mcp.*'], note: 'the approval of a project entry (a ledger), not the entry itself' }],
  [/^mcp:(test|reconnect)$/, { scope: 'view' }],
  [/^do:mcp-restart$/, { scope: 'view' }],
  // Skills, commands and hooks
  [/^cmd\*$|^pcmd\*$/, { scope: 'view' }],
  // Providers
  [/^prov\*$/, { scope: 'state', note: 'the stored provider keys (auth.json), not configuration' }],
  // Config layers
  [/^id:cq$/, { scope: 'view' }],
  // Appearance and motion
  [/^seg:(hover|motion|density|cache|title|navmode|radiomode)$/, { scope: 'browser' }],
  [/^do:railreset$/, { scope: 'browser' }],
];

export function classify(id) {
  for (const [re, v] of SETTING_MAP) if (re.test(id)) return v;
  return null;
}

/** The markup of every Settings page with the controls of each, classified; `unclassified` lists controls the table does not know. */
function settingsInventory(SL) {
  const TK = SL.toolkit, S = SL.sessions.active, ST = TK.settings.newState({ page: 'models' });
  const pages = [], unclassified = [];
  for (const [id, title, group, desc] of TK.settings.PAGES) {
    const render = TK.settings.pages[id];
    if (typeof render !== 'function') { unclassified.push(id + ': no renderer'); continue; }
    const html = render(S, ST);
    const controls = controlsOf(html).map(c => {
      const k = classify(c);
      if (!k) { unclassified.push(id + ': ' + c); return { id: c }; }
      return Object.assign({ id: c }, k);
    });
    pages.push({ id, title, group, summary: desc, controls });
  }
  return { pages, unclassified };
}

/* ------------------------------------------------------------------------------------------------------------ the dialogs */

/** What the page's modal was asked to show: its title and body, from a call of fn. */
function captureModal(SL, fn) {
  const ui = SL.ui, keep = ui.modal;
  let got = null;
  ui.modal = spec => { got = spec; };
  try { fn(); } finally { ui.modal = keep; }
  return got;
}

function dialogInventory(SL, chatFlags) {
  const ui = SL.ui;
  const ns = captureModal(SL, () => ui.dialogs.newSession());
  const rs = captureModal(SL, () => ui.dialogs.resume());
  if (!ns || !rs) throw new Error('a dialog did not open in the inventory');
  const labels = [...ns.body.matchAll(/<label>([^<]*)<\/label>/g)].map(m => m[1]);
  const checks = [...ns.body.matchAll(/<label class="chk"><input type="checkbox" data-k="([A-Za-z]+)"[^>]*> ([^<]*)<\/label>/g)].map(m => m[1] + ' ' + m[2].trim());
  const full = { resume: '20260101-000000-abcdef', cwd: '/p', model: 'a/m', mode: 'plan', swarm: 3, isolation: 'worktree', verify: 'v', commit: true, mailman: true, mailmanDefault: false,
    budget: '5', roleModels: { backend: 'a/w' }, rules: ['tests'], trustProject: true, noMcp: true, goalText: '' };
  const line = ui.dialogs.cliLine(full), lineOff = ui.dialogs.cliLine(Object.assign({}, full, { mailman: false, mailmanDefault: true }));
  /* the flags a control of the dialog sets: those its labels and checkboxes name, and a segmented control named after a flag (isolation) */
  const labelFlags = flagsIn(labels.join(' ') + ' ' + checks.join(' '));
  const segFlags = [...ns.body.matchAll(/data-seg="([a-z-]+)"/g)].map(m => m[1]).filter(f => chatFlags.includes(f));
  return {
    newSession: {
      title: ns.title,
      labels,
      checkboxes: checks,
      controlFlags: sortedUniq(labelFlags.concat(segFlags)),
      controlIds: sortedUniq([...ns.body.matchAll(/\bid="(ns[A-Za-z]+)"/g)].map(m => m[1])),
      commandLine: line,
      flags: flagsIn(line),
      flagsMailmanOff: flagsIn(lineOff),
      labelFlags,
      modes: sortedUniq([...ns.body.matchAll(/data-nsmode="([a-z-]+)"/g)].map(m => m[1])),
    },
    resume: {
      title: rs.title,
      flags: flagsIn(rs.body),
      latestButton: /data-r="latest"/.test(rs.body),
      rowButton: /data-r="2026/.test(rs.body),
      settingsButton: /data-rs="2026/.test(rs.body),
    },
  };
}

function runLineInventory(SL) {
  const M = { model: 'a/m', mode: 'plan', swarm: 3, isolation: 'worktree', verify: 'v', budget: 5, commit: true, mailman: true, noMcp: true, trustProject: true };
  const line = SL.toolkit.settings.runLine(M, {});
  return { line, flags: flagsIn(line) };
}

/* ------------------------------------------------------------------------------------------------------------ views, rail, palette, slash */

function viewsInventory(SL) {
  const V = SL.views.V;
  return Object.keys(V.reg).sort().map(name => ({ name, title: V.reg[name].title, nav: V.order.includes(name) }));
}

function navInventory(SL) {
  const sc = SL.makeScope(stub('root'), 'inventory');
  SL.ui.navMount(sc);
  return SL.ui.nav.items.map(i => ({ id: i.id, view: i.view || null, tab: i.tab || null, label: i.label, key: i.key, radio: !!i.radio }));
}

/** What each palette entry does when it is run, and what each slash handler can reach. */
async function paletteInventory(SL, log) {
  const items = SL.palette.filter('', false).filter(c => !c.dyn);
  const out = [];
  for (const c of items) {
    log.length = 0;
    try { SL.palette.run(c, ''); } catch (e) { log.push('error:' + e.message); }
    await settle();
    out.push({ group: c.g, name: c.name, kind: c.slash ? 'slash' : c.view ? 'view' : c.cli ? 'program' : 'action', live: !!c.live, effects: sortedUniq(log) });
  }
  return out;
}

async function slashInventory(SL, log) {
  const H = SL.palette.H, probes = ['', 'x', '2', 'pause', 'a/m', 'high', 'on', 'm=a/m', 'default', 'tests'];
  const handlers = {};
  for (const name of Object.keys(H).sort()) {
    const effects = [];
    for (const arg of probes) {
      log.length = 0;
      try { H[name](arg); } catch (e) { /* a probe the handler does not take */ }
      await settle();
      effects.push(...log);
    }
    handlers[name] = sortedUniq(effects);
  }
  return { handlers, menu: (SL.D.spec.chatSlash || []).map(c => c.cmd).sort() };
}

/* ------------------------------------------------------------------------------------------------------------ the runner */

function runnerInventory(SL) {
  const R = SL.runner, commands = {};
  const shown = R.shownCmds({ q: '' });
  for (const c of shown) {
    const html = R.formHtml(c, { pos: {}, flags: {} });
    commands[c.path.join(' ')] = {
      mode: R.modeOf(c, { flags: {} }),
      positionals: [...html.matchAll(/data-p="([^"]*)"/g)].map(m => m[1]),
      flags: [...html.matchAll(/data-f="([^"]*)"/g)].map(m => m[1]),
      modeWith: Object.fromEntries((c.when || []).map(w => [w.flag, R.modeOf(c, { flags: { [w.flag]: w.values && w.values.length ? w.values[0] : true } })])),
    };
  }
  return { listed: shown.map(c => c.path.join(' ')), commands };
}

/* ------------------------------------------------------------------------------------------------------------ the whole inventory */

/** Builds the inventory of the page. spec is the parsed CLI spec (internal/web/clispec/clispec.json). */
export async function build(spec) {
  const { SL } = loadPage(spec);
  seedCatalogues(SL);
  seedSession(SL);
  const nav = navInventory(SL);
  const views = viewsInventory(SL);
  const settings = settingsInventory(SL);
  const dialogs = dialogInventory(SL, spec.commands.find(c => c.path.join(' ') === 'chat').flags.map(f => f.name));
  const runLine = runLineInventory(SL);
  const runner = runnerInventory(SL);
  const log = instrument(SL);
  SL.palette.build();
  const palette = await paletteInventory(SL, log);
  const slash = await slashInventory(SL, log);
  return {
    generated: 'internal/web/uidev/inventory.mjs: do not edit; regenerate with `' + REGENERATE + '`',
    specGeneratedFrom: spec.generatedFrom,
    views, nav, palette, slash,
    settings: settings.pages, unclassifiedSettings: settings.unclassified,
    newSession: dialogs.newSession, resume: dialogs.resume, runLine, runner,
  };
}

/** The text of the inventory file. */
export function render(inv) { return JSON.stringify(inv, null, 1) + '\n'; }

/** Reads the CLI spec and builds the inventory text. */
export async function generate() {
  const spec = JSON.parse(fs.readFileSync(SPEC_FILE, 'utf8'));
  const inv = await build(spec);
  return { inv, text: render(inv) };
}

async function main(argv) {
  const check = argv.includes('--check');
  const { inv, text } = await generate();
  if (inv.unclassifiedSettings.length) {
    console.error('inventory: Settings controls the table SETTING_MAP of internal/web/uidev/inventory.mjs does not classify:\n  ' + inv.unclassifiedSettings.join('\n  '));
    return 1;
  }
  const have = fs.existsSync(OUT_FILE) ? fs.readFileSync(OUT_FILE, 'utf8') : null;
  if (check) {
    if (have === text) return 0;
    console.error('inventory: internal/parity/webui.json is out of date with the page; regenerate it with `' + REGENERATE + '` and commit it');
    return 1;
  }
  if (have !== text) fs.writeFileSync(OUT_FILE, text);
  return 0;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main(process.argv.slice(2)).then(code => process.exit(code), e => { console.error(e.stack || e); process.exit(1); });
}
