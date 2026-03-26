package api

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"

	"proxyllm/internal/assets"
	"proxyllm/internal/auth"
	"proxyllm/internal/config"
	"proxyllm/internal/domain"
	"proxyllm/internal/logging"
	"proxyllm/internal/metrics"
	"proxyllm/internal/proxy"
	"proxyllm/internal/queue"
	"proxyllm/internal/ratelimit"
	"proxyllm/internal/router"
	"proxyllm/internal/storage"
)

// Server assembles all handlers and middleware into a single http.Server.
type Server struct {
	httpServer *http.Server
	logger     storage.Logger
}

// Deps groups all resolved dependencies needed to build the server.
type Deps struct {
	Config      *domain.AppConfig
	AdminCfg    struct{ Token string; Addr string; CORSOrigins []string }
	Store       storage.Storage
	Router      *router.Router
	Proxy       *proxy.Proxy
	Limiter     *ratelimit.Limiter
	Logger      storage.Logger
	ChainLogger *logging.ChainLogger
	Queue       *queue.RequestQueue
	CfgMgr      *config.ConfigManager
}

func NewServer(deps Deps) *Server {
	mux := http.NewServeMux()

	authenticator := auth.NewAuthenticator(deps.Store)

	// Middleware stacks
	// OpenAI routes: CORS → trace → access-log → auth
	openAIMW := func(h http.Handler) http.Handler {
		return chain(h,
			corsMiddleware(deps.AdminCfg.CORSOrigins),
			traceMiddleware,
			accessLogMiddleware,
			authMiddleware(authenticator),
		)
	}

	// Admin routes: CORS → trace → access-log → admin-auth
	adminMW := func(h http.Handler) http.Handler {
		return chain(h,
			corsMiddleware(deps.AdminCfg.CORSOrigins),
			traceMiddleware,
			accessLogMiddleware,
			adminAuthMiddleware(deps.AdminCfg.Token),
		)
	}

	// Register route groups
	oaiHandler := NewOpenAIHandler(deps.Router, deps.Limiter, deps.Logger, deps.ChainLogger, deps.Queue, deps.Config)
	oaiHandler.RegisterRoutes(mux, openAIMW)

	adminHandler := NewAdminHandler(deps.Store, deps.Router, deps.Limiter, deps.Logger, deps.CfgMgr)
	adminHandler.RegisterRoutes(mux, adminMW)

	// Metrics (protected by admin token)
	mux.Handle("/metrics", adminMW(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		metrics.Default.WritePrometheus(w)
	})))

	// Health check (no auth)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		status := "ok"
		checks := make(map[string]string)

		// Check DB
		if _, err := deps.Store.ListNodes(r.Context()); err != nil {
			status = "error"
			checks["db"] = "failed: " + err.Error()
		} else {
			checks["db"] = "ok"
		}

		// Check Limiter (Cache) - side-effect free
		if err := deps.Limiter.Check(r.Context()); err != nil {
			status = "error"
			checks["cache"] = "failed: " + err.Error()
		} else {
			checks["cache"] = "ok"
		}

		code := http.StatusOK
		if status != "ok" {
			code = http.StatusServiceUnavailable
		}
		writeJSON(w, code, map[string]any{
			"status": status,
			"checks": checks,
		})
	})

	// Static UI — served from embedded assets
	staticFS, err := fs.Sub(assets.Static, "static")
	if err != nil {
		panic("assets: " + err.Error())
	}
	uiHandler := http.FileServer(http.FS(staticFS))
	mux.Handle("/ui/", http.StripPrefix("/ui/", uiHandler))
	// Redirect root to /ui/
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/ui/", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	})

	return &Server{
		httpServer: &http.Server{
			Addr:    deps.AdminCfg.Addr,
			Handler: mux,
		},
		logger: deps.Logger,
	}
}

// ListenAndServe starts the HTTP server (blocks until shutdown).
func (s *Server) ListenAndServe() error {
	slog.Info("proxyllm listening", "addr", s.httpServer.Addr)
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully drains in-flight requests, flushes logs, then stops.
func (s *Server) Shutdown(ctx context.Context) error {
	slog.Info("shutting down HTTP server...")
	if err := s.httpServer.Shutdown(ctx); err != nil {
		return err
	}
	slog.Info("flushing log buffers...")
	if err := s.logger.Flush(ctx); err != nil {
		slog.Error("flush logs on shutdown", "err", err)
	}
	return nil
}
