#!/usr/bin/env node
// compare: measure a rebuild against the original from two folders written by capture.mjs, no model needed.
//
//   node compare.mjs ORIGINAL_DIR BUILD_DIR [--json]
//
// Pages are paired by path. For each page it lists claims, which must hold (the page exists; it does not scroll
// sideways at a width where the original does not; it logs no errors and fails no requests the original does not; it has
// the original's landmarks), differences to fix or keep on purpose (text sizes, each section's padding, items per row and
// content column, colours, fonts, text sizes and spacing the original uses most, the order of the page's parts, the
// accessibility basics), what could not be compared (a width or page missing from a capture) and notes (parts of the
// original that move by themselves, which a screenshot holds one frame of). The exit status is 1 when a claim fails or something could not be compared, so a part that was never
// measured never passes; 0 otherwise. Differences alone do not fail: a rebuild keeps some on purpose.
import fs from "node:fs";
import path from "node:path";
import { pathToFileURL } from "node:url";

const num = (v, k) => (v && typeof v[k] === "number" ? v[k] : 0);
const list = (v, k) => (v && Array.isArray(v[k]) ? v[k] : []);

// "#rrggbb" or "#rrggbbaa" as [r, g, b, a], or null.
export function rgba(c) {
  const m = /^#([0-9a-f]{6})([0-9a-f]{2})?$/i.exec(String(c || "").trim());
  if (!m) return null;
  const n = parseInt(m[1], 16);
  return [n >> 16, (n >> 8) & 255, n & 255, m[2] ? parseInt(m[2], 16) : 255];
}

// Two colours a person would not tell apart: each channel within 6 of 255.
export function sameColour(a, b) {
  const x = rgba(a);
  const y = rgba(b);
  if (!x || !y) return String(a).toLowerCase() === String(b).toLowerCase();
  return x.every((v, i) => Math.abs(v - y[i]) <= 6);
}

