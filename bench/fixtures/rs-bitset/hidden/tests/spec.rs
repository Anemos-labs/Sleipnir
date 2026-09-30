//! Acceptance tests for the bitset crate. Every assertion follows from README.md.

use bitset::BitSet;
use std::collections::BTreeSet;

fn set(bits: &[usize]) -> BitSet {
    let mut s = BitSet::new();
    for &bit in bits {
        s.insert(bit);
    }
    s
}

fn elems(s: &BitSet) -> Vec<usize> {
    s.iter().collect()
}

const NONE: [usize; 0] = [];

// ------------------------------------------------------------ construction

#[test]
fn new_set_is_empty() {
    let s = BitSet::new();
    assert_eq!((s.len(), s.capacity(), s.is_empty()), (0, 0, true));
    assert_eq!(elems(&s), NONE);
    assert!(!s.contains(0));
    let d = BitSet::default();
    assert_eq!((d.len(), d.capacity(), d.is_empty()), (0, 0, true));
}

#[test]
fn with_capacity_is_empty_but_sized() {
    for n in [0, 1, 63, 64, 65, 127, 128, 130] {
        let s = BitSet::with_capacity(n);
        assert_eq!(s.capacity(), n, "with_capacity({n})");
        assert_eq!(s.len(), 0);
        assert!(s.is_empty());
        assert_eq!(elems(&s), NONE);
        assert!(!s.contains(n));
        assert!(!s.contains(n.saturating_sub(1)));
    }
}

#[test]
fn iter_type_is_nameable() {
    let s = set(&[1, 2]);
    let it: bitset::Iter<'_> = s.iter();
    assert_eq!(it.collect::<Vec<usize>>(), vec![1, 2]);
}

// ------------------------------------------------- insert / remove / contains

#[test]
fn insert_reports_whether_the_bit_is_new() {
    let mut s = BitSet::new();
    assert!(s.insert(10));
    assert!(!s.insert(10));
    assert!(s.insert(64));
    assert!(!s.insert(64));
    assert!(s.insert(0));
    assert_eq!(s.len(), 3);
}

#[test]
fn insert_grows_the_capacity_to_bit_plus_one() {
    let mut s = BitSet::new();
    s.insert(0);
    assert_eq!(s.capacity(), 1);
    s.insert(5);
    assert_eq!(s.capacity(), 6);
    s.insert(3);
    assert_eq!(s.capacity(), 6);
    s.insert(63);
    assert_eq!(s.capacity(), 64);
    s.insert(64);
    assert_eq!(s.capacity(), 65);
    s.insert(200);
    assert_eq!(s.capacity(), 201);
    assert_eq!(elems(&s), vec![0, 3, 5, 63, 64, 200]);
}

#[test]
fn insert_below_the_capacity_keeps_it() {
    let mut s = BitSet::with_capacity(100);
    assert!(s.insert(99));
    assert_eq!(s.capacity(), 100);
    assert!(s.insert(0));
    assert_eq!(s.capacity(), 100);
    assert!(s.insert(100));
    assert_eq!(s.capacity(), 101);
    assert_eq!(elems(&s), vec![0, 99, 100]);
}

#[test]
fn insert_at_exactly_the_capacity() {
    for cap in [0, 1, 10, 63, 64, 65, 127, 128, 129] {
        let mut s = BitSet::with_capacity(cap);
        assert!(s.insert(cap), "insert({cap}) into capacity {cap}");
        assert_eq!(s.capacity(), cap + 1);
        assert!(s.contains(cap));
        assert_eq!(elems(&s), vec![cap]);
    }
}

#[test]
fn word_boundaries_are_ordinary_bits() {
    let bits = [
        0, 1, 62, 63, 64, 65, 126, 127, 128, 129, 191, 192, 255, 256, 1000,
    ];
    for bit in bits {
        let mut s = BitSet::new();
        assert!(s.insert(bit), "insert {bit}");
        assert!(s.contains(bit), "contains {bit}");
        assert!(!s.contains(bit + 1), "contains {}", bit + 1);
        if bit > 0 {
            assert!(!s.contains(bit - 1), "contains {}", bit - 1);
        }
        assert_eq!(s.len(), 1, "len after insert {bit}");
        assert_eq!(elems(&s), vec![bit]);
        assert_eq!(s.capacity(), bit + 1);
        assert!(s.remove(bit), "remove {bit}");
        assert!(!s.contains(bit));
        assert!(s.is_empty());
        assert_eq!(s.len(), 0);
        assert_eq!(s.capacity(), bit + 1, "remove must not change the capacity");
    }
}

