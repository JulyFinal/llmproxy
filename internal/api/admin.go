package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"proxyllm/internal/auth"
	"proxyllm/internal/config"
	"proxyllm/internal/domain"
	"proxyllm/internal/router"
	"proxyllm/internal/storage"
)

// AdminHandler serves the management API.
type AdminHandler struct {
	store  storage.Storage
	router *router.Router
	logger storage.Logger
	cfgMgr *config.ConfigManager
}

func NewAdminHandler(s storage.Storage, r *router.Router, l storage.Logger, cfgMgr *config.ConfigManager) *AdminHandler {
	return &AdminHandler{store: s, router: r, logger: l, cfgMgr: cfgMgr}
}

// RegisterRoutes mounts all admin routes under /admin/.
func (h *AdminHandler) RegisterRoutes(mux *http.ServeMux, mw func(http.Handler) http.Handler) {
	// Dashboard stats
	mux.Handle("/admin/stats", mw(http.HandlerFunc(h.stats)))

	// Nodes
	mux.Handle("/admin/nodes", mw(http.HandlerFunc(h.nodes)))
	mux.Handle("/admin/nodes/", mw(http.HandlerFunc(h.nodeByID)))

	// API Keys
	mux.Handle("/admin/keys", mw(http.HandlerFunc(h.keys)))
	mux.Handle("/admin/keys/", mw(http.HandlerFunc(h.keyByID)))

	// Logs
	mux.Handle("/admin/logs", mw(http.HandlerFunc(h.logs)))
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
		var n domain.ModelNode
		if err := json.NewDecoder(r.Body).Decode(&n); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if n.ID == "" {
			n.ID = auth.GenerateID()
		}
		if n.Priority == 0 {
			n.Priority = 99
		}
		if n.EndpointType == "" {
			n.EndpointType = domain.EndpointAll
		}
		n.Enabled = true
		if err := h.store.UpsertNode(r.Context(), &n); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		h.syncRouter(r)
		h.saveConfig(r.Context())
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
		var n domain.ModelNode
		if err := json.NewDecoder(r.Body).Decode(&n); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		n.ID = id
		if err := h.store.UpsertNode(r.Context(), &n); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		h.syncRouter(r)
		h.saveConfig(r.Context())
		writeJSON(w, http.StatusOK, n)

	case http.MethodDelete:
		if err := h.store.DeleteNode(r.Context(), id); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		h.syncRouter(r)
		h.saveConfig(r.Context())
		writeJSON(w, http.StatusOK, map[string]string{"deleted": id})

	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// syncRouter reloads all nodes from DB into the in-memory router.
func (h *AdminHandler) syncRouter(r *http.Request) {
	nodes, err := h.store.ListNodes(r.Context())
	if err == nil {
		h.router.Sync(nodes)
	}
}

// saveConfig persists the current nodes and API keys to the config file.
func (h *AdminHandler) saveConfig(ctx context.Context) {
	nodes, err1 := h.store.ListNodes(ctx)
	keys, err2 := h.store.ListAPIKeys(ctx)
	if err1 != nil || err2 != nil {
		slog.Error("saveConfig: list failed", "err1", err1, "err2", err2)
		return
	}
	if err := h.cfgMgr.Save(nodes, keys); err != nil {
		slog.Error("saveConfig: write failed", "err", err)
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
		var k domain.APIKey
		if err := json.NewDecoder(r.Body).Decode(&k); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		k.ID = auth.GenerateID()
		if k.Key == "" {
			k.Key = auth.GenerateKey()
		}
		k.Enabled = true
		k.CreatedAt = time.Now().UTC()
		if err := h.store.UpsertAPIKey(r.Context(), &k); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		h.saveConfig(r.Context())
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
		var k domain.APIKey
		if err := json.NewDecoder(r.Body).Decode(&k); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		k.ID = id
		if err := h.store.UpsertAPIKey(r.Context(), &k); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		h.saveConfig(r.Context())
		writeJSON(w, http.StatusOK, k)

	case http.MethodDelete:
		if err := h.store.DeleteAPIKey(r.Context(), id); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		h.saveConfig(r.Context())
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
	q := r.URL.Query()
	filter := domain.LogFilter{
		APIKeyID:   q.Get("api_key_id"),
		ModelAlias: q.Get("model"),
		NodeID:     q.Get("node_id"),
		Page:       intQuery(q.Get("page"), 1),
		PageSize:   intQuery(q.Get("page_size"), 20),
	}
	if v := q.Get("start"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err == nil {
			filter.StartTime = &t
		}
	}
	if v := q.Get("end"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err == nil {
			filter.EndTime = &t
		}
	}

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

// ─── Stats ────────────────────────────────────────────────────────────────────

func (h *AdminHandler) stats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx := r.Context()
	now := time.Now().UTC()
	sod := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	logStats, err := h.logger.Stats(ctx, sod)
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
