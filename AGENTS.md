# AGENT GUIDELINES

Welcome, Claude Code / Codex / Gemini CLI. When working on `ProxyLLM`, please adhere strictly to these architectural and stylistic directives.

## Tech Stack
- **Language**: Go 1.22+
- **Storage**: SQLite (`github.com/mattn/go-sqlite3`) - Note: Requires CGO.
- **Routing**: `net/http` standard library (no third-party web frameworks like Gin/Echo, keep it dependency-light).

## Core Architectural Rules

1. **State Consistency**: Memory and DB must stay synced. When modifying a node or an API key via an API handler, you MUST ensure that the in-memory `router.Router` and the `ratelimit.Limiter` are refreshed immediately (e.g., via `syncRouter`). Do not require a restart to apply changes.
2. **No Silent Panics**: Never assume a pointer is non-nil in HTTP handlers (e.g., always check if `store.GetNode` returns `nil` before modifying).
3. **Streaming Resilience**: When dealing with OpenAI streaming endpoints (SSE), remember that chunks can be massive (e.g., parallel tool calls). Always use suitably sized buffers (we currently use a 10MB buffer for `bufio.Scanner`).
4. **Database Migrations**: We currently use a basic `migrate()` function in `sqlite/storage.go` with `ALTER TABLE` for schema evolution. If you add a field to a struct, ensure it's added to the SQLite migration block, the `Upsert` statement, and the `Scan` unmarshaler.
5. **Partial Updates (Merge)**: Whenever implementing `PUT` or `PATCH` APIs, do not blindly overwrite existing DB records. Fetch the existing record, merge the provided non-zero fields, and then `Upsert`.

## LLM Interaction Mandates

- **Explain Before Acting**: Provide a concise one-sentence intent before making tool calls (e.g., "I will modify the router to support fallback logic.").
- **Test What You Touch**: We maintain high test coverage in `internal/`. If you alter routing, ratelimiting, or proxy stream logic, you must write or update the corresponding `_test.go` file. Run `go test ./...` to validate.
- **Reference Context**: Before starting a new feature, read `docs/llm/progress_and_todos.md` to understand where the last session left off. Update it when you complete a significant milestone.