#[test]
fn every_bit_of_the_first_words() {
    let mut s = BitSet::new();
    for bit in 0..192 {
        assert!(s.insert(bit));
    }
    assert_eq!(s.len(), 192);
    assert_eq!(elems(&s), (0..192).collect::<Vec<_>>());
    assert!((0..192).all(|bit| s.contains(bit)));
    assert!(!s.contains(192));
    for bit in (0..192).step_by(2) {
        assert!(s.remove(bit));
    }
    assert_eq!(s.len(), 96);
    assert_eq!(elems(&s), (1..192).step_by(2).collect::<Vec<_>>());
}

#[test]
fn contains_and_remove_outside_the_capacity() {
    let mut s = set(&[3]);
    assert_eq!(s.capacity(), 4);
    for bit in [4, 5, 63, 64, 1000, usize::MAX] {
        assert!(!s.contains(bit), "contains({bit})");
        assert!(!s.remove(bit), "remove({bit})");
    }
    assert_eq!(s.capacity(), 4);
    assert_eq!(elems(&s), vec![3]);

    // a capacity that is a multiple of 64: the first bit past the end
    let mut full = BitSet::with_capacity(64);
    assert!(!full.contains(64));
    assert!(!full.remove(64));
    assert_eq!(full.capacity(), 64);
    let mut empty = BitSet::new();
    assert!(!empty.contains(0));
    assert!(!empty.remove(0));
    assert_eq!(empty.capacity(), 0);
}

#[test]
fn remove_of_an_absent_bit_inside_the_capacity() {
    let mut s = set(&[1, 100]);
    assert!(!s.remove(2));
    assert!(!s.remove(99));
    assert!(s.remove(100));
    assert!(!s.remove(100));
    assert_eq!(elems(&s), vec![1]);
    assert_eq!(s.capacity(), 101);
}

// ------------------------------------------------------------ len / iter

#[test]
fn len_counts_distinct_elements() {
    let mut s = BitSet::new();
    for bit in [5, 5, 64, 64, 65, 200, 0, 0] {
        s.insert(bit);
    }
    assert_eq!(s.len(), 5);
    s.remove(64);
    s.remove(64);
    assert_eq!(s.len(), 4);
    assert!(!s.is_empty());
}

#[test]
fn iter_is_ascending_across_words() {
    let s = set(&[200, 3, 64, 63, 0, 127, 128, 65]);
    assert_eq!(elems(&s), vec![0, 3, 63, 64, 65, 127, 128, 200]);
    // a second pass gives the same elements
    assert_eq!(elems(&s), vec![0, 3, 63, 64, 65, 127, 128, 200]);
    let mut seen = Vec::new();
    for bit in s.iter() {
        seen.push(bit);
    }
    assert_eq!(seen, vec![0, 3, 63, 64, 65, 127, 128, 200]);
}

#[test]
fn iter_skips_removed_bits_and_empty_words() {
    let mut s = set(&[1, 70, 140, 210]);
    s.remove(70);
    s.remove(140);
    assert_eq!(elems(&s), vec![1, 210]);
    s.remove(1);
    s.remove(210);
    assert_eq!(elems(&s), NONE);
    assert_eq!(s.capacity(), 211);
}

// ------------------------------------------------------------ union

#[test]
fn union_basic() {
    let a = set(&[1, 5, 64]);
    let b = set(&[5, 6, 100]);
    let u = a.union(&b);
    assert_eq!(elems(&u), vec![1, 5, 6, 64, 100]);
    assert_eq!(u.len(), 5);
    assert_eq!(u.capacity(), 101);
    assert_eq!(elems(&b.union(&a)), vec![1, 5, 6, 64, 100]);
    // the operands are unchanged
    assert_eq!(elems(&a), vec![1, 5, 64]);
    assert_eq!(elems(&b), vec![5, 6, 100]);
    assert_eq!((a.capacity(), b.capacity()), (65, 101));
}

