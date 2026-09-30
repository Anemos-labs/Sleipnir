use crate::error::RpnError;
use crate::token::Token;

/// Run a token list on a stack machine (README.md, section Evaluation).
pub fn evaluate(_tokens: &[Token]) -> Result<f64, RpnError> {
    todo!("evaluate")
}

/// Tokenize `input`, then evaluate the tokens (README.md, section eval).
pub fn eval(_input: &str) -> Result<f64, RpnError> {
    todo!("eval")
}
