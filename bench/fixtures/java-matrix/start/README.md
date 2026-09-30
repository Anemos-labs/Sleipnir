# Matrix

`src/Matrix.java` is an immutable matrix of `long` entries (Java 21, default
package, no dependencies). Construction, element access, `equals`, `hashCode` and
`toString` are finished. Four operations are still stubs that throw
`UnsupportedOperationException`; implement them exactly as specified below:
`multiply`, `transpose`, `determinant` and `inverse`.

```java
public final class Matrix {
    public static Matrix of(long[][] rows)   // copies rows; IllegalArgumentException if rows is null or empty,
                                             // or a row is null or empty, or the rows differ in length
    public static Matrix identity(int n)     // the n x n identity; IllegalArgumentException if n < 1
    public int rows()
    public int cols()
    public long get(int row, int col)        // 0-based; IndexOutOfBoundsException outside the matrix
    public long[][] toArray()                // a new copy on every call

    public Matrix multiply(Matrix other)     // TO DO
    public Matrix transpose()                // TO DO
    public long determinant()                // TO DO
    public Matrix inverse()                  // TO DO

    // equals: same dimensions and same entries; hashCode agrees with equals;
    // toString looks like [[1, 2], [3, 4]]
}
```

## What the four operations must do

All arithmetic is exact integer arithmetic on `long`. Do not compute with
`double`: the tests include entries near 10^9 whose results are only right if
nothing is ever rounded. The test values are chosen so that the usual exact methods
(Laplace expansion, fraction-free Bareiss elimination, the adjugate) stay within
the range of `long`, so overflow is not part of this task.

A `Matrix` is immutable. No method may change `this` or its argument (for example,
do not eliminate rows inside the matrix's own array), and each operation returns a
new `Matrix`.

### `multiply(Matrix other)`

The matrix product `this x other`: the entry in row `i` and column `j` is the sum over
`k` of `this(i, k) * other(k, j)`. The result has `this.rows()` rows and
`other.cols()` columns. It requires `this.cols() == other.rows()`; otherwise it throws
`IllegalArgumentException`.

### `transpose()`

The matrix with rows and columns exchanged: it has `cols()` rows and `rows()`
columns, and `result.get(j, i) == this.get(i, j)`.

### `determinant()`

The usual determinant (for `[[a]]` it is `a`, for `[[a, b], [c, d]]` it is
`a*d - b*c`; in general, for instance, Laplace expansion or fraction-free
elimination). It is only defined for square matrices: for any other shape it throws
`IllegalArgumentException`. A zero where an elimination algorithm would look for a
pivot must not give a wrong answer: `[[0, 1], [1, 0]]` has determinant -1.

### `inverse()`

The inverse of a matrix of integers has integer entries only if the determinant is
1 or -1. So `inverse()` returns a matrix only in that case:

| the matrix | result |
|---|---|
| not square | throws `IllegalArgumentException` |
| square, determinant 0 | throws `ArithmeticException` |
| square, determinant neither 0, 1 nor -1 | throws `ArithmeticException` (the inverse exists, but its entries are not integers) |
| square, determinant 1 or -1 | returns the matrix `B` with `this x B` and `B x this` both equal to the identity |

The exception types are part of the specification; the messages are not.

## Examples

```java
Matrix a = Matrix.of(new long[][] {{1, 2}, {3, 4}});
a.transpose()                                   // [[1, 3], [2, 4]]
a.multiply(Matrix.of(new long[][] {{0, 1}, {1, 0}}))  // [[2, 1], [4, 3]]
a.determinant()                                 // -2
a.inverse()                                     // throws ArithmeticException (the determinant is -2)

Matrix.of(new long[][] {{2, 1}, {1, 1}}).inverse()    // [[1, -1], [-1, 2]]   (the determinant is 1)
Matrix.of(new long[][] {{1, 2, 3}}).multiply(a)       // throws IllegalArgumentException (1x3 times 2x2)
Matrix.of(new long[][] {{1, 2, 3}, {4, 5, 6}}).determinant()  // throws IllegalArgumentException (2x3)
```

## Building and testing

There is no JUnit. `test/TestMain.java` is a plain program with a tiny assertion
helper; it prints one line per failed check and exits with a non-zero status if any
check fails.

```
rm -rf build && mkdir -p build && javac -d build $(find . -name '*.java') && java -cp build TestMain
```

`test/TestMain.java` contains the examples above. More tests that check the rules
above are run when your solution is verified.
