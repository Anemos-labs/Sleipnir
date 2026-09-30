//! Acceptance tests for the rpn crate. Every assertion follows from README.md.

use rpn::Token::{Add, Div, Mul, Neg, Num, Pow, Rem, Sub};
use rpn::{eval, evaluate, tokenize, RpnError, Token};

fn unknown(word: &str) -> RpnError {
    RpnError::UnknownToken(word.to_string())
}

fn close(actual: f64, expected: f64) {
    assert!(
        (actual - expected).abs() < 1e-9,
        "{actual} is not close to {expected}"
    );
}

fn value(input: &str) -> f64 {
    match eval(input) {
        Ok(v) => v,
        Err(e) => panic!("eval({input:?}) failed with {e:?}"),
    }
}

// ---------------------------------------------------------------- tokenize

#[test]
fn tokenize_operators() {
    assert_eq!(
        tokenize("+ - * / % ^ neg"),
        Ok(vec![Add, Sub, Mul, Div, Rem, Pow, Neg])
    );
}

#[test]
fn tokenize_numbers() {
    assert_eq!(
        tokenize("0 7 007 2.50 123.456 0.0"),
        Ok(vec![
            Num(0.0),
            Num(7.0),
            Num(7.0),
            Num(2.5),
            Num(123.456),
            Num(0.0)
        ])
    );
}

#[test]
fn tokenize_number_is_the_nearest_f64() {
    // 0.1 and 3.14159 are not exact in binary: the value must be what str::parse gives.
    assert_eq!(
        tokenize("0.1 3.14159 12345678901234567890"),
        Ok(vec![Num(0.1), Num(3.14159), Num(12345678901234567890.0)])
    );
}

#[test]
fn tokenize_whitespace_variants() {
    let expected = Ok(vec![Num(1.0), Num(2.0), Add]);
    assert_eq!(tokenize("1 2 +"), expected);
    assert_eq!(tokenize("  1   2\t+\n"), expected);
    assert_eq!(tokenize("1\n2\r\n+"), expected);
    assert_eq!(tokenize("\t\t1\t2 +  \r\n\r\n"), expected);
}

#[test]
fn tokenize_empty_input() {
    assert_eq!(tokenize(""), Ok(vec![]));
    assert_eq!(tokenize("   "), Ok(vec![]));
    assert_eq!(tokenize(" \t\r\n "), Ok(vec![]));
}

#[test]
fn tokenize_only_whitespace_separates_words() {
    assert_eq!(tokenize("3 4+"), Err(unknown("4+")));
    assert_eq!(tokenize("3 4 ++"), Err(unknown("++")));
    assert_eq!(tokenize("neg3"), Err(unknown("neg3")));
    assert_eq!(tokenize("3neg"), Err(unknown("3neg")));
    assert_eq!(tokenize("1,2"), Err(unknown("1,2")));
}

#[test]
fn tokenize_unknown_words() {
    let words = [
        "-3", "+3", ".5", "5.", "1e3", "1E3", "1_000", "0x10", "inf", "nan", "1.2.3", "NEG",
        "Neg", "x", "--", "**", "3,5", "١٢",
    ];
    for word in words {
        assert_eq!(tokenize(word), Err(unknown(word)), "word {word:?}");
        let line = format!("1 {word} 2 +");
        assert_eq!(tokenize(&line), Err(unknown(word)), "line {line:?}");
    }
}

#[test]
fn tokenize_reports_the_leftmost_unknown_word() {
    assert_eq!(tokenize("1 foo bar +"), Err(unknown("foo")));
    assert_eq!(tokenize("bar 1 foo"), Err(unknown("bar")));
}

// ---------------------------------------------------------------- evaluate

#[test]
fn evaluate_keeps_operand_order() {
    assert_eq!(evaluate(&[Num(10.0), Num(4.0), Sub]), Ok(6.0));
    assert_eq!(evaluate(&[Num(4.0), Num(10.0), Sub]), Ok(-6.0));
    assert_eq!(evaluate(&[Num(7.0), Num(2.0), Div]), Ok(3.5));
    assert_eq!(evaluate(&[Num(2.0), Num(7.0), Div]), Ok(2.0 / 7.0));
    assert_eq!(evaluate(&[Num(2.0), Num(10.0), Pow]), Ok(1024.0));
    assert_eq!(evaluate(&[Num(10.0), Num(2.0), Pow]), Ok(100.0));
    assert_eq!(evaluate(&[Num(7.0), Num(3.0), Rem]), Ok(1.0));
    assert_eq!(evaluate(&[Num(3.0), Num(7.0), Rem]), Ok(3.0));
}

