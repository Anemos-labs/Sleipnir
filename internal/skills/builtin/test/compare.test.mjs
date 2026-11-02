// The reverse-engineering-ui skill's compare.mjs: what it claims, lists and refuses to pass, from two captures.
import { test } from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { blockDiffs, compareDirs, comparePage, render, sameColour, structureLikeness } from "../data/reverse-engineering-ui/compare.mjs";

const layout = (width, over = {}) => ({
  width,
  page_w: width,
  type: { h1: 48, h2: 32, body: 18 },
  blocks: [
    { tag: "header", heading: "", pad: [16, 32, 16, 32], gap: 0, per_row: 2 },
    { tag: "section", heading: "Plans", pad: [64, 32, 64, 32], gap: 24, per_row: 3, column: { max_w: 960 } },
  ],
  ...over,
});
const capture = (over = {}) => ({
  layouts: [layout(390, { type: { h1: 32, h2: 24, body: 16 } }), layout(1440)],
  design: { backgrounds: ["#0f172a", "#ffffff"], text_colors: ["#111827"], fonts: ["Inter"], font_sizes: ["18px"], spacing: ["24px", "64px"], radii: ["12px"] },
  a11y: { lang: "en", landmarks: { banner: 1, main: 1 }, unnamed_controls: 0, images_without_alt: 0 },
  health: { errors: [], failed_requests: [] },
  outline: [{ tag: "header" }, { tag: "nav" }, { tag: "main" }, { tag: "h1" }, { tag: "section" }, { tag: "footer" }],
  ...over,
});

test("a rebuild that matches passes with no differences", () => {
  const r = comparePage("start page", capture(), capture());
  assert.deepEqual(r.diffs, []);
  assert.deepEqual(r.unknown, []);
  assert.ok(r.claims.length >= 4 && r.claims.every((c) => c.passed), JSON.stringify(r.claims));
  assert.equal(r.structure, 100);
});

test("sideways scrolling, new errors, failed requests and missing landmarks are failed claims", () => {
  const built = capture({
    layouts: [layout(390, { page_w: 512, type: { h1: 32, h2: 24, body: 16 } }), layout(1440)],
    health: { errors: [{ text: "boom" }], failed_requests: [{ url: "http://localhost/logo.png", status: 404 }] },
    a11y: { lang: "en", landmarks: { main: 1 }, unnamed_controls: 0, images_without_alt: 0 },
  });
  const failed = comparePage("start page", capture(), built).claims.filter((c) => !c.passed).map((c) => `${c.title}: ${c.detail}`);
  assert.deepEqual(failed, [
    "start page does not scroll sideways at 390 px: it is 512 px wide",
    'start page logs no errors the original does not: "boom"',
    "start page loads every file it asks for: /logo.png (404)",
    "start page has the original's landmarks: missing: banner",
  ]);
});

test("an error or a failed request the original has too is not held against the build", () => {
  const health = { errors: [{ text: "third-party widget failed" }], failed_requests: [{ url: "https://cdn.example/x.js", error: "net::ERR_BLOCKED" }] };
  const r = comparePage("start page", capture({ health }), capture({ health }));
  assert.ok(r.claims.every((c) => c.passed));
});

test("measured differences name the section, the value and the original's value", () => {
  const theirs = layout(1440);
  const ours = layout(1440, {
    blocks: [
      { tag: "header", heading: "", pad: [16, 32, 16, 32], gap: 0, per_row: 5 },
      { tag: "section", heading: "Pricing", pad: [40, 32, 40, 32], gap: 24, per_row: 2, column: { max_w: 1100 } },
    ],
  });
  // The build wrote its own heading, so the section pairs by tag; a header's row of links is not a grid.
  assert.deepEqual(blockDiffs(theirs, ours), ['the "Plans" section: padding 40 32 40 32 vs the original\'s 64 32 64 32, 2 per row vs 3, content column 1100 px vs 960 px']);
  const missing = layout(1440, { blocks: [theirs.blocks[0]] });
  assert.deepEqual(blockDiffs(theirs, missing), ['the "Plans" section has no counterpart in the build']);
});

test("design values: near colours count as the same, others and the main font are listed", () => {
  assert.ok(sameColour("#0f172a", "#10182b"));
  assert.ok(!sameColour("#0f172a", "#1e293b"));
  const built = capture({ design: { backgrounds: ["#1e293b", "#ffffff"], text_colors: ["#111827"], fonts: ["Roboto"], font_sizes: ["18px"], spacing: ["24px"], radii: ["12px"] } });
  const diffs = comparePage("start page", capture(), built).diffs;
  assert.ok(diffs.includes("colours the original uses that the build does not: #0f172a (background)"), diffs.join("\n"));
  assert.ok(diffs.includes("main font: Inter in the original, Roboto in the build"));
  assert.ok(diffs.includes("spacing the original uses that the build does not: 64px"));
});

test("the order of the page's parts is scored", () => {
  assert.equal(structureLikeness(capture(), capture({ outline: [{ tag: "footer" }, { tag: "main" }] })), 25); // one of 6 + 2 parts in order
  assert.equal(structureLikeness(capture(), capture({ outline: [] })), null);
});

test("what was never measured never passes", () => {
  const r = comparePage("start page", capture(), capture({ layouts: [layout(1440)] }));
  assert.deepEqual(r.unknown, ["start page at 390 px was not captured in the build"]);
  const gone = comparePage("/pricing", capture(), null);
  assert.deepEqual(gone.claims.map((c) => c.passed), [false]);
});

test("folders are paired by page and the report says it is not done until everything holds", () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "compare-"));
  const write = (dir, routes, caps) => {
    fs.mkdirSync(path.join(root, dir), { recursive: true });
    fs.writeFileSync(path.join(root, dir, "routes.json"), JSON.stringify({ routes }));
    for (const [d, c] of Object.entries(caps)) {
      fs.mkdirSync(path.join(root, dir, d), { recursive: true });
      fs.writeFileSync(path.join(root, dir, d, "capture.json"), JSON.stringify(c));
    }
  };
  const routes = [{ path: "/", dir: "." }, { path: "/pricing", dir: "routes/pricing" }];
  write("orig", routes, { ".": capture(), "routes/pricing": capture() });
  write("same", routes, { ".": capture(), "routes/pricing": capture() });
  write("half", [routes[0]], { ".": capture() });
  const ok = compareDirs(path.join(root, "orig"), path.join(root, "same"));
  assert.ok(ok.pass, render(ok));
  assert.match(render(ok), /claims hold and every page was compared/);
  const half = compareDirs(path.join(root, "orig"), path.join(root, "half"));
  assert.ok(!half.pass);
  assert.match(render(half), /FAIL \/pricing exists in the build/);
  assert.match(render(half), /not done/);
  fs.rmSync(root, { recursive: true, force: true });
});
