//! The worked examples of README.md.

use rpn::{eval, evaluate, tokenize, RpnError, Token};

#[test]
fn adds() {
    assert_eq!(eval("3 4 +"), Ok(7.0));
}

#[test]
fn nested_expression() {
    assert_eq!(eval("5 1 2 + 4 * + 3 -"), Ok(14.0));
}

#[test]
fn negation_and_remainder() {
    assert_eq!(eval("7 neg 3 %"), Ok(-1.0));
}

#[test]
fn empty_input_underflows() {
    assert_eq!(eval(""), Err(RpnError::StackUnderflow));
}

#[test]
fn leftover_operands() {
    assert_eq!(eval("1 2"), Err(RpnError::TooManyOperands));
}

#[test]
fn division_by_zero() {
    assert_eq!(eval("1 0 /"), Err(RpnError::DivisionByZero));
}

#[test]
fn unknown_token_is_reported_before_evaluation_errors() {
    assert_eq!(
        eval("1 0 / foo"),
        Err(RpnError::UnknownToken("foo".to_string()))
    );
}

#[test]
fn tokens() {
    assert_eq!(tokenize("2 neg"), Ok(vec![Token::Num(2.0), Token::Neg]));
    assert_eq!(
        evaluate(&[Token::Num(9.0), Token::Num(3.0), Token::Sub]),
        Ok(6.0)
    );
}
