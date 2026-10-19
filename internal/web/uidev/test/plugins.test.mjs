// plugins.test.mjs: the Plugins view and the gating of the page by the plugin catalogue (98c-ui-plugins.js): the groups, the filters and
// the search, the requests a switch, the scope and a settings form send, the core rows, the Apply bar, a view turned off leaving the rail,
// the palette, the chords and the Cockpit's card picker, and a server older than the catalogue (404) leaving everything on. The page is
// the shipped one booted on the fake DOM of pagedom.mjs; the catalogue is the reference page's (mock/js/plugins.js).
import test from 'node:test';
import assert from 'node:assert/strict';
import { boot, FakeEvent } from './pagedom.mjs';
import { pluginsApi } from '../mockapi.mjs';

const plain = x => JSON.parse(JSON.stringify(x));
/** The reference catalogue with some plugins changed: patch maps an id to the fields it gets. */
function catalogue(patch) {
  const c = pluginsApi().catalogue;
  Object.entries(patch || {}).forEach(([id, f]) => Object.assign(c.plugins.find(p => p.id === id), f));
  return c;
}
/** A page whose server has the catalogue c (null: a server older than it, every plugin route 404). */
async function page(c, o) {
  const L = await boot(Object.assign({ route(method, p, init) {
    if (method === 'GET' && (p === '/api/plugins' || /^\/api\/sessions\/[^/]+\/plugins$/.test(p))) return c ? c : { _status: 404, error: 'not found', code: 'not_found' };
    if (method === 'POST' && p.startsWith('/api/plugins/')) {
      if (!c) return { _status: 404, error: 'not found', code: 'not_found' };
      const id = decodeURIComponent(p.slice('/api/plugins/'.length)), b = JSON.parse(init.body || '{}'), e = c.plugins.find(x => x.id === id);
      if (id === 'profile') { c.profile = b.profile; return c; }
      if (typeof b.enabled === 'boolean') Object.assign(e, { enabled: b.enabled, state: b.enabled ? 'on' : 'off', layer: b.scope || 'user' });
      if (b.config) e.config = Object.assign({}, e.config, b.config);
      return { plugin: e, restart: !!c.restart };
    }
    return undefined;
  } }, o || {}));
  await L.settle();
  return L;
}
const show = async (L, v) => { L.SL.views.show(v); await L.settle(); };
const navIds = L => L.doc.querySelectorAll('#nav [data-nav]').map(b => b.dataset.nav);
const paletteViews = L => L.SL.palette.filter('', false).filter(c => c.g === 'go to').map(c => c.name);

test('the catalogue is fetched for the active session at boot, and a view of every category is drawn in the order of the categories', async () => {
  const L = await page(catalogue());
  assert.ok(L.SL.plugins.cat, 'the catalogue arrived');
  await show(L, 'plugins');
  const groups = L.doc.querySelectorAll('.plg-grp').map(g => g.dataset.cat);
  assert.deepEqual(groups, ['tools', 'extensions', 'team', 'interface', 'external', 'core']);
  assert.deepEqual(L.doc.querySelectorAll('.plg-grp h2').map(h => h.textContent), ['Tools', 'Extensions', 'Team', 'Interface', 'External', 'Core']);
  const all = catalogue().plugins.map(p => p.id).sort();
  assert.deepEqual(L.doc.querySelectorAll('.plg').map(r => r.dataset.plg).sort(), all, 'every plugin has a row');
  const mm = L.$('[data-plg="mailman"]');
  assert.match(mm.textContent, /pending: needs swarm/); assert.match(mm.textContent, /adds: role mailman · event mail/); assert.match(mm.textContent, /set by the profile \(Standard\)/);
  assert.match(L.$('[data-plg="hooks"]').textContent, /error/); assert.match(L.$('[data-plg="hooks"] .plg-why').textContent, /unknown event/);
  assert.match(L.$('[data-plg="swarm"]').textContent, /applies on restart/);
  assert.match(L.$('[data-plg="acme-review"]').textContent, /external/);
});

