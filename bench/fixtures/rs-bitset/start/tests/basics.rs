use bitset::BitSet;

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

#[test]
fn insert_contains_remove() {
    let mut s = BitSet::new();
    assert!(s.insert(3));
    assert!(!s.insert(3));
    assert!(s.contains(3));
    assert!(!s.contains(4));
    assert!(s.remove(3));
    assert!(!s.remove(3));
    assert!(s.is_empty());
}

#[test]
fn capacity_follows_insert() {
    let mut s = BitSet::new();
    assert_eq!(s.capacity(), 0);
    s.insert(5);
    assert_eq!(s.capacity(), 6);
    s.insert(2);
    assert_eq!(s.capacity(), 6);
    s.insert(64);
    assert_eq!(s.capacity(), 65);
}

#[test]
fn iter_is_ascending() {
    let s = set(&[70, 3, 64, 0, 127]);
    assert_eq!(elems(&s), vec![0, 3, 64, 70, 127]);
    assert_eq!(s.len(), 5);
}

#[test]
fn intersection_and_difference() {
    let a = set(&[1, 2, 3, 64]);
    let b = set(&[2, 3, 4, 64]);
    assert_eq!(elems(&a.intersection(&b)), vec![2, 3, 64]);
    assert_eq!(elems(&a.difference(&b)), vec![1]);
    assert_eq!(elems(&b.difference(&a)), vec![4]);
}

#[test]
fn union_of_sets_with_different_sizes() {
    let small = set(&[1, 2]);
    let big = set(&[70, 130]);
    assert_eq!(elems(&small.union(&big)), vec![1, 2, 70, 130]);
    assert_eq!(elems(&big.union(&small)), vec![1, 2, 70, 130]);
}

#[test]
fn shrinking_removes_the_elements_above() {
    let mut s = set(&[1, 10, 20, 30]);
    s.resize(21);
    assert_eq!(s.capacity(), 21);
    assert_eq!(elems(&s), vec![1, 10, 20]);
    assert_eq!(s.len(), 3);
    assert!(!s.contains(30));
}
