/**
 * Plain-Java tests (there is no JUnit). Every failed check prints a line, and the
 * exit status is 1 if any check failed. m("1 2; 3 4") builds the matrix [[1, 2], [3, 4]].
 */
public class TestMain {
    private static int checks;
    private static int failures;

    private static void fail(String what, String detail) {
        failures++;
        System.out.println("FAIL " + what + ": " + detail);
    }

    static void assertTrue(String what, boolean condition) {
        checks++;
        if (!condition) {
            fail(what, "the condition is false");
        }
    }

    static void assertEquals(String what, Object expected, Object actual) {
        checks++;
        if (!expected.equals(actual)) {
            fail(what, "expected " + expected + " but got " + actual);
        }
    }

    static void assertEquals(String what, long expected, long actual) {
        checks++;
        if (expected != actual) {
            fail(what, "expected " + expected + " but got " + actual);
        }
    }

    static void assertThrows(String what, Class<? extends Throwable> type, Runnable body) {
        checks++;
        try {
            body.run();
        } catch (Throwable t) {
            if (!type.isInstance(t)) {
                fail(what, "expected " + type.getSimpleName() + " but got " + t);
            }
            return;
        }
        fail(what, "expected " + type.getSimpleName() + " but nothing was thrown");
    }

    /** Parses rows separated by ';' whose entries are separated by spaces. */
    static Matrix m(String rows) {
        String[] lines = rows.split(";");
        long[][] data = new long[lines.length][];
        for (int i = 0; i < lines.length; i++) {
            String[] cells = lines[i].trim().split("\\s+");
            data[i] = new long[cells.length];
            for (int j = 0; j < cells.length; j++) {
                data[i][j] = Long.parseLong(cells[j]);
            }
        }
        return Matrix.of(data);
    }

    /** Runs a group of checks; an unexpected exception fails the group instead of ending the run. */
    static void group(String name, Runnable body) {
        try {
            body.run();
        } catch (Throwable t) {
            checks++;
            fail(name, "unexpected " + t);
        }
    }

    static void detIs(String rows, long expected) {
        group("determinant of " + rows, () -> assertEquals("determinant of " + rows, expected, m(rows).determinant()));
    }

    static void productIs(String a, String b, String expected) {
        group("(" + a + ") x (" + b + ")", () -> assertEquals("(" + a + ") x (" + b + ")", m(expected), m(a).multiply(m(b))));
    }

    static void inverseIs(String rows, String expected) {
        group("inverse of " + rows, () -> assertEquals("inverse of " + rows, m(expected), m(rows).inverse()));
    }

    static void noIntegerInverse(String rows) {
        group("inverse of " + rows, () -> assertThrows("inverse of " + rows, ArithmeticException.class, () -> m(rows).inverse()));
    }

    public static void main(String[] args) {
        construction();
        transpose();
        multiply();
        determinant();
        inverse();
        System.out.println(checks + " checks, " + failures + " failed");
        if (failures > 0) {
            System.exit(1);
        }
    }

    // Finished behaviour that the operations must not break.
    static void construction() {
        group("construction", () -> {
            long[][] src = {{1, 2}, {3, 4}};
            Matrix a = Matrix.of(src);
            src[0][0] = 99;
            assertEquals("Matrix.of copies its argument", 1, a.get(0, 0));
            long[][] out = a.toArray();
            out[1][1] = 99;
            assertEquals("toArray returns a copy", 4, a.get(1, 1));
            assertEquals("rows of a 2x3", 2, m("1 2 3; 4 5 6").rows());
            assertEquals("cols of a 2x3", 3, m("1 2 3; 4 5 6").cols());
            assertThrows("of(null)", IllegalArgumentException.class, () -> Matrix.of(null));
            assertThrows("of(no rows)", IllegalArgumentException.class, () -> Matrix.of(new long[0][]));
            assertThrows("of(an empty row)", IllegalArgumentException.class, () -> Matrix.of(new long[][] {{}}));
            assertThrows("of(ragged rows)", IllegalArgumentException.class, () -> Matrix.of(new long[][] {{1, 2}, {3}}));
            assertEquals("identity(3)", m("1 0 0; 0 1 0; 0 0 1"), Matrix.identity(3));
            assertThrows("identity(0)", IllegalArgumentException.class, () -> Matrix.identity(0));
            assertThrows("get(-1, 0)", IndexOutOfBoundsException.class, () -> a.get(-1, 0));
            assertThrows("get(0, 2)", IndexOutOfBoundsException.class, () -> a.get(0, 2));
            assertThrows("get(2, 0)", IndexOutOfBoundsException.class, () -> a.get(2, 0));
            assertEquals("equal matrices are equal", m("1 2; 3 4"), m("1 2; 3 4"));
            assertEquals("equal matrices have equal hash codes", m("1 2; 3 4").hashCode(), m("1 2; 3 4").hashCode());
            assertTrue("different entries are not equal", !m("1 2; 3 4").equals(m("1 2; 3 5")));
            assertTrue("different shapes are not equal", !m("1 2 3 4").equals(m("1 2; 3 4")));
            assertEquals("toString", "[[1, 2], [3, 4]]", m("1 2; 3 4").toString());
        });
    }

