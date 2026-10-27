// parity-scenes.test.mjs: scripts/web-parity.mjs expands a scene's "each" lists only within its bound. An empty list makes the
// expansion empty before anything is built, so long lists before it cannot be multiplied into memory.
import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { loadScenes } from '../../../../scripts/web-parity.mjs';

function sceneFile(t, doc) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'parity-scenes-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const file = path.join(dir, 'scenes.json');
  fs.writeFileSync(file, JSON.stringify(doc));
  return file;
}

test('an empty list expands to nothing without building the lists before it', (t) => {
  const big = Array.from({ length: 3000 }, (_, i) => i);
  const file = sceneFile(t, { scenes: [{ name: 's-${a}-${b}-${c}', each: { a: big, b: big, c: [] } }, { name: 'plain' }] });
  const flatMap = Array.prototype.flatMap;
  let built = 0;
  Array.prototype.flatMap = function (...args) { built += this.length; return flatMap.apply(this, args); };
  try {
    assert.deepEqual(loadScenes(file).map(s => s.name), ['plain']);
  } finally {
    Array.prototype.flatMap = flatMap;
  }
  assert.equal(built, 0, 'no partial product was built');
});

test('a product past the bound, or an "each" that is not lists, is refused', (t) => {
  const big = Array.from({ length: 200 }, (_, i) => i);
  assert.throws(() => loadScenes(sceneFile(t, { scenes: [{ name: 's-${a}-${b}', each: { a: big, b: big } }] })), /more than 10000 scenes/);
  assert.throws(() => loadScenes(sceneFile(t, { scenes: [{ name: 's-${a}', each: { a: 'abc' } }] })), /not lists/);
  assert.equal(loadScenes(sceneFile(t, { scenes: [{ name: 's-${a}-${b}', each: { a: [1, 2], b: ['x', 'y', 'z'] } }] })).length, 6);
});
