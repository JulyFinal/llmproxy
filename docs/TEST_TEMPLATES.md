# 测试补全计划

本文档提供具体的测试代码模板，可直接使用或根据需要调整。

---

## 1. internal/auth/auth_test.go

```go
package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"proxyllm/internal/domain"
)

// Mock storage for testing
type mockStorage struct {
	keys map[string]*domain.APIKey
}

func (m *mockStorage) GetAPIKeyByValue(ctx context.Context, value string) (*domain.APIKey, error) {
	key, ok := m.keys[value]
	if !ok {
		return nil, nil
	}
	return key, nil
}

func TestExtractToken(t *testing.T) {
	tests := []struct {
		name      string
		header    string
		wantToken string
		wantOK    bool
	}{
		{
			name:      "valid bearer token",
			header:    "Bearer pk-test123",
			wantToken: "pk-test123",
			wantOK:    true,
		},
		{
			name:      "missing authorization header",
			header:    "",
			wantToken: "",
			wantOK:    false,
		},
		{
			name:      "non-bearer scheme",
			header:    "Basic dXNlcjpwYXNz",
			wantToken: "",
			wantOK:    false,
		},
		{
			name:      "bearer with empty token",
			header:    "Bearer ",
			wantToken: "",
			wantOK:    false,
		},
		{
			name:      "bearer lowercase",
			header:    "bearer pk-test123",
			wantToken: "",
			wantOK:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, _ := http.NewRequest("GET", "/", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}

			token, ok := ExtractToken(req)
			if token != tt.wantToken || ok != tt.wantOK {
				t.Errorf("ExtractToken() = (%q, %v), want (%q, %v)",
					token, ok, tt.wantToken, tt.wantOK)
			}
		})
	}
}

func TestAuthenticator_Authenticate(t *testing.T) {
	now := time.Now()
	future := now.Add(24 * time.Hour)
	past := now.Add(-24 * time.Hour)

	store := &mockStorage{
		keys: map[string]*domain.APIKey{
			"valid-key": {
				ID:      "key1",
				Value:   "valid-key",
				Enabled: true,
			},
			"disabled-key": {
				ID:      "key2",
				Value:   "disabled-key",
				Enabled: false,
			},
			"expired-key": {
				ID:        "key3",
				Value:     "expired-key",
				Enabled:   true,
				ExpiresAt: &past,
			},
			"future-key": {
				ID:        "key4",
				Value:     "future-key",
				Enabled:   true,
				ExpiresAt: &future,
			},
		},
	}

	auth := NewAuthenticator(store)
	ctx := context.Background()

	tests := []struct {
		name    string
		token   string
		wantErr error
		wantID  string
	}{
		{
			name:    "valid key",
			token:   "valid-key",
			wantErr: nil,
			wantID:  "key1",
		},
		{
			name:    "disabled key",
			token:   "disabled-key",
			wantErr: ErrUnauthorized,
		},
		{
			name:    "expired key",
			token:   "expired-key",
			wantErr: ErrExpired,
		},
		{
			name:    "future expiry",
			token:   "future-key",
			wantErr: nil,
			wantID:  "key4",
		},
		{
			name:    "non-existent key",
			token:   "invalid-key",
			wantErr: ErrUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, err := auth.Authenticate(ctx, tt.token)
			if err != tt.wantErr {
				t.Errorf("Authenticate() error = %v, want %v", err, tt.wantErr)
				return
			}
			if err == nil && key.ID != tt.wantID {
				t.Errorf("Authenticate() key.ID = %v, want %v", key.ID, tt.wantID)
			}
		})
	}
}

func TestCheckModelAllowed(t *testing.T) {
	tests := []struct {
		name        string
		allowModels []string
		modelAlias  string
		want        bool
	}{
		{
			name:        "empty allow list - all allowed",
			allowModels: []string{},
			modelAlias:  "gpt-4",
			want:        true,
		},
		{
			name:        "nil allow list - all allowed",
			allowModels: nil,
			modelAlias:  "gpt-4",
			want:        true,
		},
		{
			name:        "model in allow list",
			allowModels: []string{"gpt-4", "gpt-3.5-turbo"},
			modelAlias:  "gpt-4",
			want:        true,
		},
		{
			name:        "model not in allow list",
			allowModels: []string{"gpt-4"},
			modelAlias:  "claude-3",
			want:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := &domain.APIKey{
				AllowModels: tt.allowModels,
			}
			if got := CheckModelAllowed(key, tt.modelAlias); got != tt.want {
				t.Errorf("CheckModelAllowed() = %v, want %v", got, tt.want)
			}
		})
	}
}
```

