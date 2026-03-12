# ProxyLLM Architecture & Features

This document provides an overview of ProxyLLM's architecture, core features, and design decisions.

---

## Table of Contents

1. [System Architecture](#system-architecture)
2. [Core Features](#core-features)
3. [Request Flow](#request-flow)
4. [Queue & Retry System](#queue--retry-system)
5. [Rate Limiting](#rate-limiting)
6. [Logging & Observability](#logging--observability)
7. [Configuration](#configuration)

---

## System Architecture

```
┌─────────────┐
│   Clients   │ (OpenAI SDK / curl)
└──────┬──────┘
       │
       ▼
┌─────────────────────────────────────────┐
│  HTTP Server (OpenAI Compatible API)    │
│  - Authentication                        │
│  - Request Validation                    │
│  - Priority Extraction                   │
└──────┬──────────────────────────────────┘
       │
       ▼
┌─────────────────────────────────────────┐
│  Priority Queue (per model)             │
│  - Priority-based ordering               │
│  - FIFO within same priority             │
│  - Lazy deletion for cancelled requests  │
└──────┬──────────────────────────────────┘
       │
       ▼
┌─────────────────────────────────────────┐
│  Worker Pool (fixed concurrency)        │
│  - Rate limit checking                   │
│  - Node selection                        │
│  - Smart retry with failover             │
└──────┬──────────────────────────────────┘
       │
       ▼
┌─────────────────────────────────────────┐
│  Router & Load Balancer                 │
│  - Uniform random selection             │
│  - Health-aware node filtering           │
└─────────────────────────────────────────┘
       │
       ▼
┌─────────────────────────────────────────┐
│  Upstream Nodes (OpenAI/Claude/etc)     │
└─────────────────────────────────────────┘
```

---

## Core Features

### 1. Priority Request Queue

**Purpose**: Handle high request volumes without overwhelming upstream providers.

**Key Features**:
- Requests are queued by model
- Priority field (0-100) determines processing order
- Same priority = FIFO
- Configurable max wait time (default: 30 minutes)
- Automatic cleanup of cancelled requests

**Usage**:
```json
{
  "model": "gpt-4",
  "messages": [...],
  "priority": 10
}
```

### 2. Smart Retry & Failover

**Purpose**: Maximize request success rate by automatically retrying failed requests on different nodes.

**Retry Logic**:
1. Classify error (timeout, 5xx, connection error, etc.)
2. If retryable, select next available node
3. Retry up to N times (configurable, default: 3)
4. Log each attempt with detailed error information

**Retryable Errors**:
- Upstream timeout
- 5xx errors (502, 503, 504)
- 429 (rate limit)
- Connection refused/reset
- DNS errors

**Non-Retryable Errors**:
- 4xx client errors (400, 401, 403, 404)
- Invalid request format
- Model not allowed

### 3. Load Balancing

**Purpose**: Distribute traffic across multiple backend nodes.

**Selection Algorithm**:
1. Filter nodes by model alias and endpoint type
2. Filter out disabled nodes
3. Select a healthy node at random
4. If node fails, exclude it and retry with next available node

**Example Configuration**:
```toml
[providers.openai-primary]
enabled = true

[providers.openai-backup]
enabled = true
```

### 4. Advanced Rate Limiting

**Three-Layer Hierarchy**:
1. **Global**: Overall system limits
2. **Model**: Per-model limits (e.g., gpt-4 max 100 RPM)
3. **API Key**: Per-client limits

**Metrics**:
- **RPM** (Requests Per Minute): Request count limit
- **TPM** (Tokens Per Minute): Token usage limit

**Enforcement**:
- RPM checked before request enters queue
- TPM checked before forwarding to upstream
- Blocked requests are re-queued after short delay

> **Note on TPM Limits**: TPM accounting is done asynchronously after response completion. Under high concurrency, TPM acts as a "soft" quota rather than a strict hard limit.

---

## Request Flow

### Normal Flow (Success)

```
1. Client sends request
   ↓
2. Authentication & validation
   ↓
3. Extract priority (default: 0)
   ↓
4. Enqueue request
   ↓
5. Worker dequeues (when available)
   ↓
6. Check rate limits (RPM/TPM)
   ↓
7. Select node (weighted random)
   ↓
8. Forward to upstream
   ↓
9. Stream response to client
   ↓
10. Log request (async)
```

### Retry Flow (Node Failure)

```
1. Forward to Node A
   ↓
2. Node A times out (120s)
   ↓
3. Classify error: "upstream_timeout" (retryable)
   ↓
4. Exclude Node A
   ↓
5. Select Node B (same priority)
   ↓
6. Forward to Node B
   ↓
7. Node B returns 503
   ↓
8. Classify error: "upstream_5xx" (retryable)
   ↓
9. Exclude Node B
   ↓
10. Select Node C (lower priority)
    ↓
11. Forward to Node C
    ↓
12. Success! Return to client
```

### Timeout Flow (Queue Wait)

```
1. Client sends request
   ↓
2. Enqueue (position: 100)
   ↓
3. Wait in queue...
   ↓
4. 30 minutes elapsed
   ↓
5. Context timeout triggered
   ↓
6. Log timeout event
   ↓
7. Return 504 to client
```

---

## Queue & Retry System

See [QUEUE_AND_RETRY_DESIGN.md](./QUEUE_AND_RETRY_DESIGN.md) for detailed design.

**Key Components**:
- `PriorityQueue`: Min-heap based priority queue
- `WorkerPool`: Fixed-size worker pool
- `retryableRecorder`: Smart response buffering for retries

**Configuration**:
```toml
[queue]
max_queue_size = 10000
default_priority = 0

[worker]
pool_size = 10
max_retry_attempts = 3
retry_delay_ms = 100
max_wait_time_sec = 1800  # 30 minutes
```

---

## Rate Limiting

**Implementation**:
- Uses Redis or in-memory cache
- Sliding window algorithm
- Atomic increment operations

**TPM Calculation**:
- Non-streaming: Extract from response `usage` field
- Streaming: Parse from final SSE chunk or estimate (len/4)

**Approximate Nature**:
- TPM is recorded asynchronously after response
- Slight over-limit possible during high concurrency
- Acceptable trade-off for performance

---

## Logging & Observability

See [QUEUE_AND_RETRY_LOGGING.md](./QUEUE_AND_RETRY_LOGGING.md) for detailed logging design.

**Log Events**:
- `request_received`: Request arrival
- `request_enqueued`: Added to queue
- `request_dequeued`: Picked by worker
- `ratelimit_check`: Rate limit result
- `node_attempt_start`: Node attempt begins
- `node_attempt_failed`: Node attempt fails
- `node_attempt_success`: Node attempt succeeds
- `request_completed`: Request succeeds
- `request_failed`: All nodes failed
- `request_timeout`: Queue wait timeout
- `request_cancelled`: Client disconnected

**Log Format**: JSON (structured logging with `slog`)

**Query Examples**:
```bash
# View complete request chain
grep "req-abc123" /var/log/proxyllm.log | jq .

# Find all timeouts
grep '"event":"request_timeout"' /var/log/proxyllm.log

# Node failure rate
grep '"event":"node_attempt_failed"' /var/log/proxyllm.log | \
  jq -r '.node_id' | sort | uniq -c
```

---

## Configuration

### Minimal Configuration

```toml
[server]
addr = ":8080"
admin_token = "your-secret-token"

[providers.openai]
base_url = "https://api.openai.com"
api_key = "sk-..."
aliases = ["gpt-4", "gpt-3.5-turbo"]
enabled = true
```

### Full Configuration

See `config.toml` for all available options.

**Key Sections**:
- `[server]`: HTTP server settings
- `[queue]`: Queue configuration
- `[worker]`: Worker pool settings
- `[rate_limit]`: Global rate limits
- `[logging]`: Log retention and buffer settings
- `[cache]`: Cache backend (memory/redis)
- `[mq]`: Message queue backend (memory/redis/rabbitmq)
- `[providers.*]`: Backend node configurations
- `[api_keys.*]`: Pre-configured API keys

---

## Performance Considerations

**Queue Performance**:
- Enqueue: O(log N)
- Dequeue: O(M * log N) where M = number of models
- Memory: O(N) where N = queue size

**Worker Pool**:
- Fixed concurrency prevents upstream overload
- Configurable worker count (default: 10)

**Rate Limiting**:
- Cache-based, very fast (< 1ms)
- Redis recommended for multi-instance deployments

**Logging**:
- Asynchronous, non-blocking
- Buffered writes (default: 1000 entries)
- Periodic flush (default: 5 seconds)

---

## Security

**Authentication**:
- Client requests: Bearer token (API key)
- Admin endpoints: Separate admin token

**API Key Management**:
- Stored in SQLite
- Per-key rate limits
- Model access control (allow list)
- Optional expiration dates

**Admin Token**:
- Required for all `/admin/*` endpoints
- If empty, admin endpoints are disabled
- Should be kept secret and rotated regularly

---

## Deployment

**Single Instance**:
- Use in-memory cache and queue
- Suitable for < 1000 req/min

**Multi-Instance**:
- Use Redis for cache and queue
- Deploy behind load balancer
- Share SQLite via NFS or migrate to PostgreSQL (future)

**Docker**:
```bash
docker-compose up -d
```

**Binary**:
```bash
./proxyllm -config config.toml
```

---

## Future Enhancements

- [ ] PostgreSQL support for multi-instance deployments
- [ ] Prometheus metrics export
- [ ] OpenTelemetry tracing
- [ ] Web UI for monitoring and management
- [ ] More sophisticated retry strategies (exponential backoff, circuit breaker)
- [ ] Request deduplication
- [ ] Response caching

---

For implementation details, see:
- [Queue & Retry Design](./QUEUE_AND_RETRY_DESIGN.md)
- [Logging Design](./QUEUE_AND_RETRY_LOGGING.md)
- [Code Review](./CODE_REVIEW_REPORT.md)
- [Implementation Review](./IMPLEMENTATION_REVIEW.md)
