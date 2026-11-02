#!/usr/bin/env node
// capture: record a site the way a visitor sees it, as measurements a rebuild can be checked against, no model needed.
// For each page (the start page and up to --routes pages it links to on the same origin) and each width it writes a
// full-page screenshot and capture.json: the layout measured at each width (page width, heading and body text sizes,
// each section's padding, gap, items per row and content column), the design values the page uses most (colours in
// hex, fonts, text sizes, spacing, radii, shadows, :root custom properties), the outline of landmarks and headings, the
// links, forms, picture sizes, the accessibility basics, what logs errors or fails to load, and the parts that move by
// themselves (videos, canvases, running animations), which no screenshot pins down. Capture the rebuild with the same
// command and compare the two folders with compare.mjs.
//
//   node capture.mjs URL|FILE OUT [--widths 390,768,1440] [--routes 6] [--states] [--dark] [--wait MS] [--har DIR]
//
// --states also opens each menu, pop-up and tab control (aria-expanded, aria-haspopup, role=tab) one at a time at the
// widest and narrowest width and photographs it into OUT/states/ (listed in states.json). --har DIR records each width's
// network traffic as a HAR in DIR, without bodies; HARs can still hold cookies and tokens, so keep DIR outside the
// repository. Playwright is resolved from the current directory first (run it from the project that installed it), then
// globally.
import { createRequire } from "node:module";
import fs from "node:fs/promises";
import path from "node:path";

const args = process.argv.slice(2);
const opt = { widths: [390, 768, 1440], routes: 6, states: false, dark: false, wait: 800, har: "", pos: [] };
for (let i = 0; i < args.length; i++) {
  const a = args[i];
  if (a === "--states") opt.states = true;
  else if (a === "--dark") opt.dark = true;
  else if (a === "--widths") opt.widths = String(args[++i] || "").split(",").map(Number).filter((n) => n > 0);
  else if (a === "--routes") opt.routes = Math.max(0, Number(args[++i]) || 0);
  else if (a === "--wait") opt.wait = Math.max(0, Number(args[++i]) || 0);
  else if (a === "--har") opt.har = String(args[++i] || "");
  else if (a === "-h" || a === "--help") usage(0);
  else if (a.startsWith("-")) usage(2, `unknown flag ${a}`);
  else opt.pos.push(a);
}
if (opt.pos.length !== 2 || !opt.widths.length) usage(2, "give a URL (or HTML file) and an output folder");
const start = /^[a-z]+:\/\//i.test(opt.pos[0]) ? opt.pos[0] : "file://" + path.resolve(opt.pos[0]);
const out = path.resolve(opt.pos[1]);
opt.widths.sort((a, b) => a - b);

function usage(code, msg) {
  if (msg) console.error("capture: " + msg);
  console.error("usage: node capture.mjs URL|FILE OUT [--widths 390,768,1440] [--routes 6] [--states] [--dark] [--wait MS] [--har DIR]");
  process.exit(code);
}

function loadPlaywright() {
  for (const base of [path.join(process.cwd(), "noop.js"), import.meta.url]) {
    try {
      return createRequire(base)("playwright");
    } catch {}
  }
  console.error("capture: Playwright is not installed here; run `npm i -D playwright && npx playwright install chromium` in the project");
  process.exit(2);
}

