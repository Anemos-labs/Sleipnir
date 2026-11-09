#!/usr/bin/env node
// Keyless vulnerability lookups: OSV.dev (open-source packages in every major ecosystem, with GitHub's advisories),
// the NVD (NIST's CVE records with CVSS, CWE and affected CPEs), CISA's Known Exploited Vulnerabilities catalog and
// FIRST's EPSS scores. Needs Node 18 or newer and nothing else; NVD_API_KEY, when set, raises the NVD's rate limit.
//
//   node vulns.mjs deps [path...] [--json] [--all]       scan lockfiles (default: the current directory)
//   node vulns.mjs pkg <ecosystem> <name> [version] [--json]
//   node vulns.mjs id <CVE-...|GHSA-...|PYSEC-...|GO-...|RUSTSEC-...>... [--json]
//   node vulns.mjs nvd (--cpe CPE23 | --keyword WORDS) [--kev] [--since YYYY-MM-DD] [--limit N] [--json]
//   node vulns.mjs kev [--vendor NAME] [--product NAME] [--since YYYY-MM-DD] [--limit N] [--json]
//
// Exit status: 0 when nothing vulnerable was found, 3 when `deps` or `pkg` found a known vulnerability, 1 on a usage
// error, 2 when a lookup failed (network, timeout, rate limit after retries, or an unexpected status) or `deps` could
// not read a lockfile it found.
import fs from "node:fs";
import path from "node:path";
import { pathToFileURL } from "node:url";

export const OSV = "https://api.osv.dev/v1";
export const NVD = "https://services.nvd.nist.gov/rest/json/cves/2.0";
export const KEV = "https://www.cisa.gov/sites/default/files/feeds/known_exploited_vulnerabilities.json";
export const EPSS = "https://api.first.org/data/v1/epss";

const USAGE = `usage:
  vulns.mjs deps [path...] [--all] [--json]
  vulns.mjs pkg <ecosystem> <name> [version] [--json]
  vulns.mjs id <CVE-...|GHSA-...|...>... [--json]
  vulns.mjs nvd (--cpe CPE23 | --keyword WORDS) [--kev] [--since YYYY-MM-DD] [--limit N] [--json]
  vulns.mjs kev [--vendor NAME] [--product NAME] [--since YYYY-MM-DD] [--limit N] [--json]
ecosystems: npm PyPI Go crates.io RubyGems Packagist Maven NuGet Pub Hex (OSV names)`;

// UsageError is a mistake in the command line: exit status 1 and the usage text.
export class UsageError extends Error {}

const FLAGS = new Set(["--json", "--kev", "--all"]);
const OPTIONS = new Set(["--cpe", "--keyword", "--since", "--limit", "--vendor", "--product"]);

