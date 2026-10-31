#!/usr/bin/env node
// design-audit: load a page in a headless browser at phone and desktop widths and report, without a model, the defects a
// screenshot hides from a quick look (script errors, broken images, horizontal overflow, text at the screen edge or hidden,
// empty charts, low contrast, tiny text, unlabelled controls, skipped headings) and the tells of a generated interface
// (overused fonts, gradient text, purple gradients, side-stripe cards, nested cards, glows, bounce easing, icon tiles, filler
// copy). Defects make the exit status 1; tells are reported for a person or a reviewer to weigh.
//
//   node audit.mjs URL [URL...] [--widths 390,1280] [--dark] [--json] [--wait MS]
//
// Playwright is resolved from the current directory first (run it from the project that installed it), then globally.
import { createRequire } from "node:module";
import path from "node:path";

const args = process.argv.slice(2);
const opt = { widths: [390, 1280], json: false, dark: false, wait: 600, urls: [] };
for (let i = 0; i < args.length; i++) {
  const a = args[i];
  if (a === "--json") opt.json = true;
  else if (a === "--dark") opt.dark = true;
  else if (a === "--widths") opt.widths = String(args[++i] || "").split(",").map(Number).filter((n) => n > 0);
  else if (a === "--wait") opt.wait = Number(args[++i]) || 0;
  else if (a === "-h" || a === "--help") usage(0);
  else if (a.startsWith("-")) usage(2, `unknown flag ${a}`);
  else opt.urls.push(/^[a-z]+:\/\//i.test(a) ? a : "file://" + path.resolve(a));
}
if (!opt.urls.length || !opt.widths.length) usage(2, "give at least one URL or HTML file");

function usage(code, msg) {
  if (msg) console.error("design-audit: " + msg);
  console.error("usage: node audit.mjs URL|FILE [...] [--widths 390,1280] [--dark] [--json] [--wait MS]");
  process.exit(code);
}

function loadPlaywright() {
  for (const base of [path.join(process.cwd(), "noop.js"), import.meta.url]) {
    try {
      return createRequire(base)("playwright");
    } catch {}
  }
  console.error("design-audit: Playwright is not installed here; run `npm i -D playwright && npx playwright install chromium` in the project");
  process.exit(2);
}

// The checks run inside the page. Each finding is [rule, element, detail]; the page side returns them grouped by rule.
function inPage() {
  const out = {};
  const add = (rule, el, detail) => {
    const r = (out[rule] ||= { count: 0, samples: [] });
    r.count++;
    if (r.samples.length < 3) r.samples.push({ where: el ? describe(el) : "", detail: detail || "" });
  };
  const describe = (el) => {
    const id = el.id ? "#" + el.id : "";
    const cls = typeof el.className === "string" && el.className.trim() ? "." + el.className.trim().split(/\s+/).slice(0, 2).join(".") : "";
    const text = (el.innerText || el.getAttribute?.("aria-label") || "").trim().replace(/\s+/g, " ").slice(0, 40);
    return el.tagName.toLowerCase() + id + cls + (text ? ` "${text}"` : "");
  };
  const vw = document.documentElement.clientWidth;
  const all = [...document.body.querySelectorAll("*")];
  const cs = new Map(all.map((el) => [el, getComputedStyle(el)]));
  const style = (el) => cs.get(el) || getComputedStyle(el);
  const visible = (el) => {
    const r = el.getBoundingClientRect();
    const s = style(el);
    return r.width > 0 && r.height > 0 && s.visibility !== "hidden" && s.display !== "none" && (!el.checkVisibility || el.checkVisibility());
  };
  const ownText = (el) => [...el.childNodes].filter((n) => n.nodeType === 3).map((n) => n.textContent).join("").trim();
  // The box of el's own text, as painted: not the element's box, which may run to the screen's edge with the text well inside it.
  const textBox = (el) => {
    let box = null;
    for (const n of el.childNodes) {
      if (n.nodeType !== 3 || !n.textContent.trim()) continue;
      const range = document.createRange();
      range.selectNodeContents(n);
      for (const q of range.getClientRects()) {
        if (!q.width) continue;
        box = box ? { left: Math.min(box.left, q.left), right: Math.max(box.right, q.right) } : { left: q.left, right: q.right };
      }
    }
    return box;
  };
  const parseColor = (c) => {
    const m = c && c.match(/rgba?\(([^)]+)\)/);
    if (!m) return null;
    const p = m[1].split(/[ ,/]+/).filter(Boolean).map(parseFloat);
    return { r: p[0], g: p[1], b: p[2], a: p.length > 3 ? p[3] : 1 };
  };
  const lum = ({ r, g, b }) => {
    const f = (v) => ((v /= 255) <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4);
    return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b);
  };
  const hue = ({ r, g, b }) => {
    const mx = Math.max(r, g, b), mn = Math.min(r, g, b), d = mx - mn;
    if (!d) return { h: 0, s: 0 };
    let h = mx === r ? ((g - b) / d) % 6 : mx === g ? (b - r) / d + 2 : (r - g) / d + 4;
    return { h: (h * 60 + 360) % 360, s: d / mx };
  };
  // The colour behind el: the first opaque background up the tree; null when an image or gradient is in the way.
  const backdrop = (el) => {
    for (let e = el; e; e = e.parentElement) {
      const s = style(e);
      if (s.backgroundImage && s.backgroundImage !== "none") return null;
      const c = parseColor(s.backgroundColor);
      if (c && c.a > 0.9) return c;
    }
    return parseColor(getComputedStyle(document.documentElement).backgroundColor) || { r: 255, g: 255, b: 255, a: 1 };
  };
  const opacity = (el) => {
    let o = 1;
    for (let e = el; e; e = e.parentElement) o *= parseFloat(style(e).opacity);
    return o;
  };

  // Defects.
  if (document.documentElement.scrollWidth > vw + 1) add("horizontal-overflow", null, `page is ${document.documentElement.scrollWidth}px wide in a ${vw}px viewport`);
  for (const img of document.images) {
    const src = img.getAttribute("src");
    if (!src || (img.complete && img.naturalWidth === 0)) add("broken-image", img, src || "no src");
  }
  let textChars = 0, hiddenChars = 0, lastLevel = 0;
  for (const el of all) {
    const s = style(el);
    const tag = el.tagName;
    if (/^H[1-6]$/.test(tag) && visible(el)) {
      const level = +tag[1];
      if (lastLevel && level > lastLevel + 1) add("skipped-heading", el, `h${lastLevel} then h${level}`);
      lastLevel = level;
    }
    if (/^(INPUT|SELECT|TEXTAREA|BUTTON)$/.test(tag) && visible(el) && !["hidden", "submit", "reset", "image"].includes(el.type)) {
      const named = el.getAttribute("aria-label") || el.getAttribute("aria-labelledby") || el.title || (el.labels && el.labels.length) ||
        (tag === "BUTTON" && el.innerText.trim()) || (tag === "INPUT" && el.placeholder && el.type !== "checkbox" && el.type !== "radio");
      if (!named) add("unlabelled-control", el, tag.toLowerCase());
    }
    if ((tag === "CANVAS" || tag === "svg") && visible(el)) {
      const r = el.getBoundingClientRect();
      if (r.width >= 160 && r.height >= 120) {
        let empty = false;
        if (tag === "CANVAS") {
          try {
            const d = el.getContext("2d")?.getImageData(0, 0, el.width, el.height).data;
            if (d) {
              empty = true;
              for (let i = 0; i < d.length; i += 4 * 7) if (d[i] !== d[0] || d[i + 1] !== d[1] || d[i + 2] !== d[2] || d[i + 3] !== d[3]) { empty = false; break; }
            }
          } catch {}
        } else {
          // Data marks: shapes with an area that are not the plot's own background. Axes, ticks and labels alone are an empty chart.
          const marks = [...el.querySelectorAll("rect,path,circle,ellipse,polygon,polyline,image,use,foreignObject")].filter((m) => {
            const b = m.getBoundingClientRect();
            return b.width >= 2 && b.height >= 2 && !(b.width >= r.width * 0.95 && b.height >= r.height * 0.95 && m.tagName === "rect");
          });
          empty = marks.length === 0;
        }
        if (empty) add("empty-graphic", el, `${Math.round(r.width)}×${Math.round(r.height)} ${tag.toLowerCase()} draws nothing`);
      }
    }
    const text = ownText(el);
    if (!text || tag === "SCRIPT" || tag === "STYLE" || tag === "NOSCRIPT" || tag === "OPTION") continue;
    const r = el.getBoundingClientRect();
    if ((r.width === 0 && r.height === 0) || (el.checkVisibility && !el.checkVisibility({ visibilityProperty: false }))) continue; // not rendered, as in a closed <details>
    const hidden = s.visibility === "hidden" || opacity(el) < 0.05;
    textChars += text.length;
    if (hidden && s.display !== "none") hiddenChars += text.length;
    if (hidden) continue;
    const fs = parseFloat(s.fontSize);
    if (fs < 12 && text.length > 2 && !/^(SUP|SUB)$/.test(tag)) add("tiny-text", el, `${fs}px`);
    const edge = vw <= 480 ? 12 : 16;
    const tr = textBox(el);
    if (text.length > 3 && tr && (tr.left < edge - 0.5 || vw - tr.right < edge - 0.5) && tr.right > 0 && tr.left < vw) add("text-at-screen-edge", el, `${Math.round(tr.left)}px from the left, ${Math.round(vw - tr.right)}px from the right`);
    if (s.overflow.includes("hidden") && s.textOverflow !== "ellipsis" && el.scrollWidth > el.clientWidth + 2 && el.clientWidth > 0) add("clipped-text", el, `${el.scrollWidth}px of text in ${el.clientWidth}px`);
    const fg = parseColor(s.color), bg = backdrop(el);
    if (fg && bg && fg.a > 0.5) {
      const a = lum(fg) + 0.05, b = lum(bg) + 0.05, ratio = Math.max(a, b) / Math.min(a, b);
      const large = fs >= 24 || (fs >= 18.66 && parseInt(s.fontWeight) >= 700);
      if (ratio < (large ? 3 : 4.5)) add("low-contrast", el, `${ratio.toFixed(2)}:1`);
    }
    if (/^(P|LI)$/.test(tag) && r.height > fs * 2.5) {
      const lh = parseFloat(s.lineHeight);
      if (lh && lh / fs < 1.3) add("tight-leading", el, `line height ${(lh / fs).toFixed(2)}`);
      if (r.width / (fs * 0.5) > 95) add("long-lines", el, `about ${Math.round(r.width / (fs * 0.5))} characters a line`);
    }
  }
  if (textChars > 200 && hiddenChars / textChars > 0.2) add("content-hidden-at-rest", null, `${Math.round((100 * hiddenChars) / textChars)}% of the text is invisible after load`);

  // Tells of a generated interface.
  const overused = /^(inter|roboto|geist|fraunces|plus jakarta sans|space grotesk|poppins|montserrat|open sans)$/i;
  for (const el of [document.body, document.querySelector("h1")].filter(Boolean)) {
    const first = style(el).fontFamily.split(",")[0].replace(/["']/g, "").trim();
    if (overused.test(first)) add("overused-font", el, first);
  }
  const cardish = (el) => {
    const s = style(el);
    const bordered = ["Top", "Right", "Bottom", "Left"].every((d) => parseFloat(s["border" + d + "Width"]) >= 1);
    return parseFloat(s.borderRadius) >= 4 && (bordered || (s.boxShadow !== "none" && s.boxShadow)) && el.getBoundingClientRect().width >= 120;
  };
  let iconTiles = 0;
  for (const el of all) {
    const s = style(el);
    if (!visible(el)) continue;
    if (s.backgroundClip === "text" || s.webkitBackgroundClip === "text") {
      if (/gradient/.test(s.backgroundImage)) add("gradient-text", el, "");
    }
    if (/gradient\(/.test(s.backgroundImage)) {
      const cols = (s.backgroundImage.match(/rgba?\([^)]+\)/g) || []).map(parseColor).filter((c) => c && c.a > 0.3).map(hue);
      if (cols.length >= 2 && cols.some((c) => c.s > 0.35 && c.h >= 250 && c.h <= 300)) add("purple-gradient", el, "");
    }
    const widths = ["Top", "Right", "Bottom", "Left"].map((d) => parseFloat(s["border" + d + "Width"]));
    const sideColor = parseColor(s.borderLeftColor);
    if ((widths[3] >= 3 && widths[1] <= 1 || widths[1] >= 3 && widths[3] <= 1) && widths[0] <= 1 && widths[2] <= 1 && el.getBoundingClientRect().height >= 40 && sideColor && hue(sideColor).s > 0.3) add("side-stripe", el, "");
    if (cardish(el)) {
      for (let p = el.parentElement; p && p !== document.body; p = p.parentElement) if (cardish(p)) { add("nested-cards", el, ""); break; }
    }
    if (s.boxShadow && s.boxShadow !== "none") {
      for (const sh of s.boxShadow.split(/,(?![^(]*\))/)) {
        const c = parseColor(sh), nums = (sh.replace(/rgba?\([^)]+\)/, "").match(/-?[\d.]+px/g) || []).map(parseFloat);
        if (c && c.a > 0.25 && hue(c).s > 0.5 && nums.length >= 3 && !nums[0] && !nums[1] && nums[2] >= 12) { add("glow", el, ""); break; }
      }
    }
    const easing = s.transitionTimingFunction + " " + s.animationTimingFunction + " " + s.animationName;
    const bez = [...easing.matchAll(/cubic-bezier\(([^)]+)\)/g)].map((m) => m[1].split(",").map(parseFloat));
    if (/bounce|elastic|wobble/i.test(s.animationName) || bez.some((p) => p[1] > 1.15 || p[3] > 1.15 || p[1] < -0.15 || p[3] < -0.15)) add("bounce-easing", el, easing.trim());
    const r = el.getBoundingClientRect();
    const next = el.nextElementSibling;
    if (r.width >= 28 && r.width <= 72 && Math.abs(r.width - r.height) < 4 && parseFloat(s.borderRadius) >= 6 && parseColor(s.backgroundColor)?.a > 0.1 && next && /^H[2-4]$/.test(next.tagName)) iconTiles++;
  }
  if (iconTiles >= 3) add("icon-tile-stack", null, `${iconTiles} rounded icon tiles each above a heading`);
  const body = document.body.innerText || "";
  const dashes = (body.match(/—/g) || []).length;
  if (dashes >= 8 && dashes / Math.max(body.length, 1) > 1 / 600) add("em-dash-overuse", null, `${dashes} em dashes`);
  const buzz = body.match(/\b(supercharge|seamless(ly)?|revolutioni[sz]e|cutting[- ]edge|next[- ]gen(eration)?|world[- ]class|empower|unleash|game[- ]chang(er|ing)|effortless(ly)?|harness the power|elevate your|blazing[- ]fast)\b/gi) || [];
  if (buzz.length) add("filler-copy", null, [...new Set(buzz.map((b) => b.toLowerCase()))].join(", "));
  return out;
}