    static void transpose() {
        group("transpose", () -> {
            assertEquals("2x2", m("1 3; 2 4"), m("1 2; 3 4").transpose());
            assertEquals("2x3", m("1 4; 2 5; 3 6"), m("1 2 3; 4 5 6").transpose());
            assertEquals("3x2", m("1 2 3; 4 5 6"), m("1 4; 2 5; 3 6").transpose());
            assertEquals("a column", m("1 2 3"), m("1; 2; 3").transpose());
            assertEquals("a row", m("1; 2; 3"), m("1 2 3").transpose());
            assertEquals("1x1", m("7"), m("7").transpose());
            assertEquals("negative entries", m("-1 3; 0 -4"), m("-1 0; 3 -4").transpose());
            Matrix a = m("1 2 3; 4 5 6");
            Matrix t = a.transpose();
            assertEquals("rows of the transpose", 3, t.rows());
            assertEquals("cols of the transpose", 2, t.cols());
            for (int i = 0; i < a.rows(); i++) {
                for (int j = 0; j < a.cols(); j++) {
                    assertEquals("entry (" + j + ", " + i + ") of the transpose", a.get(i, j), t.get(j, i));
                }
            }
            assertEquals("transposing twice", a, t.transpose());
            assertEquals("the operand is unchanged", m("1 2 3; 4 5 6"), a);
            Matrix s = m("1 2 3; 2 4 5; 3 5 6");
            assertEquals("a symmetric matrix is its own transpose", s, s.transpose());
        });
    }

    static void multiply() {
        productIs("1 2; 3 4", "0 1; 1 0", "2 1; 4 3");
        productIs("1 2 3; 4 5 6", "7 8; 9 10; 11 12", "58 64; 139 154");
        productIs("7 8; 9 10; 11 12", "1 2 3; 4 5 6", "39 54 69; 49 68 87; 59 82 105");
        productIs("1 2 3", "4; 5; 6", "32");
        productIs("1; 2; 3", "4 5", "4 5; 8 10; 12 15");
        productIs("-1 2; 3 -4", "5 -6; -7 8", "-19 22; 43 -50");
        productIs("0 0; 0 0", "5 6; 7 8", "0 0; 0 0");
        productIs("2", "3", "6");
        productIs("1 2 3; 4 5 6", "1 0 0; 0 1 0; 0 0 1", "1 2 3; 4 5 6");
        productIs("1 0; 0 1", "1 2 3; 4 5 6", "1 2 3; 4 5 6");
        productIs("0 1; 1 0", "1 2; 3 4", "3 4; 1 2");
        productIs("2147483647 1", "2147483649; 2", "4611686018427387905");
        productIs("1 2 0; 0 1 3; 4 0 1", "2 1 0; 0 3 1; 1 0 2", "2 7 2; 3 3 7; 9 4 2");
        group("multiply: shapes and laws", () -> {
            assertThrows("1x3 times 1x2", IllegalArgumentException.class, () -> m("1 2 3").multiply(m("1 2")));
            assertThrows("2x2 times 1x3", IllegalArgumentException.class, () -> m("1 2; 3 4").multiply(m("1 2 3")));
            assertThrows("3x1 times 3x1", IllegalArgumentException.class, () -> m("1; 2; 3").multiply(m("1; 2; 3")));
            Matrix a = m("1 2; 3 4");
            Matrix b = m("0 1; 1 0");
            Matrix c = m("2 0; 1 3");
            assertTrue("multiplication is not commutative", !a.multiply(b).equals(b.multiply(a)));
            assertEquals("associativity", a.multiply(b).multiply(c), a.multiply(b.multiply(c)));
            assertEquals("the left operand is unchanged", m("1 2; 3 4"), a);
            assertEquals("the right operand is unchanged", m("0 1; 1 0"), b);
            Matrix p = m("1 2; 3 4; 5 6").multiply(m("1 0 2 0; 0 1 0 2"));
            assertEquals("rows of a 3x2 times 2x4", 3, p.rows());
            assertEquals("cols of a 3x2 times 2x4", 4, p.cols());
            assertEquals("a 3x2 times 2x4", m("1 2 2 4; 3 4 6 8; 5 6 10 12"), p);
        });
    }

