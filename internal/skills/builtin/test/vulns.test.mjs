// The vuln-check skill's vulns.mjs: what it reads from each lockfile, what it asks OSV, the NVD, KEV and EPSS, and how
// it ranks and exits, with no network.
import { test } from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { EPSS, KEV, NVD, OSV, findLockfiles, lockfileParser, nvdQuery, parseArgs, rank, run, summarize } from "../data/vuln-check/vulns.mjs";

const json = (status, body) => ({ status, json: async () => body });

// fakeFetch answers from a table of URL prefix → response (or a function of the URL and request) and records calls.
function fakeFetch(routes) {
  const calls = [];
  const fetch = async (url, init) => {
    calls.push({ url, body: init?.body ? JSON.parse(init.body) : undefined, headers: init?.headers });
    for (const [k, v] of Object.entries(routes)) if (url.startsWith(k)) return typeof v === "function" ? v(url, init) : v;
    return json(404, {});
  };
  return { fetch, calls };
}

async function capture(argv, deps) {
  const out = [];
  const err = [];
  const code = await run(argv, { out: (s) => out.push(s), err: (s) => err.push(s), wait: async () => {}, env: {}, ...deps });
  return { code, out: out.join("\n"), err: err.join("\n") };
}

const parse = (file, text) => lockfileParser(file)(text).map((p) => `${p.ecosystem}:${p.name}@${p.version}`);

test("every lockfile format yields its pinned packages", () => {
  assert.deepEqual(
    parse("package-lock.json", JSON.stringify({ lockfileVersion: 3, packages: { "": { name: "app", version: "1.0.0" }, "node_modules/lodash": { version: "4.17.15" }, "node_modules/a/node_modules/@scope/b": { version: "2.0.0" }, "node_modules/linked": { link: true } } })),
    ["npm:lodash@4.17.15", "npm:@scope/b@2.0.0"],
    "the project itself (the \"\" entry) is not a dependency",
  );
  assert.deepEqual(parse("package-lock.json", JSON.stringify({ lockfileVersion: 1, dependencies: { a: { version: "1.0.0", dependencies: { b: { version: "2.0.0" } } } } })), ["npm:a@1.0.0", "npm:b@2.0.0"]);
  assert.deepEqual(parse("yarn.lock", `# yarn lockfile v1\n\n"@babel/core@^7.0.0", "@babel/core@^7.1.0":\n  version "7.22.5"\n\nlodash@^4.17.0:\n  version "4.17.15"\n`), ["npm:@babel/core@7.22.5", "npm:lodash@4.17.15"]);
  assert.deepEqual(parse("yarn.lock", `__metadata:\n  version: 6\n\n"lodash@npm:^4.17.0":\n  version: 4.17.15\n`), ["npm:lodash@4.17.15"]);
  assert.deepEqual(parse("pnpm-lock.yaml", `lockfileVersion: '9.0'\n\npackages:\n\n  '@types/node@20.1.0':\n    resolution: {}\n\n  lodash@4.17.15:\n    resolution: {}\n`), ["npm:@types/node@20.1.0", "npm:lodash@4.17.15"]);
  assert.deepEqual(parse("pnpm-lock.yaml", `lockfileVersion: 5.4\npackages:\n  /lodash/4.17.15:\n    resolution: {}\n`), ["npm:lodash@4.17.15"]);
  assert.deepEqual(parse("requirements-dev.txt", "requests==2.19.0  # pinned\nflask>=1.0\nuvicorn[standard]==0.20.0\n-r base.txt\n"), ["PyPI:requests@2.19.0", "PyPI:uvicorn@0.20.0"]);
  assert.deepEqual(parse("poetry.lock", `[[package]]\nname = "django"\nversion = "4.2.1"\n\n[[package]]\nname = "pytz"\nversion = "2023.3"\n`), ["PyPI:django@4.2.1", "PyPI:pytz@2023.3"]);
  assert.deepEqual(parse("Cargo.lock", `[[package]]\nname = "myapp"\nversion = "0.1.0"\n\n[[package]]\nname = "time"\nversion = "0.1.45"\nsource = "registry+https://github.com/rust-lang/crates.io-index"\n`), ["crates.io:time@0.1.45"]);
  assert.deepEqual(parse("Pipfile.lock", JSON.stringify({ default: { requests: { version: "==2.19.0" } }, develop: { pytest: { version: "==7.0.0" } } })), ["PyPI:requests@2.19.0", "PyPI:pytest@7.0.0"]);
  assert.deepEqual(parse("go.mod", "module x\n\ngo 1.22\n\nrequire golang.org/x/net v0.17.0\n\nrequire (\n\tgithub.com/a/b v1.2.3 // indirect\n)\n"), ["Go:golang.org/x/net@v0.17.0", "Go:github.com/a/b@v1.2.3"]);
  assert.deepEqual(parse("Gemfile.lock", "GEM\n  remote: https://rubygems.org/\n  specs:\n    rack (2.2.3)\n      ruby2_keywords\n    nokogiri (1.13.0-x86_64-linux)\n\nPLATFORMS\n  ruby\n"), ["RubyGems:rack@2.2.3", "RubyGems:nokogiri@1.13.0"]);
  assert.deepEqual(parse("composer.lock", JSON.stringify({ packages: [{ name: "guzzlehttp/guzzle", version: "v7.4.0" }], "packages-dev": [{ name: "a/b", version: "dev-main" }] })), ["Packagist:guzzlehttp/guzzle@7.4.0"]);
  assert.equal(lockfileParser("README.md"), null);
});

