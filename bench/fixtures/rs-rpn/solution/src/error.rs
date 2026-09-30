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
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            RpnError::DivisionByZero => f.write_str("division by zero"),
            RpnError::StackUnderflow => f.write_str("stack underflow"),
            RpnError::UnknownToken(word) => write!(f, "unknown token: {word}"),
            RpnError::TooManyOperands => f.write_str("too many operands"),
        }
    }
}

impl std::error::Error for RpnError {}
