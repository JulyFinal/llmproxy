package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"proxyllm/internal/auth"
	"proxyllm/internal/domain"
	"proxyllm/internal/logging"
	"proxyllm/internal/queue"
	"proxyllm/internal/ratelimit"
	"proxyllm/internal/router"
	"proxyllm/internal/storage"
)

// OpenAIHandler handles all OpenAI-compatible endpoints.
type OpenAIHandler struct {
	router      *router.Router
	limiter     *ratelimit.Limiter
	logger      storage.Logger
	chainLogger *logging.ChainLogger
	queue       *queue.RequestQueue
	config      *domain.AppConfig
}

func NewOpenAIHandler(
	r *router.Router,
	l *ratelimit.Limiter,
	log storage.Logger,
	chainLog *logging.ChainLogger,
	q *queue.RequestQueue,
	cfg *domain.AppConfig,
) *OpenAIHandler {
	return &OpenAIHandler{
		router:      r,
		limiter:     l,
		logger:      log,
		chainLogger: chainLog,
		queue:       q,
		config:      cfg,
	}
}

// RegisterRoutes registers all OpenAI-compatible routes onto mux.
func (h *OpenAIHandler) RegisterRoutes(mux *http.ServeMux, mw func(http.Handler) http.Handler) {
	mux.Handle("/v1/chat/completions", mw(http.HandlerFunc(h.chatCompletions)))
	mux.Handle("/v1/completions", mw(http.HandlerFunc(h.chatCompletions)))
	mux.Handle("/v1/embeddings", mw(http.HandlerFunc(h.embeddings)))
	mux.Handle("/v1/responses", mw(http.HandlerFunc(h.responses)))
	mux.Handle("/v1/models", mw(http.HandlerFunc(h.listModels)))
}

// ─── /v1/chat/completions ─────────────────────────────────────────────────────

func (h *OpenAIHandler) chatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	h.forward(w, r, domain.EndpointChat)
}

// ─── /v1/responses ────────────────────────────────────────────────────────────

func (h *OpenAIHandler) responses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	h.forward(w, r, domain.EndpointResponses)
}

// ─── /v1/embeddings ───────────────────────────────────────────────────────────

func (h *OpenAIHandler) embeddings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	h.forward(w, r, domain.EndpointEmbedding)
}

// ─── /v1/models ───────────────────────────────────────────────────────────────

func (h *OpenAIHandler) listModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	nodes := h.router.ListEnabled()
	type modelObj struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		OwnedBy string `json:"owned_by"`
	}
	seen := make(map[string]bool)
	var models []modelObj
	for _, n := range nodes {
		for _, alias := range n.Aliases {
			if !seen[alias] {
				seen[alias] = true
				models = append(models, modelObj{ID: alias, Object: "model", OwnedBy: "proxyllm"})
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   models,
	})
}

// ─── core forward logic ───────────────────────────────────────────────────────

