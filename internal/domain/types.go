package domain

import "time"

// ─── Endpoint type ────────────────────────────────────────────────────────────

type EndpointType string

const (
	EndpointChat      EndpointType = "chat"
	EndpointEmbedding EndpointType = "embedding"
	EndpointResponses EndpointType = "responses"
	EndpointAll       EndpointType = "all"
)

// ─── Model node ───────────────────────────────────────────────────────────────

// ModelNode defines one upstream LLM backend.
// In config.toml it is declared as [provider.<id>]; the id field is populated
// from the TOML key by config.Load and is not written back as a TOML field.
type ModelNode struct {
	ID           string           `toml:"-"             json:"id"`
	Name         string           `toml:"name"          json:"name"`
	Aliases      []string         `toml:"aliases"       json:"aliases"`
	BaseURL      string           `toml:"base_url"      json:"base_url"`
	APIKey       string           `toml:"api_key"       json:"api_key"`
	ModelName    string           `toml:"model_name"    json:"model_name"`
	EndpointType EndpointType     `toml:"endpoint_type" json:"endpoint_type"` // "chat" | "embedding" | "all"
	TPM          int              `toml:"tpm"           json:"tpm"`           // tokens per minute, 0 = unlimited
	RPM          int              `toml:"rpm"           json:"rpm"`           // requests per minute, 0 = unlimited
	Override     map[string]any   `toml:"override"      json:"override"`      // deep-merged into upstream request
	TimeoutSec   int              `toml:"timeout_sec"   json:"timeout_sec"`
	Enabled      bool             `toml:"enabled"       json:"enabled"`
	CreatedAt    time.Time        `toml:"-"             json:"created_at"`
	UpdatedAt    time.Time        `toml:"-"             json:"updated_at"`
}

// ─── Rate limit config ────────────────────────────────────────────────────────

type RateLimitConfig struct {
	TPM        int `toml:"tpm"         json:"tpm"`          // tokens per minute, 0 = unlimited
	RPM        int `toml:"rpm"         json:"rpm"`          // requests per minute, 0 = unlimited
	MaxRetries int `toml:"max_retries" json:"max_retries"`  // upstream retry limit, default 2
}

// ─── API Key ──────────────────────────────────────────────────────────────────

// APIKey represents a bearer token for accessing the proxy.
// In config.toml it is declared as [api_keys.<id>]; the id field is populated
// from the TOML key by config.Load and is not written back as a TOML field.
type APIKey struct {
	ID          string           `toml:"-"            json:"id"`
	Key         string           `toml:"key"          json:"key"`          // plaintext bearer token
	Name        string           `toml:"name"         json:"name"`
	Enabled     bool             `toml:"enabled"      json:"enabled"`
	TPM         int              `toml:"tpm"          json:"tpm"`          // tokens per minute, 0 = inherit global
	RPM         int              `toml:"rpm"          json:"rpm"`          // requests per minute, 0 = inherit global
	AllowModels []string         `toml:"allow_models" json:"allow_models"` // empty = all models
	CreatedAt   time.Time        `toml:"-"            json:"created_at"`
	ExpiresAt   *time.Time       `toml:"-"            json:"expires_at,omitempty"`
}

// ─── Cache config ─────────────────────────────────────────────────────────────

// CacheConfig selects the backend for rate-limit counters.
// type: "memory" (default) | "redis"
type CacheConfig struct {
	Type  string      `toml:"type"`  // "memory" | "redis"
	Redis RedisConfig `toml:"redis"`
}

// ─── Message queue config ─────────────────────────────────────────────────────

// MQConfig selects the backend for the async log pipeline.
// type: "memory" (default) | "redis" | "rabbitmq"
type MQConfig struct {
	Type     string          `toml:"type"`     // "memory" | "redis" | "rabbitmq"
	Redis    RedisConfig     `toml:"redis"`
	RabbitMQ RabbitMQConfig  `toml:"rabbitmq"`
}

// RedisConfig holds connection parameters for a Redis instance.
type RedisConfig struct {
	Addr     string `toml:"addr"`     // e.g. "127.0.0.1:6379"
	Password string `toml:"password"`
	DB       int    `toml:"db"`
}

// RabbitMQConfig holds the AMQP connection URL.
type RabbitMQConfig struct {
	URL string `toml:"url"` // e.g. "amqp://admin:secret@127.0.0.1:5672/"
}

