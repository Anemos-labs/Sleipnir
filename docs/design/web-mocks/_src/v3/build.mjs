#!/usr/bin/env node
// build.mjs: concatenate the core into ONE self-contained html file.
//   node build.mjs [--manifest manifest.json] [--out OUT.html] [--test]
// The manifest lists css and js files (relative to src/css and src/js next to the manifest), the shell html, the favicon, optional data
// files (inlined before the modules: data.js, outputs.js) and optional json (assigned to window globals). A variant copies the manifest,
// edits the lists, and points --manifest at its copy. --test appends the files of manifest.test (hooks for the verification scripts):
// the shipped build never contains them.
import fs from 'node:fs';
import path from 'node:path';
const argv = process.argv.slice(2), opt = { manifest: null, out: null, test: false };
for (let i = 0; i < argv.length; i++) { if (argv[i] === '--manifest') opt.manifest = argv[++i]; else if (argv[i] === '--out') opt.out = argv[++i]; else if (argv[i] === '--test') opt.test = true; }
const here = path.dirname(new URL(import.meta.url).pathname), mpath = path.resolve(opt.manifest || path.join(here, 'manifest.json')), base = path.dirname(mpath);
const M = JSON.parse(fs.readFileSync(mpath, 'utf8'));
const rd = (p, dir) => fs.readFileSync(path.resolve(dir || base, p), 'utf8');
const css = M.css.map(f => rd(f, path.join(base, 'src/css'))).join('\n');
const shell = rd(M.shell);
const fav = rd(M.favicon).trim(), favURI = 'data:image/svg+xml,' + encodeURIComponent(fav).replace(/'/g, '%27');
// Inlined data may contain the text `</script` or `<!--` inside string literals (a sample index.html, say): in an inline <script> either one
// would end or confuse the HTML parser. Escape them (`<\/script`, `<\!--` mean the same inside a JS string); the modules must not contain them.
const safe = s => s.replace(/<\/(script)/gi, '<\\/$1').replace(/<!--/g, '<\\!--');
const data = (M.data || []).map(f => '/* data: ' + path.basename(f) + ' */\n' + safe(rd(f))).join('\n');
const json = Object.entries(M.json || {}).map(([k, f]) => 'window.' + k + ' = ' + safe(rd(f).trim()) + ';').join('\n');
const mods = M.js.map(f => '/* ' + f + ' */\n' + rd(f, path.join(base, 'src/js'))).join('\n');
const tests = opt.test ? (M.test || []).map(f => '/* test: ' + f + ' */\n' + rd(f, path.join(base, 'test'))).join('\n') : '';
const js = "(function () {\n'use strict';\nconst SL = {};\n" + data + '\n' + json + '\n' + mods + '\n' + tests + '\n})();\n';
if (/<\/script|<!--/i.test(js)) { console.error('build: the script text contains "</script" or "<!--"; escape it as <\\/script or <\\!--'); process.exit(1); }
const html = `<!doctype html>
<html lang="${M.lang || 'en'}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
<title>${M.title}</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="${M.fonts}" rel="stylesheet">
<link rel="icon" type="image/svg+xml" href="${favURI}">
<style>
${css}
</style>
</head>
<body>
${shell}
<script>
${js}</script>
</body>
</html>
`;
const out = path.resolve(opt.out || path.join(here, 'dist', opt.test ? 'test.html' : 'core.html'));
fs.mkdirSync(path.dirname(out), { recursive: true }); fs.writeFileSync(out, html);
console.log('wrote', out, (html.length / 1024).toFixed(1) + ' KB', opt.test ? '(with test hooks)' : '');