#[test]
fn union_of_sets_with_different_sizes() {
    let small = set(&[1, 2]);
    let big = set(&[70, 130]);
    for (x, y) in [(&small, &big), (&big, &small)] {
        let u = x.union(y);
        assert_eq!(elems(&u), vec![1, 2, 70, 130]);
        assert_eq!(u.len(), 4);
        assert_eq!(u.capacity(), 131);
        assert!(u.contains(130));
        assert!(u.contains(1));
    }
}

#[test]
fn union_with_the_empty_set() {
    let big = set(&[70, 130]);
    let empty = BitSet::new();
    assert_eq!(elems(&big.union(&empty)), vec![70, 130]);
    assert_eq!(elems(&empty.union(&big)), vec![70, 130]);
    assert_eq!(big.union(&empty).capacity(), 131);
    assert_eq!(empty.union(&empty).len(), 0);
    assert_eq!(empty.union(&empty).capacity(), 0);
}

// ------------------------------------------------------------ intersection

#[test]
fn intersection_basic() {
    let a = set(&[1, 2, 3, 64, 65]);
    let b = set(&[2, 3, 65, 200]);
    let i = a.intersection(&b);
    assert_eq!(elems(&i), vec![2, 3, 65]);
    assert_eq!(i.len(), 3);
    assert_eq!(i.capacity(), 66);
    assert_eq!(elems(&b.intersection(&a)), vec![2, 3, 65]);
    assert_eq!(elems(&a), vec![1, 2, 3, 64, 65]);
}

#[test]
fn intersection_of_sets_with_different_sizes() {
    let big = set(&[1, 100, 200]);
    let small = set(&[1, 2]);
    for (x, y) in [(&big, &small), (&small, &big)] {
        let i = x.intersection(y);
        assert_eq!(elems(&i), vec![1]);
        assert_eq!(i.capacity(), 3);
    }
    let disjoint = set(&[70]);
    assert_eq!(small.intersection(&disjoint).len(), 0);
    assert_eq!(disjoint.intersection(&small).len(), 0);
    assert_eq!(BitSet::new().intersection(&big).capacity(), 0);
}

// ------------------------------------------------------------ difference

#[test]
fn difference_basic() {
    let a = set(&[1, 2, 3, 70, 200]);
    let b = set(&[2, 70]);
    let d = a.difference(&b);
    assert_eq!(elems(&d), vec![1, 3, 200]);
    assert_eq!(d.len(), 3);
    assert_eq!(d.capacity(), 201);
    assert_eq!(elems(&a), vec![1, 2, 3, 70, 200]);
}

#[test]
fn difference_of_sets_with_different_sizes() {
    let small = set(&[1, 2]);
    let big = set(&[2, 300]);
    let d = small.difference(&big);
    assert_eq!(elems(&d), vec![1]);
    assert_eq!(d.capacity(), 3);
    let d = big.difference(&small);
    assert_eq!(elems(&d), vec![300]);
    assert_eq!(d.capacity(), 301);
    assert_eq!(elems(&big.difference(&BitSet::new())), vec![2, 300]);
    assert_eq!(BitSet::new().difference(&big).len(), 0);
    assert_eq!(elems(&small.difference(&small)), NONE);
}

// ------------------------------------------------------------ is_subset

#[test]
fn is_subset_basic() {
    assert!(BitSet::new().is_subset(&BitSet::new()));
    assert!(BitSet::new().is_subset(&set(&[1])));
    assert!(set(&[1, 2]).is_subset(&set(&[1, 2])));
    assert!(set(&[1, 2]).is_subset(&set(&[1, 2, 3])));
    assert!(!set(&[1, 2, 3]).is_subset(&set(&[1, 2])));
    assert!(!set(&[1, 2]).is_subset(&set(&[1])));
    assert!(!set(&[64]).is_subset(&set(&[63, 65])));
    assert!(set(&[63, 64, 65]).is_subset(&set(&[0, 63, 64, 65, 66])));
}

