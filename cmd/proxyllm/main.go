package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
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
	"proxyllm/internal/storage/rabbitmq"
	redistore "proxyllm/internal/storage/redis"
	"proxyllm/internal/storage/sqlite"
	"proxyllm/internal/worker"
)

func main() {
	cfgPath := flag.String("config", config.DefaultConfigPath, "path to config.toml")
	flag.Parse()

	// ── Load config ──────────────────────────────────────────────────────────
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		slog.Error("load config", "err", err)
		os.Exit(1)
	}
	cfgMgr := config.NewConfigManager(*cfgPath, cfg)

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

	// ── Queue ─────────────────────────────────────────────────────────────────
	msgQueue := buildQueue(cfg, slog.Default())
	_ = msgQueue // reserved for future async log pipeline

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
		_ = store.UpsertNode(ctx, n)
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
		_ = store.UpsertAPIKey(ctx, k)
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

	keyLimits := func(keyID string) *domain.RateLimitConfig {
		k, err := store.GetAPIKey(ctx, keyID)
		if err != nil || k == nil || (k.TPM <= 0 && k.RPM <= 0) {
			return nil
		}
		return &domain.RateLimitConfig{TPM: k.TPM, RPM: k.RPM}
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

	workerPool.Stop()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown error", "err", err)
	}

	cleaner.Stop()
	cache.Close()
	msgQueue.Close()
	asyncLogger.Close()
	db.Close()

	slog.Info("goodbye")
}

// buildQueue selects a Queue backend based on cfg.MQ.Type.
// Falls back to in-memory on connection failure.
func buildQueue(cfg *config.Config, log *slog.Logger) storage.Queue {
	switch cfg.MQ.Type {
	case "rabbitmq":
		rq, err := rabbitmq.New(cfg.MQ.RabbitMQ.URL)
		if err == nil {
			log.Info("queue: using RabbitMQ", "url", cfg.MQ.RabbitMQ.URL)
			return rq
		}
		log.Error("queue: RabbitMQ connect failed, using in-memory", "err", err)
		return memory.NewMemoryQueue(cfg.Logging.BufferSize)

	case "redis":
		rq := redistore.NewQueue(cfg.MQ.Redis.Addr, cfg.MQ.Redis.Password, cfg.MQ.Redis.DB)
		if err := rq.Ping(context.Background()); err == nil {
			log.Info("queue: using Redis", "addr", cfg.MQ.Redis.Addr)
			return rq
		}
		log.Error("queue: Redis queue failed, using in-memory")
		_ = rq.Close()
		return memory.NewMemoryQueue(cfg.Logging.BufferSize)

	default:
		log.Info("queue: using in-memory")
		return memory.NewMemoryQueue(cfg.Logging.BufferSize)
	}
}
