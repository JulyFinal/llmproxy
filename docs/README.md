# ProxyLLM

ProxyLLM is a high-performance, OpenAI-compatible API proxy and load balancer for managing multiple LLM backends.

It provides seamless routing, intelligent request queuing, comprehensive rate limiting (RPM & TPM), automatic retry with failover, Prometheus metrics, and detailed logging — ideal for teams consolidating API keys and managing model traffic.

## Features

### Core
- **OpenAI Compatible** — Drop-in replacement for any app using the OpenAI API format (`/v1/chat/completions`, `/v1/embeddings`, `/v1/responses`, `/v1/models`).
- **Priority Queue** — Higher-priority requests are processed first. Configurable max queue size and wait time.
- **Smart Retry & Failover** — Automatic node switching on 5xx, 429, timeout, or connection errors.
- **Load Balancing** — Random selection across healthy nodes with per-request exclusion on failure.
- **Sliding Window Rate Limiting** — Three-layer (Global → Model → API Key) enforcement of RPM and TPM using a weighted moving average.

### Reliability & Observability
- **Prometheus Metrics** — `/metrics` endpoint exposing request counts, token usage, queue depth, and worker utilization.
- **Health Check** — `/health` verifies DB and cache connectivity; returns 503 on failure.
- **Complete Chain Logging** — Full request lifecycle tracking with two-tier async logging (compact summary + full request/response bodies).
- **Streaming Support** — Robust SSE handling with 10 MB buffer for large chunks (tool calls, structured output).
- **Timeout Control** — Configurable max wait time per request (default 30 min). Proxy controls timeout, not the client.

### Admin & Security
- **Admin REST API** — Manage nodes, API keys, view logs and stats dynamically.
- **Constant-Time Token Comparison** — Admin token validated with `crypto/subtle` to prevent timing attacks.
- **Persistent Storage** — SQLite (WAL mode) for configuration and async logging with automatic retention cleanup.
- **Pluggable Backends** — In-memory, Redis, or RabbitMQ for cache and message queue.

## Quick Start

### Docker Compose

```bash
git clone <REPO_URL>
cd proxyllm

# Edit data/config.toml, data/providers.toml, data/api_keys.toml
docker-compose -f docker/docker-compose.yml up -d

curl http://localhost:8011/health
```

### Binary

```bash
cd src && go build -o proxyllm ./cmd/proxyllm
./proxyllm -data ./data
```

## Configuration

ProxyLLM is configured via TOML files in a data directory (`config.toml`, `providers.toml`, `api_keys.toml`) with environment variable overrides. Nodes and keys from config are upserted into SQLite on startup.

See [`data/config.toml.example`](../data/config.toml.example) for the full annotated reference.

Key environment variables:

| Variable | Description | Default |
|----------|-------------|---------|
| `PROXYLLM_SERVER_ADDR` | Listen address | `:8080` |
| `PROXYLLM_SERVER_ADMIN_TOKEN` | Admin API bearer token | — |
| `PROXYLLM_DB_PATH` | SQLite database path | `proxyllm.db` |
| `PROXYLLM_CACHE_TYPE` | `memory` or `redis` | `memory` |
| `PROXYLLM_MQ_TYPE` | `memory`, `redis`, or `rabbitmq` | `memory` |

## Usage

Point your OpenAI SDK or HTTP client to `http://localhost:8080/v1`.

### Chat Completion

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer <CLIENT_API_KEY>" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4",
    "messages": [{"role": "user", "content": "Hello!"}]
  }'
```

### Priority Requests

Add a `priority` field (higher = processed first, default: 0):

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer <CLIENT_API_KEY>" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4",
    "messages": [{"role": "user", "content": "Urgent!"}],
    "priority": 10
  }'
```

### Admin API

Requires `admin_token` from config. All endpoints under `/admin/` are read-only — nodes and keys are managed via TOML config files.

```bash
# View all nodes
curl http://localhost:8080/admin/nodes \
  -H "Authorization: Bearer <ADMIN_TOKEN>"

# View all API keys (redacted)
curl http://localhost:8080/admin/keys \
  -H "Authorization: Bearer <ADMIN_TOKEN>"

# View stats
curl http://localhost:8080/admin/stats \
  -H "Authorization: Bearer <ADMIN_TOKEN>"

# Prometheus metrics
curl http://localhost:8080/metrics \
  -H "Authorization: Bearer <ADMIN_TOKEN>"
```

## API Endpoints

| Endpoint | Auth | Description |
|----------|------|-------------|
| `POST /v1/chat/completions` | API Key | Chat completion (streaming supported) |
| `POST /v1/completions` | API Key | Text completion |
| `POST /v1/embeddings` | API Key | Embeddings |
| `POST /v1/responses` | API Key | Responses API |
| `GET /v1/models` | API Key | List available models |
| `GET /health` | None | Health check with DB/cache status |
| `GET /metrics` | Admin | Prometheus metrics |
| `GET /admin/nodes` | Admin | List all nodes |
| `GET /admin/nodes/{id}` | Admin | Get node by ID |
| `GET /admin/keys` | Admin | List API keys (redacted) |
| `GET /admin/logs` | Admin | Query request logs (paginated) |
| `GET /admin/logs/export` | Admin | Export logs as JSONL |
| `GET /admin/logs/{trace_id}` | Admin | Get request detail |
| `GET /admin/stats` | Admin | Aggregate stats |
| `GET /admin/stats/timeseries` | Admin | Time-bucketed stats |
| `GET /admin/stats/top` | Admin | Top models/keys by usage |

## Architecture

```
Client → HTTP Server → Auth → Priority Queue → Worker Pool → Router → Upstream LLM
                                                    ↕              ↕
                                              Rate Limiter    Retry/Failover
                                                    ↕
                                              Cache (Memory/Redis)
```

- **Priority Queue**: Global min-heap with lazy deletion of cancelled requests. O(log N) enqueue/dequeue.
- **Worker Pool**: Fixed concurrency (default 10). Each worker dequeues, checks rate limits, and forwards with retry.
- **Rate Limiter**: Sliding window (weighted moving average) across global, model, and key layers.
- **Retry**: Up to 3 attempts across different nodes. Retryable: 5xx, 429, timeout, connection errors. Non-retryable: 4xx.

## Documentation

- [Architecture & Features](./docs/guide.md)
- [Queue & Retry Design](./docs/QUEUE_AND_RETRY_DESIGN.md)
- [Chain Logging](./docs/QUEUE_AND_RETRY_LOGGING.md)
- [Code Review Report](./docs/FULL_CODE_REVIEW.md)
- [Changelog](./CHANGELOG.md)
- [Agent Guidelines](./AGENTS.md) (for AI development)

## Tech Stack

- Go 1.24+
- SQLite (WAL mode, via `mattn/go-sqlite3`, requires CGO)
- Standard library `net/http` (no web framework)
- Optional: Redis, RabbitMQ

## License

MIT
