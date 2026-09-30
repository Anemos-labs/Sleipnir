'use strict';

const { inspect } = require('node:util');

function assertFunction(listener) {
  if (typeof listener !== 'function') {
    throw new TypeError('listener must be a function');
  }
}

class EventEmitter {
  // event -> registrations in the order they were added: { listener, once, fired }
  #registrations = new Map();

  on(event, listener) {
    return this.#add(event, listener, false);
  }

  once(event, listener) {
    return this.#add(event, listener, true);
  }

  off(event, listener) {
    assertFunction(listener);
    const entries = this.#registrations.get(event);
    if (entries) {
      for (let i = entries.length - 1; i >= 0; i--) {
        if (entries[i].listener === listener) {
          this.#drop(event, entries[i]);
          break;
        }
      }
    }
    return this;
  }

  emit(event, ...args) {
    const entries = this.#registrations.get(event);
    if (!entries) {
      if (event === 'error') {
        throw args[0] instanceof Error ? args[0] : new Error(`Unhandled error. (${inspect(args[0])})`);
      }
      return false;
    }
    // The listeners of this emit are the ones registered now; changes made by the listeners
    // themselves only count for later emits.
    for (const entry of [...entries]) {
      if (entry.once) {
        if (entry.fired) {
          continue; // an inner emit has already called it
        }
        entry.fired = true;
        this.#drop(event, entry);
      }
      entry.listener.apply(this, args);
    }
    return true;
  }

  listenerCount(event) {
    const entries = this.#registrations.get(event);
    return entries ? entries.length : 0;
  }

  #add(event, listener, once) {
    assertFunction(listener);
    const entry = { listener, once, fired: false };
    const entries = this.#registrations.get(event);
    if (entries) {
      entries.push(entry);
    } else {
      this.#registrations.set(event, [entry]);
    }
    return this;
  }

  #drop(event, entry) {
    const entries = this.#registrations.get(event);
    const i = entries ? entries.indexOf(entry) : -1;
    if (i === -1) {
      return;
    }
    entries.splice(i, 1);
    if (entries.length === 0) {
      this.#registrations.delete(event);
    }
  }
}

module.exports = { EventEmitter };
