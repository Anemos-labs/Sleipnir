'use strict';

const test = require('node:test');
const assert = require('node:assert');
const { EventEmitter } = require('../src/emitter');

function thrownBy(fn) {
  try {
    fn();
  } catch (thrown) {
    return { threw: true, thrown };
  }
  return { threw: false };
}

test('an error event without listeners throws the error object itself', () => {
  const e = new EventEmitter();
  const err = new TypeError('bad');
  const result = thrownBy(() => e.emit('error', err));
  assert.strictEqual(result.threw, true);
  assert.strictEqual(result.thrown, err);
});

test('only the first argument of an unhandled error event is thrown', () => {
  const e = new EventEmitter();
  const err = new RangeError('first');
  const result = thrownBy(() => e.emit('error', err, new Error('second'), 3));
  assert.strictEqual(result.thrown, err);
});

test('an unhandled error event whose first argument is not an Error throws an Error "Unhandled error."', () => {
  const e = new EventEmitter();
  for (const args of [[], ['boom'], [42], [null], [undefined], [{ code: 'E_X' }], [{ message: 'looks like an error' }]]) {
    const result = thrownBy(() => e.emit('error', ...args));
    assert.strictEqual(result.threw, true, `args ${JSON.stringify(args)}`);
    assert.ok(result.thrown instanceof Error, `args ${JSON.stringify(args)}`);
    assert.match(result.thrown.message, /^Unhandled error\./, `args ${JSON.stringify(args)}`);
  }
});

test('with a listener an error event is an ordinary event', () => {
  const e = new EventEmitter();
  const seen = [];
  e.on('error', (err, extra) => seen.push([err, extra]));
  const err = new Error('boom');
  assert.strictEqual(e.emit('error', err, 'more'), true);
  assert.deepStrictEqual(seen, [[err, 'more']]);
  assert.strictEqual(e.emit('error', 'not an error object'), true);
  assert.strictEqual(seen.length, 2);
});

test('a listener that removes itself still counts as a listener for the emit in progress', () => {
  const e = new EventEmitter();
  const handler = () => e.off('error', handler);
  e.on('error', handler);
  assert.strictEqual(e.emit('error', new Error('handled')), true);
  const err = new Error('unhandled now');
  assert.strictEqual(thrownBy(() => e.emit('error', err)).thrown, err);
});

test('after the last error listener is removed the error event throws again', () => {
  const e = new EventEmitter();
  const handler = () => {};
  e.on('error', handler);
  assert.strictEqual(e.emit('error', new Error('x')), true);
  e.off('error', handler);
  const err = new Error('unhandled');
  assert.strictEqual(thrownBy(() => e.emit('error', err)).thrown, err);
});

test('a once error listener handles only the first error', () => {
  const e = new EventEmitter();
  const seen = [];
  e.once('error', (err) => seen.push(err));
  const first = new Error('first');
  const second = new Error('second');
  assert.strictEqual(e.emit('error', first), true);
  assert.strictEqual(thrownBy(() => e.emit('error', second)).thrown, second);
  assert.deepStrictEqual(seen, [first]);
});

test('other events without listeners do not throw, and the name Error is not special', () => {
  const e = new EventEmitter();
  assert.strictEqual(e.emit('Error', new Error('x')), false);
  assert.strictEqual(e.emit('errors', new Error('x')), false);
  assert.strictEqual(e.emit('failure', new Error('x')), false);
  assert.strictEqual(e.emit(Symbol('error'), new Error('x')), false);
});

test('an exception thrown by an error listener propagates like any other', () => {
  const e = new EventEmitter();
  const inner = new Error('handler failed');
  e.on('error', () => { throw inner; });
  assert.strictEqual(thrownBy(() => e.emit('error', new Error('original'))).thrown, inner);
});