// ─── Server config ────────────────────────────────────────────────────────────

type ServerConfig struct {
	Addr        string   `toml:"addr"`         // default :8080
	CORSOrigins []string `toml:"cors_origins"`
	AdminToken  string   `toml:"admin_token"`
	MaxMemoryMB int      `toml:"max_memory_mb"` // soft GC limit in MB, 0 = no limit
}

// ─── Logging config ───────────────────────────────────────────────────────────

type LoggingConfig struct {
	BasicMaxRows    int `toml:"basic_max_rows"`
	BasicMaxDays    int `toml:"basic_max_days"`
	BasicMaxSizeMB  int `toml:"basic_max_size_mb"`
	DetailMaxRows   int `toml:"detail_max_rows"`
	DetailMaxDays   int `toml:"detail_max_days"`
	DetailMaxSizeMB int `toml:"detail_max_size_mb"`
	BufferSize      int `toml:"buffer_size"`
	FlushIntervalMs int `toml:"flush_interval_ms"`
}

// ─── Global app config ────────────────────────────────────────────────────────

type AppConfig struct {
	Server    ServerConfig                `toml:"server"`
	RateLimit RateLimitConfig             `toml:"rate_limit"`
	Logging   LoggingConfig               `toml:"logging"`
	Cache     CacheConfig                 `toml:"cache"`
	MQ        MQConfig                    `toml:"mq"`
	Queue     QueueConfig                 `toml:"queue"`
	Worker    WorkerConfig                `toml:"worker"`
	Providers map[string]*ModelNode       `toml:"provider"`
	APIKeys   map[string]*APIKey          `toml:"api_keys"`
}

// ─── Queue config ─────────────────────────────────────────────────────────────

type QueueConfig struct {
	DefaultPriority int `toml:"default_priority"`
	MaxQueueSize    int `toml:"max_queue_size"`
}

// ─── Worker config ────────────────────────────────────────────────────────────

type WorkerConfig struct {
	PoolSize             int   `toml:"pool_size"`
	MaxRetryAttempts     int   `toml:"max_retry_attempts"`
	RetryDelayMs         int   `toml:"retry_delay_ms"`
	MaxWaitTimeSec       int   `toml:"max_wait_time_sec"`
}

// ─── Request context ──────────────────────────────────────────────────────────

type ProxyContext struct {
	RequestID  string
	SessionID  string
	APIKey     *APIKey
	TargetNode *ModelNode
	ModelAlias string
	Stream     bool
	StartTime  time.Time
}

// ─── Logs ─────────────────────────────────────────────────────────────────────

type RequestLog struct {
	ID               string    `json:"id"`
	SessionID        string    `json:"session_id"`
	Timestamp        time.Time `json:"timestamp"`
	APIKeyID         string    `json:"api_key_id"`
	ModelAlias       string    `json:"model_alias"`
	NodeID           string    `json:"node_id"`
	ActualModel      string    `json:"actual_model"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	TotalTokens      int       `json:"total_tokens"`
	DurationMs       int64     `json:"duration_ms"`
	StatusCode       int       `json:"status_code"`
	Stream           bool      `json:"stream"`
	ErrorMsg         string    `json:"error_msg,omitempty"`
	HasDetail        bool      `json:"has_detail"`
}

type DetailLog struct {
	TraceID      string    `json:"trace_id"`
	SessionID    string    `json:"session_id"`
	Timestamp    time.Time `json:"timestamp"`
	RequestBody  string    `json:"request_body"`
	ResponseBody string    `json:"response_body"`
}

type LogStats struct {
	TotalRequests    int64 `json:"total_requests"`
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

type LogFilter struct {
	APIKeyID   string
	ModelAlias string
	NodeID     string
	SessionID  string
	StatusCode int
	ErrorOnly  bool
	Keyword    string
	StartTime  *time.Time
	EndTime    *time.Time
	Page       int
	PageSize   int
}

type TimeSeriesPoint struct {
	Timestamp        time.Time `json:"timestamp"`
	Requests         int64     `json:"requests"`
	PromptTokens     int64     `json:"prompt_tokens"`
	CompletionTokens int64     `json:"completion_tokens"`
	TotalTokens      int64     `json:"total_tokens"`
}

type TopEntity struct {
	Name        string `json:"name"`
	Requests    int64  `json:"requests"`
	TotalTokens int64  `json:"total_tokens"`
}
