#!/usr/bin/env node
// Keyless Shodan lookups: InternetDB (what Shodan last saw on a public IP) and CVEDB (CVE, CPE, KEV and EPSS data).
// Needs Node 18 or newer and nothing else: no API key, no account, no npm package.
//
//   node shodan.mjs host 1.1.1.1 example.com [--cves [--top N]] [--json]
//   node shodan.mjs hosts targets.txt [--cves [--top N]] [--json]     one IP or hostname per line, # comments
//   node shodan.mjs cve CVE-2021-44228 [CVE-...] [--json]
//   node shodan.mjs cves --product nginx | --cpe cpe:2.3:a:f5:nginx:1.20.0 [--kev] [--epss] [--since YYYY-MM-DD]
//                        [--until YYYY-MM-DD] [--limit N] [--skip N] [--json]
//   node shodan.mjs cpes nginx [--limit N] [--json]
//
// Exit status: 0 when every lookup answered (an address Shodan has no data on counts as answered), 1 on a usage
// error, 2 when a lookup failed (network, timeout, rate limit after retries, or an unexpected status).
import dns from "node:dns/promises";
import fs from "node:fs";
import net from "node:net";
import { pathToFileURL } from "node:url";

export const INTERNETDB = "https://internetdb.shodan.io";
export const CVEDB = "https://cvedb.shodan.io";

const USAGE = `usage:
  shodan.mjs host <ip|hostname>... [--cves [--top N]] [--json]
  shodan.mjs hosts <file> [--cves [--top N]] [--json]
  shodan.mjs cve <CVE-ID>... [--json]
  shodan.mjs cves (--product NAME | --cpe CPE23) [--kev] [--epss] [--since DATE] [--until DATE] [--limit N] [--skip N] [--json]
  shodan.mjs cpes <product> [--limit N] [--json]`;

// UsageError is a mistake in the command line: exit status 1 and the usage text.
export class UsageError extends Error {}

const FLAGS = new Set(["--cves", "--json", "--kev", "--epss"]);
const OPTIONS = new Set(["--product", "--cpe", "--since", "--until", "--limit", "--skip", "--top"]);

// parseArgs splits argv into a command, its positional words, boolean flags and valued options.
// It throws an Error whose message is meant for the user on an unknown flag or a missing value.
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

