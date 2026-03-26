package queue

import (
	"context"
	"testing"
	"time"

	"proxyllm/internal/domain"
)

func TestRequestQueue_PriorityAndFIFO(t *testing.T) {
	q := NewRequestQueue(10)
	
	// Enqueue 3 requests with different priorities and times
	req1 := &PendingRequest{ID: "low-1", Priority: 1, Timestamp: time.Now().Add(-10 * time.Second), Context: context.Background(), ResultChan: make(chan *domain.ExecutionResult, 1)}
	req2 := &PendingRequest{ID: "high-1", Priority: 10, Timestamp: time.Now(), Context: context.Background(), ResultChan: make(chan *domain.ExecutionResult, 1)}
	req3 := &PendingRequest{ID: "low-2", Priority: 1, Timestamp: time.Now(), Context: context.Background(), ResultChan: make(chan *domain.ExecutionResult, 1)}
	
	q.Enqueue(req1)
	q.Enqueue(req2)
	q.Enqueue(req3)
	
	// Should come out in order: req2 (highest priority), then req1 (older), then req3
	got1 := q.DequeueBlocking()
	if got1.ID != "high-1" {
		t.Errorf("expected high-1, got %s", got1.ID)
	}
	
	got2 := q.DequeueBlocking()
	if got2.ID != "low-1" {
		t.Errorf("expected low-1, got %s", got2.ID)
	}
	
	got3 := q.DequeueBlocking()
	if got3.ID != "low-2" {
		t.Errorf("expected low-2, got %s", got3.ID)
	}
}

func TestRequestQueue_LazyDeletion(t *testing.T) {
	q := NewRequestQueue(10)
	ctx, cancel := context.WithCancel(context.Background())
	
	req := &PendingRequest{ID: "cancelled", Context: ctx, ResultChan: make(chan *domain.ExecutionResult, 1)}
	q.Enqueue(req)
	
	// Cancel the request
	cancel()
	
	// Enqueue another valid one
	req2 := &PendingRequest{ID: "valid", Context: context.Background(), ResultChan: make(chan *domain.ExecutionResult, 1)}
	q.Enqueue(req2)
	
	// Dequeue should skip req1 and return req2
	got := q.DequeueBlocking()
	if got.ID != "valid" {
		t.Errorf("expected valid, got %s", got.ID)
	}
}

func TestRequestQueue_Close(t *testing.T) {
	q := NewRequestQueue(10)
	
	done := make(chan bool)
	go func() {
		res := q.DequeueBlocking()
		if res == nil {
			done <- true
		}
	}()
	
	time.Sleep(50 * time.Millisecond)
	q.Close()
	
	select {
	case <-done:
		// success
	case <-time.After(1 * time.Second):
		t.Error("DequeueBlocking did not unblock after Close()")
	}
}
