// steady-refresh.test.mjs: a view that polls must not repaint what did not change. A refresh that rebuilds a panel with innerHTML
// replaces every node in it: an open fold closes, a scrolled list jumps back to its top, focus and a text selection are lost, and the
// browser paints the panel again, which reads as a flash every few seconds. Each view below is booted on the fake DOM of pagedom.mjs
// with a large scan (the size of the ones that broke the page: 1739 points, 791 files, 103 leads), refreshed with the same data and
// with data that moved on, and checked node by node: what is still on the screen must be the same element, with the folds the user
// opened still open. Add a view here when it gains a poll.
import test from 'node:test';
import assert from 'node:assert/strict';
import { boot, LOG } from './pagedom.mjs';

const LANES = ['entry', 'boundary', 'access', 'sink', 'config'];
const STATUSES = ['confirmed', 'unproven', 'open', 'rejected'];
const SEVS = ['critical', 'high', 'medium', 'low'];

/** A large scan as the server would give it at tick t: as t grows, shards are mapped and hunted, points, leads and problems are added. */
function scan(t) {
  const nShards = 400, nPoints = 1739 + t * 7, nLeads = 103 + t * 2;
  const shards = Array.from({ length: nShards }, (_, i) => ({ id: 's' + String(i).padStart(3, '0'), first: 'pkg/f' + i + '.go', files: 2, funcs: 18, lines: 660,
    claimed: i < 300 + t * 10 ? 'w' + (i % 24) : '', map: i < 250 + t * 10 ? 'reads input ' + i : '', hunt: i < 120 + t * 10 }));
  const points = Array.from({ length: nPoints }, (_, i) => ({ id: 'p' + i, lane: LANES[i % 5], file: 'pkg/f' + (i % 791) + '.go', line: i % 900, note: 'point ' + i }));
  const findings = Array.from({ length: nLeads }, (_, i) => ({ id: 'L' + i, title: 'lead ' + i, category: 'injection', severity: SEVS[i % 4], status: STATUSES[i % 4],
    verdicts: [{ confirm: true }, { confirm: i % 3 > 0 }], trace: [{ file: 'pkg/f' + i + '.go', line: 10 + i }] }));
  const problems = Array.from({ length: 6 + t }, (_, i) => ({ phase: i % 2 ? 'hunt' : 'map', text: 'shard s' + i + ' has no map yet' }));
  const counts = { files: 791, funcs: 7459, lines: 264518, points: nPoints, edges: 365 + t, leads: nLeads, confirmed: 94, reproduced: 47 };
  const run = { slug: 'scan-20261030-2033-66434ce', commit: '66434ce30e9f', depth: 'deep', active: true, phase: 'hunt', team: 32, workers: 24, files: 791, funcs: 7459,
    shards: nShards, mapped: 250 + t * 10, hunted: 120 + t * 10, leads: nLeads, open: 9, confirmed: 94, created: 2, bySeverity: { critical: 3, high: 20 + t } };
  const older = { slug: 'scan-20261001-0900-1111111', commit: '111111111111', depth: 'standard', phase: 'done', team: 16, files: 780, funcs: 7400, shards: 380, mapped: 380,
    hunted: 380, leads: 60, open: 0, confirmed: 50, created: 1 };
  const detail = Object.assign({}, run, { counts, shards, points, findings, problems, moreProblems: t, verifiers: 3, defender: true, proveFrom: 'medium', sandbox: true,
    reportDir: '/home/u/.sleipnir/scans/' + run.slug });
  const compare = { old: older.slug, oldCommit: older.commit, newCommit: run.commit, final: false, counts: { new: 40 + t, unchanged: 50, fixed: 3 },
    deltas: findings.slice(0, 40 + t).map(f => ({ kind: 'new', finding: f })).concat(findings.slice(50, 100).map(f => ({ kind: 'unchanged', finding: f }))) };
  return { runs: { runs: [run, older], on: true, sandbox: true, depths: [{ id: 'standard', title: 'Standard', about: 'x', shardFuncs: 40, shardLines: 1500, verifiers: 2 }] }, detail, compare };
}

