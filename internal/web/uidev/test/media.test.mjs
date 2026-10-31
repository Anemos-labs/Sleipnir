// media.test.mjs: SL.media (96c-media.js), the previews of files, without a page: what each path is shown as, Markdown rendered from
// hostile text (no markup, script or address of another kind gets through), project links and pictures resolved inside the project,
// CSV parsing and the addresses of served files.
import test from 'node:test';
import assert from 'node:assert/strict';
import { context, load } from './harness.mjs';

const M = load(context(), ['00-namespace.js', '00-util.js', 'api.js', '96b-ws-data.js', '96c-media.js']).SL.media;
const md = (t, o) => M.md(t, o || { path: 'docs/a.md', tab: 't1', at: 'live' });

test('a path is shown by its kind', () => {
  assert.equal(M.kind('a/B.PNG'), 'image'); assert.equal(M.kind('x.svg'), 'svg'); assert.equal(M.kind('v.webm'), 'video');
  assert.equal(M.kind('s.mp3'), 'audio'); assert.equal(M.kind('d.pdf'), 'pdf'); assert.equal(M.kind('README.md'), 'markdown');
  assert.equal(M.kind('flow.mmd'), 'mermaid'); assert.equal(M.kind('t.csv'), 'csv'); assert.equal(M.kind('main.go'), ''); assert.equal(M.kind('constructor'), '');
  assert.ok(M.served('svg') && M.served('pdf') && !M.served('markdown'));
  assert.ok(M.opaque('image') && !M.opaque('svg'));
  assert.equal(M.rawUrl('t1', 'a b/c.png', 'c02', 'v1'), '/api/sessions/t1/ws/raw?path=a%20b%2Fc.png&at=c02&v=v1');
});

