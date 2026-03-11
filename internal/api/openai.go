package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"proxyllm/internal/auth"
	"proxyllm/internal/domain"
	"proxyllm/internal/proxy"
	"proxyllm/internal/ratelimit"
	"proxyllm/internal/router"
	"proxyllm/internal/storage"
)

// OpenAIHandler handles all OpenAI-compatible endpoints.
type OpenAIHandler struct {
	router  *router.Router
	proxy   *proxy.Proxy
	limiter *ratelimit.Limiter
	logger  storage.Logger
}

func NewOpenAIHandler(
	r *router.Router,
	p *proxy.Proxy,
	l *ratelimit.Limiter,
	log storage.Logger,
) *OpenAIHandler {
	return &OpenAIHandler{router: r, proxy: p, limiter: l, logger: log}
}

// RegisterRoutes registers all OpenAI-compatible routes onto mux.
func (h *OpenAIHandler) RegisterRoutes(mux *http.ServeMux, mw func(http.Handler) http.Handler) {
	mux.Handle("/v1/chat/completions", mw(http.HandlerFunc(h.chatCompletions)))
	mux.Handle("/v1/completions", mw(http.HandlerFunc(h.chatCompletions)))
	mux.Handle("/v1/embeddings", mw(http.HandlerFunc(h.embeddings)))
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

	// Authorise model access.
	if !auth.CheckModelAllowed(pc.APIKey, modelAlias) {
		writeError(w, http.StatusForbidden, "model not allowed for this key")
		return
	}

	// Rate limit: RPM check.
	allowed, err := h.limiter.AllowRequest(ctx, pc.APIKey.ID, modelAlias)
	if err != nil {
		slog.Error("rate limiter error", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if !allowed {
		writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
		return
	}

	// Route.
	candidates := h.router.Resolve(modelAlias, et)
	if len(candidates) == 0 {
		writeError(w, http.StatusBadGateway, "no available upstream for model: "+modelAlias)
		return
	}
	node := router.Pick(candidates)
	if node == nil {
		writeError(w, http.StatusBadGateway, "no available upstream for model: "+modelAlias)
		return
	}

	pc.ModelAlias = modelAlias
	pc.TargetNode = node
	pc.Stream = isStream

	// Restore body for proxy.
	r.Body = io.NopCloser(&bytesReader{data: bodyBytes})

	result, fwdErr := h.proxy.Forward(ctx, node, r, w)

	// Parse token counts.
	var promptTokens, completionTokens int
	var responseBody string

	if result != nil {
		if result.Stream != nil {
			promptTokens = result.Stream.PromptTokens
			completionTokens = result.Stream.CompletionTokens
			responseBody = result.Stream.FullContent.String()
		} else if result.Body != nil {
			var respObj struct {
				Usage *struct {
					PromptTokens     int `json:"prompt_tokens"`
					CompletionTokens int `json:"completion_tokens"`
				} `json:"usage"`
			}
			if jsonErr := json.Unmarshal(result.Body, &respObj); jsonErr == nil && respObj.Usage != nil {
				promptTokens = respObj.Usage.PromptTokens
				completionTokens = respObj.Usage.CompletionTokens
			}
			responseBody = string(result.Body)
		}
	}

	totalTokens := promptTokens + completionTokens

	// Record TPM asynchronously — never blocks the response.
	if totalTokens > 0 {
		go func() {
			bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = h.limiter.RecordTokens(bgCtx, pc.APIKey.ID, modelAlias, totalTokens)
		}()
	}

	// Determine final status code and error message for logging.
	statusCode := http.StatusOK
	errMsg := ""
	if fwdErr != nil {
		errMsg = fwdErr.Error()
		if result == nil {
			statusCode = http.StatusBadGateway
		} else {
			statusCode = result.StatusCode
		}
	} else if result != nil {
		statusCode = result.StatusCode
	}

	durationMs := time.Since(pc.StartTime).Milliseconds()
	if result != nil {
		durationMs = result.DurationMs
	}

	// Emit async logs (never blocks).
	h.logger.AsyncLog(&domain.RequestLog{
		ID:               pc.RequestID,
		SessionID:        pc.SessionID,
		Timestamp:        pc.StartTime,
		APIKeyID:         pc.APIKey.ID,
		ModelAlias:       modelAlias,
		NodeID:           node.ID,
		ActualModel:      node.ModelName,
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      totalTokens,
		DurationMs:       durationMs,
		StatusCode:       statusCode,
		Stream:           isStream,
		ErrorMsg:         errMsg,
		HasDetail:        true,
	})
	h.logger.AsyncLogDetail(&domain.DetailLog{
		TraceID:      pc.RequestID,
		SessionID:    pc.SessionID,
		Timestamp:    pc.StartTime,
		RequestBody:  string(bodyBytes),
		ResponseBody: responseBody,
	})

	// Only write error response if proxy hasn't already written the header.
	if fwdErr != nil && result == nil {
		writeError(w, http.StatusBadGateway, "upstream error: "+fwdErr.Error())
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
