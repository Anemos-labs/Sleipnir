'use strict';

const test = require('node:test');
const assert = require('node:assert');
const { EventEmitter } = require('../src/emitter');

test('calls the listeners in registration order with the arguments', () => {
  const e = new EventEmitter();
  const calls = [];
  e.on('x', (...args) => calls.push(['first', ...args]));
  e.on('x', (...args) => calls.push(['second', ...args]));
  assert.strictEqual(e.emit('x', 1, 2), true);
  assert.deepStrictEqual(calls, [['first', 1, 2], ['second', 1, 2]]);
});

test('emit returns false when nobody listens', () => {
  assert.strictEqual(new EventEmitter().emit('nothing'), false);
});

test('once listeners are called once', () => {
  const e = new EventEmitter();
  let count = 0;
  e.once('x', () => { count += 1; });
  e.emit('x');
  e.emit('x');
  assert.strictEqual(count, 1);
});

test('off removes a listener', () => {
  const e = new EventEmitter();
  let count = 0;
  const listener = () => { count += 1; };
  e.on('x', listener);
  e.emit('x');
  e.off('x', listener);
  e.emit('x');
  assert.strictEqual(count, 1);
});

test('on and once listeners run in the order in which they were registered', () => {
  const e = new EventEmitter();
  const calls = [];
  e.on('x', () => calls.push('a'));
  e.once('x', () => calls.push('b'));
  e.on('x', () => calls.push('c'));
  e.emit('x');
  e.emit('x');
  assert.deepStrictEqual(calls, ['a', 'b', 'c', 'a', 'c']);
});

test('a listener that removes itself does not make the next listener be skipped', () => {
  const e = new EventEmitter();
  const calls = [];
  const first = () => {
    calls.push('first');
    e.off('x', first);
  };
  e.on('x', first);
  e.on('x', () => calls.push('second'));
  e.on('x', () => calls.push('third'));
  e.emit('x');
  assert.deepStrictEqual(calls, ['first', 'second', 'third']);
});

test('emit("error") without an error listener throws the error', () => {
  const e = new EventEmitter();
  const err = new Error('boom');
  assert.throws(() => e.emit('error', err), (thrown) => thrown === err);
});
