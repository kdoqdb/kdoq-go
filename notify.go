package kdoq

import "sync"

type stateEvent struct {
	state State
	err   error
}

// notifier delivers state changes to the OnState callback in order from one
// library goroutine, so that a slow callback never blocks the client.
type notifier struct {
	fn      func(State, error)
	mu      sync.Mutex
	queue   []stateEvent
	running bool
}

func newNotifier(fn func(State, error)) *notifier {
	if fn == nil {
		return nil
	}
	return &notifier{fn: fn}
}

func (n *notifier) emit(state State, err error) {
	if n == nil {
		return
	}
	n.mu.Lock()
	n.queue = append(n.queue, stateEvent{state, err})
	if !n.running {
		n.running = true
		go n.run()
	}
	n.mu.Unlock()
}

func (n *notifier) run() {
	for {
		n.mu.Lock()
		if len(n.queue) == 0 {
			n.running = false
			n.mu.Unlock()
			return
		}
		ev := n.queue[0]
		n.queue = n.queue[1:]
		n.mu.Unlock()
		n.fn(ev.state, ev.err)
	}
}
