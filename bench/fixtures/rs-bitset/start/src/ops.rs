//! Set operations. Sets may have different capacities, so their word vectors may have different lengths: a word that
//! one set does not have counts as zero.

use crate::BitSet;

/// Word `i` of `set`, or 0 when the set has no such word.
fn word(set: &BitSet, i: usize) -> u64 {
    set.words.get(i).copied().unwrap_or(0)
}

impl BitSet {
    /// Elements in `self` or in `other`. The capacity is the larger of the two.
    pub fn union(&self, other: &BitSet) -> BitSet {
        let n = self.words.len().min(other.words.len());
        BitSet {
            words: (0..n).map(|i| word(self, i) | word(other, i)).collect(),
            cap: self.cap.max(other.cap),
        }
    }

    /// Elements in both `self` and `other`. The capacity is the smaller of the two.
    pub fn intersection(&self, other: &BitSet) -> BitSet {
        let n = self.words.len().min(other.words.len());
        BitSet {
            words: (0..n).map(|i| word(self, i) & word(other, i)).collect(),
            cap: self.cap.min(other.cap),
        }
    }

    /// Elements of `self` that are not in `other`. The capacity is `self`'s.
    pub fn difference(&self, other: &BitSet) -> BitSet {
        BitSet {
            words: (0..self.words.len())
                .map(|i| word(self, i) & !word(other, i))
                .collect(),
            cap: self.cap,
        }
    }

    /// Is every element of `self` also in `other`? Capacities do not matter.
    pub fn is_subset(&self, other: &BitSet) -> bool {
        (0..other.words.len()).all(|i| word(self, i) & !word(other, i) == 0)
    }
}
