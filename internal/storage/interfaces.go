package storage

import (
	"context"
	"time"

	"proxyllm/internal/domain"
)

// Storage persists configuration data (nodes, API keys).
// Default impl: SQLite. Future: PostgreSQL.
type Storage interface {
	// Nodes
	UpsertNode(ctx context.Context, node *domain.ModelNode) error
	GetNode(ctx context.Context, id string) (*domain.ModelNode, error)
	ListNodes(ctx context.Context) ([]*domain.ModelNode, error)
	DeleteNode(ctx context.Context, id string) error

	// API Keys
	UpsertAPIKey(ctx context.Context, key *domain.APIKey) error
	GetAPIKey(ctx context.Context, id string) (*domain.APIKey, error)
	GetAPIKeyByValue(ctx context.Context, keyValue string) (*domain.APIKey, error)
	ListAPIKeys(ctx context.Context) ([]*domain.APIKey, error)
	DeleteAPIKey(ctx context.Context, id string) error

	Close() error
}

// Cache provides high-frequency key-value storage for rate limiting counters
// and hot configuration data.
// Default impl: in-memory. Future: Redis.
type Cache interface {
	Get(ctx context.Context, key string) ([]byte, bool)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Delete(ctx context.Context, key string) error

	// IncrBy atomically increments key by delta and returns the new value.
	// If the key does not exist it is created with value=delta and the given TTL.
	// TTL is only applied on key creation; subsequent calls do NOT reset it.
	IncrBy(ctx context.Context, key string, delta int64, ttl time.Duration) (int64, error)

	Close() error
}

// Queue decouples producers (request handlers) from consumers (log writers,
// metric collectors) via an async message bus.
// Default impl: buffered channel. Future: RabbitMQ / Kafka.
//
// Contract:
//   - Publish is non-blocking; if the internal buffer is full the message is
//     dropped and an error is returned (caller decides whether to retry).
//   - Subscribe is non-blocking; it starts one or more goroutines internally
//     and returns immediately. All in-flight handlers are drained before Close
//     returns.
//   - handler is called concurrently; implementations must document the
//     concurrency level.
type Queue interface {
	Publish(ctx context.Context, topic string, payload []byte) error
	Subscribe(ctx context.Context, topic string, handler func(ctx context.Context, payload []byte) error) error
	Close() error
}

// Logger is the two-tier async logging system.
//
//   Tier 1 (RequestLog): lightweight, high-frequency. Written via an async
//     channel and flushed to SQLite in batches. Never blocks the request path.
//
//   Tier 2 (DetailLog): heavy, low-frequency. Stored separately, linked to
//     Tier 1 by TraceID. Only loaded when the user clicks "details".
type Logger interface {
	// AsyncLog enqueues a request log entry. Never blocks.
	AsyncLog(log *domain.RequestLog)

	// AsyncLogDetail enqueues a detail log entry. Never blocks.
	AsyncLogDetail(log *domain.DetailLog)

	// QueryLogs is called by the admin API (synchronous).
	QueryLogs(ctx context.Context, filter domain.LogFilter) ([]*domain.RequestLog, int64, error)

	// ExportLogs returns all logs matching the filter without pagination.
	ExportLogs(ctx context.Context, filter domain.LogFilter) ([]*domain.RequestLog, error)

	// GetDetail loads a single detail log by TraceID (synchronous).
	GetDetail(ctx context.Context, traceID string) (*domain.DetailLog, error)

	// Stats returns aggregate metrics matching the filter.
	Stats(ctx context.Context, filter domain.LogFilter) (*domain.LogStats, error)

	// StatsTimeSeries returns time-bucketed metrics. granularity can be "hour" or "day".
	StatsTimeSeries(ctx context.Context, filter domain.LogFilter, granularity string) ([]*domain.TimeSeriesPoint, error)

	// StatsTop returns the top entities (models or api_keys) ranked by token usage.
	StatsTop(ctx context.Context, filter domain.LogFilter, groupBy string, limit int) ([]*domain.TopEntity, error)

	// Flush drains the internal buffers and commits pending writes to storage.
	// Called during graceful shutdown.
	Flush(ctx context.Context) error

	Close() error
}

// RateLimiter enforces three-layer limits: global → per-model → per-key.
//
// TPM is recorded post-response (after actual token count is known).
// RPM is checked pre-request.
type RateLimiter interface {
	// AllowRequest checks RPM limits for all three layers.
	// Returns false if any layer is over its RPM budget.
	AllowRequest(ctx context.Context, keyID, modelAlias string) (bool, error)

	// RecordTokens adds actualTokens to the TPM counters for all three layers.
	// Called once per request after the response (or stream) completes.
	RecordTokens(ctx context.Context, keyID, modelAlias string, actualTokens int) error
}
