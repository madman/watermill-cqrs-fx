package cqrs

// Notifier signals a worker that new work is available in the queue or outbox.
// The default implementation is in-process (buffered channel with capacity 1, non-blocking Notify).
type Notifier interface {
	// Notify sends an idempotent, non-blocking signal to wake up listening workers.
	// Multiple calls coalesce into a single signal if a signal is already pending.
	Notify()
	// C returns the receive-only channel that workers select on.
	C() <-chan struct{}
}

type channelNotifier struct {
	ch chan struct{}
}

// NewChannelNotifier returns a non-blocking in-process Notifier that coalesces wake-up signals.
func NewChannelNotifier() Notifier {
	return &channelNotifier{
		ch: make(chan struct{}, 1),
	}
}

func (n *channelNotifier) Notify() {
	select {
	case n.ch <- struct{}{}:
	default:
	}
}

func (n *channelNotifier) C() <-chan struct{} {
	return n.ch
}

// NotifierProvider is an optional interface implemented by components (such as CommandBus or workers)
// that can expose their wake-up Notifier to enable explicit sharing across components.
type NotifierProvider interface {
	Notifier() Notifier
}
