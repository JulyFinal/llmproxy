// Package config loads application configuration from a TOML file and
// environment variable overrides. ENV always wins over TOML.
//
// ENV variable mapping (prefix: PROXYLLM_):
//
//	PROXYLLM_SERVER_ADDR              → server.addr
//	PROXYLLM_SERVER_ADMIN_TOKEN       → server.admin_token
//	PROXYLLM_SERVER_CORS_ORIGINS      → server.cors_origins  (comma-separated)
//	PROXYLLM_MAX_MEMORY_MB            → server.max_memory_mb
//	PROXYLLM_RL_TPM                   → rate_limit.tpm
//	PROXYLLM_RL_RPM                   → rate_limit.rpm
//	PROXYLLM_RL_MAX_RETRIES           → rate_limit.max_retries
//	PROXYLLM_LOG_BASIC_MAX_ROWS       → logging.basic_max_rows
//	PROXYLLM_LOG_BASIC_MAX_DAYS       → logging.basic_max_days
//	PROXYLLM_LOG_BASIC_MAX_SIZE_MB    → logging.basic_max_size_mb
//	PROXYLLM_LOG_DETAIL_MAX_ROWS      → logging.detail_max_rows
//	PROXYLLM_LOG_DETAIL_MAX_DAYS      → logging.detail_max_days
//	PROXYLLM_LOG_DETAIL_MAX_SIZE_MB   → logging.detail_max_size_mb
//	PROXYLLM_LOG_BUFFER_SIZE          → logging.buffer_size
//	PROXYLLM_LOG_FLUSH_INTERVAL_MS    → logging.flush_interval_ms
//	PROXYLLM_DB_PATH                  → db_path
//	PROXYLLM_CACHE_TYPE               → cache.type  ("memory"|"redis")
//	PROXYLLM_CACHE_REDIS_ADDR         → cache.redis.addr
//	PROXYLLM_CACHE_REDIS_PASSWORD     → cache.redis.password
//	PROXYLLM_CACHE_REDIS_DB           → cache.redis.db
//	PROXYLLM_MQ_TYPE                  → mq.type  ("memory"|"redis"|"rabbitmq")
//	PROXYLLM_MQ_REDIS_ADDR            → mq.redis.addr
//	PROXYLLM_MQ_REDIS_PASSWORD        → mq.redis.password
//	PROXYLLM_MQ_REDIS_DB              → mq.redis.db
//	PROXYLLM_MQ_RABBITMQ_URL          → mq.rabbitmq.url
package config

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"

	"proxyllm/internal/domain"
)

const DefaultConfigPath = "config.toml"

// Config is the top-level config struct. It embeds domain.AppConfig and adds
// the DBPath field which is only relevant to the binary, not the domain.
type Config struct {
	domain.AppConfig
	DBPath string `toml:"db_path"`
}

// Load reads the TOML config file at path (optional) then applies ENV overrides.
// If the file doesn't exist, defaults are used. After decoding, node and key IDs
// are populated from their TOML map keys.
func Load(path string) (*Config, error) {
	cfg := defaults()

	if _, err := os.Stat(path); err == nil {
		if _, err := toml.DecodeFile(path, cfg); err != nil {
			return nil, err
		}
	}

	applyEnv(cfg)

	// Populate IDs from map keys (the TOML key is the canonical ID).
	for id, node := range cfg.Providers {
		node.ID = id
		if node.Aliases == nil {
			node.Aliases = []string{}
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

// ConfigManager holds the loaded config and writes it back to disk after
// admin UI mutations (node/key create, update, delete).
type ConfigManager struct {
	mu   sync.Mutex
	path string
	cfg  *Config
}

// NewConfigManager wraps a loaded Config with write-back capability.
func NewConfigManager(path string, cfg *Config) *ConfigManager {
	return &ConfigManager{path: path, cfg: cfg}
}

// Save writes the current config with the given nodes and API keys to disk
// atomically (temp-file + rename). The config file is reconstructed from
// the in-memory settings plus the provided slices.
func (m *ConfigManager) Save(nodes []*domain.ModelNode, keys []*domain.APIKey) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	providers := make(map[string]*domain.ModelNode, len(nodes))
	for _, n := range nodes {
		providers[n.ID] = n
	}
	apiKeys := make(map[string]*domain.APIKey, len(keys))
	for _, k := range keys {
		apiKeys[k.ID] = k
	}

	m.cfg.Providers = providers
	m.cfg.APIKeys = apiKeys

	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(m.cfg); err != nil {
		return fmt.Errorf("config marshal: %w", err)
	}

	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("config write tmp: %w", err)
	}
	if err := os.Rename(tmp, m.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("config rename: %w", err)
	}
	return nil
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
			Providers: make(map[string]*domain.ModelNode),
			APIKeys:   make(map[string]*domain.APIKey),
		},
	}
}

func applyEnv(cfg *Config) {
	if v := os.Getenv("PROXYLLM_DB_PATH"); v != "" {
		cfg.DBPath = v
	}

	// Server
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

	// Rate limit
	if v := envInt("PROXYLLM_RL_TPM"); v != nil {
		cfg.RateLimit.TPM = *v
	}
	if v := envInt("PROXYLLM_RL_RPM"); v != nil {
		cfg.RateLimit.RPM = *v
	}
	if v := envInt("PROXYLLM_RL_MAX_RETRIES"); v != nil {
		cfg.RateLimit.MaxRetries = *v
	}

	// Logging
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

	// Cache
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

	// MQ
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
