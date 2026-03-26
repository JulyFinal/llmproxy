// Package config loads application configuration from a TOML directory.
package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"

	"proxyllm/internal/domain"
)

const DefaultConfigDir = "data"

// Config is the top-level config struct.
type Config struct {
	domain.AppConfig
	DBPath string `toml:"db_path"`
}

// Load reads all TOML files from the given directory.
// It looks for config.toml, providers.toml, and api_keys.toml.
func Load(dir string) (*Config, error) {
	cfg := defaults()

	// 1. Load core config.toml
	mainPath := filepath.Join(dir, "config.toml")
	if _, err := os.Stat(mainPath); err == nil {
		if _, err := toml.DecodeFile(mainPath, cfg); err != nil {
			return nil, err
		}
	}

	// 2. Load providers.toml (optional)
	providersPath := filepath.Join(dir, "providers.toml")
	if _, err := os.Stat(providersPath); err == nil {
		if _, err := toml.DecodeFile(providersPath, cfg); err != nil {
			return nil, err
		}
	}

	// 3. Load api_keys.toml (optional)
	keysPath := filepath.Join(dir, "api_keys.toml")
	if _, err := os.Stat(keysPath); err == nil {
		if _, err := toml.DecodeFile(keysPath, cfg); err != nil {
			return nil, err
		}
	}

	// Default DB path to be inside the data dir if it remains at default
	if cfg.DBPath == "proxyllm.db" {
		cfg.DBPath = filepath.Join(dir, "proxyllm.db")
	}

	applyEnv(cfg)

	// Populate IDs and defaults
	for id, node := range cfg.Providers {
		node.ID = id
		if node.Aliases == nil {
			node.Aliases = []string{}
		}
		if node.EndpointType == "" {
			node.EndpointType = domain.EndpointAll
		}
	}
	for id, key := range cfg.APIKeys {
		key.ID = id
		if key.AllowModels == nil {
			key.AllowModels = []string{}
		}
	}

	return cfg, nil
}

// ─── ConfigManager ────────────────────────────────────────────────────────────

type ConfigManager struct {
	mu   sync.Mutex
	path string
	cfg  *Config
}

func NewConfigManager(path string, cfg *Config) *ConfigManager {
	return &ConfigManager{path: path, cfg: cfg}
}

// ─── defaults & env ───────────────────────────────────────────────────────────

func defaults() *Config {
	return &Config{
		DBPath: "proxyllm.db",
		AppConfig: domain.AppConfig{
			Server: domain.ServerConfig{
				Addr:        ":8080",
				CORSOrigins: []string{"*"},
				MaxMemoryMB: 1024,
			},
			RateLimit: domain.RateLimitConfig{
				MaxRetries: 2,
			},
			Logging: domain.LoggingConfig{
				BufferSize:      4096,
				FlushIntervalMs: 500,
				BasicMaxDays:    30,
				DetailMaxDays:   7,
				BasicMaxSizeMB:  8192,
				DetailMaxSizeMB: 2048,
			},
			Cache:     domain.CacheConfig{Type: "memory"},
			MQ:        domain.MQConfig{Type: "memory"},
			Queue: domain.QueueConfig{
				DefaultPriority: 0,
				MaxQueueSize:    10000,
			},
			Worker: domain.WorkerConfig{
				PoolSize:         10,
				MaxRetryAttempts: 3,
				RetryDelayMs:     100,
				MaxWaitTimeSec:   1800,
			},
			Providers: make(map[string]*domain.ModelNode),
			APIKeys:   make(map[string]*domain.APIKey),
		},
	}
}

func applyEnv(cfg *Config) {
	if v := os.Getenv("PROXYLLM_DB_PATH"); v != "" {
		cfg.DBPath = v
	}
	if v := os.Getenv("PROXYLLM_SERVER_ADDR"); v != "" {
		cfg.Server.Addr = v
	}
	if v := os.Getenv("PROXYLLM_SERVER_ADMIN_TOKEN"); v != "" {
		cfg.Server.AdminToken = v
	}
	if v := os.Getenv("PROXYLLM_SERVER_CORS_ORIGINS"); v != "" {
		cfg.Server.CORSOrigins = strings.Split(v, ",")
	}
	if v := envInt("PROXYLLM_MAX_MEMORY_MB"); v != nil {
		cfg.Server.MaxMemoryMB = *v
	}
	if v := envInt("PROXYLLM_RL_TPM"); v != nil {
		cfg.RateLimit.TPM = *v
	}
	if v := envInt("PROXYLLM_RL_RPM"); v != nil {
		cfg.RateLimit.RPM = *v
	}
	if v := envInt("PROXYLLM_RL_MAX_RETRIES"); v != nil {
		cfg.RateLimit.MaxRetries = *v
	}
	if v := envInt("PROXYLLM_LOG_BASIC_MAX_ROWS"); v != nil {
		cfg.Logging.BasicMaxRows = *v
	}
	if v := envInt("PROXYLLM_LOG_BASIC_MAX_DAYS"); v != nil {
		cfg.Logging.BasicMaxDays = *v
	}
	if v := envInt("PROXYLLM_LOG_BASIC_MAX_SIZE_MB"); v != nil {
		cfg.Logging.BasicMaxSizeMB = *v
	}
	if v := envInt("PROXYLLM_LOG_DETAIL_MAX_ROWS"); v != nil {
		cfg.Logging.DetailMaxRows = *v
	}
	if v := envInt("PROXYLLM_LOG_DETAIL_MAX_DAYS"); v != nil {
		cfg.Logging.DetailMaxDays = *v
	}
	if v := envInt("PROXYLLM_LOG_DETAIL_MAX_SIZE_MB"); v != nil {
		cfg.Logging.DetailMaxSizeMB = *v
	}
	if v := envInt("PROXYLLM_LOG_BUFFER_SIZE"); v != nil {
		cfg.Logging.BufferSize = *v
	}
	if v := envInt("PROXYLLM_LOG_FLUSH_INTERVAL_MS"); v != nil {
		cfg.Logging.FlushIntervalMs = *v
	}
	if v := os.Getenv("PROXYLLM_CACHE_TYPE"); v != "" {
		cfg.Cache.Type = v
	}
	if v := os.Getenv("PROXYLLM_CACHE_REDIS_ADDR"); v != "" {
		cfg.Cache.Redis.Addr = v
	}
	if v := os.Getenv("PROXYLLM_CACHE_REDIS_PASSWORD"); v != "" {
		cfg.Cache.Redis.Password = v
	}
	if v := envInt("PROXYLLM_CACHE_REDIS_DB"); v != nil {
		cfg.Cache.Redis.DB = *v
	}
	if v := os.Getenv("PROXYLLM_MQ_TYPE"); v != "" {
		cfg.MQ.Type = v
	}
	if v := os.Getenv("PROXYLLM_MQ_REDIS_ADDR"); v != "" {
		cfg.MQ.Redis.Addr = v
	}
	if v := os.Getenv("PROXYLLM_MQ_REDIS_PASSWORD"); v != "" {
		cfg.MQ.Redis.Password = v
	}
	if v := envInt("PROXYLLM_MQ_REDIS_DB"); v != nil {
		cfg.MQ.Redis.DB = *v
	}
	if v := os.Getenv("PROXYLLM_MQ_RABBITMQ_URL"); v != "" {
		cfg.MQ.RabbitMQ.URL = v
	}
}

func envInt(key string) *int {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		return nil
	}
	return &i
}