test("lockfiles are found beside each other but not inside dependency folders", () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "vulns-"));
  for (const f of ["package-lock.json", "api/poetry.lock", "node_modules/x/package-lock.json", ".venv/requirements.txt", "a/b/c/d/go.mod"]) {
    fs.mkdirSync(path.dirname(path.join(dir, f)), { recursive: true });
    fs.writeFileSync(path.join(dir, f), "{}");
  }
  assert.deepEqual(findLockfiles(dir).map((f) => path.relative(dir, f)), [path.join("api", "poetry.lock"), "package-lock.json"]);
  fs.rmSync(dir, { recursive: true, force: true });
});

test("deps asks OSV once in a batch, ranks KEV then EPSS then severity, and exits 3", async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "vulns-"));
  fs.writeFileSync(path.join(dir, "requirements.txt"), "requests==2.19.0\nflask==0.12\nsafe==1.0\n");
  const rec = (id, cve, sev, fixed, name) => ({ id, aliases: [cve], summary: id, database_specific: { severity: sev }, affected: [{ package: { ecosystem: "PyPI", name }, ranges: [{ events: [{ introduced: "0" }, { fixed }] }] }] });
  const records = { "GHSA-a": rec("GHSA-a", "CVE-2001-0001", "CRITICAL", "2.20.0", "requests"), "GHSA-b": rec("GHSA-b", "CVE-2001-0002", "MODERATE", "1.0", "flask"), "GHSA-c": rec("GHSA-c", "CVE-2001-0003", "HIGH", "2.31.0", "requests") };
  const { fetch, calls } = fakeFetch({
    [`${OSV}/querybatch`]: json(200, { results: [{ vulns: [{ id: "GHSA-a" }, { id: "GHSA-c" }] }, { vulns: [{ id: "GHSA-b" }] }, {}] }),
    [`${OSV}/vulns/`]: (url) => json(200, records[decodeURIComponent(url.split("/").pop())]),
    [KEV]: json(200, { vulnerabilities: [{ cveID: "CVE-2001-0002" }] }),
    [EPSS]: json(200, { data: [{ cve: "CVE-2001-0001", epss: "0.01" }, { cve: "CVE-2001-0003", epss: "0.5" }] }),
  });
  const r = await capture(["deps", dir, "--json"], { fetch, cwd: dir });
  assert.equal(r.code, 3);
  const batch = calls.filter((c) => c.url === `${OSV}/querybatch`);
  assert.equal(batch.length, 1);
  assert.deepEqual(batch[0].body.queries.map((q) => `${q.package.name}@${q.version}`), ["requests@2.19.0", "flask@0.12", "safe@1.0"]);
  const res = JSON.parse(r.out);
  assert.deepEqual(res.findings.map((f) => f.id), ["GHSA-b", "GHSA-c", "GHSA-a"], "KEV first, then EPSS, before a critical with low EPSS");
  assert.deepEqual(res.findings[0].fixed, ["1.0"]);
  assert.equal(res.findings[0].kev, true);
  fs.rmSync(dir, { recursive: true, force: true });
});

