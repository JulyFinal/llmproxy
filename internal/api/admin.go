package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
	"log/slog"

	"proxyllm/internal/auth"
	"proxyllm/internal/config"
	"proxyllm/internal/domain"
	"proxyllm/internal/router"
	"proxyllm/internal/storage"
)

// AdminHandler serves the management API.
type AdminHandler struct {
	store   storage.Storage
	router  *router.Router
	limiter storage.RateLimiter
	logger  storage.Logger
	cfgMgr  *config.ConfigManager
}

func NewAdminHandler(s storage.Storage, r *router.Router, lim storage.RateLimiter, l storage.Logger, cfgMgr *config.ConfigManager) *AdminHandler {
	return &AdminHandler{store: s, router: r, limiter: lim, logger: l, cfgMgr: cfgMgr}
}

// RegisterRoutes mounts all admin routes under /admin/.
func (h *AdminHandler) RegisterRoutes(mux *http.ServeMux, mw func(http.Handler) http.Handler) {
	// Dashboard stats
	mux.Handle("/admin/stats", mw(http.HandlerFunc(h.stats)))
	mux.Handle("/admin/stats/timeseries", mw(http.HandlerFunc(h.statsTimeSeries)))
	mux.Handle("/admin/stats/top", mw(http.HandlerFunc(h.statsTop)))

	// Nodes
	mux.Handle("/admin/nodes", mw(http.HandlerFunc(h.nodes)))
	mux.Handle("/admin/nodes/", mw(http.HandlerFunc(h.nodeByID)))

	// API Keys
	mux.Handle("/admin/keys", mw(http.HandlerFunc(h.keys)))
	mux.Handle("/admin/keys/", mw(http.HandlerFunc(h.keyByID)))

	// Logs
	mux.Handle("/admin/logs", mw(http.HandlerFunc(h.logs)))
	mux.Handle("/admin/logs/export", mw(http.HandlerFunc(h.logExport)))
	mux.Handle("/admin/logs/", mw(http.HandlerFunc(h.logDetail)))
}

// ─── Nodes ────────────────────────────────────────────────────────────────────