#[test]
fn evaluate_add_and_mul() {
    assert_eq!(evaluate(&[Num(3.0), Num(4.0), Add]), Ok(7.0));
    assert_eq!(evaluate(&[Num(0.5), Num(0.25), Add]), Ok(0.75));
    assert_eq!(evaluate(&[Num(3.0), Num(4.0), Mul]), Ok(12.0));
    assert_eq!(evaluate(&[Num(2.5), Num(4.0), Mul]), Ok(10.0));
}

#[test]
fn evaluate_neg() {
    assert_eq!(evaluate(&[Num(5.0), Neg]), Ok(-5.0));
    assert_eq!(evaluate(&[Num(5.0), Neg, Neg]), Ok(5.0));
    assert_eq!(evaluate(&[Num(3.0), Num(4.0), Neg, Add]), Ok(-1.0));
    assert_eq!(evaluate(&[Num(3.0), Neg, Num(4.0), Add]), Ok(1.0));
}

#[test]
fn evaluate_rem_takes_the_sign_of_the_dividend() {
    assert_eq!(evaluate(&[Num(7.0), Neg, Num(3.0), Rem]), Ok(-1.0));
    assert_eq!(evaluate(&[Num(7.0), Num(3.0), Neg, Rem]), Ok(1.0));
    assert_eq!(evaluate(&[Num(7.0), Neg, Num(3.0), Neg, Rem]), Ok(-1.0));
    assert_eq!(evaluate(&[Num(5.5), Num(2.0), Rem]), Ok(1.5));
    assert_eq!(evaluate(&[Num(5.5), Neg, Num(2.0), Rem]), Ok(-1.5));
}

#[test]
fn evaluate_pow() {
    assert_eq!(evaluate(&[Num(2.0), Num(2.0), Neg, Pow]), Ok(0.25));
    assert_eq!(evaluate(&[Num(0.0), Num(0.0), Pow]), Ok(1.0));
    assert_eq!(evaluate(&[Num(5.0), Num(0.0), Pow]), Ok(1.0));
    close(evaluate(&[Num(9.0), Num(0.5), Pow]).unwrap(), 3.0);
    close(evaluate(&[Num(2.0), Num(0.5), Pow]).unwrap(), 2f64.sqrt());
}

#[test]
fn evaluate_stack_underflow() {
    let cases: [&[Token]; 8] = [
        &[],
        &[Add],
        &[Num(1.0), Add],
        &[Neg],
        &[Num(1.0), Pow],
        &[Num(1.0), Num(2.0), Add, Mul],
        &[Num(1.0), Num(2.0), Add, Rem],
        &[Sub, Num(1.0), Num(2.0)],
    ];
    for tokens in cases {
        assert_eq!(
            evaluate(tokens),
            Err(RpnError::StackUnderflow),
            "tokens {tokens:?}"
        );
    }
}

#[test]
fn evaluate_too_many_operands() {
    let cases: [&[Token]; 4] = [
        &[Num(1.0), Num(2.0)],
        &[Num(1.0), Num(2.0), Num(3.0), Add],
        &[Num(1.0), Num(2.0), Neg],
        &[Num(1.0), Num(2.0), Num(3.0)],
    ];
    for tokens in cases {
        assert_eq!(
            evaluate(tokens),
            Err(RpnError::TooManyOperands),
            "tokens {tokens:?}"
        );
    }
}

#[test]
fn evaluate_division_by_zero() {
    assert_eq!(
        evaluate(&[Num(1.0), Num(0.0), Div]),
        Err(RpnError::DivisionByZero)
    );
    assert_eq!(
        evaluate(&[Num(1.0), Num(0.0), Rem]),
        Err(RpnError::DivisionByZero)
    );
    assert_eq!(
        evaluate(&[Num(0.0), Num(0.0), Div]),
        Err(RpnError::DivisionByZero)
    );
    // negative zero is zero
    assert_eq!(
        evaluate(&[Num(1.0), Num(0.0), Neg, Div]),
        Err(RpnError::DivisionByZero)
    );
    assert_eq!(
        evaluate(&[Num(4.0), Num(0.0), Neg, Rem]),
        Err(RpnError::DivisionByZero)
    );
    // a computed zero divisor counts too
    assert_eq!(
        evaluate(&[Num(1.0), Num(2.0), Num(2.0), Sub, Div]),
        Err(RpnError::DivisionByZero)
    );
}

#[test]
fn evaluate_zero_dividend_is_fine() {
    assert_eq!(evaluate(&[Num(0.0), Num(5.0), Div]), Ok(0.0));
    assert_eq!(evaluate(&[Num(0.0), Num(5.0), Rem]), Ok(0.0));
}