const family = (f) => String(f || "").split(",")[0].trim().replace(/^["']|["']$/g, "").toLowerCase();

const PARTS = {
  header: "header", nav: "navigation bar", main: "main area", footer: "footer", section: "section", article: "article", aside: "sidebar",
  form: "form", h1: "top heading", h2: "subheading", h3: "subheading", ul: "list", ol: "list", img: "image", video: "video", button: "button",
  input: "form field", select: "form field", textarea: "form field",
};
const ROLES = { banner: "header", navigation: "nav", main: "main", contentinfo: "footer", complementary: "aside", form: "form", search: "form", button: "button", img: "img" };

// The page's parts in document order, from the outline.
export function skeleton(cap) {
  return list(cap, "outline").map((o) => PARTS[ROLES[o.role] || o.tag]).filter(Boolean);
}

// The length of the longest sequence both have in the same order.
function common(a, b) {
  let row = new Array(b.length + 1).fill(0);
  for (const x of a) {
    const next = [0];
    for (let j = 0; j < b.length; j++) next.push(x === b[j] ? row[j] + 1 : Math.max(row[j + 1], next[j]));
    row = next;
  }
  return row[b.length];
}

// How alike the order of two pages' parts is, 0 to 100, or null when either has no outline.
export function structureLikeness(original, built) {
  const a = skeleton(original);
  const b = skeleton(built);
  if (!a.length || !b.length) return null;
  return Math.round((200 * common(a, b)) / (a.length + b.length));
}

// Sections paired by tag and heading first, then by tag in page order (a rebuild writes its own headings).
function pairBlocks(theirs, ours) {
  const key = (b) => [b.tag || "", String(b.heading || "").trim().replace(/\s+/g, " ").toLowerCase()];
  const partner = new Array(theirs.length).fill(-1);
  const taken = new Array(ours.length).fill(false);
  for (const same of [(a, b) => key(a)[0] === key(b)[0] && key(a)[1] === key(b)[1], (a, b) => key(a)[0] === key(b)[0]]) {
    theirs.forEach((a, i) => {
      if (partner[i] >= 0) return;
      const j = ours.findIndex((b, j) => !taken[j] && same(a, b));
      if (j >= 0) {
        taken[j] = true;
        partner[i] = j;
      }
    });
  }
  return partner;
}

// The differences of one width's sections: padding over 4 px, items per row (for grids, not a nav or header's row of
// links), the content column over 8 px, and the original's sections the build has no counterpart for.
export function blockDiffs(theirs, ours) {
  const a = list(theirs, "blocks");
  const b = list(ours, "blocks");
  const partner = pairBlocks(a, b);
  const out = [];
  a.forEach((x, i) => {
    const name = x.heading ? `the "${x.heading}" ${x.tag}` : `the ${x.tag}`;
    if (partner[i] < 0) {
      out.push(`${name} has no counterpart in the build`);
      return;
    }
    const y = b[partner[i]];
    const what = [];
    const pa = list(x, "pad");
    const pb = list(y, "pad");
    if (pa.length === pb.length && pa.some((v, k) => Math.abs(v - pb[k]) > 4)) what.push(`padding ${pb.join(" ")} vs the original's ${pa.join(" ")}`);
    if (!["nav", "header"].includes(x.tag) && num(x, "per_row") !== num(y, "per_row")) what.push(`${num(y, "per_row")} per row vs ${num(x, "per_row")}`);
    if (Math.abs(num(x, "gap") - num(y, "gap")) > 4) what.push(`gap ${num(y, "gap")} px vs ${num(x, "gap")} px`);
    const cx = x.column?.max_w;
    const cy = y.column?.max_w;
    if (cx && cy && Math.abs(cx - cy) > 8) what.push(`content column ${cy} px vs ${cx} px`);
    else if (cx && !cy) what.push(`no content column (the original's is ${cx} px)`);
    if (what.length) out.push(`${name}: ${what.join(", ")}`);
  });
  return out;
}

const failedPath = (f) => {
  try {
    return new URL(f.url).pathname;
  } catch {
    return String(f.url || "");
  }
};

// One page: its claims, differences and what could not be compared.
export function comparePage(label, original, built) {
  const claims = [];
  const diffs = [];
  const unknown = [];
  const notes = [];
  const claim = (title, passed, detail) => claims.push({ title: `${label} ${title}`, passed, detail: passed ? "" : detail });
  if (!built) {
    claim("exists in the build", false, "no capture of this page in the build's folder");
    return { page: label, claims, diffs, unknown, notes };
  }
  if (original.error) unknown.push(`${label}: the original ${original.error}`);
  if (built.error) unknown.push(`${label}: the build ${built.error}`);
  const ours = new Map(list(built, "layouts").map((l) => [l.width, l]));
  for (const theirs of list(original, "layouts")) {
    const w = theirs.width;
    const mine = ours.get(w);
    if (!mine) {
      unknown.push(`${label} at ${w} px was not captured in the build`);
      continue;
    }
    if (num(theirs, "page_w") <= w + 1) claim(`does not scroll sideways at ${w} px`, num(mine, "page_w") <= w + 1, `it is ${num(mine, "page_w")} px wide`);
    const at = [];
    for (const [k, name] of [["h1", "top heading"], ["h2", "subheadings"], ["body", "body text"]]) {
      const x = num(theirs.type, k);
      const y = num(mine.type, k);
      if (x && y && Math.abs(x - y) > 2) at.push(`${name} ${y} px, the original's ${x} px`);
      else if (x && !y) at.push(`no ${name} (the original's is ${x} px)`);
    }
    at.push(...blockDiffs(theirs, mine).slice(0, 6));
    if (at.length) diffs.push(`at ${w} px: ${at.join("; ")}`);
    const moving = list(theirs, "moving");
    if (moving.length && !notes.length) notes.push(`${label}: ${moving.length} part(s) of the original move by themselves (${moving.slice(0, 3).map((m) => m.what).join(", ")}), so a screenshot holds one frame: match how they look and move by eye`);
  }
  if (original.health && built.health) {
    const theirs = new Set(list(original.health, "errors").map((e) => e.text));
    const errs = list(built.health, "errors").map((e) => e.text).filter((t) => t && !theirs.has(t));
    claim("logs no errors the original does not", !errs.length, errs.slice(0, 3).map((t) => `"${t}"`).join("; "));
    const failed = new Set(list(original.health, "failed_requests").map(failedPath));
    const fails = list(built.health, "failed_requests").filter((f) => !failed.has(failedPath(f))).map((f) => `${failedPath(f)} (${f.status || f.error})`);
    claim("loads every file it asks for", !fails.length, [...new Set(fails)].slice(0, 5).join(", "));
  }
  const lm = (c) => Object.keys((c.a11y && c.a11y.landmarks) || {});
  if (built.a11y && lm(original).length) {
    const missing = lm(original).filter((l) => !lm(built).includes(l));
    claim("has the original's landmarks", !missing.length, `missing: ${missing.join(", ")}`);
  }
  if (built.a11y && original.a11y) {
    for (const [k, what] of [["unnamed_controls", "controls without a name"], ["images_without_alt", "pictures without alt text"]]) {
      if (num(built.a11y, k) > num(original.a11y, k)) diffs.push(`${num(built.a11y, k)} ${what} (the original has ${num(original.a11y, k)})`);
    }
    if (original.a11y.lang && !built.a11y.lang) diffs.push(`the page language is not set (the original's is ${original.a11y.lang})`);
  }
  const d0 = original.design || {};
  const d1 = built.design || {};
  const colours = [];
  for (const [k, what] of [["backgrounds", "background"], ["text_colors", "text"]]) {
    for (const c of list(d0, k).slice(0, 6)) if (!list(d1, k).some((o) => sameColour(c, o))) colours.push(`${c} (${what})`);
  }
  if (colours.length) diffs.push(`colours the original uses that the build does not: ${colours.join(", ")}`);
  const f0 = list(d0, "fonts")[0];
  const f1 = list(d1, "fonts")[0];
  if (f0 && f1 && family(f0) !== family(f1)) diffs.push(`main font: ${f0} in the original, ${f1} in the build`);
  for (const [k, what] of [["font_sizes", "text sizes"], ["spacing", "spacing"], ["radii", "corner radii"]]) {
    const missing = list(d0, k).slice(0, 8).filter((v) => !list(d1, k).includes(v));
    if (missing.length) diffs.push(`${what} the original uses that the build does not: ${missing.join(", ")}`);
  }
  const s = structureLikeness(original, built);
  if (s !== null && s < 80) diffs.push(`the order of the page's parts is ${s}% alike: ${skeleton(original).slice(0, 12).join(", ")} in the original`);
  return { page: label, structure: s, claims, diffs, unknown, notes };
}

function readJSON(file) {
  try {
    return JSON.parse(fs.readFileSync(file, "utf8"));
  } catch {
    return null;
  }
}

// Both folders' pages, paired by path; the start pages are always a pair.
export function compareDirs(origDir, buildDir) {
  const index = (dir) => {
    const r = readJSON(path.join(dir, "routes.json"));
    const routes = r && Array.isArray(r.routes) ? r.routes : [{ path: "/", dir: "." }];
    return routes.map((x, i) => ({ path: i === 0 ? "start page" : x.path, cap: readJSON(path.join(dir, x.dir, "capture.json")) }));
  };
  const theirs = index(origDir);
  const ours = new Map(index(buildDir).map((p) => [p.path, p.cap]));
  const pages = [];
  for (const p of theirs) {
    if (!p.cap) {
      pages.push({ page: p.path, claims: [], diffs: [], notes: [], unknown: [`${p.path}: the original's capture.json is missing or unreadable`] });
      continue;
    }
    pages.push(comparePage(p.path, p.cap, ours.get(p.path) || null));
  }
  const claims = pages.flatMap((p) => p.claims);
  const unknown = pages.flatMap((p) => p.unknown);
  return { pages, held: claims.filter((c) => c.passed).length, claims: claims.length, unknown: unknown.length, pass: claims.every((c) => c.passed) && !unknown.length };
}

// The report a person or an agent reads.
export function render(r) {
  const lines = [];
  for (const p of r.pages) {
    lines.push(`## ${p.page}${p.structure != null ? ` (parts in the same order: ${p.structure}%)` : ""}`);
    for (const c of p.claims) lines.push(`${c.passed ? "ok  " : "FAIL"} ${c.title}${c.detail ? `: ${c.detail}` : ""}`);
    for (const d of p.diffs) lines.push(`diff ${d}`);
    for (const u of p.unknown) lines.push(`not compared: ${u}`);
    for (const n of p.notes) lines.push(`note ${n}`);
    lines.push("");
  }
  lines.push(
    r.pass
      ? `${r.held} of ${r.claims} claims hold and every page was compared. Review the differences: fix those the spec says must match, keep the ones that make it your own.`
      : `${r.held} of ${r.claims} claims hold${r.unknown ? `, ${r.unknown} thing(s) not compared` : ""}: not done. Fix the failures, make the missing parts exist and render, capture the build again and compare again.`,
  );
  return lines.join("\n");
}

if (process.argv[1] && pathToFileURL(path.resolve(process.argv[1])).href === import.meta.url) {
  const args = process.argv.slice(2);
  const json = args.includes("--json");
  const pos = args.filter((a) => a !== "--json");
  if (pos.length !== 2 || pos.some((a) => a.startsWith("-"))) {
    console.error("usage: node compare.mjs ORIGINAL_DIR BUILD_DIR [--json]");
    process.exit(2);
  }
  const r = compareDirs(path.resolve(pos[0]), path.resolve(pos[1]));
  console.log(json ? JSON.stringify(r, null, 2) : render(r));
  process.exit(r.pass ? 0 : 1);
}
