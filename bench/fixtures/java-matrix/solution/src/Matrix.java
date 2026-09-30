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
        if (cols() != other.rows()) {
            throw new IllegalArgumentException(
                "cannot multiply a " + rows() + "x" + cols() + " matrix by a " + other.rows() + "x" + other.cols() + " matrix");
        }
        long[][] product = new long[rows()][other.cols()];
        for (int i = 0; i < rows(); i++) {
            for (int k = 0; k < cols(); k++) {
                long a = data[i][k];
                for (int j = 0; j < other.cols(); j++) {
                    product[i][j] += a * other.data[k][j];
                }
            }
        }
        return new Matrix(product);
    }

    /** The matrix with rows and columns exchanged. */
    public Matrix transpose() {
        long[][] t = new long[cols()][rows()];
        for (int i = 0; i < rows(); i++) {
            for (int j = 0; j < cols(); j++) {
                t[j][i] = data[i][j];
            }
        }
        return new Matrix(t);
    }

    /** The determinant of a square matrix. */
    public long determinant() {
        requireSquare("determinant");
        return det(data);
    }

    /** The inverse of a square matrix whose determinant is 1 or -1. */
    public Matrix inverse() {
        requireSquare("inverse");
        long det = det(data);
        if (det == 0) {
            throw new ArithmeticException("the matrix is singular");
        }
        if (det != 1 && det != -1) {
            throw new ArithmeticException("the inverse has no integer entries (determinant " + det + ")");
        }
        int n = rows();
        long[][] inv = new long[n][n];
        for (int i = 0; i < n; i++) {
            for (int j = 0; j < n; j++) {
                // inverse = adjugate / det, and dividing by 1 or -1 is multiplying by it
                long cofactor = det(minor(data, j, i));
                inv[i][j] = ((i + j) % 2 == 0 ? cofactor : -cofactor) * det;
            }
        }
        return new Matrix(inv);
    }

    private void requireSquare(String operation) {
        if (rows() != cols()) {
            throw new IllegalArgumentException(operation + " needs a square matrix, not " + rows() + "x" + cols());
        }
    }

    /** Laplace expansion along the first row; the determinant of the empty matrix is 1. */
    private static long det(long[][] a) {
        int n = a.length;
        if (n == 0) {
            return 1;
        }
        long sum = 0;
        for (int j = 0; j < n; j++) {
            if (a[0][j] != 0) {
                long term = a[0][j] * det(minor(a, 0, j));
                sum += j % 2 == 0 ? term : -term;
            }
        }
        return sum;
    }

    /** {@code a} without row {@code r} and column {@code c}. */
    private static long[][] minor(long[][] a, int r, int c) {
        int n = a.length;
        long[][] m = new long[n - 1][n - 1];
        for (int i = 0, mi = 0; i < n; i++) {
            if (i == r) {
                continue;
            }
            for (int j = 0, mj = 0; j < n; j++) {
                if (j != c) {
                    m[mi][mj++] = a[i][j];
                }
            }
            mi++;
        }
        return m;
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
