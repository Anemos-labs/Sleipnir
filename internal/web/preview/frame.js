/* frame.js: the driver of the diagram frame (GET /api/preview/mermaid). The frame is a sandboxed document of an opaque origin: it
 * cannot read the page, its cookie or the API. The page posts it {sl: 'mermaid', id, src, theme}; the frame draws the source with
 * mermaid and posts back {sl: 'mermaid', id, svg, w, h} or {sl: 'mermaid', id, error}. The page shows the SVG as an image, where
 * nothing in it runs. */
(function () {
  'use strict';
  var M = globalThis.mermaid, themeNow = '';
  function setup(theme) {
    if (theme === themeNow) return;
    themeNow = theme;
    M.initialize({ startOnLoad: false, securityLevel: 'strict', theme: theme === 'light' ? 'default' : 'dark', htmlLabels: false,
      flowchart: { htmlLabels: false }, fontFamily: 'ui-sans-serif, system-ui, sans-serif' });
  }
  /** the SVG with a width and a height of its own, so that an <img> draws it at its size */
  function sized(svg) {
    var doc = new DOMParser().parseFromString(svg, 'image/svg+xml'), el = doc.documentElement, vb = (el.getAttribute('viewBox') || '').split(/[\s,]+/).map(Number);
    var w = vb.length === 4 && vb[2] > 0 ? vb[2] : 600, h = vb.length === 4 && vb[3] > 0 ? vb[3] : 400;
    el.setAttribute('width', String(Math.ceil(w)));
    el.setAttribute('height', String(Math.ceil(h)));
    el.removeAttribute('style');
    return { svg: new XMLSerializer().serializeToString(el), w: w, h: h };
  }
  var n = 0, chain = Promise.resolve();
  function draw(msg) {
    var id = 'sl-m' + (++n), out = { sl: 'mermaid', id: msg.id };
    return Promise.resolve().then(function () {
      setup(msg.theme === 'light' ? 'light' : 'dark');
      return M.render(id, String(msg.src || ''));
    }).then(function (r) {
      var s = sized(r.svg); out.svg = s.svg; out.w = s.w; out.h = s.h;
    }, function (e) {
      out.error = String(e && e.message || e || 'the diagram could not be drawn').slice(0, 2000);
    }).then(function () {
      var junk = document.getElementById('d' + id); if (junk) junk.remove();
      parent.postMessage(out, '*');
    });
  }
  addEventListener('message', function (ev) {
    var msg = ev.data;
    if (ev.source !== parent || !msg || msg.sl !== 'mermaid' || typeof msg.src !== 'string') return;
    chain = chain.then(function () { return draw(msg); });      // one diagram at a time: mermaid keeps global state while it draws
  });
  parent.postMessage({ sl: 'mermaid', ready: true }, '*');
})();