    static void determinant() {
        detIs("5", 5L);
        detIs("-3", -3L);
        detIs("0", 0L);
        detIs("1 2; 3 4", -2L);
        detIs("2 0; 0 3", 6L);
        detIs("0 1; 1 0", -1L);
        detIs("7 -3; 2 5", 41L);
        detIs("0 0; 0 0", 0L);
        detIs("1 2; 2 4", 0L);
        detIs("0 5; 0 7", 0L);
        detIs("6 1 1; 4 -2 5; 2 8 7", -306L);
        detIs("2 5 7; 0 3 9; 0 0 4", 24L);
        detIs("1 2 3; 4 5 6; 7 8 9", 0L);
        detIs("0 1 0; 0 0 1; 1 0 0", 1L);
        detIs("0 0 1; 0 1 0; 1 0 0", -1L);
        detIs("1 2 3; 1 2 3; 4 5 6", 0L);
        detIs("0 0 0; 1 2 3; 4 5 6", 0L);
        detIs("3 0 0; 0 -2 0; 0 0 5", -30L);
        detIs("1 0 2; -1 3 1; 2 1 0", -15L);
        detIs("0 2 1 3; 1 0 4 1; 2 1 0 1; 3 2 1 0", -42L);
        detIs("0 0 1 2; 0 3 0 1; 4 0 0 5; 1 1 1 1", 31L);
        detIs("1 2 3 4; 5 6 7 8; 9 10 11 12; 13 14 15 16", 0L);
        detIs("2 -1 0 3 1; 1 4 -2 0 5; 0 3 1 -1 2; -2 1 5 2 0; 3 0 -1 4 1", -225L);
        detIs("4 -9 0 2 -3 -1; 9 4 -4 2 -6 4; -1 8 -4 8 -4 2; -7 7 4 9 7 -4; -5 -3 -7 -3 1 1; -9 5 1 -9 7 4", -231034L);
        detIs("-9 -9 -4 9 -4 0 -6; 4 -6 9 -4 3 -2 7; 1 -8 -2 -6 6 -3 8; -4 -1 9 4 7 -5 -8; -4 0 -9 -5 -2 2 8; 1 -4 3 1 -9 -8 -8; 5 -7 3 -4 -9 2 -3", -15727627L);
        detIs("433494437 267914296; 267914296 165580141", 1L);
        detIs("701408733 433494437; 433494437 267914296", -1L);
        detIs("94906267 94906266; 94906266 94906265", -1L);
        group("determinant: shapes and laws", () -> {
            for (int n = 1; n <= 7; n++) {
                assertEquals("determinant of the " + n + "x" + n + " identity", 1, Matrix.identity(n).determinant());
            }
            assertThrows("determinant of 2x3", IllegalArgumentException.class, () -> m("1 2 3; 4 5 6").determinant());
            assertThrows("determinant of 3x2", IllegalArgumentException.class, () -> m("1 2; 3 4; 5 6").determinant());
            assertThrows("determinant of 1x2", IllegalArgumentException.class, () -> m("1 2").determinant());
            Matrix a = m("6 1 1; 4 -2 5; 2 8 7");
            Matrix b = m("1 0 2; -1 3 1; 2 1 0");
            assertEquals("det(A^T) = det(A)", a.determinant(), a.transpose().determinant());
            assertEquals("det(AB) = det(A) det(B)", a.determinant() * b.determinant(), a.multiply(b).determinant());
            assertEquals("det(2A) = 8 det(A)", 8 * a.determinant(), a.multiply(m("2 0 0; 0 2 0; 0 0 2")).determinant());
            assertEquals("swapping two rows negates the determinant", -a.determinant(), m("4 -2 5; 6 1 1; 2 8 7").determinant());
            assertEquals("the matrix is unchanged by determinant()", m("6 1 1; 4 -2 5; 2 8 7"), a);
        });
    }