test('the filters and the search: on, off, experimental, external, and every word of the search', async () => {
  const L = await page(catalogue()), P = L.SL.plugins, cat = P.cat;
  const ids = st => P.groups(cat, Object.assign({ q: '', f: 'all' }, st)).flatMap(g => g.rows.map(p => p.id));
  assert.deepEqual(plain(ids({ f: 'off' })).sort(), ['acme-review', 'delivery', 'push-progress', 'view:replay', 'web']);
  assert.deepEqual(plain(ids({ f: 'experimental' })).sort(), ['acme-review', 'push-progress']);
  assert.deepEqual(plain(ids({ f: 'external' })), ['acme-review']);
  assert.ok(ids({ f: 'on' }).includes('core:kernel') && !ids({ f: 'on' }).includes('web'), 'core plugins are on');
  assert.deepEqual(plain(ids({ q: 'MAIL' })).sort(), ['mailman', 'view:mail']);
  assert.deepEqual(plain(ids({ q: 'view replay' })), ['view:replay'], 'every word must appear');
  assert.deepEqual(plain(ids({ q: 'web_fetch' })), ['web'], 'what a plugin adds is searched');
  /* on the page: the chips and the box */
  await show(L, 'plugins');
  L.$('[data-f="external"]').click(); await L.settle();
  assert.deepEqual(L.doc.querySelectorAll('.plg').map(r => r.dataset.plg), ['acme-review']);
  assert.equal(L.$('[data-f="external"]').getAttribute('aria-pressed'), 'true');
  L.$('[data-f="all"]').click(); const q = L.$('#pq'); q.value = 'nothing like this'; q.dispatchEvent(new FakeEvent('input', { bubbles: true })); await L.settle();
  assert.match(L.$('.plg-groups').textContent, /No plugin matches “nothing like this”/);
  q.value = ''; q.dispatchEvent(new FakeEvent('input', { bubbles: true })); await L.settle();
});

test('a switch sends POST /api/plugins/{id} with enabled and the scope, at once; a settings form sends its values; the profile is sent', async () => {
  const L = await page(catalogue());
  await show(L, 'plugins'); L.posts.length = 0;
  const sw = L.$('[data-sw="mailman"]'); sw.checked = false; sw.dispatchEvent(new FakeEvent('change', { bubbles: true })); await L.settle();
  assert.deepEqual(plain(L.posts.map(p => [p.method, p.path, p.body])), [['POST', '/api/plugins/mailman', { enabled: false, scope: 'user' }]]);
  assert.equal(L.SL.plugins.find('mailman').enabled, false, 'the answer\'s entry replaces the old one');
  /* This project: the next change is saved for the project */
  L.$('[data-scope="project"]').click(); await L.settle(); L.posts.length = 0;
  const w = L.$('[data-sw="web"]'); w.checked = true; w.dispatchEvent(new FakeEvent('change', { bubbles: true })); await L.settle();
  assert.deepEqual(plain(L.posts.map(p => [p.path, p.body])), [['/api/plugins/web', { enabled: true, scope: 'project' }]]);
  /* the settings form of goal: an integer field */
  L.$('[data-open="goal"]').click(); await L.settle(); L.posts.length = 0;
  const f = L.$('[data-form="goal"] [data-fk="max_turns"]'); assert.equal(f.getAttribute('type'), 'number'); assert.equal(f.getAttribute('value'), '60', 'the current setting is shown');
  f.value = '80'; L.$('[data-form="goal"]').dispatchEvent(new FakeEvent('submit', { bubbles: true, cancelable: true })); await L.settle();
  assert.deepEqual(plain(L.posts.map(p => [p.path, p.body])), [['/api/plugins/goal', { config: { max_turns: 80 }, scope: 'project' }]]);
  /* an enum field is a select, a boolean a switch */
  L.$('[data-open="push-progress"]').click(); await L.settle();
  assert.equal(L.$('[data-form="push-progress"] [data-fk="every"]').localName, 'select');
  assert.equal(L.$('[data-form="push-progress"] [data-fk="draft"]').getAttribute('type'), 'checkbox');
  /* the profile picker */
  L.posts.length = 0; const sel = L.$('#pprof'); sel.value = 'minimal'; sel.dispatchEvent(new FakeEvent('change', { bubbles: true })); await L.settle();
  assert.deepEqual(plain(L.posts.map(p => [p.path, p.body])), [['/api/plugins/profile', { profile: 'minimal', scope: 'project' }]]);
  L.SL.plugins.state.scope = 'user'; L.SL.plugins.state.open = {};
});