func (h *AdminHandler) nodes(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		nodes, err := h.store.ListNodes(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, nodes)

	case http.MethodPost:
		var raw map[string]any
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}

		b, _ := json.Marshal(raw)
		var n domain.ModelNode
		_ = json.Unmarshal(b, &n)

		if n.ID == "" {
			n.ID = auth.GenerateID()
		}
		if n.EndpointType == "" {
			n.EndpointType = domain.EndpointAll
		}
		if _, ok := raw["enabled"]; !ok {
			n.Enabled = true
		}

		if err := h.store.UpsertNode(r.Context(), &n); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		h.syncRouter(r)

		writeJSON(w, http.StatusCreated, n)

	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *AdminHandler) nodeByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/admin/nodes/")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing node id")
		return
	}
	switch r.Method {
	case http.MethodGet:
		n, err := h.store.GetNode(r.Context(), id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if n == nil {
			writeError(w, http.StatusNotFound, "node not found")
			return
		}
		writeJSON(w, http.StatusOK, n)

	case http.MethodPut:
		var raw map[string]any
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}

		existing, err := h.store.GetNode(r.Context(), id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if existing == nil {
			writeError(w, http.StatusNotFound, "node not found")
			return
		}

		b, _ := json.Marshal(raw)
		var n domain.ModelNode
		_ = json.Unmarshal(b, &n)

		mergeNode(existing, &n, raw)

		existing.ID = id
		if err := h.store.UpsertNode(r.Context(), existing); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		h.syncRouter(r)

		writeJSON(w, http.StatusOK, existing)

	case http.MethodDelete:
		if err := h.store.DeleteNode(r.Context(), id); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		h.syncRouter(r)

		writeJSON(w, http.StatusOK, map[string]string{"deleted": id})

	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// syncRouter reloads all nodes from DB into the in-memory router and rate limiter.
func (h *AdminHandler) syncRouter(r *http.Request) {
	nodes, err := h.store.ListNodes(r.Context())
	if err != nil {
		slog.Error("failed to sync router", "err", err)
		return
	}
	h.router.Sync(nodes)

	// Also sync model-level rate limits if the limiter supports it.
	if updater, ok := h.limiter.(interface {
		UpdateModelLimits(map[string]domain.RateLimitConfig)
	}); ok {
		modelLimits := make(map[string]domain.RateLimitConfig)
		for _, node := range nodes {
			if node.TPM <= 0 && node.RPM <= 0 {
				continue
			}
			for _, alias := range node.Aliases {
				if _, exists := modelLimits[alias]; !exists {
					modelLimits[alias] = domain.RateLimitConfig{TPM: node.TPM, RPM: node.RPM}
				}
			}
		}
		updater.UpdateModelLimits(modelLimits)
	}
}

// ─── API Keys ─────────────────────────────────────────────────────────────────

func (h *AdminHandler) keys(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		keys, err := h.store.ListAPIKeys(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, keys)

	case http.MethodPost:
		var raw map[string]any
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}

		b, _ := json.Marshal(raw)
		var k domain.APIKey
		_ = json.Unmarshal(b, &k)

		k.ID = auth.GenerateID()
		if k.Key == "" {
			k.Key = auth.GenerateKey()
		}
		if _, ok := raw["enabled"]; !ok {
			k.Enabled = true
		}
		k.CreatedAt = time.Now().UTC()

		if err := h.store.UpsertAPIKey(r.Context(), &k); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		writeJSON(w, http.StatusCreated, k) // return full key on creation only

	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *AdminHandler) keyByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/admin/keys/")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing key id")
		return
	}
	switch r.Method {
	case http.MethodPut:
		var raw map[string]any
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}

		existing, err := h.store.GetAPIKey(r.Context(), id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if existing == nil {
			writeError(w, http.StatusNotFound, "key not found")
			return
		}

		b, _ := json.Marshal(raw)
		var k domain.APIKey
		_ = json.Unmarshal(b, &k)

		mergeKey(existing, &k, raw)

		existing.ID = id
		if err := h.store.UpsertAPIKey(r.Context(), existing); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		writeJSON(w, http.StatusOK, existing)

	case http.MethodDelete:
		if err := h.store.DeleteAPIKey(r.Context(), id); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		writeJSON(w, http.StatusOK, map[string]string{"deleted": id})

	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// ─── Logs ─────────────────────────────────────────────────────────────────────

func (h *AdminHandler) logs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	filter := parseLogFilter(r)

	logs, total, err := h.logger.QueryLogs(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"total": total,
		"page":  filter.Page,
		"data":  logs,
	})
}

func (h *AdminHandler) logExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	filter := parseLogFilter(r)
	logs, err := h.logger.ExportLogs(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/x-jsonlines")
	w.Header().Set("Content-Disposition", "attachment; filename=logs_export.jsonl")

	enc := json.NewEncoder(w)
	for _, log := range logs {
		if err := enc.Encode(log); err != nil {
			return
		}
	}
}

func (h *AdminHandler) logDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	traceID := strings.TrimPrefix(r.URL.Path, "/admin/logs/")
	if traceID == "" {
		writeError(w, http.StatusBadRequest, "missing trace id")
		return
	}
	detail, err := h.logger.GetDetail(r.Context(), traceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if detail == nil {
		writeError(w, http.StatusNotFound, "detail not found")
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func parseLogFilter(r *http.Request) domain.LogFilter {
	q := r.URL.Query()
	f := domain.LogFilter{
		APIKeyID:   q.Get("api_key_id"),
		ModelAlias: q.Get("model"),
		NodeID:     q.Get("node_id"),
		SessionID:  q.Get("session_id"),
		Keyword:    q.Get("keyword"),
		StatusCode: intQuery(q.Get("status_code"), 0),
		ErrorOnly:  q.Get("error_only") == "true" || q.Get("error_only") == "1",
		Page:       intQuery(q.Get("page"), 1),
		PageSize:   intQuery(q.Get("page_size"), 20),
	}
	if v := q.Get("start"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.StartTime = &t
		}
	}
	if v := q.Get("end"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.EndTime = &t
		}
	}
	return f
}

