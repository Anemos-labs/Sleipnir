'use strict';

const test = require('node:test');
const assert = require('node:assert');
const { EventEmitter } = require('../src/emitter');

test('off removes a listener that was registered with once', () => {
  const e = new EventEmitter();
  let count = 0;
  const f = () => { count += 1; };
  e.once('x', f);
  assert.strictEqual(e.listenerCount('x'), 1);
  e.off('x', f);
  assert.strictEqual(e.listenerCount('x'), 0);
  assert.strictEqual(e.emit('x'), false);
  assert.strictEqual(count, 0);
});

test('off removes the most recent registration when a function is on and once', () => {
  const e = new EventEmitter();
  const calls = [];
  const f = () => calls.push('f');

  e.on('x', f);
  e.once('x', f);
  e.off('x', f); // the once registration was added last
  e.emit('x');
  e.emit('x');
  assert.deepStrictEqual(calls, ['f', 'f']);
  e.off('x', f);

  calls.length = 0;
  e.once('x', f);
  e.on('x', f);
  e.off('x', f); // now the on registration was added last
  e.emit('x');
  e.emit('x');
  assert.deepStrictEqual(calls, ['f']);
});

test('the same function registered twice with once is called once per registration', () => {
  const e = new EventEmitter();
  let count = 0;
  const f = () => { count += 1; };
  e.once('x', f);
  e.once('x', f);
  e.emit('x');
  assert.strictEqual(count, 2);
  e.emit('x');
  assert.strictEqual(count, 2);
});

test('a once listener is already removed while it runs', () => {
  const e = new EventEmitter();
  let inside;
  e.once('x', () => { inside = e.listenerCount('x'); });
  e.emit('x');
  assert.strictEqual(inside, 0);
});

test('a nested emit from a once listener does not call it again', () => {
  const e = new EventEmitter();
  let count = 0;
  e.once('x', () => {
    count += 1;
    if (count < 5) {
      e.emit('x');
    }
  });
  e.emit('x');
  assert.strictEqual(count, 1);
});

test('a once listener that an inner emit has called is not called again by the outer emit', () => {
  const e = new EventEmitter();
  const calls = [];
  let inner = false;
  e.on('x', () => {
    calls.push('a');
    if (!inner) {
      inner = true;
      e.emit('x');
    }
  });
  e.once('x', () => calls.push('once'));
  e.emit('x');
  assert.deepStrictEqual(calls, ['a', 'a', 'once']);
  assert.strictEqual(e.listenerCount('x'), 1);
});

test('a once listener that was not reached because of an exception stays registered', () => {
  const e = new EventEmitter();
  const calls = [];
  e.once('x', () => { calls.push('thrower'); throw new Error('stop'); });
  e.once('x', () => calls.push('second'));
  e.on('x', () => calls.push('third'));
  assert.throws(() => e.emit('x'), /stop/);
  assert.deepStrictEqual(calls, ['thrower']);
  assert.strictEqual(e.listenerCount('x'), 2);
  e.emit('x');
  assert.deepStrictEqual(calls, ['thrower', 'second', 'third']);
  assert.strictEqual(e.listenerCount('x'), 1);
});

test('a once listener removed with off by an earlier listener is still called by that emit, but not by later ones', () => {
  const e = new EventEmitter();
  const calls = [];
  const once = () => calls.push('once');
  e.on('x', () => e.off('x', once));
  e.once('x', once);
  e.emit('x');
  assert.deepStrictEqual(calls, ['once']);
  e.emit('x');
  assert.deepStrictEqual(calls, ['once']);
});