// parseArgs splits argv into a command, positional words, boolean flags and valued options.
export function parseArgs(argv) {
  const [cmd, ...rest] = argv;
  const out = { cmd, words: [], flags: new Set(), opts: {} };
  for (let i = 0; i < rest.length; i++) {
    const a = rest[i];
    if (FLAGS.has(a)) out.flags.add(a.slice(2));
    else if (OPTIONS.has(a)) {
      const v = rest[++i];
      if (v === undefined || v.startsWith("--")) throw new UsageError(`${a} needs a value`);
      out.opts[a.slice(2)] = v;
    } else if (a.startsWith("--")) throw new UsageError(`unknown option ${a}`);
    else out.words.push(a);
  }
  return out;
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// request sends one GET (or a POST with a JSON body) and returns { status, body }. A 404 is an answer. A 429, 403 from
// the NVD's rate limiter, 5xx, timeout or network error is retried with backoff; after the last try it throws.
export async function request(url, { body, headers = {}, fetch = globalThis.fetch, tries = 3, timeoutMs = 30000, wait = sleep } = {}) {
  let last;
  for (let t = 0; t < tries; t++) {
    if (t > 0) await wait(2000 * 2 ** (t - 1));
    try {
      const res = await fetch(url, {
        method: body ? "POST" : "GET",
        headers: { accept: "application/json", ...(body ? { "content-type": "application/json" } : {}), ...headers },
        body: body ? JSON.stringify(body) : undefined,
        signal: AbortSignal.timeout(timeoutMs),
      });
      if (res.status === 429 || res.status >= 500 || (res.status === 403 && url.startsWith(NVD))) {
        last = new Error(`${url.split("?")[0]}: HTTP ${res.status}`);
        continue;
      }
      let data = null;
      try {
        data = await res.json();
      } catch {
        data = null;
      }
      if (res.status === 200 || res.status === 404) return { status: res.status, body: data };
      throw Object.assign(new Error(`${url.split("?")[0]}: HTTP ${res.status}`), { final: true });
    } catch (e) {
      if (e.final) throw e;
      last = e.name === "TimeoutError" ? new Error(`${url.split("?")[0]}: no answer in ${timeoutMs / 1000}s`) : e;
    }
  }
  throw last;
}

// mapLimit runs fn over items with at most n in flight, keeping the input order.
export async function mapLimit(items, n, fn) {
  const out = new Array(items.length);
  let next = 0;
  const worker = async () => {
    while (next < items.length) {
      const i = next++;
      out[i] = await fn(items[i], i);
    }
  };
  await Promise.all(Array.from({ length: Math.min(n, items.length) }, worker));
  return out;
}

// ---- lockfiles ---------------------------------------------------------------------------------------------------

// LOCKFILES maps a file name to its parser. Each parser returns [{ ecosystem, name, version }] from the file's text.
export const LOCKFILES = {
  "package-lock.json": parseNpmLock,
  "npm-shrinkwrap.json": parseNpmLock,
  "yarn.lock": parseYarnLock,
  "pnpm-lock.yaml": parsePnpmLock,
  "requirements.txt": parseRequirements,
  "poetry.lock": (t) => parseTomlPackages(t, "PyPI"),
  "uv.lock": (t) => parseTomlPackages(t, "PyPI"),
  "Pipfile.lock": parsePipfileLock,
  "Cargo.lock": (t) => parseTomlPackages(t, "crates.io"),
  "go.mod": parseGoMod,
  "Gemfile.lock": parseGemfileLock,
  "composer.lock": parseComposerLock,
};

// lockfileParser returns the parser for a file name (requirements-dev.txt and the like count as requirements.txt).
export function lockfileParser(file) {
  const base = path.basename(file);
  if (LOCKFILES[base]) return LOCKFILES[base];
  if (/^requirements.*\.txt$/.test(base)) return parseRequirements;
  return null;
}

function parseNpmLock(text) {
  const j = JSON.parse(text);
  const out = [];
  if (j.packages) {
    for (const [p, v] of Object.entries(j.packages)) {
      if (!p || !v.version || v.link) continue; // "" is the project itself, a link a workspace member
      const name = v.name || p.slice(p.lastIndexOf("node_modules/") + "node_modules/".length);
      out.push({ ecosystem: "npm", name, version: v.version });
    }
  } else {
    const walk = (deps) => {
      for (const [name, v] of Object.entries(deps || {})) {
        if (v.version && !v.version.startsWith("file:")) out.push({ ecosystem: "npm", name, version: v.version });
        walk(v.dependencies);
      }
    };
    walk(j.dependencies);
  }
  return out;
}

function parseYarnLock(text) {
  const out = [];
  let names = null;
  for (const line of text.split(/\r?\n/)) {
    if (/^\S/.test(line) && line.endsWith(":") && !line.startsWith("#") && !line.startsWith("__metadata")) {
      names = line
        .slice(0, -1)
        .split(",")
        .map((s) => s.trim().replace(/^"|"$/g, ""))
        .map((s) => s.slice(0, s.lastIndexOf("@") > 0 ? s.lastIndexOf("@") : s.length));
      continue;
    }
    const m = names && line.match(/^\s+version:?\s+"?([^"\s]+)"?/);
    if (m) {
      out.push({ ecosystem: "npm", name: names[0], version: m[1] });
      names = null;
    }
  }
  return out;
}

function parsePnpmLock(text) {
  const out = [];
  let inPackages = false;
  for (const line of text.split(/\r?\n/)) {
    if (/^\S/.test(line)) inPackages = /^(packages|snapshots):/.test(line);
    const m = inPackages && line.match(/^ {2}'?\/?((?:@[^/@]+\/)?[^/@(]+)[@/]([^(':@\s]+)/);
    if (m) out.push({ ecosystem: "npm", name: m[1], version: m[2] });
  }
  return out;
}