---

## 2. internal/auth/keygen_test.go

```go
package auth

import (
	"strings"
	"testing"
)

func TestGenerateKey(t *testing.T) {
	// Generate multiple keys to test uniqueness
	keys := make(map[string]bool)
	for i := 0; i < 100; i++ {
		key := GenerateKey()

		// Check format
		if !strings.HasPrefix(key, "pk-") {
			t.Errorf("GenerateKey() = %q, want prefix 'pk-'", key)
		}

		// Check length (pk- + 48 chars)
		if len(key) != 51 {
			t.Errorf("GenerateKey() length = %d, want 51", len(key))
		}

		// Check uniqueness
		if keys[key] {
			t.Errorf("GenerateKey() generated duplicate: %q", key)
		}
		keys[key] = true

		// Check characters are base62
		body := strings.TrimPrefix(key, "pk-")
		for _, ch := range body {
			if !strings.ContainsRune(base62Chars, ch) {
				t.Errorf("GenerateKey() contains invalid char: %c", ch)
			}
		}
	}
}

func TestGenerateID(t *testing.T) {
	ids := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := GenerateID()

		// Check length (16 hex chars)
		if len(id) != 16 {
			t.Errorf("GenerateID() length = %d, want 16", len(id))
		}

		// Check uniqueness
		if ids[id] {
			t.Errorf("GenerateID() generated duplicate: %q", id)
		}
		ids[id] = true

		// Check all chars are hex
		for _, ch := range id {
			if !strings.ContainsRune("0123456789abcdef", ch) {
				t.Errorf("GenerateID() contains invalid hex char: %c", ch)
			}
		}
	}
}
```

---

## 3. internal/storage/memory/cache_test.go

