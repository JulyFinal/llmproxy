package worker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"proxyllm/internal/domain"
	"proxyllm/internal/logging"
	"proxyllm/internal/proxy"
	"proxyllm/internal/queue"
	"proxyllm/internal/ratelimit"
	"proxyllm/internal/router"
	"proxyllm/internal/storage/memory"
)

func TestWorkerPool_RetryAndSuccess(t *testing.T) {
	// 1. Setup Mock Upstreams
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			// First attempt fails with 503
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		// Subsequent attempts succeed
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer server.Close()

	// 2. Setup Dependencies
	cache := memory.NewMemoryCache()
	q := queue.NewRequestQueue(10)
	rt := router.New()
	p := proxy.New()
	lim := ratelimit.New(cache, domain.RateLimitConfig{RPM: 100}, nil, nil)
	log := logging.NewChainLogger(nil)
	
	// Add TWO nodes so retry has somewhere to go
	rt.Sync([]*domain.ModelNode{
		{ID: "node-1", BaseURL: server.URL, Enabled: true, Aliases: []string{"m1"}, EndpointType: domain.EndpointAll},
		{ID: "node-2", BaseURL: server.URL, Enabled: true, Aliases: []string{"m1"}, EndpointType: domain.EndpointAll},
	})

	cfg := &WorkerConfig{
		WorkerCount:      1,
		MaxRetryAttempts: 3,
		RetryDelayMs:     1,
	}
	
	pool := NewWorkerPool(q, rt, p, lim, log, cfg)
	pool.Start()
	defer pool.Stop()

	// 3. Enqueue Request
	resChan := make(chan *domain.ExecutionResult, 1)
	req := &queue.PendingRequest{
		ID:             "req-1",
		ModelAlias:     "m1",
		EndpointType:   domain.EndpointChat,
		Timestamp:      time.Now(),
		BodyBytes:      []byte("{}"),
		ResponseWriter: httptest.NewRecorder(),
		ResultChan:     resChan,
		Context:        context.Background(),
	}
	q.Enqueue(req)

	// 4. Verify result
	select {
	case result := <-resChan:
		if result.StatusCode != http.StatusOK {
			t.Errorf("expected 200, got %d. error: %s", result.StatusCode, result.Error)
		}
		if len(result.Attempts) != 2 {
			t.Errorf("expected 2 attempts, got %d", len(result.Attempts))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for worker result")
	}
}
