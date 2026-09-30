use crate::{BitSet, WORD_BITS};

/// Iterator over the elements of a [`BitSet`], in ascending order.
pub struct Iter<'a> {
    words: &'a [u64],
    /// Index of the word `current` was taken from.
    index: usize,
    /// The bits of `words[index]` that have not been yielded yet.
    current: u64,
}

impl BitSet {
    /// The elements in ascending order.
    pub fn iter(&self) -> Iter<'_> {
        Iter {
            words: &self.words,
            index: 0,
            current: self.words.first().copied().unwrap_or(0),
        }
    }
}

impl Iterator for Iter<'_> {
    type Item = usize;

    fn next(&mut self) -> Option<usize> {
        while self.current == 0 {
            self.index += 1;
            self.current = *self.words.get(self.index)?;
        }
        let bit = self.current.trailing_zeros() as usize;
        self.current &= self.current - 1; // clear the lowest set bit
        Some(self.index * WORD_BITS + bit)
    }
}