const elements = root => { const out = []; const walk = n => { for (const c of n.childNodes) if (c.nodeType === 1) { out.push(c); walk(c); } }; walk(root); return out; };

const VIEWS = [{
  name: 'scan', every: 5000,
  route: tick => (m, p) => {
    const s = scan(tick());
    if (p === '/api/sessions/a/scan') return s.runs;
    if (/^\/api\/sessions\/a\/scan\/[^/]+\/compare/.test(p)) return s.compare;
    if (/^\/api\/sessions\/a\/scan\/[^/?]+$/.test(p)) return s.detail;
    if (p === '/api/sandbox') return { docker: true };
    return undefined;
  },
  root: L => { let n = L.$('.sc-list'); while (n && !(n.querySelector('.sc-surface') && n.querySelector('.sc-detail'))) n = n.parentNode; return n; },
  // the folds a user opens and expects to find open after a refresh
  open: L => [L.$('.sc-needs'), L.$('.sc-more'), L.$('details.sc-group')],
  // rows that stay on the screen as the scan goes on (the report's index shows the findings, confirmed and unproven, at first)
  rows: L => [L.$('[data-scan="scan-20261030-2033-66434ce"]'), L.$('[data-lead="L0"]'), L.$('[data-lead="L101"]'), L.$('.sc-pt'), L.$('.sc-cell')],
}];

for (const v of VIEWS) {
  test(v.name + ': a refresh keeps every element that is still on the screen, and the folds the user opened', async () => {
    let t = 0;
    const L = await boot({ route: v.route(() => t) });
    L.SL.views.show(v.name); await L.settle(); L.advance(100); await L.settle();
    const root = v.root(L);
    const folds = v.open(L), rows = v.rows(L);
    folds.forEach((f, i) => { assert.ok(f, v.name + ': fold ' + i + ' is drawn'); f.open = true; });
    rows.forEach((r, i) => assert.ok(r, v.name + ': row ' + i + ' is drawn'));
    const before = elements(root);
    assert.ok(before.length > 3000, v.name + ': the large scan is drawn (' + before.length + ' elements)');

    // the same data: nothing is touched
    LOG.rows = [];
    L.advance(v.every); await L.settle(); L.advance(100); await L.settle();
    assert.deepEqual(before.filter(e => !e.isConnected).map(e => e.localName + '.' + e.className).slice(0, 5), [], v.name + ': the same data replaced elements');
    assert.deepEqual(LOG.rows.filter(r => / html$/.test(r)), [], v.name + ': the same data rebuilt markup');

    // the scan moves on, three polls in a row: counts go up, leads, points and problems are added
    for (t = 1; t <= 3; t++) {
      LOG.rows = [];
      L.advance(v.every); await L.settle(); L.advance(100); await L.settle();
      assert.deepEqual(LOG.rows.filter(r => / html$/.test(r)), [], v.name + ': a refresh at tick ' + t + ' rebuilt markup with innerHTML');
      folds.forEach((f, i) => { assert.ok(f.isConnected, v.name + ': fold ' + i + ' was replaced at tick ' + t); assert.equal(f.open, true, v.name + ': fold ' + i + ' closed at tick ' + t); });
      rows.forEach((r, i) => assert.ok(r.isConnected, v.name + ': row ' + i + ' was replaced at tick ' + t));
    }
    const gone = before.filter(e => !e.isConnected);
    assert.ok(gone.length === 0, v.name + ': ' + gone.length + ' of ' + before.length + ' elements were replaced, e.g. ' + gone.slice(0, 3).map(e => e.localName + '.' + e.className).join(', '));
    assert.match(L.$('.sc-needs summary').textContent, /What the scan still needs 12/, 'the refreshed data is drawn');
    assert.equal(L.doc.querySelectorAll('.sc-pt').length, 1739 + 21);
    assert.deepEqual(LOG.errors, []);
  });
}
