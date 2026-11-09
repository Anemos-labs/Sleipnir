// The shodan skill's shodan.mjs: what it sends, what it refuses to send, how it ranks and reports, with no network.
import { test } from "node:test";
import assert from "node:assert/strict";
import { CVEDB, INTERNETDB, cveRank, cvesQuery, getJSON, isPublicIP, parseArgs, run, toCPE23 } from "../data/shodan/shodan.mjs";

const json = (status, body) => ({ status, json: async () => body });

// fakeFetch answers from a table of URL → response (or a function of the URL) and records every URL asked.
function fakeFetch(routes) {
  const asked = [];
  const fetch = async (url) => {
    asked.push(url);
    for (const [k, v] of Object.entries(routes)) if (url.startsWith(k)) return typeof v === "function" ? v(url) : v;
    return json(404, { detail: "No information available" });
  };
  return { fetch, asked };
}

async function capture(argv, deps) {
  const out = [];
  const err = [];
  const code = await run(argv, { out: (s) => out.push(s), err: (s) => err.push(s), wait: async () => {}, ...deps });
  return { code, out: out.join("\n"), err: err.join("\n") };
}

test("private, loopback, shared, link-local and documentation addresses are not public", () => {
  for (const ip of ["10.1.2.3", "127.0.0.1", "172.20.0.1", "192.168.1.1", "100.64.0.1", "169.254.1.1", "192.0.2.1", "198.51.100.2", "203.0.113.9", "0.0.0.0", "224.0.0.1", "::1", "fd00::1", "fe80::1", "2001:db8::1", "::ffff:10.0.0.1"]) {
    assert.equal(isPublicIP(ip), false, ip);
  }
  for (const ip of ["1.1.1.1", "8.8.8.8", "172.32.0.1", "100.128.0.1", "2606:4700:4700::1111", "::ffff:1.1.1.1"]) {
    assert.equal(isPublicIP(ip), true, ip);
  }
  assert.equal(isPublicIP("example.com"), false);
});

test("a hostname that resolves to a private address is reported and never sent to Shodan", async () => {
  const { fetch, asked } = fakeFetch({});
  const lookup = async () => [{ address: "10.0.0.5" }];
  const r = await capture(["host", "intranet.example", "192.168.0.1"], { fetch, lookup });
  assert.equal(r.code, 0);
  assert.deepEqual(asked, []);
  assert.match(r.out, /intranet\.example → 10\.0\.0\.5: skipped, not a public address/);
  assert.match(r.out, /192\.168\.0\.1: skipped/);
});

test("a host Shodan has not seen is an answer that does not claim the host is closed", async () => {
  const { fetch, asked } = fakeFetch({});
  const r = await capture(["host", "1.2.3.4"], { fetch });
  assert.equal(r.code, 0);
  assert.deepEqual(asked, [`${INTERNETDB}/1.2.3.4`]);
  assert.match(r.out, /no data on this address .*does not mean nothing listens/);
});

test("--cves asks CVEDB once per versioned product and ranks KEV, then EPSS, then CVSS", async () => {
  const host = { ip: "1.2.3.4", ports: [22, 80], hostnames: ["a.example"], tags: [], cpes: ["cpe:/a:apache:http_server:2.4.7", "cpe:/o:canonical:ubuntu_linux"], vulns: ["CVE-1", "CVE-2", "CVE-3", "CVE-4"] };
  const cves = [
    { cve_id: "CVE-1", cvss: 9.8, epss: 0.1, kev: false, summary: "high cvss" },
    { cve_id: "CVE-2", cvss: 7.5, epss: 0.2, kev: true, summary: "exploited" },
    { cve_id: "CVE-3", cvss: 5, epss: 0.9, kev: false, summary: "likely" },
    { cve_id: "CVE-9", cvss: 10, epss: 1, kev: true, summary: "not on this host" },
  ];
  const { fetch, asked } = fakeFetch({ [`${INTERNETDB}/`]: json(200, host), [`${CVEDB}/cves?`]: json(200, { cves }) });
  const r = await capture(["host", "1.2.3.4", "--cves", "--top", "2", "--json"], { fetch });
  assert.equal(r.code, 0);
  const cvedb = asked.filter((u) => u.startsWith(CVEDB));
  assert.equal(cvedb.length, 1, "the unversioned OS CPE is not looked up");
  assert.match(cvedb[0], /cpe23=cpe%3A2\.3%3Aa%3Aapache%3Ahttp_server%3A2\.4\.7/);
  assert.match(cvedb[0], /sort_by_epss=true/);
  const [res] = JSON.parse(r.out);
  assert.deepEqual(res.cves.map((c) => c.cve_id), ["CVE-2", "CVE-3"]);
  assert.deepEqual(res.unscored, ["CVE-1", "CVE-4"]);
});

test("a rate limit is retried with backoff and a lasting failure exits 2", async () => {
  let n = 0;
  const flaky = async () => (++n < 3 ? json(429, {}) : json(200, { ok: true }));
  assert.deepEqual(await getJSON("https://x", { fetch: flaky, wait: async () => {} }), { status: 200, body: { ok: true } });
  assert.equal(n, 3);

  const { fetch } = fakeFetch({ [`${CVEDB}/cve/`]: json(503, {}) });
  const r = await capture(["cve", "CVE-2021-44228"], { fetch });
  assert.equal(r.code, 2);
  assert.match(r.err, /lookup failed: .*HTTP 503/);
});

test("command-line mistakes exit 1 with the usage and send nothing", async () => {
  const { fetch, asked } = fakeFetch({});
  for (const argv of [[], ["bogus"], ["cve", "log4shell"], ["cves"], ["cves", "--product", "a", "--cpe", "cpe:2.3:a:b:c:1"], ["cves", "--cpe", "nginx"], ["cves", "--product", "x", "--since", "May"], ["host"], ["host", "--nope"], ["cpes", "nginx", "--limit"]]) {
    const r = await capture(argv, { fetch });
    assert.equal(r.code, 1, argv.join(" "));
    assert.match(r.err + r.out, /usage:/, argv.join(" "));
  }
  assert.deepEqual(asked, []);
});

test("cves builds CVEDB's query with a small default limit", () => {
  const a = parseArgs(["cves", "--product", "nginx", "--kev", "--epss", "--since", "2025-01-01"]);
  const q = new URLSearchParams(cvesQuery(a.opts, a.flags));
  assert.equal(q.get("product"), "nginx");
  assert.equal(q.get("is_kev"), "true");
  assert.equal(q.get("sort_by_epss"), "true");
  assert.equal(q.get("start_date"), "2025-01-01");
  assert.equal(q.get("limit"), "20");
});

test("CPE names convert only when they carry a version", () => {
  assert.equal(toCPE23("cpe:/a:openbsd:openssh:6.6.1p1"), "cpe:2.3:a:openbsd:openssh:6.6.1p1");
  assert.equal(toCPE23("cpe:/o:canonical:ubuntu_linux"), "");
  assert.equal(toCPE23("cpe:/a:cloudflare:cloudflare"), "");
});

test("ranking puts records without scores last", () => {
  const list = [{ cve_id: "a" }, { cve_id: "b", epss: 0.5 }, { cve_id: "c", kev: true, epss: 0.01 }];
  assert.deepEqual(list.sort(cveRank).map((c) => c.cve_id), ["c", "b", "a"]);
});