// ─── Stats ────────────────────────────────────────────────────────────────────

func (h *AdminHandler) stats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx := r.Context()
	filter := parseLogFilter(r)

	// If no time filter is provided, default to today
	if filter.StartTime == nil {
		now := time.Now().UTC()
		sod := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		filter.StartTime = &sod
	}

	logStats, err := h.logger.Stats(ctx, filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	nodes, _ := h.store.ListNodes(ctx)
	keys, _ := h.store.ListAPIKeys(ctx)

	activeNodes, totalNodes := 0, len(nodes)
	for _, n := range nodes {
		if n.Enabled {
			activeNodes++
		}
	}
	activeKeys := 0
	for _, k := range keys {
		if k.Enabled {
			activeKeys++
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"requests_today":    logStats.TotalRequests,
		"prompt_tokens":     logStats.PromptTokens,
		"completion_tokens": logStats.CompletionTokens,
		"tokens_today":      logStats.TotalTokens,
		"active_nodes":      activeNodes,
		"total_nodes":       totalNodes,
		"active_keys":       activeKeys,
		"total_keys":        len(keys),
	})
}

func (h *AdminHandler) statsTimeSeries(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	filter := parseLogFilter(r)
	granularity := r.URL.Query().Get("granularity")
	if granularity == "" {
		granularity = "day"
	}

	series, err := h.logger.StatsTimeSeries(r.Context(), filter, granularity)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, series)
}

func (h *AdminHandler) statsTop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	filter := parseLogFilter(r)
	groupBy := r.URL.Query().Get("group_by")
	if groupBy == "" {
		groupBy = "model_alias"
	}
	limit := intQuery(r.URL.Query().Get("limit"), 10)

	top, err := h.logger.StatsTop(r.Context(), filter, groupBy, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, top)
}

func intQuery(s string, def int) int {
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < 1 {
		return def
	}
	return v
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func mergeNode(dst, src *domain.ModelNode, raw map[string]any) {
	if _, ok := raw["name"]; ok {
		dst.Name = src.Name
	}
	if _, ok := raw["aliases"]; ok {
		dst.Aliases = src.Aliases
	}
	if _, ok := raw["base_url"]; ok {
		dst.BaseURL = src.BaseURL
	}
	if _, ok := raw["api_key"]; ok {
		dst.APIKey = src.APIKey
	}
	if _, ok := raw["model_name"]; ok {
		dst.ModelName = src.ModelName
	}
	if _, ok := raw["endpoint_type"]; ok {
		dst.EndpointType = src.EndpointType
	}
	if _, ok := raw["tpm"]; ok {
		dst.TPM = src.TPM
	}
	if _, ok := raw["rpm"]; ok {
		dst.RPM = src.RPM
	}
	if _, ok := raw["override"]; ok {
		dst.Override = src.Override
	}
	if _, ok := raw["timeout_sec"]; ok {
		dst.TimeoutSec = src.TimeoutSec
	}
	if _, ok := raw["enabled"]; ok {
		dst.Enabled = src.Enabled
	}
}

func mergeKey(dst, src *domain.APIKey, raw map[string]any) {
	if _, ok := raw["key"]; ok {
		dst.Key = src.Key
	}
	if _, ok := raw["name"]; ok {
		dst.Name = src.Name
	}
	if _, ok := raw["tpm"]; ok {
		dst.TPM = src.TPM
	}
	if _, ok := raw["rpm"]; ok {
		dst.RPM = src.RPM
	}
	if _, ok := raw["allow_models"]; ok {
		dst.AllowModels = src.AllowModels
	}
	if _, ok := raw["expires_at"]; ok {
		dst.ExpiresAt = src.ExpiresAt
	}
	if _, ok := raw["enabled"]; ok {
		dst.Enabled = src.Enabled
	}
}
