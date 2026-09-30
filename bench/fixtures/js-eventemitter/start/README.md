# emitter

A small synchronous event emitter: a subset of Node's `events.EventEmitter`, implemented from
scratch (it does not use, extend or wrap `node:events`).

```js
const { EventEmitter } = require('./src/emitter');

const e = new EventEmitter();
e.on('greet', function (name) { console.log('hello', name); });
e.emit('greet', 'Ada');   // prints "hello Ada" and returns true
```

Run the tests from the repository root with `node --test test/*.test.js` (Node 22, no
dependencies).

An **event** is a string or a symbol; events are compared by identity, so `'a'` and `'A'` are
different events. A **registration** is one call to `on` or `once`.

## API

### `on(event, listener)` returns `this`

Adds a registration for `listener` at the end of the list of `event`. The same function may be
registered several times: every registration is called. Throws a `TypeError` if `listener` is not
a function.

### `once(event, listener)` returns `this`

Like `on`, but the listener is called at most once. The registration is removed right before the
listener is called for the first time, so inside the listener `listenerCount` no longer counts it
and a nested `emit` of the same event does not call it again. "At most once" also holds when emits
are nested: a `once` listener that an inner `emit` has already called is not called again when the
outer `emit` reaches it.

The registration is removed only when its turn comes. If an exception stops an `emit` before a
`once` listener is reached, that listener is still registered afterwards.

### `off(event, listener)` returns `this`

Removes **one** registration of `listener` for `event`: the most recently added one. A listener
added with `once(event, fn)` is removed by `off(event, fn)` (the function you pass is the function
you registered). Does nothing if there is no such registration. Throws a `TypeError` if `listener`
is not a function.

### `emit(event, ...args)` returns `boolean`

Calls the listeners of `event` in the order in which they were registered, `on` and `once`
registrations interleaved exactly as they were added, each with `args` and with `this` set to the
emitter. Returns `true` if the event had at least one listener when `emit` started, otherwise
`false`.

The listeners called by one `emit` are **fixed when it starts** (a snapshot of the registrations):

- A listener added while the emit is running is not called by that emit, only by later ones.
- A listener removed while the emit is running (with `off`, or because it is a `once` listener
  that has just fired) is not affected by that: if it has not been reached yet it is still called
  by this emit, and it is not called by later emits. Removing or firing a listener never makes
  another listener of the same emit get skipped or called twice.
- A nested `emit` (from inside a listener) takes its own snapshot when it starts.
- If a listener throws, the exception propagates out of `emit` and the listeners of that emit that
  have not been called yet are not called.

### `listenerCount(event)` returns a number

The number of registrations for `event`, `on` and `once` together.

### The `'error'` event

An `emit` of the event `'error'` that has no listeners when it starts does not return `false`, it
throws:

- if its first argument is an `Error` instance, that very object is thrown;
- otherwise an `Error` whose message starts with `Unhandled error.` is thrown (this includes
  `emit('error')` without arguments).

If `'error'` has at least one listener the emit is an ordinary one. Every other event (the name
`'Error'` included) with no listeners just returns `false`.

## Example

```js
const log = [];
const e = new EventEmitter();
const a = () => log.push('a');
const b = () => log.push('b');
e.on('x', a);
e.once('x', b);
e.on('x', () => log.push('c'));

e.emit('x');   // log: a b c   (true)
e.emit('x');   // log: a b c a c   (true)   b was a once listener
```

```js
// removing a listener while an emit is running
const e = new EventEmitter();
const calls = [];
const second = () => calls.push('second');
e.on('x', () => { calls.push('first'); e.off('x', second); });
e.on('x', second);
e.on('x', () => calls.push('third'));

e.emit('x');   // calls: first second third   (second was in the snapshot)
e.emit('x');   // calls: first second third first third
```
