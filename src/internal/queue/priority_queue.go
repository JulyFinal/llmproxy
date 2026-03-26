package queue

import (
	"container/heap"
	"context"
	"net/http"
	"sync"
	"time"

	"proxyllm/internal/domain"
)

// PendingRequest represents an incoming request waiting in the queue to be processed.
type PendingRequest struct {
	ID           string
	SessionID    string
	RequestID    string
	Priority     int // Higher number = higher priority
	APIKeyID     string
	ModelAlias   string
	EndpointType domain.EndpointType
	Timestamp    time.Time // Time when enqueued

	// HTTP Context & IO
	BodyBytes []byte // Buffered request body
	Headers   http.Header
	Method    string
	Path      string

	// Original ResponseWriter to allow streaming straight to client
	ResponseWriter http.ResponseWriter

	// Signaling channels
	ResultChan chan *domain.ExecutionResult

	// Context for cancellation
	Context context.Context
	Cancel  context.CancelFunc

	// Telemetry
	QueueWaitMs      int64
	ExecutionMs      int64
	RatelimitRetries int
}

// priorityHeap implements heap.Interface for PendingRequest
type priorityHeap []*PendingRequest

func (h priorityHeap) Len() int { return len(h) }
func (h priorityHeap) Less(i, j int) bool {
	if h[i].Priority == h[j].Priority {
		return h[i].Timestamp.Before(h[j].Timestamp)
	}
	return h[i].Priority > h[j].Priority
}
func (h priorityHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *priorityHeap) Push(x interface{}) {
	*h = append(*h, x.(*PendingRequest))
}

func (h *priorityHeap) Pop() interface{} {
	old := *h
	n := len(old)
	item := old[n-1]
	old[n-1] = nil
	*h = old[0 : n-1]
	return item
}

// RequestQueue provides a thread-safe priority queue backed by a single global heap.
// DequeueBlocking is O(log N) instead of O(M) where M = number of distinct models.
type RequestQueue struct {
	mu     sync.Mutex
	cond   *sync.Cond
	heap   priorityHeap
	max    int
	closed bool
}

func NewRequestQueue(maxSize int) *RequestQueue {
	q := &RequestQueue{
		max: maxSize,
	}
	q.cond = sync.NewCond(&q.mu)
	heap.Init(&q.heap)
	return q
}

func (q *RequestQueue) Close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.cond.Broadcast()
}

// Enqueue adds a request to the queue.
// Returns (position estimate, total queue length), or (-1,-1) if full/closed.
func (q *RequestQueue) Enqueue(req *PendingRequest) (position int, length int) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.closed || (q.max > 0 && q.heap.Len() >= q.max) {
		return -1, -1
	}

	heap.Push(&q.heap, req)
	length = q.heap.Len()
	// Position estimate: count items with higher or equal priority that arrived earlier.
	position = 0
	for _, r := range q.heap {
		if r.Priority > req.Priority || (r.Priority == req.Priority && r.Timestamp.Before(req.Timestamp)) {
			position++
		}
	}

	q.cond.Signal()
	return position, length
}

// DequeueBlocking blocks until a request is available, then returns it.
// Skips cancelled requests (lazy deletion). Returns nil when closed and empty.
func (q *RequestQueue) DequeueBlocking() *PendingRequest {
	q.mu.Lock()
	defer q.mu.Unlock()

	for {
		for q.heap.Len() == 0 && !q.closed {
			q.cond.Wait()
		}
		if q.closed && q.heap.Len() == 0 {
			return nil
		}

		item := heap.Pop(&q.heap).(*PendingRequest)
		if item.Context.Err() != nil {
			continue // lazy deletion
		}
		return item
	}
}

// Length returns the total number of pending requests.
func (q *RequestQueue) Length() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.heap.Len()
}

// Remove removes a request by ID. O(N) — use sparingly; prefer lazy deletion.
func (q *RequestQueue) Remove(requestID string, modelAlias string) int {
	q.mu.Lock()
	defer q.mu.Unlock()

	for i, req := range q.heap {
		if req.ID == requestID {
			heap.Remove(&q.heap, i)
			return i
		}
	}
	return -1
}
