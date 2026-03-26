package ratelimit

import (
	"context"
	"testing"
	"time"

	"proxyllm/internal/domain"
)

type mockCache struct {
	counts map[string]int64
	vals   map[string][]byte
}

func (m *mockCache) Get(ctx context.Context, key string) ([]byte, bool) {
	v, ok := m.vals[key]
	return v, ok
}
func (m *mockCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	m.vals[key] = value
	return nil
}
func (m *mockCache) Delete(ctx context.Context, key string) error {
	delete(m.counts, key)
	delete(m.vals, key)
	return nil
}
func (m *mockCache) IncrBy(ctx context.Context, key string, delta int64, ttl time.Duration) (int64, error) {
	if m.counts == nil {
		m.counts = make(map[string]int64)
	}
	m.counts[key] += delta
	return m.counts[key], nil
}
func (m *mockCache) Close() error { return nil }

func TestLimiter_AllowRequest(t *testing.T) {
	ctx := context.Background()
	cache := &mockCache{counts: make(map[string]int64), vals: make(map[string][]byte)}

	global := domain.RateLimitConfig{RPM: 2, TPM: 100}
	modelLimits := map[string]domain.RateLimitConfig{
		"gpt-4": {RPM: 1, TPM: 50},
	}
	keyLimits := func(keyID string) *domain.RateLimitConfig {
		if keyID == "key1" {
			return &domain.RateLimitConfig{RPM: 1, TPM: 10}
		}
		return nil
	}

	lim := New(cache, global, modelLimits, keyLimits)

	// Test RPM Model Limit
	allowed, _ := lim.AllowRequest(ctx, "key-other", "gpt-4")
	if !allowed {
		t.Fatal("expected gpt-4 (1st req) to be allowed")
	}
	allowed, _ = lim.AllowRequest(ctx, "key-other", "gpt-4")
	if allowed {
		t.Fatal("expected gpt-4 (2nd req) to be rejected by model RPM limit (1)")
	}

	// Reset cache for next test
	cache.counts = make(map[string]int64)
	cache.vals = make(map[string][]byte)

	// Test TPM Key Limit
	// Inject TPM usage into cache
	ts := windowTimestamp()
	cache.vals[tpmKey("key", "key1", ts)] = []byte("10")

	allowed, _ = lim.AllowRequest(ctx, "key1", "gpt-3.5")
	if allowed {
		t.Fatal("expected key1 to be rejected by TPM limit (10)")
	}

	// Test inherit global
	allowed, _ = lim.AllowRequest(ctx, "key-other", "gpt-3.5")
	if !allowed {
		t.Fatal("expected gpt-3.5 to inherit global RPM (2)")
	}
	allowed, _ = lim.AllowRequest(ctx, "key-other", "gpt-3.5")
	if !allowed {
		t.Fatal("expected gpt-3.5 2nd req to be allowed (global RPM=2)")
	}
	allowed, _ = lim.AllowRequest(ctx, "key-other", "gpt-3.5")
	if allowed {
		t.Fatal("expected gpt-3.5 3rd req to be rejected by global RPM (2)")
	}
}

func TestLimiter_UpdateModelLimits(t *testing.T) {
	ctx := context.Background()
	cache := &mockCache{counts: make(map[string]int64), vals: make(map[string][]byte)}
	lim := New(cache, domain.RateLimitConfig{RPM: 100}, nil, nil)

	// Initially unlimited for unknown model
	allowed, _ := lim.AllowRequest(ctx, "key1", "new-model")
	if !allowed {
		t.Fatal("expected new-model to be allowed initially")
	}

	// Update limits
	lim.UpdateModelLimits(map[string]domain.RateLimitConfig{
		"new-model": {RPM: 1},
	})

	// Second call: count becomes 1. 1 <= 1. Allowed.
	allowed, _ = lim.AllowRequest(ctx, "key1", "new-model")
	if !allowed {
		t.Fatal("expected new-model 2nd req to be allowed (count 1)")
	}

	// Third call: count becomes 2. 2 > 1. Rejected.
	allowed, _ = lim.AllowRequest(ctx, "key1", "new-model")
	if allowed {
		t.Fatal("expected new-model 3rd req to be rejected after limit update")
	}
}
