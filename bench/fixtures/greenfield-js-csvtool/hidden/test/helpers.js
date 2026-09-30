'use strict';
// Helpers of the acceptance tests for csvtool: run the tool, compare everything it does.

const test = require('node:test');
const assert = require('node:assert');
const { spawn } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const CLI = path.join(__dirname, '..', 'bin', 'csvtool.js');

/** Run the tool: resolves to { status, stdout, stderr }. */
function run(args, input = '') {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [CLI, ...args], { stdio: ['pipe', 'pipe', 'pipe'] });
    const out = [];
    const err = [];
    const timer = setTimeout(() => {
      child.kill('SIGKILL');
      reject(new Error(`timed out: csvtool ${JSON.stringify(args)}`));
    }, 60000);
    child.stdout.on('data', (d) => out.push(d));
    child.stderr.on('data', (d) => err.push(d));
    child.stdin.on('error', () => {}); // the tool may exit without reading its input
    child.on('error', (e) => {
      clearTimeout(timer);
      reject(e);
    });
    child.on('close', (status) => {
      clearTimeout(timer);
      resolve({ status, stdout: Buffer.concat(out).toString('utf8'), stderr: Buffer.concat(err).toString('utf8') });
    });
    child.stdin.end(input);
  });
}

/** The tool succeeds: status 0, nothing on standard error, exactly `stdout` on standard output. */
async function ok(args, input, stdout) {
  const got = await run(args, input);
  assert.deepStrictEqual(got, { status: 0, stdout, stderr: '' }, `args ${JSON.stringify(args)} input ${JSON.stringify(input)}`);
}

/** The tool fails: nothing on standard output, exactly `stderr` on standard error, status `status`. */
async function fails(args, input, status, stderr) {
  const got = await run(args, input);
  assert.deepStrictEqual(got, { status, stdout: '', stderr }, `args ${JSON.stringify(args)} input ${JSON.stringify(input)}`);
}

/** One test per group and one subtest per case, run a few at a time; a failure does not hide the other cases. */
function group(name, cases, check) {
  test(name, { concurrency: 6 }, async (t) => {
    await Promise.all(cases.map((c) => t.test(c[0], () => check(...c.slice(1)))));
  });
}

const PEOPLE = 'name,age,city\nAnn,31,"Paris, FR"\nBob,25,Rome\n"Cy ""the Kid""",40,Oslo\nDee,25,Rome\n';
const ANN = 'Ann,31,"Paris, FR"\n';
const BOB = 'Bob,25,Rome\n';
const CY = '"Cy ""the Kid""",40,Oslo\n';
const DEE = 'Dee,25,Rome\n';
const PEOPLE_HEADER = 'name,age,city\n';

async function withFiles(files, body) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'csvtool-'));
  try {
    const paths = {};
    for (const [name, content] of Object.entries(files)) {
      paths[name] = path.join(dir, name);
      fs.writeFileSync(paths[name], content);
    }
    await body(paths, dir);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
}

module.exports = {
  CLI,
  run,
  ok,
  fails,
  group,
  withFiles,
  PEOPLE,
  ANN,
  BOB,
  CY,
  DEE,
  PEOPLE_HEADER,
};
