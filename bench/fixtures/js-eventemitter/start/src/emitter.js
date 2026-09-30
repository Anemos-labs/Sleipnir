'use strict';

function assertFunction(listener) {
  if (typeof listener !== 'function') {
    throw new TypeError('listener must be a function');
  }
}

function push(map, event, listener) {
  const list = map.get(event);
  if (list) {
    list.push(listener);
  } else {
    map.set(event, [listener]);
  }
}

class EventEmitter {
  constructor() {
    this._on = new Map(); // event -> listeners added with on()
    this._once = new Map(); // event -> listeners added with once()
  }

  on(event, listener) {
    assertFunction(listener);
    push(this._on, event, listener);
    return this;
  }

  once(event, listener) {
    assertFunction(listener);
    push(this._once, event, listener);
    return this;
  }

  off(event, listener) {
    assertFunction(listener);
    const list = this._on.get(event);
    if (list) {
      const i = list.lastIndexOf(listener);
      if (i !== -1) {
        list.splice(i, 1);
      }
    }
    return this;
  }

  emit(event, ...args) {
    const regular = this._on.get(event) || [];
    const once = this._once.get(event) || [];

    for (let i = 0; i < regular.length; i++) {
      regular[i].apply(this, args);
    }

    // once listeners are cleared first so that a nested emit cannot call them again
    this._once.delete(event);
    for (const listener of once) {
      listener.apply(this, args);
    }

    return regular.length + once.length > 0;
  }

  listenerCount(event) {
    return (this._on.get(event) || []).length + (this._once.get(event) || []).length;
  }
}

module.exports = { EventEmitter };