#[test]
fn evaluate_stops_at_the_first_error() {
    // division by zero happens before the stack would underflow / overflow
    assert_eq!(
        evaluate(&[Num(1.0), Num(0.0), Div, Add]),
        Err(RpnError::DivisionByZero)
    );
    assert_eq!(
        evaluate(&[Num(1.0), Num(0.0), Div, Num(1.0), Num(2.0)]),
        Err(RpnError::DivisionByZero)
    );
    // underflow happens before the division by zero
    assert_eq!(
        evaluate(&[Add, Num(1.0), Num(0.0), Div]),
        Err(RpnError::StackUnderflow)
    );
}

// ---------------------------------------------------------------- eval

#[test]
fn eval_readme_examples() {
    assert_eq!(eval("3 4 +"), Ok(7.0));
    assert_eq!(eval("5 1 2 + 4 * + 3 -"), Ok(14.0));
    assert_eq!(eval("10 4 -"), Ok(6.0));
    assert_eq!(eval("7 2 /"), Ok(3.5));
    assert_eq!(eval("2 3 2 ^ ^"), Ok(512.0));
    assert_eq!(eval("7 neg 3 %"), Ok(-1.0));
    assert_eq!(eval("5 neg neg"), Ok(5.0));
    assert_eq!(eval("  2.5\t4 *\n"), Ok(10.0));
}

#[test]
fn eval_bigger_expressions() {
    assert_eq!(value("1 2 3 4 5 + + + +"), 15.0);
    assert_eq!(value("2 3 4 * +"), 14.0);
    assert_eq!(value("2 3 + 4 *"), 20.0);
    assert_eq!(value("100 7 % 3 ^"), 8.0);
    assert_eq!(value("1 2 - neg 10 *"), 10.0);
    assert_eq!(value("0.5 0.5 + 3 -"), -2.0);
    // (17 % 5) - (17 / 5) = 2 - 3.4
    close(value("17 5 % 17 5 / -"), -1.4);
}

#[test]
fn eval_empty_and_blank_input_underflow() {
    assert_eq!(eval(""), Err(RpnError::StackUnderflow));
    assert_eq!(eval("   \n\t "), Err(RpnError::StackUnderflow));
}

#[test]
fn eval_underflow_cases() {
    for input in ["+", "1 +", "neg", "1 2 + *", "1 2 3 + + +", "% 1"] {
        assert_eq!(
            eval(input),
            Err(RpnError::StackUnderflow),
            "input {input:?}"
        );
    }
}

#[test]
fn eval_too_many_operands_cases() {
    for input in ["1 2", "1 2 3 +", "1 2 neg", "1 2 3 4"] {
        assert_eq!(
            eval(input),
            Err(RpnError::TooManyOperands),
            "input {input:?}"
        );
    }
}

#[test]
fn eval_division_by_zero_cases() {
    for input in ["1 0 /", "4 0 neg %", "1 0.0 /", "0 0 /", "5 2 2 - %", "1 0 / 2 +"] {
        assert_eq!(
            eval(input),
            Err(RpnError::DivisionByZero),
            "input {input:?}"
        );
    }
}

#[test]
fn eval_unknown_tokens_win_over_evaluation_errors() {
    assert_eq!(eval("1 0 / foo"), Err(unknown("foo")));
    assert_eq!(eval("+ foo"), Err(unknown("foo")));
    assert_eq!(eval("1 2 bar"), Err(unknown("bar")));
    assert_eq!(eval("1 0 / foo bar"), Err(unknown("foo")));
    assert_eq!(eval("-3 4 +"), Err(unknown("-3")));
    assert_eq!(eval("3 4+"), Err(unknown("4+")));
    assert_eq!(eval("1e3 1 +"), Err(unknown("1e3")));
}

#[test]
fn eval_is_tokenize_then_evaluate() {
    let inputs = [
        "",
        "3 4 +",
        "1 2",
        "1 0 /",
        "1 0 / foo",
        "+",
        "2 3 2 ^ ^",
        "7 neg 3 %",
        "x",
    ];
    for input in inputs {
        let expected = tokenize(input).and_then(|tokens| evaluate(&tokens));
        assert_eq!(eval(input), expected, "input {input:?}");
    }
}

// ---------------------------------------------------------------- errors

#[test]
fn display_messages() {
    assert_eq!(RpnError::DivisionByZero.to_string(), "division by zero");
    assert_eq!(RpnError::StackUnderflow.to_string(), "stack underflow");
    assert_eq!(RpnError::TooManyOperands.to_string(), "too many operands");
    assert_eq!(unknown("foo").to_string(), "unknown token: foo");
    assert_eq!(unknown("1e3").to_string(), "unknown token: 1e3");
}

#[test]
fn error_is_a_std_error() {
    let boxed: Box<dyn std::error::Error> = Box::new(RpnError::StackUnderflow);
    assert_eq!(boxed.to_string(), "stack underflow");
}