const DEFECTS = new Set(["script-error", "load-failed", "horizontal-overflow", "broken-image", "empty-graphic", "content-hidden-at-rest", "text-at-screen-edge", "clipped-text", "low-contrast", "tiny-text", "unlabelled-control", "skipped-heading"]);

const { chromium } = loadPlaywright();
const browser = await chromium.launch();
const report = [];
try {
  for (const url of opt.urls) {
    for (const width of opt.widths) {
      const ctx = await browser.newContext({ viewport: { width, height: width < 600 ? 844 : 900 }, colorScheme: opt.dark ? "dark" : "light" });
      const page = await ctx.newPage();
      const errors = [];
      page.on("pageerror", (e) => errors.push(String(e.message || e).split("\n")[0]));
      page.on("console", (m) => m.type() === "error" && errors.push(m.text().split("\n")[0]));
      let findings = {};
      try {
        await page.goto(url, { waitUntil: "networkidle", timeout: 30000 });
        // Scroll through once so reveal-on-scroll content has its chance, then come back to the top.
        const scrolledThrough = await page.evaluate(async () => {
          // At most 200 steps (100 screens): a page that makes itself endlessly tall must not hold the audit.
          let y = 0;
          for (let n = 0; y < document.body.scrollHeight && n < 200; y += innerHeight / 2, n++) { scrollTo(0, y); await new Promise((r) => setTimeout(r, 60)); }
          const done = y >= document.body.scrollHeight;
          scrollTo(0, 0);
          return done;
        });
        await page.waitForTimeout(opt.wait);
        findings = await page.evaluate(inPage);
        if (!scrolledThrough) {
          // what is revealed only further down was never scrolled to, so the audit of this page is partial
          findings["scroll-incomplete"] = { count: 1, samples: [{ where: "", detail: "stopped scrolling after 100 screens; content revealed further down was not audited" }] };
        }
      } catch (e) {
        findings["load-failed"] = { count: 1, samples: [{ where: "", detail: String(e.message || e).split("\n")[0] }] };
      }
      if (errors.length) findings["script-error"] = { count: errors.length, samples: errors.slice(0, 3).map((d) => ({ where: "", detail: d })) };
      report.push({ url, width, findings });
      await ctx.close();
    }
  }
} finally {
  await browser.close();
}

