/* a new session starts with a short scripted run: the project survey (REAL recon), a plan, one worker */
S.newSessionScript = {
  note: 'what New session plays for any project: the recon survey of the sample shop (REAL), a plan of three steps, one worker and its first tool calls. Offsets in seconds after the session starts.',
  beats: [
    { at: 0.4, type: 'tool', agent: 'mgr', tool: 'recon', arg: 'survey the project', result: S.recon.footer, text: S.recon.budgets[5000].text },
    { at: 1.6, type: 'say', agent: 'mgr', text: 'The project is a Go module with a static page. I will plan before I hand anything out.' },
    { at: 2.2, type: 'plan', steps: ['Read the code the goal touches', 'Make the change with the smallest correct diff', 'Run the project\'s checks'] },
    { at: 3.4, type: 'tool', agent: 'mgr', tool: 'task', arg: 'create T1', result: 'T1 todo' },
    { at: 3.8, type: 'tool', agent: 'mgr', tool: 'spawn', arg: 'be-1 on T1', result: 'be-1 started (leg 1)' },
    { at: 4.6, type: 'state', agent: 'be-1', state: 'think', doing: 'reading the code the goal touches' },
    { at: 5.4, type: 'tool', agent: 'be-1', tool: 'read', arg: 'api/server.go', result: '41 lines' },
    { at: 6.5, type: 'state', agent: 'be-1', state: 'edit', doing: 'editing' },
  ],
};

/* ---- conveniences the brief lists by name ---------------------------------------------------------------------------------------------- */
S.sessions.recorded.concat(S.sessions.archive).forEach(function (r) {
  var age = (Date.parse(S.meta.sampleNow) - Date.parse(r.lastWritten)) / 1000;
  r.ageSeconds = Math.round(age); r.age = F.ageText(age); r.agoText = F.ago(age); r.sizeMB = Math.round(r.size / 1048576 * 10) / 10; r.older30d = age >= 30 * 86400;
});
S.schedule.jobs.forEach(function (j) { j.cwd = j.dir; j.status = j.lastExit || 'never run'; });
/** trustJson: the ledger file as the binary writes it (~/.sleipnir/trust.json, mode 0600; never inside a repository). The shop entry carries the real digest and hashes. */
S.trust.trustJson = { version: 1, projects: {
  '/home/ada/projects/handbook': { digest: 'sha256:6b0a1c9e44d27f3a8e5d01b97c2a4f6e3d8b1a0c5f7e9d2b4a6c8e0f1a3b5d7c', files: { 'AGENTS.md': '3f1d0a9b7c2e5841', '.sleipnir/config.json': 'a8c4e0129b6d3f75' }, saved: '2025-12-12' },
  '/home/ada/projects/old-prototype': { digest: 'sha256:0c9d8e7f6a5b4c3d2e1f0a9b8c7d6e5f4a3b2c1d0e9f8a7b6c5d4e3f2a1b0c9d', files: { 'AGENTS.md': '9e8d7c6b5a493827' }, saved: '2025-11-02' },
  '/home/ada/projects/orders-api': { digest: 'sha256:5a4b3c2d1e0f9a8b7c6d5e4f3a2b1c0d9e8f7a6b5c4d3e2f1a0b9c8d7e6f5a4b', files: { 'AGENTS.md': '7a6b5c4d3e2f1a09', '.sleipnir/config.json': 'd1c2b3a495867768' }, saved: '2025-12-30' },
  '/home/ada/projects/shop': { digest: S.trust.project.digestFull, files: S.trust.files.reduce(function (o, f) { o[f.path] = f.hash; return o; }, {}), saved: '2026-01-01' },
} };
S.trust.trustJsonNote = 'REAL format (internal/trust ledger.go): {version, projects:{dir:{digest, files{path: first 16 hex of its hash}, saved:day}}}; the shop entry is the real digest and hashes of the sample files; the other directories are SAMPLE';