#[test]
fn is_subset_of_a_larger_or_smaller_set() {
    // other is bigger
    assert!(set(&[1]).is_subset(&set(&[1, 2, 200])));
    assert!(!set(&[1, 100]).is_subset(&set(&[1, 2, 200])));
    // self is bigger: its elements beyond the capacity of other can never be in other
    assert!(!set(&[1, 2, 200]).is_subset(&set(&[1, 2])));
    assert!(!set(&[200]).is_subset(&set(&[1])));
    assert!(!set(&[1, 64]).is_subset(&set(&[1])));
    assert!(!set(&[64]).is_subset(&BitSet::new()));
    assert!(!set(&[0]).is_subset(&BitSet::new()));
}

#[test]
fn is_subset_ignores_the_capacity() {
    let mut big = BitSet::with_capacity(1000);
    big.insert(1);
    assert!(big.is_subset(&set(&[1, 2])));
    assert!(big.is_subset(&set(&[1])));
    assert!(!big.is_subset(&set(&[2])));
    let mut a = set(&[5, 500]);
    a.remove(500);
    assert_eq!(a.capacity(), 501);
    assert!(a.is_subset(&set(&[5])));
    assert!(BitSet::with_capacity(500).is_subset(&BitSet::new()));
}

// ------------------------------------------------------------ resize

#[test]
fn resize_grow_adds_room_not_elements() {
    let mut s = set(&[5]);
    s.resize(1000);
    assert_eq!(s.capacity(), 1000);
    assert_eq!(elems(&s), vec![5]);
    assert_eq!(s.len(), 1);
    assert!(!s.contains(999));
    assert!(s.insert(999));
    assert_eq!(s.capacity(), 1000);
    assert!(s.insert(1000));
    assert_eq!(s.capacity(), 1001);
}

#[test]
fn resize_to_the_same_capacity_changes_nothing() {
    let mut s = set(&[0, 63, 64, 127, 128]);
    s.resize(129);
    assert_eq!(s.capacity(), 129);
    assert_eq!(elems(&s), vec![0, 63, 64, 127, 128]);
}

#[test]
fn shrink_inside_a_word() {
    let mut s = set(&[1, 10, 20, 30]);
    s.resize(21);
    assert_eq!(elems(&s), vec![1, 10, 20]);
    assert_eq!(s.len(), 3);
    s.resize(20);
    assert_eq!(elems(&s), vec![1, 10]);
    assert!(!s.contains(20));
    s.resize(11);
    assert_eq!(elems(&s), vec![1, 10]);
    s.resize(10);
    assert_eq!(elems(&s), vec![1]);
    assert_eq!(s.capacity(), 10);
}

#[test]
fn shrink_inside_a_later_word() {
    let mut s = set(&[3, 70, 130]);
    s.resize(100);
    assert_eq!(s.capacity(), 100);
    assert_eq!(elems(&s), vec![3, 70]);
    assert_eq!(s.len(), 2);
    assert!(!s.contains(130));
    s.resize(71);
    assert_eq!(elems(&s), vec![3, 70]);
    s.resize(70);
    assert_eq!(elems(&s), vec![3]);
    assert!(!s.is_empty());
    s.resize(3);
    assert!(s.is_empty());
    assert_eq!(s.len(), 0);
}

