import java.util.Arrays;

/**
 * An immutable matrix of {@code long} entries. README.md specifies the behaviour.
 */
public final class Matrix {
    private final long[][] data; // never shared with callers and never modified

    private Matrix(long[][] data) {
        this.data = data;
    }

    /** Builds a matrix from its rows; the argument is copied. */
    public static Matrix of(long[][] rows) {
        if (rows == null || rows.length == 0) {
            throw new IllegalArgumentException("a matrix needs at least one row");
        }
        int cols = rows[0] == null ? 0 : rows[0].length;
        if (cols == 0) {
            throw new IllegalArgumentException("a matrix needs at least one column");
        }
        long[][] copy = new long[rows.length][];
        for (int i = 0; i < rows.length; i++) {
            if (rows[i] == null || rows[i].length != cols) {
                throw new IllegalArgumentException("all rows must have the same length");
            }
            copy[i] = rows[i].clone();
        }
        return new Matrix(copy);
    }

    /** The n x n identity matrix. */
    public static Matrix identity(int n) {
        if (n < 1) {
            throw new IllegalArgumentException("the size must be at least 1");
        }
        long[][] d = new long[n][n];
        for (int i = 0; i < n; i++) {
            d[i][i] = 1;
        }
        return new Matrix(d);
    }

    public int rows() {
        return data.length;
    }

    public int cols() {
        return data[0].length;
    }

    /** The entry in the given row and column, counting from 0. */
    public long get(int row, int col) {
        if (row < 0 || row >= rows() || col < 0 || col >= cols()) {
            throw new IndexOutOfBoundsException("no entry (" + row + ", " + col + ") in a " + rows() + "x" + cols() + " matrix");
        }
        return data[row][col];
    }

    /** A copy of the entries, one array per row. */
    public long[][] toArray() {
        long[][] copy = new long[rows()][];
        for (int i = 0; i < copy.length; i++) {
            copy[i] = data[i].clone();
        }
        return copy;
    }

    /** The matrix product {@code this x other}. */
    public Matrix multiply(Matrix other) {
        throw new UnsupportedOperationException("multiply is not implemented yet");
    }

    /** The matrix with rows and columns exchanged. */
    public Matrix transpose() {
        throw new UnsupportedOperationException("transpose is not implemented yet");
    }

    /** The determinant of a square matrix. */
    public long determinant() {
        throw new UnsupportedOperationException("determinant is not implemented yet");
    }

    /** The inverse of a square matrix whose determinant is 1 or -1. */
    public Matrix inverse() {
        throw new UnsupportedOperationException("inverse is not implemented yet");
    }

    @Override
    public boolean equals(Object o) {
        return o instanceof Matrix other && Arrays.deepEquals(data, other.data);
    }

    @Override
    public int hashCode() {
        return Arrays.deepHashCode(data);
    }

    @Override
    public String toString() {
        return Arrays.deepToString(data);
    }
}
