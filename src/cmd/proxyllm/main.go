package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"sync"
	"syscall"
	"time"

	"proxyllm/internal/api"
	"proxyllm/internal/config"
	"proxyllm/internal/domain"
	"proxyllm/internal/logging"
	"proxyllm/internal/proxy"
	"proxyllm/internal/queue"
	"proxyllm/internal/ratelimit"
	"proxyllm/internal/router"
	"proxyllm/internal/storage"
	"proxyllm/internal/storage/memory"
	redistore "proxyllm/internal/storage/redis"
	"proxyllm/internal/storage/sqlite"
	"proxyllm/internal/worker"
)

func main() {
	defaultDir := os.Getenv("PROXYLLM_DATA_DIR")
	if defaultDir == "" {
		// 优先探测容器环境的标准路径
		if info, err := os.Stat("/app/data"); err == nil && info.IsDir() {
			defaultDir = "/app/data"
		} else {
			defaultDir = config.DefaultConfigDir
		}
	}
	dataDir := flag.String("data", defaultDir, "directory containing config.toml, providers.toml, etc.")
	flag.Parse()

	// ── Load config ──────────────────────────────────────────────────────────
	cfg, err := config.Load(*dataDir)
	if err != nil {
		slog.Error("load config", "err", err)
		os.Exit(1)
	}
	cfgMgr := config.NewConfigManager(*dataDir, cfg)

	// ── Memory limit ─────────────────────────────────────────────────────────
	if mb := cfg.Server.MaxMemoryMB; mb > 0 {
		debug.SetMemoryLimit(int64(mb) * 1024 * 1024)
		slog.Info("memory limit set", "limit_mb", mb)
	}

	// ── SQLite ───────────────────────────────────────────────────────────────
	db, err := sqlite.Open(cfg.DBPath)
	if err != nil {
		slog.Error("open sqlite", "err", err)
		os.Exit(1)
	}
	store := sqlite.NewSQLiteStorage(db)
	sqliteLogger := sqlite.NewSQLiteLogger(db)

	// ── Cache ─────────────────────────────────────────────────────────────────
	var cache storage.Cache
	switch cfg.Cache.Type {
	case "redis":
		rc := redistore.New(cfg.Cache.Redis.Addr, cfg.Cache.Redis.Password, cfg.Cache.Redis.DB)
		if err := rc.Ping(context.Background()); err != nil {
			slog.Error("cache: redis ping failed — falling back to in-memory",
				"addr", cfg.Cache.Redis.Addr, "err", err)
			cache = memory.NewMemoryCache()
		} else {
			cache = rc
			slog.Info("cache: using Redis", "addr", cfg.Cache.Redis.Addr)
		}
	default:
		cache = memory.NewMemoryCache()
		slog.Info("cache: using in-memory")
	}

	// ── Logger (async two-tier) ───────────────────────────────────────────────
	flushInterval := time.Duration(cfg.Logging.FlushIntervalMs) * time.Millisecond
	asyncLogger := logging.New(sqliteLogger, cfg.Logging.BufferSize, flushInterval)

	// ── Retention cleaner ─────────────────────────────────────────────────────
	cleaner := logging.NewRetentionCleaner(
		sqliteLogger,
		cfg.Logging.BasicMaxRows, cfg.Logging.BasicMaxDays, cfg.Logging.BasicMaxSizeMB,
		cfg.Logging.DetailMaxRows, cfg.Logging.DetailMaxDays, cfg.Logging.DetailMaxSizeMB,
		1*time.Hour,
	)

	// ── Router ────────────────────────────────────────────────────────────────
	rt := router.New()
	ctx := context.Background()

	// Upsert providers from config into SQLite.
	for id, n := range cfg.Providers {
		n.ID = id
		if err := store.UpsertNode(ctx, n); err != nil {
			slog.Error("failed to initialize node from config", "id", id, "err", err)
			os.Exit(1)
		}
	}
	// Remove nodes not in config (stale DB entries).
	if existingNodes, err := store.ListNodes(ctx); err == nil {
		for _, n := range existingNodes {
			if _, ok := cfg.Providers[n.ID]; !ok {
				_ = store.DeleteNode(ctx, n.ID)
				slog.Info("removed stale node from DB", "id", n.ID, "name", n.Name)
			}
		}
	}

	// Upsert pre-configured API keys from config into SQLite.
	for id, k := range cfg.APIKeys {
		k.ID = id
		if k.Key == "" {
			continue
		}
		if k.AllowModels == nil {
			k.AllowModels = []string{}
		}
		if err := store.UpsertAPIKey(ctx, k); err != nil {
			slog.Error("failed to initialize api_key from config", "id", id, "err", err)
			os.Exit(1)
		}
	}
	// Remove keys not in config.
	if existingKeys, err := store.ListAPIKeys(ctx); err == nil {
		for _, k := range existingKeys {
			if _, ok := cfg.APIKeys[k.ID]; !ok {
				_ = store.DeleteAPIKey(ctx, k.ID)
				slog.Info("removed stale api_key from DB", "id", k.ID, "name", k.Name)
			}
		}
	}

	dbNodes, err := store.ListNodes(ctx)
	if err != nil {
		slog.Error("load nodes", "err", err)
		os.Exit(1)
	}
	rt.Sync(dbNodes)
	slog.Info("router loaded", "nodes", len(dbNodes))

	// ── Rate limiter ──────────────────────────────────────────────────────────
	// Build per-alias model limits from provider node configs.
	modelLimits := make(map[string]domain.RateLimitConfig)
	for _, node := range dbNodes {
		if node.TPM <= 0 && node.RPM <= 0 {
			continue
		}
		for _, alias := range node.Aliases {
			if _, exists := modelLimits[alias]; !exists {
				modelLimits[alias] = domain.RateLimitConfig{TPM: node.TPM, RPM: node.RPM}
			}
		}
	}

	// keyLimits with TTL cache to avoid hitting SQLite on every AllowRequest.
	type cachedKeyLimit struct {
		cfg       *domain.RateLimitConfig
		expiresAt time.Time
	}
	var keyCache sync.Map
	const keyLimitTTL = 30 * time.Second

	keyLimits := func(keyID string) *domain.RateLimitConfig {
		if v, ok := keyCache.Load(keyID); ok {
			entry := v.(*cachedKeyLimit)
			if time.Now().Before(entry.expiresAt) {
				return entry.cfg
			}
		}
		k, err := store.GetAPIKey(ctx, keyID)
		var result *domain.RateLimitConfig
		if err == nil && k != nil && (k.TPM > 0 || k.RPM > 0) {
			result = &domain.RateLimitConfig{TPM: k.TPM, RPM: k.RPM}
		}
		keyCache.Store(keyID, &cachedKeyLimit{cfg: result, expiresAt: time.Now().Add(keyLimitTTL)})
		return result
	}
	limiter := ratelimit.New(cache, cfg.RateLimit, modelLimits, keyLimits)

	// ── Queue & Worker Pool ───────────────────────────────────────────────────
	reqQueue := queue.NewRequestQueue(cfg.Queue.MaxQueueSize)
	chainLogger := logging.NewChainLogger(asyncLogger)
	p := proxy.New()
	
	workerPool := worker.NewWorkerPool(
		reqQueue,
		rt,
		p,
		limiter,
		chainLogger,
		&worker.WorkerConfig{
			WorkerCount:      cfg.Worker.PoolSize,
			MaxRetryAttempts: cfg.Worker.MaxRetryAttempts,
			RetryDelayMs:     cfg.Worker.RetryDelayMs,
			MaxWaitTime:      time.Duration(cfg.Worker.MaxWaitTimeSec) * time.Second,
		},
	)
	workerPool.Start()

	// ── HTTP Server ───────────────────────────────────────────────────────────
	srv := api.NewServer(api.Deps{
		Config: &cfg.AppConfig,
		AdminCfg: struct {
			Token       string
			Addr        string
			CORSOrigins []string
		}{
			Token:       cfg.Server.AdminToken,
			Addr:        cfg.Server.Addr,
			CORSOrigins: cfg.Server.CORSOrigins,
		},
		Store:       store,
		Router:      rt,
		Proxy:       p,
		Limiter:     limiter,
		Logger:      asyncLogger,
		ChainLogger: chainLogger,
		Queue:       reqQueue,
		CfgMgr:      cfgMgr,
	})

	// ── Graceful shutdown ─────────────────────────────────────────────────────
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		if err := srv.ListenAndServe(); err != nil {
			slog.Info("server stopped", "err", err)
		}
	}()

	<-sigCh
	slog.Info("signal received, shutting down gracefully...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown error", "err", err)
	}

	workerPool.Stop()

	cleaner.Stop()
	cache.Close()
	asyncLogger.Close()
	db.Close()

	slog.Info("goodbye")
}