#[test]
fn shrink_at_the_word_boundaries() {
    let base = set(&[0, 63, 64, 127, 128, 191]);
    let cases: [(usize, Vec<usize>); 10] = [
        (192, vec![0, 63, 64, 127, 128, 191]),
        (191, vec![0, 63, 64, 127, 128]),
        (129, vec![0, 63, 64, 127, 128]),
        (128, vec![0, 63, 64, 127]),
        (127, vec![0, 63, 64]),
        (65, vec![0, 63, 64]),
        (64, vec![0, 63]),
        (63, vec![0]),
        (1, vec![0]),
        (0, vec![]),
    ];
    for (cap, expected) in cases {
        let mut s = base.clone();
        s.resize(cap);
        assert_eq!(elems(&s), expected, "after resize({cap})");
        assert_eq!(s.len(), expected.len(), "len after resize({cap})");
        assert_eq!(s.capacity(), cap);
        assert_eq!(s.is_empty(), expected.is_empty(), "is_empty after resize({cap})");
        for bit in [0, 63, 64, 127, 128, 191] {
            assert_eq!(
                s.contains(bit),
                expected.contains(&bit),
                "contains({bit}) after resize({cap})"
            );
        }
        // growing again must not bring anything back
        s.resize(300);
        assert_eq!(elems(&s), expected, "after resize({cap}) and resize(300)");
        assert_eq!(s.len(), expected.len());
        assert_eq!(s.capacity(), 300);
    }
}

#[test]
fn resize_to_zero_empties_the_set() {
    let mut s = set(&[1, 64, 200]);
    s.resize(0);
    assert!(s.is_empty());
    assert_eq!(s.len(), 0);
    assert_eq!(s.capacity(), 0);
    assert_eq!(elems(&s), NONE);
    assert!(s.insert(1));
    assert_eq!(s.capacity(), 2);
    assert_eq!(elems(&s), vec![1]);
}

#[test]
fn shrunk_elements_do_not_come_back() {
    let mut s = set(&[3, 70, 130]);
    s.resize(65);
    assert_eq!(elems(&s), vec![3]);
    s.resize(300);
    assert_eq!(elems(&s), vec![3]);
    assert!(!s.contains(70));
    assert!(!s.contains(130));
    assert_eq!(s.len(), 1);
    // and they can be inserted again like new elements
    assert!(s.insert(70));
    assert_eq!(elems(&s), vec![3, 70]);
}

#[test]
fn insert_after_shrinking() {
    let mut s = set(&[70]);
    s.resize(65);
    assert!(s.is_empty());
    assert!(s.insert(100));
    assert_eq!(s.capacity(), 101);
    assert_eq!(elems(&s), vec![100]);
    assert!(!s.contains(70));
}

#[test]
fn growing_to_a_word_boundary_keeps_the_elements() {
    let mut a = set(&[64, 65, 100]);
    a.resize(128);
    assert_eq!(elems(&a), vec![64, 65, 100]);
    let mut b = set(&[64, 65]);
    assert!(b.insert(127));
    assert_eq!(elems(&b), vec![64, 65, 127]);
    let mut c = set(&[3, 63]);
    c.resize(64);
    assert_eq!(elems(&c), vec![3, 63]);
    let mut d = set(&[0, 1, 2]);
    d.resize(64);
    assert_eq!(elems(&d), vec![0, 1, 2]);
    let mut e = set(&[0, 1, 2, 70]);
    e.resize(256);
    assert_eq!(elems(&e), vec![0, 1, 2, 70]);
    let mut f = set(&[130]);
    assert!(f.insert(191));
    assert_eq!(elems(&f), vec![130, 191]);
}

#[test]
fn operations_after_shrinking_see_no_stale_elements() {
    let mut a = set(&[1, 70, 130]);
    a.resize(65);
    let b = set(&[1, 2, 70, 130]);
    assert_eq!(elems(&a.intersection(&b)), vec![1]);
    assert_eq!(elems(&a.union(&BitSet::new())), vec![1]);
    assert_eq!(elems(&b.difference(&a)), vec![2, 70, 130]);
    assert!(a.is_subset(&set(&[1])));
    assert!(!a.is_subset(&BitSet::new()));
    let mut c = set(&[70]);
    c.resize(65);
    assert!(c.is_subset(&BitSet::new()));
    assert_eq!(a.union(&b).len(), 4);
}

// ------------------------------------------------------------ model based

struct Model {
    elems: BTreeSet<usize>,
    cap: usize,
}

