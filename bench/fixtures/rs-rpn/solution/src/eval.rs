use crate::error::RpnError;
use crate::token::{tokenize, Token};

/// Run a token list on a stack machine (README.md, section Evaluation).
pub fn evaluate(tokens: &[Token]) -> Result<f64, RpnError> {
    let mut stack: Vec<f64> = Vec::new();
    for &token in tokens {
        match token {
            Token::Num(x) => stack.push(x),
            Token::Neg => {
                let a = stack.pop().ok_or(RpnError::StackUnderflow)?;
                stack.push(-a);
            }
            op => {
                let b = stack.pop().ok_or(RpnError::StackUnderflow)?;
                let a = stack.pop().ok_or(RpnError::StackUnderflow)?;
                stack.push(apply(op, a, b)?);
            }
        }
    }
    match stack.as_slice() {
        [] => Err(RpnError::StackUnderflow),
        [result] => Ok(*result),
        _ => Err(RpnError::TooManyOperands),
    }
}

fn apply(op: Token, a: f64, b: f64) -> Result<f64, RpnError> {
    Ok(match op {
        Token::Add => a + b,
        Token::Sub => a - b,
        Token::Mul => a * b,
        Token::Div | Token::Rem if b == 0.0 => return Err(RpnError::DivisionByZero),
        Token::Div => a / b,
        Token::Rem => a % b,
        Token::Pow => a.powf(b),
        Token::Num(_) | Token::Neg => unreachable!("handled by evaluate"),
    })
}

/// Tokenize `input`, then evaluate the tokens (README.md, section eval).
pub fn eval(input: &str) -> Result<f64, RpnError> {
    evaluate(&tokenize(input)?)
}
