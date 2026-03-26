package worker

import (
	"bytes"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"proxyllm/internal/domain"
	"proxyllm/internal/logging"
	"proxyllm/internal/metrics"
	"proxyllm/internal/proxy"
	"proxyllm/internal/queue"
	"proxyllm/internal/ratelimit"
	"proxyllm/internal/router"
)

// retryableRecorder buffers output until we are sure we won't retry.
type retryableRecorder struct {
	w           http.ResponseWriter
	header      http.Header
	statusCode  int
	wroteHeader bool
	body        bytes.Buffer
	commit      bool
}

func newRetryableRecorder(w http.ResponseWriter) *retryableRecorder {
	return &retryableRecorder{
		w:        w,
		header:   make(http.Header),
	}
}

func (r *retryableRecorder) Header() http.Header {
	return r.header
}

func (r *retryableRecorder) WriteHeader(statusCode int) {
	if r.wroteHeader { return }
	r.statusCode = statusCode
	r.wroteHeader = true

	// Commit immediately for non-retryable status codes.
	// For streams: commit on success so SSE data flows to client;
	//              buffer on retryable errors so we can retry with another node.
	errorType := logging.ClassifyError(nil, statusCode)
	if !logging.IsRetryable(errorType) {
		r.Commit()
	}
}

func (r *retryableRecorder) Commit() {
	if r.commit { return }
	r.commit = true
	// Copy headers to the real writer
	h := r.w.Header()
	for k, v := range r.header {
		h[k] = v
	}
	if r.statusCode == 0 {
		r.statusCode = http.StatusOK
	}
	r.w.WriteHeader(r.statusCode)
}

func (r *retryableRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	if r.commit {
		return r.w.Write(b)
	}
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
	wg          sync.WaitGroup

	blockedCount atomic.Uint64
	activeWorker atomic.Int32
}

func NewWorkerPool(q *queue.RequestQueue, r *router.Router, p *proxy.Proxy, l *ratelimit.Limiter, log *logging.ChainLogger, cfg *WorkerConfig) *WorkerPool {
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
	p.wg.Add(p.config.WorkerCount + 1)
	for i := 0; i < p.config.WorkerCount; i++ {
		go p.worker(i)
	}
	go p.monitor()
}

func (p *WorkerPool) Stop() {
	close(p.stopCh)
	p.queue.Close()
	p.wg.Wait()
}

func (p *WorkerPool) monitor() {
	defer p.wg.Done()
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-p.stopCh: return
		case <-ticker.C:
			qLen := p.queue.Length()
			blocked := p.blockedCount.Swap(0)
			active := p.activeWorker.Load()
			if qLen > 0 || blocked > 0 {
				slog.Info("⚙ POOL", "queue", qLen, "active", active, "blocked", blocked)
			}
		}
	}
}

func (p *WorkerPool) worker(id int) {
	defer p.wg.Done()
	for {
		select {
		case <-p.stopCh: return
		default:
		}

		req := p.queue.DequeueBlocking()
		if req == nil { return }

		p.activeWorker.Add(1)
		allowed, _, err := p.limiter.AllowRequest(req.Context, req.APIKeyID, req.ModelAlias)
		
		if !allowed || err != nil {
			p.blockedCount.Add(1)
			p.activeWorker.Add(-1)
			go func(r *queue.PendingRequest) {
				time.Sleep(100 * time.Millisecond)
				if r.Context.Err() == nil { p.queue.Enqueue(r) }
			}(req)
			continue
		}

		metrics.Default.IncWorkerBusy()
		result := p.forwardWithRetry(req)
		metrics.Default.DecWorkerBusy()
		p.activeWorker.Add(-1)

		select {
		case req.ResultChan <- result:
		case <-req.Context.Done():
		}
	}
}