function parseRequirements(text) {
  const out = [];
  for (const raw of text.split(/\r?\n/)) {
    const line = raw.replace(/#.*/, "").trim();
    const m = line.match(/^([A-Za-z0-9][A-Za-z0-9._-]*)(?:\[[^\]]*\])?\s*===?\s*([^\s;,]+)/);
    if (m) out.push({ ecosystem: "PyPI", name: m[1], version: m[2] });
  }
  return out;
}

function parseTomlPackages(text, ecosystem) {
  const out = [];
  for (const block of text.split(/^\[\[package\]\]\s*$/m).slice(1)) {
    const name = block.match(/^name\s*=\s*"([^"]+)"/m);
    const version = block.match(/^version\s*=\s*"([^"]+)"/m);
    const source = block.match(/^source\s*=\s*(.+)$/m);
    // a Cargo or uv workspace member has no registry source: it is the project itself, not a dependency
    if (name && version && (ecosystem !== "crates.io" || source)) out.push({ ecosystem, name: name[1], version: version[1] });
  }
  return out;
}

function parsePipfileLock(text) {
  const j = JSON.parse(text);
  const out = [];
  for (const group of ["default", "develop"]) {
    for (const [name, v] of Object.entries(j[group] || {})) {
      if (typeof v.version === "string") out.push({ ecosystem: "PyPI", name, version: v.version.replace(/^==/, "") });
    }
  }
  return out;
}

