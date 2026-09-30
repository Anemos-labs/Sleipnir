'use strict';

/**
 * A malformed CSV text, or a record that breaks the `ragged` policy.
 * `record` is the 1-based number of the record in which the problem was found (see README.md).
 */
class CsvError extends Error {
  constructor(message, record) {
    super(`record ${record}: ${message}`);
    this.name = 'CsvError';
    this.record = record;
  }
}

module.exports = { CsvError };