impl Model {
    fn new(cap: usize) -> Model {
        Model {
            elems: BTreeSet::new(),
            cap,
        }
    }
    fn insert(&mut self, bit: usize) -> bool {
        self.cap = self.cap.max(bit + 1);
        self.elems.insert(bit)
    }
    fn remove(&mut self, bit: usize) -> bool {
        self.elems.remove(&bit)
    }
    fn resize(&mut self, cap: usize) {
        self.cap = cap;
        self.elems.retain(|&bit| bit < cap);
    }
}

fn check(s: &BitSet, m: &Model, ctx: &str) {
    assert_eq!(
        elems(s),
        m.elems.iter().copied().collect::<Vec<_>>(),
        "{ctx}: elements"
    );
    assert_eq!(s.len(), m.elems.len(), "{ctx}: len");
    assert_eq!(s.is_empty(), m.elems.is_empty(), "{ctx}: is_empty");
    assert_eq!(s.capacity(), m.cap, "{ctx}: capacity");
}

struct Rng(u64);

impl Rng {
    fn next(&mut self) -> u64 {
        let mut x = self.0;
        x ^= x >> 12;
        x ^= x << 25;
        x ^= x >> 27;
        self.0 = x;
        x.wrapping_mul(0x2545_F491_4F6C_DD1D)
    }
    fn below(&mut self, n: usize) -> usize {
        ((self.next() >> 16) % n as u64) as usize
    }
}

const POOL: [usize; 16] = [
    0, 1, 2, 62, 63, 64, 65, 126, 127, 128, 129, 190, 191, 192, 193, 255,
];

fn pick_bit(rng: &mut Rng) -> usize {
    if rng.below(2) == 0 {
        POOL[rng.below(POOL.len())]
    } else {
        rng.below(300)
    }
}

fn random_set(rng: &mut Rng) -> (BitSet, Model) {
    let cap = if rng.below(3) == 0 { pick_bit(rng) } else { 0 };
    let mut s = BitSet::with_capacity(cap);
    let mut m = Model::new(cap);
    for _ in 0..rng.below(12) {
        let bit = pick_bit(rng);
        s.insert(bit);
        m.insert(bit);
    }
    (s, m)
}

fn check_ops(a: &BitSet, ma: &Model, b: &BitSet, mb: &Model, ctx: &str) {
    let union = Model {
        elems: ma.elems.union(&mb.elems).copied().collect(),
        cap: ma.cap.max(mb.cap),
    };
    check(&a.union(b), &union, &format!("{ctx}: union"));
    let inter = Model {
        elems: ma.elems.intersection(&mb.elems).copied().collect(),
        cap: ma.cap.min(mb.cap),
    };
    check(&a.intersection(b), &inter, &format!("{ctx}: intersection"));
    let diff = Model {
        elems: ma.elems.difference(&mb.elems).copied().collect(),
        cap: ma.cap,
    };
    check(&a.difference(b), &diff, &format!("{ctx}: difference"));
    assert_eq!(
        a.is_subset(b),
        ma.elems.is_subset(&mb.elems),
        "{ctx}: is_subset"
    );
    // the operands are not changed by any of this
    check(a, ma, &format!("{ctx}: left operand"));
    check(b, mb, &format!("{ctx}: right operand"));
}

#[test]
fn random_operations_agree_with_a_model() {
    let mut rng = Rng(0x9E37_79B9_7F4A_7C15);
    let mut s = BitSet::new();
    let mut m = Model::new(0);
    for step in 0..4000 {
        let bit = pick_bit(&mut rng);
        match rng.below(10) {
            0..=3 => assert_eq!(s.insert(bit), m.insert(bit), "step {step}: insert({bit})"),
            4 | 5 => assert_eq!(s.remove(bit), m.remove(bit), "step {step}: remove({bit})"),
            6 => {
                s.resize(bit);
                m.resize(bit);
            }
            7 => assert_eq!(
                s.contains(bit),
                m.elems.contains(&bit),
                "step {step}: contains({bit})"
            ),
            _ => {
                let (o, mo) = random_set(&mut rng);
                check_ops(&s, &m, &o, &mo, &format!("step {step}"));
                check_ops(&o, &mo, &s, &m, &format!("step {step} (swapped)"));
            }
        }
        check(&s, &m, &format!("step {step}"));
    }
}
