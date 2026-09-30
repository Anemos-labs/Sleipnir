'use strict';

const test = require('node:test');
const assert = require('node:assert');
const { EventEmitter } = require('../src/emitter');

test('on and once registrations are called in the order in which they were added', () => {
  const e = new EventEmitter();
  const calls = [];
  e.once('x', () => calls.push('1'));
  e.on('x', () => calls.push('2'));
  e.once('x', () => calls.push('3'));
  e.once('x', () => calls.push('4'));
  e.on('x', () => calls.push('5'));
  e.emit('x');
  assert.deepStrictEqual(calls, ['1', '2', '3', '4', '5']);
  calls.length = 0;
  e.emit('x');
  assert.deepStrictEqual(calls, ['2', '5']);
  assert.strictEqual(e.listenerCount('x'), 2);
});

test('a listener added again after the once listeners fired goes to the end', () => {
  const e = new EventEmitter();
  const calls = [];
  const a = () => calls.push('a');
  e.once('x', a);
  e.on('x', () => calls.push('b'));
  e.emit('x');
  e.once('x', a);
  e.emit('x');
  assert.deepStrictEqual(calls, ['a', 'b', 'b', 'a']);
});

test('the order is kept per event', () => {
  const e = new EventEmitter();
  const calls = [];
  e.on('x', () => calls.push('x1'));
  e.once('y', () => calls.push('y1'));
  e.once('x', () => calls.push('x2'));
  e.on('y', () => calls.push('y2'));
  e.emit('y');
  e.emit('x');
  assert.deepStrictEqual(calls, ['y1', 'y2', 'x1', 'x2']);
});

test('a once listener registered first runs before an on listener registered later', () => {
  const e = new EventEmitter();
  const calls = [];
  e.once('x', () => calls.push('once'));
  e.on('x', () => calls.push('on'));
  e.emit('x');
  assert.deepStrictEqual(calls, ['once', 'on']);
});
