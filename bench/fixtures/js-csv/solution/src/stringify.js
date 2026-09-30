'use strict';

const { checkDelimiter, checkNewline } = require('./options');

function text(cell) {
  return cell === null || cell === undefined ? '' : String(cell);
}

function field(value, delimiter, alone) {
  const needsQuotes = value === '' ? alone : value.includes(delimiter) || /["\r\n]/.test(value);
  return needsQuotes ? `"${value.replace(/"/g, '""')}"` : value;
}

function stringify(rows, options = {}) {
  const { delimiter = ',', newline = '\r\n', columns } = options;
  checkDelimiter(delimiter);
  checkNewline(newline);
  if (!Array.isArray(rows)) {
    throw new TypeError('rows must be an array');
  }

  const records = [];
  if (columns !== undefined) {
    if (!Array.isArray(columns)) {
      throw new TypeError('columns must be an array');
    }
    records.push(columns);
    for (const row of rows) {
      if (row === null || typeof row !== 'object' || Array.isArray(row)) {
        throw new TypeError('with columns every row must be an object');
      }
      records.push(columns.map((name) => (Object.hasOwn(row, name) ? row[name] : undefined)));
    }
  } else {
    for (const row of rows) {
      if (!Array.isArray(row)) {
        throw new TypeError('every row must be an array');
      }
      records.push(row);
    }
  }

  let out = '';
  for (const record of records) {
    if (record.length === 0) {
      throw new RangeError('a record needs at least one cell');
    }
    out += record.map((cell) => field(text(cell), delimiter, record.length === 1)).join(delimiter) + newline;
  }
  return out;
}

module.exports = { stringify };
