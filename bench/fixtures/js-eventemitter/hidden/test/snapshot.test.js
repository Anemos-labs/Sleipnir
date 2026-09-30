'use strict';

const test = require('node:test');
const assert = require('node:assert');
const { EventEmitter } = require('../src/emitter');

test('a listener that removes itself does not make the next one be skipped', () => {
  const e = new EventEmitter();
  const calls = [];
  const a = () => { calls.push('a'); e.off('x', a); };
  e.on('x', a);
  e.on('x', () => calls.push('b'));
  e.on('x', () => calls.push('c'));
  e.emit('x');
  assert.deepStrictEqual(calls, ['a', 'b', 'c']);
  e.emit('x');
  assert.deepStrictEqual(calls, ['a', 'b', 'c', 'b', 'c']);
});

test('once listeners that fire do not make the next listeners be skipped', () => {
  const e = new EventEmitter();
  const calls = [];
  e.once('x', () => calls.push('a'));
  e.once('x', () => calls.push('b'));
  e.on('x', () => calls.push('c'));
  e.once('x', () => calls.push('d'));
  e.emit('x');
  assert.deepStrictEqual(calls, ['a', 'b', 'c', 'd']);
  e.emit('x');
  assert.deepStrictEqual(calls, ['a', 'b', 'c', 'd', 'c']);
});

test('a listener removed by an earlier listener is still called by the emit in progress', () => {
  const e = new EventEmitter();
  const calls = [];
  const second = () => calls.push('second');
  e.on('x', () => { calls.push('first'); e.off('x', second); });
  e.on('x', second);
  e.on('x', () => calls.push('third'));
  e.emit('x');
  assert.deepStrictEqual(calls, ['first', 'second', 'third']);
  e.emit('x');
  assert.deepStrictEqual(calls, ['first', 'second', 'third', 'first', 'third']);
});

test('removing an earlier listener during an emit changes nothing for that emit', () => {
  const e = new EventEmitter();
  const calls = [];
  const first = () => calls.push('first');
  e.on('x', first);
  e.on('x', () => { calls.push('second'); e.off('x', first); });
  e.on('x', () => calls.push('third'));
  e.emit('x');
  assert.deepStrictEqual(calls, ['first', 'second', 'third']);
  e.emit('x');
  assert.deepStrictEqual(calls, ['first', 'second', 'third', 'second', 'third']);
});

test('removing every listener during an emit: all are still called, then none', () => {
  const e = new EventEmitter();
  const calls = [];
  const a = () => { calls.push('a'); e.off('x', a); e.off('x', b); e.off('x', c); };
  const b = () => calls.push('b');
  const c = () => calls.push('c');
  e.on('x', a);
  e.on('x', b);
  e.on('x', c);
  assert.strictEqual(e.emit('x'), true);
  assert.deepStrictEqual(calls, ['a', 'b', 'c']);
  assert.strictEqual(e.listenerCount('x'), 0);
  assert.strictEqual(e.emit('x'), false);
  assert.deepStrictEqual(calls, ['a', 'b', 'c']);
});

test('emit returns true when the only listener removes itself', () => {
  const e = new EventEmitter();
  const a = () => e.off('x', a);
  e.on('x', a);
  assert.strictEqual(e.emit('x'), true);
  assert.strictEqual(e.emit('x'), false);
});

test('a listener added during an emit is not called by it', () => {
  const e = new EventEmitter();
  const calls = [];
  e.on('x', () => {
    calls.push('first');
    e.on('x', () => calls.push('added'));
    e.once('x', () => calls.push('added once'));
  });
  e.emit('x');
  assert.deepStrictEqual(calls, ['first']);
  e.emit('x');
  assert.deepStrictEqual(calls, ['first', 'first', 'added', 'added once']);
});

test('a listener added by a once listener is not called by the same emit', () => {
  const e = new EventEmitter();
  const calls = [];
  e.once('x', () => {
    calls.push('a');
    e.once('x', () => calls.push('b'));
  });
  e.emit('x');
  assert.deepStrictEqual(calls, ['a']);
  e.emit('x');
  assert.deepStrictEqual(calls, ['a', 'b']);
  e.emit('x');
  assert.deepStrictEqual(calls, ['a', 'b']);
});

test('a nested emit takes its own snapshot', () => {
  const e = new EventEmitter();
  const calls = [];
  let depth = 0;
  e.on('x', () => {
    calls.push(`first${depth}`);
    if (depth === 0) {
      depth = 1;
      e.emit('x');
      depth = 0;
    }
  });
  e.on('x', () => calls.push(`second${depth}`));
  e.emit('x');
  assert.deepStrictEqual(calls, ['first0', 'first1', 'second1', 'second0']);
});

test('a nested emit does not see listeners removed before it started', () => {
  const e = new EventEmitter();
  const calls = [];
  let nested = false;
  const b = () => calls.push('b');
  e.on('x', () => {
    calls.push('a');
    if (!nested) {
      nested = true;
      e.off('x', b);
      e.emit('x');
    }
  });
  e.on('x', b);
  e.emit('x');
  assert.deepStrictEqual(calls, ['a', 'a', 'b']);
});
