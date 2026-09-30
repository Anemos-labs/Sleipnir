'use strict';

const { CsvError } = require('./errors');

/**
 * Parse CSV text into an array of records. The exact rules are in README.md.
 *
 * @param {string} text
 * @param {{ delimiter?: string, header?: boolean, ragged?: 'error' | 'pad' | 'keep' }} [options]
 * @returns {string[][] | object[]}
 */
function parse(text, options = {}) {
  throw new Error('parse is not implemented yet');
}

module.exports = { parse };
