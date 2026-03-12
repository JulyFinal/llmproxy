# Development Progress & TODOs

*This file serves as memory context for future LLM or developer sessions.*

## Current Status (Last Updated: 2026-03-11)

### ✅ Completed Features

**Core Proxy Functionality**:
- ✅ OpenAI-compatible API endpoints
- ✅ Streaming support with 10MB buffer for large tool calls
- ✅ Simplified multi-node routing with uniform random load balancing (Priority/Weight removed for simplicity)

**Rate Limiting**:
- ✅ Three-tier rate limiting (Global → Model → API Key)
- ✅ RPM (Requests Per Minute) enforcement
- ✅ TPM (Tokens Per Minute) tracking (async, approximate)
- ✅ Fixed memory cache encoding bug

**Queue & Retry System**:
- ✅ Priority-based request queue (per model) - client can specify priority in request body
- ✅ Worker pool with fixed concurrency
- ✅ Smart retry with automatic node failover
- ✅ Lazy deletion for cancelled requests
- ✅ Configurable max wait time (30min/1h)

**Logging & Observability**:
- ✅ Complete chain logging (request lifecycle tracking)
- ✅ Structured JSON logs with slog
- ✅ Error classification and attribution
- ✅ Two-tier logging (RequestLog + DetailLog)
- ✅ Rich analytics endpoints (timeseries, top entities)
- ✅ Log retention by rows/days/size

**Admin API**:
- ✅ Node management (CRUD)
- ✅ API Key management (CRUD)
- ✅ Log querying with filters
- ✅ Statistics and analytics
- ✅ Robust partial update support (merge logic based on field presence)
- ✅ Security: admin token required

**Testing**:
- ✅ Router tests
- ✅ Rate limiter tests
- ✅ Proxy streaming tests
- ✅ SQLite storage tests
- ✅ Admin API tests

---

## 📋 Known Issues & Improvements

### Minor Issues (Low Priority)

1. **ActualModel field not populated** (`internal/api/openai.go:220`)
   - Impact: Logs don't show actual upstream model name
   - Fix: Extract from `node.ModelName`

2. **Rate limit status incomplete** (`internal/worker/pool.go:96`)
   - Impact: Logs don't show exact RPM/TPM values
   - Fix: Add `GetStatus()` method to limiter

3. **Queue position estimation** (`internal/queue/priority_queue.go:107`)
   - Impact: Estimated position may not be exact
   - Fix: Calculate exact position (O(N) cost)

---

## 🎯 Future Enhancements

### High Priority

1. **Test Coverage**
   - Add tests for `internal/queue` (priority queue)
   - Add tests for `internal/worker` (worker pool)
   - Target: 70%+ overall coverage

2. **Monitoring & Metrics**
   - Export Prometheus metrics
   - Add `/metrics` endpoint

---

## 📝 Notes for Future Sessions

**Architecture Decisions**:
- **Simplified Routing**: Removed node-level `Priority` and `Weight`. Nodes are selected uniformly at random.
- **Queue system**: Uses `container/heap` for priority ordering.
- **Worker pool**: Uses fixed concurrency to prevent upstream overload.
- **Retry logic**: Uses `retryableRecorder` to buffer responses and allow switching nodes before committing the stream to the client.
- **Lazy deletion**: In queue avoids O(N) removal operations when clients disconnect.

---

**Last Updated**: 2026-03-11
