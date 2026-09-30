'use strict';

const test = require('node:test');
const assert = require('node:assert');
const { EventEmitter } = require('../src/emitter');

test('on, once and off return the emitter so calls can be chained', () => {
  const e = new EventEmitter();
  const f = () => {};
  assert.strictEqual(e.on('x', f), e);
  assert.strictEqual(e.once('x', f), e);
  assert.strictEqual(e.off('x', f), e);
  assert.strictEqual(e.off('never', f), e);
  assert.strictEqual(e.on('a', f).once('b', f).off('a', f), e);
});

test('listeners get every argument and the emitter as this', () => {
  const e = new EventEmitter();
  const seen = [];
  e.on('x', function (...args) { seen.push([this, ...args]); });
  e.once('x', function (...args) { seen.push([this, ...args]); });
  e.emit('x', 1, 'two', { three: 3 });
  assert.strictEqual(seen.length, 2);
  for (const [self, ...args] of seen) {
    assert.strictEqual(self, e);
    assert.deepStrictEqual(args, [1, 'two', { three: 3 }]);
  }
});

test('emit returns whether the event had listeners', () => {
  const e = new EventEmitter();
  assert.strictEqual(e.emit('x'), false);
  e.on('x', () => {});
  assert.strictEqual(e.emit('x'), true);
  assert.strictEqual(e.emit('y'), false);
  e.once('y', () => {});
  assert.strictEqual(e.emit('y'), true);
  assert.strictEqual(e.emit('y'), false);
});

test('events are compared by identity: strings are case sensitive and symbols are their own events', () => {
  const e = new EventEmitter();
  const sym = Symbol('x');
  const calls = [];
  e.on('a', () => calls.push('a'));
  e.on('A', () => calls.push('A'));
  e.on(sym, () => calls.push('sym'));
  e.emit('a');
  e.emit(sym);
  e.emit(Symbol('x'));
  e.emit('A');
  assert.deepStrictEqual(calls, ['a', 'sym', 'A']);
});

test('a listener that is not a function is a TypeError', () => {
  const e = new EventEmitter();
  for (const bad of [undefined, null, 42, 'fn', {}, []]) {
    assert.throws(() => e.on('x', bad), TypeError);
    assert.throws(() => e.once('x', bad), TypeError);
    assert.throws(() => e.off('x', bad), TypeError);
  }
  assert.strictEqual(e.listenerCount('x'), 0);
});

test('the same function can be registered several times and every registration is called', () => {
  const e = new EventEmitter();
  let count = 0;
  const f = () => { count += 1; };
  e.on('x', f);
  e.on('x', f);
  e.once('x', f);
  assert.strictEqual(e.listenerCount('x'), 3);
  e.emit('x');
  assert.strictEqual(count, 3);
  e.emit('x');
  assert.strictEqual(count, 5);
});

test('off removes one registration, the most recently added one', () => {
  const e = new EventEmitter();
  const calls = [];
  const a = () => calls.push('a');
  const b = () => calls.push('b');
  e.on('x', a);
  e.on('x', b);
  e.on('x', a);
  e.off('x', a);
  assert.strictEqual(e.listenerCount('x'), 2);
  e.emit('x');
  assert.deepStrictEqual(calls, ['a', 'b']);
  e.off('x', a);
  e.off('x', a);
  e.emit('x');
  assert.deepStrictEqual(calls, ['a', 'b', 'b']);
});

test('off for an unknown listener or event does nothing', () => {
  const e = new EventEmitter();
  const calls = [];
  e.on('x', () => calls.push('x'));
  e.off('x', () => {});
  e.off('other', () => {});
  e.emit('x');
  assert.deepStrictEqual(calls, ['x']);
  assert.strictEqual(e.listenerCount('x'), 1);
});

test('listenerCount counts on and once registrations', () => {
  const e = new EventEmitter();
  assert.strictEqual(e.listenerCount('x'), 0);
  e.on('x', () => {});
  e.once('x', () => {});
  e.on('y', () => {});
  assert.strictEqual(e.listenerCount('x'), 2);
  assert.strictEqual(e.listenerCount('y'), 1);
  e.emit('x');
  assert.strictEqual(e.listenerCount('x'), 1);
});

test('an exception from a listener propagates and stops the emit', () => {
  const e = new EventEmitter();
  const calls = [];
  const err = new Error('listener failed');
  e.on('x', () => calls.push('first'));
  e.on('x', () => { throw err; });
  e.on('x', () => calls.push('third'));
  assert.throws(() => e.emit('x'), (thrown) => thrown === err);
  assert.deepStrictEqual(calls, ['first']);
});

test('it is implemented from scratch and does not extend Node\'s EventEmitter', () => {
  const NodeEmitter = require('node:events');
  assert.ok(!(new EventEmitter() instanceof NodeEmitter));
});

test('the README examples', () => {
  const log = [];
  const e = new EventEmitter();
  const a = () => log.push('a');
  const b = () => log.push('b');
  e.on('x', a);
  e.once('x', b);
  e.on('x', () => log.push('c'));
  assert.strictEqual(e.emit('x'), true);
  assert.deepStrictEqual(log, ['a', 'b', 'c']);
  assert.strictEqual(e.emit('x'), true);
  assert.deepStrictEqual(log, ['a', 'b', 'c', 'a', 'c']);

  const e2 = new EventEmitter();
  const calls = [];
  const second = () => calls.push('second');
  e2.on('x', () => { calls.push('first'); e2.off('x', second); });
  e2.on('x', second);
  e2.on('x', () => calls.push('third'));
  e2.emit('x');
  assert.deepStrictEqual(calls, ['first', 'second', 'third']);
  e2.emit('x');
  assert.deepStrictEqual(calls, ['first', 'second', 'third', 'first', 'third']);
});
