import java.util.ArrayList;
import java.util.List;

/**
 * Plain-Java tests (there is no JUnit). Every failed check prints a line, and the
 * exit status is 1 if any check failed.
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
        group("types", () -> {
            EventBus bus = new EventBus();
            List<String> log = new ArrayList<>();
            bus.subscribe(String.class, s -> log.add("string:" + s));
            bus.subscribe(Object.class, o -> log.add("object:" + o));
            bus.subscribe(Number.class, n -> log.add("number:" + n));
            bus.publish("hi");
            assertEquals("a String", List.of("string:hi", "object:hi"), log);
            log.clear();
            bus.publish(42);
            assertEquals("an Integer", List.of("object:42", "number:42"), log);
        });

        group("order", () -> {
            EventBus bus = new EventBus();
            List<String> log = new ArrayList<>();
            bus.subscribe(String.class, s -> log.add("A"));
            bus.subscribe(Object.class, o -> log.add("B"));
            bus.subscribe(String.class, s -> log.add("C"));
            bus.publish("x");
            assertEquals("listeners run in subscription order", List.of("A", "B", "C"), log);
        });

        group("cancel between events", () -> {
            EventBus bus = new EventBus();
            List<String> log = new ArrayList<>();
            bus.subscribe(String.class, s -> log.add("A"));
            Subscription b = bus.subscribe(String.class, s -> log.add("B"));
            bus.subscribe(String.class, s -> log.add("C"));
            b.cancel();
            b.cancel();
            bus.publish("x");
            assertEquals("a cancelled subscription is not called", List.of("A", "C"), log);
        });

        group("a one-shot listener that cancels itself", () -> {
            EventBus bus = new EventBus();
            List<String> log = new ArrayList<>();
            Subscription[] once = new Subscription[1];
            bus.subscribe(String.class, s -> log.add("A:" + s));
            once[0] = bus.subscribe(String.class, s -> {
                log.add("once:" + s);
                once[0].cancel();
            });
            bus.subscribe(String.class, s -> log.add("B:" + s));
            bus.publish("1");
            assertEquals("first event", List.of("A:1", "once:1", "B:1"), log);
            log.clear();
            bus.publish("2");
            assertEquals("second event", List.of("A:2", "B:2"), log);
        });

        // The first example of README.md.
        group("cancelled during delivery", () -> {
            EventBus bus = new EventBus();
            List<String> log = new ArrayList<>();
            Subscription[] b = new Subscription[1];
            bus.subscribe(String.class, s -> {
                log.add("a:" + s);
                b[0].cancel();
            });
            b[0] = bus.subscribe(String.class, s -> log.add("b:" + s));
            bus.publish("x");
            assertEquals("event x", List.of("a:x", "b:x"), log);
            bus.publish("y");
            assertEquals("after event y", List.of("a:x", "b:x", "a:y"), log);
        });

        // The second example of README.md.
        group("subscribed during delivery", () -> {
            EventBus bus = new EventBus();
            List<String> log = new ArrayList<>();
            bus.subscribe(String.class, s -> {
                log.add("a:" + s);
                if (s.equals("x")) {
                    bus.subscribe(String.class, t -> log.add("late:" + t));
                }
            });
            bus.publish("x");
            assertEquals("event x", List.of("a:x"), log);
            bus.publish("y");
            assertEquals("after event y", List.of("a:x", "a:y", "late:y"), log);
        });

        System.out.println(checks + " checks, " + failures + " failed");
        if (failures > 0) {
            System.exit(1);
        }
    }
}
