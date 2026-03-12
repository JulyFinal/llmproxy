# ProxyLLM

ProxyLLM is a high-performance, OpenAI-compatible API proxy and load balancer designed for managing multiple Large Language Model (LLM) backends. 

It provides seamless routing, intelligent request queuing, comprehensive rate limiting (RPM & TPM), automatic retry with failover, and detailed logging, making it ideal for teams looking to consolidate API keys and manage model traffic effectively.

## Features

### Core Capabilities
- **OpenAI Compatible**: Drop-in replacement for any application using the OpenAI API format.
- **Priority Request Queue**: Intelligent queuing system with priority support - high-priority requests processed first.
- **Smart Retry & Failover**: Automatic node switching on failure - if one backend times out or errors, seamlessly tries another.
- **Load Balancing**: Distribute traffic across multiple backend nodes.
- **Advanced Rate Limiting**: Tri-layer rate limiting (Global -> Model -> API Key) enforcing both Requests Per Minute (RPM) and Tokens Per Minute (TPM).

### Reliability & Observability
- **Complete Chain Logging**: Full request lifecycle tracking - from arrival to completion, with detailed error attribution.
- **Streaming Support**: Robust handling of Server-Sent Events (SSE) including large data chunks (e.g., for structured outputs or tool calling).
- **Timeout Control**: Configurable max wait time (e.g., 30min/1h) - proxy controls timeout, not the client.
- **Admin Management API**: RESTful API for managing backend nodes, API keys, and monitoring logs dynamically.

### Security & Storage
- **Security-First**: Strict token-based access control and admin endpoints protection.
- **Persistent Storage**: SQLite-backed state management for configurations and detailed asynchronous logging.

## Quick Start (Docker)

1. **Clone the repository**:
   ```bash
   git clone https://github.com/yourusername/proxyllm.git
   cd proxyllm
   ```

2. **Start with Docker Compose**:
   ```bash
   docker-compose up -d
   ```

3. **Verify it's running**:
   ```bash
   curl http://localhost:8080/health
   ```

## Using the Proxy

Once running, you can point your OpenAI SDK or any standard HTTP client to `http://localhost:8080/v1`. 

### Priority Requests (Optional)

ProxyLLM supports request-level priority. Higher priority requests are processed first when the queue is busy:

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer <CLIENT_API_KEY>" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4",
    "messages": [{"role": "user", "content": "Urgent request!"}],
    "priority": 10
  }'
```

**Priority levels**: Higher numbers = higher priority (default: 0)

### Add a Backend Node (Admin API)
*Requires `PROXYLLM_SERVER_ADMIN_TOKEN` configured in docker-compose.yml or config.toml.*

```bash
curl -X POST http://localhost:8080/admin/nodes \
  -H "Authorization: Bearer super-secret-admin-token" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "OpenAI Primary",
    "aliases": ["gpt-4", "gpt-3.5-turbo"],
    "base_url": "https://api.openai.com",
    "api_key": "sk-your-openai-key",
    "model_name": "gpt-4",
    "enabled": true
  }'
```

### Create a Client API Key (Admin API)

```bash
curl -X POST http://localhost:8080/admin/keys \
  -H "Authorization: Bearer super-secret-admin-token" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Frontend Team App",
    "rate_limit": {
        "rpm": 50,
        "tpm": 10000
    },
    "allow_models": ["gpt-4"]
  }'
```

### Make an LLM Request

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer <CLIENT_API_KEY_FROM_PREVIOUS_STEP>" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4",
    "messages": [{"role": "user", "content": "Hello, world!"}]
  }'
```

## Documentation

- [Architecture & Features Guide](./docs/guide.md)
- [Queue & Retry System Design](./docs/QUEUE_AND_RETRY_DESIGN.md)
- [Complete Chain Logging](./docs/QUEUE_AND_RETRY_LOGGING.md)
- [Code Review Report](./docs/CODE_REVIEW_REPORT.md)
- [Implementation Review](./docs/IMPLEMENTATION_REVIEW.md)
- [Test Templates](./docs/TEST_TEMPLATES.md)
- [Agent Guidelines](./AGENTS.md) (For AI Development)
