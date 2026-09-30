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
pub fn tokenize(input: &str) -> Result<Vec<Token>, RpnError> {
    input.split_ascii_whitespace().map(token).collect()
}

fn token(word: &str) -> Result<Token, RpnError> {
    Ok(match word {
        "+" => Token::Add,
        "-" => Token::Sub,
        "*" => Token::Mul,
        "/" => Token::Div,
        "%" => Token::Rem,
        "^" => Token::Pow,
        "neg" => Token::Neg,
        _ if is_number(word) => match word.parse() {
            Ok(value) => Token::Num(value),
            Err(_) => return Err(RpnError::UnknownToken(word.to_string())),
        },
        _ => return Err(RpnError::UnknownToken(word.to_string())),
    })
}

/// `[0-9]+(\.[0-9]+)?`: what `str::parse::<f64>` accepts is much wider (signs, exponents, `inf`, ...).
fn is_number(word: &str) -> bool {
    let digits = |s: &str| !s.is_empty() && s.bytes().all(|b| b.is_ascii_digit());
    match word.split_once('.') {
        Some((int, frac)) => digits(int) && digits(frac),
        None => digits(word),
    }
}