// plain drops terminal controls from text the audited page chose (escape sequences, other C0/C1 controls), keeping tabs, and makes
// its line breaks blanks: what the page chose stays on its own line of the report and cannot add lines that read as findings.
const plain = (t) => String(t).replace(/[\n\u2028\u2029]/g, " ").replace(/[\u0000-\u0008\u000b-\u001f\u007f-\u009f]/g, "");

let defects = 0;
for (const r of report) for (const k of Object.keys(r.findings)) if (DEFECTS.has(k)) defects += r.findings[k].count;
if (opt.json) {
  console.log(JSON.stringify({ defects, report, defectRules: [...DEFECTS] }, null, 2));
} else {
  for (const r of report) {
    const keys = Object.keys(r.findings).sort((a, b) => DEFECTS.has(b) - DEFECTS.has(a) || a.localeCompare(b));
    console.log(`\n${plain(r.url)} at ${r.width}px${opt.dark ? " (dark)" : ""}: ${keys.length ? "" : "nothing found"}`);
    for (const k of keys) {
      const f = r.findings[k];
      console.log(`  ${DEFECTS.has(k) ? "DEFECT" : "tell  "} ${k} ×${f.count}`);
      for (const s of f.samples) console.log(`         ${plain([s.where, s.detail].filter(Boolean).join(": "))}`);
    }
  }
  console.log(`\n${defects} defect${defects === 1 ? "" : "s"}; tells are for judgement, defects are to fix.`);
}
process.exit(defects ? 1 : 0);