test('a core plugin\'s switch is on and disabled, and says why; a click on it sends nothing', async () => {
  const L = await page(catalogue());
  await show(L, 'plugins'); L.posts.length = 0;
  for (const id of ['core:kernel', 'core:agent-loop', 'view:settings']) {
    const sw = L.$('[data-sw="' + id + '"]'); assert.ok(sw.disabled, id + ' is disabled'); assert.match(sw.parentNode.title, /^Always on: /);
    assert.match(L.$('[data-plg="' + id + '"] .plg-why').textContent, /always on/);
    sw.dispatchEvent(new FakeEvent('change', { bubbles: true }));
  }
  await L.settle(); assert.deepEqual(L.posts, [], 'no request for a core plugin');
});

test('the Apply bar counts the changes that wait for the team and its button restarts the session once', async () => {
  const L = await page(catalogue());
  await show(L, 'plugins');
  const bar = L.$('.plg-apply .pendbar'); assert.ok(bar, 'the bar is shown while a change waits');
  assert.match(bar.textContent, /1 change applies when the team starts again: Team/);
  L.$('[data-do="apply"]').click(); await L.settle(); L.posts.length = 0;
  L.$('#overlayHost [data-ok]').click(); await L.settle();
  assert.deepEqual(plain(L.posts.map(p => [p.path, p.body])), [['/api/sessions/a/restart', { kind: 'restart', fresh: false, flags: [] }]]);
  const c2 = catalogue({ swarm: { state: 'on', layer: 'profile', reason: '' } }); c2.restart = false;
  const L2 = await page(c2); await show(L2, 'plugins');
  assert.ok(L2.$('.plg-apply .pendbar') === null, 'no bar when nothing waits');
});

test('a view turned off leaves the rail, the palette, the g chords and the card picker; showing it draws the note with the way back', async () => {
  const L = await page(catalogue({ 'view:mail': { enabled: false, state: 'off', layer: 'user' }, 'view:workspace': { enabled: false, state: 'off', layer: 'project' } }));
  const nav = navIds(L);
  for (const id of ['mail', 'files', 'changes', 'checkpoints', 'merge', 'replay']) assert.ok(!nav.includes(id), id + ' left the rail');
  for (const id of ['cockpit', 'cache', 'board', 'sessions', 'plugins', 'tools', 'settings']) assert.ok(nav.includes(id), id + ' is in the rail');
  const pal = paletteViews(L);
  for (const n of ['Mail', 'Files', 'Changes', 'Replay']) assert.ok(!pal.includes(n), n + ' left the palette');
  assert.ok(pal.includes('Plugins') && pal.includes('Cache'), 'Plugins and Cache are in the palette');
  /* g m does nothing now; g u opens Plugins */
  await show(L, 'cockpit'); L.doc.body.focus();
  L.press('g'); L.press('m'); await L.settle(); assert.equal(L.SL.views.current.name, 'cockpit', 'g m is not a chord any more');
  L.press('g'); L.press('u'); await L.settle(); assert.equal(L.SL.views.current.name, 'plugins');
  /* the Cockpit's card picker */
  await show(L, 'cockpit'); L.SL.ui.dash.picker(); await L.settle();
  const adds = L.doc.querySelectorAll('#overlayHost [data-add]').map(b => b.dataset.add);
  assert.ok(adds.includes('v:cache') && !adds.includes('v:mail') && !adds.includes('v:workspace'), adds.join(' '));
  L.SL.ui.closeModal(true);
  /* the view itself: the note, and Turn it on sends the request */
  await show(L, 'mail');
  assert.match(L.$('.v-mail').textContent, /This view is turned off\. Turn it on in Plugins\./); L.posts.length = 0;
  L.$('[data-plg-on]').click(); await L.settle();
  assert.deepEqual(plain(L.posts.map(p => [p.path, p.body])), [['/api/plugins/view%3Amail', { enabled: true, scope: 'user' }]]);
  assert.ok(navIds(L).includes('mail'), 'Mail is back in the rail'); assert.ok(L.$('.v-mail') && !/turned off/.test(L.$('.v-mail').textContent), 'the view is drawn again');
});

