'use strict';

const LINE_BREAKS = new Set(['\r\n', '\n']);

function checkDelimiter(delimiter) {
  if (typeof delimiter !== 'string' || delimiter.length !== 1 || delimiter === '"' || delimiter === '\r' || delimiter === '\n') {
    throw new RangeError('delimiter must be one character other than a double quote, CR or LF');
  }
}

function checkNewline(newline) {
  if (!LINE_BREAKS.has(newline)) {
    throw new RangeError("newline must be '\\r\\n' or '\\n'");
  }
}

module.exports = { checkDelimiter, checkNewline };
