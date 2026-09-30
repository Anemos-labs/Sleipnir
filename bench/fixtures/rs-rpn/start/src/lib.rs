//! A small reverse-polish-notation calculator. `README.md` is the specification.

mod error;
mod eval;
mod token;

pub use error::RpnError;
pub use eval::{eval, evaluate};
pub use token::{tokenize, Token};