func (h *OpenAIHandler) forward(w http.ResponseWriter, r *http.Request, et domain.EndpointType) {
	ctx := r.Context()
	pc := proxyCtxFrom(ctx)

	// Read body once; proxy will re-wrap it.
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "cannot read request body")
		return
	}
	r.Body.Close()

	var reqBody map[string]any
	if err := json.Unmarshal(bodyBytes, &reqBody); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	modelAlias, _ := reqBody["model"].(string)
	if modelAlias == "" {
		writeError(w, http.StatusBadRequest, "missing 'model' field")
		return
	}
	isStream, _ := reqBody["stream"].(bool)

	// Extract priority (optional, default 0 or from config)
	priority := h.config.Queue.DefaultPriority
	if p, ok := reqBody["priority"].(float64); ok {
		priority = int(p)
	}

	// Authorise model access.
	if !auth.CheckModelAllowed(pc.APIKey, modelAlias) {
		writeError(w, http.StatusForbidden, "model not allowed for this key")
		return
	}

	// Create pending request
	maxWaitSec := h.config.Worker.MaxWaitTimeSec
	if maxWaitSec <= 0 {
		maxWaitSec = 1800 // default 30 min
	}
	reqCtx, cancel := context.WithTimeout(ctx, time.Duration(maxWaitSec)*time.Second)
	defer cancel()

	pending := &queue.PendingRequest{
		ID:             pc.RequestID,
		SessionID:      pc.SessionID,
		RequestID:      pc.RequestID,
		Priority:       priority,
		APIKeyID:       pc.APIKey.ID,
		ModelAlias:     modelAlias,
		EndpointType:   et,
		Timestamp:      time.Now(),
		BodyBytes:      bodyBytes,
		Headers:        r.Header,
		Method:         r.Method,
		Path:           r.URL.Path,
		ResponseWriter: w,
		ResultChan:     make(chan *domain.ExecutionResult, 1),
		Context:        reqCtx,
		Cancel:         cancel,
	}

	// Log arrival
	h.chainLogger.LogRequestReceived(ctx, pending)

	// Enqueue
	position, length := h.queue.Enqueue(pending)
	if position == -1 {
		writeError(w, http.StatusServiceUnavailable, "queue is full")
		return
	}
	h.chainLogger.LogEnqueued(ctx, pending, position, length)

	// Wait for result or timeout
	select {
	case result := <-pending.ResultChan:
		pending.ExecutionMs = result.ExecutionMs
		pending.QueueWaitMs = time.Since(pending.Timestamp).Milliseconds() - result.ExecutionMs

		if result.Error == "" {
			h.chainLogger.LogRequestCompleted(ctx, pending, result)
		} else {
			h.chainLogger.LogRequestFailed(ctx, pending, result.Attempts)
			// Wait, the recorder handles writing to w for retries, but if it exhausted ALL retries,
			// it MIGHT have flushed an error. If it didn't flush, we should write the error here.
			// Actually, WorkerPool flushes if it doesn't retry. We don't need to writeError here
			// unless we really need to.
			// But let's check if we should writeError if nothing was written.
			// WorkerPool's rec.commit logic guarantees it flushes the last error. So nothing to do here.
		}

		// Asynchronous tracking
		totalTokens := result.TotalTokens
		promptTokens := result.PromptTokens
		completionTokens := result.CompletionTokens

		if totalTokens <= 0 && len(result.Body) > 0 {
			var usage struct {
				Usage struct {
					Prompt     int `json:"prompt_tokens"`
					Completion int `json:"completion_tokens"`
					Total      int `json:"total_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal(result.Body, &usage); err == nil && usage.Usage.Total > 0 {
				totalTokens = usage.Usage.Total
				promptTokens = usage.Usage.Prompt
				completionTokens = usage.Usage.Completion
			}
		}

		if totalTokens > 0 {
			go func() {
				bgCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
				defer c()
				_ = h.limiter.RecordTokens(bgCtx, pc.APIKey.ID, modelAlias, totalTokens)
			}()
		}

		// SQLite Logging
		h.logger.AsyncLog(&domain.RequestLog{
			ID:               pc.RequestID,
			SessionID:        pc.SessionID,
			Timestamp:        pc.StartTime,
			APIKeyID:         pc.APIKey.ID,
			ModelAlias:       modelAlias,
			NodeID:           result.FinalNodeID,
			ActualModel:      "", // We could extract this if needed, but omitted for brevity
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
			TotalTokens:      totalTokens,
			DurationMs:       time.Since(pc.StartTime).Milliseconds(),
			StatusCode:       result.StatusCode,
			Stream:           isStream,
			ErrorMsg:         result.Error,
			HasDetail:        true,
		})
		h.logger.AsyncLogDetail(&domain.DetailLog{
			TraceID:      pc.RequestID,
			SessionID:    pc.SessionID,
			Timestamp:    pc.StartTime,
			RequestBody:  string(bodyBytes),
			ResponseBody: string(result.Body),
		})

	case <-reqCtx.Done():
		if errors.Is(reqCtx.Err(), context.DeadlineExceeded) {
			// Queue wait timeout
			// Best effort remove from queue (O(N) in heap, but we use lazy deletion mostly)
			// Actually, we'll let lazy deletion handle it, no need to manually Remove.
			// Just log it.
			h.chainLogger.LogRequestTimeout(ctx, pending, maxWaitSec, -1)
			writeError(w, http.StatusGatewayTimeout, "request timeout after waiting in queue")
		} else {
			// Client disconnected
			// Let lazy deletion handle it
			h.chainLogger.LogRequestCancelled(ctx, pending, 0, "")
		}
	}
}

// ─── helpers ──────────────────────────────────────────────────────────────────

type bytesReader struct {
	data []byte
	pos  int
}

func (b *bytesReader) Read(p []byte) (n int, err error) {
	if b.pos >= len(b.data) {
		return 0, io.EOF
	}
	n = copy(p, b.data[b.pos:])
	b.pos += n
	return n, nil
}
