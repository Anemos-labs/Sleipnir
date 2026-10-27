/* 96c-media.js: SL.media, the previews of files: images (SVG included), video, audio and PDF drawn by the browser from the bytes the
 * server serves (GET /api/sessions/{id}/ws/raw), Markdown rendered from its text, mermaid diagrams drawn by the sandboxed diagram frame
 * (GET /api/preview/mermaid) and shown as images, CSV and TSV as tables. The Workspace shows a file this way in its Preview view, and
 * the chat and the agent drawer show a picture an agent read or wrote under its tool row.
 *
 * Every piece of a file is data. Markdown is parsed here, never handed to the browser as markup: its text is made printable and escaped,
 * a link keeps only http, https and mailto (a project path opens in the Workspace), an image is a project file or nothing, and raw HTML
 * shows as text. The diagram frame cannot reach the page (it is a document of an opaque origin) and what it sends back is drawn as an
 * <img>, where nothing in an SVG runs. The served files carry a policy of their own that sandboxes them when opened in a tab. */
(function (SL) {
  'use strict';
  const U = SL.u, { esc } = U, F = SL.ws && SL.ws.fmt;
  const media = SL.media = {};
  const vis = t => (F ? F.vis(t) : String(t == null ? '' : t));
  const attr = s => esc(String(s == null ? '' : s));

  /* ================================================================ kinds ============================================================== */
  const EXT = {
    png: 'image', apng: 'image', jpg: 'image', jpeg: 'image', gif: 'image', webp: 'image', avif: 'image', bmp: 'image', ico: 'image', svg: 'svg',
    mp4: 'video', m4v: 'video', webm: 'video', ogv: 'video', mov: 'video',
    mp3: 'audio', wav: 'audio', ogg: 'audio', oga: 'audio', opus: 'audio', m4a: 'audio', aac: 'audio', flac: 'audio',
    pdf: 'pdf', md: 'markdown', markdown: 'markdown', mdx: 'markdown', mmd: 'mermaid', mermaid: 'mermaid', csv: 'csv', tsv: 'tsv'
  };
  /** What a path is shown as: image, svg, video, audio, pdf (bytes the server serves), markdown, mermaid, csv or tsv (text), else ''. */
  media.kind = p => { const m = /\.([A-Za-z0-9]+)$/.exec(String(p || '')); return m && Object.prototype.hasOwnProperty.call(EXT, m[1].toLowerCase()) ? EXT[m[1].toLowerCase()] : ''; };
  /** Whether the kind is drawn from the served bytes (GET /ws/raw), rather than from the file's text. */
  media.served = k => k === 'image' || k === 'svg' || k === 'video' || k === 'audio' || k === 'pdf';
  /** Whether the kind has no text worth reading line by line: its preview stands in for the whole file. */
  media.opaque = k => k === 'image' || k === 'video' || k === 'audio' || k === 'pdf';
  /** The address of a file's bytes at a point (live, base or a checkpoint id) of a tab; v changes the address when the file changes. */
  media.rawUrl = (tab, path, at, v) => SL.api.tab(tab) + '/ws/raw?path=' + encodeURIComponent(path) + '&at=' + encodeURIComponent(at || 'live') + (v ? '&v=' + encodeURIComponent(v) : '');
  const NOUN = { image: 'image', svg: 'SVG image', video: 'video', audio: 'audio file', pdf: 'PDF', markdown: 'Markdown', mermaid: 'diagram', csv: 'table', tsv: 'table' };
  media.noun = k => NOUN[k] || 'file';
  const size = n => (n >= 1 << 20 ? (n / (1 << 20)).toFixed(1) + ' MiB' : n >= 1024 ? Math.round(n / 1024) + ' KiB' : n + ' bytes');

  /* ================================================================ served files ======================================================= */
  /** The links under a served file: open it alone (a sandboxed tab) and save it. */
  const links = (url, name) => '<a class="btn sm" href="' + attr(url) + '" target="_blank" rel="noopener noreferrer">Open in a new tab</a><a class="btn sm" href="' + attr(url) + '" download="' + attr(name) + '">Download</a>';
  /** The preview of a file the browser draws: o = {kind, url, path, size, label}. */
  media.servedHtml = function (o) {
    const name = String(o.path || '').split('/').pop(), k = o.kind, bar = '<div class="pv-bar"><span class="tag">' + esc(media.noun(k)) + '</span>' + (o.size ? '<span class="num dim">' + size(o.size) + '</span>' : '') + '<span class="num dim pv-dim"></span>' + (o.label ? '<span class="dim">' + esc(o.label) + '</span>' : '') + '<span class="pv-sp"></span>' + links(o.url, name) + '</div>';
    let el;
    if (k === 'image' || k === 'svg') el = '<div class="pv-stage pv-img" title="click: actual size / fit"><img class="pv-m" src="' + attr(o.url) + '" alt="' + attr(vis(name)) + '" decoding="async"></div>';
    else if (k === 'video') el = '<div class="pv-stage"><video class="pv-m" controls preload="metadata" src="' + attr(o.url) + '"></video></div>';
    else if (k === 'audio') el = '<div class="pv-stage pv-aud"><audio class="pv-m" controls preload="metadata" src="' + attr(o.url) + '"></audio></div>';
    else if (k === 'pdf') el = '<div class="pv-stage pv-pdfs"><iframe class="pv-pdf" src="' + attr(o.url) + '" title="' + attr(vis(name)) + '" referrerpolicy="no-referrer"></iframe></div>';
    else return '';
    return '<div class="pv">' + bar + el + '</div>';
  };
  /** Two pictures side by side: a file before and after a change (an empty url is a side where the file did not exist). */
  media.compareHtml = function (o) {
    const side = (url, lab) => '<figure class="pv-side"><figcaption class="lab">' + esc(lab) + '</figcaption>' + (url ? '<div class="pv-stage pv-img"><img class="pv-m" src="' + attr(url) + '" alt="' + attr(lab) + '" decoding="async"></div><span class="num dim pv-dim"></span>' : '<p class="ws-none">not there</p>') + '</figure>';
    return '<div class="pv"><div class="pv-bar"><span class="tag">' + esc(media.noun(o.kind)) + '</span><span class="dim">' + esc(o.note || 'before and after') + '</span><span class="pv-sp"></span>' + (o.to ? links(o.to, String(o.path || '').split('/').pop()) : '') + '</div><div class="pv-cmp">' + side(o.from, o.fromLab || 'before') + side(o.to, o.toLab || 'after') + '</div></div>';
  };
  /** A small picture under a tool row of the chat: an image or an SVG file an agent read or wrote. */
  media.thumbHtml = (url, path) => '<span class="pv-thumb"><img class="pv-m" src="' + attr(url) + '" alt="' + attr(vis(String(path || '').split('/').pop())) + '" loading="lazy" decoding="async"></span>';

  /* ================================================================ CSV and TSV ======================================================== */
  const MAX_ROWS = 500, MAX_COLS = 60;
  /** The rows of a CSV (RFC 4180 quoting) or TSV text, at most MAX_ROWS + 1. */
  media.parseCsv = function (text, sep) {
    const rows = []; let row = [], cell = '', q = false, i = 0; const t = String(text || ''), n = t.length;
    for (; i < n && rows.length <= MAX_ROWS; i++) {
      const c = t[i];
      if (q) { if (c === '"') { if (t[i + 1] === '"') { cell += '"'; i++; } else q = false; } else cell += c; continue; }
      if (c === '"' && cell === '') q = true;
      else if (c === sep) { row.push(cell); cell = ''; }
      else if (c === '\n' || c === '\r') { if (c === '\r' && t[i + 1] === '\n') i++; row.push(cell); rows.push(row); row = []; cell = ''; }
      else cell += c;
    }
    if (cell !== '' || row.length) { row.push(cell); rows.push(row); }
    return rows;
  };
  media.tableHtml = function (text, sep) {
    const rows = media.parseCsv(text, sep), more = rows.length > MAX_ROWS; if (more) rows.length = MAX_ROWS;
    if (!rows.length) return '<p class="ws-none">An empty table.</p>';
    const w = Math.min(MAX_COLS, Math.max.apply(null, rows.map(r => r.length))), cells = (r, tag) => { let h = ''; for (let j = 0; j < w; j++) h += '<' + tag + '>' + esc(vis(r[j] == null ? '' : r[j])) + '</' + tag + '>'; return h; };
    return '<div class="pv pv-tbl"><div class="pv-bar"><span class="tag">table</span><span class="num dim">' + (rows.length - 1) + (more ? '+' : '') + ' rows · ' + w + ' columns</span></div><div class="pv-scroll"><table class="tbl"><thead><tr>' + cells(rows[0], 'th') + '</tr></thead><tbody>' +
      rows.slice(1).map(r => '<tr>' + cells(r, 'td') + '</tr>').join('') + '</tbody></table></div>' + (more ? '<p class="ws-help">The first ' + MAX_ROWS + ' rows are shown.</p>' : '') + '</div>';
  };

  /* ================================================================ Markdown =========================================================== */
  /** A project path joined to the directory of the file that names it, or '' when it leaves the project. */
  media.resolve = function (base, ref) {
    const parts = (ref.charAt(0) === '/' ? [] : String(base || '').split('/').slice(0, -1)), out = []; let leaves = false;
    parts.concat(ref.replace(/^\/+/, '').split('/')).forEach(p => { if (p === '..') { if (out.length) out.pop(); else leaves = true; } else if (p && p !== '.') out.push(p); });
    return leaves ? '' : out.join('/');
  };
  const unesc = s => s.replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&quot;/g, '"').replace(/&#39;/g, "'").replace(/&amp;/g, '&');
  /** The address of a link or an image as the reader asked for it, decoded from its escaped form; scheme '' for a project path. */
  function target(raw) {
    const u = unesc(raw).trim().replace(/^<|>$/g, ''), m = /^([A-Za-z][A-Za-z0-9+.-]*):/.exec(u);
    return { u, scheme: m ? m[1].toLowerCase() : '' };
  }
  const MAX_INLINE = 20000;
  /** The inline markup of one escaped line run: code spans, images, links, emphasis, strike-through, hard breaks. */
  // a held piece (\u0000n\u0000) never enters an address or an alt text: those are written into attributes
  function inline(text, o) {
    // a paragraph this long is shown as plain text: the markup's expressions are not linear on every input
    if (text.length > MAX_INLINE) return esc(vis(text)).replace(/\n/g, '<br>');
    const keep = []; const hold = h => '\u0000' + (keep.push(h) - 1) + '\u0000';
    let s = vis(text).replace(/(`+)([^`]|[^`][\s\S]*?[^`])\1(?!`)/g, (m, f, c) => hold('<code>' + esc(c.trim()) + '</code>'));
    s = esc(s.replace(/\\([\\`*_{}[\]()#+\-.!~|<>])/g, (m, c) => hold(esc(c))));
    // a label holds no "[" and an address no "(": each attempt then stops at the next one, so text of many unclosed openers costs
    // linear time, not a scan to its end from each
    s = s.replace(/!\[([^\][\u0000]*)\]\(\s*([^()\s\u0000]+)(?:\s+&quot;[^&]*&quot;)?\s*\)/g, (m, alt, src) => hold(imageHtml(alt, src, o)));
    s = s.replace(/\[([^\][]+)\]\(\s*([^()\s\u0000]+)(?:\s+&quot;[^&]*&quot;)?\s*\)/g, (m, lab, href) => hold(linkHtml(lab, href, o)));
    s = s.replace(/&lt;((?:https?:\/\/|mailto:)[^\s&\u0000]+)&gt;/g, (m, u) => hold(linkHtml(u, u, o)));
    s = s.replace(/(^|[\s(])(https?:\/\/[^\s<\u0000]+[^\s<.,;:!?)'"\u0000])/g, (m, pre, u) => pre + hold(linkHtml(u, u, o)));
    s = s.replace(/(\*\*|__)(?=\S)([\s\S]*?\S)\1/g, '<strong>$2</strong>').replace(/(^|[^*\w])\*(?=\S)([^*]*?\S)\*(?!\*)/g, '$1<em>$2</em>').replace(/(^|[^_\w])_(?=\S)([^_]*?\S)_(?!\w)/g, '$1<em>$2</em>').replace(/~~(?=\S)([\s\S]*?\S)~~/g, '<del>$1</del>');
    s = s.replace(/( {2,}|\\)\n/g, '<br>');
    return s.replace(/\u0000(\d+)\u0000/g, (m, i) => keep[+i]).replace(/\u0000(\d+)\u0000/g, (m, i) => keep[+i]);
  }
  /** A percent-encoded project path decoded (my%20shot.png names my shot.png); text that does not decode stays as it is. */
  const dec = s => { try { return decodeURIComponent(s); } catch (e) { return s; } };
  function linkHtml(lab, raw, o) {
    const t = target(raw), body = lab;
    if (t.scheme === 'http' || t.scheme === 'https' || t.scheme === 'mailto') return '<a href="' + attr(t.u) + '" target="_blank" rel="noopener noreferrer">' + body + '</a>';
    if (t.scheme || t.u.charAt(0) === '#' || !o.path) return '<span class="pv-ln" title="' + attr(t.u) + '">' + body + '</span>';
    const p = media.resolve(o.path, dec(t.u.split('#')[0].split('?')[0]));
    return p ? '<button type="button" class="pv-ln linkbtn" data-file="' + attr(p) + '" title="open ' + attr(vis(p)) + ' in the Workspace">' + body + '</button>' : '<span class="pv-ln">' + body + '</span>';
  }
  function imageHtml(alt, raw, o) {
    const t = target(raw), p = !t.scheme && o.path && o.tab ? media.resolve(o.path, dec(t.u.split('#')[0].split('?')[0])) : '', k = media.kind(p);
    if (p && (k === 'image' || k === 'svg')) return '<img class="pv-m pv-inl" src="' + attr(media.rawUrl(o.tab, p, o.at, o.v)) + '" alt="' + alt + '" loading="lazy" decoding="async">';
    // a picture from elsewhere is not fetched (the page loads nothing from another origin): its address is a link
    return t.scheme === 'http' || t.scheme === 'https' ? '<a class="pv-noimg" href="' + attr(t.u) + '" target="_blank" rel="noopener noreferrer">🖼 ' + (alt || attr(t.u)) + '</a>' : '<span class="pv-noimg">🖼 ' + (alt || attr(t.u)) + '</span>';
  }
  const HR = /^ {0,3}([-*_])(?:\s*\1){2,}\s*$/, ATX = /^ {0,3}(#{1,6})(?:\s+(.*?))?(?:\s+#+)?\s*$/, FENCE = /^ {0,3}(`{3,}|~{3,})\s*([^`\s]*)[^`]*$/, QUOTE = /^ {0,3}> ?/,
    LI = /^( {0,3})([-*+]|\d{1,9}[.)])(\s+|$)(.*)$/, TROW = /^\s*\|?.*\|.*$/, TSEP = /^\s*\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?\s*$/;
  const cellsOf = l => { let s = l.trim(); if (s.startsWith('|')) s = s.slice(1); if (s.endsWith('|') && !s.endsWith('\\|')) s = s.slice(0, -1); return s.split(/(?<!\\)\|/).map(c => c.trim()); };
  /** The blocks of Markdown text as HTML. o = {path, tab, at, v}: the file (for relative links and images) and the point it is read at. */
  function blocks(lines, o, depth) {
    let h = '', i = 0, para = [];
    const flush = () => { if (para.length) { h += '<p>' + inline(para.join('\n'), o) + '</p>'; para = []; } };
    while (i < lines.length) {
      const l = lines[i];
      if (!l.trim()) { flush(); i++; continue; }
      let m = FENCE.exec(l);
      if (m) {
        flush(); const fence = m[1], lang = m[2].toLowerCase(), body = []; i++;
        while (i < lines.length && !(lines[i].trim().startsWith(fence) && /^\s*[`~]+\s*$/.test(lines[i]))) body.push(lines[i++]);
        i++;
        h += lang === 'mermaid' ? media.mermaidHtml(body.join('\n')) : '<pre class="pv-code"' + (lang ? ' data-lang="' + attr(lang) + '"' : '') + '><code>' + body.map(x => F ? F.hl(x) : esc(x)).join('\n') + '</code></pre>';
        continue;
      }
      if ((m = ATX.exec(l))) { flush(); const n = m[1].length; h += '<h' + n + '>' + inline(m[2] || '', o) + '</h' + n + '>'; i++; continue; }
      if (para.length && /^ {0,3}(=+|-+)\s*$/.test(l)) { const n = l.trim().charAt(0) === '=' ? 1 : 2, t = para.join('\n'); para = []; h += '<h' + n + '>' + inline(t, o) + '</h' + n + '>'; i++; continue; }
      if (HR.test(l)) { flush(); h += '<hr>'; i++; continue; }
      if (QUOTE.test(l)) {
        flush(); const q = []; while (i < lines.length && lines[i].trim() && (QUOTE.test(lines[i]) || q.length)) { q.push(lines[i].replace(QUOTE, '')); i++; if (i < lines.length && !QUOTE.test(lines[i])) break; }
        h += '<blockquote>' + (depth < 8 ? blocks(q, o, depth + 1) : esc(q.join('\n'))) + '</blockquote>'; continue;
      }
      if (TROW.test(l) && i + 1 < lines.length && TSEP.test(lines[i + 1]) && l.indexOf('|') >= 0) {
        flush(); const head = cellsOf(l), al = cellsOf(lines[i + 1]).map(c => (/^:-+:$/.test(c) ? 'c' : /-+:$/.test(c) ? 'r' : '')); i += 2; const rows = [];
        while (i < lines.length && lines[i].trim() && lines[i].indexOf('|') >= 0) rows.push(cellsOf(lines[i++]));
        const td = (c, j, tag) => '<' + tag + (al[j] ? ' class="' + al[j] + '"' : '') + '>' + inline(c || '', o) + '</' + tag + '>';
        h += '<div class="pv-scroll"><table class="tbl"><thead><tr>' + head.map((c, j) => td(c, j, 'th')).join('') + '</tr></thead><tbody>' + rows.map(r => '<tr>' + head.map((x, j) => td(r[j], j, 'td')).join('') + '</tr>').join('') + '</tbody></table></div>';
        continue;
      }
      if ((m = LI.exec(l)) && (!para.length || m[4].trim())) {
        flush(); const ordered = /\d/.test(m[2]), start = ordered ? parseInt(m[2], 10) : 1, items = []; let cur = null;
        while (i < lines.length) {
          const x = lines[i], mm = LI.exec(x);
          if (mm && (cur ? mm[1].length < cur.ind : mm[1].length <= 3) && /\d/.test(mm[2]) === ordered) { cur = { ind: mm[1].length + mm[2].length + Math.max(1, Math.min(4, mm[3].length)), body: [mm[4]] }; items.push(cur); i++; continue; }
          if (!x.trim()) { if (i + 1 < lines.length && (/^\s+\S/.test(lines[i + 1]) && cur && lines[i + 1].search(/\S/) >= cur.ind - 1)) { cur.body.push(''); i++; continue; } break; }
          if (cur && (x.search(/\S/) >= 2 || !LI.test(x) && !HR.test(x) && !ATX.test(x) && !FENCE.test(x))) { cur.body.push(x.replace(new RegExp('^ {0,' + cur.ind + '}'), '')); i++; continue; }
          break;
        }
        const li = it => { let b = it.body, task = ''; const tm = /^\[([ xX])\]\s+/.exec(b[0]); if (tm) { task = '<input type="checkbox" disabled' + (tm[1] !== ' ' ? ' checked' : '') + '> '; b = [b[0].slice(tm[0].length)].concat(b.slice(1)); }
          const inner = depth < 8 ? blocks(b, o, depth + 1) : esc(b.join('\n')), tight = b.filter(x => !x.trim()).length === 0 && /^<p>[\s\S]*?<\/p>/.test(inner);
          return '<li' + (task ? ' class="task"' : '') + '>' + task + (tight ? inner.replace(/^<p>([\s\S]*?)<\/p>/, '$1') : inner) + '</li>'; };
        h += ordered ? '<ol' + (start !== 1 ? ' start="' + start + '"' : '') + '>' + items.map(li).join('') + '</ol>' : '<ul>' + items.map(li).join('') + '</ul>';
        continue;
      }
      if (/^ {4,}\S/.test(l) && !para.length) { const body = []; while (i < lines.length && (/^ {4,}/.test(lines[i]) || !lines[i].trim())) body.push(lines[i++].slice(4)); while (body.length && !body[body.length - 1].trim()) body.pop(); h += '<pre class="pv-code"><code>' + body.map(esc).join('\n') + '</code></pre>'; continue; }
      para.push(l.replace(/^ {0,3}/, '')); i++;
    }
    flush();
    return h;
  }
  /** Markdown as HTML (CommonMark's common blocks and GitHub's tables, task lists and strike-through). The front matter of a page is shown
   *  as code. o = {path, tab, at, v}. */
  media.md = function (text, o) {
    const ls = String(text == null ? '' : text).replace(/\r\n?/g, '\n').split('\n'); o = o || {}; let front = '';
    if (ls[0] === '---') { const e = ls.indexOf('---', 1); if (e > 0) { front = '<pre class="pv-code pv-front"><code>' + ls.slice(1, e).map(esc).join('\n') + '</code></pre>'; ls.splice(0, e + 1); } }
    return front + blocks(ls, o, 0);
  };
  media.mdHtml = (text, o) => '<div class="pv"><article class="pv-md">' + media.md(text, o) + '</article></div>';

  /* ================================================================ mermaid ============================================================ */
  const SRC = new Map(), DONE = new Map(), MAX_DONE = 60;   // source of each placeholder key; drawn diagrams by source (most recent last)
  let frame = null, ready = false, seq = 0; const waiting = new Map(), queue = [];
  const keyOf = src => { let x = 2166136261; for (let i = 0; i < src.length; i++) x = Math.imul(x ^ src.charCodeAt(i), 16777619); return 'm' + (x >>> 0).toString(36) + src.length.toString(36); };
  /** The place of a diagram: drawn at once when this source was drawn before, else when the frame answers. */
  media.mermaidHtml = function (src) {
    src = String(src || ''); const k = keyOf(src); SRC.set(k, src); const d = DONE.get(src);
    return '<div class="pv-mmd' + (d ? ' done' : '') + '" data-mmd="' + k + '">' + (d ? drawn(d) : '<span class="dim">Drawing the diagram…</span>') + '</div>';
  };
  const drawn = d => d.error ? '<div class="pv-merr"><b class="err">The diagram could not be drawn.</b> <span class="dim">' + esc(vis(d.error)) + '</span></div><pre class="pv-code"><code>' + esc(vis(d.src)) + '</code></pre>'
    : '<img class="pv-dg" alt="a diagram" width="' + Math.ceil(d.w) + '" height="' + Math.ceil(d.h) + '" src="data:image/svg+xml;charset=utf-8,' + encodeURIComponent(d.svg) + '">';
  function boot() {
    if (frame) return;
    frame = document.createElement('iframe');
    frame.className = 'pv-frame'; frame.setAttribute('sandbox', 'allow-scripts'); frame.setAttribute('aria-hidden', 'true'); frame.tabIndex = -1; frame.title = 'diagram renderer';
    frame.src = '/api/preview/mermaid';
    document.body.appendChild(frame);
  }
  function send(src) { const id = ++seq; waiting.set(id, src); frame.contentWindow.postMessage({ sl: 'mermaid', id, src, theme: 'dark' }, '*'); }
  if (typeof addEventListener === 'function') addEventListener('message', ev => {
    const d = ev.data; if (!frame || ev.source !== frame.contentWindow || !d || d.sl !== 'mermaid') return;
    if (d.ready) { ready = true; queue.splice(0).forEach(send); return; }
    const src = waiting.get(d.id); if (src == null) return; waiting.delete(d.id);
    const r = typeof d.svg === 'string' ? { svg: d.svg, w: +d.w || 600, h: +d.h || 400 } : { error: String(d.error || 'unknown error'), src };
    DONE.set(src, r); if (DONE.size > MAX_DONE) DONE.delete(DONE.keys().next().value);
    fill();
  });
  /** Draw every diagram place in the page that is not drawn yet. */
  function fill() {
    document.querySelectorAll('.pv-mmd:not(.done)').forEach(el => {
      const src = SRC.get(el.dataset.mmd); if (src == null) return; const d = DONE.get(src);
      if (d) { el.classList.add('done'); el.innerHTML = drawn(d); return; }
      if (el._asked) return; el._asked = true;
      if ([...waiting.values()].indexOf(src) >= 0 || queue.indexOf(src) >= 0) return;
      boot(); if (ready) send(src); else queue.push(src);
    });
  }
  media.fill = fill;

  /* ================================================================ the page ============================================================ */
  if (typeof document === 'undefined' || !document.addEventListener) return;   // the node tests load the parsers without a page
  const isTag = (t, ...names) => !!t && typeof t.tagName === 'string' && names.indexOf(t.tagName) >= 0;   // the fake DOM of the tests has no element classes
  let pending = 0;
  const watch = () => {
    if (!document.body || typeof MutationObserver !== 'function') return;
    new MutationObserver(() => { if (!pending) pending = requestAnimationFrame(() => { pending = 0; if (document.querySelector('.pv-mmd:not(.done)')) fill(); }); }).observe(document.body, { childList: true, subtree: true });
  };
  if (document.body) watch(); else document.addEventListener('DOMContentLoaded', watch);
  // a picture's natural size under it; one that cannot be drawn says so instead of a broken icon
  document.addEventListener('load', e => {
    const t = e.target; if (!isTag(t, 'IMG', 'VIDEO') || !t.classList.contains('pv-m')) return;
    const w = t.naturalWidth || t.videoWidth, h = t.naturalHeight || t.videoHeight, pv = t.closest('.pv-side') || t.closest('.pv'), dim = pv && pv.querySelector('.pv-dim');
    if (dim && w) dim.textContent = w + ' × ' + h;
  }, true);
  document.addEventListener('loadedmetadata', e => { const t = e.target; if (isTag(t, 'VIDEO') && t.classList.contains('pv-m')) { const pv = t.closest('.pv'), dim = pv && pv.querySelector('.pv-dim'); if (dim && t.videoWidth) dim.textContent = t.videoWidth + ' × ' + t.videoHeight + ' · ' + Math.round(t.duration || 0) + ' s'; } }, true);
  document.addEventListener('error', e => {
    const t = e.target; if (!t || !t.classList || !t.classList.contains('pv-m')) return;
    const box = t.closest('.pv-thumb') || t.closest('.pv-stage') || t;
    if (box.classList.contains('pv-thumb') || t.classList.contains('pv-inl')) { box.hidden = true; return; }
    box.outerHTML = '<p class="ws-none">The browser could not show this file: its content is not in a format it reads, or the file is not there at this point.</p>';
  }, true);
  document.addEventListener('click', e => { const img = isTag(e.target, 'IMG') && e.target.closest('.pv-img'); if (img) img.classList.toggle('actual'); });
})(window.SL);
