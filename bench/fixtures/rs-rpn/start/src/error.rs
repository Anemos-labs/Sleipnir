use std::fmt;

/// Everything that can go wrong while tokenizing or evaluating an expression.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum RpnError {
    DivisionByZero,
    StackUnderflow,
    UnknownToken(String),
    TooManyOperands,
}

impl fmt::Display for RpnError {
    fn fmt(&self, _f: &mut fmt::Formatter<'_>) -> fmt::Result {
        todo!("the messages are listed in README.md, section Display")
    }
}

impl std::error::Error for RpnError {}
