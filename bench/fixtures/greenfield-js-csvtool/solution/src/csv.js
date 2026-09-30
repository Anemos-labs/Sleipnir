'use strict';

const { failure } = require('./errors');

const malformed = () => failure('malformed CSV');

/** Parse CSV text into an array of records (arrays of strings); empty lines are skipped. */
function parseCsv(text) {
  const records = [];
  const n = text.length;
  // the line terminator at i ('\n' or '\r\n'): its length, or 0
  const eol = (i) => (text[i] === '\n' ? 1 : text[i] === '\r' && text[i + 1] === '\n' ? 2 : 0);
  let i = 0;
  while (i < n) {
    const blank = eol(i);
    if (blank) {
      i += blank;
      continue;
    }
    const fields = [];
    for (;;) {
      if (text[i] === '"') {
        let value = '';
        i++;
        for (;;) {
          if (i >= n) throw malformed();
          if (text[i] === '"') {
            if (text[i + 1] === '"') {
              value += '"';
              i += 2;
              continue;
            }
            i++;
            break;
          }
          value += text[i++];
        }
        if (i < n && text[i] !== ',' && !eol(i)) throw malformed();
        fields.push(value);
      } else {
        const start = i;
        while (i < n && text[i] !== ',' && !eol(i)) {
          if (text[i] === '"') throw malformed();
          i++;
        }
        fields.push(text.slice(start, i));
      }
      if (text[i] === ',') {
        i++; // another field follows, possibly empty (a trailing comma)
        continue;
      }
      i += eol(i); // end of the record (or of the input)
      break;
    }
    records.push(fields);
  }
  return records;
}

function formatField(field, alone) {
  if (/[",\r\n]/.test(field) || (alone && field === '')) return `"${field.replace(/"/g, '""')}"`;
  return field;
}

/** One record as a line of CSV, with its line terminator. */
function formatRecord(fields) {
  return fields.map((f) => formatField(f, fields.length === 1)).join(',') + '\n';
}

module.exports = { parseCsv, formatRecord };
