package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"proxyllm/internal/auth"
	"proxyllm/internal/domain"
)

// ─── CORS ─────────────────────────────────────────────────────────────────────

func corsMiddleware(origins []string) func(http.Handler) http.Handler {
	allowAll := len(origins) == 1 && origins[0] == "*"
	originSet := make(map[string]bool, len(origins))
	for _, o := range origins {
		originSet[o] = true
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && (allowAll || originSet[origin]) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type,Authorization,X-Session-Id")
				w.Header().Set("Access-Control-Max-Age", "86400")
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ─── TraceID injection ────────────────────────────────────────────────────────

func traceMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rid := r.Header.Get("X-Request-Id")
		if rid == "" {
			rid = auth.GenerateID()
		}
		sid := r.Header.Get("X-Session-Id")

		ctx := withRequestID(r.Context(), rid)
		ctx = withSessionID(ctx, sid)

		w.Header().Set("X-Request-Id", rid)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ─── Auth ─────────────────────────────────────────────────────────────────────

func authMiddleware(a *auth.Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := auth.ExtractToken(r)
			if !ok {
				writeError(w, http.StatusUnauthorized, "missing or invalid Authorization header")
				return
			}
			key, err := a.Authenticate(r.Context(), token)
			if err != nil {
				switch err {
				case auth.ErrExpired:
					writeError(w, http.StatusUnauthorized, "api key expired")
				default:
					writeError(w, http.StatusUnauthorized, "unauthorized")
				}
				return
			}

			pc := &domain.ProxyContext{
				RequestID: requestIDFrom(r.Context()),
				SessionID: sessionIDFrom(r.Context()),
				APIKey:    key,
				StartTime: time.Now(),
			}
			ctx := withProxyCtx(r.Context(), pc)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ─── Admin auth (separate token) ─────────────────────────────────────────────

func adminAuthMiddleware(adminToken string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if adminToken == "" {
				// No admin token configured: allow all (local-only use case)
				next.ServeHTTP(w, r)
				return
			}
			token, ok := auth.ExtractToken(r)
			if !ok || token != adminToken {
				writeError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ─── Access log ───────────────────────────────────────────────────────────────

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.status = code
	sr.ResponseWriter.WriteHeader(code)
}

func accessLogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(rec, r)
		slog.Info("access",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"request_id", requestIDFrom(r.Context()),
			"remote", r.RemoteAddr,
		)
	})
}

// ─── chain helper ─────────────────────────────────────────────────────────────

func chain(h http.Handler, middlewares ...func(http.Handler) http.Handler) http.Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		h = middlewares[i](h)
	}
	return h
}

// ─── error writer ─────────────────────────────────────────────────────────────

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": msg,
			"type":    strings.ToLower(http.StatusText(status)),
			"code":    status,
		},
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
