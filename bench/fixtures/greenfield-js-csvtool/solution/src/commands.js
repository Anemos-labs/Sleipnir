'use strict';

const { failure } = require('./errors');
const { isNumber, compareStrings, compareValues, formatNumber } = require('./values');

const OPERATORS = {
  '=': (c) => c === 0,
  '!=': (c) => c !== 0,
  '<': (c) => c < 0,
  '<=': (c) => c <= 0,
  '>': (c) => c > 0,
  '>=': (c) => c >= 0,
};

function columnIndex(header, name) {
  const i = header.indexOf(name);
  if (i < 0) throw failure(`unknown column: ${name}`);
  return i;
}

function notANumber(value) {
  return failure(`not a number: ${value}`);
}

/**
 * Each command takes the parsed table (`header` and `rows`), its own arguments and its options, and returns the records
 * to print. `arity` is the number of its own arguments; `options` lists the options it accepts.
 */
const commands = {
  head: {
    arity: 1,
    options: [],
    run({ header, rows }, [n]) {
      return [header, ...rows.slice(0, Number(n))];
    },
  },

  select: {
    arity: 1,
    options: [],
    run({ header, rows }, [cols]) {
      const picked = cols.split(',').map((name) => columnIndex(header, name));
      return [header, ...rows].map((record) => picked.map((i) => record[i]));
    },
  },

  filter: {
    arity: 3,
    options: [],
    run({ header, rows }, [col, op, value]) {
      const i = columnIndex(header, col);
      const keep = (cell) => (op === 'contains' ? cell.includes(value) : OPERATORS[op](compareValues(cell, value)));
      return [header, ...rows.filter((row) => keep(row[i]))];
    },
  },

  sort: {
    arity: 1,
    options: ['--desc', '--numeric'],
    run({ header, rows }, [col], options) {
      const i = columnIndex(header, col);
      const numeric = options.has('--numeric');
      if (numeric) {
        const bad = rows.find((row) => !isNumber(row[i]));
        if (bad) throw notANumber(bad[i]);
      }
      const sign = options.has('--desc') ? -1 : 1;
      const keyed = rows.map((row, index) => ({ row, index }));
      keyed.sort((a, b) => {
        const x = a.row[i];
        const y = b.row[i];
        const c = numeric ? Math.sign(Number(x) - Number(y)) : compareStrings(x, y);
        return sign * c || a.index - b.index; // ties keep the input order, in both directions
      });
      return [header, ...keyed.map((k) => k.row)];
    },
  },

  stats: {
    arity: 1,
    options: [],
    run({ header, rows }, [col]) {
      const i = columnIndex(header, col);
      const values = [];
      for (const row of rows) {
        if (row[i] === '') continue;
        if (!isNumber(row[i])) throw notANumber(row[i]);
        values.push(Number(row[i]));
      }
      const head = ['count', 'min', 'max', 'mean'];
      if (values.length === 0) return [head, ['0', '', '', '']];
      const sum = values.reduce((a, b) => a + b, 0);
      const min = values.reduce((a, b) => (b < a ? b : a));
      const max = values.reduce((a, b) => (b > a ? b : a));
      const cells = [min, max, sum / values.length].map(formatNumber);
      return [head, [String(values.length), ...cells]];
    },
  },
};

module.exports = { commands, OPERATORS };
