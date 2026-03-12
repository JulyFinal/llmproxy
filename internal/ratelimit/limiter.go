// Package ratelimit implements the three-layer rate limiter:
// global → per-model → per-key.
//
// RPM is checked synchronously before the request is forwarded.
// TPM is recorded asynchronously after the response completes.
//
// All counters live in the Cache (sliding 1-minute window).
// Key schema:
//
//	rl:rpm:global:{windowTs}
//	rl:rpm:model:{alias}:{windowTs}
//	rl:rpm:key:{keyID}:{windowTs}
//	rl:tpm:global:{windowTs}
//	rl:tpm:model:{alias}:{windowTs}
//	rl:tpm:key:{keyID}:{windowTs}
package ratelimit

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"proxyllm/internal/domain"
	"proxyllm/internal/storage"
)

// Limiter is the concrete RateLimiter implementation.
type Limiter struct {
	mu          sync.RWMutex
	cache       storage.Cache
	global      domain.RateLimitConfig
	modelLimits map[string]domain.RateLimitConfig // alias → config
	keyLimits   func(keyID string) *domain.RateLimitConfig
}

// New creates a Limiter.
//   - cache: the cache backend
//   - global: app-level defaults
//   - modelLimits: per-alias overrides (may be nil)
//   - keyLimits: callback to look up per-key config; return nil to inherit global
func New(
	cache storage.Cache,
	global domain.RateLimitConfig,
	modelLimits map[string]domain.RateLimitConfig,
	keyLimits func(keyID string) *domain.RateLimitConfig,
) *Limiter {
	if modelLimits == nil {
		modelLimits = make(map[string]domain.RateLimitConfig)
	}
	return &Limiter{
		cache:       cache,
		global:      global,
		modelLimits: modelLimits,
		keyLimits:   keyLimits,
	}
}

// UpdateModelLimits replaces the per-model limit table at runtime.
func (l *Limiter) UpdateModelLimits(limits map[string]domain.RateLimitConfig) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.modelLimits = limits
}

// AllowRequest checks RPM and TPM budgets for all three layers.
// Returns (false, nil) when any layer is over budget.
func (l *Limiter) AllowRequest(ctx context.Context, keyID, modelAlias string) (bool, error) {
	ts := windowTimestamp()

	// 1. Check RPM (synchronous increment)
	rpmLayers := []struct {
		key string
		rpm int
	}{
		{rpmKey("global", "", ts), l.global.RPM},
		{rpmKey("model", modelAlias, ts), l.modelRPM(modelAlias)},
		{rpmKey("key", keyID, ts), l.keyRPM(keyID)},
	}

	var incremented []string
	for _, layer := range rpmLayers {
		if layer.rpm <= 0 {
			continue
		}
		count, err := l.cache.IncrBy(ctx, layer.key, 1, 61*time.Second)
		if err != nil {
			l.undo(ctx, incremented)
			return false, fmt.Errorf("ratelimit: rpm cache error: %w", err)
		}
		incremented = append(incremented, layer.key)
		if count > int64(layer.rpm) {
			l.undo(ctx, incremented)
			return false, nil
		}
	}

	// 2. Check TPM (read-only check)
	tpmLayers := []struct {
		key string
		tpm int
	}{
		{tpmKey("global", "", ts), l.global.TPM},
		{tpmKey("model", modelAlias, ts), l.modelTPM(modelAlias)},
		{tpmKey("key", keyID, ts), l.keyTPM(keyID)},
	}

	for _, layer := range tpmLayers {
		if layer.tpm <= 0 {
			continue
		}
		val, ok := l.cache.Get(ctx, layer.key)
		if !ok {
			continue
		}
		currentTPM, _ := strconv.ParseInt(string(val), 10, 64)
		if currentTPM >= int64(layer.tpm) {
			l.undo(ctx, incremented)
			return false, nil
		}
	}

	return true, nil
}

func (l *Limiter) undo(ctx context.Context, keys []string) {
	for _, k := range keys {
		_, _ = l.cache.IncrBy(ctx, k, -1, 61*time.Second)
	}
}

// RecordTokens adds actualTokens to the TPM sliding window for all three layers.
func (l *Limiter) RecordTokens(ctx context.Context, keyID, modelAlias string, actualTokens int) error {
	if actualTokens <= 0 {
		return nil
	}
	ts := windowTimestamp()
	delta := int64(actualTokens)
	ttl := 61 * time.Second

	keys := []string{
		tpmKey("global", "", ts),
		tpmKey("model", modelAlias, ts),
		tpmKey("key", keyID, ts),
	}
	for _, k := range keys {
		if _, err := l.cache.IncrBy(ctx, k, delta, ttl); err != nil {
			return fmt.Errorf("ratelimit: record tokens: %w", err)
		}
	}
	return nil
}

// ─── helpers ──────────────────────────────────────────────────────────────────

// windowTimestamp returns a Unix timestamp truncated to the current minute,
// used as part of the cache key to implement a fixed 1-minute window.
func windowTimestamp() int64 {
	return time.Now().Unix() / 60
}

func rpmKey(scope, id string, ts int64) string {
	if id == "" {
		return fmt.Sprintf("rl:rpm:%s:%d", scope, ts)
	}
	return fmt.Sprintf("rl:rpm:%s:%s:%d", scope, id, ts)
}

func tpmKey(scope, id string, ts int64) string {
	if id == "" {
		return fmt.Sprintf("rl:tpm:%s:%d", scope, ts)
	}
	return fmt.Sprintf("rl:tpm:%s:%s:%d", scope, id, ts)
}

func (l *Limiter) modelRPM(alias string) int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if cfg, ok := l.modelLimits[alias]; ok {
		return cfg.RPM
	}
	return 0
}

func (l *Limiter) modelTPM(alias string) int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if cfg, ok := l.modelLimits[alias]; ok {
		return cfg.TPM
	}
	return 0
}

func (l *Limiter) keyRPM(keyID string) int {
	if l.keyLimits == nil {
		return 0
	}
	cfg := l.keyLimits(keyID)
	if cfg == nil {
		return 0
	}
	return cfg.RPM
}

func (l *Limiter) keyTPM(keyID string) int {
	if l.keyLimits == nil {
		return 0
	}
	cfg := l.keyLimits(keyID)
	if cfg == nil {
		return 0
	}
	return cfg.TPM
}
