#!/usr/bin/env node
'use strict';

const fs = require('node:fs');
const { parseCsv, formatRecord } = require('../src/csv');
const { commands, OPERATORS } = require('../src/commands');
const { CliError, usage, failure } = require('../src/errors');

/** Split the arguments after the command into the command's own arguments and the optional file. */
function parseArguments(argv) {
  const [name, ...rest] = argv;
  if (name === undefined) throw usage('missing command');
  if (!Object.hasOwn(commands, name)) throw usage(`unknown command: ${name}`);
  const command = commands[name];

  const options = new Set();
  const positional = [];
  for (const arg of rest) {
    if (!arg.startsWith('--')) positional.push(arg);
    else if (command.options.includes(arg)) options.add(arg);
    else throw usage(`unknown option: ${arg}`);
  }
  if (positional.length !== command.arity && positional.length !== command.arity + 1) {
    throw usage(`${name}: wrong number of arguments`);
  }
  const args = positional.slice(0, command.arity);
  if (name === 'head' && !/^[0-9]+$/.test(args[0])) throw usage('head: N must be a non-negative integer');
  if (name === 'filter' && args[1] !== 'contains' && !Object.hasOwn(OPERATORS, args[1])) {
    throw usage(`filter: unknown operator: ${args[1]}`);
  }
  return { command, args, options, file: positional[command.arity] };
}

function readInput(file) {
  try {
    return fs.readFileSync(file === undefined ? 0 : file, 'utf8');
  } catch {
    throw failure(`cannot read ${file}`);
  }
}

function toTable(text) {
  const records = parseCsv(text);
  if (records.length === 0) throw failure('empty input');
  const [header, ...rows] = records;
  rows.forEach((row, index) => {
    if (row.length !== header.length) {
      throw failure(`row ${index + 1} has ${row.length} fields, expected ${header.length}`);
    }
  });
  return { header, rows };
}

function main(argv) {
  const { command, args, options, file } = parseArguments(argv);
  const table = toTable(readInput(file));
  const records = command.run(table, args, options);
  process.stdout.write(records.map(formatRecord).join(''));
}

try {
  main(process.argv.slice(2));
} catch (err) {
  if (!(err instanceof CliError)) throw err;
  process.stderr.write(`csvtool: ${err.message}\n`);
  process.exitCode = err.status;
}