// The folder name of a page: "/" is "index", "/pricing/teams" is "pricing-teams".
function slug(p) {
  const s = p.replace(/[?#].*$/, "").replace(/^\/+|\/+$/g, "").replace(/\.html?$/i, "").replace(/[^a-z0-9]+/gi, "-").toLowerCase();
  return s || "index";
}

// The path a page is known by: its URL path, or for a file, its path from the start file's folder, so that a capture of
// the original site and one of a rebuild served from files name the same pages alike.
function routeKey(u) {
  if (u.protocol !== "file:") return u.pathname || "/";
  const base = path.posix.dirname(new URL(start).pathname);
  return "/" + path.posix.relative(base, u.pathname);
}

// Runs inside the page: the measurements of one width. `full` adds what does not change with the width (design values,
// outline, links, forms, accessibility), measured once at the widest width.
function measure(full) {
  const vw = document.documentElement.clientWidth;
  const all = [...document.body.querySelectorAll("*")];
  const visible = (el) => {
    const r = el.getBoundingClientRect();
    const s = getComputedStyle(el);
    return r.width > 0 && r.height > 0 && s.visibility !== "hidden" && s.display !== "none";
  };
  const shown = all.filter(visible);
  const px = (v) => Math.round(parseFloat(v) || 0);
  const hex = (c) => {
    const m = /rgba?\(([\d.]+)[ ,]+([\d.]+)[ ,]+([\d.]+)(?:[ ,/]+([\d.]+%?))?\)/.exec(c || "");
    if (!m) return "";
    let a = m[4] === undefined ? 1 : m[4].endsWith("%") ? parseFloat(m[4]) / 100 : parseFloat(m[4]);
    if (a === 0) return "";
    const h = (n) => Math.round(Number(n)).toString(16).padStart(2, "0");
    return "#" + h(m[1]) + h(m[2]) + h(m[3]) + (a < 1 ? h(a * 255) : "");
  };
  const ownText = (el) => [...el.childNodes].some((n) => n.nodeType === 3 && n.textContent.trim());
  const text = (el) => (el.innerText || "").trim().replace(/\s+/g, " ");
  const top = (counts, n) => Object.entries(counts).sort((a, b) => b[1] - a[1] || (a[0] < b[0] ? -1 : 1)).slice(0, n).map(([v]) => v);
  const most = (els) => {
    const c = {};
    for (const el of els) if (visible(el)) c[px(getComputedStyle(el).fontSize)] = (c[px(getComputedStyle(el).fontSize)] || 0) + 1;
    return Number(top(c, 1)[0] || 0);
  };
  const first = (sel) => [...document.querySelectorAll(sel)].find(visible);

  // Sections: landmarks and sections not inside another one (main itself is a container, not a section).
  const blockSel = "header, nav, section, footer, aside, article, form, [role=banner], [role=navigation], [role=contentinfo], [role=region]";
  const blocks = shown.filter((el) => el.matches(blockSel) && !el.parentElement.closest(blockSel) && el.getBoundingClientRect().width >= vw * 0.5);
  const perRow = (el) => {
    for (const c of [el, ...el.querySelectorAll("*")]) {
      const s = getComputedStyle(c);
      if (!/(grid|flex)/.test(s.display)) continue;
      const kids = [...c.children].filter(visible);
      if (kids.length < 2) continue;
      const y = kids[0].getBoundingClientRect().top;
      return kids.filter((k) => Math.abs(k.getBoundingClientRect().top - y) <= 4).length;
    }
    return 1;
  };
  const column = (el) => {
    const b = el.getBoundingClientRect();
    for (const c of el.querySelectorAll("*")) {
      if (!visible(c)) continue;
      const r = c.getBoundingClientRect();
      if (r.width >= b.width - 16 || r.width < b.width * 0.4) continue;
      if (Math.abs(r.left - b.left - (b.right - r.right)) <= 4) return { max_w: Math.round(r.width) };
    }
    return null;
  };
  const block = (el) => {
    const s = getComputedStyle(el);
    const h = el.querySelector("h1, h2, h3");
    const b = { tag: el.matches("header, [role=banner]") ? "header" : el.matches("nav, [role=navigation]") ? "nav" : el.matches("footer, [role=contentinfo]") ? "footer" : el.tagName.toLowerCase(), heading: h ? text(h).slice(0, 80) : "" };
    b.pad = [s.paddingTop, s.paddingRight, s.paddingBottom, s.paddingLeft].map(px);
    b.gap = px(s.rowGap) || px(s.columnGap);
    b.per_row = perRow(el);
    const col = column(el);
    if (col) b.column = col;
    b.h = Math.round(el.getBoundingClientRect().height);
    return b;
  };
  const layout = {
    width: vw,
    page_w: Math.max(document.documentElement.scrollWidth, document.body.scrollWidth),
    page_h: Math.max(document.documentElement.scrollHeight, document.body.scrollHeight),
    type: { h1: first("h1") ? px(getComputedStyle(first("h1")).fontSize) : 0, h2: first("h2") ? px(getComputedStyle(first("h2")).fontSize) : 0, body: most(document.querySelectorAll("p, li")) },
    blocks: blocks.map(block),
  };
  // Parts that change by themselves: a screenshot holds one frame of them.
  const moving = [];
  for (const el of shown) {
    const r = el.getBoundingClientRect();
    if (r.width * r.height < 400) continue;
    const anim = el.getAnimations?.().some((a) => a.playState === "running" && (a.effect?.getTiming?.().iterations === Infinity || a.effect?.getTiming?.().duration > 0));
    if (el.matches("video, canvas, iframe, marquee") || anim) moving.push({ what: el.tagName.toLowerCase() + (el.id ? "#" + el.id : ""), box: [r.left, r.top + scrollY, r.width, r.height].map(Math.round) });
  }
  layout.moving = moving.slice(0, 20);
  if (!full) return { layout };

  const counts = (props) => Object.fromEntries(props.map((p) => [p, {}]));
  const c = counts(["backgrounds", "text_colors", "fonts", "font_sizes", "spacing", "radii", "shadows"]);
  const bump = (k, v, n = 1) => {
    if (v !== "" && v !== undefined) c[k][v] = (c[k][v] || 0) + n;
  };
  for (const el of shown) {
    const s = getComputedStyle(el);
    const r = el.getBoundingClientRect();
    bump("backgrounds", hex(s.backgroundColor), Math.max(1, Math.round((r.width * r.height) / 1e4)));
    if (ownText(el)) {
      bump("text_colors", hex(s.color));
      bump("fonts", s.fontFamily.split(",")[0].trim().replace(/^["']|["']$/g, ""));
      bump("font_sizes", px(s.fontSize) + "px");
    }
    for (const p of [s.paddingTop, s.paddingLeft, s.rowGap, s.columnGap, s.marginBottom]) if (px(p) > 0) bump("spacing", px(p) + "px");
    if (px(s.borderTopLeftRadius) > 0) bump("radii", px(s.borderTopLeftRadius) + "px");
    if (s.boxShadow && s.boxShadow !== "none") bump("shadows", s.boxShadow);
  }
  const design = {
    backgrounds: top(c.backgrounds, 8), text_colors: top(c.text_colors, 8), fonts: top(c.fonts, 4), font_sizes: top(c.font_sizes, 10),
    spacing: top(c.spacing, 12), radii: top(c.radii, 6), shadows: top(c.shadows, 4),
  };
  const root = getComputedStyle(document.documentElement);
  const vars = [...document.styleSheets].flatMap((sh) => {
    try {
      return [...sh.cssRules];
    } catch {
      return [];
    }
  }).filter((r) => r.selectorText === ":root").flatMap((r) => [...r.style]).filter((n) => n.startsWith("--"));
  design.vars = Object.fromEntries([...new Set(vars)].slice(0, 80).map((n) => [n, root.getPropertyValue(n).trim()]));

  // Landmarks, counted by their role: a header or footer inside a section or article is not the page's banner.
  const landmarks = {};
  const roleOf = (el) => {
    const r = el.getAttribute("role");
    if (r) return r;
    const t = el.tagName.toLowerCase();
    const scoped = el.parentElement?.closest("article, aside, main, nav, section");
    if (t === "header") return scoped ? "" : "banner";
    if (t === "footer") return scoped ? "" : "contentinfo";
    return { nav: "navigation", main: "main", aside: "complementary", search: "search" }[t] || (t === "form" && (el.getAttribute("aria-label") || el.getAttribute("aria-labelledby")) ? "form" : "");
  };
  const lm = ["banner", "navigation", "main", "contentinfo", "complementary", "search", "form", "region"];
  for (const el of shown) {
    const r = roleOf(el);
    if (lm.includes(r)) landmarks[r] = (landmarks[r] || 0) + 1;
  }
  const named = (el) => {
    if ((el.getAttribute("aria-label") || "").trim() || el.getAttribute("aria-labelledby") || (el.getAttribute("title") || "").trim()) return true;
    if (el.matches("input, select, textarea")) return !!(el.labels?.length || el.getAttribute("placeholder") || el.type === "hidden" || el.type === "submit" && el.value);
    return !!text(el) || [...el.querySelectorAll("img[alt], svg title")].some((i) => (i.getAttribute("alt") || i.textContent || "").trim());
  };
  const controls = shown.filter((el) => el.matches("a[href], button, input, select, textarea, [role=button], [role=link], [role=tab]"));
  const a11y = {
    lang: document.documentElement.lang || "",
    landmarks,
    unnamed_controls: controls.filter((el) => !named(el)).length,
    images_without_alt: shown.filter((el) => el.matches("img") && !el.hasAttribute("alt")).length,
  };
  const outline = shown
    .filter((el) => el.matches("header, nav, main, footer, aside, section, article, form, h1, h2, h3, button, img, video, ul, ol, input, select, textarea, [role]"))
    .slice(0, 400)
    .map((el) => {
      const r = el.getBoundingClientRect();
      const o = { tag: el.tagName.toLowerCase(), box: [r.left, r.top + scrollY, r.width, r.height].map(Math.round) };
      if (el.getAttribute("role")) o.role = el.getAttribute("role");
      if (/^h[1-3]$/.test(o.tag) || o.tag === "button") o.text = text(el).slice(0, 80);
      return o;
    });
  const links = [...document.querySelectorAll("a[href]")].filter(visible).slice(0, 200).map((a) => ({ text: text(a).slice(0, 60), href: a.href, nav: !!a.closest("nav, header") }));
  const forms = [...document.querySelectorAll("form")].filter(visible).map((f) => ({
    fields: [...f.querySelectorAll("input, select, textarea")].filter((i) => i.type !== "hidden").map((i) => ({ type: i.type || i.tagName.toLowerCase(), name: i.name || "", label: (i.labels?.[0] ? text(i.labels[0]) : i.getAttribute("aria-label") || i.placeholder || "").slice(0, 60), required: i.required })),
  }));
  const pictures = shown.filter((el) => el.matches("img, svg, picture, video")).slice(0, 60).map((el) => {
    const r = el.getBoundingClientRect();
    return { tag: el.tagName.toLowerCase(), w: Math.round(r.width), h: Math.round(r.height) };
  });
  const headings = [...document.querySelectorAll("h1, h2, h3")].filter(visible).map((h) => text(h).slice(0, 120));
  return { layout, title: document.title, design, a11y, outline, links, forms, pictures, headings, text: text(document.body).slice(0, 4000) };
}

// The controls that open something in place: menus, pop-ups, disclosure buttons and tabs.
function openers() {
  const visible = (el) => {
    const r = el.getBoundingClientRect();
    return r.width > 0 && r.height > 0 && getComputedStyle(el).visibility !== "hidden";
  };
  return [...document.querySelectorAll("[aria-expanded=false], [aria-haspopup]:not([aria-haspopup=false]), [role=tab][aria-selected=false], summary")]
    .filter(visible)
    .slice(0, 12)
    .map((el, i) => {
      el.setAttribute("data-capture-opener", String(i));
      const kind = el.matches("[role=tab]") ? "tab" : el.matches("summary") ? "disclosure" : el.getAttribute("aria-haspopup") === "dialog" ? "dialog" : "menu";
      return { i, kind, label: (el.getAttribute("aria-label") || el.innerText || el.getAttribute("title") || "").trim().replace(/\s+/g, " ").slice(0, 40) };
    });
}

async function main() {
  const { chromium } = loadPlaywright();
  await fs.mkdir(out, { recursive: true });
  if (opt.har) await fs.mkdir(opt.har, { recursive: true });
  const browser = await chromium.launch();
  const origin = new URL(start).origin;
  const startPath = routeKey(new URL(start));
  const routes = [{ path: startPath, url: start, slug: "index" }];
  const seen = new Set([startPath]);
  const report = [];

  for (let r = 0; r < routes.length; r++) {
    const route = routes[r];
    const dir = r === 0 ? out : path.join(out, "routes", route.slug);
    await fs.mkdir(dir, { recursive: true });
    const cap = { url: route.url, path: route.path, widths: opt.widths, layouts: [], health: { errors: [], failed_requests: [] } };
    for (const width of [...opt.widths].reverse()) {
      const full = width === opt.widths[opt.widths.length - 1];
      const ctx = await browser.newContext({
        viewport: { width, height: 900 }, colorScheme: opt.dark ? "dark" : "light", reducedMotion: "no-preference",
        ...(opt.har ? { recordHar: { path: path.join(opt.har, `${route.slug}-${width}.har`), content: "omit" } } : {}),
      });
      const page = await ctx.newPage();
      if (full) {
        page.on("console", (m) => m.type() === "error" && cap.health.errors.push({ text: m.text().slice(0, 300) }));
        page.on("pageerror", (e) => cap.health.errors.push({ text: String(e.message || e).slice(0, 300) }));
        page.on("requestfailed", (q) => cap.health.failed_requests.push({ url: q.url(), error: q.failure()?.errorText || "failed" }));
        page.on("response", (s) => s.status() >= 400 && cap.health.failed_requests.push({ url: s.url(), status: s.status() }));
      }
      try {
        await page.goto(route.url, { waitUntil: "load", timeout: 45000 });
        await page.waitForLoadState("networkidle", { timeout: 8000 }).catch(() => {});
      } catch (e) {
        cap.error = `could not load at ${width} px: ${String(e.message || e).split("\n")[0]}`;
        await ctx.close();
        break;
      }
      // Scroll to the end and back so that lazy content and reveal-on-scroll sections are drawn, then settle.
      await page.evaluate(async () => {
        for (let y = 0; y < document.documentElement.scrollHeight && y < 60000; y += innerHeight * 0.8) {
          scrollTo(0, y);
          await new Promise((res) => setTimeout(res, 60));
        }
        scrollTo(0, 0);
      });
      await page.waitForTimeout(opt.wait);
      const m = await page.evaluate(measure, full);
      cap.layouts.unshift(m.layout);
      if (full) {
        const { layout, ...rest } = m;
        Object.assign(cap, rest);
        await fs.writeFile(path.join(dir, "aria.yml"), await page.locator("body").ariaSnapshot().catch(() => ""));
        if (r === 0) {
          // Same-origin pages it links to, from the navigation first: they are the pages a visitor reaches first.
          const found = [];
          for (const l of m.links) {
            let u;
            try {
              u = new URL(l.href);
            } catch {
              continue;
            }
            u.hash = "";
            u.search = "";
            const key = routeKey(u);
            if (u.origin !== origin || seen.has(key) || /\.(pdf|zip|png|jpe?g|gif|svg|webp|mp4|xml|json|txt)$/i.test(u.pathname)) continue;
            seen.add(key);
            found.push({ path: key, url: u.href, slug: slug(key), nav: l.nav });
          }
          found.sort((a, b) => Number(b.nav) - Number(a.nav));
          routes.push(...found.slice(0, opt.routes));
        }
      }
      await page.screenshot({ path: path.join(dir, `screenshot-${width}.png`), fullPage: true });
      if (opt.states && (full || width === opt.widths[0])) cap.states = [...(cap.states || []), ...(await states(page, route, width, dir))];
      await ctx.close();
    }
    await fs.writeFile(path.join(dir, "capture.json"), JSON.stringify(cap, null, 2) + "\n");
    if (cap.states?.length) await fs.writeFile(path.join(dir, "states.json"), JSON.stringify({ states: cap.states }, null, 2) + "\n");
    report.push(`${route.path}: ${cap.error || `${cap.layouts.length} width(s), ${cap.layouts[0]?.blocks.length ?? 0} section(s), ${cap.health.errors.length} error(s), ${cap.health.failed_requests.length} failed request(s)`}`);
  }
  await fs.writeFile(path.join(out, "routes.json"), JSON.stringify({ start, routes: routes.map(({ path: p, slug: s }, i) => ({ path: p, dir: i === 0 ? "." : `routes/${s}` })) }, null, 2) + "\n");
  await browser.close();
  console.log(report.join("\n"));
  console.log(`capture: ${routes.length} page(s) in ${out}${opt.har ? `; HARs in ${opt.har} (keep them out of the repository)` : ""}`);
}

// Open each control one at a time from a fresh load and photograph what it opens.
async function states(page, route, width, dir) {
  const found = [];
  const list = await page.evaluate(openers);
  if (list.length) await fs.mkdir(path.join(dir, "states"), { recursive: true });
  for (const o of list) {
    await page.goto(route.url, { waitUntil: "load" }).catch(() => {});
    await page.waitForTimeout(200);
    const again = await page.evaluate(openers);
    const el = again.find((x) => x.kind === o.kind && x.label === o.label);
    if (!el) continue;
    const before = page.url();
    try {
      await page.locator(`[data-capture-opener="${el.i}"]`).click({ timeout: 3000 });
    } catch {
      continue;
    }
    await page.waitForTimeout(400);
    if (page.url().split("#")[0] !== before.split("#")[0]) continue;
    const file = `states/${width}-${o.kind}-${slug(o.label || String(o.i))}.png`;
    await page.screenshot({ path: path.join(dir, file) });
    found.push({ kind: o.kind, label: o.label, width, file });
  }
  await page.goto(route.url, { waitUntil: "load" }).catch(() => {});
  return found;
}

await main();
