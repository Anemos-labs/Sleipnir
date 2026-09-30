import java.util.ArrayList;
import java.util.List;
import java.util.Objects;
import java.util.function.Consumer;

/**
 * A small synchronous event bus. README.md specifies the behaviour.
 */
public final class EventBus {
    /** One call of subscribe. It is compared by identity, so equal listeners stay distinct. */
    private static final class Registration {
        final Class<?> type;
        final Consumer<Object> listener;

        Registration(Class<?> type, Consumer<Object> listener) {
            this.type = type;
            this.listener = listener;
        }
    }

    private final List<Registration> registrations = new ArrayList<>();

    /** Calls listener for every event that is an instance of type, until the subscription is cancelled. */
    public <T> Subscription subscribe(Class<T> type, Consumer<? super T> listener) {
        Objects.requireNonNull(type, "type");
        Objects.requireNonNull(listener, "listener");
        Registration registration = new Registration(type, event -> listener.accept(type.cast(event)));
        registrations.add(registration);
        return () -> registrations.remove(registration);
    }

    /** Delivers event to the matching listeners, in the order in which they subscribed. */
    public void publish(Object event) {
        Objects.requireNonNull(event, "event");
        // A listener may subscribe or cancel while it runs: deliver to the listeners
        // that are subscribed right now, not to the list as it changes.
        for (Registration registration : List.copyOf(registrations)) {
            if (registration.type.isInstance(event)) {
                registration.listener.accept(event);
            }
        }
    }
}
