'use strict';

/**
 * Turn `text` into a URL slug. The exact rules are in README.md.
 *
 * @param {string} text
 * @param {{ separator?: string, maxLength?: number }} [options]
 * @returns {string}
 */
function slugify(text, options = {}) {
  throw new Error('slugify is not implemented yet');
}

module.exports = { slugify };