test('Markdown keeps no markup of its own and no address but http, https, mailto and project paths', () => {
  const h = md('<script>alert(1)</script>\n\n<img src=x onerror=alert(1)>\n\n[a](javascript:alert(1)) [b](data:text/html,x) [c](https://e.com/?q=1&r="2") ![d](https://e.com/x.png) ![e](../../etc/passwd.png)');
  assert.doesNotMatch(h, /<script|<img src=x|<[^>]*\sonerror=|href="javascript|href="data/);
  assert.match(h, /&lt;script&gt;/);
  assert.match(h, /<a href="https:\/\/e\.com\/\?q=1&amp;r=&quot;2&quot;" target="_blank" rel="noopener noreferrer">c<\/a>/);
  assert.match(h, /class="pv-noimg" href="https:\/\/e\.com\/x\.png"/, 'a picture from elsewhere is a link, never fetched');
  assert.doesNotMatch(h, /ws\/raw[^"]*etc/, 'a picture outside the project is not asked for');
});

test('Markdown blocks: headings, lists, tasks, quotes, tables, code and mermaid', () => {
  const h = md('# T\n\npara **b** *i* `c<d>` ~~s~~\n\n- one\n- two\n  - nested\n- [x] done\n\n1. a\n2. b\n\n> q\n\n| a | b |\n|:-|-:|\n| 1 | 2 |\n\n```go\nfunc f() {}\n```\n\n```mermaid\ngraph TD; A-->B\n```\n\n---\n');
  assert.match(h, /<h1>T<\/h1>/);
  assert.match(h, /<strong>b<\/strong> <em>i<\/em> <code>c&lt;d&gt;<\/code> <del>s<\/del>/);
  assert.match(h, /<ul><li>one<\/li><li>two<ul><li>nested<\/li><\/ul><\/li><li class="task"><input type="checkbox" disabled checked> done<\/li><\/ul>/);
  assert.match(h, /<ol><li>a<\/li><li>b<\/li><\/ol>/);
  assert.match(h, /<blockquote><p>q<\/p><\/blockquote>/);
  assert.match(h, /<th class="r">b<\/th>/);
  assert.match(h, /<pre class="pv-code" data-lang="go"><code><em class="k">func<\/em> f\(\) \{\}<\/code><\/pre>/);
  assert.match(h, /<div class="pv-mmd" data-mmd="m[0-9a-z]+"><span class="dim">Drawing the diagram…<\/span><\/div>/);
  assert.match(h, /<hr>/);
});

test('a relative link opens a project file; a picture of the project is served at the same point', () => {
  const h = md('[up](../README.md#x) ![p](img/a.png) [out](../../x.md)', { path: 'docs/a.md', tab: 't1', at: 'c03', v: 'k' });
  assert.match(h, /data-file="README\.md"/);
  assert.match(h, /src="\/api\/sessions\/t1\/ws\/raw\?path=docs%2Fimg%2Fa\.png&amp;at=c03&amp;v=k"/);
  assert.doesNotMatch(h, /data-file="x\.md"/, 'a path that leaves the project opens nothing');
  assert.match(h, /<button type="button" class="pv-ln linkbtn" data-file="README\.md"/, 'a project link is a button: it opens from the keyboard');
  const sp = md('![s](img/my%20shot.png) [n](notes%20v2.md) [bad](%E0%A4%A.md)', { path: 'docs/a.md', tab: 't1', at: 'live' });
  assert.match(sp, /path=docs%2Fimg%2Fmy%20shot\.png/, 'a percent-encoded picture path is decoded once');
  assert.match(sp, /data-file="docs\/notes v2\.md"/);
  assert.match(sp, /data-file="docs\/%E0%A4%A\.md"/, 'a path that does not decode stays as written');
  assert.equal(M.resolve('a/b.md', '../../c'), '');
  assert.equal(M.resolve('a/b.md', '/c/d.png'), 'c/d.png');
});

test('no held piece of markup ends up inside an attribute', () => {
  const h = md('[x](a`"b`.md) ![`"q`](img/a.png) [y](https://e.com/`x`) <https://e.com/\\*>');
  for (const m of h.matchAll(/\s(?:href|src|alt|title|data-file)="([^"]*)"/g)) assert.doesNotMatch(m[1], /<|\u0000/, m[0]);
});

test('invisible and bidirectional characters stay visible in rendered text', () => {
  assert.match(md('safe‮evil'), /⟨U\+202E⟩/);
});

test('Markdown tables: a document draws a bounded number of cells, however wide the header and however many its rows', () => {
  const wide = '|' + 'h|'.repeat(5000) + '\n|' + '-|'.repeat(5000) + '\n' + '|\n'.repeat(5000);
  const h = md(wide);
  assert.ok((h.match(/<td>/g) || []).length <= 20000, 'too many cells drawn');
  assert.equal((h.match(/<th>/g) || []).length, 60);
  assert.match(h, /more rows not shown/);
  assert.match(h, /4940 more columns not shown/);
  assert.match(md('|' + 'h|'.repeat(61) + '\n|' + '-|'.repeat(61) + '\n|' + 'x|'.repeat(61) + '\n'), /1 more column not shown/, 'a cut column is reported even when the rows fit');
  assert.doesNotMatch(md('| a | b |\n|-|-|\n| 1 | 2 |\n'), /not shown/);
  /* header-only tables count too: many of them draw no more cells than the budget */
  const heads = ('|' + 'h|'.repeat(60) + '\n|' + '-|'.repeat(60) + '\n\n').repeat(1000), hh = md(heads);
  assert.ok((hh.match(/<th>/g) || []).length <= 20000, 'too many header cells drawn');
  assert.match(hh, /A table not shown/);
});

test('CSV: quoted fields, doubled quotes, CRLF; TSV by tab; a table is escaped', () => {
  assert.deepEqual(JSON.parse(JSON.stringify(M.parseCsv('a,b\r\n"x,y","q""z"\n1,2', ','))), [['a', 'b'], ['x,y', 'q"z'], ['1', '2']]);
  assert.deepEqual(JSON.parse(JSON.stringify(M.parseCsv('a\tb\n1\t2\n', '\t'))), [['a', 'b'], ['1', '2']]);
  assert.match(M.tableHtml('h\n<b>x</b>\n', ','), /<td>&lt;b&gt;x&lt;\/b&gt;<\/td>/);
});

test('Markdown of many unclosed link openers renders in linear time', () => {
  for (const t of ['[a'.repeat(30000), '[x]('.repeat(30000), '**a '.repeat(30000)]) {
    const t0 = Date.now();
    md(t);
    assert.ok(Date.now() - t0 < 1000, 'took ' + (Date.now() - t0) + ' ms on ' + t.slice(0, 8));
  }
  assert.match(md('[a](b.md) and [c](https://e.com/y)'), /data-file="docs\/b\.md".*href="https:\/\/e\.com\/y"/);
});

test('code spans and table rows cost linear time on text of many unclosed openers', () => {
  assert.match(md('a `b` c ``d`e`` f'), /<code>b<\/code> c <code>d`e<\/code> f/);
  assert.match(md('`` not closed `x`'), /`` not closed <code>x<\/code>/);
  // Below MAX_INLINE (20000), so the inline parser and its code-span scanner run on it rather than the plain fallback.
  const runs = Array.from({ length: 650 }, (_, i) => '`'.repeat(1 + (i % 50))).join(' x ');
  assert.ok(runs.length < 20000, 'the input is ' + runs.length + ' characters, past MAX_INLINE');
  let t0 = Date.now();
  assert.match(md(runs), /<code>/);
  md(' '.repeat(60000) + '|\n' + ' '.repeat(60000) + '-' + ' '.repeat(60000) + 'x');
  assert.ok(Date.now() - t0 < 2000, 'took ' + (Date.now() - t0) + ' ms');
});

test('a long line that is almost a code fence fails in linear time', () => {
  const t0 = Date.now();
  md('```' + 'a'.repeat(40000) + '`');
  md('```' + ' '.repeat(40000) + '`');
  md('~~~' + 'a'.repeat(40000) + '`');
  assert.ok(Date.now() - t0 < 1000, 'took ' + (Date.now() - t0) + ' ms');
  assert.match(md('````go x\nfunc f() {}\n````\n'), /data-lang="go"/);
  assert.doesNotMatch(md('```a`b\n'), /pv-code/);
});
