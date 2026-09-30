//! `BitSet`: a growable set of small `usize` values stored as a bit vector. `README.md` is the specification.

mod iter;
mod ops;

pub use iter::Iter;

/// Bits per storage word.
pub(crate) const WORD_BITS: usize = 64;

/// Number of words needed to store `bits` bits.
pub(crate) fn words_for(bits: usize) -> usize {
    bits.div_ceil(WORD_BITS)
}

/// A growable set of `usize` values (see README.md).
#[derive(Clone, Debug, Default)]
pub struct BitSet {
    /// Bit `i` lives in `words[i / 64]`, at position `i % 64`.
    pub(crate) words: Vec<u64>,
    /// The capacity: only the values `0..cap` can be elements.
    pub(crate) cap: usize,
}

impl BitSet {
    /// The empty set, with capacity 0.
    pub fn new() -> BitSet {
        BitSet::default()
    }

    /// The empty set, with capacity `cap`.
    pub fn with_capacity(cap: usize) -> BitSet {
        BitSet {
            words: vec![0; words_for(cap)],
            cap,
        }
    }

    /// The number of addressable bits.
    pub fn capacity(&self) -> usize {
        self.cap
    }

    /// Add `bit`, growing the capacity to `bit + 1` if needed. Returns whether `bit` is new.
    pub fn insert(&mut self, bit: usize) -> bool {
        if bit >= self.cap {
            self.resize(bit + 1);
        }
        let mask = 1u64 << (bit % WORD_BITS);
        let word = &mut self.words[bit / WORD_BITS];
        let is_new = *word & mask == 0;
        *word |= mask;
        is_new
    }

    /// Remove `bit`. Returns whether it was in the set.
    pub fn remove(&mut self, bit: usize) -> bool {
        match self.words.get_mut(bit / WORD_BITS) {
            Some(word) => {
                let mask = 1u64 << (bit % WORD_BITS);
                let was_present = *word & mask != 0;
                *word &= !mask;
                was_present
            }
            None => false,
        }
    }

    /// Is `bit` in the set?
    pub fn contains(&self, bit: usize) -> bool {
        match self.words.get(bit / WORD_BITS) {
            Some(word) => word & (1u64 << (bit % WORD_BITS)) != 0,
            None => false,
        }
    }

    /// The number of elements.
    pub fn len(&self) -> usize {
        self.words.iter().map(|w| w.count_ones() as usize).sum()
    }

    /// Does the set have no elements?
    pub fn is_empty(&self) -> bool {
        self.words.iter().all(|&w| w == 0)
    }

    /// Set the capacity to exactly `cap`. Shrinking removes every element `>= cap`.
    pub fn resize(&mut self, cap: usize) {
        self.words.resize(words_for(cap), 0);
        // The last word may hold bits at or above `cap` (a shrink that stops inside a word): clear them.
        let used = cap % WORD_BITS;
        if used != 0 {
            if let Some(last) = self.words.last_mut() {
                *last &= (1u64 << used) - 1;
            }
        }
        self.cap = cap;
    }
}
