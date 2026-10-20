/* plugins.js: the reference page's plugin catalogue (window.SLPLUGINS), in the shape of GET /api/plugins (docs/WEB-API.md), and, on the
 * reference page (which has no SL.api), a small in-memory SL.api that answers the plugin routes from it, so that the shipped Plugins view
 * (internal/web/ui/js/98c-ui-plugins.js) runs here on sample data. internal/web/uidev/mockapi.mjs and the node tests read the same
 * catalogue. Every id of the registry is listed; a few are off, mailman waits for swarm (pending), hooks failed to load (error), swarm
 * was turned on and waits for the team to start again (restart), and one external plugin is installed. Nothing here ships. */
(function (G) {
  'use strict';
  const C = (kind, name) => ({ kind, name });
  const row = (id, title, category, description, o) => Object.assign({ id, title, description, category, core: false, experimental: false, external: false, default: true, enabled: true, state: 'on', reason: '', layer: 'profile', requires: [], contributes: [], config_schema: null, config: {} }, o || {});
  const core = (id, title, description, contributes) => row(id, title, 'core', description, { core: true, layer: 'default', reason: 'Sleipnir cannot run without it', contributes: contributes || [] });
  const plugins = [
    /* tools */
    row('fs', 'Files', 'tools', 'Read, write and edit files inside the project.', { contributes: [C('tool', 'read'), C('tool', 'write'), C('tool', 'edit'), C('tool', 'glob'), C('tool', 'grep')] }),
    row('shell', 'Shell', 'tools', 'Run commands in the project, behind the permission rules.', { contributes: [C('tool', 'bash')] }),
    row('web', 'Web', 'tools', 'Fetch web pages and search the web; page text is data, never instructions.', { enabled: false, state: 'off', layer: 'user', contributes: [C('tool', 'web_fetch'), C('tool', 'web_search')],
      config_schema: { type: 'object', properties: { allow_domains: { type: 'string', title: 'Allowed domains', description: 'comma-separated; empty allows every domain' }, timeout_s: { type: 'integer', title: 'Timeout (seconds)', minimum: 1, maximum: 120, default: 20 } } } }),
    row('recall', 'Recall', 'tools', 'Search what earlier sessions of this project said and did.', { contributes: [C('tool', 'recall')] }),
    row('skill', 'Skill loader', 'tools', 'Lets an agent load a skill when the task needs it.', { contributes: [C('tool', 'skill')], requires: ['skills'] }),
    row('memory', 'Memory', 'tools', 'Notes an agent keeps across compactions.', { contributes: [C('tool', 'memory')] }),
    row('plan', 'Plan', 'tools', 'The shared plan: steps the manager writes and the team ticks off.', { contributes: [C('tool', 'plan'), C('web.card', 'plan')] }),
    row('mcp', 'MCP client', 'tools', 'Connect the tool servers listed in the configuration.', { contributes: [C('tool', 'mcp__*'), C('slash', 'mcp'), C('web.setting', 'MCP servers')] }),
    /* extensions */
    row('skills', 'Skills', 'extensions', 'Skills from ~/.sleipnir/skills and the project.', { contributes: [C('skill', '*'), C('slash', 'skills')] }),
    row('builtin-skills', 'Built-in skills', 'extensions', 'The skills that ship with Sleipnir (Blender, reverse engineering ...).', { contributes: [C('skill', 'blender'), C('skill', 'reverse-engineering')] }),
    row('commands', 'Custom commands', 'extensions', 'Your own slash commands from .sleipnir/commands.', { contributes: [C('slash', '*')] }),
    row('agents', 'Agent definitions', 'extensions', 'Extra agent roles defined in .sleipnir/agents.', { contributes: [C('role', '*')] }),
    row('hooks', 'Hooks', 'extensions', 'Commands that run before and after tool calls.', { state: 'error', reason: 'hooks.json line 4: unknown event "PreTool" (did you mean "PreToolUse"?)', contributes: [C('hook', 'PreToolUse'), C('hook', 'PostToolUse')] }),
    row('instructions', 'Instruction files', 'extensions', 'AGENTS.md and CLAUDE.md read into the shared prompt.', { contributes: [C('prompt', 'AGENTS.md')] }),
    /* team */
    row('swarm', 'Team', 'team', 'A manager and workers of eight roles share one board.', { state: 'restart', reason: 'applies when the team starts again', layer: 'project', contributes: [C('role', 'manager'), C('slash', 'swarm'), C('web.view', 'Board')],
      config_schema: { type: 'object', properties: { workers: { type: 'integer', title: 'Workers', description: '0 is a single agent; a team has at least 8', minimum: 0, default: 8 }, isolation: { type: 'string', title: 'Isolation', enum: ['none', 'worktree'], default: 'worktree' } } } }),
    row('mailman', 'Mailman', 'team', 'Reads worker mail and routes it, so the manager reads less.', { state: 'pending', reason: 'needs swarm', requires: ['swarm'], contributes: [C('role', 'mailman'), C('event', 'mail')],
      config_schema: { type: 'object', properties: { model: { type: 'string', title: 'Model', description: 'the model mailman runs on; empty uses the worker model' } } } }),
    row('auto-docs', 'Auto docs', 'team', 'Keeps README and docs in step with the code after each merge.', { requires: ['swarm'], contributes: [C('role', 'docs')], config_schema: { type: 'object', properties: { paths: { type: 'string', title: 'Paths', default: 'README.md, docs/' } } } }),
    row('manager-approves', 'Manager approves', 'team', 'The manager answers the routine questions of its workers.', { requires: ['swarm'], contributes: [C('hook', 'approval')] }),
    row('push-progress', 'Push progress', 'team', 'Pushes the branch and comments on the pull request as tasks merge.', { experimental: true, default: false, enabled: false, state: 'off', contributes: [C('event', 'push')], config_schema: { type: 'object', properties: { every: { type: 'string', title: 'Push after', enum: ['task', 'hour'], default: 'task' }, draft: { type: 'boolean', title: 'Open as draft', default: true } } } }),
    row('goal', 'Goal', 'team', 'A goal the team keeps working on until it is met.', { contributes: [C('slash', 'goal'), C('web.card', 'goal')], config_schema: { type: 'object', properties: { max_turns: { type: 'integer', title: 'Turns at most', minimum: 1, default: 40 } } }, config: { max_turns: 60 } }),
    row('delivery', 'Delivery', 'team', 'Opens a pull request, fixes CI and review comments, and merges.', { default: false, enabled: false, state: 'off', contributes: [C('slash', 'delivery'), C('cli', 'pr')] }),
    row('checkpoints', 'Checkpoints', 'team', 'A checkpoint after every change, to diff and restore.', { contributes: [C('slash', 'rewind'), C('web.view', 'Checkpoints')] }),
    /* interface */
    row('view:cockpit', 'Cockpit view', 'interface', 'The horse, the stalls, the gantt and the board on one page.', { contributes: [C('web.view', 'Cockpit'), C('web.nav', 'Cockpit')] }),
    row('view:workspace', 'Workspace view', 'interface', 'Files, Changes, Checkpoints and Merge, with the diff.', { contributes: [C('web.view', 'Files'), C('web.view', 'Changes'), C('web.view', 'Checkpoints'), C('web.view', 'Merge')] }),
    row('view:cache', 'Cache view', 'interface', 'The prompt by layer, requests and cache breaks.', { contributes: [C('web.view', 'Cache')] }),
    row('view:mail', 'Mail view', 'interface', 'Every mail between agents, with its text.', { contributes: [C('web.view', 'Mail')] }),
    row('view:board', 'Board view', 'interface', 'The task board with its dependencies.', { contributes: [C('web.view', 'Board')] }),
    row('view:replay', 'Replay view', 'interface', 'The session as a recording: scrub and speed.', { enabled: false, state: 'off', layer: 'user', contributes: [C('web.view', 'Replay')] }),
    row('view:sessions', 'Sessions view', 'interface', 'Live and recorded sessions, prune and delete.', { contributes: [C('web.view', 'Sessions')] }),
    row('view:tools', 'Tools view', 'interface', 'Every sleipnir command with a form, Doctor and Schedule.', { contributes: [C('web.view', 'Tools'), C('web.view', 'Doctor'), C('web.view', 'Schedule')] }),
    row('view:plugins', 'Plugins view', 'interface', 'This page: turn parts of Sleipnir on and off.', { contributes: [C('web.view', 'Plugins')] }),
    /* external */
    row('acme-review', 'Acme review', 'external', 'Reviews each merged task against the Acme style guide.', { external: true, experimental: true, default: false, enabled: false, state: 'off', layer: 'default', contributes: [C('skill', 'acme-style'), C('slash', 'acme-review'), C('web.card', 'Acme review')], config_schema: { type: 'object', properties: { strict: { type: 'boolean', title: 'Strict', description: 'fail a task on any finding', default: false } } } }),
    /* core */
    core('core:kernel', 'Kernel', 'The plugin registry and the event bus everything else plugs into.'),
    core('core:config', 'Configuration', 'Reads the layered configuration files.', [C('cli', 'config')]),
    core('core:session-log', 'Session log', 'Records every session so it can be resumed and replayed.', [C('cli', 'sessions')]),
    core('core:agent-loop', 'Agent loop', 'Sends the prompt, runs the tools, appends the results.'),
    core('core:providers', 'Providers', 'Reaches the model endpoints and keeps their keys.', [C('cli', 'login'), C('provider', '*')]),
    core('core:web-shell', 'Web shell', 'This page\'s frame: the header, the rails and the chat.'),
    core('view:settings', 'Settings view', 'Models, roles, budget, permissions and the rest.', [C('web.view', 'Settings')]),
  ];
  const catalogue = {
    profile: 'standard',
    profiles: [
      { id: 'standard', title: 'Standard', description: 'Everything most projects need: the team, the tools and every view.' },
      { id: 'minimal', title: 'Minimal', description: 'A single agent with files and the shell; no team, no extras.' },
      { id: 'team', title: 'Team', description: 'Standard, with delivery and progress pushes for unattended teams.' },
      { id: 'headless', title: 'Headless', description: 'For scheduled runs: no web views, nobody to answer.' },
    ],
    restart: true,
    plugins,
    /* the programs and tool servers the plugins that are on use (GET /api/needs): Blender located, uv on PATH, the Blender tool server not yet connected */
    needs: [
      { id: 'blender', plugin: 'builtin-skills', title: 'Blender', description: 'the 3D suite the Blender skills drive (renders, models, GLB exports)', homepage: 'https://www.blender.org', required: false, server: false, found: true, path: '/opt/blender-4.5/blender', via: 'located', version: 'Blender 4.5.3 LTS', methods: [{ id: 'flatpak', title: 'Flatpak (Flathub)', setup: [['flatpak', 'remote-add', '--user', '--if-not-exists', 'flathub', 'https://dl.flathub.org/repo/flathub.flatpakrepo']], argv: ['flatpak', 'install', '--user', '-y', '--noninteractive', 'flathub', 'org.blender.Blender'], available: true }] },
      { id: 'blender-mcp', plugin: 'builtin-skills', title: 'Blender tool server', description: 'lets agents drive a running Blender through tools (MCP)', homepage: 'https://github.com/ahujasid/mcp-for-blender', required: false, server: true, found: false, methods: [
        { id: 'addon', scope: 'need.install:0123456789abcdef', title: 'Install the Blender add-on and connect', env: { BLENDERMCP_ADDONS_DIR: ['blender', '--background', '--factory-startup', '--python-expr', "import bpy; print('BLENDERMCP_ADDONS_DIR=' + bpy.utils.user_resource('SCRIPTS', path='addons', create=True))"] }, argv: ['uvx', 'mcp-for-blender@2.2.0', 'install-addon'], connect: 'adds the "blender" tool server (uvx mcp-for-blender@2.2.0) to your configuration', note: 'then in Blender enable the add-on and click Connect in the BlenderMCP tab', available: true },
        { id: 'connect', title: 'Connect only (the add-on is already installed)', connect: 'adds the "blender" tool server (uvx mcp-for-blender@2.2.0) to your configuration', available: true }] },
      { id: 'rea', plugin: 'builtin-skills', title: 'REA', description: 'the reverse-engineering workbench\'s command line (rea)', homepage: 'https://github.com/morluto/rea', required: false, server: false, found: false, methods: [{ id: 'npm', title: 'npm (global)', argv: ['npm', 'install', '-g', 'rea-agents@6.7.0'], available: false, reason: 'needs Node.js first' }] },
      { id: 'uv', plugin: 'builtin-skills', title: 'uv', description: 'the Python tool runner that starts the Blender tool server (uvx)', required: false, server: false, found: true, path: '/home/me/.local/bin/uv', via: 'path', methods: [] },
    ],
  };
  G.SLPLUGINS = catalogue;

  /* the reference page: an in-memory SL.api for the plugin routes (the shipped page has the real one) */
  const SL = G.SL; if (!SL || SL.api) return;
  const copy = x => JSON.parse(JSON.stringify(x));
  const db = copy(catalogue);
  const ok = data => Promise.resolve({ ok: true, status: 200, data: copy(data) });
  const none = () => Promise.resolve({ ok: false, status: 404, code: 'not_found', message: 'not served by the reference page' });
  const entry = id => db.plugins.find(p => p.id === id);
  const decide = (p, on, layer) => { p.enabled = !!on; p.layer = layer; p.state = p.category === 'interface' ? (on ? 'on' : 'off') : 'restart'; p.reason = p.state === 'restart' ? 'applies when the team starts again' : ''; db.restart = db.plugins.some(x => x.state === 'restart'); };
  SL.api = {
    tab: id => '/api/sessions/' + encodeURIComponent(String(id)), seg: s => encodeURIComponent(String(s == null ? '' : s)),
    get: () => none(), post: () => none(), del: () => none(),
    plugins: () => ok(db),
    setPlugin(id, body) {
      const p = entry(id); if (!p) return none(); if (p.core) return Promise.resolve({ ok: false, status: 409, code: 'core', message: p.title + ' is always on' });
      if (body && typeof body.enabled === 'boolean') decide(p, body.enabled, body.scope === 'project' ? 'project' : 'user');
      if (body && body.config) p.config = Object.assign({}, p.config, body.config);
      return ok({ plugin: p, restart: db.restart });
    },
    setProfile(profile) { if (!db.profiles.some(x => x.id === profile)) return none(); db.profile = profile; return ok(db); },
    needs: () => ok({ needs: db.needs }),
    locateNeed(id, path) { const n = db.needs.find(x => x.id === id); if (!n || n.server) return none(); Object.assign(n, { found: true, path: String(path), via: 'located' }); return ok(n); },
    forgetNeed(id) { const n = db.needs.find(x => x.id === id); if (!n) return none(); Object.assign(n, { found: false, path: '', via: '', version: '' }); return ok(n); },
    installNeed(id, method) { const n = db.needs.find(x => x.id === id); if (!n) return none(); Object.assign(n, { found: true, via: n.server ? 'config' : 'path', install: { method, running: false, output: 'done\n' } }); return ok(n); },
    resetPlugin(id) { const p = entry(id); if (!p) return none(); decide(p, p.default, 'profile'); return ok({ plugin: p, restart: db.restart }); },
  };
})(window);
