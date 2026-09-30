/** The handle of one subscription to an {@link EventBus}. */
public interface Subscription {
    /** Ends this subscription. Calling it again does nothing. */
    void cancel();
}
