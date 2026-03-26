# Changelog

All notable changes to ProxyLLM are documented in this file.

## [Unreleased] — 2026-03-17

### Added
- **Prometheus Metrics** (`/metrics`): Exposes request counts, token usage, queue depth, and busy worker count in Prometheus text format. Protected by admin token.
- **Enhanced Health Check** (`/health`): Now verifies SQLite connectivity and cache backend reachability. Returns `503 Service Unavailable` when any check fails.
- **Sliding Window Rate Limiting**: Replaced fixed 1-minute window with a weighted moving average across two adjacent windows, eliminating the 2x burst at window boundaries.
- **Queue Graceful Shutdown**: `RequestQueue.Close()` wakes all blocked workers via `cond.Broadcast`, preventing deadlock on shutdown.
- **Worker Pool WaitGroup**: `WorkerPool.Stop()` now waits for all worker goroutines to finish before returning, preventing use-after-close on shared resources.
- **Retry Response Buffering**: `retryableRecorder` uses an isolated header map during tentative attempts and only commits to the real `ResponseWriter` on success or final failure.
- **New Tests**: `pool_test.go` (retry & success), `priority_queue_test.go` (priority ordering, lazy deletion, close), `proxy_test.go` (error attribution, success forwarding).
- **SQLite Indexes**: Added indexes on `request_logs(session_id)`, `request_logs(node_id)`, `detail_logs(session_id)`, `detail_logs(timestamp)` for faster filtered queries.

### Changed
- **Graceful Shutdown Order**: Now `srv.Shutdown()` → `workerPool.Stop()` (was reversed), ensuring no new requests are accepted while workers drain.
- **SQLite Connection Pool**: `MaxOpenConns` increased from 1 to 100 with `ConnMaxLifetime` of 1 hour, allowing concurrent reads under WAL mode.
- **ExportLogs Safety Limit**: Added `LIMIT 100000` to prevent OOM on very large datasets.
- **Dockerfile**: Updated base image from `golang:1.22-alpine` to `golang:1.24-alpine` to match `go.mod`.
- **Config Upsert on Startup**: Provider and API key upsert errors now cause a fatal exit with a clear error message instead of being silently ignored.
- **Metrics Labels**: Removed API key ID from Prometheus labels to avoid cardinality explosion and potential information leakage.

### Fixed
- **Admin Token Timing Attack**: Replaced `!=` string comparison with `crypto/subtle.ConstantTimeCompare`.
- **`keygen.go` Deprecated API**: Removed `mathrand.Seed()` call (auto-seeded in Go 1.20+).
- **`statusRecorder` Missing Flusher**: Added `Flush()` method so SSE streaming works correctly through the access log middleware.
- **Dead Code**: Removed unused `bytesReader` type from `openai.go`.
- **`server.go` Syntax Error**: Removed stray `...` placeholder that caused compilation failure.

### Security
- Admin token comparison is now constant-time.
- `/metrics` endpoint is protected by admin authentication.
- Health check no longer increments rate-limit counters (uses side-effect-free `Limiter.Check()`).

## [0.1.0] — 2026-03-11

### Added
- Initial release.
- OpenAI-compatible API proxy (`/v1/chat/completions`, `/v1/completions`, `/v1/embeddings`, `/v1/responses`, `/v1/models`).
- Multi-model priority request queue with configurable max size and wait time.
- Worker pool with smart retry and failover across multiple backend nodes.
- Three-layer rate limiting: Global → Model → API Key (RPM & TPM).
- Two-tier async logging: compact request logs + detailed request/response bodies.
- Log retention policies: rows, age, and size-based pruning with incremental vacuum.
- Admin REST API for managing nodes, API keys, and querying logs/stats.
- Pluggable backends: SQLite storage, in-memory/Redis cache, in-memory/Redis/RabbitMQ message queue.
- Embedded static UI served at `/ui/`.
- Docker and Docker Compose support.
- Full configuration via TOML file with environment variable overrides.
