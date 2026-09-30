'use strict';

/**
 * Write rows as CSV text. The exact rules are in README.md.
 *
 * @param {Array<Array<unknown> | object>} rows
 * @param {{ delimiter?: string, newline?: '\r\n' | '\n', columns?: string[] }} [options]
 * @returns {string}
 */
function stringify(rows, options = {}) {
  throw new Error('stringify is not implemented yet');
}

module.exports = { stringify };