func (p *WorkerPool) forwardWithRetry(req *queue.PendingRequest) *domain.ExecutionResult {
	startTime := time.Now()
	candidates := p.router.Resolve(req.ModelAlias, req.EndpointType)
	if len(candidates) == 0 {
		return &domain.ExecutionResult{StatusCode: 502, Error: "no nodes", ExecutionMs: time.Since(startTime).Milliseconds()}
	}

	excludedNodes := make(map[string]bool)
	var attempts []*domain.AttemptLog

	for attempt := 1; attempt <= p.config.MaxRetryAttempts; attempt++ {
		select {
		case <-req.Context.Done():
			p.logger.LogRequestCancelled(req.Context, req, attempt, "")
			return &domain.ExecutionResult{StatusCode: 499, Error: "cancelled", Attempts: attempts, ExecutionMs: time.Since(startTime).Milliseconds()}
		default:
		}

		node := pickNode(candidates, excludedNodes)
		if node == nil { break }

		p.logger.LogNodeAttemptStart(req.Context, req, attempt, node)
		httpReq, _ := p.buildHTTPRequest(req, node)
		
		// Use the specific recorder that supports buffering and manual commit
		rec := newRetryableRecorder(req.ResponseWriter)
		
		attemptStart := time.Now()
		fwdRes, fwdErr := p.proxy.Forward(req.Context, node, httpReq, rec)
		attemptDuration := time.Since(attemptStart).Milliseconds()

		statusCode := 0
		if fwdRes != nil { statusCode = fwdRes.StatusCode } else if rec.statusCode != 0 { statusCode = rec.statusCode }

		errorType := logging.ClassifyError(fwdErr, statusCode)
		retryable := logging.IsRetryable(errorType)
		
		attemptLog := &domain.AttemptLog{
			Attempt: attempt, NodeID: node.ID, NodeName: node.Name,
			DurationMs: attemptDuration, StatusCode: statusCode, ErrorType: errorType, Error: fwdErr,
		}
		if fwdRes != nil && fwdRes.Stream != nil {
			attemptLog.PromptTokens = fwdRes.Stream.PromptTokens
			attemptLog.CompletionTokens = fwdRes.Stream.CompletionTokens
		}
		attempts = append(attempts, attemptLog)

		// Success logic
		if fwdErr == nil && !retryable {
			// For streaming, the proxy already handles committing first chunk.
			// For non-streaming, recorder commits on success automatically.
			res := &domain.ExecutionResult{StatusCode: statusCode, FinalNodeID: node.ID, Attempts: attempts, ExecutionMs: time.Since(startTime).Milliseconds()}
			if fwdRes != nil {
				if fwdRes.Stream != nil {
					res.PromptTokens = fwdRes.Stream.PromptTokens
					res.CompletionTokens = fwdRes.Stream.CompletionTokens
					res.TotalTokens = res.PromptTokens + res.CompletionTokens
				} else { res.Body = fwdRes.Body }
			}
			p.logger.LogNodeAttemptSuccess(req.Context, req, attempt, node, res)
			return res
		}

		// Failed: add to excluded
		excludedNodes[node.ID] = true
		
		willRetry := attempt < p.config.MaxRetryAttempts && pickNode(candidates, excludedNodes) != nil
		p.logger.LogNodeAttemptFailed(req.Context, req, attempt, node, fwdErr, statusCode, attemptDuration, willRetry, "")

		if !willRetry {
			// Failed all attempts: flush whatever we have in recorder
			if !rec.commit && rec.wroteHeader {
				rec.Commit()
				rec.w.Write(rec.body.Bytes())
			}
			return &domain.ExecutionResult{StatusCode: statusCode, Error: errorType, ErrorType: errorType, Attempts: attempts, ExecutionMs: time.Since(startTime).Milliseconds()}
		}

		if p.config.RetryDelayMs > 0 { time.Sleep(time.Duration(p.config.RetryDelayMs) * time.Millisecond) }
	}

	return &domain.ExecutionResult{StatusCode: 502, Error: "all failed", Attempts: attempts, ExecutionMs: time.Since(startTime).Milliseconds()}
}

func pickNode(candidates []*domain.ModelNode, excluded map[string]bool) *domain.ModelNode {
	var available []*domain.ModelNode
	for _, n := range candidates {
		if !excluded[n.ID] && n.Enabled { available = append(available, n) }
	}
	if len(available) == 0 { return nil }
	return router.Pick(available)
}

func (p *WorkerPool) buildHTTPRequest(req *queue.PendingRequest, node *domain.ModelNode) (*http.Request, error) {
	r, _ := http.NewRequestWithContext(req.Context, req.Method, req.Path, bytes.NewReader(req.BodyBytes))
	r.Header = req.Headers.Clone()
	return r, nil
}