    static void inverse() {
        inverseIs("1", "1");  // det 1
        inverseIs("-1", "-1");  // det -1
        noIntegerInverse("2");  // det 2
        noIntegerInverse("0");  // det 0
        noIntegerInverse("-2");  // det -2
        inverseIs("2 1; 1 1", "1 -1; -1 2");  // det 1
        inverseIs("3 2; 4 3", "3 -2; -4 3");  // det 1
        inverseIs("5 3; -2 -1", "-1 -3; 2 5");  // det 1
        inverseIs("0 1; 1 0", "0 1; 1 0");  // det -1
        inverseIs("-1 0; 0 -1", "-1 0; 0 -1");  // det 1
        inverseIs("1 5; 0 1", "1 -5; 0 1");  // det 1
        noIntegerInverse("1 2; 3 4");  // det -2
        noIntegerInverse("1 2; 2 4");  // det 0
        noIntegerInverse("2 0; 0 2");  // det 4
        noIntegerInverse("1 1; 1 1");  // det 0
        inverseIs("1 2 3; 0 1 4; 5 6 0", "-24 18 5; 20 -15 -4; -5 4 1");  // det 1
        inverseIs("1 0 0; 0 0 1; 0 1 0", "1 0 0; 0 0 1; 0 1 0");  // det -1
        inverseIs("1 2 0; 0 1 0; 0 0 1", "1 -2 0; 0 1 0; 0 0 1");  // det 1
        noIntegerInverse("2 0 0; 0 1 0; 0 0 1");  // det 2
        noIntegerInverse("0 0 0; 0 0 0; 0 0 0");  // det 0
        noIntegerInverse("1 2 3; 4 5 6; 7 8 9");  // det 0
        inverseIs("1 1 0 0; 0 1 1 0; 0 0 1 1; 0 0 0 1", "1 -1 1 -1; 0 1 -1 1; 0 0 1 -1; 0 0 0 1");  // det 1
        inverseIs("0 1 0 0; 1 0 0 0; 0 0 1 2; 0 0 0 1", "0 1 0 0; 1 0 0 0; 0 0 1 -2; 0 0 0 1");  // det -1
        inverseIs("1 2 3 4; 0 1 5 6; 0 0 1 7; 0 0 0 1", "1 -2 7 -41; 0 1 -5 29; 0 0 1 -7; 0 0 0 1");  // det 1
        inverseIs("2 1 0 0; 1 1 0 0; 0 0 3 2; 0 0 4 3", "1 -1 0 0; -1 2 0 0; 0 0 3 -2; 0 0 -4 3");  // det 1
        inverseIs("433494437 267914296; 267914296 165580141", "165580141 -267914296; -267914296 433494437");  // det 1
        inverseIs("701408733 433494437; 433494437 267914296", "-267914296 433494437; 433494437 -701408733");  // det -1
        group("inverse: shapes and laws", () -> {
            for (int n = 1; n <= 4; n++) {
                assertEquals("inverse of the " + n + "x" + n + " identity", Matrix.identity(n), Matrix.identity(n).inverse());
            }
            assertThrows("inverse of 2x3", IllegalArgumentException.class, () -> m("1 2 3; 4 5 6").inverse());
            assertThrows("inverse of 3x2", IllegalArgumentException.class, () -> m("1 2; 3 4; 5 6").inverse());
            assertThrows("inverse of 1x2", IllegalArgumentException.class, () -> m("1 2").inverse());
            String[] unimodular = {
                "2 1; 1 1",
                "1 2 3; 0 1 4; 5 6 0",
                "0 1 0 0; 1 0 0 0; 0 0 1 2; 0 0 0 1",
                "701408733 433494437; 433494437 267914296",
            };
            for (String s : unimodular) {
                Matrix a = m(s);
                Matrix inv = a.inverse();
                assertEquals("A x inverse(A) for " + s, Matrix.identity(a.rows()), a.multiply(inv));
                assertEquals("inverse(A) x A for " + s, Matrix.identity(a.rows()), inv.multiply(a));
                assertEquals("inverse(inverse(A)) for " + s, a, inv.inverse());
                assertEquals("the matrix is unchanged by inverse(): " + s, m(s), a);
            }
        });
    }
}
