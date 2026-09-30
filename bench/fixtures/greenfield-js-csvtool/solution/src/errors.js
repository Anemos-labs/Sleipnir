'use strict';

/** An error the tool reports to the user: a message and an exit status (2 usage, 1 input). */
class CliError extends Error {
  constructor(message, status) {
    super(message);
    this.status = status;
  }
}

const usage = (message) => new CliError(message, 2);
const failure = (message) => new CliError(message, 1);

module.exports = { CliError, usage, failure };