// isPublicIP reports whether ip is a globally routable address. Shodan's data for a private, loopback, link-local,
// shared, documentation or multicast address describes some other network's host, never the one you mean.
export function isPublicIP(ip) {
  const v = net.isIP(ip);
  if (v === 4) {
    const [a, b] = ip.split(".").map(Number);
    return !(
      a === 0 || a === 10 || a === 127 || a >= 224 ||
      (a === 100 && b >= 64 && b <= 127) ||
      (a === 169 && b === 254) ||
      (a === 172 && b >= 16 && b <= 31) ||
      (a === 192 && b === 168) ||
      (a === 192 && b === 0) ||
      (a === 198 && (b === 18 || b === 19 || b === 51)) ||
      (a === 203 && b === 0)
    );
  }
  if (v === 6) {
    const s = ip.toLowerCase();
    if (s === "::" || s === "::1") return false;
    const m = s.match(/^::ffff:(\d+\.\d+\.\d+\.\d+)$/);
    if (m) return isPublicIP(m[1]);
    const head = parseInt(s.split(":")[0] || "0", 16);
    return !(
      (head & 0xfe00) === 0xfc00 || // unique local
      (head & 0xffc0) === 0xfe80 || // link-local
      (head & 0xff00) === 0xff00 || // multicast
      s.startsWith("2001:db8:") || s.startsWith("2001:0db8:") // documentation
    );
  }
  return false;
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// getJSON fetches url and returns { status, body }. A 404 is an answer (Shodan has no data), not a failure. A 429 or
// 5xx, a timeout or a network error is retried with backoff; after the last try it throws.
export async function getJSON(url, { fetch = globalThis.fetch, tries = 3, timeoutMs = 30000, wait = sleep } = {}) {
  let last;
  for (let t = 0; t < tries; t++) {
    if (t > 0) await wait(1000 * 2 ** (t - 1));
    try {
      const res = await fetch(url, { headers: { accept: "application/json" }, signal: AbortSignal.timeout(timeoutMs) });
      if (res.status === 429 || res.status >= 500) {
        last = new Error(`${url}: HTTP ${res.status}`);
        continue;
      }
      let body = null;
      try {
        body = await res.json();
      } catch {
        body = null;
      }
      if (res.status === 200 || res.status === 404) return { status: res.status, body };
      const detail = body && body.detail ? `: ${JSON.stringify(body.detail).slice(0, 300)}` : "";
      throw Object.assign(new Error(`${url}: HTTP ${res.status}${detail}`), { final: true });
    } catch (e) {
      if (e.final) throw e;
      last = e.name === "TimeoutError" ? new Error(`${url}: no answer in ${timeoutMs / 1000}s`) : e;
    }
  }
  throw last;
}

// mapLimit runs fn over items with at most n in flight, keeping the input order. InternetDB and CVEDB are free
// services shared by everyone: a small n is the polite default.
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

// resolveTargets turns IPs and hostnames into lookups. A hostname is resolved through the system resolver to all its
// addresses; private and other non-public addresses are kept with a note and never sent to Shodan.
export async function resolveTargets(words, { lookup = (h) => dns.lookup(h, { all: true }) } = {}) {
  const out = [];
  for (const w of words) {
    if (net.isIP(w)) {
      out.push({ target: w, ip: w, skip: isPublicIP(w) ? "" : "not a public address" });
      continue;
    }
    let addrs;
    try {
      addrs = (await lookup(w)).map((a) => a.address);
    } catch (e) {
      out.push({ target: w, ip: "", skip: `does not resolve (${e.code || e.message})` });
      continue;
    }
    for (const ip of [...new Set(addrs)]) out.push({ target: w, ip, skip: isPublicIP(ip) ? "" : "not a public address" });
  }
  return out;
}

// short collapses whitespace and cuts s to n characters.
const short = (s, n) => {
  const t = (s || "").replace(/\s+/g, " ").trim();
  return t.length > n ? t.slice(0, n - 1) + "…" : t;
};

// toCPE23 turns InternetDB's CPE 2.2 name (cpe:/a:apache:http_server:2.4.7) into the CPE 2.3 form CVEDB takes, or
// returns "" when it has no version: an unversioned product matches every CVE it ever had.
export function toCPE23(cpe) {
  const m = /^cpe:\/([aho]):([^:]+):([^:]+):([^:]+)/.exec(cpe || "");
  return m ? `cpe:2.3:${m[1]}:${m[2]}:${m[3]}:${m[4]}` : "";
}

// cveRank orders CVEs most urgent first: known exploited, then by EPSS, then by CVSS.
export function cveRank(a, b) {
  return (b.kev === true) - (a.kev === true) || (b.epss ?? -1) - (a.epss ?? -1) || (b.cvss ?? -1) - (a.cvss ?? -1);
}

// formatCVE is one line per CVE: id, CVSS, EPSS, KEV and the start of the summary.
export function formatCVE(c) {
  const epss = typeof c.epss === "number" ? `epss ${(c.epss * 100).toFixed(1)}%` : "epss -";
  const cvss = typeof c.cvss === "number" ? `cvss ${c.cvss}` : "cvss -";
  const date = c.published_time ? ` ${c.published_time.slice(0, 10)}` : "";
  return `${c.cve_id}  ${cvss}  ${epss}${c.kev ? "  KEV" : ""}${c.ransomware_campaign ? "  ransomware" : ""}${date}  ${short(c.summary, 160)}`.trimEnd();
}

// formatHost is the text report of one InternetDB answer.
export function formatHost(r) {
  const name = r.target !== r.ip && r.ip ? `${r.target} → ${r.ip}` : r.target;
  if (r.skip) return `${name}: skipped, ${r.skip}`;
  if (r.error) return `${name}: lookup failed: ${r.error}`;
  if (!r.data) return `${name}: Shodan has no data on this address (not seen in its scans; this does not mean nothing listens)`;
  const d = r.data;
  const lines = [`${name}`];
  lines.push(`  ports     ${d.ports.length ? d.ports.join(", ") : "none seen"}`);
  if (d.hostnames.length) lines.push(`  hostnames ${short(d.hostnames.join(", "), 300)}`);
  if (d.tags.length) lines.push(`  tags      ${d.tags.join(", ")}`);
  if (d.cpes.length) lines.push(`  cpes      ${d.cpes.join(", ")}`);
  if (!d.vulns.length) lines.push("  vulns     none listed");
  else if (!r.cves) lines.push(`  vulns     ${d.vulns.length}: ${short(d.vulns.join(", "), 400)}  (add --cves for scores)`);
  else {
    lines.push(`  vulns     ${d.vulns.length}; the most urgent (inferred from software versions, not confirmed):`);
    for (const c of r.cves) lines.push(`    ${formatCVE(c)}`);
    const rest = r.unscored || [];
    if (rest.length) lines.push(`    ${rest.length} more (lower ranked, or not matched to a product): ${short(rest.join(", "), 400)}`);
  }
  return lines.join("\n");
}

// cvesQuery builds the CVEDB /cves query string from parsed options, or throws a usage error.
export function cvesQuery(opts, flags) {
  if (!opts.product === !opts.cpe) throw new UsageError("cves needs exactly one of --product or --cpe");
  if (opts.cpe && !/^cpe:2\.3:[aho]:[^:]+:[^:]+:[^:]+/.test(opts.cpe)) {
    throw new UsageError("--cpe takes a CPE 2.3 name with a version, e.g. cpe:2.3:a:f5:nginx:1.20.0 (list them with: cpes nginx)");
  }
  for (const k of ["since", "until"]) {
    if (opts[k] && !/^\d{4}-\d{2}-\d{2}(T\d{2}:\d{2}:\d{2})?$/.test(opts[k])) throw new UsageError(`--${k} takes YYYY-MM-DD`);
  }
  const q = new URLSearchParams();
  if (opts.product) q.set("product", opts.product);
  if (opts.cpe) q.set("cpe23", opts.cpe);
  if (flags.has("kev")) q.set("is_kev", "true");
  if (flags.has("epss")) q.set("sort_by_epss", "true");
  if (opts.since) q.set("start_date", opts.since);
  if (opts.until) q.set("end_date", opts.until);
  q.set("skip", String(intOpt(opts.skip, 0, "--skip")));
  q.set("limit", String(intOpt(opts.limit, 20, "--limit"))); // CVEDB's own default is 1000
  return q.toString();
}

function intOpt(v, def, name) {
  if (v === undefined) return def;
  if (!/^\d+$/.test(v)) throw new UsageError(`${name} takes a whole number`);
  return Number(v);
}

// run executes one command and returns the exit status. Output goes through out(line); deps replace the network and
// the resolver in tests.
export async function run(argv, { out = (s) => console.log(s), err = (s) => console.error(s), ...deps } = {}) {
  let a;
  try {
    a = parseArgs(argv);
    if (!a.cmd || a.cmd === "help" || a.cmd === "--help" || a.cmd === "-h") {
      out(USAGE);
      return a.cmd ? 0 : 1;
    }
  } catch (e) {
    err(`${e.message}\n${USAGE}`);
    return 1;
  }
  const json = a.flags.has("json");
  const get = (url) => getJSON(url, deps);
  let failed = false;
  try {
    switch (a.cmd) {
      case "host":
      case "hosts": {
        let words = a.words;
        if (a.cmd === "hosts") {
          if (words.length !== 1) throw new UsageError("hosts takes one file");
          words = fs.readFileSync(words[0], "utf8").split(/\r?\n/).map((l) => l.replace(/#.*/, "").trim()).filter(Boolean);
        }
        if (!words.length) throw new UsageError(`${a.cmd} needs at least one IP or hostname`);
        const targets = await resolveTargets(words, deps);
        const results = await mapLimit(targets, 4, async (t) => {
          if (t.skip) return t;
          try {
            const r = await get(`${INTERNETDB}/${encodeURIComponent(t.ip)}`);
            return { ...t, data: r.status === 200 ? r.body : null };
          } catch (e) {
            failed = true;
            return { ...t, error: e.message };
          }
        });
        if (a.flags.has("cves")) {
          const top = intOpt(a.opts.top, 15, "--top");
          // One CVEDB request per versioned product (shared by every host that runs it), ranked by EPSS on the
          // server, instead of one request per CVE: a host with old software lists hundreds.
          const cpes = [...new Set(results.flatMap((r) => (r.data ? r.data.cpes.map(toCPE23).filter(Boolean) : [])))];
          const byCPE = new Map();
          await mapLimit(cpes, 4, async (cpe) => {
            try {
              const r = await get(`${CVEDB}/cves?${new URLSearchParams({ cpe23: cpe, sort_by_epss: "true", limit: String(Math.max(top, 1) * 4) })}`);
              byCPE.set(cpe, r.status === 200 && r.body ? r.body.cves || [] : []);
            } catch {
              byCPE.set(cpe, []);
            }
          });
          for (const r of results) {
            if (!r.data) continue;
            const listed = new Set(r.data.vulns);
            const seen = new Map();
            for (const cpe of r.data.cpes.map(toCPE23).filter(Boolean)) {
              for (const c of byCPE.get(cpe) || []) if (listed.has(c.cve_id) && !seen.has(c.cve_id)) seen.set(c.cve_id, c);
            }
            r.cves = [...seen.values()].sort(cveRank).slice(0, top);
            const shown = new Set(r.cves.map((c) => c.cve_id));
            r.unscored = r.data.vulns.filter((id) => !shown.has(id));
          }
        }
        if (json) out(JSON.stringify(results, null, 2));
        else {
          out(results.map(formatHost).join("\n\n"));
          out("\nsource: Shodan InternetDB, refreshed about weekly; ports and vulns are what Shodan's scanners last saw.");
        }
        break;
      }
      case "cve": {
        if (!a.words.length) throw new UsageError("cve needs at least one CVE id");
        const bad = a.words.find((w) => !/^CVE-\d{4}-\d{4,}$/i.test(w));
        if (bad) throw new UsageError(`${bad} is not a CVE id (CVE-YYYY-NNNN)`);
        const recs = await mapLimit(a.words, 4, async (id) => {
          const r = await get(`${CVEDB}/cve/${encodeURIComponent(id.toUpperCase())}`);
          return r.status === 200 ? r.body : { cve_id: id.toUpperCase(), missing: true };
        });
        if (json) out(JSON.stringify(recs, null, 2));
        else
          for (const c of recs) {
            if (c.missing) {
              out(`${c.cve_id}: not in CVEDB`);
              continue;
            }
            out(formatCVE({ ...c, summary: "" }));
            out(`  ${c.summary}`);
            if (c.propose_action) out(`  CISA action: ${c.propose_action}`);
            if (c.references?.length) out(`  references: ${c.references.slice(0, 5).join(" ")}`);
            if (c.cpes?.length) out(`  affects ${c.cpes.length} CPEs, e.g. ${c.cpes.slice(0, 3).join(", ")}`);
          }
        break;
      }
      case "cves": {
        const q = cvesQuery(a.opts, a.flags);
        const r = await get(`${CVEDB}/cves?${q}`);
        const list = r.status === 200 && r.body ? r.body.cves || [] : [];
        if (json) out(JSON.stringify(list, null, 2));
        else {
          out(list.length ? list.map(formatCVE).join("\n") : "no CVEs match");
          out(`\nsource: Shodan CVEDB (${list.length} shown; --skip and --limit page through the rest)`);
        }
        break;
      }
      case "cpes": {
        if (a.words.length !== 1) throw new UsageError("cpes takes one product name");
        const q = new URLSearchParams({ product: a.words[0], skip: String(intOpt(a.opts.skip, 0, "--skip")), limit: String(intOpt(a.opts.limit, 50, "--limit")) });
        const r = await get(`${CVEDB}/cpes?${q}`);
        const list = r.status === 200 && r.body ? r.body.cpes || [] : [];
        out(json ? JSON.stringify(list, null, 2) : list.length ? list.join("\n") : "no CPEs match");
        break;
      }
      default:
        throw new UsageError(`unknown command ${a.cmd}`);
    }
  } catch (e) {
    if (e instanceof UsageError) {
      err(`${e.message}\n${USAGE}`);
      return 1;
    }
    if (e.code === "ENOENT" || e.code === "EISDIR" || e.code === "EACCES") {
      err(e.message);
      return 1;
    }
    err(`lookup failed: ${e.message}`);
    return 2;
  }
  return failed ? 2 : 0;
}

if (import.meta.url === pathToFileURL(process.argv[1] || "").href) {
  process.exitCode = await run(process.argv.slice(2));
}
