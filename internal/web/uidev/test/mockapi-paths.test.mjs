// mockapi-paths.test.mjs: the development server answers a malformed address with a 400 and keeps serving.
import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { start } from '../mockapi.mjs';

const get = (url) => new Promise((resolve, reject) => http.get(url, r => { r.resume(); r.on('end', () => resolve(r.statusCode)); }).on('error', reject));

test('a path that does not decode is a 400, and the server keeps serving', async () => {
  const s = await start();
  try {
    assert.equal(await get(s.url + '%E0%A4%A'), 400);
    assert.equal(await get(s.url + 'index.html'), 200);
  } finally { s.close(); }
});