```go
package memory

import (
	"context"
	"testing"
	"time"
)

func TestMemoryCache_GetSet(t *testing.T) {
	cache := NewMemoryCache()
	defer cache.Close()

	ctx := context.Background()
	key := "test-key"
	value := []byte("test-value")

	// Get non-existent key
	_, ok := cache.Get(ctx, key)
	if ok {
		t.Error("Get() on non-existent key should return false")
	}

	// Set and get
	if err := cache.Set(ctx, key, value, 0); err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	got, ok := cache.Get(ctx, key)
	if !ok {
		t.Fatal("Get() should return true after Set()")
	}
	if string(got) != string(value) {
		t.Errorf("Get() = %q, want %q", got, value)
	}
}

func TestMemoryCache_Expiration(t *testing.T) {
	cache := NewMemoryCache()
	defer cache.Close()

	ctx := context.Background()
	key := "expire-key"
	value := []byte("expire-value")

	// Set with 100ms TTL
	if err := cache.Set(ctx, key, value, 100*time.Millisecond); err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	// Should exist immediately
	if _, ok := cache.Get(ctx, key); !ok {
		t.Error("Get() should return true immediately after Set()")
	}

	// Wait for expiration
	time.Sleep(150 * time.Millisecond)

	// Should be expired
	if _, ok := cache.Get(ctx, key); ok {
		t.Error("Get() should return false after TTL expires")
	}
}

func TestMemoryCache_Delete(t *testing.T) {
	cache := NewMemoryCache()
	defer cache.Close()

	ctx := context.Background()
	key := "delete-key"
	value := []byte("delete-value")

	cache.Set(ctx, key, value, 0)
	if err := cache.Delete(ctx, key); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	if _, ok := cache.Get(ctx, key); ok {
		t.Error("Get() should return false after Delete()")
	}
}

func TestMemoryCache_IncrBy(t *testing.T) {
	cache := NewMemoryCache()
	defer cache.Close()

	ctx := context.Background()
	key := "counter"

	// First increment creates the key
	val, err := cache.IncrBy(ctx, key, 5, 0)
	if err != nil {
		t.Fatalf("IncrBy() error = %v", err)
	}
	if val != 5 {
		t.Errorf("IncrBy() = %d, want 5", val)
	}

	// Second increment adds to existing
	val, err = cache.IncrBy(ctx, key, 3, 0)
	if err != nil {
		t.Fatalf("IncrBy() error = %v", err)
	}
	if val != 8 {
		t.Errorf("IncrBy() = %d, want 8", val)
	}

	// Negative increment
	val, err = cache.IncrBy(ctx, key, -2, 0)
	if err != nil {
		t.Fatalf("IncrBy() error = %v", err)
	}
	if val != 6 {
		t.Errorf("IncrBy() = %d, want 6", val)
	}
}

func TestMemoryCache_IncrBy_TTL(t *testing.T) {
	cache := NewMemoryCache()
	defer cache.Close()

	ctx := context.Background()
	key := "counter-ttl"

	// Create with TTL
	_, err := cache.IncrBy(ctx, key, 1, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("IncrBy() error = %v", err)
	}

	// Increment again (should NOT reset TTL)
	_, err = cache.IncrBy(ctx, key, 1, 1*time.Hour)
	if err != nil {
		t.Fatalf("IncrBy() error = %v", err)
	}

	// Wait for original TTL to expire
	time.Sleep(150 * time.Millisecond)

	// Should be expired (TTL not reset)
	if _, ok := cache.Get(ctx, key); ok {
		t.Error("Key should be expired (TTL should not reset on subsequent IncrBy)")
	}
}

func TestMemoryCache_Cleanup(t *testing.T) {
	cache := NewMemoryCache()
	defer cache.Close()

	ctx := context.Background()

	// Create multiple keys with short TTL
	for i := 0; i < 10; i++ {
		key := string(rune('a' + i))
		cache.Set(ctx, key, []byte("value"), 50*time.Millisecond)
	}

	// Wait for cleanup to run (runs every 10s, but we'll wait for expiration)
	time.Sleep(100 * time.Millisecond)

	// Trigger cleanup by waiting
	time.Sleep(11 * time.Second)

	// All keys should be cleaned up
	cache.mu.RLock()
	count := len(cache.entries)
	cache.mu.RUnlock()

	if count > 0 {
		t.Errorf("cleanup() should have removed expired entries, got %d remaining", count)
	}
}
```

---

## 4. internal/storage/memory/queue_test.go

```go
package memory

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMemoryQueue_PublishSubscribe(t *testing.T) {
	q := NewMemoryQueue(0)
	defer q.Close()

	ctx := context.Background()
	topic := "test-topic"
	payload := []byte("test-message")

	var received []byte
	var wg sync.WaitGroup
	wg.Add(1)

	err := q.Subscribe(ctx, topic, func(ctx context.Context, msg []byte) error {
		received = msg
		wg.Done()
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}

	// Give subscriber time to start
	time.Sleep(10 * time.Millisecond)

	if err := q.Publish(ctx, topic, payload); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	// Wait for message
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		if string(received) != string(payload) {
			t.Errorf("received = %q, want %q", received, payload)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for message")
	}
}

func TestMemoryQueue_MultipleSubs cribers(t *testing.T) {
	q := NewMemoryQueue(0)
	defer q.Close()

	ctx := context.Background()
	topic := "fanout-topic"
	payload := []byte("broadcast")

	var count atomic.Int32
	var wg sync.WaitGroup

	// Create 3 subscribers
	for i := 0; i < 3; i++ {
		wg.Add(1)
		err := q.Subscribe(ctx, topic, func(ctx context.Context, msg []byte) error {
			count.Add(1)
			wg.Done()
			return nil
		})
		if err != nil {
			t.Fatalf("Subscribe() error = %v", err)
		}
	}

	time.Sleep(10 * time.Millisecond)

	if err := q.Publish(ctx, topic, payload); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	// Wait for all subscribers
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		if count.Load() != 3 {
			t.Errorf("received count = %d, want 3", count.Load())
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for subscribers")
	}
}

func TestMemoryQueue_ContextCancellation(t *testing.T) {
	q := NewMemoryQueue(0)
	defer q.Close()

	ctx, cancel := context.WithCancel(context.Background())
	topic := "cancel-topic"

	var received atomic.Int32
	err := q.Subscribe(ctx, topic, func(ctx context.Context, msg []byte) error {
		received.Add(1)
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}

	time.Sleep(10 * time.Millisecond)

	// Send first message
	q.Publish(context.Background(), topic, []byte("msg1"))
	time.Sleep(10 * time.Millisecond)

	// Cancel context
	cancel()
	time.Sleep(10 * time.Millisecond)

	// Send second message (should not be received)
	q.Publish(context.Background(), topic, []byte("msg2"))
	time.Sleep(10 * time.Millisecond)

	if received.Load() != 1 {
		t.Errorf("received count = %d, want 1 (second message should be dropped)", received.Load())
	}
}

func TestMemoryQueue_BufferFull(t *testing.T) {
	q := NewMemoryQueue(2) // Small buffer
	defer q.Close()

	ctx := context.Background()
	topic := "buffer-topic"

	// Don't subscribe, so messages accumulate

	// Fill buffer
	for i := 0; i < 2; i++ {
		if err := q.Publish(ctx, topic, []byte("msg")); err != nil {
			t.Fatalf("Publish() error = %v on message %d", err, i)
		}
	}

	// Next publish should fail (buffer full)
	err := q.Publish(ctx, topic, []byte("overflow"))
	if err == nil {
		t.Error("Publish() should return error when buffer is full")
	}
}
```

