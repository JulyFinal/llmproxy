package worker

import (
	"bytes"
	"fmt"
	"net/http"
	"time"

	"proxyllm/internal/domain"
	"proxyllm/internal/logging"
	"proxyllm/internal/proxy"
	"proxyllm/internal/queue"
	"proxyllm/internal/ratelimit"
	"proxyllm/internal/router"
)

// retryableRecorder intercepts Writes. If the status is retryable (like 5xx or 429),
// it buffers the response so it can be discarded if we retry.
// If the status is 200 OK, it immediately flushes to the original writer.
type retryableRecorder struct {
	w           http.ResponseWriter
	statusCode  int
	wroteHeader bool
	body        bytes.Buffer
	commit      bool // true if we decided not to retry and are streaming to client
}

func (r *retryableRecorder) Header() http.Header {
	if r.commit {
		return r.w.Header()
	}
	return r.w.Header()
}

func (r *retryableRecorder) WriteHeader(statusCode int) {
	if r.wroteHeader {
		return
	}
	r.statusCode = statusCode
	r.wroteHeader = true

	// If it's a success or a non-retryable error (e.g. 400), we commit to sending it to client.
	errorType := logging.ClassifyError(nil, statusCode)
	if !logging.IsRetryable(errorType) {
		r.commit = true
		r.w.WriteHeader(statusCode)
	}
}

func (r *retryableRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	if r.commit {
		return r.w.Write(b)
	}
	// Buffer it if we might retry
	return r.body.Write(b)
}

func (r *retryableRecorder) Flush() {
	if f, ok := r.w.(http.Flusher); ok && r.commit {
		f.Flush()
	}
}

type WorkerConfig struct {
	WorkerCount      int
	MaxRetryAttempts int
	RetryDelayMs     int
	MaxWaitTime      time.Duration
}

type WorkerPool struct {
	queue       *queue.RequestQueue
	router      *router.Router
	proxy       *proxy.Proxy
	limiter     *ratelimit.Limiter
	logger      *logging.ChainLogger
	config      *WorkerConfig
	stopCh      chan struct{}
}

func NewWorkerPool(
	q *queue.RequestQueue,
	r *router.Router,
	p *proxy.Proxy,
	l *ratelimit.Limiter,
	log *logging.ChainLogger,
	cfg *WorkerConfig,
) *WorkerPool {
	return &WorkerPool{
		queue:   q,
		router:  r,
		proxy:   p,
		limiter: l,
		logger:  log,
		config:  cfg,
		stopCh:  make(chan struct{}),
	}
}

func (p *WorkerPool) Start() {
	for i := 0; i < p.config.WorkerCount; i++ {
		go p.worker(i)
	}
}

func (p *WorkerPool) Stop() {
	close(p.stopCh)
}

func (p *WorkerPool) worker(id int) {
	workerID := fmt.Sprintf("worker-%d", id)

	for {
		// Check for shutdown
		select {
		case <-p.stopCh:
			return
		default:
		}

		// Blocking dequeue
		req := p.queue.DequeueBlocking()
		if req == nil {
			continue // Should only happen on shutdown or queue clear
		}

		p.logger.LogDequeued(req.Context, req, workerID)

		// Check Rate Limits
		allowed, err := p.limiter.AllowRequest(req.Context, req.APIKeyID, req.ModelAlias)
		
		status := domain.RateLimitStatus{} // Simplified, exact status hard to extract without touching limiter internals
		if err != nil {
			status.BlockReason = "cache_error"
		} else if !allowed {
			status.BlockReason = "limit_exceeded"
		}

		p.logger.LogRateLimitCheck(req.Context, req, allowed, status)

		if !allowed || err != nil {
			// Non-blocking backoff retry
			// Send back to queue after a short delay so we don't stall THIS worker
			go func(r *queue.PendingRequest) {
				time.Sleep(100 * time.Millisecond)
				// Re-enqueue
				if r.Context.Err() == nil {
					p.queue.Enqueue(r)
				}
			}(req)
			continue
		}

		// Execute
		result := p.forwardWithRetry(req)

		// Send result back to original handler
		select {
		case req.ResultChan <- result:
		case <-req.Context.Done():
			// Client disconnected before we could return the result (or while returning)
		}
	}
}

