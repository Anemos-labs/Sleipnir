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

    static void detIs(String rows, long expected) {
        assertEquals("determinant of " + rows, expected, m(rows).determinant());
    }

    static void productIs(String a, String b, String expected) {
        assertEquals("(" + a + ") x (" + b + ")", m(expected), m(a).multiply(m(b)));
    }

    static void inverseIs(String rows, String expected) {
        assertEquals("inverse of " + rows, m(expected), m(rows).inverse());
    }

    static void noIntegerInverse(String rows) {
        assertThrows("inverse of " + rows, ArithmeticException.class, () -> m(rows).inverse());
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

    public static void main(String[] args) {
        // The examples of README.md.
        group("transpose", () -> {
            assertEquals("transpose of 2x2", m("1 3; 2 4"), m("1 2; 3 4").transpose());
            assertEquals("transpose of 2x3", m("1 4; 2 5; 3 6"), m("1 2 3; 4 5 6").transpose());
        });
        group("multiply", () -> {
            productIs("1 2; 3 4", "0 1; 1 0", "2 1; 4 3");
            productIs("1 2 3; 4 5 6", "7 8; 9 10; 11 12", "58 64; 139 154");
            assertThrows("1x3 times 2x2", IllegalArgumentException.class, () -> m("1 2 3").multiply(m("1 2; 3 4")));
        });
        group("determinant", () -> {
            detIs("5", 5);
            detIs("1 2; 3 4", -2);
            detIs("0 1; 1 0", -1);
            detIs("6 1 1; 4 -2 5; 2 8 7", -306);
            assertThrows("determinant of 2x3", IllegalArgumentException.class, () -> m("1 2 3; 4 5 6").determinant());
        });
        group("inverse", () -> {
            inverseIs("2 1; 1 1", "1 -1; -1 2");
            noIntegerInverse("1 2; 3 4");
        });

        System.out.println(checks + " checks, " + failures + " failed");
        if (failures > 0) {
            System.exit(1);
        }
    }
}