function parseGoMod(text) {
  const out = [];
  let block = false;
  for (const raw of text.split(/\r?\n/)) {
    const line = raw.replace(/\/\/.*/, "").trim();
    if (/^require\s*\($/.test(line)) block = true;
    else if (block && line === ")") block = false;
    const m = block ? line.match(/^(\S+)\s+(v\S+)$/) : line.match(/^require\s+(\S+)\s+(v\S+)$/);
    if (m) out.push({ ecosystem: "Go", name: m[1], version: m[2] });
  }
  return out;
}

function parseGemfileLock(text) {
  const out = [];
  let specs = false;
  for (const line of text.split(/\r?\n/)) {
    if (/^ {2}specs:$/.test(line)) specs = true;
    else if (/^\S/.test(line)) specs = false;
    const m = specs && line.match(/^ {4}([^\s(]+) \(([^)\s]+)\)$/);
    if (m) out.push({ ecosystem: "RubyGems", name: m[1], version: m[2].replace(/-[a-z].*$/, "") });
  }
  return out;
}

function parseComposerLock(text) {
  const j = JSON.parse(text);
  return [...(j.packages || []), ...(j["packages-dev"] || [])]
    .filter((p) => p.name && p.version && !p.version.startsWith("dev-"))
    .map((p) => ({ ecosystem: "Packagist", name: p.name, version: p.version.replace(/^v/, "") }));
}

const SKIP_DIRS = new Set(["node_modules", ".git", "vendor", "target", ".venv", "venv", "dist", "build", ".next", "__pycache__"]);

// findLockfiles lists the lockfiles under dir, three levels deep at most, outside dependency and build folders.
export function findLockfiles(dir, depth = 3) {
  const out = [];
  let entries;
  try {
    entries = fs.readdirSync(dir, { withFileTypes: true });
  } catch {
    return out;
  }
  for (const e of entries.sort((a, b) => a.name.localeCompare(b.name))) {
    const p = path.join(dir, e.name);
    if (e.isFile() && lockfileParser(e.name)) out.push(p);
    else if (e.isDirectory() && depth > 0 && !SKIP_DIRS.has(e.name) && !e.name.startsWith(".")) out.push(...findLockfiles(p, depth - 1));
  }
  return out;
}

// ---- enrichment and ranking ---------------------------------------------------------------------------------------

const SEVERITY_RANK = { CRITICAL: 4, HIGH: 3, MODERATE: 2, MEDIUM: 2, LOW: 1 };

// summarize reduces an OSV record to what a report needs, for the package it was found in.
export function summarize(v, pkg) {
  const cves = [v.id, ...(v.aliases || [])].filter((a) => /^CVE-\d{4}-\d+$/.test(a));
  const fixed = [];
  for (const a of v.affected || []) {
    if (pkg && (a.package?.name !== pkg.name || a.package?.ecosystem !== pkg.ecosystem)) continue;
    for (const r of a.ranges || []) for (const e of r.events || []) if (e.fixed) fixed.push(e.fixed);
  }
  const sev = (v.database_specific?.severity || v.affected?.[0]?.ecosystem_specific?.severity || "").toUpperCase();
  return {
    id: v.id,
    cves: [...new Set(cves)],
    summary: (v.summary || v.details || "").replace(/\s+/g, " ").trim(),
    severity: SEVERITY_RANK[sev] ? sev : "",
    cvss_vector: (v.severity || []).map((s) => s.score).find(Boolean) || "",
    fixed: [...new Set(fixed)],
    published: v.published || "",
  };
}

// rank orders findings most urgent first: known exploited, then EPSS, then severity.
export function rank(a, b) {
  return (b.kev === true) - (a.kev === true) || (b.epss ?? -1) - (a.epss ?? -1) || (SEVERITY_RANK[b.severity] || 0) - (SEVERITY_RANK[a.severity] || 0);
}

// enrich adds kev (CISA KEV) and epss (FIRST) to findings that carry a CVE id, with one request per hundred CVEs.
async function enrich(findings, deps) {
  const cves = [...new Set(findings.flatMap((f) => f.cves))];
  if (!cves.length) return;
  const kev = new Set();
  try {
    const r = await request(KEV, deps);
    for (const v of r.body?.vulnerabilities || []) kev.add(v.cveID);
  } catch {
    /* scores are an addition: a report without them still stands */
  }
  const epss = new Map();
  for (let i = 0; i < cves.length; i += 100) {
    try {
      const r = await request(`${EPSS}?cve=${cves.slice(i, i + 100).join(",")}`, deps);
      for (const d of r.body?.data || []) epss.set(d.cve, Number(d.epss));
    } catch {
      /* as above */
    }
  }
  for (const f of findings) {
    f.kev = f.cves.some((c) => kev.has(c));
    const s = f.cves.map((c) => epss.get(c)).filter((x) => typeof x === "number");
    f.epss = s.length ? Math.max(...s) : undefined;
  }
}

const short = (s, n) => (s.length > n ? s.slice(0, n - 1) + "…" : s);

// formatFinding is one line for one vulnerability, with the package it was found in when there is one.
export function formatFinding(f) {
  const where = f.package ? `${f.package.name}@${f.package.version} (${f.package.ecosystem})  ` : "";
  const ids = [f.id, ...f.cves.filter((c) => c !== f.id)].join(" ");
  const tags = [f.severity && f.severity.toLowerCase(), f.kev && "KEV", typeof f.epss === "number" && `epss ${(f.epss * 100).toFixed(1)}%`].filter(Boolean).join(", ");
  const fix = f.fixed.length ? `  fixed in ${f.fixed.join(", ")}` : "  no fixed version listed";
  return `${where}${ids}${tags ? `  [${tags}]` : ""}${fix}\n    ${short(f.summary || "(no summary)", 200)}`;
}

// ---- commands -----------------------------------------------------------------------------------------------------

async function scan(pkgs, deps) {
  // OSV's querybatch answers ids only, at most 1000 queries a call; the records are fetched once per id.
  const ids = pkgs.map(() => []);
  for (let i = 0; i < pkgs.length; i += 1000) {
    const chunk = pkgs.slice(i, i + 1000);
    const r = await request(`${OSV}/querybatch`, {
      ...deps,
      body: { queries: chunk.map((p) => ({ package: { ecosystem: p.ecosystem, name: p.name }, version: p.version })) },
    });
    (r.body?.results || []).forEach((res, j) => (ids[i + j] = (res.vulns || []).map((v) => v.id)));
  }
  const unique = [...new Set(ids.flat())];
  const records = new Map();
  await mapLimit(unique, 6, async (id) => {
    const r = await request(`${OSV}/vulns/${encodeURIComponent(id)}`, deps);
    if (r.status === 200) records.set(id, r.body);
  });
  const findings = [];
  pkgs.forEach((p, i) => {
    for (const id of ids[i]) if (records.has(id)) findings.push({ ...summarize(records.get(id), p), package: p });
  });
  return findings;
}

// dedupe keeps one finding per package and vulnerability: OSV lists an advisory under each of its aliases.
function dedupe(findings) {
  const seen = new Set();
  return findings.filter((f) => {
    const key = `${f.package?.ecosystem}|${f.package?.name}|${f.package?.version}|${[f.id, ...f.cves].sort()[0]}`;
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
}

function nvdSummary(c) {
  const m = c.metrics || {};
  const pick = (k) => (m[k] || []).find((x) => x.type === "Primary") || (m[k] || [])[0];
  const best = pick("cvssMetricV40") || pick("cvssMetricV31") || pick("cvssMetricV30") || pick("cvssMetricV2");
  return {
    id: c.id,
    status: c.vulnStatus,
    published: c.published,
    cvss: best ? { version: best.cvssData.version, score: best.cvssData.baseScore, vector: best.cvssData.vectorString } : null,
    cwes: [...new Set((c.weaknesses || []).flatMap((w) => w.description.map((d) => d.value)).filter((x) => x.startsWith("CWE-")))],
    description: (c.descriptions || []).find((d) => d.lang === "en")?.value || "",
    kev: c.cisaExploitAdd ? { added: c.cisaExploitAdd, action: c.cisaRequiredAction, due: c.cisaActionDue } : null,
    references: (c.references || []).map((r) => r.url),
  };
}

function formatNvd(n) {
  const cvss = n.cvss ? `cvss ${n.cvss.version} ${n.cvss.score} ${n.cvss.vector}` : "no cvss yet";
  const lines = [`${n.id}  ${cvss}${n.kev ? "  KEV" : ""}  ${n.status}  ${(n.published || "").slice(0, 10)}`];
  if (n.cwes.length) lines.push(`  weakness   ${n.cwes.join(", ")}`);
  lines.push(`  ${short(n.description.replace(/\s+/g, " "), 400)}`);
  if (n.kev) lines.push(`  CISA: added ${n.kev.added}, due ${n.kev.due}: ${short(n.kev.action || "", 200)}`);
  if (n.references.length) lines.push(`  references ${n.references.slice(0, 4).join(" ")}`);
  return lines.join("\n");
}

const DATE = /^\d{4}-\d{2}-\d{2}$/;

// nvdQuery builds the NVD search query from options, or throws a usage error. The NVD takes a publication window of
// at most 120 days, so --since is limited to that.
export function nvdQuery(opts, flags, now = new Date()) {
  if (!opts.cpe === !opts.keyword) throw new UsageError("nvd needs exactly one of --cpe or --keyword");
  if (opts.cpe && !/^cpe:2\.3:[aho]:[^:]+:[^:]+/.test(opts.cpe)) throw new UsageError("--cpe takes a CPE 2.3 name, e.g. cpe:2.3:a:openbsd:openssh:9.6");
  const q = new URLSearchParams();
  if (opts.cpe) q.set("virtualMatchString", opts.cpe);
  if (opts.keyword) q.set("keywordSearch", opts.keyword);
  if (flags.has("kev")) q.set("hasKev", "");
  if (opts.since) {
    if (!DATE.test(opts.since)) throw new UsageError("--since takes YYYY-MM-DD");
    const start = new Date(`${opts.since}T00:00:00Z`);
    if ((now - start) / 86400000 > 120) throw new UsageError("the NVD searches at most 120 days of publications: --since must be within 120 days");
    q.set("pubStartDate", start.toISOString().replace("Z", ""));
    q.set("pubEndDate", now.toISOString().replace("Z", ""));
  }
  q.set("resultsPerPage", String(intOpt(opts.limit, 20)));
  return q.toString().replace("hasKev=&", "hasKev&").replace(/hasKev=$/, "hasKev");
}

function intOpt(v, def) {
  if (v === undefined) return def;
  if (!/^\d+$/.test(v) || Number(v) < 1) throw new UsageError("--limit takes a positive whole number");
  return Number(v);
}

// run executes one command and returns the exit status. deps replace the network and the environment in tests.
export async function run(argv, { out = (s) => console.log(s), err = (s) => console.error(s), env = process.env, cwd = process.cwd(), ...deps } = {}) {
  let a;
  try {
    a = parseArgs(argv);
  } catch (e) {
    err(`${e.message}\n${USAGE}`);
    return 1;
  }
  if (!a.cmd || ["help", "--help", "-h"].includes(a.cmd)) {
    (a.cmd ? out : err)(USAGE);
    return a.cmd ? 0 : 1;
  }
  const json = a.flags.has("json");
  const nvdHeaders = env.NVD_API_KEY ? { apiKey: env.NVD_API_KEY } : {};
  try {
    switch (a.cmd) {
      case "deps": {
        const targets = a.words.length ? a.words : [cwd];
        const files = [];
        for (const t of targets) {
          const st = fs.statSync(t);
          if (st.isDirectory()) files.push(...findLockfiles(t));
          else if (lockfileParser(t)) files.push(t);
          else throw new UsageError(`${t}: not a lockfile this script reads (${Object.keys(LOCKFILES).join(", ")})`);
        }
        if (!files.length) throw new UsageError(`no lockfile found under ${targets.join(", ")}`);
        const pkgs = [];
        const seen = new Set();
        const unreadable = [];
        for (const f of files) {
          let list;
          try {
            list = lockfileParser(f)(fs.readFileSync(f, "utf8"));
          } catch (e) {
            err(`${f}: could not be read as a lockfile: ${e.message}`);
            unreadable.push(path.relative(cwd, f) || f);
            continue;
          }
          for (const p of list) {
            const k = `${p.ecosystem}|${p.name}|${p.version}`;
            if (!seen.has(k)) {
              seen.add(k);
              pkgs.push({ ...p, file: path.relative(cwd, f) || f });
            }
          }
        }
        const findings = dedupe(await scan(pkgs, deps));
        await enrich(findings, deps);
        findings.sort(rank);
        if (json) out(JSON.stringify({ files: files.map((f) => path.relative(cwd, f) || f), packages: pkgs.length, findings }, null, 2));
        else {
          const shown = a.flags.has("all") ? findings : findings.slice(0, 40);
          const affected = new Set(findings.map((f) => `${f.package.ecosystem}|${f.package.name}|${f.package.version}`)).size;
          out(`${pkgs.length} packages from ${files.map((f) => path.relative(cwd, f) || f).join(", ")}`);
          out(findings.length ? `${findings.length} known vulnerabilities in ${affected} packages, most urgent first:\n` : "no known vulnerabilities (OSV.dev)");
          for (const f of shown) out(formatFinding(f));
          if (shown.length < findings.length) out(`\n${findings.length - shown.length} more: --all lists them, --json gives every record`);
          out("\nsource: OSV.dev (GitHub, PyPI, Go, RustSec and other advisory databases), CISA KEV, FIRST EPSS");
        }
        if (unreadable.length) err(`not scanned: ${unreadable.join(", ")}`);
        // a lockfile that could not be read was not scanned: never report it clean
        return findings.length ? 3 : unreadable.length ? 2 : 0;
      }
      case "pkg": {
        const [ecosystem, name, version] = a.words;
        if (!ecosystem || !name || a.words.length > 3) throw new UsageError("pkg takes an ecosystem, a name and optionally a version");
        const body = { package: { ecosystem, name }, ...(version ? { version } : {}) };
        const r = await request(`${OSV}/query`, { ...deps, body });
        const findings = dedupe((r.body?.vulns || []).map((v) => ({ ...summarize(v, { ecosystem, name }), package: { ecosystem, name, version: version || "any" } })));
        await enrich(findings, deps);
        findings.sort(rank);
        if (json) out(JSON.stringify(findings, null, 2));
        else {
          out(findings.length ? findings.map(formatFinding).join("\n") : `no known vulnerabilities for ${name}${version ? "@" + version : ""} in ${ecosystem} (OSV.dev)`);
          if (!version && findings.length) out("\n(no version given: every advisory the package ever had)");
        }
        return findings.length ? 3 : 0;
      }
      case "id": {
        if (!a.words.length) throw new UsageError("id needs at least one vulnerability id");
        const bad = a.words.find((w) => !/^[A-Z][A-Z0-9]*-[A-Za-z0-9.:_-]+$/i.test(w));
        if (bad) throw new UsageError(`${bad} is not a vulnerability id`);
        const results = [];
        let nvdAsked = false; // every NVD request counts against its limit, an answer without a record too
        for (const w of a.words) {
          const id = /^cve-/i.test(w) ? w.toUpperCase() : w;
          const osv = await request(`${OSV}/vulns/${encodeURIComponent(id)}`, deps);
          const rec = { id, osv: osv.status === 200 ? summarize(osv.body) : null };
          if (rec.osv) rec.packages = (osv.body.affected || []).map((x) => `${x.package?.name} (${x.package?.ecosystem})`).filter((x) => !x.startsWith("undefined"));
          const cve = /^CVE-/.test(id) ? id : rec.osv?.cves[0];
          if (cve) {
            if (nvdAsked) await (deps.wait || sleep)(env.NVD_API_KEY ? 700 : 6500); // the NVD's keyless limit: 5 requests in 30 s
            nvdAsked = true;
            const n = await request(`${NVD}?cveId=${cve}`, { ...deps, headers: nvdHeaders });
            const c = n.body?.vulnerabilities?.[0]?.cve;
            rec.nvd = c ? nvdSummary(c) : null;
          }
          const f = { cves: cve ? [cve] : [] };
          await enrich([f], deps);
          rec.kev = f.kev;
          rec.epss = f.epss;
          results.push(rec);
        }
        if (json) out(JSON.stringify(results, null, 2));
        else
          for (const r of results) {
            if (!r.osv && !r.nvd) {
              out(`${r.id}: in neither OSV.dev nor the NVD`);
              continue;
            }
            if (r.nvd) out(formatNvd(r.nvd));
            else out(`${r.id}  ${r.osv.severity.toLowerCase() || "no severity"}  ${short(r.osv.summary, 300)}`);
            if (typeof r.epss === "number") out(`  epss       ${(r.epss * 100).toFixed(1)}% chance of exploitation in the next 30 days${r.kev ? "; on CISA's KEV list" : ""}`);
            const shownId = r.nvd ? r.nvd.id : r.id;
            if (r.id !== shownId || (r.osv?.id && r.osv.id !== shownId)) out(`  also       ${[...new Set([r.id, r.osv?.id].filter((x) => x && x !== shownId))].join(", ")}`);
            if (r.packages?.length) out(`  packages   ${short([...new Set(r.packages)].join(", "), 300)}${r.osv.fixed.length ? `; fixed in ${short(r.osv.fixed.join(", "), 120)}` : ""}`);
            out("");
          }
        return 0;
      }
      case "nvd": {
        const q = nvdQuery(a.opts, a.flags);
        const r = await request(`${NVD}?${q}`, { ...deps, headers: nvdHeaders });
        const list = (r.body?.vulnerabilities || []).map((v) => nvdSummary(v.cve));
        if (json) out(JSON.stringify(list, null, 2));
        else {
          out(list.length ? list.map(formatNvd).join("\n\n") : "no CVEs match");
          out(`\nsource: NVD (${list.length} of ${r.body?.totalResults ?? 0} shown; --limit for more)`);
        }
        return 0;
      }
      case "kev": {
        if (a.opts.since && !DATE.test(a.opts.since)) throw new UsageError("--since takes YYYY-MM-DD");
        const limit = intOpt(a.opts.limit, 30);
        const r = await request(KEV, deps);
        let list = r.body?.vulnerabilities || [];
        const has = (field, v) => (x) => (x[field] || "").toLowerCase().includes(v.toLowerCase());
        if (a.opts.vendor) list = list.filter(has("vendorProject", a.opts.vendor));
        if (a.opts.product) list = list.filter(has("product", a.opts.product));
        if (a.opts.since) list = list.filter((x) => x.dateAdded >= a.opts.since);
        list = [...list].sort((x, y) => (y.dateAdded || "").localeCompare(x.dateAdded || "")).slice(0, limit);
        if (json) out(JSON.stringify(list, null, 2));
        else {
          for (const v of list) out(`${v.cveID}  ${v.dateAdded}  ${v.vendorProject} ${v.product}${v.knownRansomwareCampaignUse === "Known" ? "  ransomware" : ""}  due ${v.dueDate}\n    ${short(v.shortDescription || "", 200)}`);
          out(`\nsource: CISA Known Exploited Vulnerabilities catalog ${r.body?.catalogVersion || ""} (${list.length} shown)`);
        }
        return 0;
      }
      default:
        throw new UsageError(`unknown command ${a.cmd}`);
    }
  } catch (e) {
    if (e instanceof UsageError) {
      err(`${e.message}\n${USAGE}`);
      return 1;
    }
    if (e.code === "ENOENT" || e.code === "EACCES") {
      err(e.message);
      return 1;
    }
    err(`lookup failed: ${e.message}`);
    return 2;
  }
}

if (import.meta.url === pathToFileURL(process.argv[1] || "").href) {
  process.exitCode = await run(process.argv.slice(2));
}