func (p *WorkerPool) forwardWithRetry(req *queue.PendingRequest) *domain.ExecutionResult {
	startTime := time.Now()

	candidates := p.router.Resolve(req.ModelAlias, req.EndpointType)
	if len(candidates) == 0 {
		return &domain.ExecutionResult{
			StatusCode:  http.StatusBadGateway,
			Error:       fmt.Sprintf("no available nodes for model: %s", req.ModelAlias),
			ExecutionMs: time.Since(startTime).Milliseconds(),
		}
	}

	excludedNodes := make(map[string]bool)
	var attempts []*domain.AttemptLog

	for attempt := 1; attempt <= p.config.MaxRetryAttempts; attempt++ {
		// Check client cancellation
		select {
		case <-req.Context.Done():
			p.logger.LogRequestCancelled(req.Context, req, attempt, "")
			return &domain.ExecutionResult{
				StatusCode:  499,
				Error:       "client disconnected",
				Attempts:    attempts,
				ExecutionMs: time.Since(startTime).Milliseconds(),
			}
		default:
		}

		node := pickNode(candidates, excludedNodes)
		if node == nil {
			break // Exhausted nodes
		}

		attemptStart := time.Now()
		p.logger.LogNodeAttemptStart(req.Context, req, attempt, node)

		httpReq, err := p.buildHTTPRequest(req, node)
		if err != nil {
			return &domain.ExecutionResult{
				StatusCode:  http.StatusInternalServerError,
				Error:       "failed to build request: " + err.Error(),
				ExecutionMs: time.Since(startTime).Milliseconds(),
			}
		}

		// Forward!
		// Use our recorder to prevent writing to the client if the upstream returns a 503
		rec := &retryableRecorder{w: req.ResponseWriter}

		fwdRes, fwdErr := p.proxy.Forward(req.Context, node, httpReq, rec)
		attemptDuration := time.Since(attemptStart).Milliseconds()

		attemptLog := &domain.AttemptLog{
			Attempt:    attempt,
			NodeID:     node.ID,
			NodeName:   node.Name,
			DurationMs: attemptDuration,
			Error:      fwdErr,
		}

		if fwdRes != nil {
			attemptLog.StatusCode = fwdRes.StatusCode
			if fwdRes.Stream != nil {
				attemptLog.PromptTokens = fwdRes.Stream.PromptTokens
				attemptLog.CompletionTokens = fwdRes.Stream.CompletionTokens
			}
		} else if fwdErr != nil {
			attemptLog.StatusCode = http.StatusBadGateway
		} else if rec.statusCode != 0 {
			attemptLog.StatusCode = rec.statusCode
		}

		attempts = append(attempts, attemptLog)

		// Determine if we can retry
		errorType := logging.ClassifyError(fwdErr, attemptLog.StatusCode)
		attemptLog.ErrorType = errorType
		retryable := logging.IsRetryable(errorType)
		willRetry := retryable && attempt < p.config.MaxRetryAttempts

		// If we succeed or if it's a non-retryable error, we are done with this request.
		// If it was a non-retryable error, rec.commit is already true and it streamed.
		// If it's a retryable error but we are out of attempts, we must flush the buffered error to the client.
		if fwdErr == nil && !retryable {
			exRes := &domain.ExecutionResult{
				StatusCode:  fwdRes.StatusCode,
				FinalNodeID: node.ID,
				Attempts:    attempts,
				ExecutionMs: time.Since(startTime).Milliseconds(),
			}
			if fwdRes.Stream != nil {
				exRes.PromptTokens = fwdRes.Stream.PromptTokens
				exRes.CompletionTokens = fwdRes.Stream.CompletionTokens
				exRes.TotalTokens = fwdRes.Stream.PromptTokens + fwdRes.Stream.CompletionTokens
			} else {
				exRes.Body = fwdRes.Body
			}
			p.logger.LogNodeAttemptSuccess(req.Context, req, attempt, node, exRes)
			return exRes
		}

		if !willRetry {
			// We have exhausted retries or it's non-retryable, and we didn't commit.
			// Force flush the buffered error response to the client.
			if !rec.commit && rec.wroteHeader {
				req.ResponseWriter.WriteHeader(rec.statusCode)
				req.ResponseWriter.Write(rec.body.Bytes())
			}

			return &domain.ExecutionResult{
				StatusCode:  attemptLog.StatusCode,
				Error:       attemptLog.ErrorType, // fallback msg
				ErrorType:   errorType,
				Attempts:    attempts,
				ExecutionMs: time.Since(startTime).Milliseconds(),
			}
		}

		// Logging a retry
		nextNodeID := ""
		excludedNodes[node.ID] = true
		if n := pickNode(candidates, excludedNodes); n != nil {
			nextNodeID = n.ID
		} else {
			willRetry = false
		}
		delete(excludedNodes, node.ID)

		p.logger.LogNodeAttemptFailed(req.Context, req, attempt, node, fwdErr, attemptLog.StatusCode, attemptDuration, willRetry, nextNodeID)

		if !willRetry {
			if !rec.commit && rec.wroteHeader {
				req.ResponseWriter.WriteHeader(rec.statusCode)
				req.ResponseWriter.Write(rec.body.Bytes())
			}
			return &domain.ExecutionResult{
				StatusCode:  attemptLog.StatusCode,
				Error:       "no more nodes",
				ErrorType:   errorType,
				Attempts:    attempts,
				ExecutionMs: time.Since(startTime).Milliseconds(),
			}
		}

		// Proceed to retry
		excludedNodes[node.ID] = true
		// Clear headers written by the failed attempt so the next attempt can write fresh headers
		for k := range req.ResponseWriter.Header() {
			delete(req.ResponseWriter.Header(), k)
		}

		if p.config.RetryDelayMs > 0 && attempt < p.config.MaxRetryAttempts {
			time.Sleep(time.Duration(p.config.RetryDelayMs) * time.Millisecond)
		}
	}

	return &domain.ExecutionResult{
		StatusCode:  http.StatusBadGateway,
		Error:       fmt.Sprintf("all %d attempts failed", len(attempts)),
		Attempts:    attempts,
		ExecutionMs: time.Since(startTime).Milliseconds(),
	}
}

func pickNode(candidates []*domain.ModelNode, excluded map[string]bool) *domain.ModelNode {
	var available []*domain.ModelNode
	for _, node := range candidates {
		if !excluded[node.ID] && node.Enabled {
			available = append(available, node)
		}
	}
	if len(available) == 0 {
		return nil
	}
	return router.Pick(available)
}

func (p *WorkerPool) buildHTTPRequest(req *queue.PendingRequest, node *domain.ModelNode) (*http.Request, error) {
	r, err := http.NewRequestWithContext(req.Context, req.Method, req.Path, bytes.NewReader(req.BodyBytes))
	if err != nil {
		return nil, err
	}
	r.Header = req.Headers.Clone()
	return r, nil
}
