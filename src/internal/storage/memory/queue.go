package memory

import (
	"context"
	"errors"
	"sync"
)

const defaultBufferSize = 4096

// topicChannel holds the buffered channel for a single topic.
type topicChannel struct {
	ch chan []byte
}

// MemoryQueue is an in-memory implementation of storage.Queue.
// Each topic has its own buffered channel. Multiple subscribers on the same
// topic each receive every message via independent goroutines that fan-out
// from a single internal dispatcher goroutine per topic.
//
// Publish is non-blocking: if the topic buffer is full the message is dropped
// and an error is returned.
// Subscribe is non-blocking: it returns immediately after starting a goroutine.
// Close drains all in-flight handlers before returning.
type MemoryQueue struct {
	mu         sync.Mutex
	bufferSize int
	topics     map[string]*topicChannel
	// fanout tracks per-topic subscriber handler channels so that the
	// dispatcher can fan out to each subscriber independently.
	fanout map[string][]chan []byte
	// dispatchers holds the cancel func for each topic dispatcher goroutine.
	dispatchers map[string]context.CancelFunc
	wg          sync.WaitGroup
	closed      bool
}

// NewMemoryQueue returns a new MemoryQueue. bufferSize is the capacity of each
// topic's internal channel; pass 0 to use the default (4096).
func NewMemoryQueue(bufferSize int) *MemoryQueue {
	if bufferSize <= 0 {
		bufferSize = defaultBufferSize
	}
	return &MemoryQueue{
		bufferSize:  bufferSize,
		topics:      make(map[string]*topicChannel),
		fanout:      make(map[string][]chan []byte),
		dispatchers: make(map[string]context.CancelFunc),
	}
}

// ensureTopic creates the topic channel and dispatcher goroutine if they do
// not yet exist. Must be called with mu held.
func (q *MemoryQueue) ensureTopic(topic string) *topicChannel {
	tc, ok := q.topics[topic]
	if ok {
		return tc
	}
	tc = &topicChannel{ch: make(chan []byte, q.bufferSize)}
	q.topics[topic] = tc

	ctx, cancel := context.WithCancel(context.Background())
	q.dispatchers[topic] = cancel

	q.wg.Add(1)
	go q.dispatch(ctx, topic, tc.ch)

	return tc
}

// dispatch reads from the topic channel and fans each message out to every
// registered subscriber channel for that topic.
func (q *MemoryQueue) dispatch(ctx context.Context, topic string, src <-chan []byte) {
	defer q.wg.Done()
	for {
		select {
		case msg, ok := <-src:
			if !ok {
				// Channel closed; drain subscriber channels and exit.
				return
			}
			q.mu.Lock()
			subs := make([]chan []byte, len(q.fanout[topic]))
			copy(subs, q.fanout[topic])
			q.mu.Unlock()

			for _, sub := range subs {
				// Non-blocking send to subscriber channel; drop if full.
				select {
				case sub <- msg:
				default:
				}
			}
		case <-ctx.Done():
			return
		}
	}
}

// Publish sends payload to the given topic. If the topic buffer is full the
// message is dropped and ErrQueueFull is returned.
func (q *MemoryQueue) Publish(_ context.Context, topic string, payload []byte) error {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return errors.New("memory queue: closed")
	}
	tc := q.ensureTopic(topic)
	q.mu.Unlock()

	select {
	case tc.ch <- payload:
		return nil
	default:
		return errors.New("memory queue: topic buffer full, message dropped")
	}
}

// Subscribe registers handler to receive messages published to topic.
// It starts one goroutine per call and returns immediately.
// handler may be called concurrently if multiple messages arrive while a
// previous invocation is still running.
func (q *MemoryQueue) Subscribe(ctx context.Context, topic string, handler func(ctx context.Context, payload []byte) error) error {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return errors.New("memory queue: closed")
	}
	// Ensure the topic exists so the dispatcher is running.
	q.ensureTopic(topic)

	// Create a dedicated buffered channel for this subscriber.
	subCh := make(chan []byte, q.bufferSize)
	q.fanout[topic] = append(q.fanout[topic], subCh)
	q.mu.Unlock()

	q.wg.Add(1)
	go func() {
		defer q.wg.Done()
		for {
			select {
			case msg, ok := <-subCh:
				if !ok {
					return
				}
				// Invoke the handler; errors are intentionally ignored here —
				// callers that need retry logic should wrap the handler.
				_ = handler(ctx, msg)
			case <-ctx.Done():
				return
			}
		}
	}()

	return nil
}

// Close shuts down all topic channels and waits for all goroutines to finish.
func (q *MemoryQueue) Close() error {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return nil
	}
	q.closed = true

	// Cancel all dispatcher goroutines.
	for _, cancel := range q.dispatchers {
		cancel()
	}

	// Close all topic channels so dispatchers unblock from range reads.
	for _, tc := range q.topics {
		close(tc.ch)
	}

	// Close all subscriber channels so subscriber goroutines unblock.
	for _, subs := range q.fanout {
		for _, sub := range subs {
			close(sub)
		}
	}
	q.mu.Unlock()

	q.wg.Wait()
	return nil
}
