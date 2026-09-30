# EventBus

A small synchronous event bus (Java 21, default package, no dependencies):
`src/EventBus.java` and `src/Subscription.java`.

```java
public final class EventBus {
    public <T> Subscription subscribe(Class<T> type, Consumer<? super T> listener);
    public void publish(Object event);
}

public interface Subscription {
    void cancel();
}
```

## Behaviour

1. **Types.** A listener subscribed with `type` receives every published event `e`
   for which `type.isInstance(e)` is true. A listener for `Object.class` receives
   every event, one for `Number.class` receives Integers and Doubles, one for an
   interface such as `CharSequence.class` receives all its implementations.

2. **Synchronous.** `publish` calls the matching listeners on the calling thread,
   one after the other, and returns after the last of them has returned. A listener
   may itself call `publish`: that nested event is delivered completely, to all its
   listeners, before the outer delivery goes on with its next listener. The bus is
   used from one thread only.

3. **Order.** The matching listeners are called in the order in which they were
   subscribed, whatever their types.

4. **Snapshot.** The listeners that receive an event are exactly the ones that are
   subscribed at the moment `publish` is called. What a listener changes while an
   event is being delivered only affects later events:
   - a listener that is subscribed during the delivery does **not** receive the
     event in progress;
   - a subscription that is cancelled during the delivery (by its own listener or by
     another one) **still receives the event in progress** if its turn has not come
     yet. It receives no later event.

5. **Subscriptions.** Every call of `subscribe` creates a subscription of its own,
   even when the same listener object is subscribed more than once (it is then
   called once for each subscription). `cancel()` ends that one subscription only,
   and calling it again does nothing.

## Examples

```java
EventBus bus = new EventBus();
List<String> log = new ArrayList<>();

// A subscription cancelled during delivery still gets the event in progress.
Subscription[] b = new Subscription[1];
bus.subscribe(String.class, s -> { log.add("a:" + s); b[0].cancel(); });
b[0] = bus.subscribe(String.class, s -> log.add("b:" + s));
bus.publish("x");   // log: [a:x, b:x]
bus.publish("y");   // log: [a:x, b:x, a:y]
```

```java
EventBus bus = new EventBus();
List<String> log = new ArrayList<>();

// A listener subscribed during delivery only gets later events.
bus.subscribe(String.class, s -> {
    log.add("a:" + s);
    if (s.equals("x")) bus.subscribe(String.class, t -> log.add("late:" + t));
});
bus.publish("x");   // log: [a:x]
bus.publish("y");   // log: [a:x, a:y, late:y]
```

## Building and testing

There is no JUnit. `test/TestMain.java` is a plain program with a tiny assertion
helper; it prints one line per failed check and exits with a non-zero status if any
check fails.

```
rm -rf build && mkdir -p build && javac -d build $(find . -name '*.java') && java -cp build TestMain
```

`test/TestMain.java` contains the examples above and a few more. More tests that
check the rules above are run when your solution is verified.
