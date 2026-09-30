import java.util.ArrayList;
import java.util.List;
import java.util.function.Consumer;

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
            bus.subscribe(CharSequence.class, c -> log.add("chars:" + c));
            bus.publish("hi");
            assertEquals("a String", List.of("string:hi", "object:hi", "chars:hi"), log);
            log.clear();
            bus.publish(42);
            assertEquals("an Integer", List.of("object:42", "number:42"), log);
            log.clear();
            bus.publish(2.5);
            assertEquals("a Double", List.of("object:2.5", "number:2.5"), log);
            log.clear();
            bus.publish(new StringBuilder("sb"));
            assertEquals("a StringBuilder (a CharSequence, not a String)", List.of("object:sb", "chars:sb"), log);
            log.clear();
            bus.publish(List.of(1));
            assertEquals("a List", List.of("object:[1]"), log);
        });

        group("no matching listener", () -> {
            EventBus bus = new EventBus();
            bus.publish("nobody is listening");
            List<String> log = new ArrayList<>();
            bus.subscribe(Integer.class, i -> log.add("int:" + i));
            bus.publish("a string");
            assertEquals("an Integer listener does not see a String", List.of(), log);
        });

        group("order", () -> {
            EventBus bus = new EventBus();
            List<String> log = new ArrayList<>();
            bus.subscribe(String.class, s -> log.add("A"));
            bus.subscribe(Object.class, o -> log.add("B"));
            bus.subscribe(String.class, s -> log.add("C"));
            bus.publish("x");
            assertEquals("a String", List.of("A", "B", "C"), log);
            log.clear();
            bus.publish(1);
            assertEquals("an Integer", List.of("B"), log);
        });

        group("cancel between events", () -> {
            EventBus bus = new EventBus();
            List<String> log = new ArrayList<>();
            Subscription a = bus.subscribe(String.class, s -> log.add("A"));
            Subscription b = bus.subscribe(String.class, s -> log.add("B"));
            bus.subscribe(String.class, s -> log.add("C"));
            bus.publish("1");
            assertEquals("all three", List.of("A", "B", "C"), log);
            b.cancel();
            log.clear();
            bus.publish("2");
            assertEquals("B cancelled", List.of("A", "C"), log);
            a.cancel();
            a.cancel();
            log.clear();
            bus.publish("3");
            assertEquals("A cancelled twice", List.of("C"), log);
            b.cancel();
            log.clear();
            bus.publish("4");
            assertEquals("cancelling B again changes nothing", List.of("C"), log);
        });

        group("the same listener subscribed twice", () -> {
            EventBus bus = new EventBus();
            List<String> log = new ArrayList<>();
            Consumer<String> listener = s -> log.add("L");
            Subscription first = bus.subscribe(String.class, listener);
            bus.subscribe(String.class, s -> log.add("between"));
            Subscription second = bus.subscribe(String.class, listener);
            bus.publish("x");
            assertEquals("called once per subscription", List.of("L", "between", "L"), log);
            second.cancel();
            log.clear();
            bus.publish("y");
            assertEquals("the second subscription is gone, the first is not", List.of("L", "between"), log);
            second.cancel();
            log.clear();
            bus.publish("z");
            assertEquals("cancelling again removes nothing more", List.of("L", "between"), log);
            first.cancel();
            log.clear();
            bus.publish("w");
            assertEquals("both gone", List.of("between"), log);
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
            bus.subscribe(String.class, s -> log.add("C:" + s));
            bus.publish("1");
            assertEquals("first event", List.of("A:1", "once:1", "B:1", "C:1"), log);
            log.clear();
            bus.publish("2");
            assertEquals("second event", List.of("A:2", "B:2", "C:2"), log);
        });

        group("a one-shot listener in first place", () -> {
            EventBus bus = new EventBus();
            List<String> log = new ArrayList<>();
            Subscription[] once = new Subscription[1];
            once[0] = bus.subscribe(String.class, s -> {
                log.add("once:" + s);
                once[0].cancel();
            });
            bus.subscribe(String.class, s -> log.add("B:" + s));
            bus.publish("1");
            assertEquals("first event", List.of("once:1", "B:1"), log);
            log.clear();
            bus.publish("2");
            assertEquals("second event", List.of("B:2"), log);
        });

        group("a one-shot listener in last place", () -> {
            EventBus bus = new EventBus();
            List<String> log = new ArrayList<>();
            Subscription[] once = new Subscription[1];
            bus.subscribe(String.class, s -> log.add("A:" + s));
            once[0] = bus.subscribe(String.class, s -> {
                log.add("once:" + s);
                once[0].cancel();
            });
            bus.publish("1");
            assertEquals("first event", List.of("A:1", "once:1"), log);
            log.clear();
            bus.publish("2");
            assertEquals("second event", List.of("A:2"), log);
        });

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

        group("one listener cancels two others", () -> {
            EventBus bus = new EventBus();
            List<String> log = new ArrayList<>();
            Subscription[] earlier = new Subscription[1];
            Subscription[] later = new Subscription[1];
            earlier[0] = bus.subscribe(String.class, s -> log.add("first:" + s));
            bus.subscribe(String.class, s -> {
                log.add("killer:" + s);
                later[0].cancel();
                earlier[0].cancel();
            });
            later[0] = bus.subscribe(String.class, s -> log.add("later:" + s));
            bus.subscribe(String.class, s -> log.add("last:" + s));
            bus.publish("1");
            assertEquals("first event: everyone that was subscribed when it was published",
                List.of("first:1", "killer:1", "later:1", "last:1"), log);
            log.clear();
            bus.publish("2");
            assertEquals("second event", List.of("killer:2", "last:2"), log);
        });

        group("subscribed during delivery", () -> {
            EventBus bus = new EventBus();
            List<String> log = new ArrayList<>();
            bus.subscribe(String.class, s -> {
                log.add("a:" + s);
                if (s.equals("x")) {
                    bus.subscribe(String.class, t -> log.add("late:" + t));
                }
            });
            bus.subscribe(String.class, s -> log.add("b:" + s));
            bus.publish("x");
            assertEquals("event x", List.of("a:x", "b:x"), log);
            log.clear();
            bus.publish("y");
            assertEquals("event y", List.of("a:y", "b:y", "late:y"), log);
        });

        group("subscribing from the last listener", () -> {
            EventBus bus = new EventBus();
            List<String> log = new ArrayList<>();
            bus.subscribe(String.class, s -> {
                log.add("only:" + s);
                if (s.equals("1")) {
                    bus.subscribe(String.class, t -> log.add("new:" + t));
                }
            });
            bus.publish("1");
            assertEquals("event 1", List.of("only:1"), log);
            log.clear();
            bus.publish("2");
            assertEquals("event 2", List.of("only:2", "new:2"), log);
        });

        group("nested publish", () -> {
            EventBus bus = new EventBus();
            List<String> log = new ArrayList<>();
            bus.subscribe(String.class, s -> {
                log.add("outer-start:" + s);
                if (s.equals("go")) {
                    bus.publish(7);
                }
                log.add("outer-end:" + s);
            });
            bus.subscribe(Integer.class, i -> log.add("int:" + i));
            bus.subscribe(String.class, s -> log.add("second:" + s));
            bus.publish("go");
            assertEquals("the nested event is delivered in full first",
                List.of("outer-start:go", "int:7", "outer-end:go", "second:go"), log);

            EventBus countdown = new EventBus();
            List<String> steps = new ArrayList<>();
            countdown.subscribe(Integer.class, n -> {
                steps.add("n" + n);
                if (n > 0) {
                    countdown.publish(n - 1);
                }
            });
            countdown.publish(2);
            assertEquals("recursive publishing", List.of("n2", "n1", "n0"), steps);
        });

        group("a thousand one-shot listeners", () -> {
            EventBus bus = new EventBus();
            List<String> log = new ArrayList<>();
            int[] calls = {0};
            for (int i = 0; i < 1000; i++) {
                Subscription[] self = new Subscription[1];
                self[0] = bus.subscribe(String.class, s -> {
                    calls[0]++;
                    self[0].cancel();
                });
            }
            bus.subscribe(String.class, s -> log.add("survivor"));
            bus.publish("x");
            assertEquals("every one-shot listener ran once", 1000, calls[0]);
            assertEquals("the survivor ran", List.of("survivor"), log);
            bus.publish("y");
            assertEquals("no one-shot listener ran again", 1000, calls[0]);
            assertEquals("the survivor ran again", List.of("survivor", "survivor"), log);
        });

        group("subscribe returns a subscription", () -> {
            EventBus bus = new EventBus();
            assertTrue("a subscription is returned", bus.subscribe(String.class, s -> { }) != null);
        });

        System.out.println(checks + " checks, " + failures + " failed");
        if (failures > 0) {
            System.exit(1);
        }
    }
}
