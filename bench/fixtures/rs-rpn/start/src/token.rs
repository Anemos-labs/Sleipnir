use crate::error::RpnError;

/// One lexical token of an RPN expression.
#[derive(Debug, Clone, Copy, PartialEq)]
pub enum Token {
    Num(f64),
    Add,
    Sub,
    Mul,
    Div,
    Rem,
    Pow,
    Neg,
}

/// Split `input` into tokens (README.md, section Tokens).
pub fn tokenize(_input: &str) -> Result<Vec<Token>, RpnError> {
    todo!("tokenize")
}