test('a server older than the catalogue (404 on /api/plugins) leaves every view, rail item and palette entry on', async () => {
  const L = await page(null);
  const P = L.SL.plugins; assert.equal(P.cat, null); assert.ok(P.absent); assert.ok(P.on('view:mail') && P.on('anything') && P.viewOn('workspace'));
  const nav = navIds(L); for (const id of ['cockpit', 'files', 'changes', 'checkpoints', 'merge', 'cache', 'mail', 'board', 'replay', 'sessions', 'plugins', 'tools', 'settings']) assert.ok(nav.includes(id), id);
  const pal = paletteViews(L); for (const n of ['Mail', 'Replay', 'Files', 'Plugins']) assert.ok(pal.includes(n), n);
  await show(L, 'mail'); assert.ok(!/turned off/.test(L.$('.v-mail').textContent));
  await show(L, 'plugins'); assert.match(L.$('.plg-groups').textContent, /older than Sleipnir 0\.3, and every part of it is on/);
});

test('the catalogue is asked of the session first and of the configuration when the session route is missing', async () => {
  const asked = [], c = catalogue();
  const L = await boot({ route(method, p) { if (method === 'GET' && /plugins$/.test(p)) { asked.push(p); return p === '/api/plugins' ? c : { _status: 404, error: 'no', code: 'not_found' }; } return undefined; } });
  await L.settle();
  assert.deepEqual(asked.slice(0, 2), ['/api/sessions/a/plugins', '/api/plugins']);
  assert.ok(L.SL.plugins.cat && L.SL.plugins.cat.plugins.length === c.plugins.length);
});

test('what a plugin adds, in words', async () => {
  const L = await page(null), a = L.SL.plugins.addsText;
  assert.equal(a([]), '');
  assert.equal(a([{ kind: 'tool', name: 'read' }, { kind: 'tool', name: 'write' }, { kind: 'slash', name: 'goal' }, { kind: 'web.view', name: 'Board' }, { kind: 'cli', name: 'pr' }]), 'adds: tools read, write · command /goal · view Board · CLI command sleipnir pr');
});

test('text from the server is text: a hostile title, description, reason, contribution, setting and profile make no element', async () => {
  const EVIL = '<img src=x onerror="window.__pwn=1"><script>window.__pwn=2</script>"\'&';
  const c = catalogue({ mailman: { title: EVIL, description: EVIL, reason: EVIL, contributes: [{ kind: 'role', name: EVIL }], config: { model: EVIL } }, hooks: { reason: EVIL }, 'view:mail': { enabled: false, state: 'off', title: EVIL } });
  c.profiles[0].title = EVIL; c.profiles[0].description = EVIL;
  const L = await page(c);
  await show(L, 'plugins'); L.$('[data-open="mailman"]').click(); await L.settle();
  assert.equal(L.doc.querySelectorAll('.plugv img').length + L.doc.querySelectorAll('.plugv script').length, 0);
  assert.ok(L.$('[data-plg="mailman"] .plg-h b').textContent === EVIL, 'the title shows as typed');
  assert.ok(L.$('[data-form="mailman"] [data-fk="model"]').getAttribute('value') === EVIL, 'the setting shows as typed');
  await show(L, 'mail');
  assert.equal(L.doc.querySelectorAll('.v-mail img').length + L.doc.querySelectorAll('.v-mail script').length, 0);
  assert.ok(L.$('.v-mail h2').textContent === EVIL, 'the note names the plugin as typed');
});
