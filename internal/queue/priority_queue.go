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
	QueueWaitMs int64
	ExecutionMs int64
}

// priorityHeap implements heap.Interface for PendingRequest
type priorityHeap []*PendingRequest

func (h priorityHeap) Len() int { return len(h) }
func (h priorityHeap) Less(i, j int) bool {
	// Higher priority comes first. If priorities are equal, older timestamp comes first (FIFO)
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
	old[n-1] = nil // avoid memory leak
	*h = old[0 : n-1]
	return item
}

// RequestQueue provides a thread-safe, multi-model priority queue.
type RequestQueue struct {
	mu     sync.Mutex
	cond   *sync.Cond
	queues map[string]*priorityHeap // group by modelAlias
	count  int                      // Total items in all queues
	max    int
}

func NewRequestQueue(maxSize int) *RequestQueue {
	q := &RequestQueue{
		queues: make(map[string]*priorityHeap),
		max:    maxSize,
	}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// Enqueue adds a request to the queue.
// Returns the position of the request in its model queue, and the total length of that model queue.
func (q *RequestQueue) Enqueue(req *PendingRequest) (position int, length int) {
	q.mu.Lock()
	defer q.mu.Unlock()

	// Safety: prevent unlimited growth
	if q.max > 0 && q.count >= q.max {
		// Drop the request if we are at absolute capacity
		// For now we just enqueue it and risk going slightly over if max is hit, 
		// but ideally we'd reject. Let's return -1 if rejected.
		return -1, -1
	}

	modelQueue, ok := q.queues[req.ModelAlias]
	if !ok {
		mq := make(priorityHeap, 0)
		modelQueue = &mq
		heap.Init(modelQueue)
		q.queues[req.ModelAlias] = modelQueue
	}

	heap.Push(modelQueue, req)
	q.count++
	length = modelQueue.Len()
	
	// Since it's a heap, finding exact position is O(N).
	// We'll estimate position as length for now to save CPU.
	position = length 

	q.cond.Signal() // Wake up a waiting worker
	return position, length
}

// DequeueBlocking blocks until a request is available, then returns it.
// It implements Lazy Deletion: if a request's context is already cancelled, it drops it and pulls the next one.
func (q *RequestQueue) DequeueBlocking() *PendingRequest {
	q.mu.Lock()
	defer q.mu.Unlock()

	for {
		if q.count == 0 {
			q.cond.Wait()
		}

		// Find the highest priority request across all queues
		// To prevent starvation, a more complex scheduler could be used,
		// but for now we iterate and find the absolute highest priority item.
		var bestModel string
		var bestReq *PendingRequest

		for modelAlias, h := range q.queues {
			if h.Len() > 0 {
				peek := (*h)[0]
				if bestReq == nil {
					bestReq = peek
					bestModel = modelAlias
				} else if peek.Priority > bestReq.Priority || (peek.Priority == bestReq.Priority && peek.Timestamp.Before(bestReq.Timestamp)) {
					bestReq = peek
					bestModel = modelAlias
				}
			}
		}

		if bestReq != nil {
			// Pop it
			item := heap.Pop(q.queues[bestModel]).(*PendingRequest)
			q.count--

			// Lazy Deletion Check: has the client already disconnected or timed out?
			if item.Context.Err() != nil {
				// Client gone, skip and look for another
				continue
			}

			return item
		}
	}
}

// Remove is implemented for compatibility, but primarily we rely on lazy deletion in DequeueBlocking.
// O(N) operation, so we only use this if absolutely necessary.
func (q *RequestQueue) Remove(requestID string, modelAlias string) int {
	q.mu.Lock()
	defer q.mu.Unlock()

	h, ok := q.queues[modelAlias]
	if !ok {
		return -1
	}

	for i, req := range *h {
		if req.ID == requestID {
			heap.Remove(h, i)
			q.count--
			return i
		}
	}
	return -1
}