test("a clean scan exits 0 and a failed service exits 2", async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "vulns-"));
  fs.writeFileSync(path.join(dir, "requirements.txt"), "safe==1.0\n");
  let r = await capture(["deps"], { fetch: fakeFetch({ [`${OSV}/querybatch`]: json(200, { results: [{}] }) }).fetch, cwd: dir });
  assert.equal(r.code, 0);
  assert.match(r.out, /no known vulnerabilities/);
  r = await capture(["deps"], { fetch: fakeFetch({ [`${OSV}/querybatch`]: json(503, {}) }).fetch, cwd: dir });
  assert.equal(r.code, 2);
  assert.match(r.err, /lookup failed: .*HTTP 503/);
  fs.rmSync(dir, { recursive: true, force: true });
});

test("id waits between NVD requests to stay under the keyless limit, and sends the key when there is one", async () => {
  const waits = [];
  const { fetch, calls } = fakeFetch({ [NVD]: (url) => json(200, { vulnerabilities: [{ cve: { id: new URL(url).searchParams.get("cveId"), descriptions: [{ lang: "en", value: "x" }], metrics: {} } }] }) });
  let r = await capture(["id", "CVE-2024-0001", "cve-2024-0002"], { fetch, wait: async (ms) => waits.push(ms) });
  assert.equal(r.code, 0);
  assert.deepEqual(calls.filter((c) => c.url.startsWith(NVD)).map((c) => new URL(c.url).searchParams.get("cveId")), ["CVE-2024-0001", "CVE-2024-0002"]);
  assert.ok(waits.some((ms) => ms >= 6000), `waits ${waits}`);
  r = await capture(["id", "CVE-2024-0001"], { fetch, env: { NVD_API_KEY: "k" } });
  assert.equal(calls.filter((c) => c.url.startsWith(NVD)).at(-1).headers.apiKey, "k");
});

test("nvd builds a bounded query and refuses a window the NVD would reject", () => {
  const now = new Date("2026-11-09T00:00:00Z");
  const a = parseArgs(["nvd", "--cpe", "cpe:2.3:a:openbsd:openssh:9.6", "--kev", "--since", "2026-10-01"]);
  const q = nvdQuery(a.opts, a.flags, now);
  assert.match(q, /virtualMatchString=cpe%3A2\.3%3Aa%3Aopenbsd%3Aopenssh%3A9\.6/);
  assert.match(q, /(^|&)hasKev(&|$)/);
  assert.match(q, /pubStartDate=2026-10-01T00%3A00%3A00\.000/);
  assert.match(q, /resultsPerPage=20/);
  const old = parseArgs(["nvd", "--keyword", "x", "--since", "2025-01-01"]);
  assert.throws(() => nvdQuery(old.opts, old.flags, now), /120 days/);
});

test("command-line mistakes exit 1 with the usage and send nothing", async () => {
  const { fetch, calls } = fakeFetch({});
  for (const argv of [[], ["bogus"], ["pkg", "npm"], ["id"], ["id", "not an id"], ["nvd"], ["nvd", "--cpe", "openssh"], ["kev", "--since", "May"], ["deps", "--nope"], ["deps", "README.md"]]) {
    const r = await capture(argv, { fetch, cwd: os.tmpdir() });
    assert.equal(r.code, 1, argv.join(" "));
  }
  assert.deepEqual(calls, []);
});

test("an advisory's fixed versions are those of the package it was found in", () => {
  const v = { id: "GHSA-x", aliases: ["CVE-2020-1"], affected: [{ package: { ecosystem: "npm", name: "a" }, ranges: [{ events: [{ fixed: "1.2.3" }] }] }, { package: { ecosystem: "npm", name: "b" }, ranges: [{ events: [{ fixed: "9.9.9" }] }] }] };
  assert.deepEqual(summarize(v, { ecosystem: "npm", name: "a" }).fixed, ["1.2.3"]);
  assert.deepEqual(summarize(v, { ecosystem: "npm", name: "a" }).cves, ["CVE-2020-1"]);
  const list = [{ severity: "CRITICAL" }, { severity: "LOW", epss: 0.2 }, { severity: "LOW", kev: true }];
  assert.deepEqual(list.sort(rank).map((f) => f.severity + (f.kev ? "+kev" : "")), ["LOW+kev", "LOW", "CRITICAL"]);
});