---

## 5. internal/logging/logger_test.go

```go
package logging

import (
	"context"
	"testing"
	"time"

	"proxyllm/internal/domain"
	"proxyllm/internal/storage/sqlite"
)

func TestAsyncLogger_AsyncLog(t *testing.T) {
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer db.Close()

	sl := sqlite.NewSQLiteLogger(db)
	logger := New(sl, 10, 100*time.Millisecond)
	defer logger.Close()

	// Log a request
	log := &domain.RequestLog{
		ID:         "req-123",
		SessionID:  "sess-456",
		Timestamp:  time.Now(),
		APIKeyID:   "key-789",
		ModelAlias: "gpt-4",
		NodeID:     "node-1",
	}

	logger.AsyncLog(log)

	// Flush to ensure write
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	if err := logger.Flush(ctx); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}

	// Query back
	logs, _, err := logger.QueryLogs(ctx, domain.LogFilter{})
	if err != nil {
		t.Fatalf("QueryLogs() error = %v", err)
	}

	if len(logs) != 1 {
		t.Fatalf("QueryLogs() returned %d logs, want 1", len(logs))
	}

	if logs[0].ID != log.ID {
		t.Errorf("QueryLogs() ID = %q, want %q", logs[0].ID, log.ID)
	}
}

func TestAsyncLogger_BufferFlush(t *testing.T) {
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer db.Close()

	sl := sqlite.NewSQLiteLogger(db)
	logger := New(sl, 5, 10*time.Second) // Small buffer, long interval
	defer logger.Close()

	ctx := context.Background()

	// Send 5 logs to fill buffer
	for i := 0; i < 5; i++ {
		logger.AsyncLog(&domain.RequestLog{
			ID:        string(rune('a' + i)),
			Timestamp: time.Now(),
		})
	}

	// Buffer should auto-flush
	time.Sleep(100 * time.Millisecond)

	logs, _, err := logger.QueryLogs(ctx, domain.LogFilter{})
	if err != nil {
		t.Fatalf("QueryLogs() error = %v", err)
	}

	if len(logs) != 5 {
		t.Errorf("QueryLogs() returned %d logs, want 5 (auto-flush)", len(logs))
	}
}
```

---

## 使用说明

1. **创建测试文件**: 将上述代码复制到对应的 `_test.go` 文件中
2. **运行测试**: `go test ./internal/auth -v`
3. **检查覆盖率**: `go test ./internal/auth -cover`
4. **生成覆盖率报告**: 
   ```bash
   go test ./internal/auth -coverprofile=coverage.out
   go tool cover -html=coverage.out
   ```

## 注意事项

- 某些测试（如 `TestMemoryCache_Cleanup`）依赖时间，可能在 CI 环境中不稳定
- 集成测试（Redis、RabbitMQ）需要外部服务，建议使用 Docker Compose 或跳过
- 使用 `-race` 标志运行测试以检测竞态条件：`go test -race ./...`
