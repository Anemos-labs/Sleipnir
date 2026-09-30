'use strict';

const NUMBER = /^-?[0-9]+(\.[0-9]+)?$/;

const isNumber = (s) => NUMBER.test(s);

/** Compare two strings by UTF-16 code unit, like JavaScript's `<`. */
const compareStrings = (a, b) => (a < b ? -1 : a > b ? 1 : 0);

/** Numbers compare as numbers when both are numbers, everything else as strings. */
function compareValues(a, b) {
  if (isNumber(a) && isNumber(b)) return Math.sign(Number(a) - Number(b));
  return compareStrings(a, b);
}

/** Rounded to 0.0001, plain decimal notation, no trailing zeros; zero is "0". */
const formatNumber = (x) => String(Number(x.toFixed(4)));

module.exports = { isNumber, compareStrings, compareValues, formatNumber };
