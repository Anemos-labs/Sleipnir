'use strict';

const { CsvError } = require('./errors');
const { checkDelimiter } = require('./options');

const RAGGED = new Set(['error', 'pad', 'keep']);

/** Yield the records of `text` one by one, as arrays of field strings. */
function* records(text, delimiter) {
  const end = text.length;
  const atLineBreak = (i) => text[i] === '\n' || (text[i] === '\r' && text[i + 1] === '\n');
  let i = 0;
  let number = 0;
  while (i < end) {
    if (atLineBreak(i)) {
      i += text[i] === '\r' ? 2 : 1; // a blank line
      continue;
    }
    number += 1;
    const fields = [];
    for (;;) {
      if (text[i] === '"') {
        let value = '';
        i += 1;
        for (;;) {
          const quote = text.indexOf('"', i);
          if (quote === -1) {
            throw new CsvError('quoted field is not closed', number);
          }
          value += text.slice(i, quote);
          if (text[quote + 1] === '"') {
            value += '"';
            i = quote + 2;
          } else {
            i = quote + 1;
            break;
          }
        }
        if (i < end && text[i] !== delimiter && !atLineBreak(i)) {
          throw new CsvError('unexpected character after the closing quote', number);
        }
        fields.push(value);
      } else {
        const start = i;
        while (i < end && text[i] !== delimiter && !atLineBreak(i)) {
          if (text[i] === '"') {
            throw new CsvError('double quote inside an unquoted field', number);
          }
          i += 1;
        }
        fields.push(text.slice(start, i));
      }
      if (i < end && text[i] === delimiter) {
        i += 1; // another field follows, even if it is empty and ends the text
        continue;
      }
      if (i < end) {
        i += text[i] === '\r' ? 2 : 1; // the line break that ends the record
      }
      break;
    }
    yield fields;
  }
}

function toObject(names, fields) {
  const object = {};
  for (let i = 0; i < names.length && i < fields.length; i++) {
    object[names[i]] = fields[i];
  }
  return object;
}

function parse(text, options = {}) {
  if (typeof text !== 'string') {
    throw new TypeError('text must be a string');
  }
  const { delimiter = ',', header = false, ragged = 'error' } = options;
  checkDelimiter(delimiter);
  if (!RAGGED.has(ragged)) {
    throw new RangeError("ragged must be 'error', 'pad' or 'keep'");
  }

  const rows = [];
  let names = null;
  let width = 0;
  let number = 0;
  for (const fields of records(text, delimiter)) {
    number += 1;
    if (number === 1) {
      width = fields.length;
      if (header) {
        names = fields;
        continue;
      }
    } else if (fields.length !== width) {
      if (ragged === 'error' || (ragged === 'pad' && fields.length > width)) {
        throw new CsvError(`expected ${width} fields but found ${fields.length}`, number);
      }
      if (ragged === 'pad') {
        while (fields.length < width) {
          fields.push('');
        }
      }
    }
    rows.push(header ? toObject(names, fields) : fields);
  }
  return rows;
}

module.exports = { parse };
