// Sleipnir research wiki: theme, the pages menu, search, citation pop-ups and the table of contents. No dependencies; the index is
// window.WIKI (assets/search-index.js), so it all works when the wiki is opened from disk.
(function () {
  'use strict';
  var W = window.WIKI || { entries: [], sources: {}, findings: {} };
  var root = document.documentElement;

  function store(k, v) { try { if (v === undefined) return localStorage.getItem(k); localStorage.setItem(k, v); } catch (e) { return null; } return null; }
  function el(tag, cls, text) { var e = document.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; }

  // theme: the saved choice, else the system's
  var saved = store('wiki-theme');
  var light = saved ? saved === 'light' : (window.matchMedia && matchMedia('(prefers-color-scheme: light)').matches);
  root.setAttribute('data-theme', light ? 'light' : 'dark');
  var tb = document.querySelector('[data-theme-toggle]');
  if (tb) tb.addEventListener('click', function () {
    var next = root.getAttribute('data-theme') === 'light' ? 'dark' : 'light';
    root.setAttribute('data-theme', next); store('wiki-theme', next);
  });

  // the pages menu on narrow screens
  var mb = document.querySelector('[data-menu]');
  if (mb) mb.addEventListener('click', function () {
    var open = document.body.classList.toggle('nav-open');
    mb.setAttribute('aria-expanded', open ? 'true' : 'false');
  });
  document.addEventListener('click', function (e) {
    if (document.body.classList.contains('nav-open') && !e.target.closest('.side') && !e.target.closest('[data-menu]')) {
      document.body.classList.remove('nav-open'); if (mb) mb.setAttribute('aria-expanded', 'false');
    }
  });

  // citation pop-ups: a source's title, site and first quote
  var pop = document.querySelector('.pop');
  var hideTimer = 0;
  function showPop(a) {
    var id = a.getAttribute('data-src'), s = W.sources[id];
    if (!s || !pop) return;
    clearTimeout(hideTimer);
    pop.textContent = '';
    pop.appendChild(el('strong', null, s.t));
    pop.appendChild(el('span', 'site', id + ' · ' + s.h + ' · ' + s.k));
    if (s.q) pop.appendChild(el('blockquote', null, s.q));
    pop.hidden = false;
    var r = a.getBoundingClientRect();
    var x = Math.min(window.scrollX + r.left - 12, window.scrollX + document.documentElement.clientWidth - pop.offsetWidth - 12);
    pop.style.left = Math.max(8, x) + 'px';
    var below = window.scrollY + r.bottom + 8;
    pop.style.top = (r.bottom + pop.offsetHeight + 16 > window.innerHeight ? window.scrollY + r.top - pop.offsetHeight - 8 : below) + 'px';
  }
  function hidePop() { hideTimer = setTimeout(function () { if (pop) pop.hidden = true; }, 120); }
  document.querySelectorAll('[data-src]').forEach(function (a) {
    a.addEventListener('mouseenter', function () { showPop(a); });
    a.addEventListener('focus', function () { showPop(a); });
    a.addEventListener('mouseleave', hidePop);
    a.addEventListener('blur', hidePop);
  });
  if (pop) { pop.addEventListener('mouseenter', function () { clearTimeout(hideTimer); }); pop.addEventListener('mouseleave', hidePop); }

  // the table of contents follows the reading position
  var tocLinks = Array.prototype.slice.call(document.querySelectorAll('.toc a[href^="#"]'));
  if (tocLinks.length && 'IntersectionObserver' in window) {
    var byId = {};
    tocLinks.forEach(function (a) { byId[decodeURIComponent(a.getAttribute('href').slice(1))] = a; });
    var obs = new IntersectionObserver(function (es) {
      es.forEach(function (e) {
        if (e.isIntersecting && byId[e.target.id]) {
          tocLinks.forEach(function (a) { a.classList.remove('on'); });
          byId[e.target.id].classList.add('on');
        }
      });
    }, { rootMargin: '-64px 0px -70% 0px' });
    Object.keys(byId).forEach(function (id) { var h = document.getElementById(id); if (h) obs.observe(h); });
  }

  // search: every page, section, source, finding and snippet, ranked by where the words occur
  var finder = null, input, list, results = [], sel = 0;
  function fold(s) { return (s || '').toLowerCase().normalize('NFD').replace(/[̀-ͯ]/g, ''); }
  W.entries.forEach(function (e) { e._t = fold(e.t); e._s = fold(e.s); e._x = fold(e.x); e._k = fold(e.k); });
  function score(e, words) {
    var n = 0;
    for (var i = 0; i < words.length; i++) {
      var w = words[i], s = 0;
      if (e._t.indexOf(w) === 0) s = 12; else if (e._t.indexOf(' ' + w) >= 0) s = 9; else if (e._t.indexOf(w) >= 0) s = 6;
      else if (e._s.indexOf(w) >= 0 || e._k.indexOf(w) >= 0) s = 4; else if (e._x.indexOf(w) >= 0) s = 2;
      if (!s) return 0;
      n += s;
    }
    var k = e.k; if (k.indexOf('Section') === 0) n += 3; else if (k.indexOf('Source') !== 0 && k.indexOf('Finding') !== 0 && k.indexOf('Snippet') !== 0) n += 8; // pages, then their sections, then the ledger
    return n;
  }
  function excerpt(e, words) {
    var src = e.x || e.s || '', f = fold(src), at = -1;
    for (var i = 0; i < words.length && at < 0; i++) at = f.indexOf(words[i]);
    if (at < 0) return src.slice(0, 140);
    var start = Math.max(0, at - 50);
    return (start > 0 ? '…' : '') + src.slice(start, start + 160);
  }
  function mark(node, text, words) {
    var f = fold(text), i = 0;
    while (i < text.length) {
      var best = -1, len = 0;
      words.forEach(function (w) { var j = f.indexOf(w, i); if (j >= 0 && (best < 0 || j < best)) { best = j; len = w.length; } });
      if (best < 0) { node.appendChild(document.createTextNode(text.slice(i))); break; }
      node.appendChild(document.createTextNode(text.slice(i, best)));
      node.appendChild(el('mark', null, text.slice(best, best + len)));
      i = best + len;
    }
  }
  function render() {
    var words = fold(input.value).split(/\s+/).filter(Boolean);
    list.textContent = '';
    results = [];
    if (!words.length) { list.appendChild(el('li', 'empty', 'Search pages, sections, sources and findings.')); return; }
    W.entries.forEach(function (e) { var s = score(e, words); if (s) results.push({ e: e, s: s }); });
    results.sort(function (a, b) { return b.s - a.s || a.e.t.localeCompare(b.e.t); });
    results = results.slice(0, 40);
    if (!results.length) { list.appendChild(el('li', 'empty', 'Nothing matches “' + input.value + '”.')); return; }
    sel = 0;
    results.forEach(function (r, i) {
      var li = el('li'), a = el('a');
      a.href = r.e.u;
      a.appendChild(el('span', 'k', r.e.k));
      var t = el('span', 't'); mark(t, r.e.t, words); a.appendChild(t);
      var x = el('span', 'x'); mark(x, excerpt(r.e, words), words); a.appendChild(x);
      if (i === 0) a.classList.add('sel');
      li.appendChild(a); list.appendChild(li);
    });
  }
  function move(d) {
    var as = list.querySelectorAll('a');
    if (!as.length) return;
    as[sel].classList.remove('sel');
    sel = (sel + d + as.length) % as.length;
    as[sel].classList.add('sel');
    as[sel].scrollIntoView({ block: 'nearest' });
  }
  function openFinder() {
    if (!finder) {
      finder = el('div', 'finder'); finder.setAttribute('role', 'dialog'); finder.setAttribute('aria-label', 'Search the wiki');
      var box = el('div', 'box');
      input = el('input'); input.type = 'search'; input.placeholder = 'Search the wiki'; input.setAttribute('aria-label', 'Search');
      list = el('ol');
      box.appendChild(input); box.appendChild(list);
      box.appendChild(el('div', 'hint', '↑ ↓ to choose · Enter to open · Esc to close'));
      finder.appendChild(box);
      document.body.appendChild(finder);
      input.addEventListener('input', render);
      input.addEventListener('keydown', function (e) {
        if (e.key === 'ArrowDown') { e.preventDefault(); move(1); }
        else if (e.key === 'ArrowUp') { e.preventDefault(); move(-1); }
        else if (e.key === 'Enter') { var a = list.querySelectorAll('a')[sel]; if (a) location.href = a.href; }
        else if (e.key === 'Escape') closeFinder();
      });
      finder.addEventListener('click', function (e) { if (e.target === finder) closeFinder(); });
    }
    finder.hidden = false; input.value = ''; render(); input.focus();
  }
  function closeFinder() { if (finder) finder.hidden = true; }
  var sb = document.querySelector('[data-search]');
  if (sb) sb.addEventListener('click', openFinder);
  document.addEventListener('keydown', function (e) {
    var typing = /^(INPUT|TEXTAREA|SELECT)$/.test((e.target.tagName || '')) || e.target.isContentEditable;
    if (!typing && (e.key === '/' || (e.key === 'k' && (e.metaKey || e.ctrlKey)))) { e.preventDefault(); openFinder(); }
    else if (e.key === 'Escape') { closeFinder(); if (pop) pop.hidden = true; }
  });
})();
